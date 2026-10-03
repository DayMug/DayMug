package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func modelCall(subagent bool) agent.StreamEvent {
	return agent.StreamEvent{Kind: agent.KindModelCall, Content: `{}`, Subagent: subagent}
}

func toolCall(id string) agent.StreamEvent {
	return agent.StreamEvent{Kind: agent.KindToolUseStart, Content: `{"name":"Read","id":"` + id + `"}`}
}

func toolResult(id string) agent.StreamEvent {
	return agent.StreamEvent{Kind: agent.KindToolResult, Content: `{"id":"` + id + `","content":"ok"}`}
}

func TestTaskGuardrailRecordsThresholdWithoutInterrupting(t *testing.T) {
	guard := newTaskGuardrail(config.GuardrailsConfig{
		MaxModelCallsPerTurn: 2,
		MaxToolCallsPerTurn:  2,
	})
	for i := 0; i < 4; i++ {
		guard.observe(modelCall(false))
		guard.observe(toolCall(string(rune('a' + i))))
		guard.observe(toolResult(string(rune('a' + i))))
	}

	snapshot := guard.reminder()
	if snapshot == nil {
		t.Fatal("completed task did not produce a reminder")
	}
	if snapshot.ModelCalls != 4 || snapshot.ToolCalls != 4 {
		t.Fatalf("snapshot counts = model:%d tool:%d, want 4/4", snapshot.ModelCalls, snapshot.ToolCalls)
	}
	if !strings.Contains(snapshot.Trigger, "模型请求次数") || !strings.Contains(snapshot.Trigger, "工具调用次数") {
		t.Fatalf("snapshot trigger = %q", snapshot.Trigger)
	}
}

func TestTaskGuardrailIgnoresSubagentCalls(t *testing.T) {
	guard := newTaskGuardrail(config.GuardrailsConfig{
		MaxModelCallsPerTurn: 1,
		MaxToolCallsPerTurn:  1,
	})
	for i := 0; i < 10; i++ {
		tool := toolCall("sub")
		tool.Subagent = true
		guard.observe(modelCall(true))
		guard.observe(tool)
	}
	if snapshot := guard.reminder(); snapshot != nil {
		t.Fatalf("sub-agent activity produced reminder: %+v", snapshot)
	}

	guard.observe(modelCall(false))
	guard.observe(toolCall("parent"))
	if snapshot := guard.reminder(); snapshot == nil || snapshot.ModelCalls != 1 || snapshot.ToolCalls != 1 {
		t.Fatalf("parent activity reminder = %+v", snapshot)
	}
}

func TestTaskGuardrailReminderIncludesSettledCost(t *testing.T) {
	guard := newTaskGuardrail(config.GuardrailsConfig{MaxCostUSDPerTurn: 0.25})
	guard.setConversationCost(1.2)
	guard.addCost(0.25)

	snapshot := guard.reminder()
	if snapshot == nil || !strings.Contains(snapshot.Prompt, "本会话已记录成本 $1.4500") {
		t.Fatalf("cost reminder = %+v", snapshot)
	}
	if !strings.Contains(snapshot.Prompt, "本条消息仍会正常执行") {
		t.Fatalf("reminder does not explain non-blocking behavior: %q", snapshot.Prompt)
	}
}

func TestGuardrailReminderTrackerConsumesOncePerConversation(t *testing.T) {
	tracker := NewGuardrailReminderTracker()
	tracker.Put("conv-a", &GuardrailSnapshot{Prompt: "remember me"})
	tracker.Put("conv-b", &GuardrailSnapshot{Prompt: "other"})

	got, ok := tracker.Take("conv-a")
	if !ok || got.Prompt != "remember me" {
		t.Fatalf("first take = %+v, %v", got, ok)
	}
	if _, ok := tracker.Take("conv-a"); ok {
		t.Fatal("reminder was delivered more than once")
	}
	if got, ok := tracker.Take("conv-b"); !ok || got.Prompt != "other" {
		t.Fatalf("conversation isolation failed: %+v, %v", got, ok)
	}
}

type guardrailBackend struct {
	mu   sync.Mutex
	runs int
}

func (*guardrailBackend) Name() string                     { return "guardrail-script" }
func (*guardrailBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *guardrailBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.mu.Lock()
	b.runs++
	b.mu.Unlock()
	defer close(ch)
	for _, evt := range []agent.StreamEvent{
		modelCall(false),
		{Kind: agent.KindDelta, Content: "partial"},
		modelCall(false),
		{Kind: agent.KindResult, Content: "done"},
	} {
		select {
		case ch <- evt:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (*guardrailBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*guardrailBackend) SessionExists(string, string, string) bool    { return false }
func (*guardrailBackend) SessionLogPath(string, string, string) string { return "" }

func (b *guardrailBackend) runCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.runs
}

func TestAgentStreamerRemindsOnlyWhenNextUserMessageStarts(t *testing.T) {
	tracker := NewGuardrailReminderTracker()
	backend := &guardrailBackend{}
	streamer := &AgentStreamer{
		Store:              storetest.New(),
		Guardrails:         config.GuardrailsConfig{MaxModelCallsPerTurn: 1},
		GuardrailReminders: tracker,
	}
	var frames []ServerMessage
	request := func(prompt string) AgentStreamRequest {
		return AgentStreamRequest{
			Backend: backend, Prompt: prompt, ConversationID: "conv-reminder",
			Mode: AgentStreamAggregate, UserInitiated: true,
			Broadcast: func(msg ServerMessage) { frames = append(frames, msg) },
		}
	}

	first := streamer.Run(context.Background(), request("first"))
	if first.Err != nil || first.Content != "done" || backend.runCount() != 1 {
		t.Fatalf("first turn was interrupted: content=%q err=%v runs=%d", first.Content, first.Err, backend.runCount())
	}
	for _, frame := range frames {
		if frame.Type == "user_question" {
			t.Fatalf("active turn asked a guardrail question: %+v", frame)
		}
		if frame.Type == "session_warning" && strings.Contains(frame.Message, "上一条消息") {
			t.Fatalf("first turn displayed its own reminder: %+v", frame)
		}
	}

	frames = nil
	second := streamer.Run(context.Background(), request("second"))
	if second.Err != nil || second.Content != "done" || backend.runCount() != 2 {
		t.Fatalf("second turn did not run normally: content=%q err=%v runs=%d", second.Content, second.Err, backend.runCount())
	}
	var reminders int
	for _, frame := range frames {
		if frame.Type == "user_question" {
			t.Fatalf("reminder used a question frame: %+v", frame)
		}
		if frame.Type == "session_warning" && strings.Contains(frame.Message, "上一条消息") {
			reminders++
			var meta struct {
				NoticeType string `json:"notice_type"`
				Notice     Notice `json:"notice"`
			}
			if err := json.Unmarshal(frame.Metadata, &meta); err != nil {
				t.Fatalf("reminder metadata %q: %v", frame.Metadata, err)
			}
			if meta.NoticeType != "session_warning" || meta.Notice.Kind != NoticeKindTaskGuardrail ||
				!reflect.DeepEqual(meta.Notice.Triggers, []string{GuardrailTriggerModelCalls}) || meta.Notice.ModelCalls < 1 {
				t.Fatalf("reminder metadata = %+v", meta)
			}
		}
	}
	if reminders != 1 {
		t.Fatalf("next message received %d reminders, want 1; frames=%+v", reminders, frames)
	}
}

func TestAgentStreamerUnboundedRequestSkipsGuardrailReminder(t *testing.T) {
	tracker := NewGuardrailReminderTracker()
	streamer := &AgentStreamer{
		Store:              storetest.New(),
		Guardrails:         config.GuardrailsConfig{MaxModelCallsPerTurn: 1},
		GuardrailReminders: tracker,
	}
	backend := &guardrailBackend{}
	streamer.Run(context.Background(), AgentStreamRequest{
		Backend: backend, ConversationID: "unbounded", Mode: AgentStreamAggregate,
		UserInitiated: true, Unbounded: true,
	})
	if _, ok := tracker.Take("unbounded"); ok {
		t.Fatal("unbounded turn left a guardrail reminder")
	}
}

func TestAgentStreamerNonUserTurnDoesNotConsumeOrCreateReminder(t *testing.T) {
	tracker := NewGuardrailReminderTracker()
	tracker.Put("cron", &GuardrailSnapshot{Prompt: "pending user reminder"})
	streamer := &AgentStreamer{
		Store:              storetest.New(),
		Guardrails:         config.GuardrailsConfig{MaxModelCallsPerTurn: 1},
		GuardrailReminders: tracker,
	}
	var frames []ServerMessage
	streamer.Run(context.Background(), AgentStreamRequest{
		Backend: &guardrailBackend{}, ConversationID: "cron", Mode: AgentStreamAggregate,
		Broadcast: func(msg ServerMessage) { frames = append(frames, msg) },
	})
	for _, frame := range frames {
		if strings.Contains(frame.Message, "pending user reminder") {
			t.Fatalf("non-user turn consumed reminder: %+v", frame)
		}
	}
	if got, ok := tracker.Take("cron"); !ok || got.Prompt != "pending user reminder" {
		t.Fatalf("pending reminder changed after non-user turn: %+v, %v", got, ok)
	}
}
