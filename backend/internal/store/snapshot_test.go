package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotDatabaseCopiesCommittedRows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "database.db")
	db, err := sql.Open("sqlite", dsnWithPragmas(src))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// WAL mode with the writer still open: the rows live in the -wal file,
	// which a plain file copy of database.db would miss.
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"CREATE TABLE t (v TEXT)",
		"INSERT INTO t VALUES ('a'), ('b')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	dst := filepath.Join(dir, "snap.db")
	if err := SnapshotDatabase(context.Background(), src, dst); err != nil {
		t.Fatalf("SnapshotDatabase: %v", err)
	}
	snap, err := sql.Open("sqlite", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	var n int
	if err := snap.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if n != 2 {
		t.Fatalf("snapshot rows = %d, want 2", n)
	}
}

func TestSnapshotDatabaseRefusesExistingDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "database.db")
	if err := os.WriteFile(src, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(dst, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotDatabase(context.Background(), src, dst); err == nil {
		t.Fatal("expected an error for an existing destination")
	}
	if got, _ := os.ReadFile(dst); string(got) != "keep me" {
		t.Fatalf("destination was modified: %q", got)
	}
}

func TestSnapshotDatabaseMissingSourceDoesNotCreateIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "missing.db")
	if err := SnapshotDatabase(context.Background(), src, filepath.Join(dir, "snap.db")); err == nil {
		t.Fatal("expected an error for a missing source")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("snapshot must not create the source db: %v", err)
	}
}
