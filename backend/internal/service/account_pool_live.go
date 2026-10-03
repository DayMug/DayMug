package service

import (
	"errors"
	"fmt"
	"sync"
)

// LiveMultiplier sets how many interactive tty-mode processes may be alive
// per account relative to MaxConcurrent. Two means: up to 2×MaxConcurrent
// parked sessions, while at most MaxConcurrent run a turn at once (the latter
// gated by the existing Enter/Release execution semaphore).
const LiveMultiplier = 2

// ErrTooManyLiveSessions is returned by EnterLive when the account already
// has LiveMultiplier×MaxConcurrent interactive processes alive. The caller
// (the PTY WebSocket handler) translates it into a user-visible "connect
// refused" so the browser doesn't spawn beyond the cap.
var ErrTooManyLiveSessions = errors.New("too many live terminal sessions for this account")

// EnterLive reserves a "live process" slot for an interactive tty-mode
// session on the named account, enforcing the LiveMultiplier×max cap. Unlike
// EnterForUser (the per-turn execution semaphore that queues), EnterLive never
// queues: a session either gets a slot now or is refused, because a parked
// terminal that can't even start has nothing to wait for.
//
// On success it returns a release closure that frees the slot exactly once.
// Wiring: handler.AdminTerminalHandler.spawn takes the slot just before it
// spawns the PTY child, and the per-process pump goroutine calls release after
// proc.Wait() returns — the process's own lifetime, not the WebSocket's, since
// the child outlives the socket by however long its SIGTERM takes to land.
//
// A max of zero is treated as "no live limit" so a deliberately-unbounded
// account isn't accidentally locked out of tty mode.
func (p *Pool) EnterLive(accountName string) (release func(), err error) {
	if accountName == "" {
		provider, ok := p.cfg.DefaultProvider()
		if !ok {
			return nil, fmt.Errorf("%w: no provider configured", ErrAccountNotFound)
		}
		accountName = provider.Name
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, ok := p.accounts[accountName]
	if !ok || !acc.enabled {
		return nil, fmt.Errorf("%w: %q", ErrAccountNotFound, accountName)
	}
	if acc.max > 0 {
		cap := acc.max * LiveMultiplier
		if acc.liveInUse >= cap {
			return nil, fmt.Errorf("%w: %q (%d/%d)", ErrTooManyLiveSessions, accountName, acc.liveInUse, cap)
		}
	}
	acc.liveInUse++

	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			if acc.liveInUse > 0 {
				acc.liveInUse--
			}
			p.mu.Unlock()
		})
	}, nil
}

// LiveCount reports how many live tty sessions are currently parked on the
// account. Exposed for tests and for the handler's pty_status reporting.
func (p *Pool) LiveCount(accountName string) int {
	if accountName == "" {
		provider, ok := p.cfg.DefaultProvider()
		if !ok {
			return 0
		}
		accountName = provider.Name
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if acc, ok := p.accounts[accountName]; ok {
		return acc.liveInUse
	}
	return 0
}
