package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

const (
	// MaxScheduledPromptDelay bounds how far ahead a message can be held. A
	// year is far beyond any real "send later", and a bound keeps a typo in an
	// absolute date from parking a prompt forever.
	MaxScheduledPromptDelay = 366 * 24 * time.Hour
	// scheduledPromptMaxSleep caps one wait of the scheduler loop, so a missed
	// wake-up or a clock jump costs at most this much delay.
	scheduledPromptMaxSleep = time.Minute
	// scheduledPromptMinSleep keeps a store that keeps failing from turning
	// the loop into a busy spin.
	scheduledPromptMinSleep = time.Second
)

// ScheduledPromptScheduler turns due scheduled prompts into ordinary web-chat
// submissions. A fired prompt goes through PromptIntake.Submit exactly as if
// the user had pressed send at that moment, so it is queued behind any running
// turn and then runs with the same accounts, sandbox and persistence.
//
// Rows live in the store, so prompts survive a restart; ones that came due
// while the server was down fire as soon as it is back.
type ScheduledPromptScheduler struct {
	rt   *Runtime
	wake chan struct{}
	// now is swapped by tests.
	now func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewScheduledPromptScheduler(rt *Runtime) *ScheduledPromptScheduler {
	return &ScheduledPromptScheduler{rt: rt, wake: make(chan struct{}, 1), now: time.Now}
}

// Start launches the loop. Idempotent; a nil scheduler is a no-op.
func (s *ScheduledPromptScheduler) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.loop(ctx, s.done)
}

// Stop ends the loop and waits for it.
func (s *ScheduledPromptScheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done = nil, nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Wake makes the loop re-read the next send time, after a prompt is added.
func (s *ScheduledPromptScheduler) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *ScheduledPromptScheduler) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		s.FireDue(ctx)
		timer := time.NewTimer(s.nextSleep(ctx))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *ScheduledPromptScheduler) nextSleep(ctx context.Context) time.Duration {
	sleep := scheduledPromptMaxSleep
	next, ok, err := s.rt.Store.NextScheduledPromptTime(ctx)
	if err != nil {
		log.Printf("[scheduled] next send time: %v", err)
		return sleep
	}
	if ok {
		// send_at is whole seconds, so wake just past it.
		if until := next.Sub(s.now()) + 50*time.Millisecond; until < sleep {
			sleep = until
		}
	}
	if sleep < scheduledPromptMinSleep {
		sleep = scheduledPromptMinSleep
	}
	return sleep
}

// FireDue submits every prompt whose send time has passed. Exported for tests
// and for the loop; safe to call concurrently with cancels.
func (s *ScheduledPromptScheduler) FireDue(ctx context.Context) {
	due, err := s.rt.Store.ListDueScheduledPrompts(ctx, s.now())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[scheduled] list due prompts: %v", err)
		}
		return
	}
	for _, p := range due {
		if ctx.Err() != nil {
			return
		}
		s.fire(ctx, p)
	}
}

func (s *ScheduledPromptScheduler) fire(ctx context.Context, p store.ScheduledPrompt) {
	// Deleting is the claim: a cancel racing this fire either removed the
	// row first (ErrNotFound here, nothing sent) or finds it gone.
	claimed, err := s.rt.Store.DeleteScheduledPrompt(ctx, p.ConversationID, p.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("[scheduled] claim id=%s conv=%s: %v", p.ID, p.ConversationID, err)
		}
		return
	}
	BroadcastScheduledPrompts(ctx, s.rt.Store, s.rt.Broadcaster, claimed.ConversationID)

	sub := PromptSubmission{
		ConversationID: claimed.ConversationID,
		UserID:         claimed.UserID,
		Content:        claimed.Content,
		OnQueued: func(saved store.Message) {
			// Every tab, the scheduling one included: none of them staged it.
			if data, err := json.Marshal(ServerMessage{
				Type:           "user_message",
				Content:        saved.Content,
				ConversationID: saved.ConversationID,
				MessageID:      saved.ID,
				Metadata:       saved.Metadata,
			}); err == nil {
				s.rt.Broadcaster.Broadcast(saved.ConversationID, data)
			}
		},
	}
	if len(claimed.Metadata) > 0 {
		metadata := claimed.Metadata
		sub.AttachmentMetadata = func(string, string) (json.RawMessage, error) { return metadata, nil }
	}
	if _, err := s.rt.PromptIntake().Submit(ctx, sub); err != nil {
		log.Printf("[scheduled] submit id=%s conv=%s: %v", claimed.ID, claimed.ConversationID, err)
	}
}

// BroadcastScheduledPrompts sends the conversation's current scheduled list to
// every tab in its room.
func BroadcastScheduledPrompts(ctx context.Context, st store.Store, b *Broadcaster, conversationID string) {
	if st == nil || b == nil || conversationID == "" {
		return
	}
	frame, err := ScheduledPromptsFrame(ctx, st, conversationID)
	if err != nil {
		log.Printf("[scheduled] list conv=%s: %v", conversationID, err)
		return
	}
	if data, err := json.Marshal(frame); err == nil {
		b.Broadcast(conversationID, data)
	}
}

// ScheduledPromptsFrame builds the scheduled_prompts frame for one
// conversation.
func ScheduledPromptsFrame(ctx context.Context, st store.Store, conversationID string) (ServerMessage, error) {
	prompts, err := st.ListScheduledPrompts(ctx, conversationID)
	if err != nil {
		return ServerMessage{}, err
	}
	return ServerMessage{
		Type:             "scheduled_prompts",
		ConversationID:   conversationID,
		ScheduledPrompts: prompts,
	}, nil
}

// Schedule holds a web-chat prompt until sendAt instead of submitting it now.
// Attachment metadata is resolved immediately, against the same agent scope
// Submit would use, so the stored row needs nothing from the transport when it
// fires.
func (in *PromptIntake) Schedule(ctx context.Context, sub PromptSubmission, sendAt, now time.Time) (store.ScheduledPrompt, error) {
	if in.Store == nil {
		return store.ScheduledPrompt{}, NewServiceError(http.StatusServiceUnavailable, "prompt dispatcher unavailable", nil)
	}
	if sub.ConversationID == "" {
		return store.ScheduledPrompt{}, BadRequest("init required before input")
	}
	if !sendAt.After(now) || sendAt.Sub(now) > MaxScheduledPromptDelay {
		return store.ScheduledPrompt{}, BadRequest("invalid scheduled time")
	}
	conversation, err := in.Store.GetConversation(ctx, sub.ConversationID)
	if err != nil {
		return store.ScheduledPrompt{}, NotFound("conversation not found")
	}
	metadata, _, _, err := in.attachmentMetadata(ctx, conversation, sub)
	if err != nil {
		return store.ScheduledPrompt{}, BadRequest("invalid attachment metadata")
	}
	saved, err := in.Store.CreateScheduledPrompt(ctx, store.ScheduledPrompt{
		ConversationID: sub.ConversationID,
		UserID:         sub.UserID,
		Content:        sub.Content,
		Metadata:       metadata,
		SendAt:         sendAt,
	})
	if err != nil {
		log.Printf("[scheduled] save conv=%s: %v", sub.ConversationID, err)
		return store.ScheduledPrompt{}, Internal("failed to schedule prompt", err)
	}
	BroadcastScheduledPrompts(ctx, in.Store, in.Broadcaster, sub.ConversationID)
	in.Scheduled.Wake()
	return saved, nil
}

// CancelScheduled drops one scheduled prompt of the conversation and tells
// every tab. found is false when the prompt already fired or was cancelled
// elsewhere — not an error, the caller just has nothing to recall.
func (in *PromptIntake) CancelScheduled(ctx context.Context, conversationID, id string) (store.ScheduledPrompt, bool, error) {
	if in.Store == nil || conversationID == "" || id == "" {
		return store.ScheduledPrompt{}, false, nil
	}
	dropped, err := in.Store.DeleteScheduledPrompt(ctx, conversationID, id)
	if errors.Is(err, store.ErrNotFound) {
		BroadcastScheduledPrompts(ctx, in.Store, in.Broadcaster, conversationID)
		return store.ScheduledPrompt{}, false, nil
	}
	if err != nil {
		return store.ScheduledPrompt{}, false, err
	}
	BroadcastScheduledPrompts(ctx, in.Store, in.Broadcaster, conversationID)
	return dropped, true, nil
}
