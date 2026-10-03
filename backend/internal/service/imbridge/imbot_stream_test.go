package imbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// The three tests below pin the behaviours an IM run only acquired once both
// transports were folded into service.AgentStreamer: token billing, rate-limit
// cooldowns, and consistent session continuity after Stop. Before the merge an
// IM turn burned tokens no bill ever saw or kept hammering a limited account;
// now it also follows the web contract that Stop preserves provider context.

// usageIMBackend emits the frame order a real CLI uses: system_init names the
// model, usage reports the turn's tokens, result closes it out.
type usageIMBackend struct {
	model string
	usage string
}

func (usageIMBackend) Name() string                     { return "usage-im" }
func (usageIMBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b usageIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindSystemInit, Content: `{"model":"` + b.model + `"}`}
	ch <- agent.StreamEvent{Kind: agent.KindUsage, Content: b.usage}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "巡检完成"}
	close(ch)
	return nil
}
func (usageIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "巡检完成", nil
}
func (usageIMBackend) SessionExists(string, string, string) bool    { return false }
func (usageIMBackend) SessionLogPath(string, string, string) string { return "" }

// waitForTokenUsage polls because PersistTokenUsage writes from its own
// goroutine — the stream loop must never block on the billing round-trip.
func waitForTokenUsage(t *testing.T, ms *storetest.Fake) []store.TokenUsageRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ms.QueueMu.Lock()
		rows := append([]store.TokenUsageRecord(nil), ms.TokenUsage...)
		ms.QueueMu.Unlock()
		if len(rows) > 0 {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatal("IM run wrote no token usage row; the turn's tokens are unbilled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestIMRunBillsTokenUsageToTheAgentOwner(t *testing.T) {
	const (
		ownerID = "agent-1"
		model   = "claude-im-model"
	)
	ms := storetest.New()
	bridge := &IMBridge{Runtime: &service.Runtime{Store: ms}}

	usage := `{"input_tokens":1200,"output_tokens":340,` +
		`"cache_read_input_tokens":90,"cache_creation_input_tokens":10,` +
		`"total_cost_usd":0.4213}`
	result, _, _, err := bridge.runAndStream(
		context.Background(), usageIMBackend{model: model, usage: usage},
		"prompt", t.TempDir(), ownerID, "acc1", agent.RunRequest{},
		"conversation", imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"},
		&imbottest.ReplyRecorder{},
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result != "巡检完成" {
		t.Fatalf("result = %q, want the agent reply", result)
	}

	rows := waitForTokenUsage(t, ms)
	if len(rows) != 1 {
		t.Fatalf("token usage rows = %+v, want exactly one", rows)
	}
	got := rows[0]
	want := store.TokenUsageRecord{
		UserID:                   ownerID,
		Model:                    model,
		Day:                      got.Day,
		InputTokens:              1200,
		OutputTokens:             340,
		CacheReadInputTokens:     90,
		CacheCreationInputTokens: 10,
		CostUSD:                  0.4213,
		Turns:                    1,
	}
	if got != want {
		t.Fatalf("token usage row = %+v, want %+v", got, want)
	}
}

// rateLimitIMBackend reports the account's quota as exhausted mid-stream, then
// still finishes the turn — exactly what the CLI does when the window closes
// on a request it had already started.
type rateLimitIMBackend struct {
	payload string
}

func (rateLimitIMBackend) Name() string                     { return "rate-limit-im" }
func (rateLimitIMBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b rateLimitIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindRateLimit, Content: b.payload}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "已回复"}
	close(ch)
	return nil
}
func (rateLimitIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "已回复", nil
}
func (rateLimitIMBackend) SessionExists(string, string, string) bool    { return false }
func (rateLimitIMBackend) SessionLogPath(string, string, string) string { return "" }

func TestIMRunCoolsDownRateLimitedAccount(t *testing.T) {
	const (
		accountName = "acc1"
		limited     = "claude-limited"
		other       = "claude-other"
	)
	useAccountModels(t, map[string][]string{accountName: {limited, other}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: accountName, Type: config.CLITypeClaude, MaxConcurrent: 2,
	}}}
	pool := service.NewPool(cfg)
	bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New(), Cfg: cfg, Pool: pool}}

	resetsAt := time.Unix(time.Now().Add(45*time.Minute).Unix(), 0)
	payload := `{"status":"blocked","resets_at":` + strconv.FormatInt(resetsAt.Unix(), 10) + `}`

	if _, _, _, err := bridge.runAndStream(
		context.Background(), rateLimitIMBackend{payload: payload},
		"prompt", t.TempDir(), "agent-1", accountName,
		agent.RunRequest{Model: limited},
		"conversation", imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"},
		&imbottest.ReplyRecorder{},
	); err != nil {
		t.Fatalf("run: %v", err)
	}

	until, cooling := pool.CooldownUntil(accountName, limited)
	if !cooling {
		t.Fatalf("account %q model %q was not cooled down after a denial; IM keeps retrying into a live limit", accountName, limited)
	}
	if !until.Equal(resetsAt) {
		t.Fatalf("cooldown until = %s, want the reported reset time %s", until, resetsAt)
	}
	if _, cooling := pool.CooldownUntil(accountName, other); cooling {
		t.Fatalf("model %q was cooled down too; a denial must not gate a model that still has quota", other)
	}

	// Enter must now refuse the limited model with the typed cooldown error the
	// IM run surfaces to the thread.
	_, err := pool.EnterForUser("", accountName, limited)
	var cd *service.CooldownError
	if !errors.As(err, &cd) {
		t.Fatalf("pool.Enter error = %v, want *service.CooldownError", err)
	}
}

// sessionLogIMBackend appends this turn's line to the session JSONL the moment
// it starts — the real CLI writes the user/assistant lines before the API
// roundtrip — so a rollback has something concrete to undo.
type sessionLogIMBackend struct {
	path    string
	started chan struct{}
	block   bool
}

func (*sessionLogIMBackend) Name() string                     { return "session-log-im" }
func (*sessionLogIMBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *sessionLogIMBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	f, err := os.OpenFile(b.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		close(ch)
		return err
	}
	if _, err := f.WriteString(sessionLogTurnLine); err != nil {
		_ = f.Close()
		close(ch)
		return err
	}
	if err := f.Close(); err != nil {
		close(ch)
		return err
	}
	if b.started != nil {
		b.started <- struct{}{}
	}
	if b.block {
		<-ctx.Done()
		close(ch)
		return errors.New("signal: terminated")
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "完成"}
	close(ch)
	return nil
}
func (*sessionLogIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "完成", nil
}
func (*sessionLogIMBackend) SessionExists(string, string, string) bool { return false }
func (b *sessionLogIMBackend) SessionLogPath(string, string, string) string {
	return b.path
}

const (
	sessionLogPriorTurn = "{\"role\":\"user\",\"text\":\"上一轮\"}\n"
	sessionLogTurnLine  = "{\"role\":\"user\",\"text\":\"被取消的这一轮\"}\n"
)

func TestIMRunPreservesSessionLogWhenCancelled(t *testing.T) {
	const conversationID = "conversation"

	newLog := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, []byte(sessionLogPriorTurn), 0o600); err != nil {
			t.Fatalf("seed session log: %v", err)
		}
		return path
	}

	t.Run("cancelled turn stays in the session log", func(t *testing.T) {
		path := newLog(t)
		broadcaster := service.NewBroadcaster()
		bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New(), Broadcaster: broadcaster}}
		backend := &sessionLogIMBackend{path: path, started: make(chan struct{}), block: true}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if !broadcaster.StartJob(conversationID, cancel) {
			t.Fatal("StartJob refused a fresh conversation")
		}
		defer broadcaster.EndJob(conversationID)

		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _, _ = bridge.runAndStream(
				ctx, backend, "prompt", t.TempDir(), "agent-1", "acc1",
				agent.RunRequest{SessionID: "session-1"}, conversationID,
				imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"},
				&imbottest.ReplyRecorder{},
			)
		}()

		select {
		case <-backend.started:
		case <-time.After(2 * time.Second):
			t.Fatal("backend never started")
		}
		// The CLI has already appended this turn; Stop must retain it for the
		// next message in the same conversation.
		if got := readFile(t, path); got != sessionLogPriorTurn+sessionLogTurnLine {
			t.Fatalf("session log before cancel = %q, want the appended turn", got)
		}
		if !broadcaster.CancelJob(conversationID) {
			t.Fatal("CancelJob found no registered job")
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("IM run did not finish after cancel")
		}

		if got := readFile(t, path); got != sessionLogPriorTurn+sessionLogTurnLine {
			t.Fatalf("session log after cancel = %q, want the stopped turn preserved", got)
		}
	})

	t.Run("completed turn keeps its session log", func(t *testing.T) {
		path := newLog(t)
		broadcaster := service.NewBroadcaster()
		bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New(), Broadcaster: broadcaster}}
		backend := &sessionLogIMBackend{path: path}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if !broadcaster.StartJob(conversationID, cancel) {
			t.Fatal("StartJob refused a fresh conversation")
		}
		defer broadcaster.EndJob(conversationID)

		if _, _, _, err := bridge.runAndStream(
			ctx, backend, "prompt", t.TempDir(), "agent-1", "acc1",
			agent.RunRequest{SessionID: "session-1"}, conversationID,
			imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"},
			&imbottest.ReplyRecorder{},
		); err != nil {
			t.Fatalf("run: %v", err)
		}

		if got := readFile(t, path); got != sessionLogPriorTurn+sessionLogTurnLine {
			t.Fatalf("session log after a clean run = %q, want the turn preserved", got)
		}
	})
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "<missing>"
		}
		t.Fatalf("read session log: %v", err)
	}
	return string(data)
}
