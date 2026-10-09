package codexapp

import (
	"encoding/json"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

// rateLimitsNotification is account-scoped: it carries no threadId, so the
// dispatcher fans it out to every turn on the account's server instead of
// routing it to one thread.
const rateLimitsNotification = "account/rateLimits/updated"

// rateLimitWindowTypes maps codex's window length onto the window names the
// claude transports already use, so the UI renders both providers with one
// badge. Codex reports its windows as an unordered primary/secondary pair —
// a Pro account may have only the weekly one, in the primary slot — so the
// duration, not the slot, decides which row a window fills.
var rateLimitWindowTypes = map[int64]string{
	5 * 60:      "five_hour",
	7 * 24 * 60: "seven_day",
}

type codexRateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// rateLimitEvents turns one sparse account/rateLimits/updated snapshot into a
// KindRateLimit event per window it reports. No status is emitted: codex
// doesn't report one per window, and the service layer cools an account down
// on a "rejected" status — a guess here could lock out capacity it still has.
func rateLimitEvents(params json.RawMessage) []agent.StreamEvent {
	var p struct {
		RateLimits struct {
			Primary   *codexRateLimitWindow `json:"primary"`
			Secondary *codexRateLimitWindow `json:"secondary"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	var events []agent.StreamEvent
	for _, w := range []*codexRateLimitWindow{p.RateLimits.Primary, p.RateLimits.Secondary} {
		if w == nil || w.WindowDurationMins == nil || w.ResetsAt == nil || *w.ResetsAt == 0 {
			continue
		}
		kind, ok := rateLimitWindowTypes[*w.WindowDurationMins]
		if !ok {
			continue
		}
		events = append(events, streamcommon.Event(agent.KindRateLimit, map[string]any{
			"type":        kind,
			"resets_at":   *w.ResetsAt,
			"utilization": w.UsedPercent,
		})...)
	}
	return events
}
