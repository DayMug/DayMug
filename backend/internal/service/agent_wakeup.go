package service

import (
	"context"
	"log"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// WakeupOrigin marks an assistant row the provider produced on its own, once
// background work a previous turn armed finally settled. It rides on the row's
// metadata so the chat can tell a turn the user asked for from one that woke
// up by itself — and so a page refresh still can.
const WakeupOrigin = "background_wakeup"

// wakeupSettleGrace bounds how long the copier waits for frames still buffered
// behind the settle signal. The bridge stops writing before it settles, so this
// only ever covers the queue drain.
const wakeupSettleGrace = 5 * time.Second

// WakeupRunner drives turns nobody prompted.
//
// A resident bridge can start a turn on its own: a Monitor fires, the model
// reacts, and frames arrive with no pending prompt and no caller waiting. That
// turn still needs everything an ordinary one gets — usage accounting,
// rate-limit cooldowns, tool rows, artifact extraction, the assistant row —
// which is why it goes through AgentStreamer rather than a second, thinner
// writer that would drift from it.
type WakeupRunner struct {
	Streamer  *AgentStreamer
	Broadcast func(convID string, msg ServerMessage)
	// Resolve supplies the conversation's current routing. It is called at
	// wakeup time, not captured at park time: a bridge can outlive an edit to
	// the conversation, and a deleted one must refuse rather than resurrect.
	Resolve func(convID string) (WakeupTarget, bool)
}

// WakeupTarget is what a conversation needs for a turn to be attributable.
type WakeupTarget struct {
	Backend         agent.Backend
	WorkDir         string
	ArtifactRootDir string
	OwnerID         string
	AccountName     string
	Model           string
}

// Sink returns the pair a backend's Residency.Wake contract expects: a channel
// to stream a provider-initiated turn into, and a callback to settle it.
//
// It must not block — the adapter calls it from the goroutine draining the
// provider's stdout — so the turn itself runs on its own goroutine and this
// only hands back the plumbing.
func (w *WakeupRunner) Sink(convID string) func() (chan<- agent.StreamEvent, func(error)) {
	if w == nil {
		return nil
	}
	return func() (chan<- agent.StreamEvent, func(error)) {
		frames := make(chan agent.StreamEvent, 64)
		settled := make(chan error, 1)
		go w.deliver(convID, frames, settled)
		return frames, func(err error) { settled <- err }
	}
}

// deliver runs one provider-initiated turn to completion.
func (w *WakeupRunner) deliver(convID string, frames <-chan agent.StreamEvent, settled <-chan error) {
	target, ok := w.resolve(convID)
	if !ok {
		// Nothing to attribute the turn to. Drain so the adapter's router
		// isn't left blocked on a send nobody will ever read.
		go drainFrames(frames, settled)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	broadcast := func(msg ServerMessage) { w.emit(convID, msg) }

	// Registering the job is what makes Stop work on a wakeup turn and what
	// drives the composer's thinking state. Losing the race is not fatal:
	// something else (a /compact, a prompt that landed in the same instant)
	// owns the room, the provider is already producing tokens, and dropping
	// the turn would be worse than rendering it without the status frames.
	owned := w.Streamer != nil && w.Streamer.Broadcaster != nil &&
		w.Streamer.Broadcaster.StartJob(convID, cancel)
	if owned {
		defer func() {
			broadcast(ServerMessage{Type: "status", Status: "ready"})
			w.Streamer.Broadcaster.EndJob(convID)
		}()
		broadcast(ServerMessage{Type: "status", Status: "thinking"})
	} else {
		log.Printf("[wakeup] conv=%s another job owns the room; delivering without status frames", convID)
	}

	out := w.Streamer.Run(ctx, AgentStreamRequest{
		Backend:         target.Backend,
		WorkDir:         target.WorkDir,
		ArtifactRootDir: target.ArtifactRootDir,
		ConversationID:  convID,
		OwnerID:         target.OwnerID,
		AccountName:     target.AccountName,
		// EnableUserQuestions is what registers the room's answer hook, and a
		// wakeup needs it as much as a prompted turn does: the resident bridge
		// was spawned with questions enabled, so the model can still call
		// AskUserQuestion once it wakes up. Without the flag the question card
		// renders but nothing is listening, and the reply comes back as "the
		// user question is no longer active" — a turn the user can only escape
		// by pressing Stop.
		Opts:                  agent.RunRequest{Model: target.Model, EnableUserQuestions: true},
		Mode:                  AgentStreamPerResult,
		ForwardSubagentFrames: true,
		Notify:                true,
		ResultOrigin:          WakeupOrigin,
		Attach:                attachFrames(frames, settled),
		Broadcast:             broadcast,
	})
	if out.Err != nil {
		log.Printf("[wakeup] conv=%s turn failed: %v", convID, out.Err)
	}
}

func (w *WakeupRunner) resolve(convID string) (WakeupTarget, bool) {
	if w == nil || w.Streamer == nil || w.Resolve == nil {
		return WakeupTarget{}, false
	}
	target, ok := w.Resolve(convID)
	if !ok || target.Backend == nil {
		log.Printf("[wakeup] conv=%s is no longer deliverable; dropping the provider-initiated turn", convID)
		return WakeupTarget{}, false
	}
	return target, true
}

func (w *WakeupRunner) emit(convID string, msg ServerMessage) {
	if w.Broadcast != nil {
		w.Broadcast(convID, msg)
	}
}

// attachFrames adapts the adapter's push channel to AgentStreamer's pull
// contract: copy until the turn settles, then drain whatever is still queued
// so no frame is lost between the last send and the settle signal.
func attachFrames(frames <-chan agent.StreamEvent, settled <-chan error) func(context.Context, chan<- agent.StreamEvent) error {
	return func(ctx context.Context, outputCh chan<- agent.StreamEvent) error {
		for {
			select {
			case evt := <-frames:
				select {
				case outputCh <- evt:
				case <-ctx.Done():
					return ctx.Err()
				}
			case err := <-settled:
				deadline := time.After(wakeupSettleGrace)
				for {
					select {
					case evt := <-frames:
						select {
						case outputCh <- evt:
						case <-ctx.Done():
							return ctx.Err()
						}
					case <-deadline:
						return err
					default:
						return err
					}
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// drainFrames keeps an undeliverable turn from wedging the adapter's router,
// which sends into the channel while holding the bridge lock.
func drainFrames(frames <-chan agent.StreamEvent, settled <-chan error) {
	for {
		select {
		case <-frames:
		case <-settled:
			return
		case <-time.After(parkedTurnDrainTimeout):
			return
		}
	}
}

// parkedTurnDrainTimeout bounds the drain of a turn nobody will render. The
// bridge settles its turns, so reaching this means the process died mid-turn.
const parkedTurnDrainTimeout = 2 * time.Minute
