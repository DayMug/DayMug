package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// A symlink planted inside work_dir (by the agent, an extracted archive, or
// a git checkout) used to make the whole host readable through the file API,
// daymug.db and config.yaml included.
func TestFile_SafePath_SymlinkEscapeBlocked(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "daymug.db")
	if err := os.WriteFile(secret, []byte("session tokens"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(workDir, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := safePath(workDir, "./link"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

// The escape can also hide in a directory symlink one level up, which is
// what a write to a not-yet-existing file traverses.
func TestFile_SafePath_SymlinkedParentEscapeBlocked(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workDir, "data")); err != nil {
		t.Fatal(err)
	}

	if _, err := safePath(workDir, "data/new.txt"); err == nil {
		t.Fatal("expected escape through a symlinked parent to be rejected")
	}
}

// A symlink that stays inside work_dir is still legitimate.
func TestFile_SafePath_InternalSymlinkAllowed(t *testing.T) {
	workDir := t.TempDir()
	inner := filepath.Join(workDir, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, filepath.Join(workDir, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := safePath(workDir, "link/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(workDir, "link", "file.txt"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// "..config" is a legal filename; only a leading ".." *segment* escapes.
func TestFile_SafePath_DotDotPrefixedFilenameAllowed(t *testing.T) {
	workDir := t.TempDir()
	got, err := safePath(workDir, "..config")
	if err != nil {
		t.Fatalf("unexpected error for %q: %v", "..config", err)
	}
	if want := filepath.Join(workDir, "..config"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if _, err := safePath(workDir, "sub/..config"); err != nil {
		t.Errorf("unexpected error for nested ..config: %v", err)
	}
}

func TestFile_SafePath_NestedPathAllowed(t *testing.T) {
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := safePath(workDir, "a/b/c.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(workDir, "a", "b", "c.txt"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// End-to-end through the HTTP surface described in docs/architecture/multi-user.md as
// jailing reads to the caller's work_dir.
func TestFile_ReadFile_SymlinkEscapeForbidden(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "config.yaml")
	if err := os.WriteFile(secret, []byte("provider_api_key: sk-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(workDir, "link")); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "U", Username: "u", WorkDir: workDir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/read?path=./link", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "" && rec.Code == http.StatusOK {
		t.Fatalf("secret leaked: %s", body)
	}
}

func TestFile_ReadFile_AgentMayReadOwnerWorkDir(t *testing.T) {
	ownerDir := t.TempDir()
	agentDir := filepath.Join(ownerDir, "Agent-Ada")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownerDir, "MEMORY.md"), []byte("shared memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../MEMORY.md", filepath.Join(agentDir, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "owner", Name: "Owner", Username: "owner", Email: "owner@example.com", WorkDir: ownerDir},
		{ID: "agent", Name: "Agent", Email: "owner@example.com", OwnerID: "owner", WorkDir: agentDir},
	}
	r := setupAuthedFileRouter(ms, "owner")

	for _, path := range []string{"MEMORY.md", "..%2FMEMORY.md"} {
		req := httptest.NewRequest("GET", "/api/users/agent/files/read?path="+path, http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("path %q: expected 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "shared memory" {
			t.Fatalf("path %q: got body %q", path, got)
		}
	}
}

func TestFile_ReadFile_AgentCannotEscapeOwnerWorkDir(t *testing.T) {
	ownerDir := t.TempDir()
	agentDir := filepath.Join(ownerDir, "agent")
	outside := t.TempDir()
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(agentDir, "secret.txt")); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "owner", Name: "Owner", Username: "owner", Email: "owner@example.com", WorkDir: ownerDir},
		{ID: "agent", Name: "Agent", Email: "owner@example.com", OwnerID: "owner", WorkDir: agentDir},
	}
	r := setupAuthedFileRouter(ms, "owner")

	req := httptest.NewRequest("GET", "/api/users/agent/files/read?path=secret.txt", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIsPathWithin_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if isPathWithin(root, link) {
		t.Error("isPathWithin followed a symlink out of the root")
	}
	if !isPathWithin(root, filepath.Join(root, "does-not-exist-yet")) {
		t.Error("isPathWithin rejected a not-yet-created child inside the root")
	}
}

// The web upload path shares the uploads-dir jail escape with the IM ingest
// path: os.MkdirAll accepts an existing symlink, so a link there would
// silently redirect every composer attachment off the home root.
func TestUpload_UploadsDirSymlinkForbidden(t *testing.T) {
	convDir := t.TempDir()
	outside := t.TempDir()
	homeRoot := t.TempDir()
	uploadsRel, err := service.AgentUploadsDir("u1")
	if err != nil {
		t.Fatal(err)
	}
	uploadsDir := service.DaymugPath(homeRoot, uploadsRel)
	if err := os.MkdirAll(filepath.Dir(uploadsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, uploadsDir); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", Name: "Alice", WorkDir: homeRoot}
	if err := ms.CreateUser(context.Background(), human); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := ms.CreateConversation(context.Background(), "conv-1", "t", "u1", convDir, "", ""); err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	r := setupUploadRouter(t, ms, "u1", 0)

	body, ct := multipartBody(t, "file", "screenshot.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("upload leaked outside the home root: %v", entries)
	}
}
