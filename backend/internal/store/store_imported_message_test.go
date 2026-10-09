package store

import (
	"context"
	"testing"
	"time"
)

func TestSaveImportedMessage_DedupAndWatermark(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	base := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	row := Message{ConversationID: "c1", Role: "user", Content: "hello", SourceID: "u1"}

	inserted, err := s.SaveImportedMessage(ctx, row, base)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if !inserted {
		t.Fatal("first import should insert")
	}

	// Re-importing the same source_id is a no-op (idempotent re-run).
	row.ID = "" // a fresh attempt would carry a new primary key
	inserted, err = s.SaveImportedMessage(ctx, row, base)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if inserted {
		t.Fatal("re-import of same source_id should not insert")
	}

	msgs, err := s.ListMessages(ctx, "c1", 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 row after dedup, got %d", len(msgs))
	}

	// The watermark reflects the imported row's transcript timestamp, not now.
	got, ok, err := s.LatestMessageTime(ctx, "c1")
	if err != nil || !ok {
		t.Fatalf("latest time: ok=%v err=%v", ok, err)
	}
	if !got.Equal(base) {
		t.Errorf("watermark = %v, want %v", got, base)
	}
}

func TestSaveImportedMessage_RequiresSourceID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := s.SaveImportedMessage(ctx, Message{ConversationID: "c1", Role: "user", Content: "x"}, time.Now()); err == nil {
		t.Fatal("expected error for empty SourceID")
	}
}

func TestSaveMessage_SourceIDDeduplicatesLaterBackfill(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	sourceID := "im:slack:C1|171.100"
	if err := s.SaveMessage(ctx, Message{
		ID:             "live",
		ConversationID: "c1",
		Role:           "user",
		Content:        "root",
		SourceID:       sourceID,
	}); err != nil {
		t.Fatalf("save live IM message: %v", err)
	}

	inserted, err := s.SaveImportedMessage(ctx, Message{
		ID:             "backfill",
		ConversationID: "c1",
		Role:           "user",
		Content:        "root",
		SourceID:       sourceID,
	}, time.Now())
	if err != nil {
		t.Fatalf("save repeated IM backfill: %v", err)
	}
	if inserted {
		t.Fatal("repeated platform message should not insert")
	}

	msgs, err := s.ListMessages(ctx, "c1", 0, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].ID != "live" {
		t.Fatalf("messages = %+v, want only the live row", msgs)
	}
}

func TestLatestMessageTime_EmptyConversation(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, ok, err := s.LatestMessageTime(ctx, "c1"); err != nil || ok {
		t.Fatalf("empty conversation should report ok=false, got ok=%v err=%v", ok, err)
	}
}
