package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

func scanBot(row interface{ Scan(...any) error }, bot *Bot) error {
	return row.Scan(&bot.ID, &bot.AgentID, &bot.Name, &bot.Platform, &bot.Enabled,
		&bot.Model, &bot.MaxConversationDuration, &bot.BotToken, &bot.BotAppToken, &bot.BotAppID, &bot.BotAppSecret, &bot.Channels,
		&bot.UnconfiguredReply, &bot.UnauthorizedReply, &bot.CreatedAt)
}

func (s *SQLiteStore) CreateBot(ctx context.Context, bot Bot) error {
	if bot.ID == "" {
		bot.ID = uuid.New().String()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bots (id, agent_id, name, platform, enabled, model, max_conversation_duration, bot_token, bot_app_token, bot_app_id, bot_app_secret, channels, unconfigured_reply, unauthorized_reply)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		bot.ID, bot.AgentID, bot.Name, bot.Platform, bot.Enabled, bot.Model, bot.MaxConversationDuration, bot.BotToken, bot.BotAppToken,
		bot.BotAppID, bot.BotAppSecret, bot.Channels, bot.UnconfiguredReply, bot.UnauthorizedReply)
	return err
}

func (s *SQLiteStore) GetBot(ctx context.Context, id string) (Bot, error) {
	var bot Bot
	err := scanBot(s.db.QueryRowContext(ctx, `
		SELECT id, agent_id, name, platform, enabled, model, max_conversation_duration, bot_token, bot_app_token, bot_app_id, bot_app_secret, channels, unconfigured_reply, unauthorized_reply, created_at
		FROM bots WHERE id = ?`, id), &bot)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, ErrNotFound
	}
	return bot, err
}

func (s *SQLiteStore) ListBots(ctx context.Context, agentID string) ([]Bot, error) {
	query := `SELECT id, agent_id, name, platform, enabled, model, max_conversation_duration, bot_token, bot_app_token, bot_app_id, bot_app_secret, channels, unconfigured_reply, unauthorized_reply, created_at
		FROM bots`
	args := []any{}
	if agentID != "" {
		query += " WHERE agent_id = ?"
		args = append(args, agentID)
	}
	query += " ORDER BY created_at ASC, id ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	bots := []Bot{}
	for rows.Next() {
		var bot Bot
		if err := scanBot(rows, &bot); err != nil {
			return nil, err
		}
		bots = append(bots, bot)
	}
	return bots, rows.Err()
}

func (s *SQLiteStore) UpdateBot(ctx context.Context, bot Bot) error {
	return s.execSingleRowUpdate(ctx, `
		UPDATE bots SET name = ?, platform = ?, enabled = ?, model = ?, max_conversation_duration = ?, bot_token = ?, bot_app_token = ?, bot_app_id = ?, bot_app_secret = ?, channels = ?, unconfigured_reply = ?, unauthorized_reply = ?
		WHERE id = ? AND agent_id = ?`,
		bot.Name, bot.Platform, bot.Enabled, bot.Model, bot.MaxConversationDuration, bot.BotToken, bot.BotAppToken, bot.BotAppID,
		bot.BotAppSecret, bot.Channels, bot.UnconfiguredReply, bot.UnauthorizedReply, bot.ID, bot.AgentID)
}

func (s *SQLiteStore) DeleteBot(ctx context.Context, id, agentID string) error {
	return s.execSingleRowUpdate(ctx, "DELETE FROM bots WHERE id = ? AND agent_id = ?", id, agentID)
}
