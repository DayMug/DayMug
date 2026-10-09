package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAgentCanOwnMultipleBots(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, bot := range []Bot{
		{ID: "slack", AgentID: "agent", Name: "Slack", Platform: "slack"},
		{ID: "feishu", AgentID: "agent", Name: "Feishu", Platform: "feishu"},
	} {
		if err := s.CreateBot(ctx, bot); err != nil {
			t.Fatal(err)
		}
	}
	bots, err := s.ListBots(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 2 || bots[0].AgentID != "agent" || bots[1].AgentID != "agent" {
		t.Fatalf("bots = %+v", bots)
	}
}

func TestBotReplyConfigurationRoundTrips(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want := Bot{
		ID: "bot", AgentID: "agent", Name: "Bot", Platform: "slack",
		Model: "claude-sonnet", MaxConversationDuration: "3h",
		UnconfiguredReply: "custom unconfigured", UnauthorizedReply: "custom unauthorized",
	}
	if err := s.CreateBot(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBot(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != want.Model || got.MaxConversationDuration != want.MaxConversationDuration ||
		got.UnconfiguredReply != want.UnconfiguredReply ||
		got.UnauthorizedReply != want.UnauthorizedReply {
		t.Fatalf("reply configuration = %+v", got)
	}
}

func TestDeleteAgentDeletesAttachedBots(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "agent", OwnerID: "owner", Name: "Agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, Bot{ID: "bot", AgentID: "agent", Name: "Bot", Platform: "slack"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	bots, err := s.ListBots(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 0 {
		t.Fatalf("attached bots remain after agent delete: %+v", bots)
	}
}
