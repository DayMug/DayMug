package service

import (
	"context"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
)

type notificationCapture struct {
	calls int
}

func (c *notificationCapture) Send(context.Context, string, string, string, string) error {
	c.calls++
	return nil
}

func TestMaybeNotifySkipsBotConversation(t *testing.T) {
	s := newMessagePersistTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{
		ID: "owner", Name: "Owner", Username: "owner", BarkURL: "https://bark.example/key",
	}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{
		ID: "agent", Name: "Agent", OwnerID: "owner", WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateConversation(ctx, "conv1", "Bot thread", "agent", t.TempDir(), "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.UpsertBotThread(ctx, store.BotThread{
		Platform: "slack@agent@bot1", ChannelID: "C1", ThreadID: "t1",
		AgentID: "agent", ConversationID: "conv1",
	}); err != nil {
		t.Fatalf("insert bot thread: %v", err)
	}

	sender := &notificationCapture{}
	p := &MessagePersister{Store: s, BarkSender: sender}
	p.MaybeNotify("conv1", "finished")

	if sender.calls != 0 {
		t.Fatalf("bot conversation sent %d notifications, want 0", sender.calls)
	}
}
