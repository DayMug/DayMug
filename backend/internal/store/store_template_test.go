package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"modernc.org/sqlite"
)

// migratedTemplate is a fully migrated database built once per test binary.
// Running all ~100 migration steps for each of the ~200 store tests used to
// dominate the package's runtime; restoring a copy of the result is a single
// page copy.
var migratedTemplate struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if migratedTemplate.dir != "" {
		_ = os.RemoveAll(migratedTemplate.dir)
	}
	os.Exit(code)
}

func migratedTemplatePath() (string, error) {
	migratedTemplate.once.Do(func() {
		migratedTemplate.path, migratedTemplate.err = buildMigratedTemplate()
	})
	return migratedTemplate.path, migratedTemplate.err
}

func buildMigratedTemplate() (string, error) {
	dir, err := os.MkdirTemp("", "daymug-store-template-")
	if err != nil {
		return "", err
	}
	migratedTemplate.dir = dir
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	if err := s.Init(); err != nil {
		return "", fmt.Errorf("migrate template: %w", err)
	}
	path := filepath.Join(dir, "migrated.db")
	if _, err := s.db.Exec("VACUUM INTO ?", path); err != nil {
		return "", fmt.Errorf("write template: %w", err)
	}
	return path, nil
}

// restoreFrom overwrites the store's database with the file at path through
// SQLite's online backup API. For a ":memory:" store every pooled connection
// is its own database, so this fills only the connection it runs on — the one
// NewSQLiteStore just opened and parked idle, which is exactly the connection
// a fresh Init would have migrated.
func restoreFrom(s *SQLiteStore, path string) error {
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Raw(func(driverConn any) error {
		restorer, ok := driverConn.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("driver connection %T cannot restore a backup", driverConn)
		}
		backup, err := restorer.NewRestore(path)
		if err != nil {
			return err
		}
		if _, err := backup.Step(-1); err != nil {
			_ = backup.Finish()
			return err
		}
		return backup.Finish()
	})
}

// newTestStore returns an in-memory store holding a fresh copy of the
// migrated template. Init still runs so the store starts exactly as the
// server's does (ledger check, deferred steps, maintenance loop), but every
// step is already recorded and none of them executes.
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path, err := migratedTemplatePath()
	if err != nil {
		t.Fatalf("migrated template: %v", err)
	}
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := restoreFrom(s, path); err != nil {
		t.Fatalf("restore template: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return s
}

// The template must be indistinguishable from migrating in place, or every
// test built on newTestStore would exercise a schema production never sees.
func TestNewTestStore_MatchesFreshlyMigratedSchema(t *testing.T) {
	t.Parallel()
	fresh, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if err := fresh.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	want := schemaSnapshot(t, fresh)
	got := schemaSnapshot(t, newTestStore(t))
	if len(got) != len(want) {
		t.Fatalf("schema objects = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("schema differs:\n got: %s\nwant: %s", got[i], want[i])
		}
	}
	var applied, wantApplied int
	if err := fresh.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&wantApplied); err != nil {
		t.Fatal(err)
	}
	if err := newTestStore(t).db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != wantApplied {
		t.Fatalf("recorded migrations = %d, want %d", applied, wantApplied)
	}
}

// Each test gets its own copy: writes in one store never reach another.
func TestNewTestStore_CopiesAreIndependent(t *testing.T) {
	t.Parallel()
	a, b := newTestStore(t), newTestStore(t)
	if _, err := a.db.Exec("INSERT INTO conversations (id, user_id, title) VALUES ('only-a', 'u1', 't')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	assertCount(t, b, "SELECT COUNT(*) FROM conversations", 0)
	assertCount(t, a, "SELECT COUNT(*) FROM conversations", 1)
}
