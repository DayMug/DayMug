package service

import (
	"context"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func newAttentionAdmission(t *testing.T) (*TurnAdmission, *storetest.Fake) {
	t.Helper()
	a := newAdmitTestAdmission(t, 1)
	fake := storetest.New()
	a.Store = fake
	return a, fake
}

func trackedSpec(convID string) AdmitSpec {
	spec := admitTestSpec(convID)
	spec.TrackAttention = true
	return spec
}

// A turn clears any stale flag when it starts and leaves "done" behind when
// it ends, so the conversation shows up until the user opens it.
func TestTrackedTurnLeavesDoneWhenItFinishes(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	_ = fake.SetConversationAttention(context.Background(), "c1", store.AttentionError)

	lease, err := a.Admit(trackedSpec("c1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if got := fake.Attention["c1"]; got != "" {
		t.Fatalf("attention while running = %q, want cleared", got)
	}
	lease.Release()
	if got := fake.Attention["c1"]; got != store.AttentionDone {
		t.Fatalf("attention after finish = %q, want done", got)
	}
}

// A prompt the user sent while the turn ran is still waiting to run, so the
// task is not over: the finishing turn must not claim "done".
func TestTrackedTurnWithQueuedFollowUpLeavesNoFlag(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	lease, err := a.Admit(trackedSpec("c1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if _, err := fake.EnqueuePrompt(context.Background(), "c1", "and also this"); err != nil {
		t.Fatalf("EnqueuePrompt: %v", err)
	}
	lease.Release()
	if got := fake.Attention["c1"]; got != "" {
		t.Fatalf("attention after finish = %q, want none while a follow-up is queued", got)
	}
}

func TestTrackedTurnThatFailedLeavesError(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	lease, err := a.Admit(trackedSpec("c1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	a.Drainer.MarkConversationFailed("c1")
	lease.Release()
	if got := fake.Attention["c1"]; got != store.AttentionError {
		t.Fatalf("attention = %q, want error", got)
	}
}

// Parking on a question flags the conversation; answering it clears the flag
// again because the user is plainly engaged with it.
func TestTrackedTurnFlagsAQuestionUntilItResumes(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	lease, err := a.Admit(trackedSpec("c1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	defer lease.Release()

	lease.Gate().Park()
	if got := fake.Attention["c1"]; got != store.AttentionWaiting {
		t.Fatalf("attention while parked = %q, want waiting", got)
	}
	if err := lease.Gate().Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := fake.Attention["c1"]; got != "" {
		t.Fatalf("attention after resume = %q, want cleared", got)
	}
}

// A prompt refused before it got a slot (recalled, drained) produced nothing
// to come back for.
func TestTrackedTurnRefusedWithoutErrorLeavesNoFlag(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	holdAdmitSlot(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spec := trackedSpec("c1")
	spec.QueueCtx = ctx
	if _, err := a.Admit(spec); err == nil {
		t.Fatal("Admit succeeded with a cancelled queue context")
	}
	if got := fake.Attention["c1"]; got != "" {
		t.Fatalf("attention = %q, want none", got)
	}
}

func TestUntrackedTurnNeverTouchesAttention(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	lease, err := a.Admit(admitTestSpec("c1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	lease.Release()
	if len(fake.Attention) != 0 {
		t.Fatalf("attention = %v, want untouched", fake.Attention)
	}
}

// A scheduled run that finished cleanly is read on arrival: nobody was
// waiting for it, so it must not badge the conversation.
func TestSkipDoneTurnLeavesNoFlagWhenItFinishes(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	spec := trackedSpec("c1")
	spec.SkipDone = true
	lease, err := a.Admit(spec)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	lease.Release()
	if got := fake.Attention["c1"]; got != "" {
		t.Fatalf("attention after finish = %q, want none", got)
	}
}

// Skipping "done" must not hide a failure: that still needs the user.
func TestSkipDoneTurnThatFailedStillLeavesError(t *testing.T) {
	a, fake := newAttentionAdmission(t)
	spec := trackedSpec("c1")
	spec.SkipDone = true
	lease, err := a.Admit(spec)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	a.Drainer.MarkConversationFailed("c1")
	lease.Release()
	if got := fake.Attention["c1"]; got != store.AttentionError {
		t.Fatalf("attention = %q, want error", got)
	}
}
