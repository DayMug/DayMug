package streamcommon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// ErrConsumerStalled means nobody read the turn's event channel for
// SendTimeout. It is a failure of the reader, not of the model, and it is
// reported as this turn's error so the run ends instead of hanging.
var ErrConsumerStalled = errors.New("event consumer stalled")

// DefaultSendTimeout bounds one send on a turn's event channel.
//
// Sized against what the consumer legitimately does per frame, not against
// how fast the model streams. service.runAgentStream persists tool rows and
// extracts artifacts inline, and Broadcaster.Broadcast already drops frames
// for slow websocket clients rather than blocking — so no healthy consumer
// spends anywhere near this long on one frame, while a deadlocked or
// abandoned one never recovers no matter how long we wait.
const DefaultSendTimeout = 30 * time.Second

// Emitter is the one place that owns what an adapter may do when it writes to
// a turn's event channel. Every adapter routed its own sends before this
// existed: StreamLines selected on ctx, codexcli's forwarder selected on its
// run context, and codexapp sent bare. The bare sends are the reason this is
// a type rather than a convention — a consumer that stops reading wedges the
// pump loop, and in codexapp that loop is also what services the stall
// watchdog and MaxSilentTimeout, so the one mechanism that should have caught
// the hang was blocked by it.
//
// The rule: a send waits for the consumer, the caller's context, or
// SendTimeout, whichever comes first, and the error is sticky so an adapter
// that streams on after a failure pays the timeout once rather than per frame.
//
// # Why nothing is dropped
//
// The obvious refinement — drop text deltas under backpressure, since the
// authoritative full text arrives in the terminal KindResult frame — does not
// hold here. service.runAgentStream accumulates KindDelta into deltaBuf and
// KindThinkingDelta into thinkingBuf, and both become *persisted* content:
// deltaBuf is the assistant text saved when a turn is cancelled or ends with
// an empty result, and thinkingBuf is saved alongside every result. Dropping
// either would silently truncate stored history rather than a live view.
//
// Failing the turn loudly is therefore the correct terminal action today. A
// drop tier becomes safe only once each message carries a reconciliation
// frame with its authoritative full text (Mentor's streamcommon does this
// with an Event.Replace flag); until then, do not add one.
type Emitter struct {
	ctx     context.Context
	out     chan<- agent.StreamEvent
	timeout time.Duration
	// label names the adapter in the stall error and log line; without it a
	// stalled consumer looks identical whichever backend hit it.
	label string
	// err is sticky: the first failure ends the emitter for good.
	err error
	// stalled distinguishes "the consumer went away" from "the caller's
	// context ended", which callers report differently.
	stalled bool
	// warned caps malformed-event logging at one line per turn.
	warned bool
}

// NewEmitter wraps a turn's event channel. label names the adapter
// ("codex CAS", "claude CLI") and appears in the stall error.
func NewEmitter(ctx context.Context, out chan<- agent.StreamEvent, label string) *Emitter {
	return &Emitter{ctx: ctx, out: out, timeout: DefaultSendTimeout, label: label}
}

// WithTimeout overrides DefaultSendTimeout. Non-positive values are ignored so
// a zero-valued config field can't turn the bound off.
func (e *Emitter) WithTimeout(d time.Duration) *Emitter {
	if d > 0 {
		e.timeout = d
	}
	return e
}

// Emit sends one event. It returns ctx.Err() if the caller's context ended
// first, ErrConsumerStalled if the consumer went away, and nil otherwise.
//
// Callers must propagate the error: it means this turn cannot deliver its
// remaining output, and continuing to produce events would burn upstream
// tokens nobody will ever see.
func (e *Emitter) Emit(evt agent.StreamEvent) error {
	if e.err != nil {
		return e.err
	}
	e.warnIfInvalid(evt)
	timer := time.NewTimer(e.timeout)
	defer timer.Stop()
	select {
	case e.out <- evt:
		return nil
	case <-e.ctx.Done():
		e.err = e.ctx.Err()
		return e.err
	case <-timer.C:
		e.stalled = true
		// The kind is the useful half of the diagnosis: a stall on a delta
		// says the consumer is slow, a stall on a tool_result says it is
		// stuck inside its own persistence path.
		log.Printf("[%s] event consumer stalled for %s on a %s frame; abandoning the rest of this turn",
			e.label, e.timeout, evt.Kind)
		e.err = fmt.Errorf("%w: no reader for %s after %s (frame: %s)", ErrConsumerStalled, e.label, e.timeout, evt.Kind)
		return e.err
	}
}

// EmitAll sends events in order, stopping at the first failure.
func (e *Emitter) EmitAll(events []agent.StreamEvent) error {
	for _, evt := range events {
		if err := e.Emit(evt); err != nil {
			return err
		}
	}
	return nil
}

// warnIfInvalid reports a malformed event without changing what happens to
// it. Dropping would be worse than forwarding: ServerMessage already degrades
// a non-JSON payload to a plain string rather than failing the frame, so a
// bad event renders badly but a dropped one leaves a hole in the transcript
// with nothing to explain it. The point here is that the adapter gets named
// in the log at the moment it produced the event, instead of someone later
// finding an empty tool chip in the UI and having nowhere to start.
//
// Once per emitter: a parser bug that mangles one frame usually mangles every
// frame of that kind, and a turn's worth of identical lines buries the rest of
// the log.
func (e *Emitter) warnIfInvalid(evt agent.StreamEvent) {
	if e.warned {
		return
	}
	if err := evt.Validate(); err != nil {
		e.warned = true
		log.Printf("[%s] emitting a malformed event: %v (further malformed events this turn are not logged)", e.label, err)
	}
}

// Err returns the sticky failure, or nil while the emitter is still usable.
func (e *Emitter) Err() error { return e.err }

// Stalled reports whether this emitter gave up on its consumer, as opposed to
// ending because the caller's context was cancelled.
func (e *Emitter) Stalled() bool { return e.stalled }
