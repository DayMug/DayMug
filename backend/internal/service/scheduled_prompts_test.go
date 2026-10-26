package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

type scheduledFixture struct {
	rt        *Runtime
	store     *storetest.Fake
	processed chan string
	room      chan []byte
}

func newScheduledFixture(t *testing.T) scheduledFixture {
	t.Helper()
	processed := make(chan string, 8)
	intake, ms := newIntakeFixture(t, processed)
	rt := &Runtime{
		Store:       ms,
		Dispatcher:  intake.Dispatcher,
		Broadcaster: intake.Broadcaster,
		UserHub:     intake.UserHub,
		Pause:       NewPauseGate(),
	}
	rt.ScheduledPrompts = NewScheduledPromptScheduler(rt)
	room := make(chan []byte, 32)
	rt.Broadcaster.Join("c1", "tab", room)
	return scheduledFixture{rt: rt, store: ms, processed: processed, room: room}
}

// frames drains the room and returns the frames of the given type.
func (f scheduledFixture) frames(t *testing.T, typ string) []ServerMessage {
	t.Helper()
	var out []ServerMessage
	for {
		select {
		case data := <-f.room:
			var msg ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			if msg.Type == typ {
				out = append(out, msg)
			}
		default:
			return out
		}
	}
}

func TestScheduleRejectsTimesOutsideTheWindow(t *testing.T) {
	f := newScheduledFixture(t)
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	for name, sendAt := range map[string]time.Time{
		"now":      now,
		"past":     now.Add(-time.Minute),
		"too late": now.Add(MaxScheduledPromptDelay + time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.rt.PromptIntake().Schedule(context.Background(),
				PromptSubmission{ConversationID: "c1", Content: "hi"}, sendAt, now)
			var svcErr *ServiceError
			if !errors.As(err, &svcErr) || svcErr.Message() != "invalid scheduled time" {
				t.Fatalf("err = %v, want invalid scheduled time", err)
			}
		})
	}
	if len(f.store.ScheduledPrompts) != 0 {
		t.Fatalf("rejected prompts were stored: %+v", f.store.ScheduledPrompts)
	}
}

// A scheduled prompt stays out of history until it is due, then enters the
// conversation exactly like a plain send.
func TestScheduledPromptFiresOnlyWhenDue(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	intake := f.rt.PromptIntake()
	soon, err := intake.Schedule(ctx, PromptSubmission{ConversationID: "c1", UserID: "owner-1", Content: "soon"}, now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("schedule soon: %v", err)
	}
	if _, err := intake.Schedule(ctx, PromptSubmission{ConversationID: "c1", Content: "later"}, now.Add(time.Hour), now); err != nil {
		t.Fatalf("schedule later: %v", err)
	}
	lists := f.frames(t, "scheduled_prompts")
	if len(lists) != 2 || len(lists[1].ScheduledPrompts) != 2 || lists[1].ScheduledPrompts[0].ID != soon.ID {
		t.Fatalf("scheduled_prompts broadcasts = %+v", lists)
	}
	if msgs := f.store.Messages["c1"]; len(msgs) != 0 {
		t.Fatalf("scheduled prompt reached history early: %+v", msgs)
	}

	sched := f.rt.ScheduledPrompts
	sched.now = func() time.Time { return now.Add(2 * time.Minute) }
	sched.FireDue(ctx)

	select {
	case got := <-f.processed:
		if got != "soon" {
			t.Fatalf("processed %q, want soon", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("due prompt never reached the dispatcher")
	}
	echoes := f.frames(t, "user_message")
	if len(echoes) != 1 || echoes[0].Content != "soon" || echoes[0].MessageID == "" {
		t.Fatalf("user_message echoes = %+v", echoes)
	}
	remaining, _ := f.store.ListScheduledPrompts(ctx, "c1")
	if len(remaining) != 1 || remaining[0].Content != "later" {
		t.Fatalf("remaining = %+v", remaining)
	}
	if _, found, err := intake.CancelScheduled(ctx, "c1", soon.ID); err != nil || found {
		t.Fatalf("cancelling a fired prompt: found=%v err=%v, want nothing to recall", found, err)
	}
}

func TestScheduledPromptCarriesResolvedAttachments(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	metadata := json.RawMessage(`{"attachments":[{"name":"a.png","mime":"image/png","path":".daymug/agents/agent-1/uploads/a.png","url":"/x"}]}`)
	_, err := f.rt.PromptIntake().Schedule(ctx, PromptSubmission{
		ConversationID: "c1",
		Content:        "look",
		AttachmentMetadata: func(agentID, homeOwnerID string) (json.RawMessage, error) {
			if agentID != "agent-1" || homeOwnerID != "owner-1" {
				t.Errorf("resolved scope = %q/%q", agentID, homeOwnerID)
			}
			return metadata, nil
		},
	}, now.Add(time.Minute), now)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	f.rt.ScheduledPrompts.now = func() time.Time { return now.Add(time.Hour) }
	f.rt.ScheduledPrompts.FireDue(ctx)
	<-f.processed
	msgs := f.store.Messages["c1"]
	if len(msgs) != 1 || string(msgs[0].Metadata) != string(metadata) {
		t.Fatalf("fired message = %+v", msgs)
	}
}

func TestCancelScheduledReturnsThePromptOnce(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	intake := f.rt.PromptIntake()
	saved, err := intake.Schedule(ctx, PromptSubmission{ConversationID: "c1", Content: "maybe"}, now.Add(time.Hour), now)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	f.frames(t, "scheduled_prompts")

	dropped, found, err := intake.CancelScheduled(ctx, "c1", saved.ID)
	if err != nil || !found || dropped.Content != "maybe" {
		t.Fatalf("cancel = %+v %v %v", dropped, found, err)
	}
	lists := f.frames(t, "scheduled_prompts")
	if len(lists) != 1 || len(lists[0].ScheduledPrompts) != 0 {
		t.Fatalf("broadcast after cancel = %+v, want an empty list", lists)
	}
	if _, found, _ := intake.CancelScheduled(ctx, "c1", saved.ID); found {
		t.Fatal("second cancel found the prompt again")
	}
}

// The running loop picks up a prompt added after it went to sleep.
func TestScheduledPromptLoopWakesForNewPrompts(t *testing.T) {
	f := newScheduledFixture(t)
	sched := f.rt.ScheduledPrompts
	sched.now = func() time.Time { return time.Now().Add(time.Hour) }
	sched.Start()
	t.Cleanup(sched.Stop)

	now := time.Now().UTC()
	if _, err := f.rt.PromptIntake().Schedule(context.Background(),
		PromptSubmission{ConversationID: "c1", Content: "wake"}, now.Add(time.Minute), now); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	select {
	case got := <-f.processed:
		if got != "wake" {
			t.Fatalf("processed %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loop never fired the prompt")
	}
}
