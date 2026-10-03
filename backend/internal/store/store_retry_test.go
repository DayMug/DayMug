package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point of the helper: a transaction that lost its snapshot is
// retried from scratch and the second attempt's success is what the caller
// sees. Without this, a cancel click that races a concurrent write is dropped.
func TestRetryOnBusySnapshot_SucceedsAfterBusy(t *testing.T) {
	t.Parallel()
	attempts := 0
	err := retryOnBusySnapshot(context.Background(), func() error {
		attempts++
		if attempts == 1 {
			return &fakeSQLiteError{msg: "database is locked (517)"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success on retry, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

// A logical failure must not be retried — ErrNotFound is a real answer, and
// spinning on it would delay the caller's error by the full backoff budget.
func TestRetryOnBusySnapshot_DoesNotRetryOtherErrors(t *testing.T) {
	t.Parallel()
	attempts := 0
	err := retryOnBusySnapshot(context.Background(), func() error {
		attempts++
		return ErrNotFound
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// Retrying is bounded: a permanently contended database surfaces the busy
// error instead of hanging the request forever.
func TestRetryOnBusySnapshot_GivesUpAfterBudget(t *testing.T) {
	t.Parallel()
	attempts := 0
	err := retryOnBusySnapshot(context.Background(), func() error {
		attempts++
		return &fakeSQLiteError{msg: "database is locked (517)"}
	})
	if err == nil {
		t.Fatal("expected the busy error to surface")
	}
	if attempts != busySnapshotAttempts {
		t.Fatalf("attempts = %d, want %d", attempts, busySnapshotAttempts)
	}
}

// A canceled request stops the loop at the next backoff instead of burning the
// remaining budget, and reports the busy error rather than the context error so
// the caller's log names the real cause.
func TestRetryOnBusySnapshot_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := retryOnBusySnapshot(ctx, func() error {
		attempts++
		cancel()
		return &fakeSQLiteError{msg: "database is locked (517)"}
	})
	if err == nil {
		t.Fatal("expected the busy error to surface")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// Grounds the classifier in the driver's real behaviour rather than a
// hand-written string: a deferred transaction that reads, then writes after
// someone else committed, is the exact shape busy_timeout cannot cover.
func TestIsBusyErr_RealSnapshotUpgrade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "busy.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "t", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	// Reader takes a snapshot. The store's own transactions are IMMEDIATE and
	// can no longer hit this, but isBusyErr must still classify it: open a
	// DEFERRED connection to reproduce the conflict.
	deferred, err := sql.Open("sqlite", strings.Replace(dsnWithPragmas(dbPath), "_txlock=immediate", "_txlock=deferred", 1))
	if err != nil {
		t.Fatalf("open deferred conn: %v", err)
	}
	defer func() { _ = deferred.Close() }()
	tx, err := deferred.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations").Scan(&n); err != nil {
		t.Fatalf("read in tx: %v", err)
	}

	// ...a second connection advances the database underneath it...
	other, err := sql.Open("sqlite", dsnWithPragmas(dbPath))
	if err != nil {
		t.Fatalf("open second conn: %v", err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.ExecContext(ctx,
		"INSERT INTO conversations (id, user_id, title) VALUES ('c2', 'u1', 't')"); err != nil {
		t.Fatalf("concurrent insert: %v", err)
	}

	// ...so upgrading the stale snapshot to a write must fail immediately.
	start := time.Now()
	_, err = tx.ExecContext(ctx, "UPDATE conversations SET title = 'x' WHERE id = 'c1'")
	if err == nil {
		t.Skip("driver allowed the snapshot upgrade; nothing to classify")
	}
	if !isBusyErr(err) {
		t.Fatalf("isBusyErr(%v) = false, want true", err)
	}
	// The failure is instant, which is precisely why busy_timeout(5000) does
	// not help and the retry has to live in Go.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("snapshot conflict took %v; expected an immediate failure", elapsed)
	}
}

func TestIsBusyErr_IgnoresUnrelatedErrors(t *testing.T) {
	t.Parallel()
	for _, err := range []error{nil, ErrNotFound, sql.ErrNoRows, fmt.Errorf("no such table: users")} {
		if isBusyErr(err) {
			t.Errorf("isBusyErr(%v) = true, want false", err)
		}
	}
}

type fakeSQLiteError struct{ msg string }

func (e *fakeSQLiteError) Error() string { return e.msg }
