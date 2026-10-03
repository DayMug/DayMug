package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// Admission refusals. Each transport maps them to its own wording (the web
// room frames, the IM thread's Chinese notices, /compact's ServiceError); the
// admission itself never picks the text.
var (
	// ErrTurnPaused: the operator's pause switch is on.
	ErrTurnPaused = errors.New("new tasks are paused by an administrator")
	// ErrTurnBusy: the conversation room already has a job. When the caller
	// asked to wait for the room it wraps the context error that ended the wait.
	ErrTurnBusy = errors.New("another run is already in progress for this conversation")
	// ErrTurnDraining: the server is draining for a restart.
	ErrTurnDraining = errors.New("service is restarting")
)

// PoolEntryError is the account pool refusing a ticket outright: unknown
// account, a queue already at its depth limit, or a rate-limit cooldown
// (errors.As still finds the *CooldownError underneath).
type PoolEntryError struct{ Err error }

func (e *PoolEntryError) Error() string { return e.Err.Error() }
func (e *PoolEntryError) Unwrap() error { return e.Err }

// QueueTimeoutError is the admission's own queue budget running out. It is
// distinct from the caller's context ending, which surfaces as that context's
// error: a user who recalled a prompt must not be told the queue is backed up.
type QueueTimeoutError struct {
	Account string
	Waited  time.Duration
}

func (e *QueueTimeoutError) Error() string {
	return fmt.Sprintf("account %q has no free slot after waiting %s", e.Account, e.Waited)
}
func (e *QueueTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// TurnAdmission is the one place a turn — a web prompt, an IM message, a
// /compact summary — is let in to run an agent CLI. The order is load-bearing
// and used to be hand-copied into each of those paths,
// which is how /compact ended up skipping the pause and drain gates:
//
//	pause → conversation room → drainer entry (queued) → account-pool ticket
//	      → bounded wait → drainer entry promoted to running
//
// Whatever was taken is handed back in reverse order on every refusal, and by
// TurnLease.Release on success. All dependencies are optional (nil-safe) so
// focused tests can leave any of them unwired.
type TurnAdmission struct {
	Broadcaster *Broadcaster
	Drainer     *Drainer
	Pause       *PauseGate
	Pool        *Pool
	// UserHub + Store feed the conversation-activity broadcast that follows
	// every drainer status move.
	UserHub *UserHub
	Store   store.Store
}

// TurnTarget is what the turn runs against once it is let in.
type TurnTarget struct {
	// Job is registered with the drainer; its Status is forced to queued
	// until the pool grants a slot.
	Job DrainerJob
	// PoolUserID applies the pool's per-user limit ("" = account limit only).
	PoolUserID  string
	AccountName string
	// Model scopes the pool's rate-limit cooldown check.
	Model string
}

// AdmitSpec describes one admission. Hooks let a transport keep its own side
// effects (acks, queue banners, room frames) in their historical place in the
// sequence without owning the sequence.
type AdmitSpec struct {
	// ConversationID names the Broadcaster room to claim; "" skips the room.
	ConversationID string
	// Cancel is registered with the room so any tab can abort the turn.
	Cancel context.CancelFunc
	// WaitForRoom, when set, waits for a busy room instead of refusing with
	// ErrTurnBusy. The web path uses it to queue behind a provider wakeup.
	WaitForRoom context.Context
	// PauseHeldUpstream skips the pause gate for a caller that already holds
	// paused work somewhere durable (the web dispatcher keeps prompts pending
	// while paused, so one that reached the runner was let through before).
	PauseHeldUpstream bool

	// Target is used unless Resolve is set.
	Target TurnTarget
	// Resolve computes the target once the room is held, for callers whose
	// account resolution must not run while another turn owns the room. An
	// error refuses the admission and is reported unchanged.
	Resolve func() (TurnTarget, error)

	// QueueCtx ends the wait for a pool slot (nil = never). QueueTimeout adds
	// the admission's own budget on top (0 = none) and fails with
	// *QueueTimeoutError when it, not QueueCtx, runs out. Slot re-acquisition
	// after a parked question applies the same budget to the gate's context.
	QueueCtx     context.Context
	QueueTimeout time.Duration

	// ActivityOwnerID receives conversation-activity broadcasts on each
	// drainer status move; "" skips them.
	ActivityOwnerID string
	// TrackAttention persists the conversation's "needs you" state across
	// the turn: cleared when it starts, "waiting" while parked on a
	// question, "done"/"error" when it ends. Only turns the user follows in
	// the web UI set it; IM threads are read where they were asked.
	TrackAttention bool
	// SkipDone keeps a tracked turn that finished cleanly from flagging
	// "done": nobody is waiting on a scheduled run, so its result arriving is
	// not news. Failures and parked questions still flag.
	SkipDone bool

	// OnSuspend runs when a drain ends the turn while it is parked on a
	// question, with that question's payload, just before Cancel. Callers that
	// can offer the question again after a restart persist it here.
	OnSuspend func(questionPayload string)

	// OnRoomClaimed runs once the room is held, before anything else is
	// reserved.
	OnRoomClaimed func()
	// OnTicket runs as soon as the pool issues a ticket, before waiting on
	// it, so queue positions can be relayed. requeue is true when the slot
	// gate re-enters the queue after a parked question.
	OnTicket func(t *Ticket, requeue bool)
	// OnGranted runs once the slot is held, just before the drainer entry is
	// promoted to running.
	OnGranted func()
	// OnRefused runs on a refusal that happens after the room was claimed,
	// with everything acquired still held, so frames it broadcasts land
	// before the room is handed back. It receives the described error.
	OnRefused func(err error)
	// Describe maps an admission error to the transport's wording. It is
	// applied to the error Admit returns and to slot re-acquisition failures
	// the gate reports mid-turn. nil keeps the typed errors.
	Describe func(error) error
}

func (s *AdmitSpec) describe(err error) error {
	if err == nil || s.Describe == nil {
		return err
	}
	return s.Describe(err)
}

// TurnLease is an admitted turn. Release hands back the account slot, the
// drainer entry and the room, in that order; it is idempotent and nil-safe.
type TurnLease struct {
	gate         *TurnSlotGate
	job          DrainerJob
	target       TurnTarget
	drainerDone  func()
	activity     func()
	onFinish     func()
	broadcaster  *Broadcaster
	roomID       string
	roomOnce     sync.Once
	releaseOnce  sync.Once
	roomAcquired bool
	// suspended records that a drain ended the turn on a parked question.
	suspended atomic.Bool
}

// Suspended reports whether a drain ended the turn while it waited on a
// question, as opposed to it finishing or being cancelled.
func (l *TurnLease) Suspended() bool {
	return l != nil && l.suspended.Load()
}

// Gate is the slot gate that lets the running turn give its slot back while
// it waits on the user.
func (l *TurnLease) Gate() *TurnSlotGate {
	if l == nil {
		return nil
	}
	return l.gate
}

// Job is the drainer entry as registered (status queued).
func (l *TurnLease) Job() DrainerJob {
	if l == nil {
		return DrainerJob{}
	}
	return l.job
}

// EndRoom hands the conversation room back ahead of Release, for a caller
// that must broadcast its final frame and free the room before the rest of
// the turn's teardown. Idempotent: the room is only ended once, so a later
// Release cannot end a job another turn has since started in the same room.
func (l *TurnLease) EndRoom() {
	if l == nil {
		return
	}
	l.roomOnce.Do(func() {
		if l.roomAcquired && l.broadcaster != nil {
			l.broadcaster.EndJob(l.roomID)
		}
	})
}

// Release hands back everything the admission took.
func (l *TurnLease) Release() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		l.gate.Close()
		if l.drainerDone != nil {
			l.drainerDone()
			if l.onFinish != nil {
				l.onFinish()
			}
			l.activity()
		}
		l.EndRoom()
	})
}

// Admit runs the admission sequence. On error nothing is held.
func (a *TurnAdmission) Admit(spec AdmitSpec) (*TurnLease, error) {
	lease := &TurnLease{
		broadcaster: a.Broadcaster,
		roomID:      spec.ConversationID,
		activity:    func() { a.broadcastActivity(spec.ActivityOwnerID) },
	}
	refuse := func(err error, roomHeld bool) (*TurnLease, error) {
		err = spec.describe(err)
		if roomHeld && spec.OnRefused != nil {
			spec.OnRefused(err)
		}
		lease.Release()
		return nil, err
	}

	if !spec.PauseHeldUpstream && a.Pause.Paused() {
		return refuse(ErrTurnPaused, false)
	}

	if spec.ConversationID != "" && a.Broadcaster != nil {
		cancel := spec.Cancel
		if cancel == nil {
			cancel = func() {}
		}
		if !a.Broadcaster.StartJob(spec.ConversationID, cancel) {
			if spec.WaitForRoom == nil {
				return refuse(ErrTurnBusy, false)
			}
			if !a.Broadcaster.waitStartJob(spec.WaitForRoom, spec.ConversationID, cancel) {
				return refuse(fmt.Errorf("%w: %w", ErrTurnBusy, spec.WaitForRoom.Err()), false)
			}
		}
		lease.roomAcquired = true
	}
	if spec.OnRoomClaimed != nil {
		spec.OnRoomClaimed()
	}

	target := spec.Target
	if spec.Resolve != nil {
		resolved, err := spec.Resolve()
		if err != nil {
			return refuse(err, true)
		}
		target = resolved
	}
	lease.target = target

	job := target.Job
	job.Status = JobStatusQueued
	done, setStatus, setSuspend, ok := a.Drainer.TryJobStartSuspendable(job)
	if !ok {
		return refuse(ErrTurnDraining, true)
	}
	lease.job = job
	lease.drainerDone = done
	var granted bool
	if spec.TrackAttention {
		admittedAt := time.Now()
		a.setAttention(spec.ConversationID, "")
		// Runs before the finishing activity broadcast, so a client that
		// refetches on that frame already sees the outcome. A turn refused
		// before it got a slot only surfaces if the refusal was an error: a
		// recalled or drained prompt produced nothing to come back for. Nor
		// does a turn the user already followed up on: the queued prompt
		// runs next, and the badge belongs to whichever turn ends the chain.
		lease.onFinish = func() {
			failed := a.Drainer.ConversationFailedSince(spec.ConversationID, admittedAt)
			switch {
			case lease.Suspended():
				// The question still stands and comes back after the restart.
				a.setAttention(spec.ConversationID, store.AttentionWaiting)
			case failed:
				a.setAttention(spec.ConversationID, store.AttentionError)
			case granted && !spec.SkipDone && !hasQueuedFollowUp(a.Store, spec.ConversationID):
				a.setAttention(spec.ConversationID, store.AttentionDone)
			}
		}
		inner := setStatus
		setStatus = func(status string) {
			inner(status)
			switch status {
			case JobStatusWaiting:
				a.setAttention(spec.ConversationID, store.AttentionWaiting)
			case JobStatusQueued, JobStatusRunning:
				a.setAttention(spec.ConversationID, "")
			}
		}
	}
	lease.activity()

	ticket, err := a.acquire(spec.QueueCtx, &spec, target, false)
	if err != nil {
		return refuse(err, true)
	}
	lease.gate = NewTurnSlotGate(
		ticket,
		func(ctx context.Context) (*Ticket, error) {
			t, err := a.acquire(ctx, &spec, target, true)
			return t, spec.describe(err)
		},
		setStatus,
		lease.activity,
	)
	lease.gate.onQuestion = func(payload string) {
		if payload == "" {
			setSuspend(nil)
			return
		}
		setSuspend(func() {
			lease.suspended.Store(true)
			if spec.OnSuspend != nil {
				spec.OnSuspend(payload)
			}
			if spec.Cancel != nil {
				spec.Cancel()
			}
		})
	}
	if spec.OnGranted != nil {
		spec.OnGranted()
	}
	granted = true
	setStatus(JobStatusRunning)
	lease.activity()
	return lease, nil
}

// acquire enters the pool and waits for the slot. nil + nil means no pool is
// wired (tests): the turn runs unthrottled.
//
// The budget is scoped to this call, never to the caller's context, so it can
// only ever cut short the queueing phase — never a reply already streaming.
func (a *TurnAdmission) acquire(ctx context.Context, spec *AdmitSpec, target TurnTarget, requeue bool) (*Ticket, error) {
	if a.Pool == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticket, err := a.Pool.EnterForUser(target.PoolUserID, target.AccountName, target.Model)
	if err != nil {
		return nil, &PoolEntryError{Err: err}
	}
	if spec.OnTicket != nil {
		spec.OnTicket(ticket, requeue)
	}
	waitCtx := ctx
	if spec.QueueTimeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, spec.QueueTimeout)
		defer cancel()
	}
	if err := ticket.Wait(waitCtx); err != nil {
		if spec.QueueTimeout > 0 && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, &QueueTimeoutError{Account: target.AccountName, Waited: spec.QueueTimeout}
		}
		return nil, err
	}
	return ticket, nil
}

// setAttention is best-effort: a lost flag only costs a badge, never the turn.
func (a *TurnAdmission) setAttention(conversationID, state string) {
	if a.Store == nil || conversationID == "" {
		return
	}
	if err := a.Store.SetConversationAttention(context.Background(), conversationID, state); err != nil {
		log.Printf("conversation %s: set attention %q: %v", conversationID, state, err)
	}
}

func (a *TurnAdmission) broadcastActivity(ownerID string) {
	BroadcastConversationActivity(context.Background(), a.UserHub, a.Drainer, a.Store, ownerID)
}
