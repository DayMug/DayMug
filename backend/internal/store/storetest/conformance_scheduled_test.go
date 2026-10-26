package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

func scheduledIDs(prompts []store.ScheduledPrompt) []string {
	ids := make([]string, len(prompts))
	for i, p := range prompts {
		ids[i] = p.ID
	}
	return ids
}

// Scheduled prompts come due by send time, never belong to the message
// history, and are claimed exactly once by whoever deletes them first.
func TestScheduledPromptLifecycle(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")
			newQueueConversation(t, s, "c2")

			base := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
			later, err := s.CreateScheduledPrompt(ctx, store.ScheduledPrompt{
				ID: "later", ConversationID: "c1", UserID: "u1", Content: "second",
				Metadata: []byte(`{"attachments":[]}`), SendAt: base.Add(time.Hour + 500*time.Millisecond),
			})
			if err != nil {
				t.Fatalf("create later: %v", err)
			}
			if !later.SendAt.Equal(base.Add(time.Hour)) || later.UserID != "u1" || string(later.Metadata) != `{"attachments":[]}` {
				t.Errorf("created = %+v", later)
			}
			if _, err := s.CreateScheduledPrompt(ctx, store.ScheduledPrompt{
				ID: "sooner", ConversationID: "c1", Content: "first", SendAt: base,
			}); err != nil {
				t.Fatalf("create sooner: %v", err)
			}
			if _, err := s.CreateScheduledPrompt(ctx, store.ScheduledPrompt{
				ID: "other", ConversationID: "c2", Content: "elsewhere", SendAt: base.Add(30 * time.Minute),
			}); err != nil {
				t.Fatalf("create other: %v", err)
			}

			listed, err := s.ListScheduledPrompts(ctx, "c1")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got, want := scheduledIDs(listed), []string{"sooner", "later"}; !reflect.DeepEqual(got, want) {
				t.Errorf("list c1 = %v, want %v", got, want)
			}
			if msgs := listAll(t, s, "c1"); len(msgs) != 0 {
				t.Errorf("scheduled prompts leaked into history: %+v", msgs)
			}

			next, ok, err := s.NextScheduledPromptTime(ctx)
			if err != nil || !ok || !next.Equal(base) {
				t.Errorf("next = %v %v %v, want %v", next, ok, err, base)
			}
			due, err := s.ListDueScheduledPrompts(ctx, base.Add(45*time.Minute))
			if err != nil {
				t.Fatalf("due: %v", err)
			}
			if got, want := scheduledIDs(due), []string{"sooner", "other"}; !reflect.DeepEqual(got, want) {
				t.Errorf("due = %v, want %v", got, want)
			}

			if _, err := s.DeleteScheduledPrompt(ctx, "c2", "sooner"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("delete through the wrong conversation: err = %v, want ErrNotFound", err)
			}
			deleted, err := s.DeleteScheduledPrompt(ctx, "c1", "sooner")
			if err != nil || deleted.Content != "first" {
				t.Fatalf("delete = %+v, %v", deleted, err)
			}
			if _, err := s.DeleteScheduledPrompt(ctx, "c1", "sooner"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("second delete: err = %v, want ErrNotFound", err)
			}

			if err := s.DeleteConversation(ctx, "c2"); err != nil {
				t.Fatalf("delete conversation: %v", err)
			}
			due, err = s.ListDueScheduledPrompts(ctx, base.Add(2*time.Hour))
			if err != nil {
				t.Fatalf("due after delete: %v", err)
			}
			if got, want := scheduledIDs(due), []string{"later"}; !reflect.DeepEqual(got, want) {
				t.Errorf("due after conversation delete = %v, want %v", got, want)
			}
			next, ok, err = s.NextScheduledPromptTime(ctx)
			if err != nil || !ok || !next.Equal(base.Add(time.Hour)) {
				t.Errorf("next after delete = %v %v %v", next, ok, err)
			}
		})
	}
}
