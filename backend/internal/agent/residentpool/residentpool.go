// Package residentpool holds the eviction rule shared by the adapters that
// keep agent processes alive between turns: codexapp's per-account
// app-servers and claudeagentsdk's parked bridges.
//
// It is deliberately only the rule, not a pool type. The two fleets look alike
// but differ in every other mechanism: app-servers are shared by concurrent
// turns and spawned by the pool itself (single-flight per key), bridges are
// checked out exclusively and spawned by the caller; app-servers expire on a
// flat idle TTL checked at lookup, bridges on a task-aware policy with a
// background reaper and user-visible notices; app-server teardown is
// non-blocking and runs under the pool lock, bridge teardown waits for the
// process to exit and therefore must not. A generic pool covering both would be
// the union of those behaviours behind a row of callbacks, harder to read than
// either pool is today.
package residentpool

import "time"

// Member is a pooled process as the eviction rule sees it.
type Member interface {
	// Busy reports that a turn is using the process right now.
	Busy() bool
	// IdleSince is when the process last stopped being busy.
	IdleSince() time.Time
}

// EvictOverCap removes members from entries, least recently idle first, until
// at most limit remain, and returns them in eviction order for the caller to
// tear down. The member under keep — the one the caller is about to use — and
// busy members are never chosen, so entries may stay over limit: breaking a
// running conversation to honour a number is worse than one extra process.
//
// The caller must hold whatever lock guards entries.
func EvictOverCap[M Member](entries map[string]M, limit int, keep string) []M {
	var evicted []M
	for len(entries) > limit {
		var lruKey string
		var lru M
		found := false
		for key, m := range entries {
			if key == keep || m.Busy() {
				continue
			}
			if !found || m.IdleSince().Before(lru.IdleSince()) {
				lruKey, lru, found = key, m, true
			}
		}
		if !found {
			break
		}
		delete(entries, lruKey)
		evicted = append(evicted, lru)
	}
	return evicted
}
