package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ScheduledPrompt is a composer message held back until SendAt. It becomes an
// ordinary pending prompt (and from then on a regular message) only when the
// scheduler fires it; until then it is visible solely through this store.
type ScheduledPrompt struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	// UserID is the sender's user-hub room. Server-side bookkeeping only.
	UserID   string          `json:"-"`
	Content  string          `json:"content"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	SendAt   time.Time       `json:"send_at"`
	// CreatedAt orders prompts that share a SendAt.
	CreatedAt time.Time `json:"created_at"`
}

// scheduledTimeLayout matches datetime('now'), so send_at compares as text
// against the bound "now" and sorts chronologically.
const scheduledTimeLayout = "2006-01-02 15:04:05"

const scheduledPromptColumns = "sp.id, sp.conversation_id, sp.user_id, sp.content, sp.metadata, sp.send_at, sp.created_at"

func scanScheduledPrompt(scanner interface{ Scan(...any) error }) (ScheduledPrompt, error) {
	var (
		p        ScheduledPrompt
		metadata string
	)
	if err := scanner.Scan(&p.ID, &p.ConversationID, &p.UserID, &p.Content, &metadata, &p.SendAt, &p.CreatedAt); err != nil {
		return ScheduledPrompt{}, err
	}
	if metadata != "" {
		p.Metadata = json.RawMessage(metadata)
	}
	p.SendAt = p.SendAt.UTC()
	p.CreatedAt = p.CreatedAt.UTC()
	return p, nil
}

func (s *SQLiteStore) queryScheduledPrompts(ctx context.Context, query string, args ...any) ([]ScheduledPrompt, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ScheduledPrompt
	for rows.Next() {
		p, err := scanScheduledPrompt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateScheduledPrompt persists p and returns it as stored: id assigned when
// empty, SendAt truncated to the second, CreatedAt from the column default.
func (s *SQLiteStore) CreateScheduledPrompt(ctx context.Context, p ScheduledPrompt) (ScheduledPrompt, error) {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	metadata := ""
	if len(p.Metadata) > 0 {
		metadata = string(p.Metadata)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_prompts (id, conversation_id, user_id, content, metadata, send_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.ConversationID, p.UserID, p.Content, metadata, p.SendAt.UTC().Format(scheduledTimeLayout),
	); err != nil {
		return ScheduledPrompt{}, err
	}
	row := s.db.QueryRowContext(ctx,
		"SELECT "+scheduledPromptColumns+" FROM scheduled_prompts sp WHERE sp.id = ?", p.ID)
	return scanScheduledPrompt(row)
}

// ListScheduledPrompts returns the conversation's scheduled prompts, soonest
// first.
func (s *SQLiteStore) ListScheduledPrompts(ctx context.Context, conversationID string) ([]ScheduledPrompt, error) {
	return s.queryScheduledPrompts(ctx,
		"SELECT "+scheduledPromptColumns+` FROM scheduled_prompts sp
		  WHERE sp.conversation_id = ?
		  ORDER BY sp.send_at ASC, sp.created_at ASC, sp.rowid ASC`,
		conversationID)
}

// ListDueScheduledPrompts returns every prompt whose SendAt is at or before
// now, oldest first. Prompts of soft-deleted conversations never come due.
func (s *SQLiteStore) ListDueScheduledPrompts(ctx context.Context, now time.Time) ([]ScheduledPrompt, error) {
	return s.queryScheduledPrompts(ctx,
		"SELECT "+scheduledPromptColumns+` FROM scheduled_prompts sp
		   JOIN conversations c ON c.id = sp.conversation_id
		  WHERE sp.send_at <= ? AND c.deleted_at IS NULL
		  ORDER BY sp.send_at ASC, sp.created_at ASC, sp.rowid ASC`,
		now.UTC().Format(scheduledTimeLayout))
}

// NextScheduledPromptTime reports the earliest SendAt across live
// conversations; false when nothing is scheduled.
func (s *SQLiteStore) NextScheduledPromptTime(ctx context.Context) (time.Time, bool, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT MIN(sp.send_at) FROM scheduled_prompts sp
		   JOIN conversations c ON c.id = sp.conversation_id
		  WHERE c.deleted_at IS NULL`,
	).Scan(&raw)
	if err != nil {
		return time.Time{}, false, err
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{scheduledTimeLayout, time.RFC3339Nano} {
		if t, perr := time.ParseInLocation(layout, raw.String, time.UTC); perr == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, errors.New("unparseable scheduled_prompts.send_at: " + raw.String)
}

// DeleteScheduledPrompt removes one prompt of the conversation and returns
// it. Both a cancel and the scheduler firing go through here, so whichever
// deletes the row owns it; the other gets ErrNotFound.
func (s *SQLiteStore) DeleteScheduledPrompt(ctx context.Context, conversationID, id string) (ScheduledPrompt, error) {
	var out ScheduledPrompt
	err := s.withTxRetry(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			"SELECT "+scheduledPromptColumns+" FROM scheduled_prompts sp WHERE sp.id = ? AND sp.conversation_id = ?",
			id, conversationID)
		p, err := scanScheduledPrompt(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM scheduled_prompts WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		out = p
		return nil
	})
	if err != nil {
		return ScheduledPrompt{}, err
	}
	return out, nil
}
