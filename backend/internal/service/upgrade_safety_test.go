package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func newTestDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('x')"); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestBackupDatabaseForUpgradeWritesOwnerOnlySnapshot(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "data", "database.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	newTestDB(t, dbPath)

	now := time.Date(2026, 9, 26, 8, 30, 0, 0, time.UTC)
	got, err := BackupDatabaseForUpgrade(context.Background(), dbPath, "v1.5.69", now)
	if err != nil {
		t.Fatalf("BackupDatabaseForUpgrade: %v", err)
	}
	want := filepath.Join(filepath.Dir(dbPath), "backups", "database-v1.5.69-20260926T083000Z.db")
	if got != want {
		t.Fatalf("backup path = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("backup mode = %o, want 600", perm)
	}
	db, err := sql.Open("sqlite", got)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "x" {
		t.Fatalf("snapshot content = %q, %v", v, err)
	}
}

func TestBackupDatabaseForUpgradeKeepsNewestThree(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "database.db")
	newTestDB(t, dbPath)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		// Distinct mtimes: several upgrades within one second would otherwise
		// tie and fall back to name order, which is also newest-first here.
		p, err := BackupDatabaseForUpgrade(context.Background(), dbPath, "v1.5."+string(rune('0'+i)), base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	got := listDir(t, UpgradeBackupDir(dbPath))
	want := []string{
		"database-v1.5.2-20260901T020000Z.db",
		"database-v1.5.3-20260901T030000Z.db",
		"database-v1.5.4-20260901T040000Z.db",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backups = %v, want %v", got, want)
	}
}

func TestPruneUpgradeBackupsLeavesForeignFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	files := []string{
		"database-v1-a.db", "database-v1-b.db", "database-v1-c.db", "database-v1-d.db",
		"database-v1-e.db.tmp", // interrupted snapshot
		"database.db.bak",      // hand-made backup
		"notes.txt",
	}
	for i, f := range files {
		p := filepath.Join(dir, f)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := PruneUpgradeBackups(dir, 3)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	sort.Strings(removed)
	if want := []string{"database-v1-a.db", "database-v1-e.db.tmp"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	want := []string{"database-v1-b.db", "database-v1-c.db", "database-v1-d.db", "database.db.bak", "notes.txt"}
	if got := listDir(t, dir); !reflect.DeepEqual(got, want) {
		t.Fatalf("remaining = %v, want %v", got, want)
	}
}

func TestBackupDatabaseForUpgradeNoDatabaseIsNoop(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "database.db")
	got, err := BackupDatabaseForUpgrade(context.Background(), dbPath, "v1", time.Now())
	if err != nil || got != "" {
		t.Fatalf("got %q, %v; want no-op", got, err)
	}
}

func TestStopCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode string
		want []string
	}{
		{"user", []string{"--user", "stop", "daymug"}},
		{"system", []string{"stop", "daymug"}},
	} {
		name, args := stopCommand("daymug", tc.mode)
		if name != "systemctl" || !reflect.DeepEqual(args, tc.want) {
			t.Fatalf("mode %s: %s %v, want systemctl %v", tc.mode, name, args, tc.want)
		}
	}
}

// fakeRelease writes a shell script standing in for the staged binary.
func fakeRelease(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "daymug.new")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreflightConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yaml")
	if err := os.WriteFile(valid, []byte("server:\n  addr: :8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		script  string
		config  string
		wantErr string
	}{
		{"new release accepts", `[ "$1" = check-config ] && [ "$2" = --config ] && exit 0; exit 3`, valid, ""},
		{"new release rejects", `echo "validate config: upgrade.service_mode bogus" >&2; exit 1`, valid, "upgrade.service_mode bogus"},
		// A release without check-config cannot vouch for the file, so the
		// upgrade is refused rather than validated by the running binary.
		{"release without check-config", `echo "Unknown command: $1" >&2; exit 1`, valid, "Unknown command: check-config"},
		{"no config path", `exit 1`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bin := fakeRelease(t, tc.script)
			err := PreflightConfig(context.Background(), bin, tc.config)
			// A sibling test forking at the moment fakeRelease held the
			// script open for writing leaves the child with that fd until it
			// execs, so exec here can briefly fail with ETXTBSY (golang/go#22315).
			for i := 0; i < 50 && err != nil && strings.Contains(err.Error(), "text file busy"); i++ {
				time.Sleep(20 * time.Millisecond)
				err = PreflightConfig(context.Background(), bin, tc.config)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}
