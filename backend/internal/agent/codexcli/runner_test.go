package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/sessionlog"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

func TestParseCodexLine_SessionConfigured(t *testing.T) {
	line := []byte(`{"id":"1","msg":{"type":"session_configured","session_id":"sess-abc","model":"gpt-5"}}`)
	events := ParseCodexLine(line)
	if len(events) != 1 || events[0].Kind != agent.KindSystemInit {
		t.Fatalf("expected one system_init event, got %+v", events)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("system_init content not JSON: %v", err)
	}
	if got["session_id"] != "sess-abc" || got["model"] != "gpt-5" {
		t.Errorf("unexpected system_init payload: %v", got)
	}
}

func TestParseCodexLine_AgentMessageDelta(t *testing.T) {
	events := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"agent_message_delta","delta":"Hello"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindDelta || events[0].Content != "Hello" {
		t.Fatalf("unexpected delta events: %+v", events)
	}
	// Empty delta produces no event so the UI doesn't flicker on heartbeats.
	if got := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"agent_message_delta","delta":""}}`)); len(got) != 0 {
		t.Errorf("empty delta should drop the event, got %+v", got)
	}
}

func TestParseCodexLine_AgentMessageBecomesResult(t *testing.T) {
	events := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"agent_message","message":"final text"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindResult || events[0].Content != "final text" {
		t.Fatalf("expected agent.KindResult with final text, got %+v", events)
	}
}

func TestParseCodexLine_ReasoningDeltaMappedToThinking(t *testing.T) {
	events := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"agent_reasoning_delta","delta":"plan…"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindThinkingDelta {
		t.Fatalf("expected agent.KindThinkingDelta, got %+v", events)
	}
	// agent_reasoning (terminal) is suppressed to avoid double-counting.
	if got := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"agent_reasoning","text":"x"}}`)); len(got) != 0 {
		t.Errorf("agent_reasoning should drop, got %+v", got)
	}
}

func TestParseCodexLine_ExecCommandLifecycle(t *testing.T) {
	begin := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"exec_command_begin","call_id":"c1","command":["ls","-la"],"cwd":"/tmp"}}`))
	if len(begin) != 1 || begin[0].Kind != agent.KindToolUseStart {
		t.Fatalf("expected tool_use_start, got %+v", begin)
	}
	var bp map[string]any
	if err := json.Unmarshal([]byte(begin[0].Content), &bp); err != nil {
		t.Fatalf("begin content not JSON: %v", err)
	}
	if bp["id"] != "c1" || bp["name"] != "Bash" {
		t.Errorf("unexpected begin payload: %v", bp)
	}

	delta := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"exec_command_output_delta","call_id":"c1","stream":"stdout","chunk":"line\n"}}`))
	if len(delta) != 1 || delta[0].Kind != agent.KindToolInputDelta || delta[0].Content != "line\n" {
		t.Fatalf("unexpected output_delta event: %+v", delta)
	}

	end := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"exec_command_end","call_id":"c1","exit_code":0,"stdout":"line\n"}}`))
	if len(end) != 1 || end[0].Kind != agent.KindToolResult {
		t.Fatalf("expected tool_result, got %+v", end)
	}
	var ep map[string]any
	if err := json.Unmarshal([]byte(end[0].Content), &ep); err != nil {
		t.Fatalf("end content not JSON: %v", err)
	}
	if ep["id"] != "c1" {
		t.Errorf("end payload missing id: %v", ep)
	}
	if exit, _ := ep["exit_code"].(float64); int(exit) != 0 {
		t.Errorf("exit_code lost: %v", ep)
	}
}

func TestParseCodexLine_TaskCompleteFallback(t *testing.T) {
	events := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"task_complete","last_agent_message":"done"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindResult || events[0].Content != "done" {
		t.Fatalf("expected agent.KindResult 'done', got %+v", events)
	}
	// Empty last_agent_message yields a sentinel agent.KindResult so the
	// persistence path still observes end-of-turn.
	events = ParseCodexLine([]byte(`{"id":"1","msg":{"type":"task_complete","last_agent_message":""}}`))
	if len(events) != 1 || events[0].Kind != agent.KindResult || events[0].Content != "" {
		t.Errorf("expected empty agent.KindResult, got %+v", events)
	}
}

func TestParseCodexLine_TokenCountToUsage(t *testing.T) {
	line := []byte(`{"id":"1","msg":{"type":"token_count","info":{"model":"gpt-5","model_context_window":258400,"last_token_usage":{"input_tokens":120,"output_tokens":42,"cached_input_tokens":15,"total_tokens":162}}}}`)
	events := ParseCodexLine(line)
	if len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindContextUsage {
		t.Fatalf("expected agent.KindUsage, got %+v", events)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("usage content not JSON: %v", err)
	}
	// input_tokens is emitted NET of the cache hit (120 gross - 15 cached
	// = 105) to match claude's disjoint input/cache_read semantics.
	if got["input_tokens"].(float64) != 105 || got["output_tokens"].(float64) != 42 || got["cache_read_input_tokens"].(float64) != 15 {
		t.Errorf("usage payload missing fields: %v", got)
	}
	var contextPayload map[string]any
	if err := json.Unmarshal([]byte(events[1].Content), &contextPayload); err != nil {
		t.Fatalf("context usage content not JSON: %v", err)
	}
	if contextPayload["used"] != float64(162) || contextPayload["total"] != float64(258400) || contextPayload["input_tokens"] != float64(105) || contextPayload["cache_read"] != float64(15) {
		t.Errorf("context usage payload missing fields: %v", contextPayload)
	}
}

func TestParseCodexLine_TopLevelThreadStarted(t *testing.T) {
	events := ParseCodexLine([]byte(`{"type":"thread.started","thread_id":"thread-abc"}`))
	if len(events) != 1 || events[0].Kind != agent.KindSystemInit {
		t.Fatalf("expected system_init, got %+v", events)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("system_init content not JSON: %v", err)
	}
	if got["session_id"] != "thread-abc" {
		t.Errorf("thread id not mapped to session_id: %v", got)
	}
}

func TestParseCodexLine_TopLevelAgentMessageItem(t *testing.T) {
	events := ParseCodexLine([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"final answer"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindResult || events[0].Content != "final answer" {
		t.Fatalf("expected top-level agent_message as agent.KindResult, got %+v", events)
	}
}

func TestParseCodexLine_TopLevelCommandExecutionItem(t *testing.T) {
	started := ParseCodexLine([]byte(`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}`))
	if len(started) != 1 || started[0].Kind != agent.KindToolUseStart {
		t.Fatalf("expected command item to start a tool, got %+v", started)
	}
	var startPayload map[string]any
	if err := json.Unmarshal([]byte(started[0].Content), &startPayload); err != nil {
		t.Fatalf("started content not JSON: %v", err)
	}
	if startPayload["id"] != "item_1" || startPayload["name"] != "Bash" || startPayload["command"] != "bash -lc ls" {
		t.Errorf("unexpected started payload: %v", startPayload)
	}

	completed := ParseCodexLine([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","aggregated_output":"a\nb\n","exit_code":0,"status":"completed"}}`))
	if len(completed) != 1 || completed[0].Kind != agent.KindToolResult {
		t.Fatalf("expected command item completion as tool_result, got %+v", completed)
	}
	var donePayload map[string]any
	if err := json.Unmarshal([]byte(completed[0].Content), &donePayload); err != nil {
		t.Fatalf("completed content not JSON: %v", err)
	}
	if donePayload["id"] != "item_1" || donePayload["name"] != "Bash" {
		t.Errorf("unexpected completed payload: %v", donePayload)
	}
	if donePayload["content"] != "a\nb" {
		t.Errorf("aggregated output not preserved: %v", donePayload)
	}
}

func TestParseCodexLine_TopLevelTurnCompletedUsage(t *testing.T) {
	events := ParseCodexLine([]byte(`{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":15,"output_tokens":42,"reasoning_output_tokens":9}}`))
	if len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("expected turn.completed usage, got %+v", events)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("usage content not JSON: %v", err)
	}
	// input_tokens netted: 120 gross - 15 cached = 105.
	if got["input_tokens"].(float64) != 105 || got["output_tokens"].(float64) != 42 || got["cache_read_input_tokens"].(float64) != 15 || got["reasoning_output_tokens"].(float64) != 9 {
		t.Errorf("usage payload missing fields: %v", got)
	}
}

func TestParseCodexLine_UnknownTypeIsDropped(t *testing.T) {
	// New event types must not blow up the parser — they get dropped so
	// daymug stays forward-compatible with codex CLI releases.
	if got := ParseCodexLine([]byte(`{"id":"x","msg":{"type":"some_future_event"}}`)); len(got) != 0 {
		t.Errorf("unknown type should drop, got %+v", got)
	}
	if got := ParseCodexLine([]byte(`not json`)); len(got) != 0 {
		t.Errorf("invalid json should drop, got %+v", got)
	}
}

func TestCodexStreamProcessor_Passthrough(t *testing.T) {
	p := NewStreamProcessor("")
	events := p.Process([]byte(`{"id":"1","msg":{"type":"agent_message_delta","delta":"hi"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindDelta {
		t.Errorf("processor should mirror ParseCodexLine, got %+v", events)
	}
}

// thread.started carries only thread_id, so without enrichment the chat
// header would render "Model: unknown | Tools: 0 available". The
// processor must fold the run's Model + the static Bash tool list into
// the resulting agent.KindSystemInit payload.
func TestCodexStreamProcessor_EnrichesSystemInitWithModelAndTools(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	events := p.Process([]byte(`{"type":"thread.started","thread_id":"thread-1"}`))
	if len(events) != 1 || events[0].Kind != agent.KindSystemInit {
		t.Fatalf("expected single system_init, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5", payload["model"])
	}
	if payload["session_id"] != "thread-1" {
		t.Errorf("session_id = %v, want thread-1", payload["session_id"])
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 || tools[0] != "Bash" {
		t.Errorf("tools = %v, want [Bash]", tools)
	}
}

// A legacy session_configured event that already carries its own model
// must NOT be overwritten by the run's default; the upstream payload
// wins.
func TestCodexStreamProcessor_PreservesExistingSystemInitModel(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	events := p.Process([]byte(`{"id":"1","msg":{"type":"session_configured","session_id":"sess-1","model":"gpt-5.5-preview"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindSystemInit {
		t.Fatalf("expected single system_init, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if payload["model"] != "gpt-5.5-preview" {
		t.Errorf("processor clobbered upstream model: got %v", payload["model"])
	}
}

// turn.completed → agent.KindUsage carries the per-turn token counts but the
// envelope's model field is empty. Without enrichment the admin usage
// rollup attributes every codex turn to "" and the per-account chart
// collapses to a single unlabelled bar.
func TestCodexStreamProcessor_EnrichesUsageWithPerModel(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":42,"output_tokens":7}}`))
	if len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("expected one usage event, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	perModel, ok := payload["per_model"].(map[string]any)
	if !ok {
		t.Fatalf("per_model missing: %v", payload)
	}
	entry, ok := perModel["gpt-5.5"].(map[string]any)
	if !ok {
		t.Fatalf("per_model[gpt-5.5] missing: %v", perModel)
	}
	if int(entry["input_tokens"].(float64)) != 42 || int(entry["output_tokens"].(float64)) != 7 {
		t.Errorf("per_model breakdown wrong: %v", entry)
	}
}

func TestCodexStreamProcessor_DoesNotInferContextFromAggregateUsage(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":2419458,"cached_input_tokens":2286336,"output_tokens":12134}}`))
	if len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("aggregate turn usage must not become a context gauge: %+v", events)
	}
}

func TestCodexStreamProcessor_EmitsContextFromTokenCountWindow(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	events := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"model_context_window":100,"last_token_usage":{"input_tokens":60,"cached_input_tokens":40,"output_tokens":20,"total_tokens":80}}}}`))
	if len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindContextUsage {
		t.Fatalf("expected usage and context events, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["used"] != float64(80) || payload["total"] != float64(100) || payload["input_tokens"] != float64(20) || payload["cache_read"] != float64(40) {
		t.Fatalf("context usage = %#v", payload)
	}
}

func TestCodexStreamProcessor_ContextFallsBackToInputForLegacyTokenCount(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	events := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"model_context_window":100,"last_token_usage":{"input_tokens":40,"output_tokens":7}}}}`))
	if len(events) != 2 || events[1].Kind != agent.KindContextUsage {
		t.Fatalf("expected usage and context events, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["used"] != float64(40) || payload["total"] != float64(100) {
		t.Fatalf("context usage = %#v", payload)
	}
}

// Regression: codex's own stream sometimes reports a bare upstream
// model id (e.g. "gpt-6") that doesn't match the price table key
// ("gpt-6-astra"). Before the fix, computeCostUSD was called with the
// upstream string and silently returned 0 — so every codex turn
// landed in token_usage with cost_usd = $0. The processor must use
// p.Model (the RunRequest model — what the user picked in the UI)
// when computing cost, regardless of what the stream carries.
func TestCodexStreamProcessor_UsageCostUsesRunRequestModel(t *testing.T) {
	p := NewStreamProcessor("gpt-6-astra")
	// token_count.info.model = "gpt-6" mimics a bare upstream id that
	// differs from DayMug's selected pricing key.
	events := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"model":"gpt-6","last_token_usage":{"input_tokens":1000000,"output_tokens":1000000}}}}`))
	if len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("expected one usage event, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	cost, ok := payload["total_cost_usd"].(float64)
	if !ok || cost <= 0 {
		t.Errorf("total_cost_usd missing or zero: %v", payload)
	}
	// gpt-6-astra is $10/M input + $50/M output → $60 for 1M+1M.
	if cost != 60.0 {
		t.Errorf("total_cost_usd = %v, want 60", cost)
	}
	perModel, ok := payload["per_model"].(map[string]any)
	if !ok {
		t.Fatalf("per_model missing: %v", payload)
	}
	if _, ok := perModel["gpt-6-astra"]; !ok {
		t.Errorf("per_model should be keyed by p.Model (gpt-6-astra), got %v", perModel)
	}
}

// usageFromKindUsage extracts the per-turn input/output figures from
// a single KindUsage event. Returns -1s if the event is missing or
// malformed so the caller can fail with a useful message.
func usageFromKindUsage(t *testing.T, events []agent.StreamEvent) (input, cached, output, reasoning int) {
	t.Helper()
	var usage agent.StreamEvent
	for _, evt := range events {
		if evt.Kind == agent.KindUsage {
			usage = evt
			break
		}
	}
	if usage.Kind != agent.KindUsage {
		t.Fatalf("expected KindUsage event, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(usage.Content), &payload); err != nil {
		t.Fatalf("usage content not JSON: %v", err)
	}
	in, _ := payload["input_tokens"].(float64)
	cIn, _ := payload["cache_read_input_tokens"].(float64)
	out, _ := payload["output_tokens"].(float64)
	rOut, _ := payload["reasoning_output_tokens"].(float64)
	return int(in), int(cIn), int(out), int(rOut)
}

// Regression: older codex CLIs (the family that ran the conversations
// already in the DB) omit last_token_usage and only emit a cumulative
// total_token_usage on each turn. Before this commit, the stateless
// parser used the cumulative figure as if it were per-turn, so the
// header chip showed "this turn cost 1.16M tokens" by turn 6 and the
// daily token_usage rollup quintuple-counted the same prefix bytes.
// StreamProcessor now stores the previous total and subtracts to
// recover the real per-turn delta. This test walks three turns through
// a single processor and pins each turn's emitted figures.
func TestCodexStreamProcessor_TokenCountDerivesDeltaFromCumulativeTotal(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")

	// Turn 1: cumulative total = 413k input (335k cached) / 11.6k output.
	// With no baseline we surface the cumulative as-is — it IS the
	// first turn's true cost.
	// Emitted input_tokens is NET of cache (413535 - 334848 = 78687);
	// cached/out/reasoning are surfaced as-is.
	got := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"model":"gpt-5","total_token_usage":{"input_tokens":413535,"cached_input_tokens":334848,"output_tokens":11650,"reasoning_output_tokens":8447}}}}`))
	in, cached, out, reasoning := usageFromKindUsage(t, got)
	if in != 78687 || cached != 334848 || out != 11650 || reasoning != 8447 {
		t.Errorf("turn 1: got input=%d cached=%d out=%d reasoning=%d, want 78687/334848/11650/8447", in, cached, out, reasoning)
	}

	// Turn 2: cumulative total = 785k / 14.9k. Gross deltas:
	//   input    = 785396 - 413535 = 371861
	//   cached   = 692480 - 334848 = 357632
	//   output   = 14991  - 11650  = 3341
	//   reasoning= 9766   - 8447   = 1319
	// Emitted input is netted: 371861 - 357632 = 14229.
	got = p.Process([]byte(`{"id":"2","msg":{"type":"token_count","info":{"model":"gpt-5","total_token_usage":{"input_tokens":785396,"cached_input_tokens":692480,"output_tokens":14991,"reasoning_output_tokens":9766}}}}`))
	in, cached, out, reasoning = usageFromKindUsage(t, got)
	if in != 14229 || cached != 357632 || out != 3341 || reasoning != 1319 {
		t.Errorf("turn 2: got input=%d cached=%d out=%d reasoning=%d, want 14229/357632/3341/1319", in, cached, out, reasoning)
	}

	// Turn 3: another cumulative tick — verify state keeps advancing
	// past the previous baseline, not jumping back to turn 1's value.
	// Gross input delta 125501, cached delta 121472 → netted input 4029.
	got = p.Process([]byte(`{"id":"3","msg":{"type":"token_count","info":{"model":"gpt-5","total_token_usage":{"input_tokens":910897,"cached_input_tokens":813952,"output_tokens":16324,"reasoning_output_tokens":10140}}}}`))
	in, cached, out, reasoning = usageFromKindUsage(t, got)
	if in != 4029 || cached != 121472 || out != 1333 || reasoning != 374 {
		t.Errorf("turn 3: got input=%d cached=%d out=%d reasoning=%d, want 4029/121472/1333/374", in, cached, out, reasoning)
	}
}

// Regression: codex CLI's own internal compaction can rewind its
// running total mid-session. The DayMug docs at runner.go:74-77 call
// out that we don't own a hook for that signal, so the stream just
// shows a cumulative-input number that suddenly drops. A naive
// curr - prev would emit a NEGATIVE token row which would corrupt the
// per-user daily rollup. Instead, the regression is treated as a
// fresh baseline: emit the new cumulative as-is for that turn.
func TestCodexStreamProcessor_TokenCountTreatsTotalRegressionAsFreshBaseline(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	// Establish baseline at 100k.
	p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000,"output_tokens":5000}}}}`))
	// Now total drops to 20k (post-compaction). Naive delta would
	// emit -80000 input / -2500 output. We surface 20000/2500 instead.
	got := p.Process([]byte(`{"id":"2","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20000,"output_tokens":2500}}}}`))
	in, _, out, _ := usageFromKindUsage(t, got)
	if in != 20000 || out != 2500 {
		t.Errorf("post-compaction got input=%d out=%d, want 20000/2500 (cumulative used as fresh baseline)", in, out)
	}
	// Next turn after the regression should delta against the new
	// baseline, not the pre-compaction one.
	got = p.Process([]byte(`{"id":"3","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":35000,"output_tokens":3000}}}}`))
	in, _, out, _ = usageFromKindUsage(t, got)
	if in != 15000 || out != 500 {
		t.Errorf("turn after compaction: got input=%d out=%d, want 15000/500", in, out)
	}
}

// Modern CLI emits last_token_usage AND total_token_usage on each
// event. We trust last_token_usage for the emit (per-turn already),
// but also record the running total so that if the CLI ever silently
// stops emitting last_token_usage mid-session, the delta fallback has
// a baseline to subtract against — otherwise the next turn would be
// "first turn after fallback" and surface the full cumulative.
func TestCodexStreamProcessor_TokenCountRecordsTotalBaselineEvenWhenLastIsPresent(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	// Modern event: both fields present. We emit last_token_usage.
	got := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":500,"output_tokens":50},"last_token_usage":{"input_tokens":500,"output_tokens":50}}}}`))
	in, _, out, _ := usageFromKindUsage(t, got)
	if in != 500 || out != 50 {
		t.Errorf("modern CLI first turn: got input=%d out=%d, want 500/50", in, out)
	}
	// CLI version drift: next turn carries only total_token_usage.
	// Delta against the baseline we recorded above should be 200/30,
	// NOT the cumulative 700/80 (which is what a stateless parser
	// would have surfaced before this fix).
	got = p.Process([]byte(`{"id":"2","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":700,"output_tokens":80}}}}`))
	in, _, out, _ = usageFromKindUsage(t, got)
	if in != 200 || out != 30 {
		t.Errorf("after CLI drift: got input=%d out=%d, want 200/30 (delta against recorded baseline)", in, out)
	}
}

// subtractCodexUsage's contract: nil prev means curr IS per-turn;
// otherwise per-field subtraction with clamp at zero on regression.
// Tested directly so changes to the helper are caught even when no
// event-stream test exercises the exact shape.
func TestSubtractCodexUsage(t *testing.T) {
	t.Run("nil curr returns nil", func(t *testing.T) {
		if got := subtractCodexUsage(nil, &codexTokenUsage{InputTokens: 10}); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})
	t.Run("nil prev returns curr as-is", func(t *testing.T) {
		curr := &codexTokenUsage{InputTokens: 10, OutputTokens: 5}
		got := subtractCodexUsage(curr, nil)
		if got != curr {
			t.Errorf("want exact pointer-equal curr, got %+v", got)
		}
	})
	t.Run("normal subtraction", func(t *testing.T) {
		curr := &codexTokenUsage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 20, ReasoningOutputToks: 15, TotalTokens: 135}
		prev := &codexTokenUsage{InputTokens: 30, CachedInputTokens: 20, OutputTokens: 5, ReasoningOutputToks: 3, TotalTokens: 38}
		got := subtractCodexUsage(curr, prev)
		want := &codexTokenUsage{InputTokens: 70, CachedInputTokens: 60, OutputTokens: 15, ReasoningOutputToks: 12, TotalTokens: 97}
		if *got != *want {
			t.Errorf("got %+v, want %+v", *got, *want)
		}
	})
	t.Run("input regression surfaces curr unchanged", func(t *testing.T) {
		curr := &codexTokenUsage{InputTokens: 5, OutputTokens: 10}
		prev := &codexTokenUsage{InputTokens: 100, OutputTokens: 5}
		got := subtractCodexUsage(curr, prev)
		if got != curr {
			t.Errorf("expected curr returned as-is after regression, got %+v", got)
		}
	})
	t.Run("cache-only regression clamps to zero", func(t *testing.T) {
		// Input and output advance normally but cached_input
		// regressed (e.g. cache prefix invalidated). We don't treat
		// that as a full reset — only the cached field clamps.
		curr := &codexTokenUsage{InputTokens: 200, CachedInputTokens: 10, OutputTokens: 30}
		prev := &codexTokenUsage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 20}
		got := subtractCodexUsage(curr, prev)
		if got.InputTokens != 100 || got.OutputTokens != 10 || got.CachedInputTokens != 0 {
			t.Errorf("cache-only regression: got %+v, want input=100 output=10 cached=0", *got)
		}
	})
}

// Regression: the usage enrichment once forwarded only the bare
// input/output token counts to computeCostUSD, so cache-prefix bytes
// were billed at the full input rate. A later bug also added the reasoning
// breakdown to output_tokens even though Codex already includes it there.
// This test drives a typical high-cache, high-reasoning turn and pins both
// the discounted input price and the non-duplicated output price.
func TestCodexStreamProcessor_UsageAppliesCacheDiscountAndReasoning(t *testing.T) {
	p := NewStreamProcessor("gpt-6-astra")
	// 1M input of which 800k cached + 1M total output, of which 800k is
	// reasoning. gpt-6-astra default: $10 input / $1 cached / $50 output.
	//   uncached input  : 200k × $10/M = $2.00
	//   cached input    : 800k × $1/M  = $0.80
	//   output (includes reasoning): 1M × $50/M = $50.00
	//   total                            = $52.80
	events := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"model":"gpt-6","last_token_usage":{"input_tokens":1000000,"cached_input_tokens":800000,"output_tokens":1000000,"reasoning_output_tokens":800000}}}}`))
	if len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("expected one usage event, got %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	cost, _ := payload["total_cost_usd"].(float64)
	if cost != 52.8 {
		t.Errorf("total_cost_usd = %v, want 52.8 (reasoning already included in output)", cost)
	}
	// per_model breakdown must surface the cache + reasoning fields so
	// downstream persistence can
	// attribute them to the right model row.
	perModel, ok := payload["per_model"].(map[string]any)
	if !ok {
		t.Fatalf("per_model missing: %v", payload)
	}
	entry, ok := perModel["gpt-6-astra"].(map[string]any)
	if !ok {
		t.Fatalf("per_model[gpt-6-astra] missing: %v", perModel)
	}
	if int(entry["cache_read_input_tokens"].(float64)) != 800_000 {
		t.Errorf("per_model cache_read_input_tokens = %v, want 800000", entry["cache_read_input_tokens"])
	}
	if int(entry["reasoning_output_tokens"].(float64)) != 800_000 {
		t.Errorf("per_model reasoning_output_tokens = %v, want 800000", entry["reasoning_output_tokens"])
	}
	if entry["cost_usd"].(float64) != 52.8 {
		t.Errorf("per_model cost_usd = %v, want 52.8", entry["cost_usd"])
	}
}

// Regression: codex's token_count reports input_tokens as the GROSS
// prompt (cached prefix folded in), whereas claude reports input_tokens
// and cache_read_input_tokens as disjoint. Persisting codex's gross
// figure into the same usage column made the shared "Input" tile show an
// order-of-magnitude more for codex than claude on the identical
// conversation. codexcommon.UsageEvents now nets the cache out of
// input_tokens — but must reconstruct the gross total for costing, so the dollar figure must stay put. This pins both halves.
func TestCodexStreamProcessor_NetsCacheOutOfInputButKeepsCost(t *testing.T) {
	p := NewStreamProcessor("gpt-6-astra")
	// 47630 gross input, 46464 cached → 1166 uncached. gpt-6-astra: $10/M
	// input, $1/M cached, $50/M output.
	//   uncached: 1166  × $10/M = $0.01166
	//   cached  : 46464 × $1/M  = $0.046464
	//   output  : 190   × $50/M = $0.0095
	//   total                    = $0.067624
	events := p.Process([]byte(`{"id":"1","msg":{"type":"token_count","info":{"model":"gpt-6","last_token_usage":{"input_tokens":47630,"cached_input_tokens":46464,"output_tokens":190}}}}`))
	in, cached, out, _ := usageFromKindUsage(t, events)
	if in != 1166 || cached != 46464 || out != 190 {
		t.Errorf("got input=%d cached=%d out=%d, want netted 1166/46464/190", in, cached, out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	// Cost is computed off the reconstructed gross, so the cache discount
	// still applies to all 46464 cached bytes — netting input must not
	// re-bill them at the uncached rate.
	cost, _ := payload["total_cost_usd"].(float64)
	if cost <= 0.067 || cost >= 0.068 {
		t.Errorf("total_cost_usd = %v, want ~0.067624 (cache discount preserved)", cost)
	}
}

func TestCodexStreamProcessor_ReordersUsageBeforeResult(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	if got := p.Process([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"final answer"}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "final answer" {
		t.Fatalf("agent result should stream as delta while waiting for usage, got %+v", got)
	}

	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":42,"output_tokens":7}}`))
	if len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindResult {
		t.Fatalf("expected usage then result, got %+v", events)
	}
	if events[1].Content != "final answer" {
		t.Errorf("result content = %q", events[1].Content)
	}
}

func TestCodexStreamProcessor_MergesMultipleResultsIntoOneTurn(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	if got := p.Process([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"first chunk"}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "first chunk" {
		t.Fatalf("first result should stream as delta while waiting for usage, got %+v", got)
	}
	if got := p.Process([]byte(`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"second chunk"}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "\n\nsecond chunk" {
		t.Fatalf("second result should stream as delta and merge into pending result, got %+v", got)
	}

	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":42,"output_tokens":7}}`))
	if len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindResult {
		t.Fatalf("expected usage and one merged result, got %+v", events)
	}
	if events[1].Content != "first chunk\n\nsecond chunk" {
		t.Errorf("merged content = %q", events[1].Content)
	}
}

func TestCodexStreamProcessor_NormalizesOverlappingAssistantSnapshots(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	inputs := []string{
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Lint failed on DOM globals. I will fix local types."}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"I will fix local types.\n\nThe build is still running."}}`,
		`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"The build is still running.\n\nLint is now clean."}}`,
	}
	want := []string{
		"Lint failed on DOM globals. I will fix local types.",
		"\n\nThe build is still running.",
		"\n\nLint is now clean.",
	}
	for i, input := range inputs {
		got := p.Process([]byte(input))
		if len(got) != 1 || got[0].Kind != agent.KindDelta {
			t.Fatalf("step %d: expected assistant delta, got %+v", i, got)
		}
		if got[0].Content != want[i] {
			t.Fatalf("step %d: content = %q, want %q", i, got[0].Content, want[i])
		}
	}

	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":42,"output_tokens":7}}`))
	if len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindResult {
		t.Fatalf("expected usage then result, got %+v", events)
	}
	wantResult := "Lint failed on DOM globals. I will fix local types.\n\nThe build is still running.\n\nLint is now clean."
	if events[1].Content != wantResult {
		t.Errorf("result content = %q, want %q", events[1].Content, wantResult)
	}
}

func TestCodexStreamProcessor_NormalizesCumulativeReasoningSnapshots(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	inputs := []string{
		`{"type":"item.completed","item":{"id":"r1","type":"reasoning","text":"Checking instructions"}}`,
		`{"type":"item.completed","item":{"id":"r2","type":"reasoning","text":"Checking instructions\n\nReading files"}}`,
		`{"type":"item.completed","item":{"id":"r3","type":"reasoning","text":"Checking instructions\n\nReading files\n\nPatching code"}}`,
	}
	want := []string{
		"Checking instructions",
		"\n\nReading files",
		"\n\nPatching code",
	}
	for i, input := range inputs {
		got := p.Process([]byte(input))
		if len(got) != 1 || got[0].Kind != agent.KindThinkingDelta {
			t.Fatalf("step %d: expected thinking delta, got %+v", i, got)
		}
		if got[0].Content != want[i] {
			t.Fatalf("step %d: content = %q, want %q", i, got[0].Content, want[i])
		}
	}
	if got := p.Process([]byte(inputs[len(inputs)-1])); len(got) != 0 {
		t.Fatalf("duplicate reasoning snapshot should be dropped, got %+v", got)
	}
}

func TestCodexStreamProcessor_SeparatesIndependentReasoningItems(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")

	first := p.Process([]byte(`{"type":"item.completed","item":{"id":"r1","type":"reasoning","text":"**Preparing review**"}}`))
	if len(first) != 1 || first[0].Kind != agent.KindThinkingDelta || first[0].Content != "**Preparing review**" {
		t.Fatalf("first reasoning item: %+v", first)
	}

	second := p.Process([]byte(`{"type":"item.completed","item":{"id":"r2","type":"reasoning","text":"**Planning checks**"}}`))
	if len(second) != 1 || second[0].Kind != agent.KindThinkingDelta || second[0].Content != "\n\n**Planning checks**" {
		t.Fatalf("second reasoning item: %+v", second)
	}

	// Legacy reasoning deltas are chunks within one item, so adding separators
	// between them would turn every streamed token into its own paragraph.
	legacy := NewStreamProcessor("gpt-5.5")
	_ = legacy.Process([]byte(`{"id":"r1","msg":{"type":"agent_reasoning_delta","delta":"Prepar"}}`))
	continued := legacy.Process([]byte(`{"id":"r1","msg":{"type":"agent_reasoning_delta","delta":"ing"}}`))
	if len(continued) != 1 || continued[0].Content != "ing" {
		t.Fatalf("legacy reasoning continuation: %+v", continued)
	}
}

func TestCodexStreamProcessor_FlushesResultWithoutUsage(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	if got := p.Process([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"final answer"}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "final answer" {
		t.Fatalf("agent result should stream as delta while waiting for usage, got %+v", got)
	}

	events := p.Flush()
	if len(events) != 1 || events[0].Kind != agent.KindResult || events[0].Content != "final answer" {
		t.Fatalf("expected flushed result, got %+v", events)
	}
	if got := p.Flush(); len(got) != 0 {
		t.Fatalf("flush should be empty after pending result is consumed, got %+v", got)
	}
}

func TestCodexStreamProcessor_DoesNotRestreamDuplicateTaskCompleteResult(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	if got := p.Process([]byte(`{"id":"1","msg":{"type":"agent_message","message":"final answer"}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "final answer" {
		t.Fatalf("agent message should stream once, got %+v", got)
	}
	if got := p.Process([]byte(`{"id":"1","msg":{"type":"task_complete","last_agent_message":"final answer"}}`)); len(got) != 0 {
		t.Fatalf("duplicate task_complete should only confirm pending result, got %+v", got)
	}

	events := p.Flush()
	if len(events) != 1 || events[0].Kind != agent.KindResult || events[0].Content != "final answer" {
		t.Fatalf("expected one final result, got %+v", events)
	}
}

func TestCodexStreamProcessor_DoesNotRestreamAlreadyDeltaStreamedResult(t *testing.T) {
	p := NewStreamProcessor("gpt-5.5")
	if got := p.Process([]byte(`{"id":"1","msg":{"type":"agent_message_delta","delta":"Hello "}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "Hello " {
		t.Fatalf("first delta = %+v", got)
	}
	if got := p.Process([]byte(`{"id":"1","msg":{"type":"agent_message_delta","delta":"world"}}`)); len(got) != 1 || got[0].Kind != agent.KindDelta || got[0].Content != "world" {
		t.Fatalf("second delta = %+v", got)
	}
	if got := p.Process([]byte(`{"id":"1","msg":{"type":"agent_message","message":"Hello world"}}`)); len(got) != 0 {
		t.Fatalf("complete result was already streamed and should only be held, got %+v", got)
	}

	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`))
	if len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindResult || events[1].Content != "Hello world" {
		t.Fatalf("expected usage and final result, got %+v", events)
	}
}

// Top-level error event ({"type":"error","message":"..."}) is what
// current codex builds emit when the account has hit a hard limit. The
// processor must convert it into agent.KindError so the runner's filter
// goroutine can stash it as the final wait error.
func TestParseCodexLine_TopLevelErrorBecomesKindError(t *testing.T) {
	events := ParseCodexLine([]byte(`{"type":"error","message":"You've hit your usage limit."}`))
	if len(events) != 1 || events[0].Kind != agent.KindError {
		t.Fatalf("expected single agent.KindError event, got %+v", events)
	}
	if events[0].Content != "You've hit your usage limit." {
		t.Errorf("content = %q, want usage-limit text", events[0].Content)
	}
	if got := ParseCodexLine([]byte(`{"type":"error","message":""}`)); len(got) != 0 {
		t.Errorf("empty error message should drop, got %+v", got)
	}
}

// turn.failed carries the same string as the preceding "error" event in
// the wild, but in a nested .error.message payload. Surface it the same
// way so the runner stashes whichever frame arrives last.
func TestParseCodexLine_TurnFailedBecomesKindError(t *testing.T) {
	events := ParseCodexLine([]byte(`{"type":"turn.failed","error":{"message":"Model not supported."}}`))
	if len(events) != 1 || events[0].Kind != agent.KindError {
		t.Fatalf("expected single agent.KindError event, got %+v", events)
	}
	if events[0].Content != "Model not supported." {
		t.Errorf("content = %q, want model-not-supported text", events[0].Content)
	}
	if got := ParseCodexLine([]byte(`{"type":"turn.failed"}`)); len(got) != 0 {
		t.Errorf("turn.failed without error payload should drop, got %+v", got)
	}
}

// Legacy envelope shape ({"id":..., "msg":{"type":"error","error":"<msg>"}})
// — kept working for older codex CLIs still in the wild. Same agent.KindError
// surface as the modern top-level event.
func TestParseCodexLine_LegacyEnvelopeErrorBecomesKindError(t *testing.T) {
	events := ParseCodexLine([]byte(`{"id":"1","msg":{"type":"error","error":"network blew up"}}`))
	if len(events) != 1 || events[0].Kind != agent.KindError {
		t.Fatalf("expected single agent.KindError event, got %+v", events)
	}
	if events[0].Content != "network blew up" {
		t.Errorf("content = %q", events[0].Content)
	}
}

func TestCodexRunner_UsesDangerouslyBypassApprovalsAndSandbox(t *testing.T) {
	tests := []struct {
		name string
		opts agent.RunRequest
		want []string
	}{
		{
			name: "new session",
			opts: agent.RunRequest{Model: "gpt-5", ThinkLevel: "max"},
			want: []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", "model_reasoning_summary=auto", "-c", "model_reasoning_effort=xhigh", "--model", "gpt-5"},
		},
		{
			// codex exec resume rejects --sandbox entirely; the bypass
			// flag is the one path that works on both subcommands.
			// SESSION_ID is positional and must come after the flags
			// (including the reasoning-summary config override).
			name: "resume",
			opts: agent.RunRequest{SessionID: "sess-1", IsResume: true, Model: "gpt-5"},
			want: []string{"codex", "exec", "resume", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", "model_reasoning_summary=auto", "sess-1", "--model", "gpt-5"},
		},
		{
			// ReadOnly chat turns swap the full-access bypass for codex's
			// read-only sandbox on a fresh exec — no writes/exec/network.
			name: "read-only new session",
			opts: agent.RunRequest{Model: "gpt-5", ReadOnly: true},
			want: []string{"codex", "exec", "--json", "--skip-git-repo-check", "--sandbox", "read-only", "-c", "model_reasoning_summary=auto", "--model", "gpt-5"},
		},
		{
			name: "declared context window",
			opts: agent.RunRequest{Model: "qwen", ContextWindow: 131072},
			want: []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", "model_reasoning_summary=auto", "-c", "model_context_window=131072", "--model", "qwen"},
		},
		{
			name: "minimal context new session",
			opts: agent.RunRequest{Model: "gpt-5", CodexMinimalContext: true},
			want: []string{"codex", "exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--ignore-user-config", "--ignore-rules", "--ephemeral", "-c", "model_reasoning_summary=auto", "--model", "gpt-5"},
		},
		{
			name: "minimal context resume keeps session persistence flags off",
			opts: agent.RunRequest{SessionID: "sess-1", IsResume: true, Model: "gpt-5", CodexMinimalContext: true},
			want: []string{"codex", "exec", "resume", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--ignore-user-config", "--ignore-rules", "-c", "model_reasoning_summary=auto", "sess-1", "--model", "gpt-5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spawner := &codexCaptureSpawner{stdout: `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"}
			tt.opts.Spawner = spawner
			ch := make(chan agent.StreamEvent, 4)

			if err := NewBackend().RunWithSession(context.Background(), "prompt", t.TempDir(), tt.opts, ch); err != nil {
				t.Fatalf("RunWithSession: %v", err)
			}
			if len(spawner.reqs) != 1 {
				t.Fatalf("spawn requests = %d, want 1", len(spawner.reqs))
			}
			gotArgv := spawner.reqs[0].Argv
			// argv[0] may be either the bare name (PATH-less test host) or
			// an absolute path produced by agent.ResolveAgentBinary scanning $PATH
			// / nvm — either is correct. Assert the basename and compare
			// only the semantic flags after it.
			if len(gotArgv) == 0 || filepath.Base(gotArgv[0]) != tt.want[0] {
				t.Fatalf("argv[0] = %q, want basename %q", gotArgv[0], tt.want[0])
			}
			if !slices.Equal(gotArgv[1:], tt.want[1:]) {
				t.Fatalf("argv flags = %#v, want %#v", gotArgv[1:], tt.want[1:])
			}
			if strings.Contains(strings.Join(gotArgv, " "), "--full-auto") {
				t.Fatalf("argv still contains --full-auto: %#v", gotArgv)
			}
		})
	}
}

func TestCodexRunner_MinimalContextSkipsSystemPrompt(t *testing.T) {
	spawner := &codexCaptureSpawner{stdout: `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"}
	workDir := t.TempDir()
	ch := make(chan agent.StreamEvent, 4)
	opts := agent.RunRequest{
		Model:               "gpt-5",
		SystemPrompt:        "persona that minimal-context turns must not pay for",
		CodexMinimalContext: true,
		Spawner:             spawner,
	}

	if err := NewBackend().RunWithSession(context.Background(), "prompt", workDir, opts, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if joined := strings.Join(spawner.reqs[0].Argv, " "); strings.Contains(joined, developerInstructionsKey) {
		t.Errorf("argv carries the system prompt in minimal-context mode: %s", joined)
	}
}

// TestCodexRunner_SystemPromptTravelsOnArgv is the whole point of the change:
// the persona reaches codex as a config override, and no file in the work dir
// is touched to deliver it. AGENTS.md is a tracked file in most work dirs, so
// writing there produced a permanent unstaged diff and leaked persona text
// into the operator's repo history.
func TestCodexRunner_SystemPromptTravelsOnArgv(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "fresh"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			spawner := &codexCaptureSpawner{stdout: `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"}
			workDir := t.TempDir()
			ch := make(chan agent.StreamEvent, 4)
			opts := agent.RunRequest{
				Model:        "gpt-5",
				SystemPrompt: "## Your Identity\nbe terse",
				SessionID:    "01997e5a-0000-7000-8000-000000000000",
				IsResume:     resume,
				Spawner:      spawner,
			}

			if err := NewBackend().RunWithSession(context.Background(), "prompt", workDir, opts, ch); err != nil {
				t.Fatalf("RunWithSession: %v", err)
			}
			argv := spawner.reqs[0].Argv
			want := `developer_instructions="## Your Identity\nbe terse"`
			if !slices.Contains(argv, want) {
				t.Errorf("argv missing %s: %#v", want, argv)
			}
			if _, err := os.Stat(filepath.Join(workDir, "AGENTS.md")); !os.IsNotExist(err) {
				t.Errorf("AGENTS.md stat error = %v, want not exist", err)
			}
		})
	}
}

type codexCaptureSpawner struct {
	reqs   []agent.SpawnRequest
	stdout string
}

func (s *codexCaptureSpawner) Spawn(_ context.Context, req agent.SpawnRequest) (agent.RunningProcess, error) {
	s.reqs = append(s.reqs, req)
	return &codexCaptureProcess{stdout: strings.NewReader(s.stdout)}, nil
}

type codexCaptureProcess struct {
	stdout io.Reader
}

func (p *codexCaptureProcess) Stdout() io.Reader      { return p.stdout }
func (p *codexCaptureProcess) Stderr() io.Reader      { return nil }
func (p *codexCaptureProcess) Stdin() io.WriteCloser  { return nil }
func (p *codexCaptureProcess) Wait() error            { return nil }
func (p *codexCaptureProcess) Signal(os.Signal) error { return nil }

func TestCodexRunner_SessionExists(t *testing.T) {
	dir := t.TempDir()
	prev := sessionlog.CodexSessionsDir
	sessionlog.CodexSessionsDir = func() string { return dir }
	defer func() { sessionlog.CodexSessionsDir = prev }()

	r := NewBackend()
	if r.SessionExists("", "sess-xyz", "") {
		t.Errorf("SessionExists should be false before any rollout exists")
	}
	if r.SessionExists("", "", "") {
		t.Errorf("SessionExists must reject empty sessionID")
	}

	// Codex stores rollouts nested under date dirs — verify the
	// recursive scan picks the file up regardless of depth.
	day := filepath.Join(dir, "2026", "05", "12")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rolloutPath := filepath.Join(day, "rollout-2026-05-12T08-30-00-sess-xyz.jsonl")
	if err := os.WriteFile(rolloutPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	if !r.SessionExists("", "sess-xyz", "") {
		t.Errorf("SessionExists should find the rollout under %s", rolloutPath)
	}
	if r.SessionExists("", "missing", "") {
		t.Errorf("SessionExists should not false-positive for an unrelated id")
	}
}

// Regression twin of TestClaudeRunner_SessionExists_HonorsClaudeConfigDir:
// codex maps agent.RunRequest.ConfigDir to CODEX_HOME, so the resume probe
// must scan <codexHome>/sessions when it's set rather than the default
// ~/.codex/sessions.
func TestCodexRunner_SessionExists_HonorsCodexHome(t *testing.T) {
	defaultDir := t.TempDir()
	prev := sessionlog.CodexSessionsDir
	sessionlog.CodexSessionsDir = func() string { return defaultDir }
	defer func() { sessionlog.CodexSessionsDir = prev }()

	altHome := t.TempDir()
	altSessions := filepath.Join(altHome, "sessions", "2026", "05", "12")
	if err := os.MkdirAll(altSessions, 0o755); err != nil {
		t.Fatalf("mkdir alt: %v", err)
	}
	altRollout := filepath.Join(altSessions, "rollout-2026-05-12T08-30-00-sess-alt.jsonl")
	if err := os.WriteFile(altRollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write alt: %v", err)
	}

	r := NewBackend()
	if r.SessionExists("", "sess-alt", "") {
		t.Error("default sessions root must not see the alt account's rollout")
	}
	if !r.SessionExists("", "sess-alt", altHome) {
		t.Errorf("alt CODEX_HOME probe should find %s", altRollout)
	}
}

func TestStreamCodexLines_ParsesAndSendsEvents(t *testing.T) {
	input := strings.NewReader(`{"type":"thread.started","thread_id":"thread-1"}` + "\n" +
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"final"}}` + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":3}}` + "\n")
	ch := make(chan agent.StreamEvent, 8)
	if err := streamcommon.StreamLines(context.Background(), input, ch, lineStream(NewStreamProcessor(""))); err != nil {
		t.Fatalf("streamLines: %v", err)
	}
	close(ch)
	var got []agent.StreamEvent
	for evt := range ch {
		got = append(got, evt)
	}
	if len(got) != 4 ||
		got[0].Kind != agent.KindSystemInit ||
		got[1].Kind != agent.KindDelta ||
		got[1].Content != "final" ||
		got[2].Kind != agent.KindUsage ||
		got[3].Kind != agent.KindResult {
		t.Errorf("unexpected stream output: %+v", got)
	}
}

// codexBlockingReader's Read never returns — simulates a codex CLI that has
// accepted the request and is now hung on an unresponsive upstream.
type codexBlockingReader struct{}

func (codexBlockingReader) Read(_ []byte) (int, error) {
	select {}
}

// Regression guard for the watchdog codex historically lacked: a stalled
// child (accepted the request, then never writes a byte to stdout) must
// surface a user-visible upstream-unreachable error within the stall budget
// instead of blocking the conversation forever on a silent ReadBytes.
func TestStreamCodexLines_StallTimeoutSurfacesUpstreamError(t *testing.T) {
	ch := make(chan agent.StreamEvent, 4)
	errCh := make(chan error, 1)
	go func() {
		cfg := lineStream(NewStreamProcessor(""))
		cfg.StallTimeout = 50 * time.Millisecond
		errCh <- streamcommon.StreamLines(context.Background(), codexBlockingReader{}, ch, cfg)
		close(ch)
	}()

	for range ch {
		// drain
	}
	select {
	case got := <-errCh:
		if got == nil || !strings.Contains(got.Error(), "upstream unreachable") {
			t.Fatalf("expected an upstream-unreachable error, got %v", got)
		}
		if !strings.Contains(got.Error(), "codex") {
			t.Fatalf("stall error should name the codex CLI, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("streamLines did not return after stall timeout")
	}
}

// resolveRunError decides which failure signal reaches the user. The case
// worth guarding is a clean exit alongside a stdout-reported failure: codex
// does that for "usage limit hit" and friends, and because the KindError
// frame is peeled off in-runner, a nil here means the chat goes silent
// instead of showing the reason.
func TestResolveRunError(t *testing.T) {
	waitErr := errors.New("exit status 1")
	streamErr := errors.New("upstream unreachable")

	tests := []struct {
		name        string
		waitErr     error
		capturedErr string
		stderr      string
		streamErr   error
		want        string
	}{
		{
			name: "clean run reports nothing",
			want: "",
		},
		{
			name:        "clean exit still surfaces a stdout-reported failure",
			capturedErr: "usage limit hit",
			want:        "usage limit hit",
		},
		{
			name:      "clean exit propagates a stream failure",
			streamErr: streamErr,
			want:      "upstream unreachable",
		},
		{
			name:        "stdout reason wins over the bare exit status",
			waitErr:     waitErr,
			capturedErr: "model not supported",
			stderr:      "startup banner",
			streamErr:   streamErr,
			want:        "exit status 1: model not supported",
		},
		{
			name:      "stderr is the next-best explanation",
			waitErr:   waitErr,
			stderr:    "boom",
			streamErr: streamErr,
			want:      "exit status 1: boom",
		},
		{
			name:      "a stalled stream beats an opaque exit code",
			waitErr:   waitErr,
			streamErr: streamErr,
			want:      "upstream unreachable",
		},
		{
			name:    "bare exit status is the last resort",
			waitErr: waitErr,
			want:    "exit status 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := resolveRunError(tc.waitErr, tc.capturedErr, tc.stderr, tc.streamErr)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("resolveRunError = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("resolveRunError = %v, want %q", err, tc.want)
			}
		})
	}
}
