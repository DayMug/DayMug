package filewatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestWatcher returns a watcher with a short coalesce window so the tests
// don't have to wait out the production one.
func newTestWatcher(t *testing.T) *Watcher {
	t.Helper()
	w, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SetCoalesceWindow(10 * time.Millisecond)
	return w
}

func subscribeTo(t *testing.T, w *Watcher, abs, rel []string) *Subscription {
	t.Helper()
	sub, err := w.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(sub.Close)
	if _, err := sub.Watch(abs, rel); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	return sub
}

func waitEvent(t *testing.T, sub *Subscription) Event {
	t.Helper()
	select {
	case ev := <-sub.Events():
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event within 3s")
		return Event{}
	}
}

func TestWatcherReportsWriteToSubscribedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	sub := subscribeTo(t, w, []string{target}, []string{"a.md"})

	if err := os.WriteFile(target, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	if ev := waitEvent(t, sub); ev.Path != "a.md" || ev.Op != OpChanged {
		t.Fatalf("got %+v, want {a.md changed}", ev)
	}
}

// Agents and editors overwhelmingly save by writing a temp file and renaming
// it over the target. Watching the file's own inode goes deaf after the first
// such save, which is why the watcher subscribes to the parent directory —
// this test is what pins that decision down.
func TestWatcherReportsAtomicRenameSave(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	sub := subscribeTo(t, w, []string{target}, []string{"a.md"})

	tmp := filepath.Join(dir, "a.md.tmp")
	if err := os.WriteFile(tmp, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}

	if ev := waitEvent(t, sub); ev.Path != "a.md" || ev.Op != OpChanged {
		t.Fatalf("got %+v, want {a.md changed}", ev)
	}
}

func TestWatcherReportsRemoval(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	sub := subscribeTo(t, w, []string{target}, []string{"a.md"})

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	if ev := waitEvent(t, sub); ev.Path != "a.md" || ev.Op != OpRemoved {
		t.Fatalf("got %+v, want {a.md removed}", ev)
	}
}

// A single logical save fans out into several inotify events. Only one should
// leave the coalesce window, otherwise every keystroke-triggered agent write
// turns into a burst of reloads in the browser.
func TestWatcherCoalescesBurst(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	w.SetCoalesceWindow(150 * time.Millisecond)
	sub := subscribeTo(t, w, []string{target}, []string{"a.md"})

	for i := 0; i < 5; i++ {
		if err := os.WriteFile(target, []byte{byte('a' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if ev := waitEvent(t, sub); ev.Path != "a.md" {
		t.Fatalf("got %+v, want a.md", ev)
	}
	select {
	case ev := <-sub.Events():
		t.Fatalf("burst was not coalesced: extra event %+v", ev)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestWatchRejectsOverPathLimit(t *testing.T) {
	dir := t.TempDir()
	abs := make([]string, 0, MaxPathsPerSubscription+1)
	rel := make([]string, 0, MaxPathsPerSubscription+1)
	for i := 0; i <= MaxPathsPerSubscription; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i))+".md")
		abs = append(abs, name)
		rel = append(rel, filepath.Base(name))
	}

	w := newTestWatcher(t)
	sub, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	if _, err := sub.Watch(abs, rel); !errors.Is(err, ErrCapacity) {
		t.Fatalf("got %v, want ErrCapacity", err)
	}
}

// Only files the user actually has open get watched, so these directories
// should never come up. Rejecting them explicitly keeps a future caller from
// quietly turning this into a recursive tree watch.
func TestWatchRejectsIgnoredDirectories(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(t)

	for _, rel := range []string{
		"node_modules/pkg/index.js",
		".git/config",
		"dist/bundle.js",
		"app/.next/build.json",
		"vendor/lib.go",
		"__pycache__/mod.pyc",
		".venv/bin/python",
		"target/debug/app",
	} {
		sub, err := w.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		_, err = sub.Watch([]string{filepath.Join(dir, rel)}, []string{rel})
		sub.Close()
		if !errors.Is(err, ErrIgnored) {
			t.Errorf("%s: got %v, want ErrIgnored", rel, err)
		}
	}
}

// Two viewers on the same file must not be able to unwatch each other's
// directory by disconnecting first.
func TestDirWatchRefcountedAcrossSubscriptions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	first, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Watch([]string{target}, []string{"a.md"}); err != nil {
		t.Fatal(err)
	}
	second := subscribeTo(t, w, []string{target}, []string{"a.md"})

	first.Close()
	if got := w.WatchedDirCount(); got != 1 {
		t.Fatalf("watched dirs = %d, want 1 after one of two subscribers left", got)
	}

	if err := os.WriteFile(target, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev := waitEvent(t, second); ev.Path != "a.md" || ev.Op != OpChanged {
		t.Fatalf("got %+v, want {a.md changed}", ev)
	}
}

// A leaked directory watch per dropped connection is how a long-running server
// eventually exhausts its inotify budget.
func TestCloseReleasesDirWatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	sub, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Watch([]string{target}, []string{"a.md"}); err != nil {
		t.Fatal(err)
	}
	if got := w.WatchedDirCount(); got != 1 {
		t.Fatalf("watched dirs = %d, want 1", got)
	}

	sub.Close()
	if got := w.WatchedDirCount(); got != 0 {
		t.Fatalf("watched dirs = %d, want 0 after Close", got)
	}
}

// Watch replaces the set wholesale; directories that fall out of it must be
// released, and ones that stay must keep working without a gap.
func TestWatchReplacesPreviousSet(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	fileA := filepath.Join(dirA, "a.md")
	fileB := filepath.Join(dirB, "b.md")
	for _, f := range []string{fileA, fileB} {
		if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := newTestWatcher(t)
	sub := subscribeTo(t, w, []string{fileA}, []string{"a.md"})
	if got := w.WatchedDirCount(); got != 1 {
		t.Fatalf("watched dirs = %d, want 1", got)
	}

	if _, err := sub.Watch([]string{fileB}, []string{"b.md"}); err != nil {
		t.Fatal(err)
	}
	if got := w.WatchedDirCount(); got != 1 {
		t.Fatalf("watched dirs = %d, want 1 after replacement", got)
	}

	if err := os.WriteFile(fileA, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	ev := waitEvent(t, sub)
	if ev.Path != "b.md" {
		t.Fatalf("got %+v, want b.md — a.md was unwatched", ev)
	}
}

// Sibling files in a watched directory must not leak into the event stream:
// the watcher subscribes to the directory for atomic-save reasons, but the
// subscription is still per-file.
func TestWatcherIgnoresUnsubscribedSiblings(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.md")
	sibling := filepath.Join(dir, "b.md")
	for _, f := range []string{target, sibling} {
		if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := newTestWatcher(t)
	sub := subscribeTo(t, w, []string{target}, []string{"a.md"})

	if err := os.WriteFile(sibling, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.Events():
		t.Fatalf("sibling leaked into the stream: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWatchReturnsAcceptedRelativePaths(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "a.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newTestWatcher(t)
	sub, err := w.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	accepted, err := sub.Watch([]string{target}, []string{"sub/a.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0] != "sub/a.md" {
		t.Fatalf("accepted = %v, want [sub/a.md]", accepted)
	}
}
