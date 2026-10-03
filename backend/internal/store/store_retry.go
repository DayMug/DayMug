package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// busySnapshot* bound the retry loop below. Five attempts with a doubling
// 10ms backoff is at most ~150ms of extra latency — short enough to stay
// inside a click's perceived response time, long enough to outlive the write
// transaction we collided with (a prompt enqueue / claim is a handful of
// statements). Bounded on purpose: unlike the dispatcher's claim loop, a
// cancel that never lands must surface as an error the handler can report,
// not spin forever.
const (
	busySnapshotAttempts    = 5
	busySnapshotBaseBackoff = 10 * time.Millisecond
	busySnapshotMaxBackoff  = 80 * time.Millisecond
)

// isBusyErr reports whether err is SQLite's "someone else holds the write
// lock" family — SQLITE_BUSY (5), SQLITE_LOCKED (6) and every extended code
// built on them, most importantly SQLITE_BUSY_SNAPSHOT (517).
//
// The extended code matters: a deferred transaction that reads first and
// writes later can find the database advanced underneath its read snapshot,
// and SQLite deliberately skips the busy handler for that case because
// waiting cannot refresh a stale snapshot. So busy_timeout in the DSN — which
// does cover plain writer-vs-writer contention — expires instantly here and
// the caller sees a 0ms failure. Only re-running the whole transaction fixes
// it.
//
// The string fallback exists because not every failure path in the driver
// wraps the result code in *sqlite.Error.
func isBusyErr(err error) bool {
	if err == nil {
		return false
	}
	var serr *sqlite.Error
	if errors.As(err, &serr) {
		switch serr.Code() & 0xFF {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}

// retryOnBusySnapshot re-runs fn while it fails with a lock-contention error.
//
// fn must be a self-contained transaction: each attempt begins a fresh one so
// it observes a fresh snapshot, which is the only thing that clears
// SQLITE_BUSY_SNAPSHOT. Callers that need a value out of fn should assign it
// to a captured variable — every attempt overwrites it, and only the last
// attempt's value escapes.
//
// A canceled ctx aborts the wait and surfaces the last busy error rather than
// the context error, so the caller's log line names the actual cause.
func retryOnBusySnapshot(ctx context.Context, fn func() error) error {
	backoff := busySnapshotBaseBackoff
	var err error
	for attempt := 0; attempt < busySnapshotAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return err
			}
			if backoff < busySnapshotMaxBackoff {
				backoff *= 2
			}
		}
		err = fn()
		if err == nil || !isBusyErr(err) {
			return err
		}
	}
	return err
}
