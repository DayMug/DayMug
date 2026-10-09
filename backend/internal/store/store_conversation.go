package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const ConversationListHardCap = 5000

// ConversationPageDefault is one screenful of sidebar. It bounds the preload
// that GET /api/app-state ships on every page load; clients walk past it with
// explicit limit/offset.
const ConversationPageDefault = 50

// ConversationPager names the paging capability on its own so callers that only
// need the window can depend on the narrow type. Store embeds the same method:
// paging sits on the page-load path, and a capability probe there would fall
// back to the unbounded read on any double that missed it — a silent
// regression instead of a compile error.
type ConversationPager interface {
	ListConversationsPage(context.Context, string, ConversationQuery) (ConversationPage, error)
}

type ConversationQuery struct {
	Limit  int
	Offset int
}

type ConversationPage struct {
	Conversations []Conversation
	HasMore       bool
}

var _ ConversationPager = (*SQLiteStore)(nil)

func (s *SQLiteStore) CreateConversation(ctx context.Context, id, title, userID, workDir, provider, model string) error {
	return s.CreateConversationRecord(ctx, NewConversation{
		ID: id, Title: title, UserID: userID, WorkDir: workDir, Provider: provider, Model: model,
	})
}

func (c *NewConversation) normalize() {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	if c.SessionID == "" {
		c.SessionID = uuid.New().String()
	}
	if c.SourceType == "" {
		c.SourceType = "manual"
	}
}

// CreateConversationRecord writes the whole creation-time row in one INSERT,
// so a conversation can never be observed with its seed half applied.
func (s *SQLiteStore) CreateConversationRecord(ctx context.Context, c NewConversation) error {
	c.normalize()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO conversations (
    id, title, user_id, session_id, work_dir, provider, model,
    think_level, account_name, notifications_enabled, pinned, source_type, cron_job_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		c.ID, c.Title, c.UserID, c.SessionID, c.WorkDir, c.Provider, c.Model,
		c.ThinkLevel, c.AccountName, !c.MuteNotifications, c.SourceType, c.CronJobID)
	return err
}

// conversationColumns is the one Conversation projection every read path
// selects, so a column added to the table can't be picked up by one query and
// silently left zero by another. Order must match scanConversation.
const conversationColumns = "id, user_id, title, provider, model, think_level, account_name, work_dir, session_id, notifications_enabled, pinned, pin_order, share_token, last_context_usage, source_type, cron_job_id, created_at, updated_at"

func scanConversation(row interface{ Scan(...any) error }) (Conversation, error) {
	var c Conversation
	if err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.Provider, &c.Model, &c.ThinkLevel, &c.AccountName, &c.WorkDir, &c.SessionID, &c.NotificationsEnabled, &c.Pinned, &c.PinOrder, &c.ShareToken, &c.LastContextUsage, &c.SourceType, &c.CronJobID, &c.CreatedAt, &c.UpdatedAt); err != nil {
		// A single-row lookup that matched nothing is the store-wide
		// ErrNotFound; a raw sql.ErrNoRows would surface as a 500 through
		// respondStoreError instead of the intended 404.
		if errors.Is(err, sql.ErrNoRows) {
			return Conversation{}, ErrNotFound
		}
		return Conversation{}, err
	}
	c.Shared = c.ShareToken != ""
	return c, nil
}

func (s *SQLiteStore) GetConversation(ctx context.Context, id string) (Conversation, error) {
	return scanConversation(s.db.QueryRowContext(ctx,
		"SELECT "+conversationColumns+" FROM conversations WHERE id = ? AND deleted_at IS NULL", id))
}

func (s *SQLiteStore) EnableConversationShare(ctx context.Context, id string) (Conversation, error) {
	conv, err := s.GetConversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	if conv.ShareToken != "" {
		return conv, nil
	}
	for {
		token := strings.ReplaceAll(uuid.New().String(), "-", "")
		res, err := s.db.ExecContext(ctx,
			"UPDATE conversations SET share_token = ? WHERE id = ? AND share_token = '' AND deleted_at IS NULL",
			token, id)
		if err != nil {
			if strings.Contains(err.Error(), "idx_conversations_share_token") {
				continue
			}
			return Conversation{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Conversation{}, err
		}
		if n == 0 {
			return s.GetConversation(ctx, id)
		}
		return s.GetConversation(ctx, id)
	}
}

func (s *SQLiteStore) DisableConversationShare(ctx context.Context, id string) (Conversation, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET share_token = '' WHERE id = ? AND deleted_at IS NULL",
		id)
	if err != nil {
		return Conversation{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Conversation{}, err
	}
	if n == 0 {
		return Conversation{}, ErrNotFound
	}
	return s.GetConversation(ctx, id)
}

func (s *SQLiteStore) GetSharedConversation(ctx context.Context, token string) (Conversation, error) {
	return scanConversation(s.db.QueryRowContext(ctx,
		"SELECT "+conversationColumns+" FROM conversations WHERE share_token = ? AND share_token != '' AND deleted_at IS NULL", token))
}

// UpdateConversationModel sets both the provider and model on a conversation
// in a single statement. Validation (provider/model combo, whether provider
// change is still allowed for this conversation) is the caller's job — this
// is a low-level setter. ErrNotFound when no row matches.
func (s *SQLiteStore) UpdateConversationModel(ctx context.Context, id, provider, model string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET provider = ?, model = ? WHERE id = ? AND deleted_at IS NULL",
		provider, model, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateConversationAccount pins (or clears, when account is "") the provider
// account a conversation runs against. Validation that the account belongs to
// the user's allowed set for the conversation's type is the caller's job — this
// is a low-level setter. ErrNotFound when no row matches.
func (s *SQLiteStore) UpdateConversationAccount(ctx context.Context, id, account string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET account_name = ? WHERE id = ? AND deleted_at IS NULL",
		account, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateConversationThinkLevel sets the per-conversation reasoning effort.
// Empty clears the override so adapters leave the provider default untouched.
func (s *SQLiteStore) UpdateConversationThinkLevel(ctx context.Context, id, level string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET think_level = ? WHERE id = ? AND deleted_at IS NULL",
		level, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) UpdateConversationWorkDir(ctx context.Context, id, workDir string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE conversations SET work_dir = ? WHERE id = ?", workDir, id)
	return err
}

func (s *SQLiteStore) UpdateConversationNotifications(ctx context.Context, id string, enabled bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE conversations SET notifications_enabled = ? WHERE id = ?", enabled, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetConversationAttention leaves updated_at alone: a flag change is not
// activity and must not reorder the sidebar.
func (s *SQLiteStore) SetConversationAttention(ctx context.Context, id, state string) error {
	var err error
	if state == "" {
		_, err = s.db.ExecContext(ctx,
			"UPDATE conversations SET attention = '', attention_at = NULL WHERE id = ? AND attention != ''", id)
	} else {
		_, err = s.db.ExecContext(ctx,
			"UPDATE conversations SET attention = ?, attention_at = datetime('now') WHERE id = ?", state, id)
	}
	return err
}

// SetConversationSuspendedQuestion leaves updated_at alone for the same
// reason as SetConversationAttention.
func (s *SQLiteStore) SetConversationSuspendedQuestion(ctx context.Context, id, payload string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET suspended_question = ? WHERE id = ? AND suspended_question != ?", payload, id, payload)
	return err
}

func (s *SQLiteStore) GetConversationSuspendedQuestion(ctx context.Context, id string) (string, error) {
	var payload string
	err := s.db.QueryRowContext(ctx,
		"SELECT suspended_question FROM conversations WHERE id = ? AND deleted_at IS NULL", id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return payload, err
}

func (s *SQLiteStore) SetConversationRotatedFrom(ctx context.Context, id, parentID string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE conversations SET rotated_from = ? WHERE id = ?", parentID, id)
	return err
}

func (s *SQLiteStore) GetConversationRotatedFrom(ctx context.Context, id string) (string, error) {
	var parentID string
	err := s.db.QueryRowContext(ctx, "SELECT rotated_from FROM conversations WHERE id = ?", id).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return parentID, err
}

// maxAttentionRows caps the list so an owner who never opens cron output
// cannot grow the response without bound; the badge just saturates.
const maxAttentionRows = 200

func (s *SQLiteStore) ListConversationAttention(ctx context.Context, ownerID string) ([]ConversationAttention, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.user_id, c.title, c.attention, c.attention_at, c.updated_at
  FROM conversations c
  JOIN agents a ON a.id = c.user_id
 WHERE c.attention != '' AND c.deleted_at IS NULL
   AND a.owner_id = ? AND a.deleted_at IS NULL AND a.archived_at IS NULL
 ORDER BY COALESCE(c.attention_at, c.updated_at) DESC, c.id
 LIMIT ?`, ownerID, maxAttentionRows)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []ConversationAttention{}
	for rows.Next() {
		var row ConversationAttention
		var at sql.NullTime
		if err := rows.Scan(&row.ConversationID, &row.AgentID, &row.Title, &row.State, &at, &row.At); err != nil {
			return nil, err
		}
		if at.Valid {
			row.At = at.Time
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// UpdateConversationPinned flips the pinned flag without touching updated_at —
// pinning shouldn't promote a stale conversation past one that genuinely just
// got a reply. The sidebar sorts pinned rows above unpinned ones using this
// flag as the primary key.
func (s *SQLiteStore) UpdateConversationPinned(ctx context.Context, id string, pinned bool) error {
	var res sql.Result
	var err error
	if pinned {
		// New pins land at the top of the pinned block: one below the current
		// minimum pin_order among the owner's pinned rows. The subquery sees the
		// pre-update state, so this row's stale order doesn't skew the min.
		res, err = s.db.ExecContext(ctx,
			`UPDATE conversations SET pinned = 1,
			   pin_order = COALESCE((SELECT MIN(pin_order) FROM conversations
			                          WHERE user_id = (SELECT user_id FROM conversations WHERE id = ?)
			                            AND pinned = 1 AND deleted_at IS NULL), 0) - 1
			 WHERE id = ?`, id, id)
	} else {
		// Reset pin_order too: a stale negative order left behind here would
		// sort an unpinned row above newer ones (the list orders the unpinned
		// block by updated_at, and pin_order is the tie-break that precedes it).
		res, err = s.db.ExecContext(ctx, "UPDATE conversations SET pinned = 0, pin_order = 0 WHERE id = ?", id)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReorderPinnedConversations makes `ids` the authoritative pinned set for the
// user, in order: each id is pinned with pin_order = its index, and any other
// row the user had pinned is unpinned. Runs in one transaction so a concurrent
// list never sees a half-applied order. Ids not owned by userID match no row
// and are silently skipped.
func (s *SQLiteStore) ReorderPinnedConversations(ctx context.Context, userID string, ids []string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		// Clear the user's existing pinned set, then re-apply the supplied order.
		// Items dropped from the list fall back to the unpinned block.
		if _, err := tx.ExecContext(ctx,
			"UPDATE conversations SET pinned = 0, pin_order = 0 WHERE user_id = ? AND pinned = 1 AND deleted_at IS NULL", userID); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx,
				"UPDATE conversations SET pinned = 1, pin_order = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL",
				i, id, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetSessionID overwrites a conversation's session_id with the
// supplied value. Used by the streaming loop when a runner that assigns its
// own session id (codex) emits the chosen id in its system_init event — the
// next turn needs that id to resume the right rollout. ErrNotFound when the
// conversation row is missing. Empty newID is rejected to keep the resume
// flag from accidentally picking a blank id.
func (s *SQLiteStore) SetSessionID(ctx context.Context, id, newSessionID string) error {
	if newSessionID == "" {
		return fmt.Errorf("session id is required")
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET session_id = ? WHERE id = ?",
		newSessionID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetConversationSession rotates the conversation's session_id
// to a fresh UUID and clears the cached last_context_usage. The next input
// run will start a new claude CLI session (no jsonl exists for the new
// UUID, so the runner picks --session-id over --resume) without any prior
// context, while the conversation row, message history, and UI thread
// stay intact. Returns the newly minted session ID for callers that want
// to log it; ErrNotFound when the conversation row is missing.
func (s *SQLiteStore) ResetConversationSession(ctx context.Context, id string) (string, error) {
	newID := uuid.New().String()
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET session_id = ?, last_context_usage = '' WHERE id = ?",
		newID, id)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", ErrNotFound
	}
	return newID, nil
}

// UpdateConversationContextUsage stores the most recent context_usage JSON
// payload (typically `{"used":...,"total":...,"input_tokens":...,...}`) so
// it can be replayed to a freshly-connecting client. Best-effort: missing
// rows return ErrNotFound but callers usually log+ignore since this is a
// soft cache, not authoritative state.
func (s *SQLiteStore) UpdateConversationContextUsage(ctx context.Context, id, payload string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE conversations SET last_context_usage = ? WHERE id = ?", payload, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) HasModelReply(ctx context.Context, conversationID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM messages m
			  JOIN conversations c ON c.id = m.conversation_id
			 WHERE m.conversation_id = ? AND c.deleted_at IS NULL
			   AND (m.role = 'tool'
			     OR (m.role = 'user' AND m.queue_status = 'processing')
			     OR (m.role = 'assistant' AND NOT (
			          json_valid(m.metadata)
			      AND json_type(m.metadata, '$.usage') = 'object'
			      AND COALESCE(json_extract(m.metadata, '$.usage.input_tokens'), 0) = 0
			      AND COALESCE(json_extract(m.metadata, '$.usage.output_tokens'), 0) = 0)))
		)`, conversationID).
		Scan(&exists)
	return exists, err
}

func (s *SQLiteStore) CountUserMessages(ctx context.Context, conversationID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages m
		   JOIN conversations c ON c.id = m.conversation_id
		  WHERE m.conversation_id = ? AND m.role = 'user' AND c.deleted_at IS NULL`,
		conversationID).
		Scan(&n)
	return n, err
}

func (s *SQLiteStore) UpdateConversationTitle(ctx context.Context, id, title string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE conversations SET title = ? WHERE id = ?", title, id)
	return err
}

func (s *SQLiteStore) ListConversations(ctx context.Context, userID string) ([]Conversation, error) {
	page, err := s.ListConversationsPage(ctx, userID, ConversationQuery{Limit: ConversationListHardCap})
	return page.Conversations, err
}

func (s *SQLiteStore) ListConversationsPage(ctx context.Context, userID string, query ConversationQuery) (ConversationPage, error) {
	limit := query.Limit
	if limit <= 0 || limit > ConversationListHardCap {
		limit = ConversationListHardCap
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+conversationColumns+" FROM conversations WHERE user_id = ? AND deleted_at IS NULL ORDER BY pinned DESC, CASE WHEN pinned = 1 THEN pin_order ELSE 0 END ASC, updated_at DESC, created_at DESC, id DESC LIMIT ? OFFSET ?",
		userID, limit+1, offset)
	if err != nil {
		return ConversationPage{}, err
	}
	defer func() { _ = rows.Close() }()

	convs := make([]Conversation, 0, limit)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return ConversationPage{}, err
		}
		convs = append(convs, c)
	}
	if err := rows.Err(); err != nil {
		return ConversationPage{}, err
	}
	hasMore := len(convs) > limit
	if hasMore {
		convs = convs[:limit]
	}
	return ConversationPage{Conversations: convs, HasMore: hasMore}, nil
}

// DeleteConversation soft-deletes a conversation. Messages stay on disk but
// are filtered out by every read path (which joins with `conversations.deleted_at`),
// so the user-facing behavior matches the previous hard-delete + cascade
// semantics. Idempotent on already-deleted rows: returns ErrNotFound rather
// than re-stamping the deletion timestamp.
func (s *SQLiteStore) DeleteConversation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE conversations SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
		time.Now(), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteConversationsUpdatedBefore soft-deletes every active conversation for
// an agent whose last update predates before and returns the affected ids.
func (s *SQLiteStore) DeleteConversationsUpdatedBefore(ctx context.Context, userID string, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE conversations
		SET deleted_at = ?
		WHERE user_id = ? AND updated_at < ? AND deleted_at IS NULL
		RETURNING id`, time.Now(), userID, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLiteStore) GetSessionID(ctx context.Context, conversationID string) (string, error) {
	var sessionID string
	err := s.db.QueryRowContext(ctx,
		"SELECT session_id FROM conversations WHERE id = ? AND deleted_at IS NULL", conversationID).
		Scan(&sessionID)
	if err != nil {
		return "", err
	}
	return sessionID, nil
}
