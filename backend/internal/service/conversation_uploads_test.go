package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// uploadsFixture is one agent whose owner's home holds its scope, with
// conversations whose messages attach files under it.
type uploadsFixture struct {
	ms    *storetest.Fake
	home  string
	scope string
}

func newUploadsFixture(t *testing.T) uploadsFixture {
	t.Helper()
	prev := runUploadCleanup
	runUploadCleanup = func(fn func()) { fn() }
	t.Cleanup(func() { runUploadCleanup = prev })
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: home},
		{ID: "agent-1", OwnerID: "u1", Email: "alice@example.com"},
	}
	scope, err := AgentScopeDir("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	return uploadsFixture{ms: ms, home: home, scope: scope}
}

// file writes rel (home-relative, slash-separated) and returns its abs path.
func (f uploadsFixture) file(t *testing.T, rel string) string {
	t.Helper()
	abs := DaymugPath(f.home, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func (f uploadsFixture) conv(t *testing.T, id string, updated time.Time, paths ...string) {
	t.Helper()
	f.ms.Conversations = append(f.ms.Conversations, store.Conversation{ID: id, UserID: "agent-1", UpdatedAt: updated})
	attachments := make([]map[string]string, 0, len(paths))
	for _, p := range paths {
		attachments = append(attachments, map[string]string{"path": p})
	}
	meta, err := json.Marshal(map[string]any{"attachments": attachments})
	if err != nil {
		t.Fatal(err)
	}
	f.ms.Messages[id] = append(f.ms.Messages[id], store.Message{ID: id + "-m", ConversationID: id, Role: "user", Metadata: meta})
}

func exists(t *testing.T, abs string) bool {
	t.Helper()
	_, err := os.Lstat(abs)
	return err == nil
}

func TestConversationOps_Delete_RemovesOnlyItsOwnUploads(t *testing.T) {
	f := newUploadsFixture(t)
	own := f.scope + "/uploads/20260101-own.png"
	shared := f.scope + "/uploads/20260101-shared.png"
	im := f.scope + "/slack/20260101-im.png"
	ownAbs, sharedAbs, imAbs := f.file(t, own), f.file(t, shared), f.file(t, im)
	f.conv(t, "c1", time.Now(), own, shared, im)
	f.conv(t, "c2", time.Now(), shared)

	ops := &ConversationOps{Store: f.ms}
	if err := ops.Delete(context.Background(), "c1", "u1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if exists(t, ownAbs) || exists(t, imAbs) {
		t.Fatalf("own upload or IM attachment survived the delete")
	}
	if !exists(t, sharedAbs) {
		t.Fatalf("upload still referenced by a live conversation was deleted")
	}
}

func TestConversationOps_DeleteStale_RemovesUploads(t *testing.T) {
	f := newUploadsFixture(t)
	stale := f.scope + "/uploads/20260101-stale.png"
	fresh := f.scope + "/uploads/20260101-fresh.png"
	staleAbs, freshAbs := f.file(t, stale), f.file(t, fresh)
	f.conv(t, "old", time.Now().Add(-30*24*time.Hour), stale)
	f.conv(t, "new", time.Now(), fresh)

	ops := &ConversationOps{Store: f.ms}
	ids, err := ops.DeleteStale(context.Background(), "agent-1", "u1", 14*24*time.Hour)
	if err != nil {
		t.Fatalf("delete stale: %v", err)
	}
	if len(ids) != 1 || ids[0] != "old" {
		t.Fatalf("deleted %v, want [old]", ids)
	}
	if exists(t, staleAbs) {
		t.Fatalf("stale conversation's upload survived")
	}
	if !exists(t, freshAbs) {
		t.Fatalf("live conversation's upload was deleted")
	}
}

// Recorded paths come from persisted metadata, so anything that does not sit
// directly in one of the agent's attachment directories must be left alone.
func TestRemoveConversationUploads_IgnoresPathsOutsideAttachmentDirs(t *testing.T) {
	f := newUploadsFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := f.scope + "/uploads/link.txt"
	if err := os.MkdirAll(DaymugPath(f.home, f.scope+"/uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, DaymugPath(f.home, link)); err != nil {
		t.Fatal(err)
	}
	kept := map[string]string{
		"case file":      f.file(t, f.scope+"/case/C1-T1.md"),
		"project file":   f.file(t, "project/main.go"),
		"nested upload":  f.file(t, f.scope+"/uploads/sub/deep.png"),
		"other agent":    f.file(t, ".daymug/agents/other/uploads/a.png"),
		"symlink target": outside,
	}
	f.conv(t, "c1", time.Now(),
		f.scope+"/case/C1-T1.md",
		"project/main.go",
		f.scope+"/uploads/sub/deep.png",
		".daymug/agents/other/uploads/a.png",
		f.scope+"/uploads/../../other/uploads/a.png",
		link,
	)
	if err := f.ms.DeleteConversation(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	if n := RemoveConversationUploads(context.Background(), f.ms, store.ConversationRef{ID: "c1", UserID: "agent-1"}); n != 0 {
		t.Fatalf("removed %d file(s), want 0", n)
	}
	for name, abs := range kept {
		if !exists(t, abs) {
			t.Errorf("%s was deleted", name)
		}
	}
	if !exists(t, DaymugPath(f.home, link)) {
		t.Errorf("symlink pointing outside the scope was unlinked")
	}
}
