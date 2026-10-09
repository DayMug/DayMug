package imbridge

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func TestLoadIMBotConfigLoadsMultipleBotsAttachedToOneAgent(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "owner", Username: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent", OwnerID: "owner", Name: "Web Agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, store.Bot{ID: "slack-bot", AgentID: "agent", Name: "Ops Slack", Platform: imbot.PlatformSlack,
		Enabled: true, BotToken: "xoxb", BotAppToken: "xapp", Channels: `[{"channel":"*","auto_reply":true}]`}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, store.Bot{ID: "feishu-bot", AgentID: "agent", Name: "Ops Feishu", Platform: imbot.PlatformFeishu,
		Enabled: true, BotAppID: "cli_x", BotAppSecret: "secret", Channels: `[{"channel":"dm"}]`}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadIMBotConfig(ctx, s, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bots) != 2 || cfg.Bots[0].AgentID != "agent" || cfg.Bots[1].AgentID != "agent" {
		t.Fatalf("loaded bots = %+v", cfg.Bots)
	}
}
