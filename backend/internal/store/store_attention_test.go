package store

import (
	"context"
	"testing"
)

func TestConversationAttentionIsListedForTheAgentOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, agent := range []struct{ id, owner string }{{"mine", "alice"}, {"theirs", "bob"}, {"gone", "alice"}} {
		if _, err := s.db.Exec("INSERT INTO agents (id, owner_id, name) VALUES (?, ?, ?)", agent.id, agent.owner, agent.id); err != nil {
			t.Fatalf("insert agent %s: %v", agent.id, err)
		}
	}
	if _, err := s.db.Exec("UPDATE agents SET archived_at = datetime('now') WHERE id = 'gone'"); err != nil {
		t.Fatal(err)
	}
	for _, conv := range []struct{ id, agent string }{{"done", "mine"}, {"ask", "mine"}, {"quiet", "mine"}, {"other", "theirs"}, {"archived", "gone"}, {"deleted", "mine"}} {
		if err := s.CreateConversation(ctx, conv.id, "title "+conv.id, conv.agent, "/tmp", "claude", ""); err != nil {
			t.Fatalf("create %s: %v", conv.id, err)
		}
	}
	for id, state := range map[string]string{"done": AttentionDone, "ask": AttentionWaiting, "other": AttentionError, "archived": AttentionDone, "deleted": AttentionDone} {
		if err := s.SetConversationAttention(ctx, id, state); err != nil {
			t.Fatalf("set %s: %v", id, err)
		}
	}
	if err := s.DeleteConversation(ctx, "deleted"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListConversationAttention(ctx, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]string{}
	for _, row := range rows {
		if row.AgentID != "mine" || row.Title != "title "+row.ConversationID || row.At.IsZero() {
			t.Errorf("row = %+v", row)
		}
		got[row.ConversationID] = row.State
	}
	if len(got) != 2 || got["done"] != AttentionDone || got["ask"] != AttentionWaiting {
		t.Fatalf("attention = %v, want done+ask", got)
	}

	if err := s.SetConversationAttention(ctx, "done", ""); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListConversationAttention(ctx, "alice")
	if len(rows) != 1 || rows[0].ConversationID != "ask" {
		t.Fatalf("after clearing = %+v, want only ask", rows)
	}
}

// A flag change is not activity: it must not bump a stale conversation to the
// top of the sidebar.
func TestSetConversationAttentionKeepsUpdatedAt(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c", "c", "a", "/tmp", "claude", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE conversations SET updated_at = '2026-01-01 00:00:00' WHERE id = 'c'"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConversationAttention(ctx, "c", AttentionDone); err != nil {
		t.Fatal(err)
	}
	conv, err := s.GetConversation(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if conv.UpdatedAt.Year() != 2026 || conv.UpdatedAt.Month() != 1 {
		t.Fatalf("updated_at = %v, want untouched", conv.UpdatedAt)
	}
}
