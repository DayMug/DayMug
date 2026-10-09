package service

import (
	"context"
	"errors"
	"testing"
)

// TestPool_EnterLiveCapsAtMultiplier verifies the live (tty) semaphore allows
// LiveMultiplier×max parked sessions and refuses the next one, independent of
// the execution semaphore.
func TestPool_EnterLiveCapsAtMultiplier(t *testing.T) {
	p := newTestPool(2, 0) // max=2 → live cap = 4
	var releases []func()
	for i := 0; i < 4; i++ {
		rel, err := p.EnterLive("default")
		if err != nil {
			t.Fatalf("EnterLive #%d: unexpected error %v", i+1, err)
		}
		releases = append(releases, rel)
	}
	if got := p.LiveCount("default"); got != 4 {
		t.Fatalf("LiveCount = %d, want 4", got)
	}
	// The 5th must be refused — beyond 2×max.
	if _, err := p.EnterLive("default"); !errors.Is(err, ErrTooManyLiveSessions) {
		t.Fatalf("5th EnterLive err = %v, want ErrTooManyLiveSessions", err)
	}

	// Releasing one frees a slot for a new live session; release is idempotent.
	releases[0]()
	releases[0]()
	if got := p.LiveCount("default"); got != 3 {
		t.Fatalf("after release LiveCount = %d, want 3", got)
	}
	if _, err := p.EnterLive("default"); err != nil {
		t.Fatalf("EnterLive after release: %v", err)
	}
}

// TestPool_EnterLiveIndependentOfExecution shows a parked live session does
// not consume an execution slot: a full live pool still grants exec tickets
// up to max.
func TestPool_EnterLiveIndependentOfExecution(t *testing.T) {
	p := newTestPool(1, 0) // max=1 → live cap = 2, exec cap = 1
	r1, err := p.EnterLive("default")
	if err != nil {
		t.Fatalf("EnterLive 1: %v", err)
	}
	r2, err := p.EnterLive("default")
	if err != nil {
		t.Fatalf("EnterLive 2: %v", err)
	}
	defer r1()
	defer r2()
	// Two live sessions parked, but the single execution slot is still free.
	tk, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if err := tk.Wait(context.Background()); err != nil {
		t.Fatalf("exec ticket should grant immediately, got %v", err)
	}
	tk.Release()
}
