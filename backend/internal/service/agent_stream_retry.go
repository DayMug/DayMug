package service

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// transientRetryDelays is the backoff schedule for an upstream overload.
// Anthropic 529s typically clear within a few minutes, so three attempts
// spread over nine minutes cover the common outage without pinning a turn
// open indefinitely.
var transientRetryDelays = []time.Duration{time.Minute, 3 * time.Minute, 5 * time.Minute}

// transientRetrySleep is the clock seam so tests need not wait out the real
// schedule. Returns false when ctx ended first.
var transientRetrySleep = func(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// The backends hand us the upstream failure as free text — the Agent SDK
// throws an Error whose message embeds the status ("API Error: 529
// Overloaded") and the bridge prints that verbatim. There is no structured
// code to switch on, so classification is textual and deliberately narrow.
var (
	transientUpstreamRe = regexp.MustCompile(`(?i)(?:api\s+error|status(?:\s+code)?|http)\D{0,3}\b(500|502|503|504|529)\b|overloaded`)

	// Checked first. A usage limit is not an overload: it clears on the
	// account's reset schedule (hours, not minutes) and already has its own
	// handling in Pool.Cooldown, so retrying just burns the nine minutes and
	// reports the same failure later than it needed to.
	permanentUpstreamRe = regexp.MustCompile(`(?i)\b429\b|session limit|usage limit|rate.?limit|quota|invalid[_ ]api[_ ]key|authentication`)
)

// IsTransientUpstreamError reports whether err looks like a server-side
// overload worth retrying rather than a failure the user has to act on.
func IsTransientUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if permanentUpstreamRe.MatchString(msg) {
		return false
	}
	return transientUpstreamRe.MatchString(msg)
}

// Run executes the request's backend and consumes its stream to completion,
// retrying a transient upstream overload on the transientRetryDelays
// schedule. It returns only after the child process has exited, so callers
// may safely touch the session log afterwards.
//
// A retry is only attempted when the failed turn produced nothing the user
// could see; once any text, tool row, or question has landed, re-running
// would duplicate it rather than replace it.
func (s *AgentStreamer) Run(ctx context.Context, req AgentStreamRequest) AgentStreamResult {
	if req.UserInitiated && !req.Unbounded && s.GuardrailReminders != nil {
		if snapshot, ok := s.GuardrailReminders.Take(req.ConversationID); ok {
			s.emitGuardrailReminder(ctx, req, snapshot)
		}
	}
	guardrail := newTaskGuardrail(s.Guardrails)
	if guardrail.enabled() && req.UserInitiated && !req.Unbounded {
		if s.Store != nil && req.ConversationID != "" {
			cost, err := s.Store.ConversationCostUSD(ctx, req.ConversationID)
			if err != nil {
				log.Printf("[stream] load conversation cost for guardrail conv=%s: %v", req.ConversationID, err)
			} else {
				guardrail.setConversationCost(cost)
			}
		}
		req.guardrail = guardrail
		if s.GuardrailReminders != nil {
			defer func() {
				s.GuardrailReminders.Put(req.ConversationID, guardrail.reminder())
			}()
		}
	}
	var allArtifacts []Artifact
	var allRejections []ArtifactRejection
	var allContent strings.Builder
	attempt := 0
	for {
		remaining := len(transientRetryDelays) - attempt
		attemptReq := req
		attemptReq.willRetryTransient = func(err error, produced bool) bool {
			return remaining > 0 && !produced && IsTransientUpstreamError(err)
		}

		out := s.runOnce(ctx, attemptReq)
		appendAggregateChunk(&allContent, out.Content)
		allArtifacts = append(allArtifacts, out.Artifacts...)
		allRejections = append(allRejections, out.RejectedArtifacts...)
		out.Artifacts = append([]Artifact(nil), allArtifacts...)
		out.RejectedArtifacts = append([]ArtifactRejection(nil), allRejections...)
		out.Content = allContent.String()
		// runOnce reports the error itself unless it withheld it for us.
		if !out.retryTransient {
			return out
		}
		if !s.waitBeforeRetry(ctx, req, attempt, out) {
			return out
		}
		attempt++
	}
}

func (s *AgentStreamer) emitGuardrailReminder(ctx context.Context, req AgentStreamRequest, snapshot GuardrailSnapshot) {
	broadcast := req.Broadcast
	if broadcast == nil {
		broadcast = func(ServerMessage) {}
	}
	if req.OnGuardrail != nil {
		req.OnGuardrail(snapshot)
	}
	s.persistSessionWarning(ctx, req.ConversationID, broadcast, snapshot.Prompt, snapshot.notice())
}

// waitBeforeRetry parks the turn for one backoff interval. Returns false when
// the wait was cut short — the turn is over and the caller must stop.
func (s *AgentStreamer) waitBeforeRetry(ctx context.Context, req AgentStreamRequest, attempt int, out AgentStreamResult) bool {
	delay := transientRetryDelays[attempt]

	// Erase the failed attempt from the session log. The CLI appends the user
	// message the moment it receives stdin — before the API roundtrip that
	// 529'd — so without this the retry's --resume replays the prompt on top
	// of its own copy and the model sees it twice. A resident turn is exempt
	// for the same reason the cancel path exempts it: its process still holds
	// the file open, and truncating under a live writer desyncs the session.
	if out.rollbackPath != "" && !out.Resident {
		rollbackSessionLog(req.ConversationID, out.rollbackPath, out.rollbackOriginalSize)
	}

	// Give the account slot back for the wait. Holding it would let one
	// overloaded conversation block every other conversation bound to the
	// same account for up to nine minutes. The cost is that the retry
	// re-queues, so the real gap is the delay plus whatever the queue adds.
	req.SlotGate.Park()

	// Only the first wait is announced. It states the whole plan, so the
	// later waits have nothing to add — and an IM thread that gets one line
	// per attempt reads as three failures rather than one hiccup.
	if attempt == 0 {
		broadcast := req.Broadcast
		if broadcast == nil {
			broadcast = func(ServerMessage) {}
		}
		attempts := len(transientRetryDelays)
		s.persistNotice(ctx, req.ConversationID, broadcast, transientRetryNotice(delay, attempts), &Notice{
			Kind:         NoticeKindTransientRetry,
			DelaySeconds: int(delay / time.Second),
			MaxAttempts:  attempts,
		})
	}

	if !transientRetrySleep(ctx, delay) {
		return false
	}
	// Re-acquiring can block behind other turns on the account; a cancel
	// during that wait surfaces here as an error and ends the turn.
	return req.SlotGate.Resume(ctx) == nil
}

func transientRetryNotice(delay time.Duration, attempts int) string {
	return fmt.Sprintf(
		"上游服务过载，%s后自动重试（最多 %d 次）。期间无需操作，取消可随时中止。",
		formatRetryDelay(delay), attempts,
	)
}

func formatRetryDelay(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	}
	return d.String()
}
