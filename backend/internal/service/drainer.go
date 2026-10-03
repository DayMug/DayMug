package service

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type DrainerJob struct {
	ID           int64  `json:"id"`
	UserID       string `json:"user_id,omitempty"`
	Username     string `json:"username,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	AccountName  string `json:"account_name,omitempty"`
	// Status distinguishes a job parked in the account pool's FIFO queue
	// waiting for a concurrency slot (JobStatusQueued), one that has been
	// granted a slot and is actively running a turn (JobStatusRunning), and
	// one that gave its slot back because the turn is blocked on a question
	// only the human can answer (JobStatusWaiting). All three count toward
	// InFlight (the drain gate must wait for queued work too), but operator
	// views render them separately so "N running" isn't inflated by prompts
	// that are burning no capacity at all.
	Status         string    `json:"status"`
	ConversationID string    `json:"conversation_id,omitempty"`
	StartedAt      time.Time `json:"started_at"`
}

// Job lifecycle states surfaced to operator views. A job registered without
// an explicit Status defaults to JobStatusRunning so callers that never queue
// (and existing tests) keep their prior semantics.
const (
	JobStatusQueued  = "queued"
	JobStatusRunning = "running"
	// JobStatusWaiting marks a turn that is alive but parked on an
	// AskUserQuestion prompt. It holds no account-pool slot: the human may
	// take minutes or hours to answer, and charging concurrency for that time
	// starves everyone else on the account. Answering re-queues the turn,
	// which flips it back through JobStatusQueued to JobStatusRunning.
	JobStatusWaiting = "waiting"
	// JobStatusBackground marks resident background work (a parked agent
	// process whose background tasks are still running) in operator views.
	// It is not an in-flight job, but a graceful restart still waits for it.
	JobStatusBackground = "background"
)

type DrainReason string

const (
	DrainReasonNone     DrainReason = ""
	DrainReasonShutdown DrainReason = "shutdown"
	DrainReasonUpgrade  DrainReason = "upgrade"
	DrainReasonRestart  DrainReason = "restart"
)

// Drainer coordinates graceful shutdown across the server: it tracks active
// long-running jobs (e.g. a Claude CLI invocation streaming to a WebSocket)
// and exposes a "draining" state so handlers can refuse new work while
// waiting for in-flight work to finish.
//
// A nil *Drainer is safe to use: every method becomes a no-op, IsDraining
// returns false, and DrainCtx returns context.Background(). Tests can
// construct handlers without a Drainer.
type Drainer struct {
	drainMu     sync.Mutex
	drainCtx    context.Context
	drainCancel context.CancelFunc
	drainReason DrainReason
	jobs        sync.WaitGroup
	// inFlight mirrors the WaitGroup counter so observers (e.g. the admin
	// upgrade gate) can read it without blocking. sync.WaitGroup does not
	// expose its counter, so we maintain a parallel atomic — incremented
	// in JobStart, decremented in JobDone.
	inFlight   atomic.Int64
	activityID atomic.Uint64
	nextJobID  atomic.Int64
	mu         sync.Mutex
	active     map[int64]DrainerJob
	// resident holds background activity that outlives its foreground job.
	// It is intentionally separate from active/inFlight: a parked resident
	// process must animate in the UI, but must not turn a graceful shutdown
	// wait into an unbounded wait for provider-managed background work.
	resident map[string]DrainerJob
	// suspenders holds the jobs parked on a question only a human can
	// answer, keyed by job id. Such a job is not work in progress: it neither
	// holds up an idle check nor is waited on by a drain, which ends it
	// through its suspend func instead. See TryJobStartSuspendable.
	suspenders map[int64]func()
	// failed remembers conversations whose latest turn ended in an error, so
	// the activity snapshot that drops the job can tell browsers it failed
	// rather than finished. See MarkConversationFailed.
	failed map[string]time.Time
	// residentProbe reports long-lived agent processes that belong to no
	// job. See SetResidentProbe.
	residentProbe atomic.Pointer[func() int]
}

// SetResidentProbe registers a "long-lived agent processes are still working"
// signal, consulted only by the opportunistic drains.
//
// The gap it closes: an agent process parked between turns — kept alive so a
// background task can wake the model when it finishes — holds no drainer job,
// because it is not serving a request. Graceful upgrade and scheduled restart both
// decide to act the moment inFlight hits zero, so they would see an idle
// server and restart straight through work that is very much still running.
//
// Deliberately not consulted by a shutdown drain: an operator asking the
// server to stop has already made that call, and a background task must not be
// able to refuse it.
func (d *Drainer) SetResidentProbe(probe func() int) {
	if d == nil {
		return
	}
	if probe == nil {
		d.residentProbe.Store(nil)
		return
	}
	d.residentProbe.Store(&probe)
}

// residentsBusy reports whether long-lived agent work should postpone an
// opportunistic drain.
func (d *Drainer) residentsBusy() bool {
	probe := d.residentProbe.Load()
	return probe != nil && (*probe)() > 0
}

func NewDrainer() *Drainer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Drainer{drainCtx: ctx, drainCancel: cancel}
}

// StartDrain transitions the drainer into the draining state. Idempotent.
// Existing callers may omit a reason; shutdown is the default.
func (d *Drainer) StartDrain(reasons ...DrainReason) {
	if d == nil {
		return
	}
	reason := DrainReasonShutdown
	if len(reasons) > 0 && reasons[0] != DrainReasonNone {
		reason = reasons[0]
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	if d.drainCtx.Err() != nil {
		if reason == DrainReasonShutdown {
			d.drainReason = reason
		}
		return
	}
	d.drainReason = reason
	d.drainCancel()
	d.suspendParked()
}

// TryStartDrainWhenIdle starts a drain only when no tracked job is active.
// The check is serialized with TryJobStartWithInfo so a new job cannot slip
// between the idle observation and cancellation of the shared drain context.
func (d *Drainer) TryStartDrainWhenIdle(reason DrainReason) bool {
	return d.tryStartDrainWhenIdle(reason, nil)
}

// TryStartDrainWhenIdleSince starts a drain only when the service is idle and
// no job has started or finished since activityID was observed. This lets
// graceful restart and upgrade enforce a real quiet period without a short
// request slipping entirely between polling ticks.
func (d *Drainer) TryStartDrainWhenIdleSince(reason DrainReason, activityID uint64) bool {
	return d.tryStartDrainWhenIdle(reason, &activityID)
}

func (d *Drainer) tryStartDrainWhenIdle(reason DrainReason, activityID *uint64) bool {
	if d == nil {
		return true
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	if d.drainCtx.Err() != nil || d.Busy() != 0 ||
		(activityID != nil && d.activityID.Load() != *activityID) {
		return false
	}
	// Only the opportunistic reasons defer to resident work; a shutdown drain
	// is the operator's decision and is never blocked.
	if (reason == DrainReasonUpgrade || reason == DrainReasonRestart) && d.residentsBusy() {
		return false
	}
	if reason == DrainReasonNone {
		reason = DrainReasonShutdown
	}
	d.drainReason = reason
	d.drainCancel()
	d.suspendParked()
	return true
}

// ResetDrain clears a scheduled upgrade/restart drain after it could not
// proceed. Shutdown drains are terminal and are intentionally not reset.
func (d *Drainer) ResetDrain() {
	if d == nil {
		return
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	if d.drainReason != DrainReasonUpgrade && d.drainReason != DrainReasonRestart {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.drainCtx = ctx
	d.drainCancel = cancel
	d.drainReason = DrainReasonNone
}

// IsDraining reports whether StartDrain has been called.
func (d *Drainer) IsDraining() bool {
	if d == nil {
		return false
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	return d.drainCtx.Err() != nil
}

// Reason returns why the drainer is currently draining.
func (d *Drainer) Reason() DrainReason {
	if d == nil {
		return DrainReasonNone
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	if d.drainCtx.Err() == nil {
		return DrainReasonNone
	}
	return d.drainReason
}

// DrainCtx returns a context that is cancelled when StartDrain is called.
// On a nil Drainer this returns context.Background() so callers can select
// on Done() unconditionally without ever firing.
func (d *Drainer) DrainCtx() context.Context {
	if d == nil {
		return context.Background()
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	return d.drainCtx
}

// JobStart and JobDone bracket a unit of in-flight work that the drainer
// should wait for during shutdown. Use defer JobDone() right after JobStart().
func (d *Drainer) JobStart() {
	if d == nil {
		return
	}
	d.activityID.Add(1)
	d.jobs.Add(1)
	d.inFlight.Add(1)
}

func (d *Drainer) JobDone() {
	if d == nil {
		return
	}
	d.activityID.Add(1)
	d.jobs.Done()
	d.inFlight.Add(-1)
}

// JobStartWithInfo brackets a unit of in-flight work and records who owns it
// for read-only operator views such as the admin upgrade gate.
func (d *Drainer) JobStartWithInfo(info DrainerJob) func() {
	if d == nil {
		return func() {}
	}
	id := d.startJob(info)
	var once sync.Once
	return func() {
		once.Do(func() {
			d.finishJob(id)
		})
	}
}

// TryJobStartWithInfo records a job only if the drainer is not already
// draining. The drain-state check and WaitGroup increment are serialized
// with StartDrain so no new tracked job can slip in after a drain begins.
//
// It returns a done closure to bracket the work, a setStatus closure that moves
// the job between JobStatusQueued / JobStatusRunning / JobStatusWaiting as it
// acquires, holds and gives back its account-pool slot, and ok=false if the
// drainer is already draining.
func (d *Drainer) TryJobStartWithInfo(info DrainerJob) (done func(), setStatus func(string), ok bool) {
	done, setStatus, _, ok = d.TryJobStartSuspendable(info)
	return done, setStatus, ok
}

// TryJobStartSuspendable is TryJobStartWithInfo plus setSuspend, which marks
// the job as parked on a human (fn != nil) or back at work (nil). While
// parked the job does not count toward Busy, so a graceful restart or upgrade
// no longer waits for someone to answer; when a drain starts, fn is called
// once — immediately, if the drain is already under way — and is expected to
// end the turn promptly.
func (d *Drainer) TryJobStartSuspendable(info DrainerJob) (done func(), setStatus func(string), setSuspend func(func()), ok bool) {
	if d == nil {
		return func() {}, func(string) {}, func(func()) {}, true
	}
	d.drainMu.Lock()
	defer d.drainMu.Unlock()
	if d.drainCtx.Err() != nil {
		return func() {}, func(string) {}, func(func()) {}, false
	}
	id := d.startJob(info)
	var once sync.Once
	return func() {
			once.Do(func() {
				d.finishJob(id)
			})
		}, func(status string) {
			d.setJobStatus(id, status)
		}, func(fn func()) {
			d.setJobSuspend(id, fn)
		}, true
}

// setJobSuspend registers or clears a parked job's suspend func. The drain
// check and the insert share d.mu with suspendParked, which runs after the
// drain context is cancelled, so a job parking concurrently with a drain is
// either swept by it or sees the cancelled context here — never neither.
func (d *Drainer) setJobSuspend(id int64, fn func()) {
	d.mu.Lock()
	if fn == nil {
		if _, ok := d.suspenders[id]; ok {
			delete(d.suspenders, id)
			d.activityID.Add(1)
		}
		d.mu.Unlock()
		return
	}
	if _, ok := d.active[id]; !ok {
		d.mu.Unlock()
		return
	}
	if d.drainCtx.Err() != nil {
		d.mu.Unlock()
		go fn()
		return
	}
	if d.suspenders == nil {
		d.suspenders = map[int64]func(){}
	}
	d.suspenders[id] = fn
	d.mu.Unlock()
}

// suspendParked ends every job parked on a human. Callers hold drainMu and
// have already cancelled drainCtx.
func (d *Drainer) suspendParked() {
	d.mu.Lock()
	fns := make([]func(), 0, len(d.suspenders))
	for id, fn := range d.suspenders {
		fns = append(fns, fn)
		delete(d.suspenders, id)
	}
	d.mu.Unlock()
	for _, fn := range fns {
		go fn()
	}
}

// Busy is InFlight minus the jobs parked on a human: the work a graceful
// restart or upgrade actually has to wait for.
func (d *Drainer) Busy() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	parked := len(d.suspenders)
	d.mu.Unlock()
	n := int(d.inFlight.Load()) - parked
	if n < 0 {
		return 0
	}
	return n
}

// setJobStatus moves a tracked job to a new lifecycle state — granted a pool
// slot, or handed it back while the turn waits on the human. No-op if the job
// already finished.
func (d *Drainer) setJobStatus(id int64, status string) {
	if d == nil || id == 0 || status == "" {
		return
	}
	d.mu.Lock()
	if job, ok := d.active[id]; ok {
		job.Status = status
		d.active[id] = job
	}
	d.mu.Unlock()
}

func (d *Drainer) startJob(info DrainerJob) int64 {
	d.activityID.Add(1)
	d.jobs.Add(1)
	d.inFlight.Add(1)
	id := d.nextJobID.Add(1)
	info.ID = id
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now().UTC()
	}
	if info.Status == "" {
		info.Status = JobStatusRunning
	}
	d.mu.Lock()
	if d.active == nil {
		d.active = map[int64]DrainerJob{}
	}
	d.active[id] = info
	// A new turn supersedes the previous turn's outcome.
	delete(d.failed, info.ConversationID)
	d.mu.Unlock()
	return id
}

func (d *Drainer) finishJob(id int64) {
	d.activityID.Add(1)
	d.jobs.Done()
	d.inFlight.Add(-1)
	if id == 0 {
		return
	}
	d.mu.Lock()
	delete(d.active, id)
	delete(d.suspenders, id)
	d.mu.Unlock()
}

// InFlight reports the number of jobs currently bracketed by
// JobStart/JobDone. Non-blocking; safe under concurrent JobStart/JobDone.
// Returns 0 on a nil Drainer.
func (d *Drainer) InFlight() int {
	if d == nil {
		return 0
	}
	n := d.inFlight.Load()
	if n < 0 {
		// Defensive: a mismatched JobDone (bug elsewhere) shouldn't make
		// the upgrade gate report a negative count to operators.
		return 0
	}
	return int(n)
}

// ActivityID changes whenever tracked work starts or finishes. Callers can
// snapshot it before an idle window and use TryStartDrainWhenIdleSince for an
// atomic final check. A nil Drainer has no activity and returns zero.
func (d *Drainer) ActivityID() uint64 {
	if d == nil {
		return 0
	}
	return d.activityID.Load()
}

// Jobs returns a stable snapshot of in-flight jobs with their owner metadata.
func (d *Drainer) Jobs() []DrainerJob {
	if d == nil {
		return []DrainerJob{}
	}
	d.mu.Lock()
	out := make([]DrainerJob, 0, len(d.active))
	for _, job := range d.active {
		out = append(out, job)
	}
	d.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// SetResidentActivity records one adapter-owned source of background work.
// sourceID identifies the concrete resident bridge/callback generation, so a
// retired bridge cannot clear a newer replacement for the same conversation.
// The return value reports whether the visible active set changed.
func (d *Drainer) SetResidentActivity(sourceID string, info DrainerJob, active bool) bool {
	if d == nil || sourceID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !active {
		if _, ok := d.resident[sourceID]; !ok {
			return false
		}
		delete(d.resident, sourceID)
		d.activityID.Add(1)
		return true
	}
	if d.resident == nil {
		d.resident = map[string]DrainerJob{}
	}
	info.Status = JobStatusRunning
	if existing, ok := d.resident[sourceID]; ok {
		// Keep the first timestamp: repeated level frames are state snapshots,
		// not fresh starts of the same background job.
		info.StartedAt = existing.StartedAt
		d.resident[sourceID] = info
		return false
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now().UTC()
	}
	d.resident[sourceID] = info
	d.activityID.Add(1)
	return true
}

// failedOutcomeTTL bounds how long a failure stays in the activity snapshot.
// Browsers only read it on the snapshot that drops the job; the window merely
// covers resident background work that outlives the failed foreground turn.
const failedOutcomeTTL = 10 * time.Minute

// MarkConversationFailed records that the conversation's current turn ended
// in an error. Call it before the job is finished so the snapshot broadcast on
// finish already carries the failure.
func (d *Drainer) MarkConversationFailed(conversationID string) {
	if d == nil || conversationID == "" {
		return
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed == nil {
		d.failed = map[string]time.Time{}
	}
	for id, at := range d.failed {
		if now.Sub(at) > failedOutcomeTTL {
			delete(d.failed, id)
		}
	}
	d.failed[conversationID] = now
}

// ConversationFailedSince reports whether MarkConversationFailed was called for
// the conversation at or after since, i.e. by the turn that started then.
func (d *Drainer) ConversationFailedSince(conversationID string, since time.Time) bool {
	if d == nil || conversationID == "" {
		return false
	}
	d.mu.Lock()
	at, ok := d.failed[conversationID]
	d.mu.Unlock()
	return ok && !at.Before(since)
}

// FailedConversationIDs returns conversations whose latest turn recently
// ended in an error, sorted for stable snapshots.
func (d *Drainer) FailedConversationIDs() []string {
	if d == nil {
		return []string{}
	}
	now := time.Now()
	d.mu.Lock()
	out := make([]string, 0, len(d.failed))
	for id, at := range d.failed {
		if now.Sub(at) <= failedOutcomeTTL {
			out = append(out, id)
		}
	}
	d.mu.Unlock()
	sort.Strings(out)
	return out
}

// ResidentActivities returns background work that should appear in the
// conversation activity snapshot without counting as an in-flight job.
func (d *Drainer) ResidentActivities() []DrainerJob {
	if d == nil {
		return []DrainerJob{}
	}
	d.mu.Lock()
	out := make([]DrainerJob, 0, len(d.resident))
	for _, job := range d.resident {
		out = append(out, job)
	}
	d.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ConversationID < out[j].ConversationID
		}
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}

// WaitJobs blocks until all tracked jobs complete or ctx is done.
// Returns nil on completion, ctx.Err() on timeout.
func (d *Drainer) WaitJobs(ctx context.Context) error {
	if d == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		d.jobs.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
