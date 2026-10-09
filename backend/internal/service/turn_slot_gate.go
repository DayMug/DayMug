package service

import (
	"context"
	"errors"
	"sync"
)

// errTurnGone reports that the turn owning a slot gate has already finished,
// so there is nothing left to resume.
var errTurnGone = errors.New("turn is no longer running")

// turnSlotGate owns the account-pool slot of one running turn and lets that
// turn give the slot back while it is blocked on an AskUserQuestion prompt.
//
// Without it a turn that asks the user something keeps its slot for as long as
// the human takes to answer — minutes on a phone, hours if they walk away —
// while burning nothing but wall-clock. On an account with MaxConcurrent 2
// that is half the capacity held by a process doing no work, and everyone
// else's prompts queue behind it.
//
// Park() hands the slot back; Resume() re-enters the queue exactly like a
// fresh prompt, so an answer does not jump ahead of the turns that started
// while the question was on screen. Both are idempotent and safe from any
// goroutine: an answer and a provider-side auto-resolution race routinely.
type TurnSlotGate struct {
	// acquire re-enters the account queue and blocks until a slot is granted
	// or ctx ends. A nil ticket with a nil error means no pool is configured.
	acquire func(ctx context.Context) (*Ticket, error)
	// setStatus moves the drainer job between queued / running / waiting so
	// operator views stop counting a parked turn as running work.
	setStatus func(status string)
	// onChange re-broadcasts conversation activity after a status move.
	onChange func()
	// onQuestion, when set, learns which question the turn is parked on
	// (payload) and when it stops being parked on one (""). It is set once
	// before the gate is shared, so reading it needs no lock.
	onQuestion func(payload string)

	mu sync.Mutex
	// resumeMu serializes acquisitions so two callers racing to resume the
	// same turn cannot each take a slot. It is deliberately separate from mu:
	// acquire blocks for as long as the queue is deep, and mu has to stay
	// available for Close() to cancel that wait.
	resumeMu sync.Mutex

	ticket        *Ticket
	parked        bool
	closed        bool
	cancelAcquire context.CancelFunc
}

func NewTurnSlotGate(
	ticket *Ticket,
	acquire func(ctx context.Context) (*Ticket, error),
	setStatus func(string),
	onChange func(),
) *TurnSlotGate {
	if setStatus == nil {
		setStatus = func(string) {}
	}
	if onChange == nil {
		onChange = func() {}
	}
	return &TurnSlotGate{ticket: ticket, acquire: acquire, setStatus: setStatus, onChange: onChange}
}

// ParkForQuestion is Park for a turn blocked on a question, whose payload is
// what would have to be shown again if the turn were ended before an answer.
func (g *TurnSlotGate) ParkForQuestion(payload string) {
	if g == nil {
		return
	}
	g.Park()
	g.mu.Lock()
	parked := g.parked && !g.closed
	g.mu.Unlock()
	if parked && g.onQuestion != nil {
		g.onQuestion(payload)
	}
}

// Park gives the slot back because the turn is now waiting on the human.
func (g *TurnSlotGate) Park() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed || g.parked {
		g.mu.Unlock()
		return
	}
	g.parked = true
	ticket := g.ticket
	g.ticket = nil
	g.mu.Unlock()

	if ticket != nil {
		ticket.Release()
	}
	g.setStatus(JobStatusWaiting)
	g.onChange()
}

// Resume re-enters the account queue and returns once a slot is held again.
// It is a no-op (nil error) for a turn that never parked, and returns
// errTurnGone once the turn has finished.
func (g *TurnSlotGate) Resume(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.resumeMu.Lock()
	defer g.resumeMu.Unlock()

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errTurnGone
	}
	if !g.parked {
		g.mu.Unlock()
		return nil
	}
	acquireCtx, cancel := context.WithCancel(ctx)
	g.cancelAcquire = cancel
	acquire := g.acquire
	g.mu.Unlock()

	defer func() {
		cancel()
		g.mu.Lock()
		g.cancelAcquire = nil
		g.mu.Unlock()
	}()

	if g.onQuestion != nil {
		g.onQuestion("")
	}
	g.setStatus(JobStatusQueued)
	g.onChange()

	var (
		ticket *Ticket
		err    error
	)
	if acquire != nil {
		ticket, err = acquire(acquireCtx)
	}
	if err != nil {
		g.mu.Lock()
		closed := g.closed
		g.mu.Unlock()
		if closed {
			// Close cancelled the acquisition; the turn is over and the
			// failure is bookkeeping, not something to report to the user.
			return errTurnGone
		}
		// Stay parked: the turn is still blocked on the question, and a caller
		// that retries (a second answer attempt) should queue again rather
		// than believe it holds a slot.
		return err
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		if ticket != nil {
			ticket.Release()
		}
		return errTurnGone
	}
	g.ticket = ticket
	g.parked = false
	g.mu.Unlock()

	g.setStatus(JobStatusRunning)
	g.onChange()
	return nil
}

// Close releases the slot for good and unblocks any acquisition still in
// flight. Safe to call more than once; the turn's defer is the only caller
// that has to.
func (g *TurnSlotGate) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	ticket := g.ticket
	g.ticket = nil
	cancel := g.cancelAcquire
	g.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if ticket != nil {
		ticket.Release()
	}
}
