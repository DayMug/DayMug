package codexapp

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from the current mapper output")

// goldenEvent renders a StreamEvent so a golden diff reads as payload changes:
// JSON content is embedded as JSON, anything else stays a string.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	JSON    json.RawMessage `json:"json,omitempty"`
	Content string          `json:"content,omitempty"`
}

// TestEventsGolden pins the exact tool and usage frames the app-server
// transport emits, over wire input equivalent to codexcli's TestStreamGolden.
// Keeping the case names aligned makes the two golden files diffable against
// each other: whatever still differs is a deliberate transport difference.
func TestEventsGolden(t *testing.T) {
	pricing.ResetForTest()
	t.Cleanup(pricing.ResetForTest)
	big := strings.Repeat("x", 9000)
	cases := []struct {
		name     string
		provider string
		model    string
		method   string
		params   string
	}{
		{"command_started", config.CLITypeCodex, "gpt-6-astra", "item/started", `{"item":{"id":"c1","type":"commandExecution","command":"bash -lc ls","status":"inProgress"}}`},
		{"command_started_without_command", config.CLITypeCodex, "gpt-6-astra", "item/started", `{"item":{"id":"c1","type":"commandExecution","status":"inProgress"}}`},
		{"command_completed_with_output", config.CLITypeCodex, "gpt-6-astra", "item/completed", `{"item":{"id":"c1","type":"commandExecution","command":"bash -lc ls","aggregatedOutput":"  a\nb\n","exitCode":0,"status":"completed"}}`},
		{"command_completed_empty_failed", config.CLITypeCodex, "gpt-6-astra", "item/completed", `{"item":{"id":"c1","type":"commandExecution","command":"false","aggregatedOutput":"","exitCode":2,"status":"failed"}}`},
		{"command_completed_empty_no_status", config.CLITypeCodex, "gpt-6-astra", "item/completed", `{"item":{"id":"c1","type":"commandExecution"}}`},
		{"command_completed_large_output", config.CLITypeCodex, "gpt-6-astra", "item/completed", `{"item":{"id":"c1","type":"commandExecution","command":"cat big","aggregatedOutput":"` + big + `","exitCode":0,"status":"completed"}}`},
		{"usage_last_with_context_window", config.CLITypeCodex, "gpt-6-astra", usageNotification, `{"turnId":"u","tokenUsage":{"modelContextWindow":258400,"last":{"inputTokens":47630,"cachedInputTokens":46464,"outputTokens":190,"reasoningOutputTokens":64,"totalTokens":47820}}}`},
		{"usage_last_without_total_tokens", config.CLITypeCodex, "gpt-6-astra", usageNotification, `{"tokenUsage":{"modelContextWindow":1000,"last":{"inputTokens":5000,"outputTokens":10}}}`},
		{"usage_without_context_window", config.CLITypeCodex, "gpt-6-astra", usageNotification, `{"tokenUsage":{"last":{"inputTokens":1000,"cachedInputTokens":400,"outputTokens":20}}}`},
		{"usage_without_model", config.CLITypeCodex, "", usageNotification, `{"tokenUsage":{"last":{"inputTokens":1000,"cachedInputTokens":400,"outputTokens":20}}}`},
		{"usage_unpriced_model", config.CLITypeCodex, "mystery-model", usageNotification, `{"tokenUsage":{"last":{"inputTokens":1000,"cachedInputTokens":400,"outputTokens":20}}}`},
		{"usage_openai_compatible_unpriced", config.CLITypeOpenAICompatible, "gpt-6-astra", usageNotification, `{"tokenUsage":{"last":{"inputTokens":1000,"cachedInputTokens":400,"outputTokens":20}}}`},
		{"usage_cached_exceeds_input", config.CLITypeCodex, "gpt-6-astra", usageNotification, `{"tokenUsage":{"last":{"inputTokens":100,"cachedInputTokens":400,"outputTokens":20}}}`},
	}
	got := make(map[string][]goldenEvent, len(cases))
	for _, tc := range cases {
		events, _, err := mapNotification(tc.provider, wireMessage{Method: tc.method, Params: json.RawMessage(tc.params)}, tc.model)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got[tc.name] = toGolden(events)
	}
	compareGolden(t, filepath.Join("testdata", "events.golden.json"), got)
}

func toGolden(events []agent.StreamEvent) []goldenEvent {
	out := make([]goldenEvent, 0, len(events))
	for _, evt := range events {
		g := goldenEvent{Kind: evt.Kind}
		if json.Valid([]byte(evt.Content)) && strings.HasPrefix(strings.TrimSpace(evt.Content), "{") {
			g.JSON = json.RawMessage(evt.Content)
		} else {
			g.Content = evt.Content
		}
		out = append(out, g)
	}
	return out
}

func compareGolden(t *testing.T, path string, got any) {
	t.Helper()
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if *updateGolden {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatalf("read %s (run with -update-golden to create it): %v", path, err)
	}
	if !bytes.Equal(want, encoded) {
		t.Fatalf("%s is out of date; diff it against:\n%s", path, encoded)
	}
}
