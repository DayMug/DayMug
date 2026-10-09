package claudecli

import (
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// The usage JSON rides on every persisted assistant row, so moving from a
// map payload to agent.UsageReport must not change a byte of it. The want
// strings were captured from the map-based parser before the change.
func TestResultUsageWireIsUnchanged(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{
			name: "every field",
			line: `{"type":"result","subtype":"success","result":"ok","num_turns":3,"user_message_uuid":"u1","usage":{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":56,"cache_creation_input_tokens":78,"cache_creation":{"ephemeral_5m_input_tokens":70,"ephemeral_1h_input_tokens":8}},"modelUsage":{"claude-opus-5-5":{"inputTokens":100,"outputTokens":200,"cacheReadInputTokens":300,"cacheCreationInputTokens":400,"costUSD":0.5,"contextWindow":200000},"claude-haiku-5":{"inputTokens":1,"outputTokens":2,"costUSD":0}},"total_cost_usd":0}`,
			want: `{"cache_creation":{"ephemeral_1h_input_tokens":8,"ephemeral_5m_input_tokens":70},"cache_creation_input_tokens":78,"cache_read_input_tokens":56,"input_tokens":12,"num_turns":3,"output_tokens":34,"per_model":{"claude-haiku-5":{"cost_usd":0,"input_tokens":1,"output_tokens":2},"claude-opus-5-5":{"cost_usd":0.5,"input_tokens":100,"output_tokens":200}},"total_cost_usd":0}`,
		},
		{
			name: "bare",
			line: `{"type":"result","subtype":"success","result":"ok","usage":{"input_tokens":0,"output_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0}}}`,
			want: `{"input_tokens":0,"output_tokens":0}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, evt := range NewStreamProcessorForRun("claude", "claude-opus-5-5").Process([]byte(tc.line)) {
				if evt.Kind != agent.KindUsage {
					continue
				}
				if evt.Content != tc.want {
					t.Errorf("usage JSON\n got %s\nwant %s", evt.Content, tc.want)
				}
				if report, _ := agent.UsageOf(evt); !report.Cumulative {
					t.Error("Claude Code result usage must be declared Cumulative")
				}
				return
			}
			t.Fatal("no usage event")
		})
	}
}
