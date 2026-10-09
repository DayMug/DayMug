package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ThreadCase is the durable state of one IM thread's work, independent of any
// CLI session or DayMug conversation.
//
// It is keyed by the IM thread rather than by conversation or agent on purpose:
// every agent working the same thread reads and writes one case, so a handoff
// costs the receiver a document read instead of a replay of the sender's whole
// transcript. Rotating a session throws away the transcript; the case is what
// survives, so it — not the conversation — is the unit of memory here.
type ThreadCase struct {
	ChannelID string    `json:"channel_id"`
	ThreadID  string    `json:"thread_id"`
	Doc       string    `json:"doc"`
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetThreadCase returns the current case for an IM thread. A thread that has
// never been written returns a zero-version case and no error: "no case yet" is
// the normal first turn, not a failure.
func (s *SQLiteStore) GetThreadCase(ctx context.Context, channelID, threadID string) (ThreadCase, error) {
	var c ThreadCase
	err := s.db.QueryRowContext(ctx,
		`SELECT channel_id, thread_id, doc, version, updated_by, updated_at
		 FROM thread_cases WHERE channel_id = ? AND thread_id = ?`,
		channelID, threadID).
		Scan(&c.ChannelID, &c.ThreadID, &c.Doc, &c.Version, &c.UpdatedBy, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadCase{ChannelID: channelID, ThreadID: threadID}, nil
	}
	if err != nil {
		return ThreadCase{}, err
	}
	return c, nil
}

// SaveThreadCase writes a new revision and returns it.
//
// Every revision is also appended to thread_case_history. That history is the
// only protection against the failure mode this design adds: a model can write
// something wrong into the case, and unlike a wrong sentence in a transcript it
// will be re-read as established fact on every later turn. Being able to see
// and restore the previous revision is what makes that recoverable.
//
// Retried: the transaction reads the current version and then writes with it,
// which is the deferred read-then-write shape busy_timeout cannot cover (see
// retryOnBusySnapshot). Losing that race is not cosmetic here — the case is the
// only thing a rotated session leaves behind, and a dropped save also stalls
// the rotation gate, which reads the stored revision to decide whether the
// session may be retired.
func (s *SQLiteStore) SaveThreadCase(ctx context.Context, channelID, threadID, doc, updatedBy string) (ThreadCase, error) {
	var saved ThreadCase
	err := s.withTxRetry(ctx, func(tx *sql.Tx) error {
		var err error
		saved, err = saveThreadCaseTx(ctx, tx, channelID, threadID, doc, updatedBy)
		return err
	})
	if err != nil {
		return ThreadCase{}, err
	}
	return saved, nil
}

func saveThreadCaseTx(ctx context.Context, tx *sql.Tx, channelID, threadID, doc, updatedBy string) (ThreadCase, error) {
	// The version is computed inside the INSERT rather than by a preceding
	// SELECT, so the transaction's first statement is a write. That is what
	// makes busy_timeout usable here: a transaction that reads first holds a
	// snapshot, and SQLite refuses to upgrade a stale one instantly instead of
	// invoking the busy handler — measured as an immediate "database is locked"
	// under eight concurrent writers.
	now := time.Now().UTC()
	var version int
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO thread_cases (channel_id, thread_id, doc, version, updated_by, updated_at)
		 VALUES (?, ?, ?, 1, ?, ?)
		 ON CONFLICT(channel_id, thread_id) DO UPDATE SET
		     doc = excluded.doc,
		     version = thread_cases.version + 1,
		     updated_by = excluded.updated_by,
		     updated_at = excluded.updated_at
		 RETURNING version`,
		channelID, threadID, doc, updatedBy, now).Scan(&version); err != nil {
		return ThreadCase{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO thread_case_history (channel_id, thread_id, version, doc, updated_by, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		channelID, threadID, version, doc, updatedBy, now); err != nil {
		return ThreadCase{}, err
	}
	return ThreadCase{
		ChannelID: channelID, ThreadID: threadID, Doc: doc,
		Version: version, UpdatedBy: updatedBy, UpdatedAt: now,
	}, nil
}

// ListThreadCaseHistory returns revisions newest-first, capped at limit.
func (s *SQLiteStore) ListThreadCaseHistory(ctx context.Context, channelID, threadID string, limit int) ([]ThreadCase, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_id, thread_id, doc, version, updated_by, updated_at
		 FROM thread_case_history WHERE channel_id = ? AND thread_id = ?
		 ORDER BY version DESC LIMIT ?`,
		channelID, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ThreadCase
	for rows.Next() {
		var c ThreadCase
		if err := rows.Scan(&c.ChannelID, &c.ThreadID, &c.Doc, &c.Version, &c.UpdatedBy, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestAssistantMessage returns the newest user-visible agent reply in a
// conversation, or ErrNotFound when it has none.
//
// This exists for one caller: case-mode rotation, which retires the session
// that produced that reply. A human's follow-up is usually a reply *to* it
// ("not that one, check the others"), and the platform cannot recover it any
// other way — Slack's thread backfill filters the bot's own messages out, so
// the retired conversation row is the only copy left.
func (s *SQLiteStore) LatestAssistantMessage(ctx context.Context, conversationID string) (Message, error) {
	if conversationID == "" {
		return Message{}, ErrNotFound
	}
	var m Message
	// rowid, not created_at: created_at has 1s resolution and would coin-flip
	// between two rows written in the same second (see ListMessages).
	err := s.db.QueryRowContext(ctx,
		`SELECT id, conversation_id, role, content, created_at
		   FROM messages
		  WHERE conversation_id = ? AND role = 'assistant' AND content != ''
		  ORDER BY rowid DESC LIMIT 1`,
		conversationID).
		Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return m, nil
}
