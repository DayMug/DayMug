package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const toolContentSummaryLimitBytes = 300
const toolContentJSONValueLimitBytes = 50

var uuidValuePattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func truncateUTF8Bytes(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.ValidString(s[:limit]) {
		limit--
	}
	return s[:limit]
}

func decodeJSONValue(content string) (any, bool) {
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return nil, false
	}
	return value, true
}

func marshalJSONValue(value any) (string, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func truncateToolJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = truncateToolJSONValue(child)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = truncateToolJSONValue(child)
		}
		return typed
	case string:
		if uuidValuePattern.MatchString(typed) {
			return typed
		}
		return truncateUTF8Bytes(typed, toolContentJSONValueLimitBytes)
	default:
		return value
	}
}

func persistentToolContent(content string) (contentText, contentJSON string) {
	if len(content) <= toolContentSummaryLimitBytes {
		return content, ""
	}
	if value, ok := decodeJSONValue(content); ok {
		if encoded, ok := marshalJSONValue(truncateToolJSONValue(value)); ok {
			return "", encoded
		}
	}
	if encoded, ok := marshalJSONValue(truncateUTF8Bytes(content, toolContentJSONValueLimitBytes)); ok {
		return "", encoded
	}
	return truncateUTF8Bytes(content, toolContentJSONValueLimitBytes), ""
}

func CompactToolContent(content string) string {
	contentText, contentJSON := persistentToolContent(content)
	if contentJSON != "" {
		return contentJSON
	}
	return contentText
}

func ToolContentForWire(content string) any {
	if json.Valid([]byte(content)) {
		return json.RawMessage(content)
	}
	return content
}

// messageColumns is the Message projection shared by every read path;
// messageColumnsM is the same list qualified for queries that join
// conversations (both tables have id / created_at).
const (
	messageColumns  = "id, conversation_id, role, content, content_json, metadata, created_at, queue_status"
	messageColumnsM = "m.id, m.conversation_id, m.role, m.content, m.content_json, m.metadata, m.created_at, m.queue_status"
)

// scanMessage maps the canonical messageColumns order (messageColumnsM when
// the query joins conversations) into a Message. content_json is the
// persisted compact JSON form for oversized tool rows; the public Message
// shape remains content-only, so readers prefer content_json when present.
// The metadata column is TEXT NOT NULL DEFAULT ” on disk, so an empty row
// stays as nil json.RawMessage and serialises away under `omitempty` for
// the API.
func scanMessage(row interface {
	Scan(dest ...any) error
}) (Message, error) {
	var (
		m            Message
		contentJSON  string
		metadataText string
	)
	if err := row.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &contentJSON, &metadataText, &m.CreatedAt, &m.QueueStatus); err != nil {
		return Message{}, err
	}
	if contentJSON != "" {
		m.Content = contentJSON
	}
	if metadataText != "" {
		m.Metadata = json.RawMessage(metadataText)
	}
	return m, nil
}

func (s *SQLiteStore) SaveMessage(ctx context.Context, msg Message) error {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	contentJSON := ""
	if msg.Role == "tool" {
		msg.Content, contentJSON = persistentToolContent(msg.Content)
	}

	// Empty json.RawMessage stays as the DB DEFAULT '' so legacy callers
	// that don't populate Metadata produce identical rows to before the
	// column existed.
	metadata := ""
	if len(msg.Metadata) > 0 {
		metadata = string(msg.Metadata)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO messages (id, conversation_id, role, content, content_json, metadata, queue_status, source_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			msg.ID, msg.ConversationID, msg.Role, msg.Content, contentJSON, metadata, msg.QueueStatus, msg.SourceID,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE conversations SET updated_at = datetime('now') WHERE id = ?",
			msg.ConversationID,
		)
		return err
	})
}

// sqliteTimeLayouts are the textual created_at formats the importer may
// encounter: the live path's datetime('now') second precision, and the
// millisecond form SaveImportedMessage writes. Tried in order.
var sqliteTimeLayouts = []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05"}

// importedTimeLayout is how SaveImportedMessage serialises a transcript
// timestamp into the created_at column. UTC + milliseconds so rows within a
// single turn keep their relative order and string-compare consistently
// against the live path's second-precision datetime('now') values.
const importedTimeLayout = "2006-01-02 15:04:05.000"

// SaveImportedMessage persists a transcript-backfilled message. See the Store
// interface doc for the dedup contract; the INSERT OR IGNORE leans on the
// idx_messages_source unique index so a repeated import of the same JSONL line
// inserts nothing and reports inserted=false.
func (s *SQLiteStore) SaveImportedMessage(ctx context.Context, msg Message, createdAt time.Time) (bool, error) {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.SourceID == "" {
		return false, errors.New("SaveImportedMessage: empty SourceID")
	}
	metadata := ""
	if len(msg.Metadata) > 0 {
		metadata = string(msg.Metadata)
	}
	contentJSON := ""
	if msg.Role == "tool" {
		msg.Content, contentJSON = persistentToolContent(msg.Content)
	}
	var inserted bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO messages (id, conversation_id, role, content, content_json, metadata, source_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			msg.ID, msg.ConversationID, msg.Role, msg.Content, contentJSON, metadata, msg.SourceID,
			createdAt.UTC().Format(importedTimeLayout),
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		inserted = n > 0
		if inserted {
			// Mirror SaveMessage: bump updated_at so the sidebar re-sorts the
			// conversation to the top once its tty history materialises.
			_, _ = tx.ExecContext(ctx,
				"UPDATE conversations SET updated_at = datetime('now') WHERE id = ?",
				msg.ConversationID,
			)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// LatestMessageTime returns the created_at of the newest message in the
// conversation (ok=false when there are none). Parses the stored text against
// the known layouts; an unparseable value yields ok=false so the importer
// falls back to "import everything" rather than silently dropping new lines.
func (s *SQLiteStore) LatestMessageTime(ctx context.Context, conversationID string) (time.Time, bool, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT MAX(created_at) FROM messages WHERE conversation_id = ?",
		conversationID,
	).Scan(&raw)
	if err != nil {
		return time.Time{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range sqliteTimeLayouts {
		if t, perr := time.ParseInLocation(layout, raw.String, time.UTC); perr == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, nil
}

// EnqueuePrompt persists a fresh user prompt with queue_status='pending'.
// Returns the saved Message (with id + created_at populated) so the
// dispatcher / WS handler can surface the canonical id back to the client
// via input_ack.
func (s *SQLiteStore) EnqueuePrompt(ctx context.Context, conversationID, content string) (Message, error) {
	msg := Message{
		ID:             uuid.New().String(),
		ConversationID: conversationID,
		Role:           "user",
		Content:        content,
		QueueStatus:    "pending",
	}
	if err := s.SaveMessage(ctx, msg); err != nil {
		return Message{}, err
	}
	// Re-read so CreatedAt reflects the DB-assigned value rather than the
	// zero time. Keeps the wire shape symmetrical with REST history.
	row := s.db.QueryRowContext(ctx,
		"SELECT "+messageColumns+" FROM messages WHERE id = ?",
		msg.ID,
	)
	out, err := scanMessage(row)
	if err != nil {
		return Message{}, err
	}
	return out, nil
}

// PeekNextPendingPrompt returns the oldest pending prompt for the
// conversation without modifying it. The dispatcher worker uses this
// before it tries to acquire a pool slot — we don't want to flip the row
// to 'processing' while we're still parked behind another account-mate,
// because that would prematurely move the prompt out of the staging
// area. ClaimPendingPromptByID is the matching atomic transition once a
// slot is granted. ErrNotFound when no pending row exists.
//
// Joined with `conversations.deleted_at IS NULL` so a soft-deleted
// conversation's leftover pending prompts never wake up the dispatcher.
func (s *SQLiteStore) PeekNextPendingPrompt(ctx context.Context, conversationID string) (Message, error) {
	var (
		id        string
		content   string
		createdAt time.Time
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT m.id, m.content, m.created_at
		   FROM messages m
		   JOIN conversations c ON c.id = m.conversation_id
		  WHERE m.conversation_id = ? AND m.queue_status = 'pending' AND c.deleted_at IS NULL
		  ORDER BY m.rowid ASC
		  LIMIT 1`,
		conversationID,
	).Scan(&id, &content, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:             id,
		ConversationID: conversationID,
		Role:           "user",
		Content:        content,
		CreatedAt:      createdAt,
		QueueStatus:    "pending",
	}, nil
}

// claimReinsertTx moves a still-pending prompt to 'processing' by
// deleting it and re-inserting an identical row (same id, content,
// metadata, and send-time created_at) so it receives a fresh, higher
// rowid. This re-orders the prompt to sort *after* the previous turn's
// reply: a queued prompt is first persisted the instant the user hits
// send — while the prior turn is still streaming — so its original
// rowid precedes that turn's assistant message, which is saved only at
// end-of-turn. History ordered by rowid would then read
// "my msg → my new msg → reply → new reply". The per-conversation
// dispatcher reaches this claim only once the prior reply is in the DB,
// so re-inserting here lands the prompt where the user expects it
// ("…reply → my new msg → new reply"). The pending-guarded DELETE means
// a user-initiated cancel that removed the row between peek and claim
// resolves cleanly as ErrNotFound.
func claimReinsertTx(ctx context.Context, tx *sql.Tx, messageID string) (Message, error) {
	var (
		convID    string
		content   string
		metadata  string
		createdAt time.Time
		recovery  int
	)
	err := tx.QueryRowContext(ctx,
		`SELECT conversation_id, content, metadata, created_at, recovery_count
		   FROM messages WHERE id = ? AND queue_status = 'pending'`,
		messageID,
	).Scan(&convID, &content, &metadata, &createdAt, &recovery)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}

	res, err := tx.ExecContext(ctx,
		"DELETE FROM messages WHERE id = ? AND queue_status = 'pending'",
		messageID,
	)
	if err != nil {
		return Message{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Message{}, err
	}
	if n == 0 {
		// Raced with a cancel/claim between our SELECT and DELETE.
		return Message{}, ErrNotFound
	}

	// Preserve the original send time, written in the same canonical UTC
	// text form SQLite's datetime('now') default produces, so the column
	// never mixes representations.
	createdAtStr := createdAt.UTC().Format("2006-01-02 15:04:05")
	// Carry recovery_count across the re-insert so startup recovery can keep
	// counting resume attempts for a prompt that keeps killing the server
	// (e.g. one that restarts the service) and eventually abandon it.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO messages (id, conversation_id, role, content, metadata, queue_status, created_at, recovery_count)
		 VALUES (?, ?, 'user', ?, ?, 'processing', ?, ?)`,
		messageID, convID, content, metadata, createdAtStr, recovery,
	); err != nil {
		return Message{}, err
	}
	return Message{
		ID:             messageID,
		ConversationID: convID,
		Role:           "user",
		Content:        content,
		CreatedAt:      createdAt,
		QueueStatus:    "processing",
	}, nil
}

// ClaimPendingPromptByID atomically transitions the named row from
// 'pending' to 'processing'. Used after the dispatcher worker has
// acquired a pool slot for the prompt it peeked earlier. A user-initiated
// cancel that deleted the row in the gap between peek and claim resolves
// cleanly as ErrNotFound. See claimReinsertTx for why claiming re-inserts
// the row rather than flipping a column in place.
//
// Retried: claimReinsertTx reads the pending row before deleting it, the
// deferred read-then-write shape a concurrent commit (any other
// conversation's SaveMessage) turns into an instant SQLITE_BUSY_SNAPSHOT.
func (s *SQLiteStore) ClaimPendingPromptByID(ctx context.Context, messageID string) (Message, error) {
	var msg Message
	err := s.withTxRetry(ctx, func(tx *sql.Tx) error {
		var err error
		msg, err = claimReinsertTx(ctx, tx, messageID)
		return err
	})
	if err != nil {
		return Message{}, err
	}
	return msg, nil
}

// MarkPromptDone clears queue_status. The row stays in history as a
// regular completed user message.
func (s *SQLiteStore) MarkPromptDone(ctx context.Context, messageID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE messages SET queue_status = '' WHERE id = ?", messageID,
	)
	return err
}

// ClearPoolQueuedPrompts drops the pool-queued marker from every row still
// carrying it. An IM turn cannot survive a restart — the connector, the pool
// ticket and the agent process are all gone — so a leftover marker would show
// the chat UI a queue card for a turn nobody is waiting on. Returns the number
// of rows cleared so boot can log it.
func (s *SQLiteStore) ClearPoolQueuedPrompts(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE messages SET queue_status = '' WHERE queue_status = ?", QueueStatusPoolQueued,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// DeletePendingPrompts wipes all 'pending' rows for a conversation and
// returns them so the cancel handler can ship the contents back to the
// client to repopulate the input editor. Order: oldest pending first
// (FIFO) so the client sees its messages in the original send order
// when stitching them back.
//
// Retried: this is a read-then-write deferred transaction, so a concurrent
// commit invalidates its snapshot and busy_timeout does not apply — see
// retryOnBusySnapshot. Losing that race means the user's cancel click does
// nothing while the prompt they cancelled starts running.
func (s *SQLiteStore) DeletePendingPrompts(ctx context.Context, conversationID string) ([]Message, error) {
	var deleted []Message
	err := s.withTxRetry(ctx, func(tx *sql.Tx) error {
		var err error
		deleted, err = deletePendingPromptsTx(ctx, tx, conversationID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return deleted, nil
}

func deletePendingPromptsTx(ctx context.Context, tx *sql.Tx, conversationID string) ([]Message, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, conversation_id, role, content, created_at
		   FROM messages
		  WHERE conversation_id = ? AND queue_status = 'pending'
		  ORDER BY rowid ASC`,
		conversationID,
	)
	if err != nil {
		return nil, err
	}
	var deleted []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		m.QueueStatus = "pending"
		deleted = append(deleted, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	if len(deleted) == 0 {
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM messages WHERE conversation_id = ? AND queue_status = 'pending'",
		conversationID,
	); err != nil {
		return nil, err
	}
	return deleted, nil
}

// DeletePendingPrompt removes a single 'pending' prompt by id. The DELETE
// is guarded by queue_status='pending' so a row that the worker happened to
// claim between the user's recall click and this call survives — that
// 'processing' row owns whatever partial assistant/tool history has already
// streamed against it. Returns ErrNotFound when no row was deleted.
//
// Retried for the same reason as DeletePendingPrompts: read-then-write in a
// deferred transaction is the one shape busy_timeout cannot cover.
func (s *SQLiteStore) DeletePendingPrompt(ctx context.Context, messageID string) (Message, error) {
	var msg Message
	err := s.withTxRetry(ctx, func(tx *sql.Tx) error {
		var err error
		msg, err = deletePendingPromptTx(ctx, tx, messageID)
		return err
	})
	if err != nil {
		return Message{}, err
	}
	return msg, nil
}

func deletePendingPromptTx(ctx context.Context, tx *sql.Tx, messageID string) (Message, error) {
	var m Message
	err := tx.QueryRowContext(ctx,
		`SELECT id, conversation_id, role, content, created_at
		   FROM messages
		  WHERE id = ? AND queue_status = 'pending'`,
		messageID,
	).Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	m.QueueStatus = "pending"

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM messages WHERE id = ? AND queue_status = 'pending'",
		messageID,
	); err != nil {
		return Message{}, err
	}
	return m, nil
}

// maxPromptRecoveries caps how many times startup recovery will resume a
// single prompt before giving up on it. A prompt that completes (or errors)
// normally never re-enters recovery, so the only way to reach the cap is a
// prompt that keeps killing the server before it can finish — most plausibly
// one that restarts the daymug service itself. Three attempts tolerates a
// couple of unrelated restarts mid-run while still breaking a tight loop fast.
const maxPromptRecoveries = 3

// ResetProcessingToPending demotes 'processing' rows to 'pending' for re-run,
// incrementing each row's recovery_count first. Any row whose count reaches
// maxPromptRecoveries is abandoned (queue_status cleared) instead of demoted,
// so a prompt that triggers the shutdown can't loop the server forever.
// Returns (recovered, abandoned) counts so startup can log the recovery line.
func (s *SQLiteStore) ResetProcessingToPending(ctx context.Context) (recovered, abandoned int, err error) {
	var abRes, pendRes sql.Result
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		// Count this recovery pass before deciding each row's fate.
		if _, err := tx.ExecContext(ctx,
			"UPDATE messages SET recovery_count = recovery_count + 1 WHERE queue_status = 'processing'",
		); err != nil {
			return err
		}

		// Abandon poison prompts that have now exhausted their recovery budget:
		// clear queue_status so they drop out of the pending set and are never
		// resumed. The row stays in history as a normal (reply-less) user turn.
		var err error
		abRes, err = tx.ExecContext(ctx,
			"UPDATE messages SET queue_status = '' WHERE queue_status = 'processing' AND recovery_count >= ?",
			maxPromptRecoveries,
		)
		if err != nil {
			return err
		}

		// Demote the survivors so the dispatcher re-runs them.
		pendRes, err = tx.ExecContext(ctx,
			"UPDATE messages SET queue_status = 'pending' WHERE queue_status = 'processing'",
		)
		return err
	})
	if err != nil {
		return 0, 0, err
	}

	ab, err := abRes.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	pend, err := pendRes.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	return int(pend), int(ab), nil
}

// ListConversationsWithPending returns every conversation id that has at
// least one un-finished prompt row. After ResetProcessingToPending this
// is the set of conversations for which the dispatcher should immediately
// spin up a worker. Soft-deleted conversations are skipped — their leftover
// queued prompts must not be revived after the row was hidden from the API.
func (s *SQLiteStore) ListConversationsWithPending(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT m.conversation_id
		   FROM messages m
		   JOIN conversations c ON c.id = m.conversation_id
		  WHERE m.queue_status != '' AND c.deleted_at IS NULL`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLiteStore) ListMessages(ctx context.Context, conversationID string, limit, offset int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	// JOIN with conversations so messages of a soft-deleted conversation
	// disappear from listings — the API gate on requireConv already blocks
	// the public endpoint, but the join keeps the same invariant for
	// internal callers (auto-title, terminal replay, tests) that hit the
	// store directly.
	rows, err := s.db.QueryContext(ctx,
		// Order on rowid (insertion order), matching ListMessagesAfter/
		// Before. rowid is strictly monotonic so it never coin-flips
		// between two rows saved in the same wall-clock second the way
		// created_at (1s resolution) does — and it reflects the claim-time
		// re-insertion that reorders a queued prompt to sit after the
		// previous turn's reply (see claimReinsertTx).
		`SELECT `+messageColumnsM+`
		   FROM messages m
		   JOIN conversations c ON c.id = m.conversation_id
		  WHERE m.conversation_id = ? AND c.deleted_at IS NULL
		  ORDER BY m.rowid ASC
		  LIMIT ? OFFSET ?`,
		conversationID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *SQLiteStore) ListMessagesAfter(ctx context.Context, conversationID, lastMessageID string, limit int) ([]Message, error) {
	if conversationID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}

	// Order on rowid: it's monotonically increasing per insert, so messages
	// persisted in the same wall-clock second (datetime('now') is 1s) still
	// sort by insertion order. The previous (created_at, id) tie-break was
	// effectively a coin flip — id is a random V4 UUID, so within the same
	// second a newer message could be filtered out by `id > cursor` if its
	// UUID happened to be lexicographically smaller.
	//
	// Empty or unknown cursor: cursorRowid stays 0 and `rowid > 0` matches
	// every row (SQLite rowids start at 1), so the caller gets the full
	// conversation. That's the recovery path for a reconnecting client
	// that doesn't yet have a cursor — e.g. a brand-new conversation
	// whose first user message was sent right before going offline.
	var cursorRowid int64
	if lastMessageID != "" {
		err := s.db.QueryRowContext(ctx,
			"SELECT rowid FROM messages WHERE id = ? AND conversation_id = ?",
			lastMessageID, conversationID).Scan(&cursorRowid)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageColumnsM+`
		   FROM messages m
		   JOIN conversations c ON c.id = m.conversation_id
		  WHERE m.conversation_id = ? AND m.rowid > ? AND c.deleted_at IS NULL
		  ORDER BY m.rowid ASC
		  LIMIT ?`,
		conversationID, cursorRowid, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *SQLiteStore) ListMessagesBefore(ctx context.Context, conversationID, beforeMessageID string, limit int) ([]Message, error) {
	if conversationID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}

	// Same rowid-based ordering as ListMessagesAfter — see the comment
	// there for why we don't use (created_at, id). Empty / unknown cursor
	// resolves to math.MaxInt64 so the predicate `rowid < cursor` matches
	// every row, giving the caller the latest page (the initial load).
	cursorRowid := int64(math.MaxInt64)
	if beforeMessageID != "" {
		var found int64
		err := s.db.QueryRowContext(ctx,
			"SELECT rowid FROM messages WHERE id = ? AND conversation_id = ?",
			beforeMessageID, conversationID).Scan(&found)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			cursorRowid = found
		}
	}

	// Pull the latest page in DESC order so LIMIT keeps the *newest* of the
	// matching rows, then reverse before returning so callers always see
	// ascending (oldest-first) results — matching the wire shape of
	// ListMessages and ListMessagesAfter.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+messageColumnsM+`
		   FROM messages m
		   JOIN conversations c ON c.id = m.conversation_id
		  WHERE m.conversation_id = ? AND m.rowid < ? AND c.deleted_at IS NULL
		  ORDER BY m.rowid DESC
		  LIMIT ?`,
		conversationID, cursorRowid, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *SQLiteStore) ClearMessages(ctx context.Context, conversationID string) error {
	newSessionID := uuid.New().String()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM messages WHERE conversation_id = ?", conversationID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE conversations SET session_id = ? WHERE id = ?", newSessionID, conversationID)
		return err
	})
}

// ExclusiveAttachmentPaths returns the attachment paths recorded in the
// metadata of conversationID's messages that no message of any other live
// conversation also records. Deleted conversations are read on purpose: the
// caller asks right after soft-deleting, while the rows are still on disk.
//
// The overlap check is what makes deleting the files safe. Uploads are scoped
// per agent, not per conversation, so a rotated or imported conversation can
// carry the same path — and removing the file would break that transcript.
// instr is used rather than LIKE because sanitized upload names keep `_`,
// which LIKE treats as a wildcard.
func (s *SQLiteStore) ExclusiveAttachmentPaths(ctx context.Context, conversationID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT json_extract(a.value, '$.path') AS p
		FROM messages m,
		     json_each(CASE WHEN json_valid(m.metadata) THEN m.metadata ELSE '{}' END, '$.attachments') a
		WHERE m.conversation_id = ?
		  AND json_type(a.value, '$.path') = 'text'
		  AND NOT EXISTS (
		    SELECT 1 FROM messages o
		    JOIN conversations c ON c.id = o.conversation_id
		    WHERE c.deleted_at IS NULL
		      AND o.conversation_id != m.conversation_id
		      AND o.metadata != ''
		      AND instr(o.metadata, json_extract(a.value, '$.path')) > 0
		  )`, conversationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, rows.Err()
}
