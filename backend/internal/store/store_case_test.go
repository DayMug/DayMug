package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// Case-mode rotation retires the conversation that produced the agent's last
// reply. Slack's thread backfill filters the bot's own posts, so this row is
// the only place that text still exists — it has to come back exactly, and it
// has to be the newest reply rather than whatever the ordering happens to hand
// back.
func TestLatestAssistantMessageReturnsTheNewestReply(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Username: "u"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "c1", "Chat", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for _, m := range []Message{
		{ID: "m1", ConversationID: "c1", Role: "assistant", Content: "第一条结论"},
		{ID: "m2", ConversationID: "c1", Role: "user", Content: "追问"},
		{ID: "m3", ConversationID: "c1", Role: "assistant", Content: "候选：a@x.com、b@y.com"},
		{ID: "m4", ConversationID: "c1", Role: "tool", Content: "{}"},
	} {
		if err := s.SaveMessage(ctx, m); err != nil {
			t.Fatalf("save %s: %v", m.ID, err)
		}
	}

	got, err := s.LatestAssistantMessage(ctx, "c1")
	if err != nil {
		t.Fatalf("LatestAssistantMessage: %v", err)
	}
	if got.ID != "m3" {
		t.Fatalf("LatestAssistantMessage returned %s (%q), want m3", got.ID, got.Content)
	}
}

// A conversation the agent never replied in is the ordinary "nothing to hand
// over" case, not a failure: the caller degrades to a case-only session.
func TestLatestAssistantMessageReportsNotFoundWithoutReplies(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Username: "u"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "c1", "Chat", "u1", "/tmp", "", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "问题"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := s.LatestAssistantMessage(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.LatestAssistantMessage(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty conversation id: err = %v, want ErrNotFound", err)
	}
}

// Concurrent writers must all land. SaveThreadCase reads the current version
// and then writes with it, so two turns saving at once — the normal shape when
// two agents share one thread, or when any other write commits mid-save — let
// the second one's snapshot go stale. Production logged 108 of these as
// "database is locked", each one a turn's case silently lost.
func TestSaveThreadCaseSurvivesConcurrentWriters(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "case.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}

	const writers = 8
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, writers)
	versions := make([]int, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			saved, err := s.SaveThreadCase(ctx, "C1", "T1", fmt.Sprintf("doc %d", i), "agent")
			errs[i], versions[i] = err, saved.Version
		}()
	}
	close(start)
	wg.Wait()

	seen := map[int]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
		if seen[versions[i]] {
			t.Fatalf("version %d handed out twice", versions[i])
		}
		seen[versions[i]] = true
	}
	for v := 1; v <= writers; v++ {
		if !seen[v] {
			t.Fatalf("version %d missing; revisions must be contiguous", v)
		}
	}
	history, err := s.ListThreadCaseHistory(ctx, "C1", "T1", writers+1)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != writers {
		t.Fatalf("history has %d revisions, want %d", len(history), writers)
	}
}
