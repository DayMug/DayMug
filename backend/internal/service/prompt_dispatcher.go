package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// PromptProcessor is the dispatcher's hook into the heavy claude pipeline.
// Called once per peeked prompt with a context that is cancelled when the
// dispatcher itself is shutting down. The processor owns the full run
// lifecycle: pool-slot acquisition, atomic claim (state transition
// pending → processing) once the slot is granted, the `prompt_started`
// broadcast, claude execution, and persisting tool/result/error rows.
//
// The dispatcher hands the processor a row that is still 'pending': the
// processor MUST call store.ClaimPendingPromptByID(prompt.ID) before
// emitting any visible side-effect, and must bail cleanly (release any
// acquired resources) if the claim returns ErrNotFound — that's the
// signal that a user-initiated cancel deleted the row while the worker
// was waiting for a pool slot.
type PromptProcessor func(ctx context.Context, prompt store.Message)

// dispatcherWorkerIdleTimeout caps how long a per-conversation worker
// hangs around without work before exiting. Bounded so idle chats don't
// keep goroutines alive forever; long enough that a typing user doesn't
// pay the spin-up cost on every send.
const dispatcherWorkerIdleTimeout = 30 * time.Second

// Dispatcher serializes claude prompts per conversation. Every "input"
// WS message lands here via Enqueue; the dispatcher persists the prompt
// (so it survives a browser close), then a per-conversation worker
// claims and processes it FIFO. Cancel removes still-pending prompts and
// returns them so the WS handler can ship the content back to the
// client editor for re-editing.
type Dispatcher struct {
	store     store.Store
	processor PromptProcessor
	// pause holds the operator's "stop starting new work" switch. While it
	// reads paused, workers park instead of handing rows to the processor, so
	// prompts stay at queue_status='pending' — durable, and picked up again by
	// ResumeAll (switch flipped back) or by Start (server restarted).
	// nil-safe: an unwired dispatcher is never paused.
	pause *PauseGate

	mu      sync.Mutex
	workers map[string]*workerHandle
	// preClaimCancels holds a cancel func for every prompt currently
	// being held by a processor BEFORE its row has been claimed (i.e.
	// the processor is parked on the per-account pool slot). Keyed by
	// prompt id. CancelOnePending fires the matching cancel after the
	// row is deleted so the worker bails out of its pool wait, releases
	// the ticket back to the queue, and lets sibling waiters' positions
	// reflect the new reality. Without this, the dead ticket lingers in
	// the pool queue and sibling positions broadcast as if the
	// just-cancelled prompt were still in line.
	preClaimCancels map[string]context.CancelFunc

	rootCtx    context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup
}

type workerHandle struct {
	// notify is buffered (cap 1) so a kicker never blocks; the worker
	// drains it when it loops, so a coalesced burst of enqueues still
	// translates into one wake-up rather than a queue of redundant
	// signals.
	notify chan struct{}
}

// NewDispatcher returns a Dispatcher with no workers running yet. Call
// Start to recover unfinished prompts and begin accepting new ones.
func NewDispatcher(s store.Store, p PromptProcessor) *Dispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dispatcher{
		store:           s,
		processor:       p,
		workers:         make(map[string]*workerHandle),
		preClaimCancels: make(map[string]context.CancelFunc),
		rootCtx:         ctx,
		rootCancel:      cancel,
	}
}

// SetPauseGate wires the operator pause switch. Called once during route
// assembly, before any worker starts, because workers read the field without
// a lock. A dispatcher without a gate — or a nil one, which route assembly
// produces in tests that never build a terminal runtime — simply never pauses.
func (d *Dispatcher) SetPauseGate(g *PauseGate) {
	if d == nil {
		return
	}
	d.pause = g
}

// Start performs crash-recovery: any 'processing' rows are demoted back
// to 'pending' (we don't know whether the previous run actually finished
// before the crash, so re-running is the safe default), and a worker is
// kicked off for every conversation that still has un-finished prompts.
//
// A prompt that has already been recovered too many times is abandoned
// rather than resumed (see store.ResetProcessingToPending) so a prompt that
// restarts the server can't wedge it in a crash-recover-rerun loop.
//
// Idempotent — safe to call once at server boot. Returns the number of rows
// reset, the number abandoned, and the number of conversations resumed so the
// caller can log a single recovery line.
func (d *Dispatcher) Start(ctx context.Context) (resetRows, abandoned, resumedConvs int, err error) {
	n, ab, err := d.store.ResetProcessingToPending(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	resumed, err := d.ResumeAll(ctx)
	if err != nil {
		return n, ab, 0, err
	}
	return n, ab, resumed, nil
}

// ResumeAll wakes a worker for every conversation that still has a pending
// prompt, and returns how many were kicked.
//
// Two callers, one job: boot recovery (via Start) and lifting the operator
// pause. Both face the same situation — rows sitting at 'pending' with no live
// worker, because a paused worker exits on its idle timeout exactly like one
// that never existed. Re-deriving the list from the store rather than tracking
// paused conversations in memory is what makes the two paths identical, and is
// why an operator can restart the service mid-pause and lose nothing.
func (d *Dispatcher) ResumeAll(ctx context.Context) (int, error) {
	convs, err := d.store.ListConversationsWithPending(ctx)
	if err != nil {
		return 0, err
	}
	for _, convID := range convs {
		d.kickWorker(convID)
	}
	return len(convs), nil
}

// Stop cancels the dispatcher root context and waits for every worker to
// exit. Workers honour the cancel between prompts but won't interrupt a
// processor that is currently executing — that's the processor's
// responsibility (typically scoped to its own background context for
// claude so a graceful shutdown doesn't kill an in-flight reply).
func (d *Dispatcher) Stop() {
	d.rootCancel()
	d.wg.Wait()
}

// Enqueue persists a fresh user prompt as 'pending' and signals the
// per-conversation worker. The returned Message carries the canonical
// id so the WS handler can ack it back to the sender.
func (d *Dispatcher) Enqueue(ctx context.Context, conversationID, content string) (store.Message, error) {
	msg, err := d.store.EnqueuePrompt(ctx, conversationID, content)
	if err != nil {
		return store.Message{}, err
	}
	d.kickWorker(conversationID)
	return msg, nil
}

// SavePrompt persists a fresh user prompt as 'pending' WITHOUT waking
// the per-conversation worker. The caller must follow up with KickWorker
// once it has finished any prep work that must complete before the
// dispatcher can broadcast prompt_started — typically the WS handler's
// peer-tab user_message echo, which would otherwise race the worker's
// Broadcast call and arrive at peer tabs out of order, making the
// assistant's streaming reply visible before the prompt that triggered
// it.
func (d *Dispatcher) SavePrompt(ctx context.Context, conversationID, content string) (store.Message, error) {
	return d.store.EnqueuePrompt(ctx, conversationID, content)
}

// SavePromptWithMetadata is the attachment-aware counterpart used by the web
// chat input. It intentionally leaves SavePrompt unchanged for callers that
// have no metadata while preserving the same pending-queue semantics.
func (d *Dispatcher) SavePromptWithMetadata(
	ctx context.Context,
	conversationID, content string,
	metadata json.RawMessage,
) (store.Message, error) {
	msg := store.Message{
		ID:             uuid.New().String(),
		ConversationID: conversationID,
		Role:           "user",
		Content:        content,
		QueueStatus:    "pending",
		Metadata:       metadata,
	}
	if err := d.store.SaveMessage(ctx, msg); err != nil {
		return store.Message{}, err
	}
	return msg, nil
}

// KickWorker is the public counterpart to kickWorker, used by callers
// that staged a prompt via SavePrompt and now want the worker to claim
// it. Idempotent — sending a notify when the buffered channel is full
// is a no-op (the worker already has a wake-up pending).
func (d *Dispatcher) KickWorker(conversationID string) {
	d.kickWorker(conversationID)
}

// CompleteSteeredPrompt removes the pending marker after app-server accepted
// the message into the current turn. The row keeps its original rowid so the
// transcript places it at the exact point where the user intervened.
func (d *Dispatcher) CompleteSteeredPrompt(ctx context.Context, messageID string) error {
	return d.store.MarkPromptDone(ctx, messageID)
}

// CancelPending removes every still-pending prompt for the conversation
// and returns the deleted rows in FIFO order. Does NOT touch the
// currently-processing prompt (its partial output is already tied to it
// in history) — cancelling the live claude run is the broadcaster's
// job, called in parallel by the WS cancel handler.
func (d *Dispatcher) CancelPending(ctx context.Context, conversationID string) ([]store.Message, error) {
	return d.store.DeletePendingPrompts(ctx, conversationID)
}

// CancelOnePending removes a single still-pending prompt by id. Used by
// the per-message recall affordance in the staging area UI: the user can
// click an individual queued prompt to drop it without affecting the
// currently-running prompt or any other queued siblings. Returns
// store.ErrNotFound when the row is gone (already claimed, already done,
// or never existed).
//
// On successful deletion we also fire any registered pre-claim cancel
// for this prompt. That unparks the processor that was waiting on the
// per-account pool slot: it bails out of its wait, releases the ticket,
// and the pool's position broadcasts to sibling waiters become accurate.
// Without this, the dead ticket would linger in the pool queue and a
// re-sent prompt would appear to inherit the cancelled prompt's
// position, confusing the staging-area UI.
func (d *Dispatcher) CancelOnePending(ctx context.Context, messageID string) (store.Message, error) {
	msg, err := d.store.DeletePendingPrompt(ctx, messageID)
	if err == nil {
		d.firePreClaimCancel(messageID)
	}
	return msg, err
}

// RegisterPreClaim records the cancel func for a prompt whose processor
// is about to enter (or is already in) the pool wait. The cancel is
// fired by CancelOnePending / CancelPending so the processor can bail
// out before claiming the row. Callers MUST pair every Register with a
// matching UnregisterPreClaim (typically via defer) once the pre-claim
// window closes — either because the row was successfully claimed and
// the run is starting, or because the processor returned without
// claiming.
func (d *Dispatcher) RegisterPreClaim(messageID string, cancel context.CancelFunc) {
	if messageID == "" || cancel == nil {
		return
	}
	d.mu.Lock()
	d.preClaimCancels[messageID] = cancel
	d.mu.Unlock()
}

// UnregisterPreClaim drops the pre-claim cancel registration for a
// prompt. Idempotent — safe to call when no entry exists (e.g. after a
// successful firePreClaimCancel run, the cancel was already removed).
func (d *Dispatcher) UnregisterPreClaim(messageID string) {
	if messageID == "" {
		return
	}
	d.mu.Lock()
	delete(d.preClaimCancels, messageID)
	d.mu.Unlock()
}

// firePreClaimCancel runs and removes the registered cancel for a
// prompt, if any. Always pops the entry under the lock so a racing
// UnregisterPreClaim from the processor's defer doesn't leave a
// double-fire window.
func (d *Dispatcher) firePreClaimCancel(messageID string) {
	d.mu.Lock()
	cancel, ok := d.preClaimCancels[messageID]
	if ok {
		delete(d.preClaimCancels, messageID)
	}
	d.mu.Unlock()
	if ok {
		cancel()
	}
}

// kickWorker creates or wakes the worker for the given conversation.
// Holding d.mu while sending notify is what closes the
// idle-timeout/enqueue race: a worker that's about to remove itself
// because it timed out re-checks the notify channel under the same
// lock, so a kick that lands in that window still gets honoured (either
// by the about-to-exit worker continuing its loop, or by a fresh
// worker spun up after the old one removed itself).
func (d *Dispatcher) kickWorker(conversationID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if w, ok := d.workers[conversationID]; ok {
		select {
		case w.notify <- struct{}{}:
		default:
		}
		return
	}
	w := &workerHandle{notify: make(chan struct{}, 1)}
	d.workers[conversationID] = w
	d.wg.Add(1)
	go d.runWorker(conversationID, w)
}

func (d *Dispatcher) runWorker(conversationID string, w *workerHandle) {
	defer d.wg.Done()

	for {
		// Operator pause: hold the line before peeking, so nothing is handed
		// to the processor and every queued prompt stays exactly where a
		// restart can still find it. Parking in waitForWork (rather than
		// spinning on the same row) lets the worker retire on its normal idle
		// timeout; ResumeAll re-creates it from the store when the pause lifts.
		if d.pause.Paused() {
			if !d.waitForWork(w, conversationID) {
				return
			}
			continue
		}

		// Peek (read-only) instead of claiming up-front. The row stays
		// at queue_status='pending' so the staging-area UI keeps showing
		// it while the processor parks on the per-account pool slot.
		// Without this, three concurrent conversations on a 1-slot pool
		// would each flip a row to 'processing' and broadcast
		// prompt_started before they actually had a slot — the user sees
		// the prompt leave the staging area but never makes progress.
		msg, err := d.store.PeekNextPendingPrompt(d.rootCtx, conversationID)
		if errors.Is(err, store.ErrNotFound) {
			if !d.waitForWork(w, conversationID) {
				return
			}
			continue
		}
		if err != nil {
			log.Printf("[dispatcher] peek conv=%s: %v", conversationID, err)
			select {
			case <-d.rootCtx.Done():
				d.dropWorker(conversationID, w)
				return
			case <-time.After(time.Second):
			}
			continue
		}

		// Hand the still-pending row to the processor. The processor is
		// responsible for: acquiring a pool slot, atomically claiming
		// the row by id (which is the moment 'processing' becomes
		// visible and prompt_started is broadcast), running claude, and
		// persisting the reply. If a user-initiated cancel deletes the
		// row while the processor is parked on the pool slot, the
		// processor's claim-by-id call will return ErrNotFound and the
		// processor bails cleanly.
		d.processor(d.rootCtx, msg)

		// Best-effort sweep so a processor that returned without
		// claiming the row (e.g. the conversation/user lookup failed
		// before pool acquisition) doesn't spin the worker on the same
		// 'pending' row forever. MarkPromptDone is a no-op for rows
		// that are already done, were deleted by a cancel, or were
		// transitioned to 'processing' and back to '' by the processor
		// itself.
		markCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := d.store.MarkPromptDone(markCtx, msg.ID); err != nil {
			log.Printf("[dispatcher] mark done id=%s: %v", msg.ID, err)
		}
		cancel()
	}
}

// waitForWork blocks until the worker has something to do (returns true)
// or should exit (returns false). Idle-timeout exit path removes the
// worker map entry under d.mu so a concurrent kickWorker can either
// nudge us back into the loop (if it landed first) or spin up a fresh
// replacement (if the timeout won).
func (d *Dispatcher) waitForWork(w *workerHandle, conversationID string) bool {
	timer := time.NewTimer(dispatcherWorkerIdleTimeout)
	defer timer.Stop()
	select {
	case <-d.rootCtx.Done():
		d.dropWorker(conversationID, w)
		return false
	case <-w.notify:
		return true
	case <-timer.C:
		d.mu.Lock()
		select {
		case <-w.notify:
			d.mu.Unlock()
			return true
		default:
		}
		if cur, ok := d.workers[conversationID]; ok && cur == w {
			delete(d.workers, conversationID)
		}
		d.mu.Unlock()
		return false
	}
}

func (d *Dispatcher) dropWorker(conversationID string, w *workerHandle) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.workers[conversationID]; ok && cur == w {
		delete(d.workers, conversationID)
	}
}
