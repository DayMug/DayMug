package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *SQLiteStore) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)",
		sess.Token, sess.UserID, sess.ExpiresAt)
	return err
}

func (s *SQLiteStore) GetSession(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNotFound
	}
	var sess Session
	err := s.db.QueryRowContext(ctx,
		"SELECT token, user_id, created_at, expires_at FROM sessions WHERE token = ?", token).
		Scan(&sess.Token, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if !sess.ExpiresAt.IsZero() && time.Now().After(sess.ExpiresAt) {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token = ?", token)
	return err
}

// DeleteExpiredSessions reaps rows whose expiry has passed. GetSession already
// rejects expired tokens, so this is purely about disk: without it the table
// only ever grows. OptimizeDatabase runs it as part of its reclaim pass.
func (s *SQLiteStore) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.deleteExpiredSessions(ctx)
	return err
}

// deleteExpiredSessions is the counting variant OptimizeDatabase reports from.
//
// The cutoff is a bound time.Time, never SQLite's datetime('now'): modernc/
// sqlite writes time.Time with a 'T' separator while datetime('now') emits a
// space, and TEXT comparison of the two disagrees with chronological order —
// with datetime('now') the reap silently matches nothing.
func (s *SQLiteStore) deleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", time.Now())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil //nolint:nilerr // the delete succeeded; only the count is unknown
	}
	return n, nil
}
