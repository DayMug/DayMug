package agent

import "sync"

// Long-lived agent resources — today the Codex app-server pool — belong to no
// single turn, which is exactly why the server's shutdown path used to miss
// them: the job drain waits for in-flight prompts, and TerminateAll (the thing
// that would have killed these process groups) only runs when that drain times
// out. A clean restart or a self-upgrade therefore left one orphaned child per
// pool entry, still holding the account's session state and still writing the
// rollout JSONL the next boot is about to resume from.
var registeredClosers = struct {
	mu      sync.Mutex
	closers []func()
}{}

// RegisterCloser records a teardown function to run at server shutdown.
// Adapters call this when they own a process that outlives one turn.
func RegisterCloser(close func()) {
	if close == nil {
		return
	}
	registeredClosers.mu.Lock()
	defer registeredClosers.mu.Unlock()
	registeredClosers.closers = append(registeredClosers.closers, close)
}

// Resident-process reporting, registered the same way and for the same
// reason: a process that belongs to no turn is invisible to every counter the
// server already has. Closers make shutdown see them; this makes the
// *opportunistic* drains — graceful upgrade, scheduled restart — see them too,
// so a restart chosen because "the server looks idle" doesn't quietly kill
// background work that is still running.
var residentProbes = struct {
	mu     sync.Mutex
	probes []func() int
}{}

// RegisterResidentProbe records a source of long-lived agent processes that
// are doing real work right now. Adapters call this when they park a process
// between turns.
func RegisterResidentProbe(probe func() int) {
	if probe == nil {
		return
	}
	residentProbes.mu.Lock()
	defer residentProbes.mu.Unlock()
	residentProbes.probes = append(residentProbes.probes, probe)
}

// ResidentCount sums every registered probe. Zero means no adapter is holding
// a process open, so an idle-looking server really is idle.
func ResidentCount() int {
	residentProbes.mu.Lock()
	probes := append([]func() int(nil), residentProbes.probes...)
	residentProbes.mu.Unlock()
	total := 0
	for _, probe := range probes {
		total += probe()
	}
	return total
}

// Resident-process stopping, registered the same way: a user's Stop has to
// reach a process that belongs to no turn, which the Broadcaster's per-turn
// cancel never sees.
var residentStoppers = struct {
	mu       sync.Mutex
	stoppers []func(controlID string) bool
}{}

// RegisterResidentStopper records a way to end the parked process (and the
// background work it holds) for one conversation. Adapters call this when
// they park a process between turns.
func RegisterResidentStopper(stop func(controlID string) bool) {
	if stop == nil {
		return
	}
	residentStoppers.mu.Lock()
	defer residentStoppers.mu.Unlock()
	residentStoppers.stoppers = append(residentStoppers.stoppers, stop)
}

// StopResident ends whatever parked process any adapter holds for controlID.
// It reports whether one was found.
func StopResident(controlID string) bool {
	if controlID == "" {
		return false
	}
	residentStoppers.mu.Lock()
	stoppers := append([]func(string) bool(nil), residentStoppers.stoppers...)
	residentStoppers.mu.Unlock()
	stopped := false
	for _, stop := range stoppers {
		if stop(controlID) {
			stopped = true
		}
	}
	return stopped
}

// CloseRegistered runs every registered teardown function in registration
// order and clears the list, so a second call — a shutdown path that both
// drains cleanly and then force-stops, say — is a no-op rather than a
// double-free.
func CloseRegistered() int {
	registeredClosers.mu.Lock()
	closers := registeredClosers.closers
	registeredClosers.closers = nil
	registeredClosers.mu.Unlock()
	for _, close := range closers {
		close()
	}
	return len(closers)
}
