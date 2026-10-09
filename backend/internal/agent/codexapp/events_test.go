package codexapp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcommon"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestMapNotificationAgentDelta(t *testing.T) {
	events, done, err := mapNotification("codex", wireMessage{
		Method: "item/agentMessage/delta",
		Params: json.RawMessage(`{"threadId":"t","turnId":"u","itemId":"i","delta":"hello"}`),
	}, "gpt-5.6-terra")
	if err != nil || done || len(events) != 1 {
		t.Fatalf("mapNotification = %#v, %v, %v", events, done, err)
	}
	if events[0].Kind != agent.KindDelta || events[0].Content != "hello" {
		t.Fatalf("event = %#v", events[0])
	}
}

func TestMapNotificationCommandLifecycle(t *testing.T) {
	started, _, _ := mapNotification("codex", wireMessage{Method: "item/started", Params: json.RawMessage(`{
        "item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","status":"inProgress"}
    }`)}, "")
	if len(started) != 1 || started[0].Kind != agent.KindToolUseStart || !strings.Contains(started[0].Content, "go test") {
		t.Fatalf("started = %#v", started)
	}

	completed, _, _ := mapNotification("codex", wireMessage{Method: "item/completed", Params: json.RawMessage(`{
        "item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","status":"completed","aggregatedOutput":"ok","exitCode":0}
    }`)}, "")
	if len(completed) != 1 || completed[0].Kind != agent.KindToolResult || !strings.Contains(completed[0].Content, `"exit_code":0`) {
		t.Fatalf("completed = %#v", completed)
	}
}

func TestMapNotificationUsageNormalizesCachedInputAndCost(t *testing.T) {
	events, _, err := mapNotification("codex", wireMessage{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{
		"threadId":"t","turnId":"u","tokenUsage":{"total":{"inputTokens":100,"cachedInputTokens":40,"outputTokens":20,"reasoningOutputTokens":5,"totalTokens":120},"last":{"inputTokens":100,"cachedInputTokens":40,"outputTokens":20,"reasoningOutputTokens":5,"totalTokens":120},"modelContextWindow":258400}
	}`)}, "gpt-5.6-terra")
	if err != nil || len(events) != 2 || events[0].Kind != agent.KindUsage || events[1].Kind != agent.KindContextUsage {
		t.Fatalf("usage = %#v, %v", events, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["input_tokens"] != float64(60) || payload["cache_read_input_tokens"] != float64(40) {
		t.Fatalf("normalized usage = %#v", payload)
	}
	if payload["total_cost_usd"] != float64(0.0005) || payload["per_model"] == nil {
		t.Fatalf("enriched usage = %#v", payload)
	}
	var contextPayload map[string]any
	if err := json.Unmarshal([]byte(events[1].Content), &contextPayload); err != nil {
		t.Fatal(err)
	}
	if contextPayload["used"] != float64(120) || contextPayload["total"] != float64(258400) || contextPayload["input_tokens"] != float64(60) || contextPayload["cache_read"] != float64(40) {
		t.Fatalf("context usage = %#v", contextPayload)
	}
}

func TestMapNotificationUsageOmitsContextWithoutAuthoritativeWindow(t *testing.T) {
	events, _, err := mapNotification("codex", wireMessage{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{
		"tokenUsage":{"last":{"inputTokens":100,"cachedInputTokens":40,"outputTokens":20}}
	}`)}, "gpt-5.6-terra")
	if err != nil || len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("usage = %#v, %v", events, err)
	}
}

func TestMapNotificationUsageFallsBackToInputWithoutTotalTokens(t *testing.T) {
	events, _, err := mapNotification("codex", wireMessage{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{
		"tokenUsage":{"last":{"inputTokens":100,"cachedInputTokens":40,"outputTokens":20},"modelContextWindow":1000}
	}`)}, "gpt-5.6-terra")
	if err != nil || len(events) != 2 || events[1].Kind != agent.KindContextUsage {
		t.Fatalf("usage = %#v, %v", events, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(events[1].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["used"] != float64(100) || payload["total"] != float64(1000) {
		t.Fatalf("context usage = %#v", payload)
	}
}

func TestMapNotificationTurnCompletion(t *testing.T) {
	_, done, err := mapNotification("codex", wireMessage{Method: "turn/completed", Params: json.RawMessage(`{
        "threadId":"t","turn":{"id":"u","status":"completed","items":[],"error":null}
    }`)}, "")
	if err != nil || !done {
		t.Fatalf("done = %v, err = %v", done, err)
	}
}

func TestMapNotificationUnexpectedTurnStatusFailsTheTurn(t *testing.T) {
	_, done, err := mapNotification("codex", wireMessage{Method: "turn/completed", Params: json.RawMessage(`{
        "threadId":"t","turn":{"id":"u","status":"inProgress"}
    }`)}, "")
	if !done {
		t.Fatal("turn/completed must always end the turn")
	}
	if err == nil || !strings.Contains(err.Error(), "inProgress") {
		t.Fatalf("error = %v, want the unexpected status surfaced instead of a clean completion", err)
	}
}

func TestMapNotificationRecognizesStructuredUnauthorized(t *testing.T) {
	msg := wireMessage{Method: "error", Params: json.RawMessage(`{"error":{"message":"token expired","codexErrorInfo":{"type":"Unauthorized","httpStatusCode":401}}}`)}
	_, _, err := mapNotification("codex", msg, "")
	if !errors.Is(err, errAuthenticationRequired) {
		t.Fatalf("error = %v, want authentication required", err)
	}
}

// codex reports each reconnect attempt as an "error" notification carrying
// willRetry=true while it keeps driving the turn; only the final one is a turn
// outcome. Treating the narration as fatal aborted a live turn and showed
// "Reconnecting... 2/5" in place of the real cause.
func TestMapNotificationRetryableErrorDoesNotEndTheTurn(t *testing.T) {
	retry := wireMessage{Method: "error", Params: json.RawMessage(`{
        "error":{"message":"Reconnecting... 2/5","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":402}}},
        "willRetry":true,"threadId":"t","turnId":"u"
    }`)}
	events, done, err := mapNotification("codex", retry, "")
	if len(events) != 0 || done {
		t.Fatalf("events = %#v, done = %v, want a silent non-terminal notice", events, done)
	}
	if !errors.Is(err, errStreamRetry) {
		t.Fatalf("error = %v, want a retry notice", err)
	}

	final := wireMessage{Method: "error", Params: json.RawMessage(`{
        "error":{"message":"unexpected status 402 Payment Required","codexErrorInfo":"other"},
        "willRetry":false,"threadId":"t","turnId":"u"
    }`)}
	if _, _, err := mapNotification("codex", final, ""); err == nil || errors.Is(err, errStreamRetry) {
		t.Fatalf("error = %v, want the terminal failure surfaced", err)
	} else if !strings.Contains(err.Error(), "402 Payment Required") {
		t.Fatalf("error = %v, want the upstream reason", err)
	}
}

// A 401 the server says it will retry is codex's own token refresh, not an
// account to evict from the pool: invalidating on the narration killed the
// server mid-refresh. The terminal frame still carries Unauthorized.
func TestMapNotificationRetryableUnauthorizedIsNotFatal(t *testing.T) {
	msg := wireMessage{Method: "error", Params: json.RawMessage(`{
        "error":{"message":"Reconnecting... 1/5","codexErrorInfo":{"type":"Unauthorized","httpStatusCode":401}},
        "willRetry":true
    }`)}
	_, _, err := mapNotification("codex", msg, "")
	if errors.Is(err, errAuthenticationRequired) {
		t.Fatalf("error = %v, want the retry to be left to codex", err)
	}
	if !errors.Is(err, errStreamRetry) {
		t.Fatalf("error = %v, want a retry notice", err)
	}
}

func TestMapNotificationFileChangeLifecycle(t *testing.T) {
	started, _, _ := mapNotification("codex", wireMessage{Method: "item/started", Params: json.RawMessage(`{
        "item":{"id":"patch-1","type":"fileChange","status":"inProgress","changes":[{"path":"/w/a.go","kind":"update","diff":"@@"}]}
    }`)}, "")
	if len(started) != 1 || started[0].Kind != agent.KindToolUseStart || !strings.Contains(started[0].Content, "ApplyPatch") {
		t.Fatalf("started = %#v", started)
	}

	completed, _, _ := mapNotification("codex", wireMessage{Method: "item/completed", Params: json.RawMessage(`{
        "item":{"id":"patch-1","type":"fileChange","status":"completed","changes":[{"path":"/w/a.go","kind":"update","diff":"@@"}]}
    }`)}, "")
	if len(completed) != 1 || completed[0].Kind != agent.KindToolResult {
		t.Fatalf("completed = %#v", completed)
	}
	if !strings.Contains(completed[0].Content, "/w/a.go") {
		t.Fatalf("file change result lost the path: %s", completed[0].Content)
	}
}

func TestMapNotificationMcpToolCallUsesClaudeStyleName(t *testing.T) {
	completed, _, _ := mapNotification("codex", wireMessage{Method: "item/completed", Params: json.RawMessage(`{
        "item":{"id":"mcp-1","type":"mcpToolCall","server":"notion","tool":"search","status":"completed","arguments":{"q":"x"},"result":{"content":[{"type":"text","text":"hit"}]}}
    }`)}, "")
	if len(completed) != 1 || completed[0].Kind != agent.KindToolResult {
		t.Fatalf("completed = %#v", completed)
	}
	if !strings.Contains(completed[0].Content, "mcp__notion__search") {
		t.Fatalf("mcp tool name = %s", completed[0].Content)
	}
}

func TestMapNotificationWebSearchIsVisible(t *testing.T) {
	started, _, _ := mapNotification("codex", wireMessage{Method: "item/started", Params: json.RawMessage(`{
        "item":{"id":"web-1","type":"webSearch","query":"golang generics"}
    }`)}, "")
	if len(started) != 1 || !strings.Contains(started[0].Content, "WebSearch") || !strings.Contains(started[0].Content, "generics") {
		t.Fatalf("started = %#v", started)
	}
}

func TestMapNotificationCompletedReasoningFallsBackToThinking(t *testing.T) {
	events, _, _ := mapNotification("codex", wireMessage{Method: "item/completed", Params: json.RawMessage(`{
        "item":{"id":"r-1","type":"reasoning","summary":["weighing options"],"content":["details"]}
    }`)}, "")
	if len(events) != 1 || events[0].Kind != agent.KindThinkingDelta {
		t.Fatalf("events = %#v", events)
	}
	if !strings.Contains(events[0].Content, "weighing options") {
		t.Fatalf("content = %q", events[0].Content)
	}
}

func TestMapItemTruncatesOversizedToolOutput(t *testing.T) {
	huge := strings.Repeat("x", codexcommon.ToolOutputLimit*2)
	payload, err := json.Marshal(map[string]any{"item": map[string]any{
		"id": "cmd-1", "type": "commandExecution", "command": "cat big", "status": "completed",
		"aggregatedOutput": huge, "exitCode": 0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	events, _, _ := mapNotification("codex", wireMessage{Method: "item/completed", Params: payload}, "")
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if len(events[0].Content) > 4*codexcommon.ToolOutputLimit {
		t.Fatalf("tool result content is %d bytes, want it bounded", len(events[0].Content))
	}
	if !strings.Contains(events[0].Content, "truncated") {
		t.Fatal("truncated output is not marked as such")
	}
}

// Every event this adapter produces is checked against agent.Validate, over
// the notification shapes codex actually sends. The mapper builds payloads by
// hand in a dozen branches; a Kind typo or a payload that stops being JSON in
// one of them is invisible until a chip renders blank in someone's browser.
func TestMappedEventsAreWellFormed(t *testing.T) {
	notifications := []wireMessage{
		{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"threadId":"t","itemId":"i","delta":"hello"}`)},
		{Method: "item/started", Params: json.RawMessage(`{"item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","status":"inProgress"}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","status":"completed","aggregatedOutput":"ok","exitCode":0}}`)},
		{Method: "item/started", Params: json.RawMessage(`{"item":{"id":"patch-1","type":"fileChange","status":"inProgress","changes":[{"path":"/w/a.go","kind":"update","diff":"@@"}]}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"id":"patch-1","type":"fileChange","status":"completed","changes":[{"path":"/w/a.go","kind":"update","diff":"@@"}]}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"id":"mcp-1","type":"mcpToolCall","server":"notion","tool":"search","status":"completed","arguments":{"q":"x"},"result":{"content":[{"type":"text","text":"hit"}]}}}`)},
		{Method: "item/started", Params: json.RawMessage(`{"item":{"id":"web-1","type":"webSearch","query":"golang generics"}}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"item":{"id":"msg-1","type":"agentMessage","text":"done"}}`)},
		{Method: usageNotification, Params: json.RawMessage(`{"threadId":"t","turnId":"u","tokenUsage":{"total":{"totalTokens":120},"last":{"inputTokens":100,"cachedInputTokens":40,"outputTokens":20,"totalTokens":120},"modelContextWindow":258400}}`)},
	}

	seen := 0
	for _, msg := range notifications {
		events, _, _ := mapNotification("codex", msg, "gpt-5.6-sol")
		for _, evt := range events {
			seen++
			if err := evt.Validate(); err != nil {
				t.Errorf("%s produced an invalid %s event: %v\ncontent: %s", msg.Method, evt.Kind, err, evt.Content)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no events were produced; this test is checking nothing")
	}
}

// The app-server serves both `codex` and `openai-compatible` over an
// identical wire protocol, so the only thing separating a turn billed at
// OpenAI's rates from one billed at a third-party endpoint's is the provider
// the Backend was built with. If that stops reaching the cost lookup, an
// openai-compatible turn silently inherits whatever the codex table says for
// a same-named model — a number that has nothing to do with the real bill.
func TestUsageEventsBillAgainstTheProvidersOwnTable(t *testing.T) {
	pricing.Set(config.CLITypeCodex, pricing.Table{
		"shared-id": {Input: 10, Output: 10},
	})
	pricing.Set(config.CLITypeOpenAICompatible, pricing.Table{
		"shared-id": {Input: 1, Output: 1},
	})
	t.Cleanup(pricing.ResetForTest)

	params := json.RawMessage(`{"tokenUsage":{"last":{"inputTokens":1000000,"outputTokens":0,"totalTokens":1000000}}}`)
	costFor := func(provider string) float64 {
		t.Helper()
		events, ok := usageEvents(provider, params, "shared-id")
		if !ok || len(events) == 0 {
			t.Fatalf("usageEvents(%q) produced nothing", provider)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
			t.Fatalf("decode usage payload: %v", err)
		}
		cost, _ := payload["total_cost_usd"].(float64)
		return cost
	}

	if got := costFor(config.CLITypeCodex); got != 10 {
		t.Errorf("codex cost = %v, want 10", got)
	}
	if got := costFor(config.CLITypeOpenAICompatible); got != 1 {
		t.Errorf("openai-compatible cost = %v, want 1", got)
	}
}

// The compatible variant must be the same Backend, not a degraded one: it
// steers and answers questions exactly like the first-party codex instance.
func TestCompatibleBackendKeepsTheCodexIdentityAndCapabilities(t *testing.T) {
	b := NewBackendForProvider(config.CLITypeOpenAICompatible)
	if got := agent.OptionalCapabilitiesOf(b); got != agent.OptionalCapabilitiesOf(NewBackend()) {
		t.Errorf("openai-compatible optional capabilities = %+v, want codex's", got)
	}
	if got := b.Name(); got != "openai-compatible-app-server" {
		t.Errorf("Name() = %q, want it to identify the compatible provider in logs", got)
	}
}
