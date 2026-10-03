package imbridge

import (
	"context"
	"fmt"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// LoadIMBotConfig assembles the IM connections attached to Agents. Bots carry
// no persona of their own; AgentID is the sole source of prompts and runtime
// configuration for every connection returned here. Lives in handler (not
// imbot) so the imbot package stays a pure transport layer with no store
// dependency.
func LoadIMBotConfig(ctx context.Context, agents store.Store, bots store.BotStore) (imbot.Config, error) {
	var cfg imbot.Config
	rows, err := bots.ListBots(ctx, "")
	if err != nil {
		return cfg, fmt.Errorf("list IM bots: %w", err)
	}
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		agentUser, err := agents.GetUser(ctx, row.AgentID)
		if err != nil || agentUser.Username != "" || agentUser.Archived {
			continue
		}
		rules, err := imbot.ValidateChannels(row.Channels)
		if err != nil {
			return cfg, fmt.Errorf("bot %q channels: %w", row.Name, err)
		}
		bot := imbot.BotConfig{ID: row.ID, Name: row.Name, Platform: row.Platform, Enabled: true,
			BotToken: row.BotToken, AppToken: row.BotAppToken, AppID: row.BotAppID, AppSecret: row.BotAppSecret, Channels: rules}
		normalized, err := imbot.ValidateBotConfig(bot)
		if err != nil {
			return cfg, fmt.Errorf("bot %q: %w", row.Name, err)
		}
		cfg.Bots = append(cfg.Bots, imbot.AgentBotConfig{AgentID: row.AgentID, Bot: normalized})
	}
	return cfg, nil
}
