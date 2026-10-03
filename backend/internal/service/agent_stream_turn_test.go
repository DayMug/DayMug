package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// These tests pin how runOnce folds each kind of stream event into durable
// state and wire frames — usage attribution, session capture, sub-agent
// isolation, persistence failures, the error report and the single push
// notification — so the event loop can be restructured without re-deriving
// what it used to do.

// turnRecordingStore adds the usage-event recorder the Fake lacks and can fail
// message saves by role.
type turnRecordingStore struct {
	*storetest.Fake
	mu            sync.Mutex
	usageEvents   []store.UsageEvent
	contextUpdate []turnContextUpdate
	failRoles     map[string]bool
}

type turnContextUpdate struct {
	eventID     string
	used, total int64
}

func newTurnRecordingStore() *turnRecordingStore {
	return &turnRecordingStore{Fake: storetest.New()}
}

func (s *turnRecordingStore) RecordUsageEvent(_ context.Context, e store.UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageEvents = append(s.usageEvents, e)
	return nil
}

func (s *turnRecordingStore) UpdateUsageEventContext(_ context.Context, eventID string, used, total int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextUpdate = append(s.contextUpdate, turnContextUpdate{eventID: eventID, used: used, total: total})
	return nil
}

func (s *turnRecordingStore) SaveMessage(ctx context.Context, msg store.Message) error {
	if s.failRoles[msg.Role] {
		return errors.New("disk full")
	}
	return s.Fake.SaveMessage(ctx, msg)
}

func (s *turnRecordingStore) rows(convID, role string) []store.Message {
	var out []store.Message
	for _, m := range s.SnapshotMessages(convID) {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}

// failingScriptBackend replays a script and then ends the turn with runErr.
type failingScriptBackend struct {
	events []agent.StreamEvent
	runErr error
}

func (failingScriptBackend) Name() string                     { return "failing-script" }
func (failingScriptBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b failingScriptBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	for _, e := range b.events {
		ch <- e
	}
	close(ch)
	return b.runErr
}
func (failingScriptBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (failingScriptBackend) SessionExists(string, string, string) bool    { return false }
func (failingScriptBackend) SessionLogPath(string, string, string) string { return "" }

func framesOfType(frames []ServerMessage, typ string) []ServerMessage {
	var out []ServerMessage
	for _, f := range frames {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

func TestAgentStreamerAttributesUsageAcrossTheTurn(t *testing.T) {
	const convID = "conv-usage"
	tests := []struct {
		name   string
		origin string
		// wantFirstInstructions is the user-instruction count carried by the
		// turn's first usage event.
		wantFirstInstructions int64
	}{
		{name: "user prompt", wantFirstInstructions: 1},
		{name: "provider-initiated turn", origin: "wakeup", wantFirstInstructions: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newTurnRecordingStore()
			ms.Conversations = []store.Conversation{{ID: convID, UserID: "owner", SessionID: "sid-old"}}
			backend := frameScriptBackend{events: []agent.StreamEvent{
				{Kind: agent.KindSystemInit, Content: `{"model":"model-a","session_id":"sid-new"}`},
				{Kind: agent.KindToolUseStart, Content: `{"id":"t1"}`},
				{Kind: agent.KindToolResult, Content: `{"id":"t1","output":"a"}`},
				{Kind: agent.KindToolUseStart, Content: `{"id":"t2"}`},
				{Kind: agent.KindToolResult, Content: `{"id":"t2","output":"b"}`},
				{Kind: agent.KindUsage, Content: `{"input_tokens":10,"output_tokens":5}`},
				{Kind: agent.KindContextUsage, Content: `{"used":100,"total":1000}`},
				{Kind: agent.KindToolUseStart, Content: `{"id":"t3"}`},
				{Kind: agent.KindToolResult, Content: `{"id":"t3","output":"c"}`},
				{Kind: agent.KindUsage, Content: `{"input_tokens":20,"output_tokens":7}`},
				{Kind: agent.KindResult, Content: "done"},
			}}
			var frames []ServerMessage
			streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
			out := streamer.Run(context.Background(), AgentStreamRequest{
				Backend: backend, WorkDir: t.TempDir(), ConversationID: convID, OwnerID: "owner",
				Opts:         agent.RunRequest{SessionID: "sid-old"},
				ResultOrigin: tt.origin,
				Broadcast:    func(msg ServerMessage) { frames = append(frames, msg) },
			})
			if out.Err != nil {
				t.Fatalf("run error = %v", out.Err)
			}
			if out.SessionID != "sid-new" {
				t.Errorf("result session id = %q, want sid-new", out.SessionID)
			}
			if conv, _ := ms.GetConversation(context.Background(), convID); conv.SessionID != "sid-new" {
				t.Errorf("stored session id = %q, want the reported sid-new", conv.SessionID)
			}

			if len(ms.usageEvents) != 2 {
				t.Fatalf("usage events = %+v, want two", ms.usageEvents)
			}
			first, second := ms.usageEvents[0], ms.usageEvents[1]
			if first.Model != "model-a" || second.Model != "model-a" {
				t.Errorf("usage models = %q/%q, want the system_init model", first.Model, second.Model)
			}
			if first.ToolCallCount != 2 || second.ToolCallCount != 1 {
				t.Errorf("tool calls = %d/%d, want each event to count only its own tools (2/1)",
					first.ToolCallCount, second.ToolCallCount)
			}
			if first.UserInstructionCount != tt.wantFirstInstructions || second.UserInstructionCount != 0 {
				t.Errorf("user instructions = %d/%d, want %d/0",
					first.UserInstructionCount, second.UserInstructionCount, tt.wantFirstInstructions)
			}
			if first.ContextUsedTokens != 0 || second.ContextUsedTokens != 100 || second.ContextWindowTokens != 1000 {
				t.Errorf("context tokens = %d, %d/%d, want the window reported between the two usage frames on the second",
					first.ContextUsedTokens, second.ContextUsedTokens, second.ContextWindowTokens)
			}
			if len(ms.contextUpdate) != 1 || ms.contextUpdate[0] != (turnContextUpdate{eventID: first.EventID, used: 100, total: 1000}) {
				t.Errorf("context updates = %+v, want the first usage event back-filled once", ms.contextUpdate)
			}

			// Usage never ships as its own frame; it rides on the result.
			if got := framesOfType(frames, "usage"); len(got) != 0 {
				t.Errorf("usage frames = %+v, want none", got)
			}
			results := framesOfType(frames, "result")
			if len(results) != 1 {
				t.Fatalf("result frames = %+v, want one", results)
			}
			var meta struct {
				Model  string          `json:"model"`
				Usage  json.RawMessage `json:"usage"`
				Origin string          `json:"origin"`
			}
			if err := json.Unmarshal(results[0].Metadata, &meta); err != nil {
				t.Fatalf("decode result metadata %s: %v", results[0].Metadata, err)
			}
			if meta.Model != "model-a" || !strings.Contains(string(meta.Usage), `"input_tokens":20`) || meta.Origin != tt.origin {
				t.Errorf("result metadata = %s, want model-a, the last usage payload and origin %q", results[0].Metadata, tt.origin)
			}
			rows := ms.rows(convID, "assistant")
			if len(rows) != 1 || rows[0].ID != results[0].MessageID || string(rows[0].Metadata) != string(results[0].Metadata) {
				t.Errorf("assistant rows = %+v, want one row matching the result frame", rows)
			}
			if tools := ms.rows(convID, "tool"); len(tools) != 3 {
				t.Errorf("tool rows = %d, want 3", len(tools))
			}
		})
	}
}

func TestAgentStreamerKeepsSubagentOutOfTheParentTranscript(t *testing.T) {
	const convID = "conv-subagent"
	for _, forward := range []bool{true, false} {
		name := "forwarded"
		if !forward {
			name = "dropped"
		}
		t.Run(name, func(t *testing.T) {
			ms := newTurnRecordingStore()
			pool := NewPool(&config.Config{Providers: []config.Provider{{Name: "acc", Type: config.CLITypeClaude}}})
			resetsAt := time.Now().Add(time.Hour).Unix()
			backend := frameScriptBackend{events: []agent.StreamEvent{
				{Kind: agent.KindDelta, Content: "worker chatter", Subagent: true},
				{Kind: agent.KindToolUseStart, Content: `{"id":"w1"}`, Subagent: true},
				{Kind: agent.KindToolResult, Content: `{"id":"w1","output":"x"}`, Subagent: true},
				{Kind: agent.KindResult, Content: "worker answer", Subagent: true},
				{Kind: agent.KindRateLimit, Content: `{"status":"rejected","resets_at":` + jsonInt(resetsAt) + `}`, Subagent: true},
				{Kind: agent.KindDelta, Content: "parent"},
				{Kind: agent.KindResult, Content: "parent reply"},
			}}
			var frames []ServerMessage
			var observed []agent.StreamEvent
			streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster(), Pool: pool}
			out := streamer.Run(context.Background(), AgentStreamRequest{
				Backend: backend, WorkDir: t.TempDir(), ConversationID: convID, AccountName: "acc",
				Opts:                  agent.RunRequest{Model: "model-a"},
				ForwardSubagentFrames: forward,
				Broadcast:             func(msg ServerMessage) { frames = append(frames, msg) },
				OnFrame:               func(f AgentStreamFrame) { observed = append(observed, f.Event) },
			})
			if out.Content != "parent reply" {
				t.Errorf("content = %q, want the parent's reply only", out.Content)
			}
			if got := assistantContents(t, ms.Fake, convID); !equalStrings(got, []string{"parent reply"}) {
				t.Errorf("assistant rows = %v, want only the parent reply", got)
			}
			if tools := ms.rows(convID, "tool"); len(tools) != 0 {
				t.Errorf("tool rows = %+v, want sub-agent tools left unpersisted", tools)
			}
			if _, cooling := pool.CooldownUntil("acc", "model-a"); !cooling {
				t.Error("a sub-agent rate-limit denial did not cool the account down")
			}
			for _, evt := range observed {
				if evt.Subagent {
					t.Errorf("OnFrame saw sub-agent event %+v", evt)
				}
			}
			var subFrames int
			for _, f := range frames {
				if f.Subagent {
					subFrames++
					if f.MessageID != "" {
						t.Errorf("sub-agent frame %+v carries a persisted id", f)
					}
				}
			}
			if forward && subFrames != 5 {
				t.Errorf("sub-agent frames = %d, want all 5 forwarded", subFrames)
			}
			if !forward && subFrames != 0 {
				t.Errorf("sub-agent frames = %d, want none", subFrames)
			}
		})
	}
}

func jsonInt(v int64) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestAgentStreamerReportsPersistFailures(t *testing.T) {
	const convID = "conv-persist-fail"
	ms := newTurnRecordingStore()
	ms.failRoles = map[string]bool{"tool": true, "assistant": true}
	backend := frameScriptBackend{events: []agent.StreamEvent{
		{Kind: agent.KindToolUseStart, Content: `{"id":"t1"}`},
		{Kind: agent.KindToolResult, Content: `{"id":"t1","output":"a"}`},
		{Kind: agent.KindResult, Content: "reply"},
	}}
	var frames []ServerMessage
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend: backend, WorkDir: t.TempDir(), ConversationID: convID,
		Broadcast: func(msg ServerMessage) { frames = append(frames, msg) },
	})
	if out.Err != nil {
		t.Fatalf("run error = %v, want a web turn to finish despite a failed save", out.Err)
	}
	var types []string
	for _, f := range frames {
		types = append(types, f.Type)
	}
	want := []string{"tool_use_start", "persist_failed", "tool_result", "persist_failed", "result"}
	if !equalStrings(types, want) {
		t.Fatalf("frame types = %v, want %v", types, want)
	}
	if frames[1].Message != "tool result not saved — your view may be stale after a refresh" ||
		frames[3].Message != "assistant reply not saved — your view may be stale after a refresh" {
		t.Errorf("persist_failed messages = %q / %q", frames[1].Message, frames[3].Message)
	}
	if frames[2].MessageID != "" || frames[4].MessageID != "" {
		t.Errorf("frames carry ids for rows that were never saved: %+v / %+v", frames[2], frames[4])
	}
}

func TestAgentStreamerReportsRunErrors(t *testing.T) {
	const convID = "conv-run-error"
	runErr := errors.New("exit status 1: boom")
	tests := []struct {
		name string
		// cancel ends the caller's context before the run returns.
		cancel     bool
		retry      bool
		deltas     []string
		wantReport bool
		// wantPartial is the assistant row the interrupted turn leaves behind.
		wantPartial []string
	}{
		{name: "failure is persisted and broadcast", wantReport: true},
		{name: "cancelled turn stays quiet", cancel: true},
		{name: "retried attempt stays quiet", retry: true},
		{name: "partial reply survives the failure", deltas: []string{"half ", "done"}, wantReport: true, wantPartial: []string{"half done"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newTurnRecordingStore()
			var events []agent.StreamEvent
			for _, d := range tt.deltas {
				events = append(events, agent.StreamEvent{Kind: agent.KindDelta, Content: d})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				cancel()
			}
			var frames []ServerMessage
			streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
			req := AgentStreamRequest{
				Backend: failingScriptBackend{events: events, runErr: runErr}, WorkDir: t.TempDir(), ConversationID: convID,
				Broadcast: func(msg ServerMessage) { frames = append(frames, msg) },
			}
			if tt.retry {
				req.willRetryTransient = func(error, bool) bool { return true }
			}
			out := streamer.runOnce(ctx, req)
			if !errors.Is(out.Err, runErr) {
				t.Errorf("result error = %v, want the run error", out.Err)
			}
			if out.retryTransient != tt.retry {
				t.Errorf("retryTransient = %v, want %v", out.retryTransient, tt.retry)
			}
			errFrames := framesOfType(frames, "error")
			errRows := ms.rows(convID, "error")
			if tt.wantReport {
				if len(errFrames) != 1 || errFrames[0].Message != runErr.Error() ||
					len(errRows) != 1 || errRows[0].ID != errFrames[0].MessageID {
					t.Errorf("error frames = %+v, rows = %+v, want one persisted report", errFrames, errRows)
				}
			} else if len(errFrames) != 0 || len(errRows) != 0 {
				t.Errorf("error frames = %+v, rows = %+v, want none", errFrames, errRows)
			}
			if got := assistantContents(t, ms.Fake, convID); !equalStrings(got, tt.wantPartial) {
				t.Errorf("assistant rows = %v, want %v", got, tt.wantPartial)
			}
			// An interrupted web turn ships no result frame; clients pick the
			// partial reply up over REST.
			if got := framesOfType(frames, "result"); len(got) != 0 {
				t.Errorf("result frames = %+v, want none", got)
			}
			if out.produced != (len(tt.deltas) > 0) {
				t.Errorf("produced = %v, want %v", out.produced, len(tt.deltas) > 0)
			}
		})
	}
}

func TestAgentStreamerNotifiesOncePerTurnWithTheFinalReply(t *testing.T) {
	const convID = "conv-notify-once"
	ms := storetest.New()
	ms.Users = []store.User{{ID: "owner", Username: "owner", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{{ID: convID, UserID: "owner", Title: "t", NotificationsEnabled: true}}
	bark := &questionBarkCapture{calls: make(chan questionNotificationCall, 4)}
	streamer := &AgentStreamer{
		Store: ms, Broadcaster: NewBroadcaster(),
		Persist: &MessagePersister{Store: ms, BarkSender: bark},
	}
	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        multiResultBackend{results: []string{"step one", "step two"}},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Notify:         true,
	})
	if out.Err != nil {
		t.Fatalf("run error = %v", out.Err)
	}
	select {
	case call := <-bark.calls:
		if !strings.Contains(call.body, "step two") || strings.Contains(call.body, "step one") {
			t.Errorf("notification body = %q, want the final reply only", call.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no completion notification")
	}
	select {
	case call := <-bark.calls:
		t.Fatalf("second notification for one turn: %+v", call)
	case <-time.After(200 * time.Millisecond):
	}
}

// A prompt queued while the turn ran means the user's task continues; only
// the turn that ends the chain notifies.
func TestAgentStreamerSkipsNotificationWhenAFollowUpIsQueued(t *testing.T) {
	const convID = "conv-notify-followup"
	ms := storetest.New()
	ms.Users = []store.User{{ID: "owner", Username: "owner", BarkURL: "https://bark.example/key"}}
	ms.Conversations = []store.Conversation{{ID: convID, UserID: "owner", Title: "t", NotificationsEnabled: true}}
	if _, err := ms.EnqueuePrompt(context.Background(), convID, "next"); err != nil {
		t.Fatalf("EnqueuePrompt: %v", err)
	}
	bark := &questionBarkCapture{calls: make(chan questionNotificationCall, 4)}
	streamer := &AgentStreamer{
		Store: ms, Broadcaster: NewBroadcaster(),
		Persist: &MessagePersister{Store: ms, BarkSender: bark},
	}
	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        multiResultBackend{results: []string{"done"}},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Notify:         true,
	})
	if out.Err != nil {
		t.Fatalf("run error = %v", out.Err)
	}
	select {
	case call := <-bark.calls:
		t.Fatalf("notification sent with a follow-up queued: %+v", call)
	case <-time.After(200 * time.Millisecond):
	}
}

// OnFrame exposes the accumulated buffers so a transport can render progress
// without keeping its own copy; the reply buffer restarts after each persisted
// result in per-result mode.
func TestAgentStreamerOnFrameCarriesAccumulatedText(t *testing.T) {
	tests := []struct {
		name       string
		mode       AgentStreamMode
		wantResult []string
		wantReply  []string
	}{
		{
			name:       "per-result",
			mode:       AgentStreamPerResult,
			wantReply:  []string{"", "a", "ab", "", "c"},
			wantResult: []string{"", "", "", "", ""},
		},
		{
			name:       "aggregate",
			mode:       AgentStreamAggregate,
			wantReply:  []string{"", "a", "ab", "ab", "abc"},
			wantResult: []string{"", "", "", "ab", "ab"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := frameScriptBackend{events: []agent.StreamEvent{
				{Kind: agent.KindThinkingDelta, Content: "hmm"},
				{Kind: agent.KindDelta, Content: "a"},
				{Kind: agent.KindDelta, Content: "b"},
				{Kind: agent.KindResult, Content: "ab"},
				{Kind: agent.KindDelta, Content: "c"},
			}}
			var got []AgentStreamFrame
			streamer := &AgentStreamer{Store: storetest.New(), Broadcaster: NewBroadcaster()}
			streamer.Run(context.Background(), AgentStreamRequest{
				Backend: backend, WorkDir: t.TempDir(), ConversationID: "conv-onframe", Mode: tt.mode,
				OnFrame: func(f AgentStreamFrame) { got = append(got, f) },
			})
			if len(got) != 5 {
				t.Fatalf("frames = %d, want 5", len(got))
			}
			var reply, result []string
			for _, f := range got {
				reply = append(reply, f.Reply)
				result = append(result, f.Result)
			}
			if !equalStrings(reply, tt.wantReply) {
				t.Errorf("reply = %q, want %q", reply, tt.wantReply)
			}
			if !equalStrings(result, tt.wantResult) {
				t.Errorf("result = %q, want %q", result, tt.wantResult)
			}
			if got[0].Thinking != "hmm" {
				t.Errorf("thinking = %q, want hmm", got[0].Thinking)
			}
		})
	}
}

// A resumed Claude session reports running totals — sub-agent models included
// in per_model — so the result metadata must carry the turn's own billed cost
// next to the conversation's running total, not the raw cumulative figure.
func TestAgentStreamerStampsTurnAndSessionCost(t *testing.T) {
	ctx := context.Background()
	s := newMessagePersistTestStore(t)
	if err := s.CreateUser(ctx, store.User{ID: "owner", Name: "u"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	const convID = "conv-cost"
	if err := s.CreateConversation(ctx, convID, "", "owner", t.TempDir(), config.CLITypeClaude, "opus"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SetSessionID(ctx, convID, "sid"); err != nil {
		t.Fatalf("set session: %v", err)
	}
	cumulative := func(turnID string, perModel map[string]float64) agent.StreamEvent {
		report := agent.UsageReport{InputTokens: 1, OutputTokens: 1, Cumulative: true, PerModel: map[string]agent.ModelUsage{}}
		var total float64
		for model, cost := range perModel {
			report.PerModel[model] = agent.ModelUsage{InputTokens: int64(cost * 1000), OutputTokens: 1, CostUSD: agent.USD(cost)}
			total += cost
		}
		report.TotalCostUSD = agent.USD(total)
		evt, err := agent.NewUsageEvent(report)
		if err != nil {
			t.Fatalf("usage event: %v", err)
		}
		evt.TurnID = turnID
		return evt
	}
	runTurn := func(usage agent.StreamEvent) map[string]any {
		t.Helper()
		var frames []ServerMessage
		streamer := &AgentStreamer{Store: s, Broadcaster: NewBroadcaster()}
		out := streamer.Run(ctx, AgentStreamRequest{
			Backend: frameScriptBackend{events: []agent.StreamEvent{
				{Kind: agent.KindSystemInit, Content: `{"model":"opus","session_id":"sid"}`},
				usage,
				{Kind: agent.KindResult, Content: "done"},
			}},
			WorkDir: t.TempDir(), ConversationID: convID, OwnerID: "owner",
			Opts:      agent.RunRequest{SessionID: "sid"},
			Broadcast: func(msg ServerMessage) { frames = append(frames, msg) },
		})
		if out.Err != nil {
			t.Fatalf("run error = %v", out.Err)
		}
		results := framesOfType(frames, "result")
		if len(results) != 1 {
			t.Fatalf("result frames = %+v, want one", results)
		}
		var meta struct {
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal(results[0].Metadata, &meta); err != nil {
			t.Fatalf("decode metadata %s: %v", results[0].Metadata, err)
		}
		return meta.Usage
	}
	near := func(got any, want float64) bool {
		f, ok := got.(float64)
		return ok && math.Abs(f-want) < 1e-9
	}

	first := runTurn(cumulative("turn-1", map[string]float64{"opus": 1}))
	if !near(first["turn_cost_usd"], 1) || !near(first["session_cost_usd"], 1) {
		t.Errorf("first turn usage = %v, want turn 1 / session 1", first)
	}
	second := runTurn(cumulative("turn-2", map[string]float64{"opus": 1.4, "haiku": 0.2}))
	if !near(second["turn_cost_usd"], 0.6) || !near(second["session_cost_usd"], 1.6) {
		t.Errorf("second turn usage = %v, want turn 0.6 (sub-agent included) / session 1.6", second)
	}
	if !near(second["total_cost_usd"], 1.6) {
		t.Errorf("total_cost_usd = %v, want the provider's figure untouched", second["total_cost_usd"])
	}
}

func TestWithContextWindowOverridesTheReportedTotal(t *testing.T) {
	tests := []struct {
		name, in string
		window   int
		want     string
	}{
		{"undeclared window passes through", `{"used":10,"total":200000}`, 0, `{"used":10,"total":200000}`},
		{"declared window wins", `{"cache_read":5,"total":200000,"used":10}`, 131072, `{"cache_read":5,"total":131072,"used":10}`},
		{"usage is capped at the declared window", `{"total":1000000,"used":300000}`, 131072, `{"total":131072,"used":131072}`},
		{"unparseable frame is left alone", `nope`, 131072, `nope`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withContextWindow(tt.in, tt.window); got != tt.want {
				t.Fatalf("withContextWindow = %s, want %s", got, tt.want)
			}
		})
	}
}
