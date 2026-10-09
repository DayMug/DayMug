package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestPromptRunnerBroadcastErrorPersistsDisplayOnlyHistoryRow(t *testing.T) {
	ms := storetest.New()
	b := NewBroadcaster()
	ch := make(chan []byte, 1)
	b.Join("conv-1", "client-1", ch)
	runner := &PromptRunner{Store: ms, Broadcaster: b}

	runner.broadcastError("conv-1", "provider unavailable")

	msgs, err := ms.ListMessages(context.Background(), "conv-1", 10, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "error" || msgs[0].Content != "provider unavailable" {
		t.Fatalf("persisted message = %+v", msgs[0])
	}

	select {
	case raw := <-ch:
		var frame ServerMessage
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if frame.Type != "error" || frame.MessageID != msgs[0].ID {
			t.Fatalf("frame = %+v, want persisted message id %q", frame, msgs[0].ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for error frame")
	}
}

func TestStreamEventToMessage(t *testing.T) {
	tests := []struct {
		name     string
		evt      agent.StreamEvent
		wantType string
		wantOK   bool
	}{
		{"delta", agent.StreamEvent{Kind: agent.KindDelta, Content: "hi"}, "delta", true},
		{"result", agent.StreamEvent{Kind: agent.KindResult, Content: "done"}, "result", true},
		{"context_usage", agent.StreamEvent{Kind: agent.KindContextUsage, Content: "{}"}, "context_usage", true},
		{"system_init", agent.StreamEvent{Kind: agent.KindSystemInit, Content: "{}"}, "system_init", true},
		{"session_info", agent.StreamEvent{Kind: agent.KindSessionInfo, Content: "CAS resumed thread t1"}, "session_info", true},
		{"session_warning", agent.StreamEvent{Kind: agent.KindSessionWarning, Content: "fallback"}, "", false},
		{"tool_use_start", agent.StreamEvent{Kind: agent.KindToolUseStart, Content: "{}"}, "tool_use_start", true},
		{"tool_input_delta", agent.StreamEvent{Kind: agent.KindToolInputDelta, Content: "p"}, "tool_input_delta", true},
		{"tool_result", agent.StreamEvent{Kind: agent.KindToolResult, Content: "{}"}, "tool_result", true},
		// Usage is intentionally NOT mapped to a standalone WS message;
		// the caller embeds it into the matching `result` event's
		// metadata so the token chip renders synchronously with the
		// assistant message it describes.
		{"usage", agent.StreamEvent{Kind: agent.KindUsage, Content: "{}"}, "", false},
		{"thinking_delta", agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "hmm"}, "thinking_delta", true},
		{"user_question", agent.StreamEvent{Kind: agent.KindUserQuestion, Content: "{}"}, "user_question", true},
		{"user_question_resolved", agent.StreamEvent{Kind: agent.KindUserQuestionResolved, Content: "ask-1"}, "user_question_resolved", true},
		{"rate_limit", agent.StreamEvent{Kind: agent.KindRateLimit, Content: "{}"}, "rate_limit", true},
		{"unknown", agent.StreamEvent{Kind: "unknown_kind", Content: "x"}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := streamEventToMessage(tt.evt)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && msg.Type != tt.wantType {
				t.Errorf("type = %q, want %q", msg.Type, tt.wantType)
			}
			if ok && tt.evt.Kind != agent.KindToolResult && tt.evt.Kind != agent.KindUserQuestionResolved && msg.Content != tt.evt.Content {
				t.Errorf("content = %q, want %q", msg.Content, tt.evt.Content)
			}
			if tt.evt.Kind == agent.KindUserQuestionResolved && msg.RequestID != tt.evt.Content {
				t.Errorf("request id = %q, want %q", msg.RequestID, tt.evt.Content)
			}
		})
	}
}

func TestStreamEventToMessage_ToolResultUsesCompactJSONWireContent(t *testing.T) {
	payload := `{"name":"Bash","input":{"command":"` + strings.Repeat("x", 400) + `"},"content":"` + strings.Repeat("y", 400) + `"}`
	msg, ok := streamEventToMessage(agent.StreamEvent{Kind: agent.KindToolResult, Content: payload})
	if !ok {
		t.Fatal("tool_result was not mapped")
	}
	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var wire struct {
		Content struct {
			Name  string `json:"name"`
			Input struct {
				Command string `json:"command"`
			} `json:"input"`
			Content string `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("content did not marshal as object: %v (%s)", err, encoded)
	}
	if wire.Content.Input.Command != strings.Repeat("x", 50) {
		t.Fatalf("command = %q", wire.Content.Input.Command)
	}
	if wire.Content.Content != strings.Repeat("y", 50) {
		t.Fatalf("content = %q", wire.Content.Content)
	}
	if bytes.Contains(encoded, []byte("[omitted]")) {
		t.Fatalf("encoded content still contains omitted marker: %s", encoded)
	}
}

func TestStreamEventToMessage_PropagatesSubagentFlag(t *testing.T) {
	parent := agent.StreamEvent{Kind: agent.KindDelta, Content: "p"}
	parentMsg, ok := streamEventToMessage(parent)
	if !ok || parentMsg.Subagent {
		t.Fatalf("parent delta: ok=%v subagent=%v want ok=true subagent=false", ok, parentMsg.Subagent)
	}

	sub := agent.StreamEvent{Kind: agent.KindDelta, Content: "s", Subagent: true}
	subMsg, ok := streamEventToMessage(sub)
	if !ok || !subMsg.Subagent {
		t.Fatalf("subagent delta: ok=%v subagent=%v want ok=true subagent=true", ok, subMsg.Subagent)
	}
}

// TestApplyRateLimitCooldown verifies that only a genuine denial
// ("blocked"/"rejected") gates the account. The "allowed" heartbeat and the
// approaching-the-cap warnings still carry quota, so they must NOT cool the
// account down — locking a user out while they still have quota is the bug
// this guards against.
func TestApplyRateLimitCooldown(t *testing.T) {
	resets := time.Now().Add(time.Hour).Unix()

	cases := []struct {
		name        string
		status      string
		resetsAt    int64
		wantCooling bool
	}{
		{"allowed heartbeat keeps quota", "allowed", resets, false},
		{"allowed_warning keeps quota", "allowed_warning", resets, false},
		{"warning keeps quota", "warning", resets, false},
		{"empty status keeps quota", "", resets, false},
		{"unknown status keeps quota", "throttling", resets, false},
		{"blocked denies", "blocked", resets, true},
		{"rejected denies", "rejected", resets, true},
		{"case-insensitive Blocked denies", "Blocked", resets, true},
		{"denial without reset is ignored", "blocked", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := NewPool(&config.Config{
				Providers: []config.Provider{
					{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1},
				},
			})

			payload, err := json.Marshal(map[string]any{
				"status":    tc.status,
				"resets_at": tc.resetsAt,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			applyRateLimitCooldown(pool, "default", "", string(payload))

			_, cooling := pool.CooldownUntil("default", "")
			if cooling != tc.wantCooling {
				t.Fatalf("status %q: cooling=%v, want %v", tc.status, cooling, tc.wantCooling)
			}
		})
	}
}
