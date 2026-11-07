package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Device is a desktop client (DayMug-Client) paired to a human user. Turns of
// a conversation bound to a device run their file and shell tools on that
// machine instead of the server.
type Device struct {
	ID     string `json:"id"`
	UserID string `json:"-"`
	Name   string `json:"name"`
	OS     string `json:"os"`
	// TokenHash is the SHA-256 of the bearer token the client connects with;
	// the token itself is shown once at pairing and never stored.
	TokenHash  string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// ConversationDevice binds a conversation to a device and the directory on
// that device its tools work in.
type ConversationDevice struct {
	ConversationID string `json:"conversation_id"`
	DeviceID       string `json:"device_id"`
	WorkDir        string `json:"work_dir"`
}

const deviceColumns = "id, user_id, name, os, token_hash, created_at, last_seen_at"

func scanDevice(scanner interface{ Scan(...any) error }) (Device, error) {
	var d Device
	if err := scanner.Scan(&d.ID, &d.UserID, &d.Name, &d.OS, &d.TokenHash, &d.CreatedAt, &d.LastSeenAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, ErrNotFound
		}
		return Device{}, err
	}
	d.CreatedAt = d.CreatedAt.UTC()
	d.LastSeenAt = d.LastSeenAt.UTC()
	return d, nil
}

// CreateDevice persists d, assigning an id when empty.
func (s *SQLiteStore) CreateDevice(ctx context.Context, d Device) (Device, error) {
	if d.ID == "" {
		d.ID = uuid.New().String()
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO devices (id, user_id, name, os, token_hash) VALUES (?, ?, ?, ?, ?)`,
		d.ID, d.UserID, d.Name, d.OS, d.TokenHash,
	); err != nil {
		return Device{}, err
	}
	return scanDevice(s.db.QueryRowContext(ctx, "SELECT "+deviceColumns+" FROM devices WHERE id = ?", d.ID))
}

// GetDeviceByTokenHash resolves a client's bearer token. ErrNotFound covers
// both an unknown token and a revoked device.
func (s *SQLiteStore) GetDeviceByTokenHash(ctx context.Context, tokenHash string) (Device, error) {
	if tokenHash == "" {
		return Device{}, ErrNotFound
	}
	return scanDevice(s.db.QueryRowContext(ctx, "SELECT "+deviceColumns+" FROM devices WHERE token_hash = ?", tokenHash))
}

// GetDevice returns one device by id.
func (s *SQLiteStore) GetDevice(ctx context.Context, id string) (Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, "SELECT "+deviceColumns+" FROM devices WHERE id = ?", id))
}

// ListDevices returns a user's devices, most recently created first.
func (s *SQLiteStore) ListDevices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+deviceColumns+" FROM devices WHERE user_id = ? ORDER BY created_at DESC, id", userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDevice revokes a user's device; its conversation bindings go with it.
func (s *SQLiteStore) DeleteDevice(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM devices WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchDevice records that the device connected at `at`.
func (s *SQLiteStore) TouchDevice(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE devices SET last_seen_at = ? WHERE id = ?", at.UTC().Format(scheduledTimeLayout), id)
	return err
}

// GetConversationDevice returns the conversation's device binding, or
// ErrNotFound when the conversation runs on the server.
func (s *SQLiteStore) GetConversationDevice(ctx context.Context, conversationID string) (ConversationDevice, error) {
	var b ConversationDevice
	err := s.db.QueryRowContext(ctx,
		"SELECT conversation_id, device_id, work_dir FROM conversation_devices WHERE conversation_id = ?", conversationID,
	).Scan(&b.ConversationID, &b.DeviceID, &b.WorkDir)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationDevice{}, ErrNotFound
	}
	return b, err
}

// SetConversationDevice binds (or rebinds) a conversation to a device.
func (s *SQLiteStore) SetConversationDevice(ctx context.Context, b ConversationDevice) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO conversation_devices (conversation_id, device_id, work_dir) VALUES (?, ?, ?)
		 ON CONFLICT(conversation_id) DO UPDATE SET device_id = excluded.device_id, work_dir = excluded.work_dir,
		 updated_at = datetime('now')`,
		b.ConversationID, b.DeviceID, b.WorkDir)
	return err
}

// ClearConversationDevice returns a conversation to server execution.
func (s *SQLiteStore) ClearConversationDevice(ctx context.Context, conversationID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM conversation_devices WHERE conversation_id = ?", conversationID)
	return err
}

// ListDeviceConversations returns the ids of the conversations bound to a device.
func (s *SQLiteStore) ListDeviceConversations(ctx context.Context, deviceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT conversation_id FROM conversation_devices WHERE device_id = ? ORDER BY conversation_id", deviceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
