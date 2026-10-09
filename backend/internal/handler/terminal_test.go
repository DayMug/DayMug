package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

type testRunner struct {
	events           []agent.StreamEvent
	err              error
	delay            time.Duration
	existingSessions map[string]bool
	// Per-sessionID JSONL path override for SessionLogPath. Session-continuity
	// tests point one or more session ids at a real tempfile without touching
	// the host's ~/.claude tree. Empty map keeps unrelated tests out of the
	// on-disk session path.
	sessionLogPaths map[string]string
	// appendOnRun is appended to sessionLogPaths[opts.SessionID] at the
	// start of every RunWithSession invocation. Mimics the real claude
	// CLI's behaviour of writing each turn's user/tool/assistant lines
	// into its session log so session-continuity tests can assert exactly what
	// a stopped or completed turn leaves behind for the next resume.
	appendOnRun []byte
	// startedCh, when non-nil, is closed after the synchronous append (if
	// any) finishes — giving tests a deterministic signal that the runner
	// goroutine has actually been scheduled. Removes a real-clock race
	// where the test could send cancel before Go scheduled the goroutine
	// that does the append, leaving "rollback worked" passing for the
	// wrong reason (there was nothing to roll back in the first place).
	startedCh chan struct{}
}

type capturedContinuityRun struct {
	prompt string
	opts   agent.RunRequest
}

// cancelContinuityRunner mimics a CLI that creates its session log on the
// first turn, blocks until Stop, then completes the next turn. SessionExists
// is backed by the real tempfile so the test exercises the production
// new-session -> resume decision instead of a hard-coded answer.
type cancelContinuityRunner struct {
	sessionID string
	logPath   string
	runs      chan capturedContinuityRun
	count     atomic.Int32
}

func (*cancelContinuityRunner) Name() string { return "cancel-continuity" }
func (*cancelContinuityRunner) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}
func (r *cancelContinuityRunner) RunWithSession(ctx context.Context, prompt, _ string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)

	line, err := json.Marshal(map[string]string{"role": "user", "content": prompt})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}

	r.runs <- capturedContinuityRun{prompt: prompt, opts: opts}
	if r.count.Add(1) == 1 {
		outputCh <- agent.StreamEvent{Kind: agent.KindSystemInit, Content: `{"session_id":"` + r.sessionID + `"}`}
		outputCh <- agent.StreamEvent{Kind: agent.KindDelta, Content: "partial"}
		<-ctx.Done()
		return ctx.Err()
	}
	outputCh <- agent.StreamEvent{Kind: agent.KindResult, Content: "resumed"}
	return nil
}
func (*cancelContinuityRunner) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (r *cancelContinuityRunner) SessionExists(_, sessionID, _ string) bool {
	if sessionID != r.sessionID {
		return false
	}
	_, err := os.Stat(r.logPath)
	return err == nil
}
func (r *cancelContinuityRunner) SessionLogPath(_, sessionID, _ string) string {
	if sessionID != r.sessionID {
		return ""
	}
	return r.logPath
}

func (r *testRunner) RunWithSession(ctx context.Context, _, _ string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	if len(r.appendOnRun) > 0 && r.sessionLogPaths != nil {
		if path := r.sessionLogPaths[opts.SessionID]; path != "" {
			// Best-effort append; a failure here only matters for tests
			// that read the file afterward and they'll fail loudly anyway.
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = f.Write(r.appendOnRun)
				_ = f.Close()
			}
		}
	}
	if r.startedCh != nil {
		// Idempotent: a second invocation (rare in tests) would double-
		// close, so guard with a select.
		select {
		case <-r.startedCh:
		default:
			close(r.startedCh)
		}
	}
	return r.Run(ctx, "", "", outputCh)
}

func (r *testRunner) Run(ctx context.Context, _, _ string, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	for _, evt := range r.events {
		if r.delay > 0 {
			select {
			case <-time.After(r.delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case outputCh <- evt:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.err
}

func (r *testRunner) RunOneshot(_ context.Context, _, _ string, _ agent.RunRequest) (string, error) {
	return "", r.err
}

func (r *testRunner) SessionExists(_, sessionID, _ string) bool {
	return r.existingSessions[sessionID]
}

// SessionLogPath returns the per-test override when set, otherwise empty.
// Used by tests that exercise session continuity; default-empty keeps every
// other test (which doesn't care about the JSONL) from accidentally touching
// real on-disk files.
func (r *testRunner) SessionLogPath(_, sessionID, _ string) string {
	if r.sessionLogPaths == nil {
		return ""
	}
	return r.sessionLogPaths[sessionID]
}

// Name and Capabilities satisfy agent.Backend. Tests don't rely on
// either value; we report a non-empty Name() so log lines look real
// and a maximal Capabilities() so any capability-gated UI path stays
// active under tests (the legacy behaviour before backends carried
// explicit feature flags).
func (r *testRunner) Name() string { return "test-runner" }
func (r *testRunner) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsCompaction:      true,
		SupportsThinkingStream:  true,
		SupportsRateLimitEvents: true,
		ReportsCostUSD:          true,
	}
}

func readMsg(t *testing.T, ctx context.Context, conn *websocket.Conn) serverMessage {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg serverMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return msg
}

func messageContentString(t *testing.T, msg serverMessage) string {
	t.Helper()
	content, ok := msg.Content.(string)
	if !ok {
		t.Fatalf("message content type = %T, want string", msg.Content)
	}
	return content
}

func sendMsg(t *testing.T, ctx context.Context, conn *websocket.Conn, msg clientMessage) {
	t.Helper()
	data, _ := json.Marshal(msg)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readSkipAck reads the next server frame, transparently dropping any
// `input_ack` and `prompt_started` frames it encounters along the way.
// These are book-keeping events the staging-area UI keys off — most flow
// tests don't care, so they get hidden by default. A test that explicitly
// wants to assert on either should read the frame directly via readMsg.
func readSkipAck(t *testing.T, ctx context.Context, conn *websocket.Conn) serverMessage {
	t.Helper()
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "input_ack" || msg.Type == "prompt_started" {
			continue
		}
		// init_status is the post-init authoritative snapshot. Tests that
		// don't explicitly assert it want to walk past to the next
		// substantive event (thinking / delta / result / etc.).
		if msg.Type == "init_status" {
			continue
		}
		return msg
	}
}

// terminalTestHandler builds a TerminalHandler with an in-memory store
// pre-seeded with the conversations the test needs. Replaces the legacy
// `newTestTerminalHandler(runner, nil)` pattern, which the post-dispatcher
// pipeline can no longer satisfy: every prompt has to land in a real
// store row before a worker can claim it.
//
// Conv ids passed in are all seeded under the same user id "u1" with a
// shared work_dir of /tmp so the dispatcher's downstream lookups
// (conv → user → account) all resolve.
func terminalTestHandler(runner agent.Backend, convIDs ...string) (*TerminalHandler, *storetest.Fake) {
	ms := seededTerminalTestStore(convIDs...)
	return newTestTerminalHandler(runner, ms), ms
}

// seededTerminalTestStore is terminalTestHandler's store half, split out so a
// test that needs to decorate the store can build the handler itself.
func seededTerminalTestStore(convIDs ...string) *storetest.Fake {
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: "/tmp"})
	for _, id := range convIDs {
		_ = ms.CreateConversation(context.Background(), id, "", "u1", "/tmp", "", "")
	}
	return ms
}

// terminalTestServer creates an httptest.Server that routes directly to handleWebSocket.
func terminalTestServer(h *TerminalHandler) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handleWebSocket(w, r)
	}))
}

// authedTerminalTestServer is like terminalTestServer but injects authedUID into
// the request context, mirroring what HandleTerminal does in production after
// the auth middleware has run. Used to exercise the init-msg user-id checks
// without standing up a full Gin router and session store.
func authedTerminalTestServer(h *TerminalHandler, authedUID string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), authedUserCtxKey{}, authedUID)
		h.handleWebSocket(w, r.WithContext(ctx))
	}))
}

func TestTerminalHandler_InputOutputFlow(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: "delta", Content: "Hello"},
		{Kind: "delta", Content: " World"},
		{Kind: "result", Content: "Hello World"},
	}}
	h, _ := terminalTestHandler(runner, "conv-test")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	msg := readMsg(t, ctx, conn)
	if msg.Type != "status" || msg.Status != "ready" {
		t.Fatalf("expected status:ready, got %+v", msg)
	}

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-test"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})

	msg = readSkipAck(t, ctx, conn)
	if msg.Type != "status" || msg.Status != "thinking" {
		t.Fatalf("expected status:thinking, got %+v", msg)
	}

	msg = readMsg(t, ctx, conn)
	if msg.Type != "delta" || msg.Content != "Hello" {
		t.Fatalf("expected delta:Hello, got %+v", msg)
	}

	msg = readMsg(t, ctx, conn)
	if msg.Type != "delta" || msg.Content != " World" {
		t.Fatalf("expected delta:' World', got %+v", msg)
	}

	msg = readMsg(t, ctx, conn)
	if msg.Type != "result" || msg.Content != "Hello World" {
		t.Fatalf("expected result:'Hello World', got %+v", msg)
	}

	msg = readMsg(t, ctx, conn)
	if msg.Type != "status" || msg.Status != "ready" {
		t.Fatalf("expected status:ready, got %+v", msg)
	}
}

func TestTerminalHandler_ReusesWebSocketAcrossConversationSubscriptions(t *testing.T) {
	h, _ := terminalTestHandler(&testRunner{}, "conv-a", "conv-b")
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // initial status:ready
	sendMsg(t, ctx, conn, clientMessage{
		Type:           "init",
		ConversationID: "conv-a",
		UserID:         "u1",
		SubscriptionID: "sub-a",
	})
	first := readMsg(t, ctx, conn)
	if first.Type != "init_ok" || first.ConversationID != "conv-a" || first.SubscriptionID != "sub-a" {
		t.Fatalf("unexpected first init: %+v", first)
	}
	if status := readMsg(t, ctx, conn); status.Type != "init_status" {
		t.Fatalf("expected first init_status, got %+v", status)
	}

	sendMsg(t, ctx, conn, clientMessage{
		Type:           "init",
		ConversationID: "conv-b",
		UserID:         "u1",
		SubscriptionID: "sub-b",
	})
	second := readMsg(t, ctx, conn)
	if second.Type != "init_ok" || second.ConversationID != "conv-b" || second.SubscriptionID != "sub-b" {
		t.Fatalf("unexpected second init on reused websocket: %+v", second)
	}
}

func TestTerminalHandler_RejectsInputFromStaleSubscription(t *testing.T) {
	h, ms := terminalTestHandler(&testRunner{}, "conv-a", "conv-b")
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // initial status:ready
	sendMsg(t, ctx, conn, clientMessage{
		Type:           "init",
		ConversationID: "conv-a",
		UserID:         "u1",
		SubscriptionID: "sub-a",
	})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status

	sendMsg(t, ctx, conn, clientMessage{
		Type:           "init",
		ConversationID: "conv-b",
		UserID:         "u1",
		SubscriptionID: "sub-b",
	})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status

	sendMsg(t, ctx, conn, clientMessage{
		Type:           "input",
		Content:        "must not enqueue",
		ConversationID: "conv-a",
		SubscriptionID: "sub-a",
	})
	rejected := readMsg(t, ctx, conn)
	if rejected.Type != "error" ||
		rejected.Message != "stale conversation subscription" ||
		rejected.ConversationID != "conv-a" ||
		rejected.SubscriptionID != "sub-a" {
		t.Fatalf("unexpected stale-subscription response: %+v", rejected)
	}
	if got := ms.Messages["conv-a"]; len(got) != 0 {
		t.Fatalf("stale input was enqueued into conv-a: %+v", got)
	}
	if got := ms.Messages["conv-b"]; len(got) != 0 {
		t.Fatalf("stale input was enqueued into conv-b: %+v", got)
	}
}

func TestTerminalHandler_InitResetsCachedAgentStateOnSwitch(t *testing.T) {
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", WorkDir: "/agent-one"})
	_ = ms.CreateUser(context.Background(), store.User{ID: "u2", WorkDir: "/agent-two"})
	_ = ms.CreateConversation(context.Background(), "conv-1", "", "u1", "", "", "")
	_ = ms.CreateConversation(context.Background(), "conv-2", "", "u2", "", "", "")
	h := newTestTerminalHandler(&testRunner{}, ms)

	state := connState{}
	sendCh := make(chan []byte, 16)
	h.handleInitMsg(
		context.Background(),
		clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1", SubscriptionID: "sub-1"},
		&state,
		"browser",
		sendCh,
		func(serverMessage) {},
	)
	if state.currentUser == nil || state.currentUser.ID != "u1" || state.userWorkDir != "/agent-one" {
		t.Fatalf("unexpected first agent state: %+v workdir=%q", state.currentUser, state.userWorkDir)
	}

	h.handleInitMsg(
		context.Background(),
		clientMessage{Type: "init", ConversationID: "conv-2", UserID: "u2", SubscriptionID: "sub-2"},
		&state,
		"browser",
		sendCh,
		func(serverMessage) {},
	)
	if state.currentUser == nil || state.currentUser.ID != "u2" || state.userWorkDir != "/agent-two" {
		t.Fatalf("stale agent state after switch: %+v workdir=%q", state.currentUser, state.userWorkDir)
	}
}

func TestTerminalHandler_InitReplaysPersistedContextUsage(t *testing.T) {
	// A conversation that already saw a token-usage event in some past run
	// has its payload persisted on the conversations row. A fresh client
	// reconnecting after a server restart (or with cleared localStorage)
	// must receive a context_usage frame as part of init so the bar shows
	// up immediately, without waiting for the next claude reply.
	payload := `{"used":12345,"total":200000,"input_tokens":1000,"cache_read":11000,"cache_creation":345}`
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID:               "conv-replay",
		UserID:           "u1",
		LastContextUsage: payload,
	}}

	runner := &testRunner{}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-replay"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}

	// The post-init authoritative snapshot fires before the persisted
	// context_usage replay. Walk past it to assert the replay still
	// runs and carries the persisted payload.
	if msg := readMsg(t, ctx, conn); msg.Type != "init_status" {
		t.Fatalf("expected init_status snapshot, got %+v", msg)
	}
	msg := readMsg(t, ctx, conn)
	if msg.Type != "context_usage" {
		t.Fatalf("expected context_usage replay, got %+v", msg)
	}
	if msg.Content != payload {
		t.Errorf("payload mismatch:\nwant %q\ngot  %q", payload, msg.Content)
	}
}

func TestTerminalHandler_InitSkipsReplayWhenBroadcasterAlreadyHasFresh(t *testing.T) {
	// In-memory broadcaster cache is the authoritative live source — DB
	// replay must NOT clobber it. Simulates: tab A is mid-conversation,
	// pushed a fresh context_usage; tab B opens the same conversation.
	// Tab B should receive the broadcaster's cached frame via Join, not
	// the (potentially stale) DB row.
	stalePayload := `{"used":1,"total":200000}`
	freshPayload := `{"used":99999,"total":200000}`
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID:               "conv-fresh",
		UserID:           "u1",
		LastContextUsage: stalePayload,
	}}

	runner := &testRunner{}
	h := newTestTerminalHandler(runner, ms)
	// Pre-seed the broadcaster as if a live event just landed.
	freshFrame, _ := json.Marshal(serverMessage{Type: "context_usage", Content: freshPayload})
	h.Broadcaster.SetLastContextUsage("conv-fresh", freshFrame)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-fresh"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}

	// Tab B's first context_usage frame must be the broadcaster's fresh
	// cache (delivered via Join's replay path), not the stale DB row.
	msg := readMsg(t, ctx, conn)
	if msg.Type != "context_usage" {
		t.Fatalf("expected context_usage frame, got %+v", msg)
	}
	if msg.Content != freshPayload {
		t.Errorf("expected fresh broadcaster cache to win, got %q", msg.Content)
	}
}

func TestTerminalHandler_InitBackfillsMissedMessages(t *testing.T) {
	// Reconnecting tab supplies a last_message_id cursor; server must
	// reply with a history_backfill frame containing exactly the
	// messages persisted after that cursor. This is the core fix for
	// "I left the page mid-task and never saw the assistant reply".
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-bf", UserID: "u1"}}
	ms.Messages = map[string][]store.Message{
		"conv-bf": {
			{ID: "m1", ConversationID: "conv-bf", Role: "user", Content: "hi"},
			{ID: "m2", ConversationID: "conv-bf", Role: "thinking", Content: "..."},
			{ID: "m3", ConversationID: "conv-bf", Role: "assistant", Content: "hello back"},
		},
	}

	h := newTestTerminalHandler(&testRunner{}, ms)
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-bf", LastMessageID: "m1"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}

	msg := readMsg(t, ctx, conn)
	if msg.Type != "history_backfill" {
		t.Fatalf("expected history_backfill, got %+v", msg)
	}
	if msg.ConversationID != "conv-bf" {
		t.Errorf("conv mismatch: %q", msg.ConversationID)
	}
	if len(msg.Messages) != 2 {
		t.Fatalf("expected 2 backfilled messages, got %d", len(msg.Messages))
	}
	if msg.Messages[0].ID != "m2" || msg.Messages[1].ID != "m3" {
		t.Errorf("unexpected backfill order: %v", msg.Messages)
	}
	if msg.Messages[1].Role != "assistant" || msg.Messages[1].Content != "hello back" {
		t.Errorf("payload corrupted: %+v", msg.Messages[1])
	}
}

func TestTerminalHandler_InitEmptyCursorBackfillsFullConversation(t *testing.T) {
	// Empty cursor is the recovery path for a client that never anchored
	// one — e.g. a brand-new conversation whose first user message was
	// sent right before the phone went offline. Server must hand back
	// the entire conversation so the reconnecting client can resync.
	// The client's history_backfill handler dedups by id against any
	// REST-loaded copies, so an overlap on first connect is harmless.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-empty-cursor", UserID: "u1"}}
	ms.Messages = map[string][]store.Message{
		"conv-empty-cursor": {
			{ID: "m1", ConversationID: "conv-empty-cursor", Role: "user", Content: "hi"},
			{ID: "m2", ConversationID: "conv-empty-cursor", Role: "thinking", Content: "..."},
			{ID: "m3", ConversationID: "conv-empty-cursor", Role: "assistant", Content: "hello back"},
		},
	}

	h := newTestTerminalHandler(&testRunner{}, ms)
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-empty-cursor"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}

	msg := readMsg(t, ctx, conn)
	if msg.Type != "history_backfill" {
		t.Fatalf("expected history_backfill, got %+v", msg)
	}
	if len(msg.Messages) != 3 {
		t.Fatalf("expected 3 backfilled messages on empty cursor, got %d", len(msg.Messages))
	}
	if msg.Messages[0].ID != "m1" || msg.Messages[2].ID != "m3" {
		t.Errorf("wrong backfill order: %v", msg.Messages)
	}
}

func TestTerminalHandler_InitEmptyConversationProducesNoBackfill(t *testing.T) {
	// Conversation has zero persisted messages — there's nothing to
	// backfill, so the server must stay silent (no `{messages: []}`
	// noise on the wire) until a real event fires.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-empty", UserID: "u1"}}
	ms.Messages = map[string][]store.Message{}

	h := newTestTerminalHandler(&testRunner{}, ms)
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-empty"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}
	if msg := readMsg(t, ctx, conn); msg.Type != "init_status" || msg.Status != "ready" {
		t.Fatalf("expected init_status:ready snapshot, got %+v", msg)
	}

	readCtx, readCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer readCancel()
	if _, data, err := conn.Read(readCtx); err == nil {
		t.Fatalf("unexpected frame after init_status: %s", string(data))
	}
}

func TestTerminalHandler_InitBackfillEmptyWhenAtTail(t *testing.T) {
	// Cursor points at the most recent message: no missed messages,
	// no history_backfill frame. Avoids sending {messages: []} noise.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-tail", UserID: "u1"}}
	ms.Messages = map[string][]store.Message{
		"conv-tail": {{ID: "m1", ConversationID: "conv-tail", Role: "user", Content: "hi"}},
	}

	h := newTestTerminalHandler(&testRunner{}, ms)
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-tail", LastMessageID: "m1"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}
	if msg := readMsg(t, ctx, conn); msg.Type != "init_status" || msg.Status != "ready" {
		t.Fatalf("expected init_status:ready snapshot, got %+v", msg)
	}
	readCtx, readCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer readCancel()
	if _, data, err := conn.Read(readCtx); err == nil {
		t.Fatalf("expected no follow-up frames, got: %s", string(data))
	}
}

// TestTerminalHandler_InitStatusSnapshotHealsStaleThinking covers the
// healing path for the original bug report: a tab missed the
// `status: ready` event that should have cleared its in-progress
// thinking indicator (e.g. WS dropped during the run, server's
// EndJob cleared the replay buffer, the next re-init's join finds
// an empty room). The post-init `init_status` snapshot has to land
// regardless of busy state so the client can authoritatively
// overwrite stale isThinking.
func TestTerminalHandler_InitStatusSnapshotHealsStaleThinking(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-heal", UserID: "u1"}}

	h := newTestTerminalHandler(&testRunner{}, ms)
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready (per-WS open)
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-heal"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}
	msg := readMsg(t, ctx, conn)
	if msg.Type != "init_status" {
		t.Fatalf("expected init_status snapshot, got %+v", msg)
	}
	if msg.Status != "ready" {
		t.Fatalf("expected init_status:ready (idle room), got status=%q", msg.Status)
	}
	if msg.ConversationID != "conv-heal" {
		t.Errorf("conv mismatch: %q", msg.ConversationID)
	}
}

// TestTerminalHandler_InitStatusSnapshotReportsBusy verifies the same
// snapshot carries `thinking` while the room is mid-run, so a freshly
// reconnecting tab knows to keep its thinking indicator instead of
// flashing back to ready.
func TestTerminalHandler_InitStatusSnapshotReportsBusy(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-busy-snap", UserID: "u1"}}

	h := newTestTerminalHandler(&testRunner{}, ms)
	h.Broadcaster.StartJob("conv-busy-snap", func() {})
	defer h.Broadcaster.EndJob("conv-busy-snap")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready (per-WS open)
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-busy-snap"})

	if msg := readMsg(t, ctx, conn); msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}
	msg := readMsg(t, ctx, conn)
	if msg.Type != "init_status" {
		t.Fatalf("expected init_status snapshot, got %+v", msg)
	}
	if msg.Status != "thinking" {
		t.Fatalf("expected init_status:thinking (busy room), got status=%q", msg.Status)
	}
}

func TestTerminalHandler_InitIncludesConversationActivitySnapshot(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "owner-activity", Name: "Alice User", Username: "alice"},
		{ID: "agent-activity", Name: "Alice Agent", OwnerID: "owner-activity"},
		{ID: "owner-other", Name: "Bob User", Username: "bob"},
		{ID: "agent-other", Name: "Bob Agent", OwnerID: "owner-other"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "conv-activity", UserID: "agent-activity", Model: "claude-sonnet-4-5"},
		{ID: "conv-activity-2", UserID: "agent-activity", Model: "gpt-5.2-codex"},
		{ID: "conv-other", UserID: "agent-other", Model: "claude-opus-4-1"},
	}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.Drainer = service.NewDrainer()
	firstStartedAt := time.Date(2026, time.July, 22, 9, 30, 0, 0, time.UTC)
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{
		UserID:         "owner-activity",
		ConversationID: "conv-activity",
		Status:         service.JobStatusRunning,
		StartedAt:      firstStartedAt,
	})
	defer done()
	secondStartedAt := firstStartedAt.Add(time.Minute)
	doneSecond := h.Drainer.JobStartWithInfo(service.DrainerJob{
		UserID:         "owner-activity",
		ConversationID: "conv-activity-2",
		Status:         service.JobStatusRunning,
		StartedAt:      secondStartedAt,
	})
	defer doneSecond()
	thirdStartedAt := firstStartedAt.Add(2 * time.Minute)
	doneOther := h.Drainer.JobStartWithInfo(service.DrainerJob{
		UserID:         "owner-other",
		ConversationID: "conv-other",
		Status:         service.JobStatusRunning,
		StartedAt:      thirdStartedAt,
	})
	defer doneOther()

	srv := authedTerminalTestServer(h, "owner-activity")
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{
		Type: "init", UserID: "agent-activity", ConversationID: "conv-activity",
	})
	msg := readMsg(t, ctx, conn)
	if msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}
	if !reflect.DeepEqual(msg.RunningAgentIDs, []string{"agent-activity", "agent-other"}) ||
		!reflect.DeepEqual(msg.RunningConversationIDs, []string{"conv-activity", "conv-activity-2", "conv-other"}) {
		t.Fatalf("activity snapshot = %+v", msg)
	}
	wantDetails := []service.RunningConversationActivity{
		{
			ConversationID: "conv-activity",
			AgentID:        "agent-activity",
			AgentName:      "Alice Agent",
			OwnerName:      "Alice User",
			OwnerUsername:  "alice",
			Model:          "claude-sonnet-4-5",
			StartedAt:      firstStartedAt,
		},
		{
			ConversationID: "conv-activity-2",
			AgentID:        "agent-activity",
			AgentName:      "Alice Agent",
			OwnerName:      "Alice User",
			OwnerUsername:  "alice",
			Model:          "gpt-5.2-codex",
			StartedAt:      secondStartedAt,
		},
		{
			ConversationID: "conv-other",
			AgentID:        "agent-other",
			AgentName:      "Bob Agent",
			OwnerName:      "Bob User",
			OwnerUsername:  "bob",
			Model:          "claude-opus-4-1",
			StartedAt:      thirdStartedAt,
		},
	}
	if !reflect.DeepEqual(msg.RunningConversations, wantDetails) {
		t.Fatalf("activity details = %+v, want %+v", msg.RunningConversations, wantDetails)
	}
}

// TestTerminalHandler_QueuesSecondInputBehindFirst replaces the old
// "already processing" rejection test. Under the dispatcher, a second
// input that lands while the first is still streaming is queued and
// processed FIFO once the first finishes — no error is returned.
// Both prompts get their own input_ack and their own thinking/result
// cycle, in send order.
func TestTerminalHandler_QueuesSecondInputBehindFirst(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: agent.KindResult, Content: "reply"},
		},
		delay: 50 * time.Millisecond,
	}
	ms := seededTerminalTestStore("conv-busy")
	sweeps := make(chan string, promptSweepSignalBuffer)
	h := newTestTerminalHandler(runner, &sweepSignallingStore{Fake: ms, swept: sweeps})

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-busy"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "first"})
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "second"})

	// Drain frames until we've seen both runs go through (two
	// thinking → result → ready cycles). No "already processing"
	// error should appear.
	readyCount := 0
	deadline := time.Now().Add(3 * time.Second)
	for readyCount < 2 && time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "error" {
			t.Fatalf("unexpected error frame: %+v", msg)
		}
		if msg.Type == "status" && msg.Status == "ready" {
			readyCount++
		}
	}
	if readyCount < 2 {
		t.Fatalf("expected two ready frames (one per prompt), got %d", readyCount)
	}

	// `ready` says the stream is over, not that the row has been swept: the
	// processor broadcasts it from runAgentRequest's defer and only returns
	// afterwards, and clearing queue_status is the dispatcher worker's
	// follow-up MarkPromptDone. Snapshotting on `ready` alone asserts an
	// ordering the dispatcher has never promised — that is the flake. Wait for
	// the sweep of both prompts instead.
	for i := 0; i < 2; i++ {
		select {
		case <-sweeps:
		case <-ctx.Done():
			t.Fatalf("dispatcher swept only %d of 2 prompts before the deadline", i)
		}
	}

	// Both prompts must have been persisted as user messages, in send
	// order, with queue_status cleared once processing completed. Read
	// through snapshotMessages so we hold the queue lock — without it
	// the dispatcher's MarkPromptDone races with this read on -race.
	got := ms.SnapshotMessages("conv-busy")
	var users []store.Message
	for _, m := range got {
		if m.Role == "user" {
			users = append(users, m)
		}
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 user messages persisted, got %d (%+v)", len(users), got)
	}
	if users[0].Content != "first" || users[1].Content != "second" {
		t.Errorf("FIFO order broken: got %q then %q", users[0].Content, users[1].Content)
	}
	for _, u := range users {
		if u.QueueStatus != "" {
			t.Errorf("queue_status should be cleared once processed, got %q on %q", u.QueueStatus, u.Content)
		}
	}
}

type steeringCall struct {
	controlID string
	messageID string
	input     string
}

type steeringTestRunner struct {
	testRunner
	started  chan struct{}
	finish   chan struct{}
	steers   chan steeringCall
	steerErr error
	once     sync.Once
	calls    atomic.Int32
}

func (r *steeringTestRunner) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.finish:
		outputCh <- agent.StreamEvent{Kind: agent.KindResult, Content: "reply"}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *steeringTestRunner) SteerTurn(_ context.Context, controlID, messageID, input string) error {
	r.steers <- steeringCall{controlID: controlID, messageID: messageID, input: input}
	return r.steerErr
}

func TestTerminalHandler_DefaultInputQueuesBehindActiveTurn(t *testing.T) {
	runner := &steeringTestRunner{
		started: make(chan struct{}),
		finish:  make(chan struct{}),
		steers:  make(chan steeringCall, 1),
	}
	h, ms := terminalTestHandler(runner, "conv-queue-default")
	state := &connState{conversationID: "conv-queue-default"}
	writeJSON := func(serverMessage) {}
	broadcastExceptSelf := func(string, serverMessage) {}

	h.handleInputMsg(
		context.Background(),
		clientMessage{Type: "input", Content: "first"},
		state,
		writeJSON,
		broadcastExceptSelf,
	)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first run did not start")
	}

	// A capable backend is already running, but an ordinary input must not use
	// its steering hook. It remains pending until the first turn completes.
	h.handleInputMsg(
		context.Background(),
		clientMessage{Type: "input", Content: "queue after first"},
		state,
		writeJSON,
		broadcastExceptSelf,
	)
	select {
	case got := <-runner.steers:
		t.Fatalf("ordinary input unexpectedly steered: %+v", got)
	default:
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("RunWithSession calls before first completion = %d, want 1", got)
	}
	var queued *store.Message
	for _, msg := range ms.SnapshotMessages("conv-queue-default") {
		if msg.Role == "user" && msg.Content == "queue after first" {
			copy := msg
			queued = &copy
			break
		}
	}
	if queued == nil || queued.QueueStatus != "pending" {
		t.Fatalf("ordinary follow-up should remain pending, got %+v", queued)
	}

	close(runner.finish)
}

func TestTerminalHandler_DefaultInputQueuesBehindIndependentWakeup(t *testing.T) {
	runner := &steeringTestRunner{
		started: make(chan struct{}),
		finish:  make(chan struct{}),
		steers:  make(chan steeringCall, 1),
	}
	const convID = "conv-queue-wakeup"
	h, ms := terminalTestHandler(runner, convID)
	if !h.Broadcaster.StartJob(convID, func() {}) {
		t.Fatal("independent wakeup did not claim the room")
	}

	frames := make(chan serverMessage, 8)
	h.handleInputMsg(
		context.Background(),
		clientMessage{Type: "input", Content: "queue behind wakeup"},
		&connState{conversationID: convID},
		func(msg serverMessage) { frames <- msg },
		func(string, serverMessage) {},
	)

	var queued *store.Message
	for _, msg := range ms.SnapshotMessages(convID) {
		if msg.Role == "user" && msg.Content == "queue behind wakeup" {
			copy := msg
			queued = &copy
			break
		}
	}
	if queued == nil || queued.QueueStatus != "pending" {
		t.Fatalf("prompt should remain pending behind wakeup, got %+v", queued)
	}
	select {
	case <-runner.started:
		t.Fatal("prompt started while the independent wakeup owned the room")
	default:
	}
	for len(frames) > 0 {
		if msg := <-frames; msg.Type == "error" {
			t.Fatalf("prompt emitted an error instead of queuing: %+v", msg)
		}
	}

	h.Broadcaster.EndJob(convID)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("queued prompt did not start after wakeup released the room")
	}
	close(runner.finish)
}

func TestTerminalHandler_SteersSecondInputIntoActiveTurn(t *testing.T) {
	runner := &steeringTestRunner{
		started: make(chan struct{}),
		finish:  make(chan struct{}),
		steers:  make(chan steeringCall, 1),
	}
	h, ms := terminalTestHandler(runner, "conv-steer")
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-steer"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "first"})
	select {
	case <-runner.started:
	case <-ctx.Done():
		t.Fatal("first run did not start")
	}
	// Drain the first prompt's bookkeeping so the following ids belong to the
	// intervention message.
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "thinking" {
			break
		}
	}

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "focus on tests", Steer: true})
	var secondID string
	startedSecond := false
	for secondID == "" || !startedSecond {
		msg := readMsg(t, ctx, conn)
		switch msg.Type {
		case "input_ack":
			secondID = msg.MessageID
		case "prompt_started":
			if secondID != "" && msg.MessageID == secondID {
				startedSecond = true
			}
		case "error":
			t.Fatalf("unexpected error frame: %+v", msg)
		}
	}

	select {
	case got := <-runner.steers:
		if got.controlID != "conv-steer" || got.messageID != secondID || got.input != "focus on tests" {
			t.Fatalf("steer call = %+v, secondID=%q", got, secondID)
		}
	case <-ctx.Done():
		t.Fatal("second input was not steered")
	}
	close(runner.finish)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		messages := ms.SnapshotMessages("conv-steer")
		usersDone := 0
		for _, msg := range messages {
			if msg.Role == "user" && msg.QueueStatus == "" {
				usersDone++
			}
		}
		if usersDone == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("RunWithSession calls = %d, want one turn", got)
	}
	var users []store.Message
	for _, msg := range ms.SnapshotMessages("conv-steer") {
		if msg.Role == "user" {
			users = append(users, msg)
		}
	}
	if len(users) != 2 || users[0].Content != "first" || users[1].Content != "focus on tests" {
		t.Fatalf("user history = %+v", users)
	}
	for _, msg := range users {
		if msg.QueueStatus != "" {
			t.Fatalf("queue_status for %q = %q", msg.Content, msg.QueueStatus)
		}
	}
}

func TestTerminalHandler_ReportsNoActiveTurnAndKeepsPromptQueued(t *testing.T) {
	runner := &steeringTestRunner{
		started:  make(chan struct{}),
		finish:   make(chan struct{}),
		steers:   make(chan steeringCall, 1),
		steerErr: agent.ErrNoActiveTurn,
	}
	h, ms := terminalTestHandler(runner, "conv-stale-steer")
	state := &connState{conversationID: "conv-stale-steer"}
	frames := make(chan serverMessage, 8)

	h.handleInputMsg(
		context.Background(),
		clientMessage{Type: "input", Content: "first"},
		state,
		func(msg serverMessage) { frames <- msg },
		func(string, serverMessage) {},
	)
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first run did not start")
	}

	h.handleInputMsg(
		context.Background(),
		clientMessage{Type: "input", Content: "insert too late", Steer: true},
		state,
		func(msg serverMessage) { frames <- msg },
		func(string, serverMessage) {},
	)

	foundError := false
	for len(frames) > 0 {
		msg := <-frames
		if msg.Type == "error" && msg.Message == agent.ErrNoActiveTurn.Error() {
			foundError = true
		}
	}
	if !foundError {
		t.Fatal("stale Insert did not return a no active turn error")
	}

	var queued *store.Message
	for _, msg := range ms.SnapshotMessages("conv-stale-steer") {
		if msg.Role == "user" && msg.Content == "insert too late" {
			copy := msg
			queued = &copy
			break
		}
	}
	if queued == nil || queued.QueueStatus != "pending" {
		t.Fatalf("rejected Insert should remain durable and pending, got %+v", queued)
	}
	close(runner.finish)
}

// promptSweepSignalBuffer keeps sweepSignallingStore's notifications from ever
// blocking a dispatcher worker, including sweeps a test does not consume.
const promptSweepSignalBuffer = 8

// sweepSignallingStore reports each dispatcher post-processing sweep. That
// sweep is the only thing that clears queue_status, and it runs after the
// processor returns — i.e. strictly later than the processor's `ready`
// broadcast. Signalling from the store call gives tests the real event to
// synchronise on rather than an assumption about frame ordering.
type sweepSignallingStore struct {
	*storetest.Fake
	swept chan string
}

func (s *sweepSignallingStore) MarkPromptDone(ctx context.Context, messageID string) error {
	err := s.Fake.MarkPromptDone(ctx, messageID)
	select {
	case s.swept <- messageID:
	default:
	}
	return err
}

type capturingRunner struct {
	events           []agent.StreamEvent
	mu               sync.Mutex
	gotOpts          []agent.RunRequest
	gotWorkDir       []string
	existingSessions map[string]bool
}

func (r *capturingRunner) RunWithSession(_ context.Context, _, workDir string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	r.mu.Lock()
	r.gotOpts = append(r.gotOpts, opts)
	r.gotWorkDir = append(r.gotWorkDir, workDir)
	r.mu.Unlock()
	for _, evt := range r.events {
		outputCh <- evt
	}
	return nil
}

func (r *capturingRunner) Run(ctx context.Context, prompt, workDir string, outputCh chan<- agent.StreamEvent) error {
	return r.RunWithSession(ctx, prompt, workDir, agent.RunRequest{}, outputCh)
}

func (r *capturingRunner) RunOneshot(_ context.Context, _, _ string, _ agent.RunRequest) (string, error) {
	return "", nil
}

func (r *capturingRunner) SessionExists(_, sessionID, _ string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.existingSessions[sessionID]
}

// SessionLogPath is unused by the capturingRunner tests; returning "" keeps
// their unrelated behavior independent of real on-disk session files.
func (r *capturingRunner) SessionLogPath(_, _, _ string) string { return "" }
func (r *capturingRunner) Name() string                         { return "capturing" }
func (r *capturingRunner) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: true, SupportsThinkingStream: true, SupportsRateLimitEvents: true, ReportsCostUSD: true}
}

func TestTerminalHandler_LocksConversationWorkDirOnFirstMessage(t *testing.T) {
	// Conversation has empty work_dir (created when user.WorkDir was empty), but
	// user.WorkDir has since been set. The first message must persist the resolved
	// workdir to conv.WorkDir so a later WS reconnect can't pick a different cwd
	// and break --resume.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-1", UserID: "u1", WorkDir: "", SessionID: "sess-1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/home/alice/project"}}

	runner := &capturingRunner{events: []agent.StreamEvent{
		{Kind: "result", Content: "ok"},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	runner.mu.Lock()
	gotWorkDir := runner.gotWorkDir[0]
	runner.mu.Unlock()
	if gotWorkDir != "/home/alice/project" {
		t.Errorf("expected runner workDir '/home/alice/project', got %q", gotWorkDir)
	}

	conv, err := ms.GetConversation(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if conv.WorkDir != "/home/alice/project" {
		t.Errorf("expected conv.WorkDir locked to '/home/alice/project', got %q", conv.WorkDir)
	}
}

func TestTerminalHandler_PreservesLockedWorkDirAcrossUserChange(t *testing.T) {
	// Once conv.WorkDir is locked, even if user.WorkDir changes mid-session,
	// subsequent messages must keep using the locked conv workdir so --resume
	// hits the same per-cwd Claude session file.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-1", UserID: "u1", WorkDir: "/locked/dir", SessionID: "sess-1"},
	}
	ms.Messages["conv-1"] = []store.Message{
		{ID: "m1", ConversationID: "conv-1", Role: "user", Content: "hi"},
		{ID: "m2", ConversationID: "conv-1", Role: "assistant", Content: "hello"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/different/dir"}}

	// SessionExists=true mimics the on-disk jsonl Claude wrote on the prior run.
	runner := &capturingRunner{
		events:           []agent.StreamEvent{{Kind: "result", Content: "ok"}},
		existingSessions: map[string]bool{"sess-1": true},
	}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "second message"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	runner.mu.Lock()
	gotWorkDir := runner.gotWorkDir[0]
	gotResume := runner.gotOpts[0].IsResume
	runner.mu.Unlock()
	if gotWorkDir != "/locked/dir" {
		t.Errorf("expected workDir '/locked/dir' (locked), got %q", gotWorkDir)
	}
	if !gotResume {
		t.Error("expected IsResume=true since assistant message exists")
	}
}

func TestTerminalHandler_UsesClaudeSessionID(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-1", UserID: "u1", SessionID: "claude-sess-abc"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	runner := &capturingRunner{events: []agent.StreamEvent{
		{Kind: "result", Content: "ok"},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})

	// Drain messages until ready
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.gotOpts) != 1 {
		t.Fatalf("expected 1 run call, got %d", len(runner.gotOpts))
	}
	if runner.gotOpts[0].SessionID != "claude-sess-abc" {
		t.Errorf("expected session ID 'claude-sess-abc', got '%s'", runner.gotOpts[0].SessionID)
	}
}

func TestTerminalHandler_ClearMessagesResetsResume(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-1", UserID: "u1", SessionID: "sess-old"},
	}
	// Pre-populate 2 messages so messageCount starts at 2 on init.
	ms.Messages["conv-1"] = []store.Message{
		{ID: "m1", ConversationID: "conv-1", Role: "user", Content: "hi"},
		{ID: "m2", ConversationID: "conv-1", Role: "assistant", Content: "hello"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	runner := &capturingRunner{events: []agent.StreamEvent{
		{Kind: "result", Content: "ok"},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	// Simulate ClearMessages: delete messages and rotate session_id.
	_ = ms.ClearMessages(context.Background(), "conv-1")

	// Send a new message — this should use --session-id (IsResume=false), not --resume.
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "after clear"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.gotOpts) != 1 {
		t.Fatalf("expected 1 run call, got %d", len(runner.gotOpts))
	}
	if runner.gotOpts[0].SessionID == "sess-old" {
		t.Error("expected rotated session ID, still got old 'sess-old'")
	}
	if runner.gotOpts[0].IsResume {
		t.Error("expected IsResume=false after ClearMessages, got true")
	}
}

func TestTerminalHandler_NoJsonlDoesNotTriggerResume(t *testing.T) {
	// First-attempt failure where Claude CLI never managed to create the
	// per-cwd jsonl (e.g., spawn error). Even with leftover error rows in DB,
	// the next send must use --session-id, not --resume.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-1", UserID: "u1", SessionID: "sess-1"},
	}
	ms.Messages["conv-1"] = []store.Message{
		{ID: "m1", ConversationID: "conv-1", Role: "user", Content: "hello"},
		{ID: "m2", ConversationID: "conv-1", Role: "error", Content: "exit status 1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	// existingSessions is nil → SessionExists returns false for every sid.
	runner := &capturingRunner{events: []agent.StreamEvent{
		{Kind: "result", Content: "ok"},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "retry"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.gotOpts) != 1 {
		t.Fatalf("expected 1 run call, got %d", len(runner.gotOpts))
	}
	if runner.gotOpts[0].IsResume {
		t.Error("expected IsResume=false when no on-disk jsonl exists, got true")
	}
}

func TestTerminalHandler_JsonlPresentTriggersResumeDespiteNoAssistant(t *testing.T) {
	// Regression for the "Session ID ... is already in use" bug: the first
	// run streamed a reply that Claude wrote to its jsonl, but the service
	// crashed before persisting any assistant row. Subsequent sends must
	// detect the on-disk jsonl and switch to --resume — the old "DB has
	// assistant" heuristic would have looped on --session-id forever.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-1", UserID: "u1", WorkDir: "/locked/dir", SessionID: "sess-stuck"},
	}
	ms.Messages["conv-1"] = []store.Message{
		{ID: "m1", ConversationID: "conv-1", Role: "user", Content: "first prompt"},
		{ID: "m2", ConversationID: "conv-1", Role: "error", Content: "exit status 1: Error: Session ID sess-stuck is already in use."},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/locked/dir"}}

	runner := &capturingRunner{
		events:           []agent.StreamEvent{{Kind: "result", Content: "ok"}},
		existingSessions: map[string]bool{"sess-stuck": true},
	}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-1", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "retry"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.gotOpts) != 1 {
		t.Fatalf("expected 1 run call, got %d", len(runner.gotOpts))
	}
	if runner.gotOpts[0].SessionID != "sess-stuck" {
		t.Errorf("expected session ID 'sess-stuck', got %q", runner.gotOpts[0].SessionID)
	}
	if !runner.gotOpts[0].IsResume {
		t.Error("expected IsResume=true when jsonl exists on disk, got false")
	}
}

// agenttest.FakeTitler captures GenerateTitle calls and returns a configurable result.
// Use it instead of the real claude generator in unit tests.
func TestMaybeAutoTitle_SkipsWhenNoTitleGen(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hello"},
		{ConversationID: "c1", Role: "user", Content: "world"},
	}
	h := newTestTerminalHandler(&testRunner{}, ms)
	// h.TitleGen is intentionally nil

	h.MessagePersister().MaybeAutoTitle("c1") // must not panic; must not modify title

	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "" {
		t.Errorf("expected title empty, got %q", conv.Title)
	}
}

func TestMaybeAutoTitle_FiresAfterUserMessages(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "first prompt"},
		{ConversationID: "c1", Role: "assistant", Content: "first reply"},
		{ConversationID: "c1", Role: "user", Content: "second prompt"},
		{ConversationID: "c1", Role: "assistant", Content: "second reply"},
	}
	titler := &agenttest.FakeTitler{Out: "Generated Title"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 1 {
		t.Fatalf("expected 1 generator call, got %d", titler.CallCount())
	}
	if got := titler.GotInput[0]; len(got) != 2 || got[0] != "first prompt" || got[1] != "second prompt" {
		t.Errorf("expected user prompts in order, got %v", got)
	}
	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Generated Title" {
		t.Errorf("expected title 'Generated Title', got %q", conv.Title)
	}
}

// A rotated case-mode conversation that still has a provisional title keeps
// its number on the thread when auto-title replaces it.
func TestMaybeAutoTitleReplacingWithAffixes_KeepsRotationNumber(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Title: "Slack·ops-2"}}
	ms.Messages["c1"] = []store.Message{{ConversationID: "c1", Role: "user", Content: "deploy service"}}
	titler := &agenttest.FakeTitler{Out: "COVID-19"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitleReplacingWithAffixes("c1", "Slack·ops-2", "Slack·", "-2")

	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Slack·COVID-19-2" {
		t.Fatalf("conversation title = %q, want %q", conv.Title, "Slack·COVID-19-2")
	}
}

func TestMaybeAutoTitleReplacing_PreservesManualTitle(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Title: "Manual title"}}
	ms.Messages["c1"] = []store.Message{{
		ConversationID: "c1", Role: "user", Content: "deploy service",
	}}
	titler := &agenttest.FakeTitler{Out: "Generated title"}
	persister := &service.MessagePersister{Store: ms, TitleGen: titler}

	persister.MaybeAutoTitleReplacing("c1", "Slack · ops")

	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Manual title" {
		t.Fatalf("conversation title = %q, want manual title preserved", conv.Title)
	}
	if titler.CallCount() != 0 {
		t.Fatalf("title generator calls = %d, want 0", titler.CallCount())
	}
}

func TestMaybeAutoTitle_UsesProviderSpecificGenerator(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: config.CLITypeCodex}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "codex prompt"},
	}
	defaultTitler := &agenttest.FakeTitler{Out: "Claude Title"}
	codexTitler := &agenttest.FakeTitler{Out: "Codex Title"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = defaultTitler
	h.TitleGens = map[string]agent.TitleGenerator{
		config.CLITypeCodex: codexTitler,
	}

	h.MessagePersister().MaybeAutoTitle("c1")

	if defaultTitler.CallCount() != 0 {
		t.Fatalf("expected default generator not to run, got %d calls", defaultTitler.CallCount())
	}
	if codexTitler.CallCount() != 1 {
		t.Fatalf("expected codex generator to run once, got %d calls", codexTitler.CallCount())
	}
	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Codex Title" {
		t.Errorf("expected title 'Codex Title', got %q", conv.Title)
	}
}

func TestMaybeAutoTitle_FiresOnFirstUserMessage(t *testing.T) {
	// Even a single user message (no assistant reply yet) should call the
	// generator. The generator decides whether the signal is enough; if
	// not, it returns "" and we retry on the next exchange.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "design a cache eviction policy"},
	}
	titler := &agenttest.FakeTitler{Out: "Cache Eviction Design"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 1 {
		t.Fatalf("expected 1 generator call after first user message, got %d", titler.CallCount())
	}
	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Cache Eviction Design" {
		t.Errorf("expected title persisted on first attempt, got %q", conv.Title)
	}
}

func TestMaybeAutoTitle_SkipsWhenNoUserMessages(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "thinking", Content: "noise"},
	}
	titler := &agenttest.FakeTitler{Out: "should not fire"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 0 {
		t.Errorf("expected 0 generator calls when no user prompts, got %d", titler.CallCount())
	}
}

func TestMaybeAutoTitle_DoesNotOverwriteExistingTitle(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Title: "Manual Title"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "one"},
		{ConversationID: "c1", Role: "assistant", Content: "a"},
		{ConversationID: "c1", Role: "user", Content: "two"},
		{ConversationID: "c1", Role: "assistant", Content: "b"},
	}
	titler := &agenttest.FakeTitler{Out: "Auto Title"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 0 {
		t.Errorf("expected 0 generator calls (title locked), got %d", titler.CallCount())
	}
	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Manual Title" {
		t.Errorf("expected title preserved as 'Manual Title', got %q", conv.Title)
	}
}

// Regression: title generation must run under the conversation owner's
// bound provider, not the daymug-process default. Pre-fix, every title call
// shelled out with no CLAUDE_CONFIG_DIR/CODEX_HOME override and silently
// inherited whatever the systemd unit happened to set — which both ignored
// per-user provider bindings and funneled all title traffic onto a single
// account that then hit its weekly rate limit.
func TestMaybeAutoTitle_ResolvesAccountFromUserBinding(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{
		ID:               "u1",
		Name:             "ada",
		ProviderBindings: map[string]string{config.CLITypeClaude: "hailing"},
	}}
	ms.Conversations = []store.Conversation{{
		ID:       "c1",
		UserID:   "u1",
		Provider: config.CLITypeClaude,
	}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "design a cache eviction policy"},
	}

	titler := &agenttest.FakeTitler{Out: "Cache Eviction"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler
	h.TitleGens = map[string]agent.TitleGenerator{config.CLITypeClaude: titler}
	h.Pool = service.NewPool(&config.Config{
		Providers: []config.Provider{
			{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1, ConfigDir: "/tmp/claude-default"},
			{Name: "hailing", Type: config.CLITypeClaude, MaxConcurrent: 1, ConfigDir: "/tmp/claude-hailing", Env: map[string]string{"ANTHROPIC_LOG": "info"}},
		},
	})

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 1 {
		t.Fatalf("expected 1 generator call, got %d", titler.CallCount())
	}
	got := titler.GotAccount[0]
	if got.ConfigDir != "/tmp/claude-hailing" {
		t.Errorf("ConfigDir = %q, want /tmp/claude-hailing (hailing binding, not default)", got.ConfigDir)
	}
	if got.Env["ANTHROPIC_LOG"] != "info" {
		t.Errorf("Env[ANTHROPIC_LOG] = %q, want info", got.Env["ANTHROPIC_LOG"])
	}
}

// When the owner has no binding for the conversation's CLI type, refuse to
// run the generator. The alternative — falling back to the process default —
// is the bug this whole fix targets.
func TestMaybeAutoTitle_SkipsWhenUserHasNoBinding(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "newbie"}}
	ms.Conversations = []store.Conversation{{
		ID:       "c1",
		UserID:   "u1",
		Provider: config.CLITypeClaude,
	}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hello"},
	}
	titler := &agenttest.FakeTitler{Out: "should not fire"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler
	h.TitleGens = map[string]agent.TitleGenerator{config.CLITypeClaude: titler}
	h.Pool = service.NewPool(&config.Config{
		Providers: []config.Provider{
			{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1, ConfigDir: "/tmp/claude-default"},
		},
	})

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 0 {
		t.Errorf("expected 0 generator calls (no binding), got %d", titler.CallCount())
	}
}

func TestMaybeAutoTitle_GeneratorFailureLeavesTitleEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hi"},
		{ConversationID: "c1", Role: "user", Content: "again"},
	}
	titler := &agenttest.FakeTitler{Err: context.DeadlineExceeded}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "" {
		t.Errorf("expected title empty after generator failure, got %q", conv.Title)
	}
}

func TestMaybeAutoTitle_GeneratorEmptyResultLeavesTitleEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hi"},
		{ConversationID: "c1", Role: "user", Content: "again"},
	}
	titler := &agenttest.FakeTitler{Out: ""}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "" {
		t.Errorf("expected title empty when generator returns empty, got %q", conv.Title)
	}
}

func TestMaybeAutoTitle_GeneratorAbstainKeepsTitleEmpty(t *testing.T) {
	// The generator returning "" means it can't yet make a meaningful
	// title — the title stays empty so a later call (after more context)
	// can retry.
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hi"},
	}
	titler := &agenttest.FakeTitler{Out: ""}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 1 {
		t.Fatalf("expected 1 generator call, got %d", titler.CallCount())
	}
	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "" {
		t.Errorf("expected title empty when generator abstains, got %q", conv.Title)
	}
}

func TestMaybeAutoTitle_BroadcastsTitleUpdated(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "first"},
		{ConversationID: "c1", Role: "assistant", Content: "a"},
		{ConversationID: "c1", Role: "user", Content: "second"},
		{ConversationID: "c1", Role: "assistant", Content: "b"},
	}
	titler := &agenttest.FakeTitler{Out: "Generated Title"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	// Join a fake client to the conversation room so we can observe what
	// maybeAutoTitle broadcasts after persisting the title.
	recv := make(chan []byte, 4)
	h.Broadcaster.Join("c1", "test-client", recv)
	defer h.Broadcaster.Leave("c1", "test-client")

	h.MessagePersister().MaybeAutoTitle("c1")

	select {
	case data := <-recv:
		var msg serverMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Type != "title_updated" {
			t.Errorf("expected type=title_updated, got %q", msg.Type)
		}
		if msg.ConversationID != "c1" {
			t.Errorf("expected conversation_id=c1, got %q", msg.ConversationID)
		}
		if msg.Title != "Generated Title" {
			t.Errorf("expected title='Generated Title', got %q", msg.Title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for title_updated broadcast")
	}
}

func TestMaybeAutoTitle_NoBroadcastWhenGeneratorEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hi"},
		{ConversationID: "c1", Role: "user", Content: "again"},
	}
	titler := &agenttest.FakeTitler{Out: ""}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	recv := make(chan []byte, 4)
	h.Broadcaster.Join("c1", "test-client", recv)
	defer h.Broadcaster.Leave("c1", "test-client")

	h.MessagePersister().MaybeAutoTitle("c1")

	select {
	case data := <-recv:
		t.Fatalf("expected no broadcast on empty title, got %s", string(data))
	case <-time.After(100 * time.Millisecond):
		// no message received — expected
	}
}

func TestMaybeAutoTitle_CapsPromptCount(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	for i := 1; i <= service.AutoTitlePromptCap+2; i++ {
		ms.Messages["c1"] = append(ms.Messages["c1"], store.Message{
			ConversationID: "c1",
			Role:           "user",
			Content:        "prompt" + string(rune('0'+i)),
		})
	}
	titler := &agenttest.FakeTitler{Out: "title"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler

	h.MessagePersister().MaybeAutoTitle("c1")

	if titler.CallCount() != 1 {
		t.Fatalf("expected 1 generator call, got %d", titler.CallCount())
	}
	if got := len(titler.GotInput[0]); got != service.AutoTitlePromptCap {
		t.Errorf("expected %d prompts, got %d", service.AutoTitlePromptCap, got)
	}
}

// A positive AutoTitleDelay must stagger the generator behind the main turn —
// the call only fires after the delay elapses — but still produce the title.
func TestMaybeAutoTitle_HonorsStartDelay(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ConversationID: "c1", Role: "user", Content: "hello"},
	}
	titler := &agenttest.FakeTitler{Out: "Delayed Title"}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.TitleGen = titler
	h.AutoTitleDelay = 40 * time.Millisecond

	start := time.Now()
	h.MessagePersister().MaybeAutoTitle("c1")
	elapsed := time.Since(start)

	if elapsed < h.AutoTitleDelay {
		t.Errorf("expected generator to wait at least %v, returned after %v", h.AutoTitleDelay, elapsed)
	}
	if titler.CallCount() != 1 {
		t.Fatalf("expected 1 generator call, got %d", titler.CallCount())
	}
	conv, _ := ms.GetConversation(context.Background(), "c1")
	if conv.Title != "Delayed Title" {
		t.Errorf("expected title 'Delayed Title', got %q", conv.Title)
	}
}

func TestTerminalHandler_Cancel(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "a"},
			{Kind: "delta", Content: "b"},
			{Kind: "delta", Content: "c"},
		},
		delay: 200 * time.Millisecond,
	}
	h, _ := terminalTestHandler(runner, "conv-cancel")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-cancel"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status snapshot

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})
	readSkipAck(t, ctx, conn) // thinking

	sendMsg(t, ctx, conn, clientMessage{Type: "cancel"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}
}

// TestTerminalHandler_CancelPreservesClaudeSessionLog verifies that Stop only
// interrupts execution. Claude's user prompt and partial assistant output stay
// in the session JSONL so any later message in the same conversation resumes
// with the context the user already saw.
func TestTerminalHandler_CancelPreservesClaudeSessionLog(t *testing.T) {
	jsonl := filepath.Join(t.TempDir(), "sess-rb.jsonl")
	preTurn := []byte(`{"type":"user","content":"prior turn"}` + "\n" +
		`{"type":"assistant","content":"prior reply"}` + "\n")
	if err := os.WriteFile(jsonl, preTurn, 0o644); err != nil {
		t.Fatalf("seed jsonl: %v", err)
	}

	// Append exactly the bytes claude would have written for the
	// cancelled turn (user line + partial assistant line) at the moment
	// RunWithSession is invoked, so the rollback has real content to
	// erase. Delay keeps the run alive long enough for the WS cancel
	// frame to land before the natural stream end.
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: agent.KindDelta, Content: "partial"},
		},
		delay:            500 * time.Millisecond,
		existingSessions: map[string]bool{"sess-rb": true},
		sessionLogPaths:  map[string]string{"sess-rb": jsonl},
		appendOnRun: []byte(`{"type":"user","content":"cancelled prompt"}` + "\n" +
			`{"type":"assistant","content":"partial"}` + "\n"),
		startedCh: make(chan struct{}),
	}

	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: "/tmp"})
	ms.Conversations = []store.Conversation{
		{ID: "conv-rb", UserID: "u1", WorkDir: "/tmp", SessionID: "sess-rb"},
	}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-rb"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "cancelled prompt"})
	readSkipAck(t, ctx, conn) // thinking

	// Wait for the runner goroutine to actually start (and finish the
	// synchronous append) before sending cancel. Without this gate the
	// test could send cancel before Go scheduled the goroutine, leaving
	// nothing to roll back and the final assertion passing falsely.
	select {
	case <-runner.startedCh:
	case <-ctx.Done():
		t.Fatal("runner never started")
	}

	// Now the JSONL has been appended to; the next --resume must see this
	// stopped turn as part of the conversation context.
	if info, err := os.Stat(jsonl); err != nil || info.Size() <= int64(len(preTurn)) {
		t.Fatalf("expected jsonl to have grown past pre-turn size before cancel; size=%d preTurn=%d err=%v",
			info.Size(), len(preTurn), err)
	}

	sendMsg(t, ctx, conn, clientMessage{Type: "cancel"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	got, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatalf("read jsonl after cancel: %v", err)
	}
	want := append([]byte{}, preTurn...)
	want = append(want, runner.appendOnRun...)
	if !bytes.Equal(got, want) {
		t.Fatalf("cancel must preserve the stopped turn for the next resume\n got=%q\nwant=%q", got, want)
	}
}

func TestTerminalHandler_InputAfterCancelResumesSameSession(t *testing.T) {
	const sessionID = "sess-continuity"
	runner := &cancelContinuityRunner{
		sessionID: sessionID,
		logPath:   filepath.Join(t.TempDir(), "session.jsonl"),
		runs:      make(chan capturedContinuityRun, 2),
	}
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: "/tmp"})
	ms.Conversations = []store.Conversation{
		{ID: "conv-continuity", UserID: "u1", WorkDir: "/tmp", SessionID: sessionID},
	}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-continuity"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "分析三人的 token 消耗"})
	first := <-runner.runs
	if first.opts.IsResume {
		t.Fatal("first turn unexpectedly resumed a session that did not exist")
	}
	sendMsg(t, ctx, conn, clientMessage{Type: "cancel"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	// Deliberately contains no continuation keyword: conversation identity,
	// not wording, determines whether provider context is resumed.
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "把结果按人员整理成表格"})
	second := <-runner.runs
	if !second.opts.IsResume {
		t.Fatal("input after cancel started a fresh provider session")
	}
	if second.opts.SessionID != sessionID {
		t.Fatalf("resumed session id = %q, want %q", second.opts.SessionID, sessionID)
	}
	if second.prompt != "把结果按人员整理成表格" {
		t.Fatalf("second prompt = %q", second.prompt)
	}

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}
	data, err := os.ReadFile(runner.logPath)
	if err != nil {
		t.Fatalf("read session log: %v", err)
	}
	if !bytes.Contains(data, []byte("分析三人的 token 消耗")) || !bytes.Contains(data, []byte("把结果按人员整理成表格")) {
		t.Fatalf("session log did not retain both turns: %s", data)
	}
}

// TestTerminalHandler_NormalCompletionDoesNotTouchSessionLog is the
// companion control: a turn that finishes cleanly also keeps its JSONL.
func TestTerminalHandler_NormalCompletionDoesNotTouchSessionLog(t *testing.T) {
	jsonl := filepath.Join(t.TempDir(), "sess-ok.jsonl")
	preTurn := []byte(`{"type":"user","content":"prior"}` + "\n")
	if err := os.WriteFile(jsonl, preTurn, 0o644); err != nil {
		t.Fatalf("seed jsonl: %v", err)
	}

	turnAppend := []byte(`{"type":"user","content":"this turn"}` + "\n" +
		`{"type":"assistant","content":"done"}` + "\n")
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: agent.KindResult, Content: "done"},
		},
		existingSessions: map[string]bool{"sess-ok": true},
		sessionLogPaths:  map[string]string{"sess-ok": jsonl},
		appendOnRun:      turnAppend,
	}

	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: "/tmp"})
	ms.Conversations = []store.Conversation{
		{ID: "conv-ok", UserID: "u1", WorkDir: "/tmp", SessionID: "sess-ok"},
	}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-ok"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "this turn"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	got, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	want := append([]byte{}, preTurn...)
	want = append(want, turnAppend...)
	if !bytes.Equal(got, want) {
		t.Fatalf("normal completion must leave the JSONL intact\n got=%q\nwant=%q", got, want)
	}
}

// TestTerminalHandler_CancelReturnsPendingPrompts verifies the new cancel
// contract: a prompt that's still queued behind another in-flight prompt
// (i.e. still 'pending' in the dispatcher's per-conversation queue) is
// removed from the DB on cancel, and its content is shipped back to the
// client via cancel_ack so the editor can repopulate.
func TestTerminalHandler_CancelReturnsPendingPrompts(t *testing.T) {
	// Slow runner so prompt #1 stays in flight while #2 and #3 sit
	// pending behind it.
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: agent.KindResult, Content: "ok"},
		},
		delay: 500 * time.Millisecond,
	}
	h, ms := terminalTestHandler(runner, "conv-q")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-q"})
	readMsg(t, ctx, conn) // init_ok

	// Three prompts: first one starts processing immediately, the next
	// two wait in the dispatcher's pending queue.
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "first"})
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "second"})
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "third"})

	// Wait until at least one ack + a thinking has fired (proves the
	// first prompt is in-flight and the next two are queued behind it).
	deadline := time.Now().Add(1500 * time.Millisecond)
	sawThinking := false
	for !sawThinking && time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "thinking" {
			sawThinking = true
		}
	}
	if !sawThinking {
		t.Fatal("never saw status:thinking — first prompt didn't start")
	}

	sendMsg(t, ctx, conn, clientMessage{Type: "cancel"})

	// Drain the conn looking for cancel_ack with the queued prompts.
	var ack *serverMessage
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "cancel_ack" {
			cp := msg
			ack = &cp
			break
		}
	}
	if ack == nil {
		t.Fatal("never received cancel_ack")
		return
	}
	if len(ack.CancelledPrompts) != 2 {
		t.Fatalf("expected 2 cancelled prompts (second + third), got %d: %v",
			len(ack.CancelledPrompts), ack.CancelledPrompts)
	}
	if ack.CancelledPrompts[0].Content != "second" || ack.CancelledPrompts[1].Content != "third" {
		t.Errorf("FIFO order broken in cancel_ack: %+v", ack.CancelledPrompts)
	}

	// Pending rows must be gone from the store (the in-flight 'first'
	// stays in history because it actually ran — partial output is tied
	// to it). Use snapshotMessages so the read holds the queue lock and
	// doesn't race the dispatcher worker on -race.
	leftover := ms.SnapshotMessages("conv-q")
	for _, m := range leftover {
		if m.QueueStatus == "pending" {
			t.Errorf("pending row should have been deleted on cancel: %+v", m)
		}
	}
	var firstFound bool
	for _, m := range leftover {
		if m.Role == "user" && m.Content == "first" {
			firstFound = true
		}
		if m.Role == "user" && (m.Content == "second" || m.Content == "third") {
			t.Errorf("cancelled pending prompt should not remain in history: %+v", m)
		}
	}
	if !firstFound {
		t.Error("the in-flight first prompt should still be in history")
	}
}

// Per-message recall: a cancel carrying a specific MessageID must drop
// only that pending prompt and leave the in-flight job + sibling pending
// rows intact. Powers the staging-area "click to cancel just this one"
// affordance.
func TestTerminalHandler_CancelByMessageIDDropsOnlyThatPrompt(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: agent.KindResult, Content: "ok"},
		},
		// Slow run keeps the first prompt in flight long enough for us
		// to fire the targeted cancel against the second prompt.
		delay: 800 * time.Millisecond,
	}
	h, ms := terminalTestHandler(runner, "conv-cone")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-cone"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "first"})
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "second"})
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "third"})

	// Collect the input_acks so we can target the second prompt by id.
	acks := map[string]string{}
	deadline := time.Now().Add(2 * time.Second)
	for len(acks) < 3 && time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "input_ack" && msg.MessageID != "" {
			acks[msg.MessageID] = ""
		}
	}
	if len(acks) < 3 {
		t.Fatalf("expected 3 input_acks, got %d", len(acks))
	}
	// Find the id of the prompt whose content is "second" via store snapshot.
	var secondID string
	for _, m := range ms.SnapshotMessages("conv-cone") {
		if m.Role == "user" && m.Content == "second" {
			secondID = m.ID
			break
		}
	}
	if secondID == "" {
		t.Fatal("did not find pending row for 'second' in store")
	}

	sendMsg(t, ctx, conn, clientMessage{Type: "cancel", MessageID: secondID})

	// Look for the cancel_ack carrying exactly the dropped row.
	var ack *serverMessage
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "cancel_ack" {
			cp := msg
			ack = &cp
			break
		}
	}
	if ack == nil {
		t.Fatal("never received cancel_ack for targeted cancel")
		return
	}
	if len(ack.CancelledPrompts) != 1 || ack.CancelledPrompts[0].ID != secondID {
		t.Fatalf("expected exactly the second prompt in ack, got %+v", ack.CancelledPrompts)
	}

	// 'second' must be gone from the DB; 'first' (in-flight) and 'third'
	// (still pending behind first) must remain.
	leftover := ms.SnapshotMessages("conv-cone")
	var sawFirst, sawThird bool
	for _, m := range leftover {
		if m.Role != "user" {
			continue
		}
		switch m.Content {
		case "first":
			sawFirst = true
		case "second":
			t.Errorf("targeted cancel left 'second' in history: %+v", m)
		case "third":
			sawThird = true
		}
	}
	if !sawFirst || !sawThird {
		t.Errorf("expected first + third to survive targeted cancel, sawFirst=%v sawThird=%v", sawFirst, sawThird)
	}
}

func TestTerminalHandler_CancelAckBroadcastsToOtherTabs(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: agent.KindResult, Content: "ok"},
		},
		delay: 800 * time.Millisecond,
	}
	h, ms := terminalTestHandler(runner, "conv-cancel-peer")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]

	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()
	readMsg(t, ctx, conn1) // ready
	sendMsg(t, ctx, conn1, clientMessage{Type: "init", ConversationID: "conv-cancel-peer"})
	readMsg(t, ctx, conn1) // init_ok

	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()
	readMsg(t, ctx, conn2) // ready
	sendMsg(t, ctx, conn2, clientMessage{Type: "init", ConversationID: "conv-cancel-peer"})
	readMsg(t, ctx, conn2) // init_ok

	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "first"})
	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "second"})
	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "third"})

	acks := 0
	deadline := time.Now().Add(2 * time.Second)
	for acks < 3 && time.Now().Before(deadline) {
		if msg := readMsg(t, ctx, conn1); msg.Type == "input_ack" {
			acks++
		}
	}
	if acks < 3 {
		t.Fatalf("expected 3 input_acks, got %d", acks)
	}

	var secondID string
	for _, m := range ms.SnapshotMessages("conv-cancel-peer") {
		if m.Role == "user" && m.Content == "second" {
			secondID = m.ID
			break
		}
	}
	if secondID == "" {
		t.Fatal("did not find pending row for 'second' in store")
	}

	sendMsg(t, ctx, conn1, clientMessage{Type: "cancel", MessageID: secondID})

	var peerAck *serverMessage
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn2)
		if msg.Type == "cancel_ack" {
			cp := msg
			peerAck = &cp
			break
		}
	}
	if peerAck == nil {
		t.Fatal("peer tab never received cancel_ack")
		return
	}
	if len(peerAck.CancelledPrompts) != 1 || peerAck.CancelledPrompts[0].ID != secondID {
		t.Fatalf("peer tab expected cancelled prompt %q, got %+v", secondID, peerAck.CancelledPrompts)
	}
}

func TestTerminalHandler_BroadcastToMultipleClients(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "Hi"},
			{Kind: "result", Content: "Hi"},
		},
		delay: 50 * time.Millisecond,
	}
	h, _ := terminalTestHandler(runner, "conv-bc")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]

	// Connect client 1
	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()
	readMsg(t, ctx, conn1) // ready
	sendMsg(t, ctx, conn1, clientMessage{Type: "init", ConversationID: "conv-bc"})
	readMsg(t, ctx, conn1) // init_ok

	// Connect client 2
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()
	readMsg(t, ctx, conn2) // ready
	sendMsg(t, ctx, conn2, clientMessage{Type: "init", ConversationID: "conv-bc"})
	readMsg(t, ctx, conn2) // init_ok

	// Client 1 sends input
	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "hello"})

	// Both clients should receive thinking, delta, result, ready
	for _, conn := range []*websocket.Conn{conn1, conn2} {
		var types []string
		for {
			msg := readMsg(t, ctx, conn)
			types = append(types, msg.Type)
			if msg.Type == "status" && msg.Status == "ready" {
				break
			}
		}
		if len(types) < 4 {
			t.Errorf("expected at least 4 messages, got %d: %v", len(types), types)
		}
	}
}

func TestTerminalHandler_EchoesUserMessageToOtherTabsOnly(t *testing.T) {
	// Two tabs on the same conversation. The sender's user prompt must be
	// echoed to the other tab as a "user_message" event so it appears live,
	// but must NOT be re-delivered to the sender (whose UI already pushed it).
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "ok"},
			{Kind: "result", Content: "ok"},
		},
		delay: 50 * time.Millisecond,
	}
	h, _ := terminalTestHandler(runner, "conv-echo")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]

	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()
	readMsg(t, ctx, conn1) // ready
	sendMsg(t, ctx, conn1, clientMessage{Type: "init", ConversationID: "conv-echo"})
	readMsg(t, ctx, conn1) // init_ok

	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()
	readMsg(t, ctx, conn2) // ready
	sendMsg(t, ctx, conn2, clientMessage{Type: "init", ConversationID: "conv-echo"})
	readMsg(t, ctx, conn2) // init_ok

	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "hello from tab 1"})

	// Receiver (conn2): collect every message until ready; expect exactly one
	// user_message event with the sender's content.
	var c2Types []string
	var c2UserMessages []string
	for {
		msg := readMsg(t, ctx, conn2)
		c2Types = append(c2Types, msg.Type)
		if msg.Type == "user_message" {
			c2UserMessages = append(c2UserMessages, messageContentString(t, msg))
		}
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}
	if len(c2UserMessages) != 1 || c2UserMessages[0] != "hello from tab 1" {
		t.Errorf("conn2 expected single user_message 'hello from tab 1', got %v (all types: %v)",
			c2UserMessages, c2Types)
	}

	// Sender (conn1): drain every message until ready; must NOT contain a
	// user_message echo.
	for {
		msg := readMsg(t, ctx, conn1)
		if msg.Type == "user_message" {
			t.Errorf("conn1 should not receive user_message echo, got %+v", msg)
		}
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}
}

func TestTerminalHandler_SecondTabGetsBusyStatus(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "slow"},
			{Kind: "result", Content: "slow"},
		},
		delay: 200 * time.Millisecond,
	}
	h, _ := terminalTestHandler(runner, "conv-busy2")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]

	// Connect client 1 and start processing
	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	defer func() { _ = conn1.CloseNow() }()
	readMsg(t, ctx, conn1) // ready
	sendMsg(t, ctx, conn1, clientMessage{Type: "init", ConversationID: "conv-busy2"})
	readMsg(t, ctx, conn1) // init_ok
	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "hello"})
	readSkipAck(t, ctx, conn1) // thinking

	// Connect client 2 while busy
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()
	readMsg(t, ctx, conn2) // ready
	sendMsg(t, ctx, conn2, clientMessage{Type: "init", ConversationID: "conv-busy2"})
	msg := readMsg(t, ctx, conn2) // init_ok
	if msg.Type != "init_ok" {
		t.Fatalf("expected init_ok, got %+v", msg)
	}

	// Client 2 should observe the conversation is busy. Several frames are
	// acceptable as the first such signal:
	//   - init_status snapshot with Status=thinking (the post-init
	//     authoritative snapshot)
	//   - history_backfill of the in-flight user prompt
	//   - a replayed delta / result from the active stream
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg = readMsg(t, ctx, conn2)
		if msg.Type == "init_status" && msg.Status == "thinking" {
			return
		}
		if msg.Type == "history_backfill" || msg.Type == "delta" || msg.Type == "result" {
			return
		}
	}
	t.Fatalf("expected init_status:thinking, history_backfill, or stream event for busy conv, got %+v", msg)
}

func TestTerminalHandler_ThinkingPersistence(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-think", UserID: "u1", SessionID: "sess-1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindThinkingDelta, Content: "Let me "},
		{Kind: agent.KindThinkingDelta, Content: "think about this"},
		{Kind: agent.KindDelta, Content: "Answer"},
		{Kind: agent.KindResult, Content: "Answer"},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-think", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	// Verify thinking was saved to store. snapshotMessages is the
	// thread-safe accessor — the dispatcher's worker may still be
	// running MarkPromptDone while we read.
	msgs := ms.SnapshotMessages("conv-think")
	var foundThinking, foundAssistant bool
	for _, m := range msgs {
		if m.Role == "thinking" && m.Content == "Let me think about this" {
			foundThinking = true
		}
		if m.Role == "assistant" && m.Content == "Answer" {
			foundAssistant = true
		}
	}
	if !foundThinking {
		t.Error("expected thinking message to be saved to store")
	}
	if !foundAssistant {
		t.Error("expected assistant message to be saved to store")
	}
}

// A single turn can emit several KindResult events — codex's new schema
// produces an agent_message per step, and the legacy one follows
// agent_message with a task_complete carrying the same final text. The
// push notification must fire exactly once per turn regardless, carrying
// the last reply. Persisting per-result is fine; notifying per-result is
// not (it would buzz the user once per chunk).
func TestTerminalHandler_SingleNotificationPerTurn(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-notify", UserID: "u1", SessionID: "sess-1", NotificationsEnabled: true},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", Username: "alice", WorkDir: "/tmp", BarkURL: "https://bark.example/key"}}

	// Two KindResult events in one turn, as a codex turn would emit.
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindThinkingDelta, Content: "pondering"},
		{Kind: agent.KindResult, Content: "first chunk"},
		{Kind: agent.KindResult, Content: "final answer"},
	}}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(runner, ms)
	h.BarkSender = sender

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-notify", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	// maybeNotify runs detached (go), so poll for it to land. Only one
	// notify goroutine is ever dispatched per turn, so the count can never
	// exceed 1 — but we wait to be sure it isn't 0.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(sender.Calls()) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := sender.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 bark call for a multi-result turn, got %d", len(calls))
	}
	if calls[0].body != "final answer" {
		t.Errorf("notification body = %q, want the final reply %q", calls[0].body, "final answer")
	}
}

// Tool calls fire in the middle of a turn (interleaved with delta text);
// they have to be persisted as they happen so a refresh reproduces the
// `[Read]` / `[Bash]` chips the user just watched stream in. Each
// tool_result event becomes its own role='tool' row carrying the raw JSON
// payload — frontend re-formats it identically for live and replayed views.
func TestTerminalHandler_ToolPersistence(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-tool", UserID: "u1", SessionID: "sess-1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	tool1 := `{"name":"Read","id":"t1","input":{"file":"foo.txt"}}`
	tool2 := `{"name":"Bash","id":"t2","input":{"cmd":"ls"}}`
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindToolResult, Content: tool1},
		{Kind: agent.KindDelta, Content: "Found "},
		{Kind: agent.KindToolResult, Content: tool2},
		{Kind: agent.KindDelta, Content: "it."},
		{Kind: agent.KindResult, Content: "Found it."},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-tool", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	var toolMsgs []store.Message
	var foundAssistant bool
	for _, m := range ms.SnapshotMessages("conv-tool") {
		if m.Role == "tool" {
			toolMsgs = append(toolMsgs, m)
		}
		if m.Role == "assistant" && m.Content == "Found it." {
			foundAssistant = true
		}
	}
	if len(toolMsgs) != 2 {
		t.Fatalf("expected 2 tool messages, got %d (%+v)", len(toolMsgs), toolMsgs)
	}
	if toolMsgs[0].Content != tool1 {
		t.Errorf("tool 1 content mismatch: got %q want %q", toolMsgs[0].Content, tool1)
	}
	if toolMsgs[1].Content != tool2 {
		t.Errorf("tool 2 content mismatch: got %q want %q", toolMsgs[1].Content, tool2)
	}
	if !foundAssistant {
		t.Error("expected assistant message after tool persistence")
	}
}

// Sub-agent (Task / Agent) events flow through the same outputCh as the
// parent's events but must not leak into the parent's persisted history,
// otherwise a reload would replay the worker's chatter as if the parent had
// said it. The events should still reach the WebSocket so the UI can render
// them as a separate sub-agent track.
func TestTerminalHandler_SubagentEventsAreNotPersisted(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-sub", UserID: "u1", SessionID: "sess-1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	parentTool := `{"name":"Agent","id":"toolu_parent","input":{"description":"explore"}}`
	subTool := `{"name":"Read","id":"toolu_inner","input":{"file":"foo.txt"}}`
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindDelta, Content: "Let me look. "},
		{Kind: agent.KindToolResult, Content: parentTool},
		// Sub-agent's interleaved stream — these must be dropped from
		// persistence even though they share the same channel.
		{Kind: agent.KindSystemInit, Content: `{"model":"claude-haiku-4-5"}`, Subagent: true},
		{Kind: agent.KindDelta, Content: "sub thinking out loud", Subagent: true},
		{Kind: agent.KindThinkingDelta, Content: "sub-agent thinking", Subagent: true},
		{Kind: agent.KindToolResult, Content: subTool, Subagent: true},
		{Kind: agent.KindResult, Content: "sub-agent done", Subagent: true},
		// Parent finishes.
		{Kind: agent.KindDelta, Content: "Done."},
		{Kind: agent.KindResult, Content: "Let me look. Done."},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-sub", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})

	sawSubagentWire := false
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Subagent {
			sawSubagentWire = true
		}
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}
	if !sawSubagentWire {
		t.Errorf("expected at least one wire frame with subagent=true, saw none")
	}

	var toolMsgs, assistantMsgs []store.Message
	for _, m := range ms.SnapshotMessages("conv-sub") {
		switch m.Role {
		case "tool":
			toolMsgs = append(toolMsgs, m)
		case "assistant":
			assistantMsgs = append(assistantMsgs, m)
		}
	}
	if len(toolMsgs) != 1 {
		t.Fatalf("expected exactly 1 persisted tool message (parent's only), got %d: %+v", len(toolMsgs), toolMsgs)
	}
	if toolMsgs[0].Content != parentTool {
		t.Errorf("expected only the parent tool to persist, got %q", toolMsgs[0].Content)
	}
	if len(assistantMsgs) != 1 {
		t.Fatalf("expected exactly 1 assistant message, got %d: %+v", len(assistantMsgs), assistantMsgs)
	}
	if assistantMsgs[0].Content != "Let me look. Done." {
		t.Errorf("assistant text leaked sub-agent content: got %q", assistantMsgs[0].Content)
	}
}

// When the Claude CLI emits a final `result` line with an empty result field
// (e.g. an interrupted turn), persistResult must still save the reply text
// the user actually saw on screen by falling back to the accumulated delta
// stream. Without this, the assistant message vanishes after a refresh.
func TestTerminalHandler_PersistsAccumulatedDeltasWhenResultIsEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-empty-result", UserID: "u1", SessionID: "sess-1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindDelta, Content: "Hello "},
		{Kind: agent.KindDelta, Content: "world"},
		{Kind: agent.KindResult, Content: ""},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-empty-result", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hi"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	emptyResultMsgs := ms.SnapshotMessages("conv-empty-result")
	var foundAssistant bool
	for _, m := range emptyResultMsgs {
		if m.Role == "assistant" && m.Content == "Hello world" {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Errorf("expected assistant message 'Hello world' from accumulated deltas, got messages: %+v", emptyResultMsgs)
	}
}

// When the Claude CLI is killed mid-stream (e.g. service shutdown, OS kill),
// the output channel closes without ever emitting KindResult. The accumulated
// delta text must still be persisted so the user's view survives a refresh.
func TestTerminalHandler_PersistsAccumulatedDeltasWhenChannelClosesWithoutResult(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "conv-no-result", UserID: "u1", SessionID: "sess-1"},
	}
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}

	// No KindResult at all — simulates a process killed mid-stream.
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindThinkingDelta, Content: "thinking..."},
		{Kind: agent.KindDelta, Content: "partial "},
		{Kind: agent.KindDelta, Content: "reply"},
	}}
	h := newTestTerminalHandler(runner, ms)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-no-result", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hi"})

	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	noResultMsgs := ms.SnapshotMessages("conv-no-result")
	var foundAssistant, foundThinking bool
	for _, m := range noResultMsgs {
		if m.Role == "assistant" && m.Content == "partial reply" {
			foundAssistant = true
		}
		if m.Role == "thinking" && m.Content == "thinking..." {
			foundThinking = true
		}
	}
	if !foundAssistant {
		t.Errorf("expected assistant message 'partial reply' from accumulated deltas after channel close, got messages: %+v", noResultMsgs)
	}
	if !foundThinking {
		t.Errorf("expected thinking message to also be saved on channel close, got messages: %+v", noResultMsgs)
	}
}

// persistResult bumps the conversation's updated_at via SaveMessage, so
// peer tabs (and tabs viewing other conversations of the same user) need
// a `conversation_updated` event on the user hub to re-sort their session
// list. Without it the active tab's REST refresh would correct itself but
// peer tabs would stay stale until a manual reload.
func TestTerminalHandler_PersistResult_BroadcastsConversationUpdatedToUserHub(t *testing.T) {
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: "/tmp"})
	_ = ms.CreateConversation(context.Background(), "conv-bump", "", "u1", "/tmp", "", "")

	hub := service.NewUserHub()
	peer := make(chan []byte, 4)
	hub.Join("u1", "peer-tab", peer)

	h := newTestTerminalHandler(&testRunner{}, ms)
	h.UserHub = hub

	var thinking strings.Builder
	thinking.WriteString("pondering")
	ids := h.MessagePersister().PersistResult("conv-bump", &thinking, "the answer", nil)
	if !ids.AssistantOK {
		t.Fatalf("expected assistant save to succeed, got %+v", ids)
	}

	// broadcastConvUpdated runs in a goroutine — give it a brief moment.
	var data []byte
	select {
	case data = <-peer:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for conversation_updated event")
	}

	var evt struct {
		Type           string              `json:"type"`
		ConversationID string              `json:"conversation_id"`
		Conversation   *store.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Type != "conversation_updated" {
		t.Errorf("expected type=conversation_updated, got %q", evt.Type)
	}
	if evt.ConversationID != "conv-bump" {
		t.Errorf("expected conversation_id=conv-bump, got %q", evt.ConversationID)
	}
	if evt.Conversation == nil || evt.Conversation.UserID != "u1" {
		t.Errorf("expected payload to carry the conversation row, got %+v", evt.Conversation)
	}
}

// Per-turn usage + model id is persisted onto the assistant row as
// JSON metadata so a fresh client can render the token chip from REST
// history alone, without any localStorage shadow.
func TestTerminalHandler_PersistResult_StoresMetadataOnAssistantRow(t *testing.T) {
	ms := storetest.New()
	_ = ms.CreateConversation(context.Background(), "conv-meta", "", "u1", "/tmp", "", "")

	h := newTestTerminalHandler(&testRunner{}, ms)

	meta := service.BuildAssistantMetadataFor(service.AssistantMetadata{
		Usage: `{"input_tokens":7,"output_tokens":984,"total_cost_usd":0.1074,"num_turns":2}`,
		Model: "claude-opus-4-8[1m]",
	})
	var thinking strings.Builder
	ids := h.MessagePersister().PersistResult("conv-meta", &thinking, "the answer", meta)
	if !ids.AssistantOK || ids.AssistantID == "" {
		t.Fatalf("expected assistant save to succeed, got %+v", ids)
	}

	stored := ms.SnapshotMessages("conv-meta")
	var assistant *store.Message
	for i := range stored {
		if stored[i].Role == "assistant" {
			assistant = &stored[i]
		}
	}
	if assistant == nil {
		t.Fatalf("assistant row missing, got %+v", stored)
		return
	}
	if len(assistant.Metadata) == 0 {
		t.Fatalf("expected metadata, got empty")
	}
	var parsed struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int     `json:"input_tokens"`
			OutputTokens int     `json:"output_tokens"`
			TotalCostUSD float64 `json:"total_cost_usd"`
			NumTurns     int     `json:"num_turns"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(assistant.Metadata, &parsed); err != nil {
		t.Fatalf("metadata not valid JSON: %v (%s)", err, string(assistant.Metadata))
	}
	if parsed.Model != "claude-opus-4-8[1m]" {
		t.Errorf("model = %q, want claude-opus-4-8[1m]", parsed.Model)
	}
	if parsed.Usage.InputTokens != 7 || parsed.Usage.OutputTokens != 984 {
		t.Errorf("usage I/O = %d/%d, want 7/984", parsed.Usage.InputTokens, parsed.Usage.OutputTokens)
	}
	if parsed.Usage.TotalCostUSD != 0.1074 || parsed.Usage.NumTurns != 2 {
		t.Errorf("usage cost/turns = %v/%d, want 0.1074/2", parsed.Usage.TotalCostUSD, parsed.Usage.NumTurns)
	}
}

// The handler runs a per-connection keep-alive ping so backgrounded tabs
// don't have their TCP connection reaped by NAT/proxy idle timeouts. We
// shorten the interval here to exercise the loop without sleeping for
// seconds; the connection should remain usable across multiple ping cycles.
func TestTerminalHandler_PingsKeepConnectionAlive(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{{Kind: agent.KindResult, Content: "ok"}}}
	h, _ := terminalTestHandler(runner, "conv-ping")
	h.PingInterval = 30 * time.Millisecond

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // initial "ready"

	// Sit idle past several ping intervals — the server should be sending
	// pings to us in the background. The library auto-replies with pongs;
	// we just need the connection to remain usable afterwards.
	time.Sleep(150 * time.Millisecond)

	// Connection still healthy: an init round-trip succeeds.
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-ping"})
	got := readMsg(t, ctx, conn)
	if got.Type != "init_ok" {
		t.Fatalf("expected init_ok after ping cycles, got %+v", got)
	}
}

func TestTerminalHandler_DrainNotifiesAndQueuesInput(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{{Kind: "delta", Content: "x"}}}
	h, _ := terminalTestHandler(runner, "conv-drain")
	h.Drainer = service.NewDrainer()

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready

	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-drain"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status snapshot

	// Trigger drain. Existing client should receive a "shutdown" status.
	h.Drainer.StartDrain()

	msg := readMsg(t, ctx, conn)
	if msg.Type != "status" || msg.Status != "shutdown" {
		t.Fatalf("expected status:shutdown, got %+v", msg)
	}

	// New input messages are still persisted and acked, but not executed;
	// the restarted server will recover the pending queue.
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hello"})
	msg = readMsg(t, ctx, conn)
	if msg.Type != "input_ack" || msg.ConversationID != "conv-drain" || msg.MessageID == "" {
		t.Fatalf("expected queued input ack, got %+v", msg)
	}
	h.Store.(*storetest.Fake).QueueMu.Lock()
	queued := append([]store.Message(nil), h.Store.(*storetest.Fake).Messages["conv-drain"]...)
	h.Store.(*storetest.Fake).QueueMu.Unlock()
	if len(queued) != 1 || queued[0].Content != "hello" || queued[0].QueueStatus != "pending" {
		t.Fatalf("expected pending queued message, got %+v", queued)
	}
}

func TestTerminalHandler_PausedInputBroadcastsQueuedActivity(t *testing.T) {
	h, _ := terminalTestHandler(&testRunner{}, "conv-paused")
	h.Drainer = service.NewDrainer()
	h.Pause = service.NewPauseGate()
	h.Pause.SetPaused(true)
	h.Dispatcher.SetPauseGate(h.Pause)

	srv := terminalTestServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-paused", UserID: "u1"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "wait here"})
	if ack := readMsg(t, ctx, conn); ack.Type != "input_ack" {
		t.Fatalf("expected input_ack, got %+v", ack)
	}
	activity := readMsg(t, ctx, conn)
	if activity.Type != "conversation_activity" || len(activity.QueuedConversations) != 1 ||
		activity.QueuedConversations[0].ConversationID != "conv-paused" {
		t.Fatalf("queued activity = %+v", activity)
	}
}

func TestTerminalHandler_ReconnectReplaysInFlightStream(t *testing.T) {
	// Pre-fix: a page refresh during a Claude response killed the CLI process;
	// the next message hit "Session ID is already in use" because the prior
	// process hadn't released the lock. Now the job is detached from the
	// WebSocket lifecycle and the broadcaster buffers stream events so a
	// reconnecting tab catches up via replay. Verifies: WS1 starts a job and
	// disconnects mid-stream; WS2 reconnects to the same conversation and
	// receives every delta + result + final "ready", proving the job survived
	// the disconnect AND missed events were replayed.
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "a"},
			{Kind: "delta", Content: "b"},
			{Kind: "delta", Content: "c"},
			{Kind: "delta", Content: "d"},
			{Kind: "result", Content: "abcd"},
		},
		delay: 80 * time.Millisecond,
	}
	h, _ := terminalTestHandler(runner, "conv-replay")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]

	// WS1: start the job, read the first delta, then drop the connection.
	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial1: %v", err)
	}
	readMsg(t, ctx, conn1) // ready
	sendMsg(t, ctx, conn1, clientMessage{Type: "init", ConversationID: "conv-replay"})
	readMsg(t, ctx, conn1) // init_ok
	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "go"})
	readSkipAck(t, ctx, conn1) // thinking
	if first := readMsg(t, ctx, conn1); first.Type != "delta" {
		t.Fatalf("expected delta on conn1, got %+v", first)
	}
	_ = conn1.CloseNow()

	// WS2: reconnect to the same conversation. Must receive every delta,
	// the result, and a final "ready" — some via replay, some live.
	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()
	readMsg(t, ctx, conn2) // ready
	sendMsg(t, ctx, conn2, clientMessage{Type: "init", ConversationID: "conv-replay"})
	readMsg(t, ctx, conn2) // init_ok

	seenDeltas := map[string]bool{}
	var sawResult, sawReady bool
	for !sawReady {
		msg := readMsg(t, ctx, conn2)
		switch msg.Type {
		case "delta":
			seenDeltas[messageContentString(t, msg)] = true
		case "result":
			if messageContentString(t, msg) == "abcd" {
				sawResult = true
			}
		case "status":
			if msg.Status == "ready" {
				sawReady = true
			}
		}
	}

	for _, want := range []string{"a", "b", "c", "d"} {
		if !seenDeltas[want] {
			t.Errorf("conn2 missing delta %q (got %v) — replay or live delivery failed", want, seenDeltas)
		}
	}
	if !sawResult {
		t.Error("conn2 never saw result:abcd")
	}
}

func TestTerminalHandler_CancelWorksFromReconnectedClient(t *testing.T) {
	// The cancel func now lives in the Broadcaster keyed by conversationID, so
	// a fresh WebSocket can abort a job started by an earlier (now disconnected)
	// tab. Without that, the only way to stop a runaway response after a refresh
	// would be to wait for it to finish or kill the server.
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "x"},
			{Kind: "delta", Content: "y"},
			{Kind: "delta", Content: "z"},
			{Kind: "result", Content: "xyz"},
		},
		delay: 500 * time.Millisecond, // long enough that cancel beats completion
	}
	h, _ := terminalTestHandler(runner, "conv-cancel-rc")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]

	conn1, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial1: %v", err)
	}
	readMsg(t, ctx, conn1) // ready
	sendMsg(t, ctx, conn1, clientMessage{Type: "init", ConversationID: "conv-cancel-rc"})
	readMsg(t, ctx, conn1) // init_ok
	sendMsg(t, ctx, conn1, clientMessage{Type: "input", Content: "go"})
	readSkipAck(t, ctx, conn1) // thinking
	_ = conn1.CloseNow()

	conn2, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer func() { _ = conn2.CloseNow() }()
	readMsg(t, ctx, conn2) // ready
	sendMsg(t, ctx, conn2, clientMessage{Type: "init", ConversationID: "conv-cancel-rc"})
	readMsg(t, ctx, conn2) // init_ok

	sendMsg(t, ctx, conn2, clientMessage{Type: "cancel"})

	// "ready" should arrive well before the runner's natural completion
	// (4 events × 500ms = 2s). If cancel didn't propagate, we'd hit the
	// outer test timeout instead.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn2)
		if msg.Type == "status" && msg.Status == "ready" {
			return
		}
	}
	t.Fatal("cancel from reconnected WS did not abort the job in time")
}

// fakeBarkSender records every Send call so tests can assert on what was
// (or wasn't) dispatched without making any real HTTP requests.
type fakeBarkSender struct {
	mu    sync.Mutex
	calls []barkCall
}

type barkCall struct {
	url, title, body, clickURL string
}

func (f *fakeBarkSender) Send(_ context.Context, url, title, body, clickURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, barkCall{url: url, title: title, body: body, clickURL: clickURL})
	return nil
}

func (f *fakeBarkSender) Calls() []barkCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]barkCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestMaybeNotify_FiresWhenEnabled(t *testing.T) {
	// Notification config now lives on the human owner — the conversation's
	// user is resolved via GetOwner, so the test row needs Username (or a
	// matching email) set to count as a human and resolve to itself.
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Alice", Username: "alice", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "Project work", NotificationsEnabled: true},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender

	h.MessagePersister().MaybeNotify("c1", "Done refactoring the auth module.")

	calls := sender.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 bark call, got %d", len(calls))
	}
	if calls[0].url != "https://bark.example/key" {
		t.Errorf("expected url 'https://bark.example/key', got %q", calls[0].url)
	}
	if calls[0].title != "Project work" {
		t.Errorf("expected title 'Project work', got %q", calls[0].title)
	}
	if calls[0].body != "Done refactoring the auth module." {
		t.Errorf("expected body to match result, got %q", calls[0].body)
	}
	if calls[0].clickURL != "" {
		t.Errorf("expected no click URL without a configured public URL, got %q", calls[0].clickURL)
	}
}

func TestMaybeNotify_IncludesDeepLinkWhenPublicURLSet(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Alice", Username: "alice", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "Project work", NotificationsEnabled: true},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender
	// Trailing slash must be tolerated and trimmed.
	h.Cfg = &config.Config{Server: config.ServerConfig{PublicURL: "https://daymug.example.com/"}}

	h.MessagePersister().MaybeNotify("c1", "Done.")

	calls := sender.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 bark call, got %d", len(calls))
	}
	want := "https://daymug.example.com/chat/u1/c1"
	if calls[0].clickURL != want {
		t.Errorf("expected click URL %q, got %q", want, calls[0].clickURL)
	}
}

func TestMaybeNotify_SkipsWhenConvDisabled(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "T", NotificationsEnabled: false},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender

	h.MessagePersister().MaybeNotify("c1", "result")

	if got := sender.Calls(); len(got) != 0 {
		t.Errorf("expected 0 bark calls when conv disabled, got %d", len(got))
	}
}

func TestMaybeNotify_SkipsWhenURLEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", BarkURL: ""}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", NotificationsEnabled: true},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender

	h.MessagePersister().MaybeNotify("c1", "result")

	if got := sender.Calls(); len(got) != 0 {
		t.Errorf("expected 0 bark calls when bark url missing, got %d", len(got))
	}
}

func TestMaybeNotify_SkipsWhenSenderNil(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", NotificationsEnabled: true},
	}
	h := newTestTerminalHandler(&testRunner{}, ms)
	// h.BarkSender intentionally nil — must not panic, must be a no-op.
	h.MessagePersister().MaybeNotify("c1", "result")
}

// TestMaybeNotify_UsesOwnerWhenConvBelongsToAgent verifies the new
// "notifications live on the human owner" routing: the conversation is
// owned by an agent (no username, BarkURL empty), but the agent's owner —
// the human sharing its email — has a Bark URL set, and the push fires
// against that URL.
func TestMaybeNotify_UsesOwnerWhenConvBelongsToAgent(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human", Name: "Alice", Username: "alice", Email: "alice@example.com", BarkURL: "https://bark.example/owner"},
		{ID: "agent", Name: "Researcher", Email: "alice@example.com"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "agent", Title: "Investigation", NotificationsEnabled: true},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender

	h.MessagePersister().MaybeNotify("c1", "result")

	calls := sender.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 bark call routed via owner, got %d", len(calls))
	}
	if calls[0].url != "https://bark.example/owner" {
		t.Errorf("expected owner's url, got %q", calls[0].url)
	}
}

// TestMaybeNotify_SkipsWhenAgentHasNoOwner covers the orphan case: an
// agent row with no email matches no human, so GetOwner fails and the
// notification is silently dropped rather than panicking or sending to a
// stale per-agent URL.
func TestMaybeNotify_SkipsWhenAgentHasNoOwner(t *testing.T) {
	ms := storetest.New()
	// Agent has a BarkURL that we deliberately ignore now — only the
	// owner's URL counts. No human row sharing the agent's email.
	ms.Users = []store.User{{ID: "agent", BarkURL: "https://bark.example/agent"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "agent", NotificationsEnabled: true},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender

	h.MessagePersister().MaybeNotify("c1", "result")

	if got := sender.Calls(); len(got) != 0 {
		t.Errorf("expected 0 bark calls when agent has no owner, got %d", len(got))
	}
}

func TestMaybeNotify_FallsBackToDefaultTitle(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "", NotificationsEnabled: true},
	}
	sender := &fakeBarkSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = sender

	h.MessagePersister().MaybeNotify("c1", "")

	calls := sender.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 bark call, got %d", len(calls))
	}
	if calls[0].title == "" {
		t.Error("expected non-empty fallback title when conv title is blank")
	}
	if calls[0].body == "" {
		t.Error("expected non-empty fallback body when result is blank")
	}
}

// fakePushDeerSender mirrors fakeBarkSender for the PushDeer dispatch path.
type fakePushDeerSender struct {
	mu    sync.Mutex
	calls []pushDeerCall
}

type pushDeerCall struct {
	key, title, body string
}

func (f *fakePushDeerSender) Send(_ context.Context, key, title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, pushDeerCall{key: key, title: title, body: body})
	return nil
}

func (f *fakePushDeerSender) Calls() []pushDeerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]pushDeerCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestMaybeNotify_FiresPushDeerWhenOnlyPushDeerConfigured(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", PushDeerKey: "pdkey"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "Build feature", NotificationsEnabled: true},
	}
	bark := &fakeBarkSender{}
	pd := &fakePushDeerSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = bark
	h.PushDeerSender = pd

	h.MessagePersister().MaybeNotify("c1", "ship it")

	if got := bark.Calls(); len(got) != 0 {
		t.Errorf("expected 0 bark calls when only pushdeer configured, got %d", len(got))
	}
	calls := pd.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 pushdeer call, got %d", len(calls))
	}
	if calls[0].key != "pdkey" || calls[0].title != "Build feature" || calls[0].body != "ship it" {
		t.Errorf("unexpected pushdeer payload: %+v", calls[0])
	}
}

func TestMaybeNotify_BothConfiguredDefaultsToBark(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "u1", Username: "alice",
		BarkURL: "https://bark.example/k", PushDeerKey: "pdkey",
	}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "T", NotificationsEnabled: true},
	}
	bark := &fakeBarkSender{}
	pd := &fakePushDeerSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = bark
	h.PushDeerSender = pd

	h.MessagePersister().MaybeNotify("c1", "body")

	if got := bark.Calls(); len(got) != 1 {
		t.Errorf("expected 1 bark call (default when both configured), got %d", len(got))
	}
	if got := pd.Calls(); len(got) != 0 {
		t.Errorf("expected 0 pushdeer calls (bark wins by default), got %d", len(got))
	}
}

func TestMaybeNotify_BothConfiguredHonorsExplicitPushDeer(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "u1", Username: "alice",
		BarkURL: "https://bark.example/k", PushDeerKey: "pdkey",
		NotificationChannel: "pushdeer",
	}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Title: "T", NotificationsEnabled: true},
	}
	bark := &fakeBarkSender{}
	pd := &fakePushDeerSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = bark
	h.PushDeerSender = pd

	h.MessagePersister().MaybeNotify("c1", "body")

	if got := bark.Calls(); len(got) != 0 {
		t.Errorf("expected 0 bark calls when user picked pushdeer, got %d", len(got))
	}
	if got := pd.Calls(); len(got) != 1 {
		t.Errorf("expected 1 pushdeer call when user picked pushdeer, got %d", len(got))
	}
}

func TestMaybeNotify_SkipsWhenNeitherChannelConfigured(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice"}}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", NotificationsEnabled: true},
	}
	bark := &fakeBarkSender{}
	pd := &fakePushDeerSender{}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.BarkSender = bark
	h.PushDeerSender = pd

	h.MessagePersister().MaybeNotify("c1", "body")

	if got := bark.Calls(); len(got) != 0 {
		t.Errorf("expected 0 bark calls when no channels configured, got %d", len(got))
	}
	if got := pd.Calls(); len(got) != 0 {
		t.Errorf("expected 0 pushdeer calls when no channels configured, got %d", len(got))
	}
}

func TestTerminalHandler_DrainWaitsForActiveJob(t *testing.T) {
	runner := &testRunner{
		events: []agent.StreamEvent{
			{Kind: "delta", Content: "part"},
			{Kind: "result", Content: "done"},
		},
		delay: 80 * time.Millisecond,
	}
	h, _ := terminalTestHandler(runner, "conv-drain-wait")
	h.Drainer = service.NewDrainer()

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-drain-wait"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "go"})
	readSkipAck(t, ctx, conn) // thinking

	// Trigger drain while a job is in flight; WaitJobs should not return until
	// the job's deferred JobDone fires.
	h.Drainer.StartDrain()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := h.Drainer.WaitJobs(waitCtx); err != nil {
		t.Fatalf("WaitJobs returned %v, expected nil after job completion", err)
	}
}

func TestTerminalHandler_InitAllowsAgentPersonaForAuthedUser(t *testing.T) {
	// An agent — User row with no username, created by the human via the
	// settings UI — must be reachable from an authenticated session even
	// though its ID differs from the auth'd user. Otherwise switching into
	// any agent fails with "user_id mismatch".
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-1", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/home/alice"},
		{ID: "agent-1", Name: "Helper", Username: "", Email: "alice@x", OwnerID: "human-1", WorkDir: "/home/alice/agent-ws"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "conv-agent", UserID: "agent-1"},
	}

	runner := &testRunner{events: []agent.StreamEvent{{Kind: "result", Content: "ok"}}}
	h := newTestTerminalHandler(runner, ms)

	srv := authedTerminalTestServer(h, "human-1")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-agent", UserID: "agent-1"})
	msg := readMsg(t, ctx, conn)
	if msg.Type != "init_ok" {
		t.Fatalf("expected init_ok when authed human switches into agent persona, got %+v", msg)
	}
}

func TestTerminalHandler_InitRejectsOtherHumanUser(t *testing.T) {
	// In contrast to agents, a User row with a non-empty username represents
	// an independently login-able human; the auth'd session must not be able
	// to impersonate them.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-1", Name: "Alice", Username: "alice", WorkDir: "/home/alice"},
		{ID: "human-2", Name: "Bob", Username: "bob", WorkDir: "/home/bob"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "conv-bob", UserID: "human-2"},
	}

	runner := &testRunner{events: []agent.StreamEvent{{Kind: "result", Content: "ok"}}}
	h := newTestTerminalHandler(runner, ms)

	srv := authedTerminalTestServer(h, "human-1")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-bob", UserID: "human-2"})
	msg := readMsg(t, ctx, conn)
	if msg.Type != "error" || msg.Message != "user_id mismatch" {
		t.Fatalf("expected user_id mismatch error, got %+v", msg)
	}
}

func TestTerminalHandler_InitRejectsConversationOfOtherHuman(t *testing.T) {
	// The auth'd session must not be able to subscribe to another human's
	// conversation room by passing only the conversation_id and omitting
	// user_id (which would otherwise default to the auth'd user).
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-1", Name: "Alice", Username: "alice"},
		{ID: "human-2", Name: "Bob", Username: "bob"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "conv-bob", UserID: "human-2"},
	}

	runner := &testRunner{events: []agent.StreamEvent{{Kind: "result", Content: "ok"}}}
	h := newTestTerminalHandler(runner, ms)

	srv := authedTerminalTestServer(h, "human-1")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-bob"})
	msg := readMsg(t, ctx, conn)
	if msg.Type != "error" || msg.Message != "conversation not accessible" {
		t.Fatalf("expected conversation not accessible error, got %+v", msg)
	}
}

// TestTerminalHandler_ResultEventCarriesPersistedIDs locks in the
// id-flow contract: after the assistant reply is persisted, the
// `result` event broadcast to clients includes the persisted DB id
// (and the thinking row's id when applicable). Without this, clients
// have no canonical key for dedup against REST history / backfill,
// which is the architectural invariant for "no missing, no duplicate".
func TestTerminalHandler_ResultEventCarriesPersistedIDs(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindThinkingDelta, Content: "let me ponder"},
		{Kind: agent.KindResult, Content: "answer"},
	}}
	h, _ := terminalTestHandler(runner, "conv-ids")

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-ids"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hi"})

	// Drain until we see the result event. The id fields are what we
	// care about; everything else (thinking, status, ack) just gets
	// skipped.
	deadline := time.Now().Add(3 * time.Second)
	var resultMsg *serverMessage
	for time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "result" {
			cp := msg
			resultMsg = &cp
			break
		}
	}
	if resultMsg == nil {
		t.Fatal("never saw the result event")
		return
	}
	if resultMsg.MessageID == "" {
		t.Errorf("result event must carry the persisted assistant id, got empty")
	}
	if resultMsg.ThinkingMessageID == "" {
		t.Errorf("result event must carry the persisted thinking id when a thinking row was saved, got empty")
	}
}

// TestTerminalHandler_PersistFailedBroadcastOnSaveFailure exercises
// the new safety net: when the DB save retries are exhausted, the
// server broadcasts `persist_failed` so the client knows its view will
// drift from the DB. The original "got Bark, didn't see the message"
// failure mode collapses to this signal — silent loss is no longer
// possible.
func TestTerminalHandler_PersistFailedBroadcastOnSaveFailure(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindResult, Content: "answer"},
	}}
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: "/tmp"})
	_ = ms.CreateConversation(context.Background(), "conv-fail", "", "u1", "/tmp", "", "")
	// Wrap the store so SaveMessage permanently rejects every attempt
	// (no SQLITE_BUSY shape, so the retry loop returns immediately).
	bad := &nonBusyStore{Fake: ms}
	h := newTestTerminalHandler(runner, bad)

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-fail"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "trigger fail"})

	// Look for persist_failed somewhere in the event stream.
	deadline := time.Now().Add(3 * time.Second)
	sawPersistFailed := false
	for time.Now().Before(deadline) {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "persist_failed" {
			sawPersistFailed = true
			break
		}
		if msg.Type == "status" && msg.Status == "ready" {
			// End of turn: if we haven't seen persist_failed yet we're done.
			break
		}
	}
	if !sawPersistFailed {
		t.Fatal("expected persist_failed event after permanent save failure")
	}
}

// flakySaveStore wraps storetest.Fake so SaveMessage fails the first N calls with a
// SQLite BUSY-shaped error, then succeeds. Lets us verify the retry helper
// recovers a reply that would otherwise vanish on transient WAL contention.
type flakySaveStore struct {
	*storetest.Fake
	failBusy int
	calls    int
}

func (s *flakySaveStore) SaveMessage(ctx context.Context, msg store.Message) error {
	s.calls++
	if s.calls <= s.failBusy {
		return errBusy{}
	}
	return s.Fake.SaveMessage(ctx, msg)
}

type errBusy struct{}

func (errBusy) Error() string { return "database is locked (SQLITE_BUSY)" }

func TestSaveMessageWithRetry_RecoversFromBusy(t *testing.T) {
	ms := storetest.New()
	flaky := &flakySaveStore{Fake: ms, failBusy: 2}
	h := newTestTerminalHandler(nil, flaky)

	ctx := context.Background()
	if err := h.MessagePersister().SaveMessageWithRetry(ctx, store.Message{
		ConversationID: "conv-1",
		Role:           "assistant",
		Content:        "ok",
	}); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if flaky.calls != 3 {
		t.Fatalf("expected 3 calls (2 busy + 1 success), got %d", flaky.calls)
	}
	if got := len(ms.Messages["conv-1"]); got != 1 {
		t.Fatalf("expected 1 persisted message, got %d", got)
	}
}

func TestSaveMessageWithRetry_NonBusyErrorReturnsImmediately(t *testing.T) {
	ms := storetest.New()
	flaky := &flakySaveStore{Fake: ms, failBusy: 99}
	flaky.calls = 0
	// Override SaveMessage via wrapping: simulate a permanent non-busy failure.
	bad := &nonBusyStore{Fake: ms}
	h := newTestTerminalHandler(nil, bad)

	ctx := context.Background()
	err := h.MessagePersister().SaveMessageWithRetry(ctx, store.Message{ConversationID: "c", Role: "assistant", Content: "x"})
	if err == nil {
		t.Fatalf("expected error to bubble up")
	}
	if bad.calls != 1 {
		t.Fatalf("expected single attempt for non-busy error, got %d", bad.calls)
	}
}

type nonBusyStore struct {
	*storetest.Fake
	calls int
}

func (s *nonBusyStore) SaveMessage(_ context.Context, _ store.Message) error {
	s.calls++
	return errFatal{}
}

type errFatal struct{}

func (errFatal) Error() string { return "constraint violation" }

// blockingRunner gates StreamEvent emission on an external "release"
// channel so a test can hold the in-flight Claude run and observe what
// happens to a sibling conversation queued behind it on the per-account
// pool.
type blockingRunner struct {
	release <-chan struct{}
	mu      sync.Mutex
	started int
}

func (r *blockingRunner) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	r.mu.Lock()
	r.started++
	r.mu.Unlock()
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case outputCh <- agent.StreamEvent{Kind: agent.KindResult, Content: "ok"}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (r *blockingRunner) Run(ctx context.Context, _, _ string, outputCh chan<- agent.StreamEvent) error {
	return r.RunWithSession(ctx, "", "", agent.RunRequest{}, outputCh)
}

func (r *blockingRunner) RunOneshot(_ context.Context, _, _ string, _ agent.RunRequest) (string, error) {
	return "", nil
}

func (r *blockingRunner) SessionExists(_, _, _ string) bool    { return false }
func (r *blockingRunner) SessionLogPath(_, _, _ string) string { return "" }
func (r *blockingRunner) Name() string                         { return "blocking" }
func (r *blockingRunner) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: true, SupportsThinkingStream: true, SupportsRateLimitEvents: true, ReportsCostUSD: true}
}

// TestTerminalHandler_PoolFullDefersPromptStarted is the regression test
// for the original bug: with a per-account pool of size 1 and two
// conversations each sending a prompt, only conv A gets prompt_started
// — conv B's prompt must stay 'pending' (visible in the staging area)
// until A finishes and the slot is released. Pre-fix, B's worker would
// claim the row and broadcast prompt_started immediately at peek time,
// even though it was about to park inside the pool waiting for a slot,
// so the user saw their message leave the staging area but never make
// progress.
func TestTerminalHandler_PoolFullDefersPromptStarted(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	runner := &blockingRunner{release: release}

	h, ms := terminalTestHandler(runner, "conv-a", "conv-b")
	// 1-slot pool on the default account. Both conversations share the
	// same user ("u1"). Implicit fallback to "default" is no longer allowed,
	// so u1 is explicitly bound to it. The conversations have no stored
	// provider and the handler has no Cfg, so the resolved CLI type is the
	// empty string — bind under that same key.
	h.Pool = service.NewPool(&config.Config{
		Providers: []config.Provider{
			{Name: "default", MaxConcurrent: 1},
		},
	})
	if err := ms.SetUserProviderBinding(context.Background(), "u1", "", "default"); err != nil {
		t.Fatalf("bind u1: %v", err)
	}

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	dial := func(convID string) *websocket.Conn {
		c, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			t.Fatalf("dial %s: %v", convID, err)
		}
		readMsg(t, ctx, c) // ready
		sendMsg(t, ctx, c, clientMessage{Type: "init", ConversationID: convID})
		readMsg(t, ctx, c) // init_ok
		return c
	}

	connA := dial("conv-a")
	defer func() { _ = connA.CloseNow() }()
	connB := dial("conv-b")
	defer func() { _ = connB.CloseNow() }()

	// A acquires the only slot. Send and wait for prompt_started so we
	// know the pool is occupied before B's prompt enqueues.
	sendMsg(t, ctx, connA, clientMessage{Type: "input", Content: "hello A"})
	awaitPromptStarted := func(c *websocket.Conn, conv string) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			msg := readMsg(t, ctx, c)
			if msg.Type == "prompt_started" {
				return
			}
		}
		t.Fatalf("%s never received prompt_started", conv)
	}
	awaitPromptStarted(connA, "conv-a")

	// Now send on B. With concurrency=1 and A holding the slot, B must
	// NOT see prompt_started until A finishes.
	sendMsg(t, ctx, connB, clientMessage{Type: "input", Content: "hello B"})

	sawPromptStartedB := make(chan struct{})
	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer bgCancel()
		for {
			_, data, err := connB.Read(bgCtx)
			if err != nil {
				return
			}
			var msg serverMessage
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			if msg.Type == "prompt_started" {
				close(sawPromptStartedB)
				return
			}
		}
	}()

	// Give B a window to (incorrectly) emit prompt_started while A is
	// still parked inside the runner.
	select {
	case <-sawPromptStartedB:
		t.Fatal("conv-b received prompt_started while pool was full — staging area would have prematurely emptied")
	case <-time.After(300 * time.Millisecond):
	}

	// B's row must still be in 'pending' (visible in the staging area).
	// Read through snapshotMessages so we hold the queue lock — without
	// it MarkPromptDone races with this read on -race.
	gotB := ms.SnapshotMessages("conv-b")
	if len(gotB) != 1 {
		t.Fatalf("expected exactly one message persisted on conv-b, got %d", len(gotB))
	}
	if gotB[0].QueueStatus != "pending" {
		t.Errorf("expected conv-b row to stay 'pending' while pool full, got %q", gotB[0].QueueStatus)
	}

	// Release A. Once its slot is freed, B should pick it up and emit
	// prompt_started.
	release <- struct{}{}
	select {
	case <-sawPromptStartedB:
	case <-time.After(2 * time.Second):
		t.Fatal("conv-b never received prompt_started after pool slot was released")
	}
}

// TestTerminalHandler_TransientClaimErrorRetries is the regression for
// the "third tab stuck on queued forever" report. Three browser tabs
// hammering the DB at once raced their writes badly enough that
// ClaimPendingPromptByID returned SQLITE_BUSY for one prompt; the
// pre-fix processPrompt logged the error and returned, the dispatcher's
// MarkPromptDone sweep cleared the still-pending row, and the user's
// prompt was silently lost. The handler now retries claim-by-id past
// any non-ErrNotFound error so a transient hiccup never drops the
// prompt — and the staging-area UI is guaranteed to receive
// prompt_started even after the bumpy start.
func TestTerminalHandler_TransientClaimErrorRetries(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindResult, Content: "ok"},
	}}
	h, ms := terminalTestHandler(runner, "conv-flaky")

	// First two ClaimPendingPromptByID calls return a transient error;
	// the third one (and beyond) falls through to the real claim. With
	// the retry loop the prompt should still reach the runner; without
	// it the very first failure swept the row to "" and the prompt was
	// dropped on the floor.
	var attempts atomic.Int32
	ms.ClaimErrFn = func(string) error {
		if attempts.Add(1) <= 2 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}

	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-flaky"})
	readMsg(t, ctx, conn) // init_ok

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "hi"})

	// The retry loop's backoff is 200ms per attempt; we expect ~400ms
	// of waiting before claim succeeds. Allow generous headroom.
	deadline := time.Now().Add(3 * time.Second)
	var sawStarted bool
	for time.Now().Before(deadline) && !sawStarted {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "prompt_started" {
			sawStarted = true
		}
	}
	if !sawStarted {
		t.Fatalf("never received prompt_started after transient claim errors (attempts=%d)", attempts.Load())
	}

	// The user prompt must reach the runner, which means the row was
	// claimed (transitioned to 'processing') instead of being swept to
	// '' before claim succeeded. Without the retry loop the original
	// SQLITE_BUSY would silently drop the prompt and no run would fire.
	if attempts.Load() < 3 {
		t.Fatalf("expected at least 3 claim attempts (2 fails + 1 success), got %d", attempts.Load())
	}
}

// newTestTerminalHandler builds a chat handler on a fresh runtime whose only
// backend is runner (the fallback for every provider), mirroring the old
// NewTerminalHandler(runner, store) shape most tests were written against.
func newTestTerminalHandler(runner agent.Backend, s store.Store) *TerminalHandler {
	return NewTerminalHandler(service.NewRuntime(s, service.NewBackendRegistry(nil, runner)))
}

func TestTerminalHandler_InitReplaysRememberedRateLimits(t *testing.T) {
	// A browser that never ran a turn on this account (new device, cleared
	// storage) still gets the quota badge: init replays the server's last
	// reading for the conversation's account.
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{
		ID:               "u1",
		ProviderBindings: map[string]string{"claude": "acc1"},
	})
	ms.Conversations = []store.Conversation{{ID: "conv-rl", UserID: "u1", Provider: "claude"}}
	h := newTestTerminalHandler(&testRunner{}, ms)
	h.Pool = service.NewPool(&config.Config{Providers: []config.Provider{{Name: "acc1", Type: "claude", MaxConcurrent: 1}}})
	reset := time.Now().Add(time.Hour).Unix()
	h.Pool.RecordRateLimit("acc1", `{"type":"five_hour","status":"allowed","resets_at":`+strconv.FormatInt(reset, 10)+`}`)

	var got []serverMessage
	state := connState{}
	h.handleInitMsg(
		context.Background(),
		clientMessage{Type: "init", ConversationID: "conv-rl", UserID: "u1", SubscriptionID: "sub-1"},
		&state,
		"browser",
		make(chan []byte, 16),
		func(m serverMessage) { got = append(got, m) },
	)
	var frames []serverMessage
	for _, m := range got {
		if m.Type == "rate_limit" {
			frames = append(frames, m)
		}
	}
	if len(frames) != 1 {
		t.Fatalf("want one rate_limit replay, got %+v", got)
	}
	if frames[0].SubscriptionID != "sub-1" || !strings.Contains(frames[0].Content.(string), `"observed_at"`) {
		t.Fatalf("unexpected replay frame: %+v", frames[0])
	}
}
