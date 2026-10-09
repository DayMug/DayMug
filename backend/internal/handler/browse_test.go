package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupBrowseRouter() *gin.Engine {
	r := gin.New()
	// No store / no auth context: the handler falls back to legacy
	// "show $HOME" behavior, which is what the existing fixtures expect.
	h := NewBrowseHandler(nil)
	r.GET("/api/browse-dirs", h.BrowseDirs)
	r.POST("/api/browse-dirs/mkdir", h.MkdirBrowseDir)
	return r
}

// setupAuthedBrowseRouter wires the handler with a fake auth middleware
// pinning the authenticated user id. Used to exercise the home-jail —
// every browse / mkdir is rejected unless the path is within the caller's
// work_dir.
func setupAuthedBrowseRouter(s *storetest.Fake, authedUID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	h := NewBrowseHandler(s)
	r.GET("/api/browse-dirs", h.BrowseDirs)
	r.POST("/api/browse-dirs/mkdir", h.MkdirBrowseDir)
	return r
}

func TestBrowseDirs_DefaultPath(t *testing.T) {
	r := setupBrowseRouter()
	req := httptest.NewRequest("GET", "/api/browse-dirs", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result browseResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Current == "" {
		t.Error("expected non-empty current path")
	}
}

func TestBrowseDirs_SpecificPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "myproject")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Also create a file to ensure it's excluded
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	r := setupBrowseRouter()
	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+dir, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result browseResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Current != dir {
		t.Errorf("expected current=%q, got %q", dir, result.Current)
	}
	if len(result.Dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(result.Dirs))
	}
	if result.Dirs[0].Name != "myproject" {
		t.Errorf("expected dir name 'myproject', got %q", result.Dirs[0].Name)
	}
}

func TestBrowseDirs_HiddenExcluded(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".hidden"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "visible"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	r := setupBrowseRouter()
	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+dir, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var result browseResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if len(result.Dirs) != 1 || result.Dirs[0].Name != "visible" {
		t.Errorf("expected only 'visible', got %v", result.Dirs)
	}
}

func TestBrowseDirs_NotFound(t *testing.T) {
	r := setupBrowseRouter()
	req := httptest.NewRequest("GET", "/api/browse-dirs?path=/nonexistent_path_12345", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestBrowseDirs_ParentPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "child")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	r := setupBrowseRouter()
	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+sub, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var result browseResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result.Parent != dir {
		t.Errorf("expected parent=%q, got %q", dir, result.Parent)
	}
}

func postJSON(t *testing.T, r http.Handler, url string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", url, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestMkdirBrowseDir_Creates(t *testing.T) {
	dir := t.TempDir()
	r := setupBrowseRouter()

	rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{"path": dir, "name": "newproj"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var entry browseEntry
	if err := json.NewDecoder(rec.Body).Decode(&entry); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := filepath.Join(dir, "newproj")
	if entry.Path != want {
		t.Errorf("expected path=%q, got %q", want, entry.Path)
	}
	if entry.Name != "newproj" {
		t.Errorf("expected name=newproj, got %q", entry.Name)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat new dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("created entry is not a directory")
	}
}

func TestMkdirBrowseDir_Conflict(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "exists"), 0o755); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}
	r := setupBrowseRouter()

	rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{"path": dir, "name": "exists"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMkdirBrowseDir_RejectsBadName(t *testing.T) {
	dir := t.TempDir()
	r := setupBrowseRouter()

	cases := []string{"", ".", "..", ".secret", "child/grandchild", `back\slash`}
	for _, name := range cases {
		rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{"path": dir, "name": name})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name=%q: expected 400, got %d", name, rec.Code)
		}
	}
}

func TestMkdirBrowseDir_ParentMissing(t *testing.T) {
	r := setupBrowseRouter()
	rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{
		"path": "/nonexistent_path_12345/never",
		"name": "x",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestMkdirBrowseDir_ParentNotDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	r := setupBrowseRouter()
	rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{"path": file, "name": "child"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBrowseDirs_AuthedDefaultsToHome — with auth, the empty-path query
// lands on the caller's work_dir, not the server user's $HOME.
func TestBrowseDirs_AuthedDefaultsToHome(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "child"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "alice", Username: "alice", WorkDir: home}}
	r := setupAuthedBrowseRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/browse-dirs", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result browseResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Current != home {
		t.Errorf("expected current=%q, got %q", home, result.Current)
	}
	// Already at root — backend clears parent so the UI can disable Up.
	if result.Parent != "" {
		t.Errorf("expected empty parent at root, got %q", result.Parent)
	}
}

// TestBrowseDirs_AuthedClearsParentAtRoot — even when a path query is
// supplied, walking the listing whose parent is above the home jail
// must report parent="" so the UI's Up button can stop there.
func TestBrowseDirs_AuthedClearsParentAtRoot(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "alice", Username: "alice", WorkDir: home}}
	r := setupAuthedBrowseRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+home, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var result browseResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result.Parent != "" {
		t.Errorf("parent should be empty at jail root, got %q", result.Parent)
	}
}

// TestBrowseDirs_AuthedRefusesOutsideHome — explicit paths outside the
// caller's work_dir are rejected, including prefix-sibling paths.
func TestBrowseDirs_AuthedRefusesOutsideHome(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir() // outside `home`
	ms := storetest.New()
	ms.Users = []store.User{{ID: "alice", Username: "alice", WorkDir: home}}
	r := setupAuthedBrowseRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+other, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for outside-jail path, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestBrowseDirs_AuthedAllowsSubdir — paths inside the home are fine and
// expose their parent so the user can navigate back.
func TestBrowseDirs_AuthedAllowsSubdir(t *testing.T) {
	home := t.TempDir()
	sub := filepath.Join(home, "proj")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "alice", Username: "alice", WorkDir: home}}
	r := setupAuthedBrowseRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+sub, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var result browseResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result.Current != sub {
		t.Errorf("current=%q want %q", result.Current, sub)
	}
	// Parent is the home root — caller is still inside the jail.
	if result.Parent != home {
		t.Errorf("parent=%q want %q", result.Parent, home)
	}
}

// TestMkdirBrowseDir_AuthedRefusesOutsideHome — same jail applies to the
// "+ new folder" button on the picker.
func TestMkdirBrowseDir_AuthedRefusesOutsideHome(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "alice", Username: "alice", WorkDir: home}}
	r := setupAuthedBrowseRouter(ms, "alice")

	rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{"path": other, "name": "child"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestBrowseDirs_AdminWalksOutsideHome — admins assign other users'
// work_dirs, which can live anywhere, so their picker is not jailed.
func TestBrowseDirs_AdminWalksOutsideHome(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "root", Username: "root", WorkDir: home, IsAdmin: true}}
	r := setupAuthedBrowseRouter(ms, "root")

	req := httptest.NewRequest("GET", "/api/browse-dirs?path="+other, http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var result browseResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result.Current != other {
		t.Errorf("current=%q want %q", result.Current, other)
	}
	if result.Parent != filepath.Dir(other) {
		t.Errorf("parent=%q want %q", result.Parent, filepath.Dir(other))
	}
}

// TestBrowseDirs_AdminDefaultsToHomeWithParent — the admin picker still
// opens at their own work_dir, but can walk up from it.
func TestBrowseDirs_AdminDefaultsToHomeWithParent(t *testing.T) {
	home := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "root", Username: "root", WorkDir: home, IsAdmin: true}}
	r := setupAuthedBrowseRouter(ms, "root")

	req := httptest.NewRequest("GET", "/api/browse-dirs", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var result browseResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result.Current != home {
		t.Errorf("current=%q want %q", result.Current, home)
	}
	if result.Parent != filepath.Dir(home) {
		t.Errorf("parent=%q want %q", result.Parent, filepath.Dir(home))
	}
}

// TestMkdirBrowseDir_AdminCreatesOutsideHome — the picker's "+ new folder"
// follows the same admin exemption.
func TestMkdirBrowseDir_AdminCreatesOutsideHome(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "root", Username: "root", WorkDir: home, IsAdmin: true}}
	r := setupAuthedBrowseRouter(ms, "root")

	rec := postJSON(t, r, "/api/browse-dirs/mkdir", map[string]string{"path": other, "name": "child"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(filepath.Join(other, "child")); err != nil || !info.IsDir() {
		t.Fatalf("child dir not created: %v", err)
	}
}
