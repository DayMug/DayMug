package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BotThread binds one IM thread to a DayMug conversation and the agent CLI
// session serving it. Platform + ChannelID + ThreadID form the identity.
type BotThread struct {
	Platform       string    `json:"platform"`
	ChannelID      string    `json:"channel_id"`
	ThreadID       string    `json:"thread_id"`
	AgentID        string    `json:"agent_id"`
	ConversationID string    `json:"conversation_id"`
	SessionID      string    `json:"session_id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	LastMessageID  string    `json:"last_message_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// IsBotConversation reports whether a conversation is mirrored from an IM
// thread. A conversation may have many platform messages, but one bot_threads
// binding is enough to make external completion notifications redundant.
func (s *SQLiteStore) IsBotConversation(ctx context.Context, conversationID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM bot_threads WHERE conversation_id = ?)",
		conversationID,
	).Scan(&exists)
	return exists, err
}

// GetBotThread returns the thread binding, or ErrNotFound when this thread has
// never talked to an agent before.
func (s *SQLiteStore) GetBotThread(ctx context.Context, platform, channelID, threadID string) (BotThread, error) {
	var t BotThread
	err := s.db.QueryRowContext(ctx,
		`SELECT platform, channel_id, thread_id, agent_id, conversation_id, session_id, provider, model, last_message_id, created_at, updated_at
		   FROM bot_threads WHERE platform = ? AND channel_id = ? AND thread_id = ?`,
		platform, channelID, threadID).
		Scan(&t.Platform, &t.ChannelID, &t.ThreadID, &t.AgentID, &t.ConversationID, &t.SessionID, &t.Provider, &t.Model, &t.LastMessageID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BotThread{}, ErrNotFound
	}
	if err != nil {
		return BotThread{}, err
	}
	return t, nil
}

// GetBotThreadByConversation performs the reverse lookup used when a prompt
// enters through DayMug Web and needs to be mirrored to its originating IM
// thread. updated_at makes the choice deterministic should more than one
// binding point at the same conversation.
func (s *SQLiteStore) GetBotThreadByConversation(ctx context.Context, conversationID string) (BotThread, error) {
	var t BotThread
	err := s.db.QueryRowContext(ctx,
		`SELECT platform, channel_id, thread_id, agent_id, conversation_id, session_id, provider, model, last_message_id, created_at, updated_at
		   FROM bot_threads WHERE conversation_id = ? ORDER BY updated_at DESC LIMIT 1`,
		conversationID).
		Scan(&t.Platform, &t.ChannelID, &t.ThreadID, &t.AgentID, &t.ConversationID, &t.SessionID, &t.Provider, &t.Model, &t.LastMessageID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BotThread{}, ErrNotFound
	}
	if err != nil {
		return BotThread{}, err
	}
	return t, nil
}

// CronDelivery is where a scheduled run's answer goes: the thread to post into,
// plus the bot to post through. BotID is empty for a job that named no bot, and
// the caller then falls back to inferring the bot from the thread.
type CronDelivery struct {
	Thread BotThread
	BotID  string
}

// CronBotDeliveryTarget resolves the IM thread a scheduled task's answer should
// be relayed into, given the conversation that run created. ErrNotFound means
// "no relay": either the job did not opt in, or its Agent has never talked to a
// bot, and the caller falls back to leaving the run web-only.
//
// The join is on the job's Agent rather than on the conversation, because a cron
// run mints a plain conversation with no bot_threads row of its own — that is
// deliberate, and it is what keeps the run a normal web conversation whose
// reasoning stays readable in the SPA.
//
// A job that names a bot is narrowed to that bot's threads. bot_threads has no
// bot_id column — the id lives in the platform column's third segment
// ("<platform>@<agentID>@<botID>") — so the match goes through the bots row. A
// job that names no bot takes the Agent's most recently used thread.
//
// messageID scopes the relay to the scheduler's own turn. last_conversation_id
// keeps pointing at that conversation until the job fires again, so without this
// a human who later opens the run in the SPA and asks a follow-up would have it
// pushed into someone's IM thread under the scheduled-task label — a message
// nobody sent to that thread, attributed to a task that did not produce it.
func (s *SQLiteStore) CronBotDeliveryTarget(ctx context.Context, conversationID, messageID string) (CronDelivery, error) {
	var d CronDelivery
	t := &d.Thread
	err := s.db.QueryRowContext(ctx,
		`SELECT bt.platform, bt.channel_id, bt.thread_id, bt.agent_id, bt.conversation_id,
		        bt.session_id, bt.provider, bt.model, bt.last_message_id, bt.created_at, bt.updated_at,
		        COALESCE(b.id, '')
		   FROM cron_jobs cj
		   LEFT JOIN bots b ON b.id = cj.bot_id AND b.agent_id = cj.agent_id
		   JOIN bot_threads bt ON bt.agent_id = cj.agent_id
		    AND (cj.bot_id = ''
		         OR bt.platform = b.platform || '@' || b.agent_id || '@' || b.id)
		  WHERE cj.last_conversation_id = ? AND cj.deliver_to_bot = 1
		    AND NOT EXISTS (
		        SELECT 1 FROM messages m
		         WHERE m.conversation_id = cj.last_conversation_id
		           AND m.role = 'user' AND m.id <> ?)
		  ORDER BY bt.updated_at DESC LIMIT 1`,
		conversationID, messageID).
		Scan(&t.Platform, &t.ChannelID, &t.ThreadID, &t.AgentID, &t.ConversationID, &t.SessionID, &t.Provider, &t.Model, &t.LastMessageID, &t.CreatedAt, &t.UpdatedAt, &d.BotID)
	if errors.Is(err, sql.ErrNoRows) {
		return CronDelivery{}, ErrNotFound
	}
	if err != nil {
		return CronDelivery{}, err
	}
	return d, nil
}

// UpsertBotThread creates or refreshes the thread binding. Called after every
// successful run so a backend-assigned session id (codex) sticks for the next
// turn in the thread.
func (s *SQLiteStore) UpsertBotThread(ctx context.Context, t BotThread) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bot_threads (platform, channel_id, thread_id, agent_id, conversation_id, session_id, provider, model, last_message_id, updated_at)
		     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(platform, channel_id, thread_id) DO UPDATE SET
		    agent_id   = excluded.agent_id,
		    conversation_id = excluded.conversation_id,
		    session_id = excluded.session_id,
		    provider   = excluded.provider,
		    model      = excluded.model,
		    last_message_id = excluded.last_message_id,
		    updated_at = excluded.updated_at`,
		t.Platform, t.ChannelID, t.ThreadID, t.AgentID, t.ConversationID, t.SessionID, t.Provider, t.Model, t.LastMessageID)
	return err
}
