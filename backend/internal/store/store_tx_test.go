package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func countConversations(t *testing.T, s *SQLiteStore, id string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM conversations WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count conversations: %v", err)
	}
	return n
}

func insertConversationTx(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO conversations (id, user_id, title) VALUES (?, 'u1', 't')", id)
	return err
}

func TestWithTx_CommitsOnNil(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.withTx(ctx, func(tx *sql.Tx) error { return insertConversationTx(ctx, tx, "c1") }); err != nil {
		t.Fatalf("withTx: %v", err)
	}
	if got := countConversations(t, s, "c1"); got != 1 {
		t.Fatalf("rows = %d, want 1", got)
	}
}

func TestWithTx_RollsBackAndReturnsCallbackError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := insertConversationTx(ctx, tx, "c1"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if got := countConversations(t, s, "c1"); got != 0 {
		t.Fatalf("rows = %d, want 0 after rollback", got)
	}
}

// Dry runs do all the work and then discard it without reporting a failure.
func TestWithTx_RollbackSentinelDiscardsWritesWithoutError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := insertConversationTx(ctx, tx, "c1"); err != nil {
			return err
		}
		return errRollbackTx
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := countConversations(t, s, "c1"); got != 0 {
		t.Fatalf("rows = %d, want 0 after dry-run rollback", got)
	}
}

// Transactions are IMMEDIATE, so one that reads before it writes can no
// longer be invalidated by a concurrent commit (SQLITE_BUSY_SNAPSHOT, which
// busy_timeout does not cover). A concurrent writer instead waits for the
// lock and lands after the commit; both writes succeed on the first try.
func TestWithTx_ConcurrentWriterWaitsInsteadOfInvalidatingSnapshot(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "retry.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	other, err := sql.Open("sqlite", dsnWithPragmas(dbPath))
	if err != nil {
		t.Fatalf("open second conn: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })

	ctx := context.Background()
	attempts := 0
	concurrent := make(chan error, 1)
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		attempts++
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations").Scan(&n); err != nil {
			return err
		}
		go func() {
			_, err := other.ExecContext(ctx,
				"INSERT INTO conversations (id, user_id, title) VALUES ('concurrent', 'u1', 't')")
			concurrent <- err
		}()
		// Give the concurrent writer time to hit the lock while we hold it.
		time.Sleep(200 * time.Millisecond)
		select {
		case err := <-concurrent:
			t.Fatalf("concurrent insert finished inside our transaction (err=%v); the write lock was not held", err)
		default:
		}
		return insertConversationTx(ctx, tx, "mine")
	})
	if err != nil {
		t.Fatalf("withTx: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if err := <-concurrent; err != nil {
		t.Fatalf("concurrent insert after commit: %v", err)
	}
	for _, id := range []string{"mine", "concurrent"} {
		if got := countConversations(t, s, id); got != 1 {
			t.Fatalf("rows for %s = %d, want 1", id, got)
		}
	}
}
