package codexcli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// The tests below pin the de-duplication semantics of StreamProcessor's two
// accumulators (assistant text and reasoning text). Codex replays the whole
// assistant message in its terminal agent_message / task_complete frames after
// having already streamed it as deltas, so the processor has to decide — per
// frame — whether the payload is new text, a prefix replay, an exact repeat, or
// a partially overlapping replay. Those decisions read the accumulated value,
// which is exactly what makes the accumulator representation load-bearing:
// these tests exist so the storage can be changed without changing behaviour.

// collect drives Process over lines and appends Flush's tail, which is where a
// held result frame surfaces when no usage frame follows it.
func collect(p *StreamProcessor, lines ...string) []agent.StreamEvent {
	var out []agent.StreamEvent
	for _, line := range lines {
		out = append(out, p.Process([]byte(line))...)
	}
	return append(out, p.Flush()...)
}

func kindContents(events []agent.StreamEvent, kind string) []string {
	var out []string
	for _, evt := range events {
		if evt.Kind == kind {
			out = append(out, evt.Content)
		}
	}
	return out
}

func assertContents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d contents %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("content[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// quote escapes text so embedded newlines and quotes survive as JSON string
// content instead of producing a line the parser silently drops.
func quote(text string) string {
	b, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func deltaLine(text string) string {
	return `{"msg":{"type":"agent_message_delta","delta":` + quote(text) + `}}`
}

func messageLine(text string) string {
	return `{"msg":{"type":"agent_message","message":` + quote(text) + `}}`
}

func reasoningDeltaLine(text string) string {
	return `{"msg":{"type":"agent_reasoning_delta","delta":` + quote(text) + `}}`
}

func reasoningItemLine(text string) string {
	return `{"type":"item.completed","item":{"type":"reasoning","text":` + quote(text) + `}}`
}

func TestStreamProcessor_ResultCarriesConcatenatedDeltas(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		deltaLine("Hello "),
		deltaLine("brave "),
		deltaLine("world"),
		messageLine("Hello brave world"),
	)

	assertContents(t, kindContents(events, agent.KindDelta), "Hello ", "brave ", "world")
	// The replayed agent_message adds nothing new, so it must not spawn a
	// fourth delta, and the result carries the full accumulated turn.
	assertContents(t, kindContents(events, agent.KindResult), "Hello brave world")
}

func TestStreamProcessor_ResultBeyondDeltasEmitsRemainderOnly(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		deltaLine("Hello "),
		messageLine("Hello brave world"),
	)

	// Prefix replay: only the unsent tail is streamed to the client.
	assertContents(t, kindContents(events, agent.KindDelta), "Hello ", "brave world")
	assertContents(t, kindContents(events, agent.KindResult), "Hello brave world")
}

func TestStreamProcessor_DuplicateResultDoesNotDuplicateText(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		deltaLine("Answer: 42"),
		messageLine("Answer: 42"),
		// A second identical replay (item.completed after agent_message) is
		// swallowed by the Contains check rather than appended again.
		`{"type":"item.completed","item":{"type":"agent_message","text":"Answer: 42"}}`,
		`{"msg":{"type":"task_complete","last_agent_message":"Answer: 42"}}`,
	)

	assertContents(t, kindContents(events, agent.KindDelta), "Answer: 42")
	assertContents(t, kindContents(events, agent.KindResult), "Answer: 42")
}

func TestStreamProcessor_SubstringResultIsSwallowed(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		deltaLine("alpha beta gamma"),
		// "beta" already sits inside the accumulated text: Contains wins and
		// nothing is re-sent.
		messageLine("beta"),
	)

	assertContents(t, kindContents(events, agent.KindDelta), "alpha beta gamma")
	assertContents(t, kindContents(events, agent.KindResult), "alpha beta gamma")
}

func TestStreamProcessor_UnrelatedResultIsAppendedWithBlankLine(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		deltaLine("first block"),
		messageLine("second block"),
	)

	assertContents(t, kindContents(events, agent.KindDelta), "first block", "\n\nsecond block")
	assertContents(t, kindContents(events, agent.KindResult), "first block\n\nsecond block")
}

func TestStreamProcessor_TrailingOverlapReplaySendsOnlyNewTail(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	// Codex sometimes replays from the middle of the turn: the frame starts
	// with a suffix of what we already streamed, so only the bytes past the
	// overlap are new.
	events := collect(p,
		deltaLine("aaa bbb"),
		messageLine("bbb ccc"),
	)

	assertContents(t, kindContents(events, agent.KindDelta), "aaa bbb", " ccc")
	assertContents(t, kindContents(events, agent.KindResult), "aaa bbb ccc")
}

func TestStreamProcessor_EmptyResultKeepsAccumulatedText(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		deltaLine("streamed only"),
		// task_complete without last_agent_message yields an empty result; the
		// content must stay empty rather than being back-filled, so downstream
		// persistence falls back to the delta stream.
		`{"msg":{"type":"task_complete","last_agent_message":""}}`,
	)

	assertContents(t, kindContents(events, agent.KindDelta), "streamed only")
	assertContents(t, kindContents(events, agent.KindResult), "")
}

func TestStreamProcessor_ReasoningPrefixReplayEmitsRemainder(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		reasoningDeltaLine("Let me "),
		// Legacy codex replays the whole reasoning summary so far; only the
		// unsent tail should reach the client.
		reasoningDeltaLine("Let me think"),
		reasoningDeltaLine("Let me think harder"),
	)

	assertContents(t, kindContents(events, agent.KindThinkingDelta), "Let me ", "think", " harder")
}

func TestStreamProcessor_ReasoningExactRepeatIsDropped(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		reasoningDeltaLine("same"),
		reasoningDeltaLine("same"),
	)

	assertContents(t, kindContents(events, agent.KindThinkingDelta), "same")
}

func TestStreamProcessor_CompletedReasoningItemsSeparatedByBlankLine(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		reasoningItemLine("**First**"),
		reasoningItemLine("**Second**"),
	)

	assertContents(t, kindContents(events, agent.KindThinkingDelta), "**First**", "\n\n**Second**")
}

func TestStreamProcessor_CompletedReasoningItemKeepsSingleNewline(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		reasoningItemLine("first\n"),
		reasoningItemLine("second"),
	)

	// The accumulator already ends in one newline, so only one more is added.
	assertContents(t, kindContents(events, agent.KindThinkingDelta), "first\n", "\nsecond")
}

func TestStreamProcessor_ReasoningAndAssistantStreamsAreIndependent(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	events := collect(p,
		reasoningDeltaLine("plan: greet"),
		deltaLine("Hi"),
		reasoningDeltaLine("plan: greet then stop"),
		deltaLine(" there"),
		// Both replays must be resolved against their own accumulator: the
		// assistant text is not a prefix of the reasoning text and vice versa.
		messageLine("Hi there"),
	)

	assertContents(t, kindContents(events, agent.KindThinkingDelta), "plan: greet", " then stop")
	assertContents(t, kindContents(events, agent.KindDelta), "Hi", " there")
	assertContents(t, kindContents(events, agent.KindResult), "Hi there")
}

func TestStreamProcessor_ManyDeltasAccumulateExactly(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	chunk := strings.Repeat("x", 512)
	lines := make([]string, 0, 65)
	for range 64 {
		lines = append(lines, deltaLine(chunk))
	}
	want := strings.Repeat(chunk, 64)
	lines = append(lines, messageLine(want))

	events := collect(p, lines...)
	results := kindContents(events, agent.KindResult)
	if len(results) != 1 || results[0] != want {
		t.Fatalf("result did not equal the exact concatenation of 64 deltas (len %d)", len(results))
	}
	if got := len(kindContents(events, agent.KindDelta)); got != 64 {
		t.Errorf("got %d deltas, want 64 (the replayed message must add none)", got)
	}
}

func TestStreamProcessor_MultibyteTrailingOverlap(t *testing.T) {
	p := NewStreamProcessor("gpt-5")
	// The overlap scan only accepts UTF-8 boundaries, so a replay that starts
	// mid-rune must not be treated as an overlap.
	events := collect(p,
		deltaLine("你好世界"),
		messageLine("世界和平"),
	)

	assertContents(t, kindContents(events, agent.KindDelta), "你好世界", "和平")
	assertContents(t, kindContents(events, agent.KindResult), "你好世界和平")
}
