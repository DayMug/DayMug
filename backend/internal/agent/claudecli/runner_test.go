package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

func TestParseStreamLine_Delta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindDelta {
		t.Fatalf("expected kind=%s, got %q", agent.KindDelta, events[0].Kind)
	}
	if events[0].Content != "Hello" {
		t.Fatalf("expected content=%q, got %q", "Hello", events[0].Content)
	}
}

func TestParseStreamLine_Result(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"Hello world"}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindResult {
		t.Fatalf("expected kind=%s, got %q", agent.KindResult, events[0].Kind)
	}
	if events[0].Content != "Hello world" {
		t.Fatalf("expected content=%q, got %q", "Hello world", events[0].Content)
	}
}

func TestParseStreamLine_ResultCarriesTurnCorrelation(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"done","user_message_uuid":"user-turn-1","origin":{"kind":"task-notification","subkind":"completed"}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if got := events[0]; got.Kind != agent.KindResult || got.TurnID != "user-turn-1" || got.TurnOrigin != "task-notification:completed" {
		t.Fatalf("result correlation = %#v", got)
	}
}

// A steered message folded into a running turn is reported only in the
// consumption list; the result's user_message_uuid still names the prompt
// that opened the turn.
func TestParseStreamLine_ResultCarriesTheWholeConsumptionList(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"done","user_message_uuid":"user-turn-1","user_message_uuids":["user-turn-1","steered-2"]}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.TurnID != "user-turn-1" {
		t.Fatalf("TurnID = %q, want user-turn-1", got.TurnID)
	}
	if len(got.TurnIDs) != 2 || got.TurnIDs[0] != "user-turn-1" || got.TurnIDs[1] != "steered-2" {
		t.Fatalf("TurnIDs = %q, want [user-turn-1 steered-2]", got.TurnIDs)
	}
}

func TestParseStreamLine_ResultWithContextUsage(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"Hello","usage":{"input_tokens":100,"output_tokens":50},"modelUsage":{"claude-opus-4-6":{"contextWindow":200000}}}`
	events := ParseStreamLine([]byte(line))
	// context_usage + usage + result = 3
	if len(events) != 3 {
		t.Fatalf("expected 3 events (context_usage + usage + result), got %d", len(events))
	}
	if events[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected first event kind=%s, got %q", agent.KindContextUsage, events[0].Kind)
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(events[0].Content), &cu); err != nil {
		t.Fatalf("unmarshal context_usage: %v", err)
	}
	if cu["used"] != 100 { // input only; output_tokens are the response, not context
		t.Errorf("expected used=100, got %d", cu["used"])
	}
	if cu["total"] != 200000 {
		t.Errorf("expected total=200000, got %d", cu["total"])
	}
	if events[1].Kind != agent.KindUsage {
		t.Fatalf("expected second event kind=%s, got %q", agent.KindUsage, events[1].Kind)
	}
	if events[2].Kind != agent.KindResult || events[2].Content != "Hello" {
		t.Fatalf("expected result event, got %+v", events[2])
	}
}

func TestParseStreamLine_ResultWithContextUsageCacheTokens(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"Hello","usage":{"input_tokens":2,"output_tokens":17,"cache_creation_input_tokens":19885},"modelUsage":{"claude-opus-4-6[1m]":{"contextWindow":1000000}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 3 {
		t.Fatalf("expected 3 events (context_usage + usage + result), got %d", len(events))
	}
	if events[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected first event kind=%s, got %q", agent.KindContextUsage, events[0].Kind)
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(events[0].Content), &cu); err != nil {
		t.Fatalf("unmarshal context_usage: %v", err)
	}
	if cu["used"] != 19887 { // input(2) + cache_read(0) + cache_create(19885); output is excluded
		t.Errorf("expected used=19887, got %d", cu["used"])
	}
	if cu["total"] != 1000000 {
		t.Errorf("expected total=1000000, got %d", cu["total"])
	}
}

func TestParseStreamLine_ResultWithSubagentPicksPrimaryModel(t *testing.T) {
	// When a turn invokes a sub-agent (e.g. Task tool with Haiku), modelUsage
	// has multiple entries. We must pick the one with the most inputTokens —
	// that's the main conversation model — not whichever one Go's randomized
	// map iteration happens to return first.
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":50000,"output_tokens":1000},` +
		`"modelUsage":{` +
		`"claude-haiku-4-5":{"contextWindow":200000,"inputTokens":800},` +
		`"claude-opus-4-8[1m]":{"contextWindow":1000000,"inputTokens":48000}` +
		`}}`

	// Run several times since map iteration order is randomized; the result
	// must be stable.
	for i := 0; i < 20; i++ {
		events := ParseStreamLine([]byte(line))
		var cuEvt *agent.StreamEvent
		for j := range events {
			if events[j].Kind == agent.KindContextUsage {
				cuEvt = &events[j]
				break
			}
		}
		if cuEvt == nil {
			t.Fatalf("iter %d: missing context_usage event", i)
			return
		}
		var cu map[string]int
		if err := json.Unmarshal([]byte(cuEvt.Content), &cu); err != nil {
			t.Fatalf("iter %d: unmarshal: %v", i, err)
		}
		if cu["total"] != 1000000 {
			t.Fatalf("iter %d: expected total=1000000 (main Opus 1M model), got %d", i, cu["total"])
		}
	}
}

func TestParseStreamLine_ResultFallsBackTo1MContextWindow(t *testing.T) {
	// CLI didn't include contextWindow in modelUsage; fall back to model-name
	// lookup. The "[1m]" suffix marks the 1M-context variant.
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":12345,"output_tokens":100},` +
		`"modelUsage":{"claude-sonnet-4-6[1m]":{"inputTokens":12345}}}`
	events := ParseStreamLine([]byte(line))
	var cuEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindContextUsage {
			cuEvt = &events[i]
			break
		}
	}
	if cuEvt == nil {
		t.Fatal("missing context_usage event")
		return
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(cuEvt.Content), &cu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cu["used"] != 12345 {
		t.Errorf("expected used=12345, got %d", cu["used"])
	}
	if cu["total"] != 1_000_000 {
		t.Errorf("expected fallback total=1000000 for [1m] model, got %d", cu["total"])
	}
}

func TestParseStreamLine_ResultFallsBackTo200KContextWindow(t *testing.T) {
	// Same fallback path, but for a non-[1m] model — should resolve to 200K.
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1000,"output_tokens":100},` +
		`"modelUsage":{"claude-opus-4-8":{"inputTokens":1000}}}`
	events := ParseStreamLine([]byte(line))
	var cuEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindContextUsage {
			cuEvt = &events[i]
			break
		}
	}
	if cuEvt == nil {
		t.Fatal("missing context_usage event")
		return
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(cuEvt.Content), &cu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cu["total"] != 200_000 {
		t.Errorf("expected fallback total=200000, got %d", cu["total"])
	}
}

func TestParseStreamLine_ResultFallsBackToMimoContextWindow(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1000,"output_tokens":100},` +
		`"modelUsage":{"mimo-v2.5-pro":{"inputTokens":1000}}}`
	events := ParseStreamLine([]byte(line))
	var cuEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindContextUsage {
			cuEvt = &events[i]
			break
		}
	}
	if cuEvt == nil {
		t.Fatal("missing context_usage event")
		return
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(cuEvt.Content), &cu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cu["total"] != 1_000_000 {
		t.Errorf("expected fallback total=1000000 for MiMo model, got %d", cu["total"])
	}
}

func TestParseStreamLine_ResultEmitsCacheBreakdown(t *testing.T) {
	// context_usage payload should now include input_tokens / cache_read /
	// cache_creation alongside the legacy used+total — the frontend tooltip
	// uses these to explain what's filling the window.
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":300,"output_tokens":50,` +
		`"cache_read_input_tokens":1200,"cache_creation_input_tokens":80},` +
		`"modelUsage":{"claude-opus-4-8":{"contextWindow":200000,"inputTokens":300}}}`
	events := ParseStreamLine([]byte(line))
	var cuEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindContextUsage {
			cuEvt = &events[i]
			break
		}
	}
	if cuEvt == nil {
		t.Fatal("missing context_usage event")
		return
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(cuEvt.Content), &cu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cu["used"] != 1580 {
		t.Errorf("expected used=1580 (300+1200+80), got %d", cu["used"])
	}
	if cu["input_tokens"] != 300 {
		t.Errorf("expected input_tokens=300, got %d", cu["input_tokens"])
	}
	if cu["cache_read"] != 1200 {
		t.Errorf("expected cache_read=1200, got %d", cu["cache_read"])
	}
	if cu["cache_creation"] != 80 {
		t.Errorf("expected cache_creation=80, got %d", cu["cache_creation"])
	}
}

func TestParseStreamLine_MessageStartEmitsLiveUsage(t *testing.T) {
	// stream_event/message_start carries usage at the start of every Anthropic
	// reply. We translate it into agent.KindLiveUsage (no total yet) so the
	// StreamProcessor can synthesize a full bar update without waiting until
	// end-of-turn `result`.
	line := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_1","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":40,"output_tokens":1,"cache_read_input_tokens":12000,` +
		`"cache_creation_input_tokens":80}}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d (%+v)", len(events), events)
	}
	if events[0].Kind != agent.KindLiveUsage {
		t.Fatalf("expected kind=%s, got %q", agent.KindLiveUsage, events[0].Kind)
	}
	var lu map[string]int
	if err := json.Unmarshal([]byte(events[0].Content), &lu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lu["used"] != 12120 {
		t.Errorf("expected used=12120 (40+12000+80), got %d", lu["used"])
	}
	if lu["cache_read"] != 12000 {
		t.Errorf("expected cache_read=12000, got %d", lu["cache_read"])
	}
}

func TestStreamProcessor_LiveUsageUsesLastTotal(t *testing.T) {
	// Sequence: system_init → result (sets lastTotal=1M) → message_start
	// (live_usage) → must synthesize agent.KindContextUsage with total=1M.
	p := NewStreamProcessor()

	systemInit := `{"type":"system","subtype":"init","model":"claude-opus-4-8[1m]"}`
	if got := p.Process([]byte(systemInit)); len(got) != 1 || got[0].Kind != agent.KindSystemInit {
		t.Fatalf("system_init: got %+v", got)
	}

	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":50000,"output_tokens":1000},` +
		`"modelUsage":{"claude-opus-4-8[1m]":{"contextWindow":1000000,"inputTokens":50000}}}`
	got := p.Process([]byte(result))
	// result yields context_usage + usage + result
	if len(got) != 3 {
		t.Fatalf("result events: expected 3, got %d", len(got))
	}

	// Now a follow-up turn's message_start arrives.
	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_2","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":99000,"output_tokens":1,"cache_read_input_tokens":50000}}}}`
	got = p.Process([]byte(msgStart))
	if len(got) != 1 {
		t.Fatalf("message_start synth: expected 1 event, got %d (%+v)", len(got), got)
	}
	if got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected synth kind=context_usage, got %q", got[0].Kind)
	}
	if !got[0].ModelCall {
		t.Fatal("message_start context usage must mark one real model call")
	}
	var cu map[string]int
	if err := json.Unmarshal([]byte(got[0].Content), &cu); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cu["used"] != 149000 {
		t.Errorf("expected used=149000, got %d", cu["used"])
	}
	if cu["total"] != 1_000_000 {
		t.Errorf("expected total=1000000 (carried from previous result), got %d", cu["total"])
	}
}

func TestStreamProcessor_LiveUsageFallsBackToModelName(t *testing.T) {
	// First-turn case: no result has yet set lastTotal. Processor must use
	// the system_init model name to look up a hard-coded window.
	p := NewStreamProcessor()
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-sonnet-4-6[1m]"}`))

	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_1","model":"claude-sonnet-4-6","usage":{"input_tokens":700,"output_tokens":1}}}}`
	got := p.Process([]byte(msgStart))
	if len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected synthesized context_usage, got %+v", got)
	}
	var cu map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &cu)
	if cu["total"] != 1_000_000 {
		t.Errorf("expected fallback total=1000000 from [1m] system_init, got %d", cu["total"])
	}
}

func TestStreamProcessor_LiveUsageKeepsModelCallWhenNoTotal(t *testing.T) {
	// No system_init, no prior result — we genuinely don't know the total.
	// There is no context bar to render, but the request still counts toward
	// the per-task model-call guardrail.
	p := NewStreamProcessor()
	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_1","usage":{"input_tokens":10}}}}`
	got := p.Process([]byte(msgStart))
	if len(got) != 1 || got[0].Kind != agent.KindModelCall {
		t.Errorf("expected only an internal model-call event when total is unknown, got %+v", got)
	}
}

// TestStreamProcessor_ResultContextUsageMergedWithLive locks down the
// fix for the "bar pinned at 100%" symptom. result.usage is cumulative
// across every internal Anthropic call in the turn; on multi-tool-iteration
// turns its {input,cache_read,cache_creation}_tokens sum exceeds the model
// context window and gets clamped to 100% by extractContextUsage. When a
// per-API-call message_start has already driven the live bar (the
// authoritative single-call snapshot), the cumulative event must be
// merged: keep result's `total` (the only authoritative window source
// — see TestStreamProcessor_ResultContextUsageMergeUsesResultTotal), but
// substitute live's per-call `used` + breakdown so the bar reflects the
// real end-of-turn input, not the summed-across-iterations figure.
func TestStreamProcessor_ResultContextUsageMergedWithLive(t *testing.T) {
	p := NewStreamProcessor()
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}`))

	// First API call of the turn: 30K input context.
	msgStart1 := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_1","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":5,"cache_read_input_tokens":25000,"cache_creation_input_tokens":5000}}}}`
	got := p.Process([]byte(msgStart1))
	if len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("msgStart1 should synthesize context_usage, got %+v", got)
	}
	var cu1 map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &cu1)
	if cu1["used"] != 30005 {
		t.Errorf("msgStart1 used: expected 30005, got %d", cu1["used"])
	}

	// Second API call of the same turn (tool iteration): 45K input context.
	// This is the authoritative end-of-turn snapshot.
	msgStart2 := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_2","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":3,"cache_read_input_tokens":40000,"cache_creation_input_tokens":5000}}}}`
	got = p.Process([]byte(msgStart2))
	if len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("msgStart2 should synthesize context_usage, got %+v", got)
	}
	var cu2 map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &cu2)
	if cu2["used"] != 45003 {
		t.Errorf("msgStart2 used: expected 45003, got %d", cu2["used"])
	}

	// Now the cumulative result lands.
	// Result's `usage` sums all internal Anthropic calls:
	//   input=8, cache_read=65000, cache_creation=10000 → sum 75008.
	// Real end-of-turn input was msgStart2's 45003. We assert the
	// emitted context_usage carries total=200000 (from modelUsage,
	// authoritative) but used=45003 (from live, not the cumulative
	// 75008). On bigger turns this same shape would push past 200K and
	// previously got clamped to "100%".
	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":8,"output_tokens":1000,` +
		`"cache_read_input_tokens":65000,"cache_creation_input_tokens":10000},` +
		`"modelUsage":{"claude-opus-4-8":{"contextWindow":200000,"inputTokens":8}}}`
	got = p.Process([]byte(result))
	if len(got) != 3 {
		t.Fatalf("expected 3 events (context_usage + usage + result), got %d (%+v)", len(got), got)
	}
	if got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected first event context_usage, got %s", got[0].Kind)
	}
	var merged map[string]int
	if err := json.Unmarshal([]byte(got[0].Content), &merged); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}
	if merged["total"] != 200000 {
		t.Errorf("merged total: expected 200000 (from result), got %d", merged["total"])
	}
	if merged["used"] != 45003 {
		t.Errorf("merged used: expected 45003 (from live, not cumulative 75008), got %d", merged["used"])
	}
	if merged["cache_read"] != 40000 {
		t.Errorf("merged cache_read: expected 40000 (from live), got %d", merged["cache_read"])
	}
	if merged["cache_creation"] != 5000 {
		t.Errorf("merged cache_creation: expected 5000 (from live), got %d", merged["cache_creation"])
	}
	if merged["input_tokens"] != 3 {
		t.Errorf("merged input_tokens: expected 3 (from live), got %d", merged["input_tokens"])
	}

	// After result the snapshot fields reset: a fresh turn's first
	// message_start should be the one that re-arms the merge for that
	// turn's result.
	msgStart3 := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_3","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":2,"cache_read_input_tokens":50000}}}}`
	got = p.Process([]byte(msgStart3))
	if len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("post-result msgStart should still synthesize live context_usage, got %+v", got)
	}
}

// TestStreamProcessor_ResultContextUsageMergeUsesResultTotal locks down the
// 200K-vs-1M correctness case. The CLI silently upgrades non-[1m] model
// names to the 1M tier — system_init reports "claude-opus-4-8" (no
// suffix) while modelUsage reports "claude-opus-4-8[1m]" with
// contextWindow=1000000. The live synth uses lookupContextWindow on
// system_init's name and gets 200K (wrong); only result has the right
// total. The merge must preserve result's total even when live's was
// stale, otherwise the bar would show used/200K on a 1M window for the
// rest of the first turn.
func TestStreamProcessor_ResultContextUsageMergeUsesResultTotal(t *testing.T) {
	p := NewStreamProcessor()
	// system_init reports the bare model name. Reproduces the case where
	// the user picked the non-[1m] entry from ProviderModels — the CLI
	// silently upgrades to 1M tier server-side but echoes back the bare
	// name, leaving the live bar to fall back to lookupContextWindow=200K
	// until the result event lands and the merge corrects it.
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}`))

	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_1","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":10,"cache_read_input_tokens":50000}}}}`
	got := p.Process([]byte(msgStart))
	if len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected synthesized context_usage, got %+v", got)
	}
	var live map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &live)
	if live["total"] != 200_000 {
		t.Fatalf("live should fall back to 200K from bare model name, got %d", live["total"])
	}
	if live["used"] != 50010 {
		t.Fatalf("live used: expected 50010, got %d", live["used"])
	}

	// Result reveals the true window: modelUsage has the [1m] suffix and
	// contextWindow=1M. The merge must keep this total (not live's stale
	// 200K) and the live's per-call used.
	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":10,"output_tokens":500,"cache_read_input_tokens":50000},` +
		`"modelUsage":{"claude-opus-4-8[1m]":{"contextWindow":1000000,"inputTokens":10}}}`
	got = p.Process([]byte(result))
	if len(got) != 3 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected 3 events with context_usage first, got %+v", got)
	}
	var merged map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &merged)
	if merged["total"] != 1_000_000 {
		t.Errorf("merged total: expected 1000000 (from result modelUsage), got %d", merged["total"])
	}
	if merged["used"] != 50010 {
		t.Errorf("merged used: expected 50010 (from live snapshot), got %d", merged["used"])
	}
}

// TestStreamProcessor_ModelSwitchMidSessionMergeFixesTotal covers the
// mid-session model switch path enabled by the per-conversation model
// picker (commit d696bc0). After a turn on Opus[1m] (lastTotal=1M), the
// next turn switches to a 200K Sonnet model. The live synth on the new
// turn's first message_start still sees p.lastTotal=1M (stale, from
// previous turn) and emits total=1M. The merge must override with
// result's authoritative 200K so the bar reflects the new window.
func TestStreamProcessor_ModelSwitchMidSessionMergeFixesTotal(t *testing.T) {
	p := NewStreamProcessor()

	// Turn 1 on a 1M model — seeds lastTotal=1M.
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8[1m]"}`))
	turn1Result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":10,"output_tokens":50},` +
		`"modelUsage":{"claude-opus-4-8[1m]":{"contextWindow":1000000,"inputTokens":10}}}`
	p.Process([]byte(turn1Result))
	if p.lastTotal != 1_000_000 {
		t.Fatalf("after turn1 lastTotal should be 1M, got %d", p.lastTotal)
	}

	// Turn 2: user switched the picker to Sonnet (200K).
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-sonnet-4-6"}`))

	// Live synth still uses the stale lastTotal=1M.
	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_2","model":"claude-sonnet-4-6","usage":{"input_tokens":40000}}}}`
	got := p.Process([]byte(msgStart))
	var live map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &live)
	if live["total"] != 1_000_000 {
		t.Fatalf("live should still show stale 1M total, got %d", live["total"])
	}

	// Result lands with the real 200K window. The merge must pick this
	// up so the bar updates from 40000/1M (4%) to 40000/200K (20%).
	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":40000,"output_tokens":100},` +
		`"modelUsage":{"claude-sonnet-4-6":{"contextWindow":200000,"inputTokens":40000}}}`
	got = p.Process([]byte(result))
	if len(got) != 3 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected 3 events with context_usage first, got %+v", got)
	}
	var merged map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &merged)
	if merged["total"] != 200_000 {
		t.Errorf("merged total: expected 200000 (from new model's result), got %d", merged["total"])
	}
	if merged["used"] != 40000 {
		t.Errorf("merged used: expected 40000 (from live snapshot), got %d", merged["used"])
	}
}

// TestStreamProcessor_PrimaryRespectsCurrentModelOverLargestTokens locks
// down the parent-vs-sub-agent primary-selection fix. Setup mirrors the
// real failure mode: user picked plain "claude-opus-4-8" (200K), and a
// turn invoked a heavy sub-agent that ran on "claude-opus-4-8[1m]". The
// sub-agent's inputTokens outweigh the parent's, so the legacy "largest
// wins" heuristic would flip the bar to /1M for that turn. Process must
// anchor on the parent's currentModel from system_init and emit /200K
// even though the [1m] sub-agent did more work.
func TestStreamProcessor_PrimaryRespectsCurrentModelOverLargestTokens(t *testing.T) {
	p := NewStreamProcessor()
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}`))

	// Live event from the parent: drives the bar during the turn.
	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_1","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":2,"cache_read_input_tokens":4998}}}}`
	if got := p.Process([]byte(msgStart)); len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected live context_usage, got %+v", got)
	}

	// Result reports two modelUsage entries: the parent (5K input tokens,
	// 200K window) and a sub-agent that ran Opus[1m] (60K input tokens,
	// 1M window). Largest-wins would pick the sub-agent and flip total
	// to 1M; the parent-model anchor must pick the parent's entry.
	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":2,"output_tokens":100,"cache_read_input_tokens":4998},` +
		`"modelUsage":{` +
		`"claude-opus-4-8":{"contextWindow":200000,"inputTokens":5000},` +
		`"claude-opus-4-8[1m]":{"contextWindow":1000000,"inputTokens":60000}` +
		`}}`
	got := p.Process([]byte(result))
	if len(got) != 3 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected 3 events with context_usage first, got %+v", got)
	}
	var merged map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &merged)
	if merged["total"] != 200_000 {
		t.Errorf("merged total: expected 200000 (parent's window), got %d — sub-agent's [1m] entry leaked into the parent's bar", merged["total"])
	}

	// Sanity: stateless ParseStreamLine still falls back to "largest
	// wins" — the model-aware behavior is opt-in through Process.
	stateless := ParseStreamLine([]byte(result))
	var statelessCU *agent.StreamEvent
	for i := range stateless {
		if stateless[i].Kind == agent.KindContextUsage {
			statelessCU = &stateless[i]
			break
		}
	}
	if statelessCU == nil {
		t.Fatal("stateless: missing context_usage")
		return
	}
	var cu map[string]int
	_ = json.Unmarshal([]byte(statelessCU.Content), &cu)
	if cu["total"] != 1_000_000 {
		t.Errorf("stateless: expected legacy largest-wins to pick [1m] (1M), got %d", cu["total"])
	}
}

// TestStreamProcessor_TransitionalClampUsesRawLive covers the 200K→1M
// silent-upgrade window. After a 200K turn primes lastTotal=200K, the
// next turn jumps to 1M and the very first message_start reports raw
// used=400K. synthesizeFromLive clamps that to lastTotal=200K for the
// emitted bar (we don't know yet that the window grew), but the
// snapshot we feed into the merge must preserve the raw 400K — when
// result lands with the authoritative 1M total, the merged event needs
// 400K/1M (40%), not the post-clamp 200K/1M (20%) that we'd get if the
// snapshot read from synth.Content.
func TestStreamProcessor_TransitionalClampUsesRawLive(t *testing.T) {
	p := NewStreamProcessor()
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}`))

	// Turn 1 on 200K — seeds lastTotal=200K.
	turn1 := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1000,"output_tokens":50},` +
		`"modelUsage":{"claude-opus-4-8":{"contextWindow":200000,"inputTokens":1000}}}`
	p.Process([]byte(turn1))
	if p.lastTotal != 200_000 {
		t.Fatalf("after turn1 lastTotal should be 200K, got %d", p.lastTotal)
	}

	// Turn 2: claude silently upgraded to 1M and the first message_start
	// arrives with raw used = 400K. synthesizeFromLive will clamp the
	// emitted bar to 200K (still using lastTotal), but the snapshot the
	// next merge consumes must hold the raw value.
	msgStart := `{"type":"stream_event","event":{"type":"message_start","message":{` +
		`"id":"msg_2","model":"claude-opus-4-8","usage":{` +
		`"input_tokens":1,"cache_read_input_tokens":399999}}}}`
	got := p.Process([]byte(msgStart))
	if len(got) != 1 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected synthesized context_usage, got %+v", got)
	}
	var clamped map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &clamped)
	if clamped["used"] != 200_000 {
		t.Errorf("during transition the live bar should still clamp to stale 200K, got used=%d", clamped["used"])
	}
	if p.lastLiveUsed != 400_000 {
		t.Errorf("snapshot must hold the raw pre-clamp value, got lastLiveUsed=%d", p.lastLiveUsed)
	}

	// Result reveals the 1M window. Merge should use raw 400K, not clamped.
	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1,"output_tokens":50,"cache_read_input_tokens":399999},` +
		`"modelUsage":{"claude-opus-4-8[1m]":{"contextWindow":1000000,"inputTokens":1}}}`
	got = p.Process([]byte(result))
	if len(got) != 3 || got[0].Kind != agent.KindContextUsage {
		t.Fatalf("expected 3 events with context_usage first, got %+v", got)
	}
	var merged map[string]int
	_ = json.Unmarshal([]byte(got[0].Content), &merged)
	if merged["total"] != 1_000_000 {
		t.Errorf("merged total: expected 1M from result, got %d", merged["total"])
	}
	if merged["used"] != 400_000 {
		t.Errorf("merged used: expected 400000 (raw live, not the 200K clamp), got %d", merged["used"])
	}
}

// TestStreamProcessor_ResultContextUsageKeptWhenNoLive covers the one-shot
// / older-CLI path: when no message_start arrived in the turn, the
// result-derived context_usage must still flow through so the bar shows
// *something*. The fix is conditional on a live event having been seen.
func TestStreamProcessor_ResultContextUsageKeptWhenNoLive(t *testing.T) {
	p := NewStreamProcessor()
	p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8"}`))

	result := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":2000,"output_tokens":50},` +
		`"modelUsage":{"claude-opus-4-8":{"contextWindow":200000,"inputTokens":2000}}}`
	got := p.Process([]byte(result))
	if len(got) != 3 {
		t.Fatalf("no-live path: expected 3 events (context_usage + usage + result), got %d", len(got))
	}
	if got[0].Kind != agent.KindContextUsage {
		t.Errorf("no-live path: expected first event context_usage, got %s", got[0].Kind)
	}
}

func TestParseStreamLine_RateLimitEvent(t *testing.T) {
	// Anthropic emits rate_limit_event in camelCase; we normalise to
	// snake_case so the WS payload matches the rest of our stream events.
	line := `{"type":"rate_limit_event","rate_limit_info":{` +
		`"status":"allowed","resetsAt":1778940000,"rateLimitType":"five_hour",` +
		`"overageStatus":"rejected","overageDisabledReason":"org_level_disabled",` +
		`"isUsingOverage":false},"uuid":"abc","session_id":"sess"}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindRateLimit {
		t.Fatalf("expected kind=%s, got %q", agent.KindRateLimit, events[0].Kind)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["type"] != "five_hour" {
		t.Errorf("expected type=five_hour, got %v", got["type"])
	}
	if got["status"] != "allowed" {
		t.Errorf("expected status=allowed, got %v", got["status"])
	}
	if got["resets_at"] != float64(1778940000) {
		t.Errorf("expected resets_at=1778940000, got %v", got["resets_at"])
	}
	if got["overage_status"] != "rejected" {
		t.Errorf("expected overage_status=rejected, got %v", got["overage_status"])
	}
	// is_using_overage:false should be omitted (we only set it when true).
	if _, present := got["is_using_overage"]; present {
		t.Errorf("expected is_using_overage to be omitted when false, got %v", got["is_using_overage"])
	}
}

func TestParseStreamLine_RateLimitEvent_UnifiedWindows(t *testing.T) {
	// Captured from claude-agent-sdk 0.3.287: one frame reports every plan
	// window, utilization as a 0–1 fraction.
	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1791111000,` +
		`"rateLimitType":"five_hour","isUsingOverage":false,"unifiedWindows":{` +
		`"five_hour":{"utilization":0.04,"resetsAt":1791111000},` +
		`"seven_day":{"utilization":0.375,"resetsAt":1791572400}}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 2 {
		t.Fatalf("expected one event per window, got %d", len(events))
	}
	var five, seven map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &five); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(events[1].Content), &seven); err != nil {
		t.Fatal(err)
	}
	if five["type"] != "five_hour" || five["status"] != "allowed" || five["utilization"] != float64(4) {
		t.Errorf("five_hour frame = %v", five)
	}
	if seven["type"] != "seven_day" || seven["resets_at"] != float64(1791572400) || seven["utilization"] != 37.5 {
		t.Errorf("seven_day frame = %v", seven)
	}
	// The secondary window's status is unknown; a guessed one could trip the
	// account cooldown.
	if _, ok := seven["status"]; ok {
		t.Errorf("seven_day frame must not carry a status, got %v", seven["status"])
	}
}

func TestParseStreamLine_RateLimitEvent_DropsIncomplete(t *testing.T) {
	// Missing rateLimitType — can't be attributed to a window, drop it.
	noType := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1778940000}}`
	if events := ParseStreamLine([]byte(noType)); len(events) != 0 {
		t.Errorf("expected 0 events when rateLimitType missing, got %d", len(events))
	}
	// Missing resetsAt — the countdown is the whole point, drop it.
	noResets := `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour"}}`
	if events := ParseStreamLine([]byte(noResets)); len(events) != 0 {
		t.Errorf("expected 0 events when resetsAt missing, got %d", len(events))
	}
}

func TestParseStreamLine_ResultWithCost(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"ok","total_cost_usd":0.05,"num_turns":2,"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":500,"cache_creation_input_tokens":200},"modelUsage":{"claude-opus-4-6":{"contextWindow":200000}}}`
	events := ParseStreamLine([]byte(line))

	var usageEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindUsage {
			usageEvt = &events[i]
			break
		}
	}
	if usageEvt == nil {
		t.Fatal("expected usage event")
		return
	}

	var usage map[string]any
	if err := json.Unmarshal([]byte(usageEvt.Content), &usage); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if usage["input_tokens"] != float64(100) {
		t.Errorf("expected input_tokens=100, got %v", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(50) {
		t.Errorf("expected output_tokens=50, got %v", usage["output_tokens"])
	}
	if usage["cache_read_input_tokens"] != float64(500) {
		t.Errorf("expected cache_read=500, got %v", usage["cache_read_input_tokens"])
	}
	if usage["total_cost_usd"] != 0.05 {
		t.Errorf("expected cost=0.05, got %v", usage["total_cost_usd"])
	}
	if usage["num_turns"] != float64(2) {
		t.Errorf("expected num_turns=2, got %v", usage["num_turns"])
	}
}

// TestParseStreamLine_ResultUsagePerModel verifies the per_model breakdown is
// surfaced in the agent.KindUsage payload so the handler's token-accounting path
// can attribute primary + sub-agent worker tokens to the right model rows.
func TestParseStreamLine_ResultUsagePerModel(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"ok","user_message_uuid":"turn-1",` +
		`"usage":{"input_tokens":100,"output_tokens":50},` +
		`"modelUsage":{` +
		`"claude-opus-4-8":{"contextWindow":200000,"inputTokens":80,"outputTokens":40,"costUSD":0.04},` +
		`"claude-haiku-4-5":{"contextWindow":200000,"inputTokens":20,"outputTokens":10,"costUSD":0.001}` +
		`}}`
	events := ParseStreamLine([]byte(line))

	var usageEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindUsage {
			usageEvt = &events[i]
			break
		}
	}
	if usageEvt == nil {
		t.Fatal("expected usage event")
		return
	}
	if usageEvt.TurnID != "turn-1" {
		t.Fatalf("usage TurnID = %q, want turn-1", usageEvt.TurnID)
	}
	var u struct {
		PerModel map[string]map[string]float64 `json:"per_model"`
	}
	if err := json.Unmarshal([]byte(usageEvt.Content), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(u.PerModel) != 2 {
		t.Fatalf("expected 2 models in per_model, got %d", len(u.PerModel))
	}
	opus := u.PerModel["claude-opus-4-8"]
	if opus["input_tokens"] != 80 || opus["output_tokens"] != 40 || opus["cost_usd"] < 0.039 {
		t.Errorf("opus row wrong: %+v", opus)
	}
	haiku := u.PerModel["claude-haiku-4-5"]
	if haiku["input_tokens"] != 20 || haiku["output_tokens"] != 10 {
		t.Errorf("haiku row wrong: %+v", haiku)
	}
}

// TestParseStreamLine_ResultUsageCacheCreationBreakdown locks in the
// 5m / 1h ephemeral split that Anthropic emits under usage.cache_creation.
// Without forwarding the sub-object the chat strip can't tell whether a
// 165k cache_creation_input_tokens spike was overwhelmingly cheap 5m
// writes or expensive 1h ones — which is exactly the gap that made a
// turn's cost look "off" relative to the visible token count.
func TestParseStreamLine_ResultUsageCacheCreationBreakdown(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":7,"output_tokens":273,` +
		`"cache_read_input_tokens":198056,"cache_creation_input_tokens":165418,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":165000,"ephemeral_1h_input_tokens":418}},` +
		`"total_cost_usd":1.1398,"num_turns":2}`
	events := ParseStreamLine([]byte(line))
	var usageEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindUsage {
			usageEvt = &events[i]
			break
		}
	}
	if usageEvt == nil {
		t.Fatal("expected usage event")
		return
	}
	var u struct {
		CacheCreation map[string]float64 `json:"cache_creation"`
	}
	if err := json.Unmarshal([]byte(usageEvt.Content), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.CacheCreation["ephemeral_5m_input_tokens"] != 165000 {
		t.Errorf("ephemeral_5m: got %v want 165000", u.CacheCreation["ephemeral_5m_input_tokens"])
	}
	if u.CacheCreation["ephemeral_1h_input_tokens"] != 418 {
		t.Errorf("ephemeral_1h: got %v want 418", u.CacheCreation["ephemeral_1h_input_tokens"])
	}
}

// TestParseStreamLine_ResultUsageNoCacheCreationSubobject covers the
// older-CLI fallback: when the cache_creation sub-object is absent the
// usage event must still parse cleanly with no `cache_creation` key
// rather than emitting an empty placeholder that the frontend would
// have to special-case.
func TestParseStreamLine_ResultUsageNoCacheCreationSubobject(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":80}}`
	events := ParseStreamLine([]byte(line))
	var usageEvt *agent.StreamEvent
	for i := range events {
		if events[i].Kind == agent.KindUsage {
			usageEvt = &events[i]
			break
		}
	}
	if usageEvt == nil {
		t.Fatal("expected usage event")
		return
	}
	var u map[string]any
	if err := json.Unmarshal([]byte(usageEvt.Content), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := u["cache_creation"]; ok {
		t.Errorf("cache_creation key should be absent when CLI omits the sub-object, got %v", u["cache_creation"])
	}
}

func TestParseStreamLine_SystemInit(t *testing.T) {
	line := `{"type":"system","subtype":"init","model":"claude-opus-4-6","session_id":"abc-123","tools":["Read","Write","Bash"]}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindSystemInit {
		t.Fatalf("expected kind=%s, got %q", agent.KindSystemInit, events[0].Kind)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info["model"] != "claude-opus-4-6" {
		t.Errorf("expected model=claude-opus-4-6, got %v", info["model"])
	}
	tools := info["tools"].([]any)
	if len(tools) != 3 {
		t.Errorf("expected 3 tools, got %d", len(tools))
	}
}

func TestParseStreamLine_ToolUseStart(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"Read"}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindToolUseStart {
		t.Fatalf("expected kind=%s, got %q", agent.KindToolUseStart, events[0].Kind)
	}
	var info map[string]string
	if err := json.Unmarshal([]byte(events[0].Content), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info["name"] != "Read" {
		t.Errorf("expected name=Read, got %q", info["name"])
	}
	if info["id"] != "toolu_123" {
		t.Errorf("expected id=toolu_123, got %q", info["id"])
	}
}

func TestParseStreamLine_ToolInputDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file\":"}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindToolInputDelta {
		t.Fatalf("expected kind=%s, got %q", agent.KindToolInputDelta, events[0].Kind)
	}
	if events[0].Content != `{"file":` {
		t.Errorf("expected partial json, got %q", events[0].Content)
	}
}

func TestParseStreamLine_ThinkingDelta(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindThinkingDelta {
		t.Fatalf("expected kind=%s, got %q", agent.KindThinkingDelta, events[0].Kind)
	}
	if events[0].Content != "Let me think..." {
		t.Errorf("expected thinking content, got %q", events[0].Content)
	}
}

func TestParseStreamLine_AssistantToolUse(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_abc","name":"Read","input":{"file_path":"/tmp/test.go"}}]}}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindToolResult {
		t.Fatalf("expected kind=%s, got %q", agent.KindToolResult, events[0].Kind)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info["name"] != "Read" {
		t.Errorf("expected name=Read, got %v", info["name"])
	}
	input := info["input"].(map[string]any)
	if input["file_path"] != "/tmp/test.go" {
		t.Errorf("expected file_path=/tmp/test.go, got %v", input["file_path"])
	}
}

func TestParseStreamLine_IgnoredTypes(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"system_non_init", `{"type":"system","subtype":"other"}`},
		{"rate_limit", `{"type":"rate_limit_event"}`},
		{"assistant_text_only", `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`},
		{"non_text_event", `{"type":"stream_event","event":{"type":"message_start"}}`},
		{"message_stop", `{"type":"stream_event","event":{"type":"message_stop"}}`},
		{"user_type", `{"type":"user"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ParseStreamLine([]byte(tt.line))
			if len(events) != 0 {
				t.Fatalf("expected no events, got %d: %+v", len(events), events)
			}
		})
	}
}

func TestParseStreamLine_InvalidJSON(t *testing.T) {
	events := ParseStreamLine([]byte("not json"))
	if len(events) != 0 {
		t.Fatalf("expected no events for invalid JSON, got %d", len(events))
	}
}

func TestParseStreamLine_SubagentFlag(t *testing.T) {
	tests := []struct {
		name string
		line string
		kind string
	}{
		{
			name: "system_init",
			line: `{"type":"system","subtype":"init","model":"claude-haiku-4-5","session_id":"sub","tools":["Read"],"parent_tool_use_id":"toolu_parent"}`,
			kind: agent.KindSystemInit,
		},
		{
			name: "delta",
			line: `{"type":"stream_event","parent_tool_use_id":"toolu_parent","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"sub"}}}`,
			kind: agent.KindDelta,
		},
		{
			name: "tool_use_start",
			line: `{"type":"stream_event","parent_tool_use_id":"toolu_parent","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_inner","name":"Read"}}}`,
			kind: agent.KindToolUseStart,
		},
		{
			name: "tool_result",
			line: `{"type":"assistant","parent_tool_use_id":"toolu_parent","message":{"content":[{"type":"tool_use","id":"toolu_inner","name":"Read","input":{"path":"x"}}]}}`,
			kind: agent.KindToolResult,
		},
		{
			name: "thinking_delta",
			line: `{"type":"stream_event","parent_tool_use_id":"toolu_parent","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"..."}}}`,
			kind: agent.KindThinkingDelta,
		},
		{
			name: "result",
			line: `{"type":"result","subtype":"success","parent_tool_use_id":"toolu_parent","result":"sub done"}`,
			kind: agent.KindResult,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := ParseStreamLine([]byte(tt.line))
			if len(events) == 0 {
				t.Fatalf("expected at least 1 event, got 0")
			}
			for _, evt := range events {
				if !evt.Subagent {
					t.Errorf("expected Subagent=true on %s, got false (kind=%s)", tt.name, evt.Kind)
				}
			}
			// Sanity check: at least one event of the expected kind exists.
			found := false
			for _, evt := range events {
				if evt.Kind == tt.kind {
					found = true
					break
				}
			}
			if !found {
				kinds := make([]string, len(events))
				for i, e := range events {
					kinds[i] = e.Kind
				}
				t.Errorf("expected kind=%s in events, got %v", tt.kind, kinds)
			}
		})
	}
}

func TestParseStreamLine_NoParentToolUseID_NotSubagent(t *testing.T) {
	line := `{"type":"system","subtype":"init","model":"claude-opus-4-8","tools":["Read"]}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Subagent {
		t.Errorf("expected Subagent=false on parent system_init")
	}
}

// TestStreamProcessor_InsertsSeparatorBetweenTextBlocks locks down the
// fix for the "headings render as raw text mid-stream" regression: when
// the model emits two text content_blocks in one turn (commonly a prose
// preamble followed by a list of "### N." sections, or text on either
// side of a tool_use), the frontend's accumulator naively concatenates
// them. Without a paragraph separator markdown-it sees "prose### 1."
// mid-line and refuses to recognise the heading. StreamProcessor
// injects "\n\n" into the first delta of a new block so the wire format
// matches what the model intended.
func TestStreamProcessor_InsertsSeparatorBetweenTextBlocks(t *testing.T) {
	p := NewStreamProcessor()

	// Block 0: prose ending without a trailing newline.
	first := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"prose."}}}`))
	if len(first) != 1 || first[0].Content != "prose." {
		t.Fatalf("first delta: expected unchanged %q, got %+v", "prose.", first)
	}

	// Same-block delta: must NOT be prefixed.
	same := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" continued"}}}`))
	if len(same) != 1 || same[0].Content != " continued" {
		t.Fatalf("same-block delta: expected unchanged %q, got %+v", " continued", same)
	}

	// New-block delta (index changed): MUST be prefixed with "\n\n" so
	// markdown-it sees "prose. continued\n\n### 1." and the heading
	// renders as <h3>.
	next := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"### 1. Heading"}}}`))
	if len(next) != 1 || next[0].Content != "\n\n### 1. Heading" {
		t.Fatalf("new-block delta: expected leading \\n\\n, got %q", next[0].Content)
	}

	// Result resets the per-turn tracker so the next turn doesn't ship a
	// stray leading "\n\n".
	res := p.Process([]byte(`{"type":"result","subtype":"success","result":"done"}`))
	if len(res) == 0 || res[len(res)-1].Kind != agent.KindResult {
		t.Fatalf("expected trailing result event, got %+v", res)
	}

	// New turn: first delta in a fresh stream must NOT carry a leading
	// separator regardless of its block index.
	again := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"next turn"}}}`))
	if len(again) != 1 || again[0].Content != "next turn" {
		t.Fatalf("post-result delta: expected unchanged %q, got %+v", "next turn", again)
	}
}

// TestStreamProcessor_SubagentDeltasUntouched ensures the parent's
// block-index tracking isn't disturbed by sub-agent deltas, and that
// sub-agent deltas don't get a "\n\n" splice (they live in the worker's
// own visual track on the frontend with its own buffer).
func TestStreamProcessor_SubagentDeltasUntouched(t *testing.T) {
	p := NewStreamProcessor()

	// Parent block 0.
	_ = p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"parent A"}}}`))

	// Sub-agent delta on a different block index: should NOT get
	// "\n\n" injected.
	sub := p.Process([]byte(`{"type":"stream_event","parent_tool_use_id":"toolu_x","event":{"type":"content_block_delta","index":5,"delta":{"type":"text_delta","text":"worker text"}}}`))
	if len(sub) != 1 || sub[0].Content != "worker text" || !sub[0].Subagent {
		t.Fatalf("subagent delta: expected unchanged subagent event, got %+v", sub)
	}

	// Parent's next delta (back on block 0) must not see the sub-agent's
	// excursion as a transition: same parent block as before, no separator.
	cont := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" continued"}}}`))
	if len(cont) != 1 || cont[0].Content != " continued" {
		t.Fatalf("parent same-block delta: expected unchanged, got %+v", cont)
	}
}

// TestStreamProcessor_InsertsSeparatorBetweenThinkingBlocks mirrors the
// text-block test for thinking_delta events — the same multi-block
// concatenation hazard applies to the thinking pill.
func TestStreamProcessor_InsertsSeparatorBetweenThinkingBlocks(t *testing.T) {
	p := NewStreamProcessor()

	a := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"first thought."}}}`))
	if len(a) != 1 || a[0].Content != "first thought." {
		t.Fatalf("first thinking delta: %+v", a)
	}
	b := p.Process([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"### Plan"}}}`))
	if len(b) != 1 || b[0].Content != "\n\n### Plan" {
		t.Fatalf("new-block thinking delta: expected \\n\\n prefix, got %q", b[0].Content)
	}
}

func TestStreamProcessor_SubagentSystemInitDoesNotOverwriteParentModel(t *testing.T) {
	p := NewStreamProcessor()
	parent := []byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8[1m]","tools":["Read"]}`)
	if events := p.Process(parent); len(events) != 1 {
		t.Fatalf("parent init: expected 1 event, got %d", len(events))
	}
	if p.currentModel != "claude-opus-4-8[1m]" {
		t.Fatalf("currentModel after parent init = %q, want %q", p.currentModel, "claude-opus-4-8[1m]")
	}

	sub := []byte(`{"type":"system","subtype":"init","model":"claude-haiku-4-5","parent_tool_use_id":"toolu_parent","tools":["Read"]}`)
	if events := p.Process(sub); len(events) != 1 || !events[0].Subagent {
		t.Fatalf("sub init: expected 1 subagent event, got %+v", events)
	}
	if p.currentModel != "claude-opus-4-8[1m]" {
		t.Errorf("sub-agent system_init overwrote parent currentModel: got %q", p.currentModel)
	}
}

func TestStreamProcessor_SubagentLiveUsageDropped(t *testing.T) {
	p := NewStreamProcessor()
	// Seed parent state via a parent system_init so synthesis would otherwise succeed.
	_ = p.Process([]byte(`{"type":"system","subtype":"init","model":"claude-opus-4-8","tools":["Read"]}`))
	sub := []byte(`{"type":"stream_event","parent_tool_use_id":"toolu_parent","event":{"type":"message_start","message":{"usage":{"input_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}}`)
	events := p.Process(sub)
	if len(events) != 1 || events[0].Kind != agent.KindModelCall || !events[0].Subagent {
		t.Fatalf("sub-agent message_start must keep exactly one accounting edge, got %+v", events)
	}
	for _, evt := range events {
		if evt.Kind == agent.KindContextUsage {
			t.Errorf("sub-agent live usage should not synthesize a context_usage; got %+v", evt)
		}
	}
}

func parseStreamLines(lines []string) []agent.StreamEvent {
	var events []agent.StreamEvent
	for _, line := range lines {
		events = append(events, ParseStreamLine([]byte(line))...)
	}
	return events
}

func joinDeltas(events []agent.StreamEvent) string {
	var parts []string
	for _, e := range events {
		if e.Kind == agent.KindDelta {
			parts = append(parts, e.Content)
		}
	}
	return strings.Join(parts, "")
}

func TestParseStreamLines(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-4-6","session_id":"s1","tools":["Read"]}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"2 "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"+ 2 = 4."}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","result":"2 + 2 = 4."}`,
	}

	events := parseStreamLines(lines)
	// system_init + delta + delta + result = 4
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Kind != agent.KindSystemInit {
		t.Fatalf("expected system_init, got %+v", events[0])
	}
	if events[1].Kind != agent.KindDelta || events[1].Content != "2 " {
		t.Fatalf("unexpected event: %+v", events[1])
	}
	if events[2].Kind != agent.KindDelta || events[2].Content != "+ 2 = 4." {
		t.Fatalf("unexpected event: %+v", events[2])
	}
	if events[3].Kind != agent.KindResult || events[3].Content != "2 + 2 = 4." {
		t.Fatalf("unexpected event: %+v", events[3])
	}

	joined := joinDeltas(events)
	if joined != "2 + 2 = 4." {
		t.Fatalf("expected joined=%q, got %q", "2 + 2 = 4.", joined)
	}
}

func TestParseStreamLines_WithToolUse(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-opus-4-6","session_id":"s1","tools":["Read"]}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"x"}}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done"}}}`,
		`{"type":"result","subtype":"success","result":"Done"}`,
	}

	events := parseStreamLines(lines)
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}

	expected := []string{
		agent.KindSystemInit,
		agent.KindToolUseStart,
		agent.KindToolInputDelta, agent.KindToolInputDelta,
		agent.KindToolResult,
		agent.KindDelta,
		agent.KindResult,
	}
	if len(kinds) != len(expected) {
		t.Fatalf("expected kinds %v, got %v", expected, kinds)
	}
	for i := range expected {
		if kinds[i] != expected[i] {
			t.Errorf("event[%d]: expected kind=%s, got %s", i, expected[i], kinds[i])
		}
	}
}

type mockRunner struct {
	events []agent.StreamEvent
	err    error
}

func (m *mockRunner) Run(ctx context.Context, _, _ string, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	for _, evt := range m.events {
		select {
		case outputCh <- evt:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.err
}

func TestMockRunner_StreamsEvents(t *testing.T) {
	runner := &mockRunner{events: []agent.StreamEvent{
		{Kind: agent.KindDelta, Content: "Hello"},
		{Kind: agent.KindDelta, Content: " World"},
		{Kind: agent.KindResult, Content: "Hello World"},
	}}
	ch := make(chan agent.StreamEvent, 10)
	err := runner.Run(context.Background(), "test", "", ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got []agent.StreamEvent
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
}

func TestMockRunner_CancelStopsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := &mockRunner{events: []agent.StreamEvent{
		{Kind: agent.KindDelta, Content: "Hello"},
	}}
	ch := make(chan agent.StreamEvent) // unbuffered so send blocks
	err := runner.Run(ctx, "test", "", ch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestClaudeRunner_SessionExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := "/home/alice/projects/daymug"
	sid := "abc-123"
	encoded := "-home-alice-projects-daymug"
	jsonl := filepath.Join(home, ".claude", "projects", encoded, sid+".jsonl")

	r := &runner{}

	if r.SessionExists(workDir, sid, "") {
		t.Fatal("expected false before jsonl is created")
	}

	if err := os.MkdirAll(filepath.Dir(jsonl), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(jsonl, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !r.SessionExists(workDir, sid, "") {
		t.Errorf("expected true once %s exists", jsonl)
	}

	// Trailing slash on workDir must normalize to the same encoded directory.
	if !r.SessionExists(workDir+"/", sid, "") {
		t.Error("expected true with trailing slash on workDir")
	}

	if r.SessionExists("", sid, "") {
		t.Error("expected false with empty workDir")
	}
	if r.SessionExists(workDir, "", "") {
		t.Error("expected false with empty sessionID")
	}
}

// SessionLogPath returns the deterministic on-disk path Claude CLI writes
// its session log to. Used by the retry-rollback path before the file
// exists, so it MUST NOT depend on os.Stat — pure path computation only.
func TestClaudeRunner_SessionLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workDir := "/home/alice/projects/daymug"
	sid := "abc-123"
	encoded := "-home-alice-projects-daymug"
	want := filepath.Join(home, ".claude", "projects", encoded, sid+".jsonl")

	r := &runner{}

	// File doesn't exist yet — path must still resolve so a caller can
	// snapshot "no file" state pre-launch and remove a failed first attempt
	// before retrying it.
	if got := r.SessionLogPath(workDir, sid, ""); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// claudeConfigDir override redirects the path to the per-account tree
	// so retry rollback writes under the right account's dir.
	alt := t.TempDir()
	wantAlt := filepath.Join(alt, "projects", encoded, sid+".jsonl")
	if got := r.SessionLogPath(workDir, sid, alt); got != wantAlt {
		t.Errorf("config dir override: got %q, want %q", got, wantAlt)
	}

	// Empty inputs return "" so callers know to skip rollback.
	if got := r.SessionLogPath("", sid, ""); got != "" {
		t.Errorf("empty workDir: got %q, want empty", got)
	}
	if got := r.SessionLogPath(workDir, "", ""); got != "" {
		t.Errorf("empty sessionID: got %q, want empty", got)
	}
}

// Regression for the multi-account bug: when CLAUDE_CONFIG_DIR is overridden
// per account, SessionExists must look under that dir — not ~/.claude.
// Otherwise switching a user's claude_account makes the resume probe see
// the old account's jsonl (or miss the new one), and the next send either
// trips "Session ID is already in use" or silently drops history.
func TestClaudeRunner_SessionExists_HonorsClaudeConfigDir(t *testing.T) {
	defaultHome := t.TempDir()
	t.Setenv("HOME", defaultHome)

	altDir := t.TempDir()
	workDir := "/home/alice/projects/daymug"
	sid := "abc-123"
	encoded := "-home-alice-projects-daymug"

	defaultJsonl := filepath.Join(defaultHome, ".claude", "projects", encoded, sid+".jsonl")
	altJsonl := filepath.Join(altDir, "projects", encoded, sid+".jsonl")

	r := &runner{}

	// Write only to the alt dir. The default-home probe must not find it.
	if err := os.MkdirAll(filepath.Dir(altJsonl), 0o755); err != nil {
		t.Fatalf("mkdir alt: %v", err)
	}
	if err := os.WriteFile(altJsonl, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write alt: %v", err)
	}

	if r.SessionExists(workDir, sid, "") {
		t.Error("default account must not see the alt account's jsonl")
	}
	if !r.SessionExists(workDir, sid, altDir) {
		t.Errorf("alt-dir probe should find %s", altJsonl)
	}

	// And vice versa: writing only under default home must be invisible
	// to a probe scoped to the alt account.
	if err := os.MkdirAll(filepath.Dir(defaultJsonl), 0o755); err != nil {
		t.Fatalf("mkdir default: %v", err)
	}
	if err := os.WriteFile(defaultJsonl, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write default: %v", err)
	}
	// Remove the alt jsonl so the alt probe must return false for the
	// new file we just wrote to default home.
	if err := os.Remove(altJsonl); err != nil {
		t.Fatalf("remove alt: %v", err)
	}
	if r.SessionExists(workDir, sid, altDir) {
		t.Error("alt account must not see the default account's jsonl")
	}
	if !r.SessionExists(workDir, sid, "") {
		t.Error("default-account probe should still find the default jsonl")
	}
}

// Regression for the "Session ID is already in use" bug: Claude CLI
// collapses every rune outside [A-Za-z0-9-] to a single "-" when
// computing the per-cwd directory name under ~/.claude/projects. Earlier
// the encoder only replaced "/", so any workDir containing ".", "_", or
// CJK runes pointed SessionExists at the wrong directory and the next
// send re-used --session-id over an existing jsonl. Empirically derived
// against Claude Code 2.1.131/133 (Node) on Linux.
func TestClaudeRunner_SessionExists_EncodingMatchesClaudeCLI(t *testing.T) {
	cases := []struct {
		name    string
		workDir string
		encoded string
	}{
		{"cjk_segment", "/home/alice/work/运营", "-home-alice-work---"},
		{"hidden_dir", "/home/alice/.daymug", "-home-alice--daymug"},
		{"underscore", "/tmp/probe_test_GOrNc6", "-tmp-probe-test-GOrNc6"},
		{"mixed", "/srv/data/v1.2/项目_a", "-srv-data-v1-2----a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			sid := "abc-123"
			jsonl := filepath.Join(home, ".claude", "projects", tc.encoded, sid+".jsonl")

			r := &runner{}
			if r.SessionExists(tc.workDir, sid, "") {
				t.Fatal("expected false before jsonl is created")
			}

			if err := os.MkdirAll(filepath.Dir(jsonl), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(jsonl, []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			if !r.SessionExists(tc.workDir, sid, "") {
				t.Errorf("expected true once %s exists", jsonl)
			}
		})
	}
}

// Regression for the silent stream truncation that swallowed assistant
// responses after a Read on a >1 MiB PDF. The previous implementation used
// bufio.Scanner with a 1 MiB cap; once a tool_result line exceeded that cap
// every event after it (the assistant's reply, the result frame) was lost
// without any error surfacing. ReadBytes has no per-line cap, so events on
// later lines must still arrive.
func TestStreamClaudeLines_PreservesEventsAfterOversizedLine(t *testing.T) {
	var buf bytes.Buffer
	huge := strings.Repeat("a", 2*1024*1024) // 2 MiB — past the old 1 MiB cap
	fmt.Fprintf(&buf,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":%q}]}}`+"\n",
		huge,
	)
	buf.WriteString(`{"type":"result","result":"final answer"}` + "\n")

	ch := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- streamcommon.StreamLines(context.Background(), &buf, ch, lineStream(NewStreamProcessor()))
		close(ch)
	}()

	var got []agent.StreamEvent
	for evt := range ch {
		got = append(got, evt)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("streamLines: %v", err)
	}

	var sawResult bool
	for _, e := range got {
		if e.Kind == agent.KindResult && e.Content == "final answer" {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatalf("expected agent.KindResult after oversized line; got %d events: %+v", len(got), got)
	}
}

// Sanity: ReadBytes must surface IO errors instead of silently halting the
// stream (the old Scanner path swallowed bufio.ErrTooLong the same way).
func TestStreamClaudeLines_PropagatesReadError(t *testing.T) {
	want := fmt.Errorf("synthetic read failure")
	r := &errReader{err: want}
	ch := make(chan agent.StreamEvent, 4)
	errCh := make(chan error, 1)
	go func() {
		errCh <- streamcommon.StreamLines(context.Background(), r, ch, lineStream(NewStreamProcessor()))
		close(ch)
	}()

	for range ch {
		// drain
	}
	select {
	case got := <-errCh:
		if got == nil || !strings.Contains(got.Error(), want.Error()) {
			t.Fatalf("expected wrapped %q, got %v", want, got)
		}
	case <-time.After(time.Second):
		t.Fatal("streamLines did not return")
	}
}

// Regression for the MiMo "no response, forever" bug: when the underlying
// CLI accepts the request but never produces stdout (the upstream MiMo
// gateway has been seen to hold the connection open while the model is
// degraded), streamLines must surface a user-visible upstream-unreachable
// error within the stall budget instead of blocking the conversation
// forever on a silent ReadBytes.
func TestStreamClaudeLines_StallTimeoutSurfacesUpstreamError(t *testing.T) {
	ch := make(chan agent.StreamEvent, 4)
	errCh := make(chan error, 1)
	go func() {
		cfg := lineStream(NewStreamProcessor())
		cfg.StallTimeout = 50 * time.Millisecond
		errCh <- streamcommon.StreamLines(context.Background(), &blockingReader{}, ch, cfg)
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
	case <-time.After(2 * time.Second):
		t.Fatal("streamLines did not return after stall timeout")
	}
}

// Sanity: a stream that keeps producing data within the stall window must
// drain to EOF cleanly — the stall watchdog must not fire while bytes are
// still arriving.
func TestStreamClaudeLines_StallTimerResetsOnEachLine(t *testing.T) {
	body := strings.Repeat(`{"type":"result","result":"ok"}`+"\n", 3)
	r := &slowReader{data: []byte(body), delay: 20 * time.Millisecond}
	ch := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		cfg := lineStream(NewStreamProcessor())
		cfg.StallTimeout = 100 * time.Millisecond
		errCh <- streamcommon.StreamLines(context.Background(), r, ch, cfg)
		close(ch)
	}()

	var results int
	for evt := range ch {
		if evt.Kind == agent.KindResult {
			results++
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("streamLines: %v", err)
	}
	if results != 3 {
		t.Fatalf("expected 3 result events, got %d", results)
	}
}

// TestStreamProcessor_CompatibleOverridesCostUSD verifies that a
// StreamProcessor bound to provider="claude-compatible" replaces the CLI's
// total_cost_usd (which Claude Code computes against Anthropic's catalog, not
// the third-party endpoint's) and rewrites every per_model row's cost_usd
// using the local pricing table. Model ids here are a real endpoint's, to
// keep the fixture honest about the catalog being someone else's.
//
// Every per_model figure is a session running total, so each row is priced
// on its own cumulative tokens and cache counters. The top-level cache
// counters are this turn's only and must not leak into a cumulative price.
func TestStreamProcessor_CompatibleOverridesCostUSD(t *testing.T) {
	pricing.Set("claude-compatible", pricing.Table{
		"mimo-v2.5-pro": {Input: 3.0, CachedInput: 0.3, Output: 15.0, CacheCreation5m: 3.75, CacheCreation1h: 6.0},
		"mimo-v2.5":     {Input: 0.8, CachedInput: 0.08, Output: 4.0},
	})
	t.Cleanup(pricing.ResetForTest)

	p := NewStreamProcessorForRun("claude-compatible", "mimo-v2.5-pro")

	// Result frame mirrors what the CLI emits for a compatible turn: per_model
	// includes the parent model AND a sub-agent worker on the cheaper
	// model. CLI-reported total_cost_usd is intentionally a wrong number
	// (5.0) — we expect enrichUsage to throw it away and compute from
	// the local table.
	line := []byte(`{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1000000,"output_tokens":1000000,` +
		`"cache_read_input_tokens":500000,"cache_creation_input_tokens":200000,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":150000,"ephemeral_1h_input_tokens":50000}},` +
		`"modelUsage":{"mimo-v2.5-pro":{"inputTokens":1000000,"outputTokens":1000000,"cacheReadInputTokens":600000,"cacheCreationInputTokens":400000,"costUSD":4.2},` +
		`"mimo-v2.5":{"inputTokens":50000,"outputTokens":80000,"costUSD":0.8}},` +
		`"total_cost_usd":5.0}`)

	var usageEvt *agent.StreamEvent
	for _, evt := range p.Process(line) {
		if evt.Kind == agent.KindUsage {
			usageEvt = &evt
			break
		}
	}
	if usageEvt == nil {
		t.Fatal("expected usage event")
		return
	}
	var got struct {
		TotalCostUSD float64                       `json:"total_cost_usd"`
		PerModel     map[string]map[string]float64 `json:"per_model"`
	}
	if err := json.Unmarshal([]byte(usageEvt.Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// With the pinned table (pro Input=3, CachedInput=0.3, Output=15,
	// Cache5m=3.75, Cache1h=6; mini Input=0.8, Output=4):
	//   primary (its own cumulative cache; modelUsage has no TTL split, so
	//   every cache write is priced at the 5m rate):
	//     1M  * 3.00 = 3.00
	//   + 0.6M * 0.30 = 0.18
	//   + 0.4M * 3.75 = 1.50
	//   + 1M  * 15.00 = 15.00
	//   = 19.68
	//   sub-agent (reported no cache):
	//     0.05M * 0.80 = 0.04
	//   + 0.08M * 4.00 = 0.32
	//   = 0.36
	const wantPro = 19.68
	const wantMini = 0.36
	const wantTotal = wantPro + wantMini
	approx := func(got, want float64) bool { d := got - want; return d < 1e-6 && d > -1e-6 }

	if !approx(got.TotalCostUSD, wantTotal) {
		t.Errorf("total_cost_usd = %v, want %v", got.TotalCostUSD, wantTotal)
	}
	if !approx(got.PerModel["mimo-v2.5-pro"]["cost_usd"], wantPro) {
		t.Errorf("per_model[mimo-v2.5-pro].cost_usd = %v, want %v",
			got.PerModel["mimo-v2.5-pro"]["cost_usd"], wantPro)
	}
	if !approx(got.PerModel["mimo-v2.5"]["cost_usd"], wantMini) {
		t.Errorf("per_model[mimo-v2.5].cost_usd = %v, want %v",
			got.PerModel["mimo-v2.5"]["cost_usd"], wantMini)
	}
	if _, leaked := got.PerModel["mimo-v2.5-pro"]["cache_read_input_tokens"]; leaked {
		t.Errorf("per_model gained a cumulative cache counter on the wire: %s", usageEvt.Content)
	}
	if report, _ := agent.UsageOf(*usageEvt); !report.Cumulative {
		t.Error("a per_model-priced compatible report must stay Cumulative")
	}
}

// Without modelUsage a compatible result carries only this turn's counters,
// so it is priced from them — honouring the TTL split — and must not be
// differenced as if it were a running total.
func TestStreamProcessor_CompatibleWithoutPerModelIsPerTurn(t *testing.T) {
	pricing.Set("claude-compatible", pricing.Table{
		"mimo-v2.5-pro": {Input: 3.0, CachedInput: 0.3, Output: 15.0, CacheCreation5m: 3.75, CacheCreation1h: 6.0},
	})
	t.Cleanup(pricing.ResetForTest)

	p := NewStreamProcessorForRun("claude-compatible", "mimo-v2.5-pro")
	line := []byte(`{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1000000,"output_tokens":1000000,` +
		`"cache_read_input_tokens":500000,"cache_creation_input_tokens":200000,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":150000,"ephemeral_1h_input_tokens":50000}},` +
		`"total_cost_usd":5.0}`)
	for _, evt := range p.Process(line) {
		report, ok := agent.UsageOf(evt)
		if !ok {
			continue
		}
		// 3.00 + 0.15 + 0.5625 + 0.30 + 15.00
		if d := report.Cost() - 19.0125; d > 1e-6 || d < -1e-6 {
			t.Errorf("total_cost_usd = %v, want 19.0125", report.Cost())
		}
		if report.Cumulative {
			t.Error("a report priced from per-turn counters must not be Cumulative")
		}
		return
	}
	t.Fatal("expected usage event")
}

// TestStreamProcessor_ClaudeProviderLeavesCostAlone is the inverse: a
// StreamProcessor bound to provider="claude" must NOT touch the CLI's
// reported cost. We trust Claude Code's total_cost_usd because it
// matches Anthropic billing exactly.
func TestStreamProcessor_ClaudeProviderLeavesCostAlone(t *testing.T) {
	p := NewStreamProcessorForRun("claude", "claude-opus-4-8")
	line := []byte(`{"type":"result","subtype":"success","result":"ok",` +
		`"usage":{"input_tokens":1000,"output_tokens":2000},"total_cost_usd":1.234}`)

	var usageEvt *agent.StreamEvent
	for _, evt := range p.Process(line) {
		if evt.Kind == agent.KindUsage {
			usageEvt = &evt
			break
		}
	}
	if usageEvt == nil {
		t.Fatal("expected usage event")
		return
	}
	var got struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal([]byte(usageEvt.Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TotalCostUSD != 1.234 {
		t.Errorf("claude provider altered cost: got %v, want 1.234", got.TotalCostUSD)
	}
}

type errReader struct{ err error }

func (e *errReader) Read(p []byte) (int, error) { return 0, e.err }

var _ io.Reader = (*errReader)(nil)

// blockingReader's Read never returns — used to simulate a CLI that has
// accepted the request and is now hung on an unresponsive upstream.
type blockingReader struct{}

func (blockingReader) Read(p []byte) (int, error) {
	select {}
}

// slowReader hands out the input in one-line chunks, sleeping `delay`
// before each line so the stall watchdog has reason to wait but never
// crosses the timeout boundary while bytes are still flowing.
type slowReader struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	end := s.pos
	for end < len(s.data) && s.data[end] != '\n' {
		end++
	}
	if end < len(s.data) {
		end++
	}
	n := copy(p, s.data[s.pos:end])
	s.pos += n
	return n, nil
}

// Every Fable release is a single 1M-context tier, so the bare id (no [1m]
// suffix) must resolve to 1M for the live context bar — otherwise it would hit
// the generic claude- 200K fallback and under-report the window until the
// result event.
func TestLookupContextWindow_Fable(t *testing.T) {
	cases := map[string]int{
		"claude-fable-5-1":    1_000_000,
		"claude-fable-5":      1_000_000,
		"claude-opus-4-8[1m]": 1_000_000,
		"claude-opus-4-8":     200_000,
		"mimo-v2.5-pro":       1_000_000,
	}
	for model, want := range cases {
		if got := lookupContextWindow(model); got != want {
			t.Errorf("lookupContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
}
