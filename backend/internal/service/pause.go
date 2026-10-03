package service

import "sync/atomic"

// PauseGate is the operator's manual "stop starting new work" switch, flipped
// from the global admin page so a release can be staged without racing live
// prompts.
//
// It is deliberately NOT persisted. The switch exists to hold traffic still
// across an upgrade or restart, and the restart itself is the event that ends
// the pause — a value written to app_settings would survive the restart and
// leave the server wedged with nobody obviously to blame. Losing the flag on
// boot is the safe direction to fail: worst case an operator flips it again.
//
// Distinct from Drainer despite the family resemblance. A drain is a one-way
// transition into shutdown and refuses work outright; a pause is reversible
// and, on the Dispatcher path, holds prompts at queue_status='pending' so they
// run later instead of erroring now.
//
// A nil *PauseGate reads as "never paused", matching Drainer's nil-safety so
// tests can leave it unwired.
type PauseGate struct {
	paused atomic.Bool
}

// NewPauseGate returns a gate in the running (not paused) state.
func NewPauseGate() *PauseGate { return &PauseGate{} }

// Paused reports whether new work should be held.
func (g *PauseGate) Paused() bool { return g != nil && g.paused.Load() }

// SetPaused writes the new state and reports whether it actually changed, so
// the caller can skip the resume sweep on a no-op toggle.
func (g *PauseGate) SetPaused(paused bool) (changed bool) {
	if g == nil {
		return false
	}
	return g.paused.Swap(paused) != paused
}
