package codexapp

import (
	"encoding/json"
)

// usageNotification is the app-server method that reports token spend.
const usageNotification = "thread/tokenUsage/updated"

// Codex reports usage as `tokenUsage.last` — the numbers for the last
// *completed* turn on the thread, not necessarily this one. A turn that is
// interrupted before it spends anything therefore receives a notification
// restating the previous turn's figures, and billing them again would charge
// the account twice for one answer.
//
// Two gates, either of which is sufficient:
//
//  1. The notification names its turn (`turnId`, required by the app-server
//     schema). A different id is conclusive.
//  2. The thread's cumulative total has not moved past what it was when this
//     turn started. Nothing was spent, so `last` must describe an older turn.
//     A zero baseline means this process has never seen the thread (a new
//     thread, or a resume after the pool reclaimed the old server), and the
//     very first notification may legitimately already include this turn's
//     spend — so with no baseline only gate 1 applies.
//
// Gate 2 exists because gate 1 is not available everywhere: the field is
// absent on older app-servers, and an empty id must not be read as "not mine".
type usageReport struct {
	// turnID is the turn the notification belongs to; empty when the server
	// does not report it.
	turnID string
	// cumulative is the thread's running total (tokenUsage.total.totalTokens).
	cumulative int
}

func parseUsageReport(raw json.RawMessage) (usageReport, bool) {
	var p struct {
		TurnID     string `json:"turnId"`
		TokenUsage struct {
			Total struct {
				Total int `json:"totalTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return usageReport{}, false
	}
	return usageReport{turnID: p.TurnID, cumulative: p.TokenUsage.Total.Total}, true
}

// acceptUsage reports whether a usage notification describes turnID, and
// records its cumulative total either way — a rejected report still tells us
// where the thread's counter stands.
func (s *accountServer) acceptUsage(threadID, turnID string, raw json.RawMessage) bool {
	report, ok := parseUsageReport(raw)
	if !ok {
		// Unparseable params are not evidence of staleness; let the mapper
		// decide what, if anything, it can make of them.
		return true
	}
	baseline := s.usageBaseline(threadID)
	s.noteUsageTotal(threadID, report.cumulative)

	if report.turnID != "" && turnID != "" && report.turnID != turnID {
		return false
	}
	if baseline > 0 && report.cumulative > 0 && report.cumulative <= baseline {
		return false
	}
	return true
}

// usageBaseline returns the cumulative token count this process last saw for
// the thread. Zero means "never seen".
func (s *accountServer) usageBaseline(threadID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadTotals[threadID]
}

// noteUsageTotal records a thread's cumulative count, monotonically: an
// out-of-order notification must never drag the baseline backwards, or the
// next turn would start believing less was spent than really was.
func (s *accountServer) noteUsageTotal(threadID string, total int) {
	if total <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if total > s.threadTotals[threadID] {
		s.threadTotals[threadID] = total
	}
}
