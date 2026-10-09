package service

import (
	"testing"
	"time"
)

func TestParkedJobDoesNotBlockIdleDrainAndIsSuspended(t *testing.T) {
	d := NewDrainer()
	done, _, setSuspend, ok := d.TryJobStartSuspendable(DrainerJob{ConversationID: "c1"})
	if !ok {
		t.Fatal("job refused")
	}
	defer done()
	if d.TryStartDrainWhenIdle(DrainReasonRestart) {
		t.Fatal("drain started while a job was working")
	}

	suspended := make(chan struct{})
	setSuspend(func() { close(suspended) })
	if got := d.Busy(); got != 0 {
		t.Fatalf("Busy = %d with only a parked job, want 0", got)
	}
	if !d.TryStartDrainWhenIdle(DrainReasonRestart) {
		t.Fatal("parked job held up the restart drain")
	}
	select {
	case <-suspended:
	case <-time.After(time.Second):
		t.Fatal("drain did not suspend the parked job")
	}
}

func TestResumedJobBlocksIdleDrainAgain(t *testing.T) {
	d := NewDrainer()
	done, _, setSuspend, _ := d.TryJobStartSuspendable(DrainerJob{ConversationID: "c1"})
	defer done()
	setSuspend(func() { t.Error("resumed job must not be suspended") })
	setSuspend(nil)
	if d.TryStartDrainWhenIdle(DrainReasonUpgrade) {
		t.Fatal("drain started while the answered job was working again")
	}
}

func TestJobParkingDuringDrainIsSuspendedImmediately(t *testing.T) {
	d := NewDrainer()
	done, _, setSuspend, _ := d.TryJobStartSuspendable(DrainerJob{ConversationID: "c1"})
	defer done()
	d.StartDrain()
	suspended := make(chan struct{})
	setSuspend(func() { close(suspended) })
	select {
	case <-suspended:
	case <-time.After(time.Second):
		t.Fatal("a job parking after the drain began was not suspended")
	}
}
