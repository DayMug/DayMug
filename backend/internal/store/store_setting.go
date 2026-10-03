package store

import (
	"context"
	"database/sql"
	"errors"
)

// GetAppSetting returns the value stored under the given key. An unknown
// key is not an error — the caller can't distinguish "never set" from
// "cleared" and shouldn't need to.
func (s *SQLiteStore) GetAppSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx,
		"SELECT value FROM app_settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetAppSetting upserts the row keyed by `key`. Empty value is allowed
// and represents "cleared" (so the frontend hides the help button).
func (s *SQLiteStore) SetAppSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at)
		     VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET
		    value      = excluded.value,
		    updated_at = excluded.updated_at`, key, value)
	return err
}
