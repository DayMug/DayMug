package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// autoCompactLookupTimeout bounds the single conversation read the threshold
// check makes. It runs after every turn, so it must never be able to hang one.
const autoCompactLookupTimeout = 5 * time.Second

// autoCompactRunTimeout bounds the pre-flight portion of the triggered Compact.
// Compact roots its own (longer) timeouts for the CLI run and the writes that
// follow, deliberately detached from whatever context it was handed — see its
// doc comment. This one exists only so a wedged pre-flight read can't leak the
// goroutine and its in-flight entry forever.
const autoCompactRunTimeout = 2 * time.Minute

// autoCompactInFlight tracks conversations whose auto-compact goroutine is
// still running, keyed by conversation id.
//
// Compact's own StartJob claim already refuses a second concurrent run, but it
// only does so *after* the pre-flight reads and the pool wait. Without this
// gate a conversation that ends several turns in quick succession (the IM
// bridge does exactly that when a thread backfills) would queue several
// compacts, each burning an account slot just to lose the StartJob race. The
// gate is process-local on purpose: it guards goroutines this process spawned,
// and a restart legitimately starts fresh.
var autoCompactInFlight sync.Map

// contextUsagePayload is the subset of the persisted `context_usage` frame that
// the threshold check needs. Field names match what the CLI adapters emit and
// what conversations.last_context_usage stores verbatim.
type contextUsagePayload struct {
	Used  int64 `json:"used"`
	Total int64 `json:"total"`
}

// MaybeAutoCompact runs /compact in the background when the conversation's last
// recorded context usage is at or above Cfg.AutoCompactRatio.
//
// Call it AFTER the turn's EndJob, never before: Compact refuses while a job is
// in flight, so calling it from inside a run would just log a conflict every
// turn. Returns immediately — the compact itself is a full CLI child and can
// take minutes, which must not be charged to the turn that triggered it.
//
// Every skip path is silent except real failures. This runs after every single
// turn, so a chatty "not compacting yet" would drown the log.
func (r *PromptRunner) MaybeAutoCompact(convID string) {
	if convID == "" || r.Cfg == nil || r.Cfg.AutoCompactRatio <= 0 {
		return
	}
	if r.Store == nil {
		return
	}
	// Reading the row rather than the broadcaster cache: the cache is dropped
	// when the last client leaves the room, and the IM bridge has no client at
	// all. The row is written by PersistContextUsage on every usage frame.
	ctx, cancel := context.WithTimeout(context.Background(), autoCompactLookupTimeout)
	defer cancel()
	conv, err := r.Store.GetConversation(ctx, convID)
	if err != nil {
		return
	}
	ratio, ok := contextUsageRatio(conv.LastContextUsage)
	if !ok || ratio < r.Cfg.AutoCompactRatio {
		return
	}
	// Backends that can't compact would fail inside Compact with a 400 on every
	// turn once their context crossed the line. Check here so they stay silent.
	//
	// !ReportsContextUsage is checked for a different reason: such a backend
	// never overwrites last_context_usage, so the row read above can only be a
	// leftover from whatever transport wrote it last. Codex-over-CLI rows are
	// pinned at used==total by an older estimating build, which would read as
	// 100% and compact the conversation after every single turn.
	caps := agent.Capabilities{}
	if backend := r.backendFor(conv.Provider); backend != nil {
		caps = backend.Capabilities()
	}
	if !caps.SupportsCompaction || !caps.ReportsContextUsage {
		return
	}
	if _, loaded := autoCompactInFlight.LoadOrStore(convID, struct{}{}); loaded {
		return
	}

	go func() {
		defer autoCompactInFlight.Delete(convID)
		// Background context, not the turn's: the turn is already finished and
		// its context may be cancelled. Compact roots its own timeouts anyway;
		// this one only covers the pre-flight reads.
		runCtx, runCancel := context.WithTimeout(context.Background(), autoCompactRunTimeout)
		defer runCancel()
		// authedUID "" is the documented in-process bypass — there is no HTTP
		// caller to attribute this to, and ownership was never in question.
		if _, err := r.Compact(runCtx, convID, ""); err != nil {
			// A conflict means the user sent the next prompt before we got the
			// room. That is normal and self-correcting: the following turn
			// re-evaluates the same threshold.
			var svcErr *ServiceError
			if errors.As(err, &svcErr) && svcErr.Status == http.StatusConflict {
				return
			}
			log.Printf("[auto-compact] conv=%s ratio=%.2f: %v", convID, ratio, err)
			return
		}
		log.Printf("[auto-compact] conv=%s compacted at %.0f%% context", convID, ratio*100)
	}()
}

// contextUsageRatio extracts used/total from a persisted context_usage payload.
// Returns ok=false for anything unusable — empty string (no turn has reported
// usage yet), malformed JSON, or a non-positive total. Callers treat !ok as
// "don't compact", never as an error: a backend that reports no window size is
// not a failure, it just can't be measured.
func contextUsageRatio(payload string) (float64, bool) {
	if payload == "" {
		return 0, false
	}
	var usage contextUsagePayload
	if err := json.Unmarshal([]byte(payload), &usage); err != nil {
		return 0, false
	}
	if usage.Total <= 0 || usage.Used <= 0 {
		return 0, false
	}
	return float64(usage.Used) / float64(usage.Total), true
}
