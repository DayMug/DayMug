package service_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func TestPauseGate_NilGateIsNeverPaused(t *testing.T) {
	var g *service.PauseGate
	if g.Paused() {
		t.Error("nil gate reported paused")
	}
	if g.SetPaused(true) {
		t.Error("nil gate reported a state change")
	}
}

func TestPauseGate_SetPausedReportsOnlyRealChanges(t *testing.T) {
	g := service.NewPauseGate()
	if g.Paused() {
		t.Fatal("a fresh gate must start running, not paused")
	}
	tests := []struct {
		name        string
		set         bool
		wantChanged bool
	}{
		{"first pause changes", true, true},
		{"repeat pause is a no-op", true, false},
		{"resume changes", false, true},
		{"repeat resume is a no-op", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.SetPaused(tc.set); got != tc.wantChanged {
				t.Errorf("SetPaused(%v) changed = %v, want %v", tc.set, got, tc.wantChanged)
			}
			if got := g.Paused(); got != tc.set {
				t.Errorf("Paused() = %v, want %v", got, tc.set)
			}
		})
	}
}

// TestDispatcher_PausedHoldsPromptsPending is the core promise of the switch:
// while paused nothing reaches the processor, and — critically — the row is
// still sitting at 'pending' so a restart can find it.
func TestDispatcher_PausedHoldsPromptsPending(t *testing.T) {
	q := newQueueStore()
	var processed atomic.Int32
	d := service.NewDispatcher(q, func(context.Context, store.Message) {
		processed.Add(1)
	})
	defer d.Stop()

	gate := service.NewPauseGate()
	gate.SetPaused(true)
	d.SetPauseGate(gate)

	if _, err := d.Enqueue(context.Background(), "conv-1", "held"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// No completion signal to wait on — the assertion is that nothing happens,
	// so the only option is to give the worker room to misbehave.
	time.Sleep(200 * time.Millisecond)

	if n := processed.Load(); n != 0 {
		t.Errorf("processor ran %d times while paused, want 0", n)
	}
	for _, m := range q.snapshot() {
		if m.QueueStatus != "pending" {
			t.Errorf("prompt %q left at queue_status %q, want pending so a restart recovers it",
				m.Content, m.QueueStatus)
		}
	}
}

// TestDispatcher_ResumeAllReleasesHeldPrompts covers the other half: prompts
// parked during a pause must run once it lifts, including for a conversation
// whose worker already retired on its idle timeout — which is why the resume
// path re-derives the work list from the store instead of memory.
func TestDispatcher_ResumeAllReleasesHeldPrompts(t *testing.T) {
	q := newQueueStore()
	done := make(chan struct{})
	var processed atomic.Int32
	d := service.NewDispatcher(q, func(_ context.Context, msg store.Message) {
		if processed.Add(1) == 2 {
			close(done)
		}
	})
	defer d.Stop()

	gate := service.NewPauseGate()
	gate.SetPaused(true)
	d.SetPauseGate(gate)

	for _, conv := range []string{"conv-1", "conv-2"} {
		if _, err := d.Enqueue(context.Background(), conv, "held"); err != nil {
			t.Fatalf("enqueue %s: %v", conv, err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n := processed.Load(); n != 0 {
		t.Fatalf("processor ran %d times while paused, want 0", n)
	}

	gate.SetPaused(false)
	resumed, err := d.ResumeAll(context.Background())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed != 2 {
		t.Errorf("resumed %d conversations, want 2", resumed)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("held prompts never ran after resume (processed %d)", processed.Load())
	}
}

// TestDispatcher_UnpausedIsUnaffected guards the default path: with a gate
// wired but never flipped, prompts flow exactly as they did before the switch
// existed.
func TestDispatcher_UnpausedIsUnaffected(t *testing.T) {
	q := newQueueStore()
	done := make(chan struct{})
	d := service.NewDispatcher(q, func(context.Context, store.Message) { close(done) })
	defer d.Stop()
	d.SetPauseGate(service.NewPauseGate())

	if _, err := d.Enqueue(context.Background(), "conv-1", "go"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never processed with the gate open")
	}
}
