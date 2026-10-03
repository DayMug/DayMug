package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainer_NilSafe(t *testing.T) {
	var d *Drainer
	d.StartDrain()
	if d.IsDraining() {
		t.Fatal("nil drainer should never report draining")
	}
	d.JobStart()
	d.JobDone()
	if err := d.WaitJobs(context.Background()); err != nil {
		t.Fatalf("nil WaitJobs returned %v", err)
	}
	select {
	case <-d.DrainCtx().Done():
		t.Fatal("nil DrainCtx should not be done")
	default:
	}
}

func TestDrainer_StartDrainFlipsState(t *testing.T) {
	d := NewDrainer()
	if d.IsDraining() {
		t.Fatal("fresh drainer should not be draining")
	}
	d.StartDrain()
	if !d.IsDraining() {
		t.Fatal("drainer should report draining after StartDrain")
	}
	// Idempotent.
	d.StartDrain()
	select {
	case <-d.DrainCtx().Done():
	default:
		t.Fatal("DrainCtx should be done after StartDrain")
	}
}

func TestDrainer_ResetUpgradeDrain(t *testing.T) {
	d := NewDrainer()
	d.StartDrain(DrainReasonUpgrade)
	if !d.IsDraining() || d.Reason() != DrainReasonUpgrade {
		t.Fatalf("drain state = draining %v reason %q, want upgrade", d.IsDraining(), d.Reason())
	}
	d.ResetDrain()
	if d.IsDraining() {
		t.Fatal("upgrade drain should reset")
	}
	if d.Reason() != DrainReasonNone {
		t.Fatalf("reason after reset = %q, want none", d.Reason())
	}
	done, _, ok := d.TryJobStartWithInfo(DrainerJob{Username: "alice"})
	if !ok {
		t.Fatal("job should start after reset")
	}
	done()
}

// A parked agent process holds no drainer job, so a graceful upgrade would read
// inFlight==0 as "idle" and restart straight through background work that is
// still running. The probe is what makes that work visible to exactly the
// paths that would otherwise destroy it.
func TestDrainer_ResidentWorkPostponesOpportunisticDrains(t *testing.T) {
	residents := 1
	d := NewDrainer()
	d.SetResidentProbe(func() int { return residents })

	for _, reason := range []DrainReason{DrainReasonUpgrade, DrainReasonRestart} {
		if d.TryStartDrainWhenIdle(reason) {
			t.Fatalf("%s drain started while resident work was live", reason)
		}
		if d.IsDraining() {
			t.Fatalf("%s drain flipped the state despite being refused", reason)
		}
	}

	residents = 0
	if !d.TryStartDrainWhenIdle(DrainReasonUpgrade) {
		t.Fatal("upgrade drain refused once the resident work finished")
	}
}

// An operator asking the server to stop has already made the call; background
// work must not be able to refuse it.
func TestDrainer_ShutdownDrainIgnoresResidentWork(t *testing.T) {
	d := NewDrainer()
	d.SetResidentProbe(func() int { return 3 })

	if !d.TryStartDrainWhenIdle(DrainReasonShutdown) {
		t.Fatal("shutdown drain was blocked by resident work")
	}
	if !d.IsDraining() || d.Reason() != DrainReasonShutdown {
		t.Fatalf("drain state = draining %v reason %q, want shutdown", d.IsDraining(), d.Reason())
	}
}

func TestDrainer_ResidentProbeIsOptional(t *testing.T) {
	d := NewDrainer()
	if !d.TryStartDrainWhenIdle(DrainReasonUpgrade) {
		t.Fatal("an unset probe blocked an upgrade drain")
	}
	d.ResetDrain()
	d.SetResidentProbe(func() int { return 2 })
	d.SetResidentProbe(nil)
	if !d.TryStartDrainWhenIdle(DrainReasonUpgrade) {
		t.Fatal("clearing the probe did not restore the unblocked path")
	}
}

func TestDrainer_ResetRestartDrain(t *testing.T) {
	d := NewDrainer()
	d.StartDrain(DrainReasonRestart)
	if !d.IsDraining() || d.Reason() != DrainReasonRestart {
		t.Fatalf("drain state = draining %v reason %q, want restart", d.IsDraining(), d.Reason())
	}
	d.ResetDrain()
	if d.IsDraining() {
		t.Fatal("restart drain should reset")
	}
	if d.Reason() != DrainReasonNone {
		t.Fatalf("reason after reset = %q, want none", d.Reason())
	}
}

func TestDrainer_TryJobStartWithInfoRefusesDuringDrain(t *testing.T) {
	d := NewDrainer()
	d.StartDrain(DrainReasonUpgrade)
	done, _, ok := d.TryJobStartWithInfo(DrainerJob{Username: "alice"})
	defer done()
	if ok {
		t.Fatal("TryJobStartWithInfo should refuse during drain")
	}
	if got := d.InFlight(); got != 0 {
		t.Fatalf("InFlight = %d, want 0", got)
	}
}

func TestDrainer_ShutdownDrainDoesNotReset(t *testing.T) {
	d := NewDrainer()
	d.StartDrain(DrainReasonShutdown)
	d.ResetDrain()
	if !d.IsDraining() {
		t.Fatal("shutdown drain should not reset")
	}
}

func TestDrainer_WaitJobsCompletesWhenJobsFinish(t *testing.T) {
	d := NewDrainer()
	d.JobStart()
	d.JobStart()

	go func() {
		time.Sleep(20 * time.Millisecond)
		d.JobDone()
		d.JobDone()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.WaitJobs(ctx); err != nil {
		t.Fatalf("WaitJobs returned %v, want nil", err)
	}
}

func TestDrainer_WaitJobsTimesOut(t *testing.T) {
	d := NewDrainer()
	d.JobStart()
	defer d.JobDone()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := d.WaitJobs(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestDrainer_InFlightTracksJobStartDone(t *testing.T) {
	d := NewDrainer()
	if d.InFlight() != 0 {
		t.Fatalf("fresh drainer InFlight = %d, want 0", d.InFlight())
	}
	d.JobStart()
	d.JobStart()
	if d.InFlight() != 2 {
		t.Fatalf("after two JobStart: InFlight = %d, want 2", d.InFlight())
	}
	d.JobDone()
	if d.InFlight() != 1 {
		t.Fatalf("after JobDone: InFlight = %d, want 1", d.InFlight())
	}
	d.JobDone()
	if d.InFlight() != 0 {
		t.Fatalf("after both done: InFlight = %d, want 0", d.InFlight())
	}
}

func TestDrainer_JobsTracksOwnerInfo(t *testing.T) {
	d := NewDrainer()
	done := d.JobStartWithInfo(DrainerJob{
		UserID:         "u1",
		Username:       "alice",
		ProviderType:   "codex",
		AccountName:    "codex-main",
		ConversationID: "c1",
	})
	if d.InFlight() != 1 {
		t.Fatalf("after JobStartWithInfo: InFlight = %d, want 1", d.InFlight())
	}
	jobs := d.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("Jobs len = %d, want 1", len(jobs))
	}
	if jobs[0].Username != "alice" || jobs[0].AccountName != "codex-main" || jobs[0].ProviderType != "codex" {
		t.Fatalf("job owner = %+v", jobs[0])
	}
	if jobs[0].StartedAt.IsZero() {
		t.Fatal("job StartedAt should be set")
	}
	done()
	if d.InFlight() != 0 {
		t.Fatalf("after done: InFlight = %d, want 0", d.InFlight())
	}
	if got := d.Jobs(); len(got) != 0 {
		t.Fatalf("after done: Jobs len = %d, want 0", len(got))
	}
}

func TestDrainer_ResidentActivityIsVisibleWithoutBecomingInFlight(t *testing.T) {
	d := NewDrainer()
	info := DrainerJob{
		UserID: "owner", AccountName: "claude-main", ConversationID: "conv-resident",
	}
	if !d.SetResidentActivity("bridge-1", info, true) {
		t.Fatal("first resident activation did not report a change")
	}
	if d.InFlight() != 0 || len(d.Jobs()) != 0 {
		t.Fatalf("resident activity leaked into jobs: inFlight=%d jobs=%+v", d.InFlight(), d.Jobs())
	}
	activities := d.ResidentActivities()
	if len(activities) != 1 || activities[0].ConversationID != "conv-resident" ||
		activities[0].Status != JobStatusRunning || activities[0].StartedAt.IsZero() {
		t.Fatalf("resident activities = %+v", activities)
	}
	startedAt := activities[0].StartedAt
	if d.SetResidentActivity("bridge-1", info, true) {
		t.Fatal("repeated resident level snapshot reported a visible change")
	}
	if got := d.ResidentActivities()[0]; !got.StartedAt.Equal(startedAt) || got.Status != JobStatusRunning {
		t.Fatalf("resident start moved from %v to %v", startedAt, got)
	}
	if !d.SetResidentActivity("bridge-1", info, false) {
		t.Fatal("resident deactivation did not report a change")
	}
	if got := d.ResidentActivities(); len(got) != 0 {
		t.Fatalf("resident activity survived deactivation: %+v", got)
	}
}

func TestDrainer_JobStatusDefaultsToRunning(t *testing.T) {
	d := NewDrainer()
	done := d.JobStartWithInfo(DrainerJob{Username: "alice"})
	defer done()
	jobs := d.Jobs()
	if len(jobs) != 1 || jobs[0].Status != JobStatusRunning {
		t.Fatalf("job status = %q, want %q", statusOf(jobs), JobStatusRunning)
	}
}

func TestDrainer_QueuedJobPromotesToRunning(t *testing.T) {
	d := NewDrainer()
	done, setStatus, ok := d.TryJobStartWithInfo(DrainerJob{
		Username: "alice",
		Status:   JobStatusQueued,
	})
	if !ok {
		t.Fatal("job should start")
	}
	defer done()

	if got := statusOf(d.Jobs()); got != JobStatusQueued {
		t.Fatalf("status before setStatus = %q, want %q", got, JobStatusQueued)
	}
	// Queued and running work both count toward the drain gate.
	if d.InFlight() != 1 {
		t.Fatalf("InFlight while queued = %d, want 1", d.InFlight())
	}

	setStatus(JobStatusRunning)
	if got := statusOf(d.Jobs()); got != JobStatusRunning {
		t.Fatalf("status after setStatus = %q, want %q", got, JobStatusRunning)
	}
}

func TestDrainer_WaitingJobIsNotCountedAsRunning(t *testing.T) {
	d := NewDrainer()
	done, setStatus, ok := d.TryJobStartWithInfo(DrainerJob{Username: "alice"})
	if !ok {
		t.Fatal("job should start")
	}
	defer done()

	setStatus(JobStatusWaiting)
	if got := statusOf(d.Jobs()); got != JobStatusWaiting {
		t.Fatalf("status after parking on a question = %q, want %q", got, JobStatusWaiting)
	}
	// A turn parked on a user question holds no account slot, but it still owns
	// a live child process — a drain has to wait for it.
	if d.InFlight() != 1 {
		t.Fatalf("InFlight while waiting = %d, want 1", d.InFlight())
	}

	setStatus(JobStatusRunning)
	if got := statusOf(d.Jobs()); got != JobStatusRunning {
		t.Fatalf("status after the answer landed = %q, want %q", got, JobStatusRunning)
	}
}

func TestDrainer_TryStartDrainWhenIdleDoesNotCancelActiveJob(t *testing.T) {
	d := NewDrainer()
	done, _, ok := d.TryJobStartWithInfo(DrainerJob{Username: "alice"})
	if !ok {
		t.Fatal("job should start")
	}
	jobCtx := d.DrainCtx()

	if d.TryStartDrainWhenIdle(DrainReasonUpgrade) {
		t.Fatal("drain should not start while a job is active")
	}
	select {
	case <-jobCtx.Done():
		t.Fatal("failed idle drain must not cancel the active job")
	default:
	}

	done()
	if !d.TryStartDrainWhenIdle(DrainReasonUpgrade) {
		t.Fatal("drain should start after the active job finishes")
	}
}

func TestDrainer_TryStartDrainWhenIdleSinceRejectsCompletedActivity(t *testing.T) {
	d := NewDrainer()
	activityID := d.ActivityID()

	done, _, ok := d.TryJobStartWithInfo(DrainerJob{Username: "alice"})
	if !ok {
		t.Fatal("job should start")
	}
	done()

	if d.TryStartDrainWhenIdleSince(DrainReasonRestart, activityID) {
		t.Fatal("drain should not start after activity inside the quiet window")
	}
	if d.IsDraining() {
		t.Fatal("rejected quiet-window drain must leave the service accepting work")
	}
	if !d.TryStartDrainWhenIdleSince(DrainReasonRestart, d.ActivityID()) {
		t.Fatal("drain should start when the activity snapshot is current")
	}
}

// statusOf returns the status of the single tracked job, or "" if the count
// isn't exactly one — keeps the assertions above terse.
func statusOf(jobs []DrainerJob) string {
	if len(jobs) != 1 {
		return ""
	}
	return jobs[0].Status
}

func TestDrainer_InFlightNilSafe(t *testing.T) {
	var d *Drainer
	if got := d.InFlight(); got != 0 {
		t.Fatalf("nil Drainer.InFlight() = %d, want 0", got)
	}
	if got := d.Jobs(); len(got) != 0 {
		t.Fatalf("nil Drainer.Jobs len = %d, want 0", len(got))
	}
}
