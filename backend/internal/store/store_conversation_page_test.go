package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestListConversationsPage(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)
	for i := range 4 {
		id := fmt.Sprintf("c%d", i)
		if err := s.CreateConversation(ctx, id, id, "u1", "/tmp", "claude", ""); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if _, err := s.db.Exec("UPDATE conversations SET updated_at = ? WHERE id = ?", base.Add(time.Duration(i)*time.Minute), id); err != nil {
			t.Fatalf("set updated_at for %s: %v", id, err)
		}
	}

	first, err := s.ListConversationsPage(ctx, "u1", ConversationQuery{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !first.HasMore || len(first.Conversations) != 2 ||
		first.Conversations[0].ID != "c3" || first.Conversations[1].ID != "c2" {
		t.Fatalf("first page = %+v", first)
	}

	second, err := s.ListConversationsPage(ctx, "u1", ConversationQuery{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if second.HasMore || len(second.Conversations) != 2 ||
		second.Conversations[0].ID != "c1" || second.Conversations[1].ID != "c0" {
		t.Fatalf("second page = %+v", second)
	}
}

func TestListConversationsPage_NormalizesNegativeOffset(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "one", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	page, err := s.ListConversationsPage(ctx, "u1", ConversationQuery{Limit: 1, Offset: -5})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0].ID != "c1" {
		t.Fatalf("page = %+v", page)
	}
}

func TestListConversations_AppliesCompatibilityHardCap(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.db.Exec(`
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO conversations (id, user_id, title, session_id)
		SELECT printf('bulk-%05d', n), 'u1', 'bulk', printf('session-%05d', n)
		  FROM seq`, ConversationListHardCap+1); err != nil {
		t.Fatalf("seed conversations: %v", err)
	}
	conversations, err := s.ListConversations(context.Background(), "u1")
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != ConversationListHardCap {
		t.Fatalf("conversation count = %d, want hard cap %d", len(conversations), ConversationListHardCap)
	}
}

// The list projection used to omit source_type / cron_job_id, so cron
// conversations came back as manual in the sidebar while GetConversation
// reported them correctly.
func TestListConversationsPage_IncludesUsageSource(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversationRecord(ctx, NewConversation{
		ID: "c1", Title: "c1", UserID: "u1", WorkDir: "/tmp", Provider: "claude", SourceType: "cron", CronJobID: "job-7",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	page, err := s.ListConversationsPage(ctx, "u1", ConversationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Conversations) != 1 {
		t.Fatalf("conversations = %+v", page.Conversations)
	}
	got := page.Conversations[0]
	if got.SourceType != "cron" || got.CronJobID != "job-7" {
		t.Fatalf("source = %q/%q, want cron/job-7", got.SourceType, got.CronJobID)
	}
}

// Every creation-time column lands in the one INSERT, so a seeded
// conversation is never observable half-applied.
func TestCreateConversationRecord_WritesSeedAtomically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	want := NewConversation{
		ID: "c1", Title: "t", UserID: "u1", WorkDir: "/tmp", Provider: "claude", Model: "opus",
		ThinkLevel: "high", AccountName: "acct-a", SessionID: "sess-1",
		MuteNotifications: true, SourceType: "cron", CronJobID: "job-1",
	}
	if err := s.CreateConversationRecord(ctx, want); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ThinkLevel != "high" || got.AccountName != "acct-a" || got.SessionID != "sess-1" ||
		got.NotificationsEnabled || got.SourceType != "cron" || got.CronJobID != "job-1" ||
		got.Provider != "claude" || got.Model != "opus" || got.WorkDir != "/tmp" {
		t.Fatalf("conversation = %+v, want the full seed", got)
	}

	if err := s.CreateConversationRecord(ctx, NewConversation{ID: "c2", UserID: "u1"}); err != nil {
		t.Fatalf("create defaults: %v", err)
	}
	def, err := s.GetConversation(ctx, "c2")
	if err != nil {
		t.Fatalf("get defaults: %v", err)
	}
	if def.SessionID == "" || !def.NotificationsEnabled || def.SourceType != "manual" || def.ThinkLevel != "" {
		t.Fatalf("defaults = %+v, want a minted session, notifications on, manual source", def)
	}
}
