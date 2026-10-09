package codexcli

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
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/*.golden.json from the current parser output")

// goldenEvent renders a StreamEvent so a golden diff reads as payload changes:
// JSON content is embedded as JSON, anything else stays a string.
type goldenEvent struct {
	Kind    string          `json:"kind"`
	JSON    json.RawMessage `json:"json,omitempty"`
	Content string          `json:"content,omitempty"`
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

// TestStreamGolden pins the exact tool and usage frames the `codex exec --json`
// transport emits. The app-server transport has a twin (codexapp
// TestEventsGolden) over the equivalent wire input, so moving the shared
// translation into codexcommon shows up here as a payload diff, not as a
// silently different chat card or bill.
func TestStreamGolden(t *testing.T) {
	pricing.ResetForTest()
	t.Cleanup(pricing.ResetForTest)
	big := strings.Repeat("x", 9000)
	cases := []struct {
		name  string
		model string
		lines []string
	}{
		{"command_started", "gpt-6-astra", []string{`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}`}},
		{"command_started_without_command", "gpt-6-astra", []string{`{"type":"item.started","item":{"id":"c1","type":"command_execution","status":"in_progress"}}`}},
		{"command_completed_with_output", "gpt-6-astra", []string{`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"bash -lc ls","aggregated_output":"  a\nb\n","exit_code":0,"status":"completed"}}`}},
		{"command_completed_empty_failed", "gpt-6-astra", []string{`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"false","aggregated_output":"","exit_code":2,"status":"failed"}}`}},
		{"command_completed_empty_no_status", "gpt-6-astra", []string{`{"type":"item.completed","item":{"id":"c1","type":"command_execution"}}`}},
		{"command_completed_large_output", "gpt-6-astra", []string{`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"cat big","aggregated_output":"` + big + `","exit_code":0,"status":"completed"}}`}},
		{"usage_last_with_context_window", "gpt-6-astra", []string{`{"id":"1","msg":{"type":"token_count","info":{"model":"gpt-6","model_context_window":258400,"last_token_usage":{"input_tokens":47630,"cached_input_tokens":46464,"output_tokens":190,"reasoning_output_tokens":64,"total_tokens":47820}}}}`}},
		{"usage_last_without_total_tokens", "gpt-6-astra", []string{`{"id":"1","msg":{"type":"token_count","info":{"model_context_window":1000,"last_token_usage":{"input_tokens":5000,"output_tokens":10}}}}`}},
		{"usage_cumulative_total_only", "gpt-6-astra", []string{
			`{"id":"1","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":200,"output_tokens":50,"total_tokens":1050}}}}`,
			`{"id":"2","msg":{"type":"token_count","info":{"total_token_usage":{"input_tokens":2500,"cached_input_tokens":1200,"output_tokens":80,"total_tokens":2580}}}}`,
		}},
		{"usage_turn_completed_twice", "gpt-6-astra", []string{
			`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":20}}`,
			`{"type":"turn.completed","usage":{"input_tokens":3000,"cached_input_tokens":1400,"output_tokens":50}}`,
		}},
		{"usage_without_model", "", []string{`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":20}}`}},
		{"usage_unpriced_model", "mystery-model", []string{`{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"output_tokens":20}}`}},
		{"usage_cached_exceeds_input", "gpt-6-astra", []string{`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":400,"output_tokens":20}}`}},
	}
	got := make(map[string][]goldenEvent, len(cases))
	for _, tc := range cases {
		p := NewStreamProcessor(tc.model)
		var events []agent.StreamEvent
		for _, line := range tc.lines {
			events = append(events, p.Process([]byte(line))...)
		}
		events = append(events, p.Flush()...)
		got[tc.name] = toGolden(events)
	}
	compareGolden(t, filepath.Join("testdata", "stream.golden.json"), got)
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
