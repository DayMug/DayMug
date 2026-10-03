package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriterAppendsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "daymug.log")
	for _, line := range []string{"first\n", "second\n"} {
		w, err := Open(path, 0, 0)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "first\nsecond\n" {
		t.Fatalf("log = %q, want both lines — a restart must not truncate history", got)
	}
}

// Rotation is what keeps the log from becoming the reason the disk fills, which
// would trade one silent failure for a louder one.
func TestWriterRotatesAndBoundsGenerations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daymug.log")
	w, err := Open(path, 16, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = w.Close() }()

	for i := range 10 {
		if _, err := fmt.Fprintf(w, "line-%02d\n", i); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) > 3 {
		t.Fatalf("log generations = %v, want the current file plus at most 2 rotations", names)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if !strings.Contains(string(current), "line-09") {
		t.Fatalf("current log = %q, want the newest record", current)
	}
}

// A record never straddles a rotation: half a stack trace in each of two files
// is worse than either file alone.
func TestWriterKeepsRecordsWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daymug.log")
	w, err := Open(path, 16, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = w.Close() }()

	record := []byte("a-record-longer-than-the-cap\n")
	if _, err := w.Write([]byte("short\n")); err != nil {
		t.Fatalf("write short: %v", err)
	}
	if _, err := w.Write(record); err != nil {
		t.Fatalf("write record: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(record) {
		t.Fatalf("current log = %q, want exactly the whole oversized record", got)
	}
}

// The log quotes workdirs and agent errors, so other local accounts must not
// read it — including a log a pre-0600 release already created as 0644.
func TestWriterTightensExistingLogToOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daymug.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, 0, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = w.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log mode = %o, want 600", perm)
	}
}
