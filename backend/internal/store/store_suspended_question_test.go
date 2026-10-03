package store

import (
	"context"
	"testing"
)

func TestConversationSuspendedQuestionRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "t", "agent", "/tmp", "claude", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetConversationSuspendedQuestion(ctx, "c1"); err != nil || got != "" {
		t.Fatalf("fresh = %q, %v; want empty", got, err)
	}
	if err := s.SetConversationSuspendedQuestion(ctx, "c1", `{"request_id":"r1"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetConversationSuspendedQuestion(ctx, "c1"); got != `{"request_id":"r1"}` {
		t.Fatalf("stored = %q", got)
	}
	if err := s.SetConversationSuspendedQuestion(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetConversationSuspendedQuestion(ctx, "c1"); got != "" {
		t.Fatalf("cleared = %q", got)
	}
}
