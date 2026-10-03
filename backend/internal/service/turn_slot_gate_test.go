package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// statusLog records the drainer status moves a gate reports, in order.
type statusLog struct {
	mu   sync.Mutex
	seen []string
}

func (l *statusLog) record(status string) {
	l.mu.Lock()
	l.seen = append(l.seen, status)
	l.mu.Unlock()
}

func (l *statusLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

// poolAcquire mirrors TurnAdmission's slot acquisition: enter the queue, then
// block until the slot is granted or ctx ends.
func poolAcquire(p *Pool, account string) func(context.Context) (*Ticket, error) {
	return func(ctx context.Context) (*Ticket, error) {
		ticket, err := p.EnterForUser("", account, "")
		if err != nil {
			return nil, err
		}
		if err := ticket.Wait(ctx); err != nil {
			return nil, err
		}
		return ticket, nil
	}
}

func newHeldGate(t *testing.T, p *Pool, log *statusLog) *TurnSlotGate {
	t.Helper()
	ticket, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	return NewTurnSlotGate(ticket, poolAcquire(p, "default"), log.record, nil)
}

func inUse(t *testing.T, p *Pool) int {
	t.Helper()
	used, _, ok := p.Stats("default")
	if !ok {
		t.Fatalf("no stats for account")
	}
	return used
}

func TestTurnSlotGate_ParkFreesTheSlotAndResumeTakesItBack(t *testing.T) {
	p := newTestPool(1, 0)
	log := &statusLog{}
	gate := newHeldGate(t, p, log)
	defer gate.Close()

	if got := inUse(t, p); got != 1 {
		t.Fatalf("inUse before park = %d, want 1", got)
	}

	gate.Park()
	if got := inUse(t, p); got != 0 {
		t.Fatalf("inUse after park = %d, want 0", got)
	}

	if err := gate.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := inUse(t, p); got != 1 {
		t.Fatalf("inUse after resume = %d, want 1", got)
	}
	want := []string{JobStatusWaiting, JobStatusQueued, JobStatusRunning}
	if got := log.snapshot(); len(got) != len(want) {
		t.Fatalf("status moves = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("status moves = %v, want %v", got, want)
			}
		}
	}
}

func TestTurnSlotGate_ParkIsIdempotentAndResumeIsANoOpWhenNotParked(t *testing.T) {
	p := newTestPool(1, 0)
	log := &statusLog{}
	gate := newHeldGate(t, p, log)
	defer gate.Close()

	gate.Park()
	gate.Park()
	if got := inUse(t, p); got != 0 {
		t.Fatalf("inUse after double park = %d, want 0", got)
	}

	if err := gate.Resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// A second resume (the provider echoing its own question resolution after
	// we already delivered the answer) must not take a second slot.
	if err := gate.Resume(context.Background()); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if got := inUse(t, p); got != 1 {
		t.Fatalf("inUse after double resume = %d, want 1", got)
	}
}

func TestTurnSlotGate_ResumeQueuesBehindWorkStartedWhileParked(t *testing.T) {
	p := newTestPool(1, 0)
	gate := newHeldGate(t, p, &statusLog{})
	defer gate.Close()

	gate.Park()

	// Someone else grabs the freed slot while the question is on screen.
	other, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter other: %v", err)
	}
	if err := other.Wait(context.Background()); err != nil {
		t.Fatalf("wait other: %v", err)
	}

	resumed := make(chan error, 1)
	go func() { resumed <- gate.Resume(context.Background()) }()

	select {
	case err := <-resumed:
		t.Fatalf("resume returned %v while the account was full", err)
	case <-time.After(50 * time.Millisecond):
	}

	other.Release()
	select {
	case err := <-resumed:
		if err != nil {
			t.Fatalf("resume after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resume never completed after the slot freed")
	}
	if got := inUse(t, p); got != 1 {
		t.Fatalf("inUse after resume = %d, want 1", got)
	}
}

func TestTurnSlotGate_CloseUnblocksAPendingResume(t *testing.T) {
	p := newTestPool(1, 0)
	gate := newHeldGate(t, p, &statusLog{})

	gate.Park()
	other, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter other: %v", err)
	}
	if err := other.Wait(context.Background()); err != nil {
		t.Fatalf("wait other: %v", err)
	}
	defer other.Release()

	resumed := make(chan error, 1)
	go func() { resumed <- gate.Resume(context.Background()) }()
	time.Sleep(20 * time.Millisecond)

	gate.Close()
	select {
	case err := <-resumed:
		if !errors.Is(err, errTurnGone) {
			t.Fatalf("resume after close = %v, want errTurnGone", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not unblock the pending resume")
	}
	if got := inUse(t, p); got != 1 {
		t.Fatalf("inUse after close = %d, want 1 (the other holder only)", got)
	}
}

func TestTurnSlotGate_ResumeAfterCloseReportsTheTurnIsGone(t *testing.T) {
	p := newTestPool(1, 0)
	gate := newHeldGate(t, p, &statusLog{})

	gate.Close()
	gate.Close()
	if got := inUse(t, p); got != 0 {
		t.Fatalf("inUse after close = %d, want 0", got)
	}
	if err := gate.Resume(context.Background()); !errors.Is(err, errTurnGone) {
		t.Fatalf("resume after close = %v, want errTurnGone", err)
	}
}

func TestTurnSlotGate_NilGateIsInert(t *testing.T) {
	var gate *TurnSlotGate
	gate.Park()
	gate.Close()
	if err := gate.Resume(context.Background()); err != nil {
		t.Fatalf("nil gate resume = %v, want nil", err)
	}
}

func TestSlotGateReportsTheQuestionItIsParkedOn(t *testing.T) {
	var seen []string
	gate := NewTurnSlotGate(nil, nil, nil, nil)
	gate.onQuestion = func(payload string) { seen = append(seen, payload) }

	gate.ParkForQuestion(`{"request_id":"r1"}`)
	if err := gate.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != `{"request_id":"r1"}` || seen[1] != "" {
		t.Fatalf("onQuestion calls = %q, want payload then clear", seen)
	}
}
