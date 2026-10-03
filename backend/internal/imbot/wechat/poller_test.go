package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func textMessage(from, text string) map[string]any {
	return map[string]any{
		"from_user_id": from,
		"message_type": MessageTypeUser,
		"item_list":    []map[string]any{{"type": ItemTypeText, "text_item": map[string]any{"text": text}}},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestPollerDeliversMessagesInOrderAndPersistsCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(t, w, map[string]any{
				"ret":             0,
				"get_updates_buf": "cursor-2",
				"msgs":            []map[string]any{textMessage("u1@im.wechat", "first"), textMessage("u1@im.wechat", "second")},
			})
			return
		}
		cancel()
		writeJSON(t, w, map[string]any{"ret": 0})
	})

	cursor := &MemoryCursor{}
	var got []string
	p := &Poller{
		Client: client,
		Cursor: cursor,
		Handle: func(_ context.Context, m *Message) { got = append(got, m.PlainText()) },
	}

	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("delivered %v, want [first second] in order", got)
	}
	saved, _ := cursor.Load()
	if saved != "cursor-2" {
		t.Errorf("saved cursor = %q, want cursor-2", saved)
	}
}

func TestPollerReplaysSavedCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sent []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req GetUpdatesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		sent = append(sent, req.GetUpdatesBuf)
		if len(sent) == 1 {
			writeJSON(t, w, map[string]any{"ret": 0, "get_updates_buf": "cursor-next"})
			return
		}
		cancel()
		writeJSON(t, w, map[string]any{"ret": 0})
	})

	cursor := &MemoryCursor{}
	if err := cursor.Save("cursor-saved"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	p := &Poller{Client: client, Cursor: cursor}
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sent) < 2 {
		t.Fatalf("only %d polls made, want at least 2", len(sent))
	}
	if sent[0] != "cursor-saved" {
		t.Errorf("first poll sent %q, want the persisted cursor", sent[0])
	}
	if sent[1] != "cursor-next" {
		t.Errorf("second poll sent %q, want the cursor from the first response", sent[1])
	}
}

// An empty get_updates_buf means "keep the cursor you have", not "reset".
func TestPollerKeepsCursorWhenResponseOmitsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sent []string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req GetUpdatesRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		sent = append(sent, req.GetUpdatesBuf)
		if len(sent) >= 2 {
			cancel()
		}
		writeJSON(t, w, map[string]any{"ret": 0})
	})

	cursor := &MemoryCursor{}
	_ = cursor.Save("keep-me")
	p := &Poller{Client: client, Cursor: cursor}
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sent[1] != "keep-me" {
		t.Errorf("second poll sent %q, want the cursor to survive an empty response", sent[1])
	}
}

func TestPollerStopsOnSessionExpiry(t *testing.T) {
	polls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		polls++
		writeJSON(t, w, map[string]any{"ret": 0, "errcode": ErrCodeSessionExpired, "errmsg": "session timeout"})
	})

	p := &Poller{Client: client, Cursor: &MemoryCursor{}}
	err := p.Run(context.Background())
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Run err = %v, want ErrSessionExpired", err)
	}
	// Retrying cannot fix an expired token, so it must not be retried at all.
	if polls != 1 {
		t.Errorf("polled %d times, want exactly 1", polls)
	}
}

func TestPollerStopsOnHTTPPermissionFailure(t *testing.T) {
	polls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		polls++
		w.WriteHeader(http.StatusForbidden)
	})

	p := &Poller{Client: client, Cursor: &MemoryCursor{}}
	p.sleep = func(context.Context, time.Duration) error {
		t.Fatal("permission failure must not enter retry backoff")
		return nil
	}
	err := p.Run(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Permanent() {
		t.Fatalf("Run err = %v, want permanent APIError", err)
	}
	if polls != 1 {
		t.Errorf("polled %d times, want exactly 1", polls)
	}
}

func TestPollerStopsOnGatewayPermissionFailure(t *testing.T) {
	polls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		polls++
		writeJSON(t, w, map[string]any{"ret": 403, "errcode": 403, "errmsg": "permission denied"})
	})

	p := &Poller{Client: client, Cursor: &MemoryCursor{}}
	p.sleep = func(context.Context, time.Duration) error {
		t.Fatal("permission failure must not enter retry backoff")
		return nil
	}
	err := p.Run(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Permanent() {
		t.Fatalf("Run err = %v, want permanent APIError", err)
	}
	if polls != 1 {
		t.Errorf("polled %d times, want exactly 1", polls)
	}
}

func TestPollerEscalatesToBackoffAfterRepeatedFailures(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var waits []time.Duration
	p := &Poller{
		Client:                 client,
		Cursor:                 &MemoryCursor{},
		RetryDelay:             time.Second,
		BackoffDelay:           time.Minute,
		MaxConsecutiveFailures: 3,
	}
	p.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 4 {
			cancel()
			return context.Canceled
		}
		return nil
	}

	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []time.Duration{time.Second, time.Second, time.Minute, time.Minute}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait[%d] = %s, want %s", i, waits[i], want[i])
		}
	}
}

// A gateway that answers 200 with a non-zero ret is still a failure and must
// feed the same backoff path as a transport error.
func TestPollerBacksOffOnGatewayRejection(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"ret": 500, "errmsg": "internal"})
	})

	slept := 0
	p := &Poller{Client: client, Cursor: &MemoryCursor{}}
	p.sleep = func(context.Context, time.Duration) error {
		slept++
		if slept == 2 {
			return context.Canceled
		}
		return nil
	}
	if err := p.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if slept != 2 {
		t.Errorf("slept %d times, want 2", slept)
	}
}

// A client-side long-poll deadline is how an idle connection ends. It must be
// retried immediately rather than counted as a failure.
func TestPollerTreatsLongPollTimeoutAsIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Atomic because the abandoned first handler is still parked when the retry
	// arrives: two handler goroutines and the assertion below all touch this.
	var polls atomic.Int64
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) == 1 {
			// Outlast the client's long-poll deadline. Waiting on the request
			// context instead would leave the handler parked: Go's server does
			// not always observe a client-side abort on an idle connection.
			select {
			case <-r.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
			return
		}
		cancel()
		writeJSON(t, w, map[string]any{"ret": 0})
	})

	p := &Poller{Client: client, Cursor: &MemoryCursor{}, LongPollTimeout: 20 * time.Millisecond}
	p.sleep = func(context.Context, time.Duration) error {
		t.Error("an idle long poll must not trigger backoff")
		return context.Canceled
	}
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := polls.Load(); got < 2 {
		t.Errorf("polled %d times, want the loop to retry after the timeout", got)
	}
}

func TestPollerSurvivesHandlerPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(t, w, map[string]any{
				"ret":  0,
				"msgs": []map[string]any{textMessage("u1@im.wechat", "boom"), textMessage("u1@im.wechat", "survivor")},
			})
			return
		}
		cancel()
		writeJSON(t, w, map[string]any{"ret": 0})
	})

	var seen []string
	p := &Poller{
		Client: client,
		Cursor: &MemoryCursor{},
		Logf:   func(string, ...any) {},
		Handle: func(_ context.Context, m *Message) {
			seen = append(seen, m.PlainText())
			if m.PlainText() == "boom" {
				panic("handler exploded")
			}
		},
	}
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 || seen[1] != "survivor" {
		t.Errorf("delivered %v, want the message after the panic to still arrive", seen)
	}
}
