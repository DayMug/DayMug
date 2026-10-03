package codexcommon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
)

func decode(t *testing.T, evt agent.StreamEvent) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(evt.Content), &payload); err != nil {
		t.Fatalf("%s payload is not JSON: %v", evt.Kind, err)
	}
	return payload
}

func TestCommandCompletedNamesStatusWhenOutputIsEmpty(t *testing.T) {
	exit := 2
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"with status", Command{ID: "c", Status: "failed", ExitCode: &exit}, "(failed, exit 2)"},
		{"without status", Command{ID: "c"}, "(exit 0)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := decode(t, CommandCompleted(tc.cmd)[0])
			if payload["content"] != tc.want {
				t.Errorf("content = %v, want %q", payload["content"], tc.want)
			}
			if _, ok := payload["input"].(map[string]any)["command"]; ok {
				t.Errorf("an unknown command must be omitted, not sent empty: %v", payload["input"])
			}
		})
	}
}

func TestCommandCompletedTruncatesLongOutput(t *testing.T) {
	payload := decode(t, CommandCompleted(Command{ID: "c", AggregatedOutput: strings.Repeat("x", 3*ToolOutputLimit)})[0])
	content, _ := payload["content"].(string)
	if len(content) > ToolOutputLimit+32 || !strings.HasSuffix(content, "(truncated)") {
		t.Errorf("content length %d, want capped near %d and marked truncated", len(content), ToolOutputLimit)
	}
}

func TestUsageEventsPricesAgainstTheProvidersTable(t *testing.T) {
	pricing.Set(config.CLITypeOpenAICompatible, pricing.Table{"m": {Input: 1, CachedInput: 0.5, Output: 2}})
	t.Cleanup(pricing.ResetForTest)

	events := UsageEvents(config.CLITypeOpenAICompatible, "m", TokenUsage{
		InputTokens: 1_000_000, CachedInputTokens: 400_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000,
	}, 0)
	if len(events) != 1 || events[0].Kind != agent.KindUsage {
		t.Fatalf("events = %+v, want exactly one usage frame without a context window", events)
	}
	payload := decode(t, events[0])
	// 600k uncached × $1 + 400k cached × $0.5 + 1M output × $2 = $2.80.
	if payload["input_tokens"] != float64(600_000) || payload["total_cost_usd"] != 2.8 {
		t.Errorf("payload = %v, want net input 600000 and cost 2.8", payload)
	}
}
