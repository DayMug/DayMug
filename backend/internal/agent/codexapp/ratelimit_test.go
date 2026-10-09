package codexapp

import (
	"encoding/json"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
)

func decodeRateLimits(t *testing.T, events []agent.StreamEvent) []map[string]any {
	t.Helper()
	out := make([]map[string]any, 0, len(events))
	for _, evt := range events {
		if evt.Kind != agent.KindRateLimit {
			t.Fatalf("kind = %q, want %q", evt.Kind, agent.KindRateLimit)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(evt.Content), &payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, payload)
	}
	return out
}

func TestRateLimitWindowsAreNamedByDurationNotSlot(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   []map[string]any
	}{
		{
			// Captured from codex-cli 0.159.2 on a Pro login: the only window is
			// the weekly one, and it sits in the primary slot.
			name:   "weekly only in primary",
			params: `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1791588664},"secondary":null}}`,
			want:   []map[string]any{{"type": "seven_day", "resets_at": float64(1791588664), "utilization": float64(12)}},
		},
		{
			name:   "five hour and weekly",
			params: `{"rateLimits":{"primary":{"usedPercent":40,"windowDurationMins":300,"resetsAt":100},"secondary":{"usedPercent":7,"windowDurationMins":10080,"resetsAt":200}}}`,
			want: []map[string]any{
				{"type": "five_hour", "resets_at": float64(100), "utilization": float64(40)},
				{"type": "seven_day", "resets_at": float64(200), "utilization": float64(7)},
			},
		},
		{
			name:   "unknown duration and missing reset are skipped",
			params: `{"rateLimits":{"primary":{"usedPercent":40,"windowDurationMins":60,"resetsAt":100},"secondary":{"usedPercent":7,"windowDurationMins":10080,"resetsAt":null}}}`,
			want:   []map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, done, err := mapNotification("codex", wireMessage{Method: rateLimitsNotification, Params: json.RawMessage(tt.params)}, "")
			if err != nil || done {
				t.Fatalf("done=%v err=%v", done, err)
			}
			got := decodeRateLimits(t, events)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				for k, v := range tt.want[i] {
					if got[i][k] != v {
						t.Errorf("event %d %s = %v, want %v", i, k, got[i][k], v)
					}
				}
				if _, ok := got[i]["status"]; ok {
					t.Errorf("event %d carries a status; codex reports none per window", i)
				}
			}
		})
	}
}

// The account-wide quota update has no threadId; it must reach every turn on
// the server, and a full queue must skip it rather than fail that turn.
func TestRateLimitUpdateReachesEveryTurnWithoutFailingAFullOne(t *testing.T) {
	idle, full := newSubscription(), newSubscription()
	for range cap(full.ch) {
		full.ch <- wireMessage{}
	}
	srv := &accountServer{subs: map[string]map[*subscription]struct{}{
		"thread-a": {idle: {}},
		"thread-b": {full: {}},
	}}
	srv.broadcast(wireMessage{Method: rateLimitsNotification})

	if got := <-idle.ch; got.Method != rateLimitsNotification {
		t.Fatalf("idle turn got %q", got.Method)
	}
	select {
	case <-full.lost:
		t.Fatal("a skipped quota update must not mark the turn's stream as lost")
	default:
	}
}

func TestOnlyFirstPartyCodexAdvertisesRateLimits(t *testing.T) {
	if !NewBackend().Capabilities().SupportsRateLimitEvents {
		t.Error("first-party codex reports its plan windows and must advertise them")
	}
	if NewBackendForProvider(config.CLITypeOpenAICompatible).Capabilities().SupportsRateLimitEvents {
		t.Error("an openai-compatible endpoint has no ChatGPT plan windows")
	}
}
