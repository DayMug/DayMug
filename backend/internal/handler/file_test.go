package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupFileRouter(ms *storetest.Fake) *gin.Engine {
	r := gin.New()
	h := NewFileHandler(ms)
	g := r.Group("/api/users/:id/files")
	g.GET("", h.ListDir)
	g.GET("/inspect", h.InspectFile)
	g.GET("/search", h.Search)
	g.GET("/read", h.ReadFile)
	g.PUT("/write", h.WriteFile)
	g.GET("/download", h.DownloadFile)
	g.GET("/download-zip", h.DownloadZip)
	g.POST("/upload/check", h.CheckUploadConflicts)
	g.POST("/upload", h.Upload)
	g.PUT("/rename", h.Rename)
	g.DELETE("", h.Delete)
	g.POST("/mkdir", h.Mkdir)
	g.PUT("/move", h.Move)
	g.POST("/copy", h.Copy)
	g.POST("/extract", h.Extract)
	g.POST("/compress", h.Compress)
	return r
}

// setupAuthedFileRouter is like setupFileRouter but also installs a fake auth
// middleware that pins the authenticated user id to authedUID. Use it to
// exercise the cross-user / agent ownership checks in getUserFileAccess.
func setupAuthedFileRouter(ms *storetest.Fake, authedUID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	h := NewFileHandler(ms)
	g := r.Group("/api/users/:id/files")
	g.GET("", h.ListDir)
	g.GET("/read", h.ReadFile)
	return r
}

func TestFile_SafePath_TraversalBlocked(t *testing.T) {
	_, err := safePath("/home/user", "../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestFile_SafePath_AbsoluteBlocked(t *testing.T) {
	_, err := safePath("/home/user", "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestFile_SafePath_Valid(t *testing.T) {
	dir := t.TempDir()
	got, err := safePath(dir, "subdir/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(dir, "subdir", "file.txt")
	if got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestFile_ListDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files?path=", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var listing dirListing
	if err := json.NewDecoder(rec.Body).Decode(&listing); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(listing.Entries))
	}
}

func TestFile_ListDir_AgentAccessibleByAuthedUser(t *testing.T) {
	ownerDir := t.TempDir()
	agentDir := filepath.Join(ownerDir, "agent")
	_ = os.Mkdir(agentDir, 0o755)
	_ = os.WriteFile(filepath.Join(agentDir, "hello.txt"), []byte("hi"), 0o644)

	ms := storetest.New()
	// human user with a username + agent (Username == "") owned by the
	// human (matched by shared email — that's the ownership pointer).
	ms.Users = []store.User{
		{ID: "human", Name: "Human", Username: "human", Email: "human@x", WorkDir: ownerDir},
		{ID: "agent", Name: "Agent", Username: "", Email: "human@x", OwnerID: "human", WorkDir: agentDir},
	}
	r := setupAuthedFileRouter(ms, "human")

	req := httptest.NewRequest("GET", "/api/users/agent/files?path=", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_InspectFile_DetectsUnknownExtensionText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.custom"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/inspect?path=notes.custom", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got fileInspection
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.IsText {
		t.Fatalf("expected text file inspection, got %#v", got)
	}
	if got.ContentType == "" {
		t.Fatal("expected content type")
	}
}

func TestFile_InspectFile_DetectsBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob"), []byte{0x00, 0x01, 0x02, 0xff}, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/inspect?path=blob", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got fileInspection
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IsText {
		t.Fatalf("expected binary file inspection, got %#v", got)
	}
}

func TestFile_ListDir_OtherHumanForbidden(t *testing.T) {
	ms := storetest.New()
	// two real human users — neither is an agent
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", WorkDir: t.TempDir()},
		{ID: "bob", Name: "Bob", Username: "bob", WorkDir: t.TempDir()},
	}
	r := setupAuthedFileRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/users/bob/files?path=", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_ListDir_OtherUsersAgentForbidden(t *testing.T) {
	// Even though bob's row has an empty username (and so used to be
	// treated as a "shared" agent), it's bound to bob via email. alice
	// must not be able to list its files.
	bobAgentDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(bobAgentDir, "secret.txt"), []byte("nope"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: t.TempDir()},
		{ID: "bob", Name: "Bob", Username: "bob", Email: "bob@x", WorkDir: t.TempDir()},
		{ID: "bob-agent", Name: "Bob Agent", Username: "", Email: "bob@x", OwnerID: "bob", WorkDir: bobAgentDir},
	}
	r := setupAuthedFileRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/users/bob-agent/files?path=", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (alice -> bob's agent's files), got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_ListDir_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files?path=../../etc", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestFile_ReadFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "test.txt"), []byte("content"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/read?path=test.txt", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "content" {
		t.Errorf("expected 'content', got '%s'", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Errorf("expected private revalidation cache policy, got %q", cc)
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("expected ETag")
	}
}

func TestFile_ReadFile_ETagReturnsNotModified(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "photo.png"), []byte("image-data"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	firstReq := httptest.NewRequest("GET", "/api/users/u1/files/read?path=photo.png", http.NoBody)
	firstRec := httptest.NewRecorder()
	r.ServeHTTP(firstRec, firstReq)
	etag := firstRec.Header().Get("ETag")
	if firstRec.Code != http.StatusOK || etag == "" {
		t.Fatalf("initial response: status=%d etag=%q", firstRec.Code, etag)
	}

	cachedReq := httptest.NewRequest("GET", "/api/users/u1/files/read?path=photo.png", http.NoBody)
	cachedReq.Header.Set("If-None-Match", etag)
	cachedRec := httptest.NewRecorder()
	r.ServeHTTP(cachedRec, cachedReq)

	if cachedRec.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d: %s", cachedRec.Code, cachedRec.Body.String())
	}
	if cachedRec.Body.Len() != 0 {
		t.Fatalf("expected empty 304 body, got %q", cachedRec.Body.String())
	}
}

func TestFile_ReadFile_ETagChangesForSameSecondRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preview.png")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	baseTime := time.Unix(1_700_000_000, 100)
	if err := os.Chtimes(path, baseTime, baseTime); err != nil {
		t.Fatalf("set initial mtime: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	firstReq := httptest.NewRequest("GET", "/api/users/u1/files/read?path=preview.png", http.NoBody)
	firstRec := httptest.NewRecorder()
	r.ServeHTTP(firstRec, firstReq)
	firstETag := firstRec.Header().Get("ETag")

	if err := os.WriteFile(path, []byte("after!"), 0o644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	nextTime := baseTime.Add(100 * time.Nanosecond)
	if err := os.Chtimes(path, nextTime, nextTime); err != nil {
		t.Fatalf("set rewritten mtime: %v", err)
	}

	revalidateReq := httptest.NewRequest("GET", "/api/users/u1/files/read?path=preview.png", http.NoBody)
	revalidateReq.Header.Set("If-None-Match", firstETag)
	revalidateRec := httptest.NewRecorder()
	r.ServeHTTP(revalidateRec, revalidateReq)

	if revalidateRec.Code != http.StatusOK {
		t.Fatalf("expected changed file to return 200, got %d", revalidateRec.Code)
	}
	if revalidateRec.Body.String() != "after!" {
		t.Fatalf("expected rewritten body, got %q", revalidateRec.Body.String())
	}
	if got := revalidateRec.Header().Get("ETag"); got == "" || got == firstETag {
		t.Fatalf("expected a new ETag, before=%q after=%q", firstETag, got)
	}
}

func TestFile_Download(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "dl.txt"), []byte("data"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/download?path=dl.txt", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	cd := rec.Header().Get("Content-Disposition")
	if cd == "" {
		t.Error("expected Content-Disposition header")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Errorf("expected private revalidation cache policy, got %q", cc)
	}
	if etag := rec.Header().Get("ETag"); etag == "" {
		t.Error("expected ETag")
	}
}

func TestFile_Delete(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "todelete.txt"), []byte("bye"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/users/u1/files?path=todelete.txt", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(dir, "todelete.txt")); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestFile_Delete_Root_Blocked(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/users/u1/files?path=", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestFile_Rename_Success(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "old.txt"), []byte("data"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"old_path":"old.txt","new_path":"new.txt"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Error("expected old file to be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err != nil {
		t.Error("expected new file to exist")
	}
}

func TestFile_Rename_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("data"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"old_path":"a.txt","new_path":"../../evil.txt"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestFile_Rename_SourceNotFound(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"old_path":"missing.txt","new_path":"new.txt"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Rename_DestExists(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"old_path":"a.txt","new_path":"b.txt"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Rename_RootBlocked(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"old_path":".","new_path":"new"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_UserNotFound(t *testing.T) {
	ms := storetest.New()
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/missing/files?path=", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- Mkdir tests ---

func TestFile_Mkdir_Success(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"newdir"}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/mkdir", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(filepath.Join(dir, "newdir")); err != nil || !info.IsDir() {
		t.Error("expected newdir to exist as a directory")
	}
}

func TestFile_Mkdir_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "existing"), 0o755)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"existing"}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/mkdir", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Mkdir_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"../../evil"}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/mkdir", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Move tests ---

func TestFile_Move_Success(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"file.txt","dst_path":"subdir"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); !os.IsNotExist(err) {
		t.Error("expected original file to be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "subdir", "file.txt")); err != nil {
		t.Error("expected file to exist in subdir")
	}
}

func TestFile_Move_SrcNotFound(t *testing.T) {
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"missing.txt","dst_path":"subdir"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Move_ConflictDefault(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("new"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "subdir", "file.txt"), []byte("old"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"file.txt","dst_path":"subdir"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	// Source still present, destination unchanged
	if data, _ := os.ReadFile(filepath.Join(dir, "file.txt")); string(data) != "new" {
		t.Error("expected source to remain")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "subdir", "file.txt")); string(data) != "old" {
		t.Error("expected destination to be untouched")
	}
}

func TestFile_Move_ConflictOverwrite(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("new"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "subdir", "file.txt"), []byte("old"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"file.txt","dst_path":"subdir","on_conflict":"overwrite"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); !os.IsNotExist(err) {
		t.Error("expected source to be gone")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "subdir", "file.txt")); string(data) != "new" {
		t.Errorf("expected destination to be overwritten, got %q", string(data))
	}
}

func TestFile_Move_ConflictRename(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("new"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "subdir", "file.txt"), []byte("old"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"file.txt","dst_path":"subdir","on_conflict":"rename"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); !os.IsNotExist(err) {
		t.Error("expected source to be gone")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "subdir", "file.txt")); string(data) != "old" {
		t.Errorf("expected pre-existing destination to be preserved, got %q", string(data))
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "subdir", "file (copy).txt")); string(data) != "new" {
		t.Errorf("expected source to be moved with deduplicated name, got %q", string(data))
	}
}

func TestFile_Move_ConflictInvalidValue(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"file.txt","dst_path":"subdir","on_conflict":"bogus"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Move_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"../../evil","dst_path":"."}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/move", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Copy tests ---

func TestFile_Copy_Success(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o755)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"file.txt","dst_path":"subdir"}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/copy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	// Original still exists
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); err != nil {
		t.Error("expected original file to still exist")
	}
	// Copy exists in subdir
	if _, err := os.Stat(filepath.Join(dir, "subdir", "file.txt")); err != nil {
		t.Error("expected copied file to exist in subdir")
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["path"] == "" {
		t.Error("expected non-empty path in response")
	}
}

func TestFile_Copy_Dir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "srcdir")
	_ = os.Mkdir(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "inner.txt"), []byte("content"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "dstdir"), 0o755)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"srcdir","dst_path":"dstdir"}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/copy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	copied := filepath.Join(dir, "dstdir", "srcdir", "inner.txt")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("expected copied inner.txt to exist: %v", err)
	}
	if string(data) != "content" {
		t.Errorf("expected 'content', got '%s'", string(data))
	}
}

func TestFile_Copy_Dedup(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	// Copy file to same dir — dedupName should produce "file (copy).txt"
	body := `{"src_path":"file.txt","dst_path":"."}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/copy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "file (copy).txt")); err != nil {
		t.Error("expected 'file (copy).txt' to exist")
	}

	// Copy again — should produce "file (copy 2).txt"
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/users/u1/files/copy", strings.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected 201 on second copy, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "file (copy 2).txt")); err != nil {
		t.Error("expected 'file (copy 2).txt' to exist")
	}
}

func TestFile_Copy_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"src_path":"../../evil","dst_path":"."}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/copy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_WriteFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"note.md","content":"new content"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "new content" {
		t.Fatalf("content mismatch: %q", got)
	}
	// Mode must survive the temp-file + rename round-trip; the editor
	// silently changing 0600 to 0644 would surprise the user.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed: got %o, want 0600", info.Mode().Perm())
	}
}

func TestFile_WriteFile_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"fresh.txt","content":"hello"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "fresh.txt"))
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content mismatch: %q", got)
	}
}

func TestFile_WriteFile_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"sub","content":"x"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_WriteFile_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"path":"../escape.txt","content":"x"}`
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_WriteFile_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	huge := strings.Repeat("a", maxEditableFileBytes+1)
	payload, _ := json.Marshal(map[string]string{"path": "big.txt", "content": huge})
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

// readETag seeds a file, reads it back through the handler, and returns the
// validator the editor would have been holding.
func readETag(t *testing.T, r *gin.Engine, relPath string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/users/u1/files/read?path="+relPath, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("read for etag: %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("read returned no ETag")
	}
	return etag
}

func writeWithIfMatch(r *gin.Engine, body, ifMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestFile_WriteFile_IfMatchCurrentSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	rec := writeWithIfMatch(r, `{"path":"note.md","content":"mine"}`, readETag(t, r, "note.md"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "mine" {
		t.Fatalf("content mismatch: %q", got)
	}
	// The response must carry the post-write validator, otherwise a second
	// save from the same buffer would fail its own precondition.
	var body struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ETag == "" {
		t.Fatal("write response carried no etag")
	}
	rec2 := writeWithIfMatch(r, `{"path":"note.md","content":"again"}`, body.ETag)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second save with returned etag: got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// The whole point of the lock: an agent rewrites the file while a human has
// it open, and the human's save must not silently discard that rewrite.
func TestFile_WriteFile_IfMatchStaleRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	stale := readETag(t, r, "note.md")
	// Size differs, so the validator changes even if the clock is coarse.
	if err := os.WriteFile(path, []byte("agent wrote this"), 0o644); err != nil {
		t.Fatalf("agent write: %v", err)
	}

	rec := writeWithIfMatch(r, `{"path":"note.md","content":"human"}`, stale)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "agent wrote this" {
		t.Fatalf("stale save clobbered the file: %q", got)
	}
	// The current validator comes back so the client can re-sync without a
	// second round-trip.
	var body struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ETag == "" || body.ETag == stale {
		t.Fatalf("expected a fresh etag in the 412 body, got %q", body.ETag)
	}
}

func TestFile_WriteFile_NoIfMatchStillOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	if rec := writeWithIfMatch(r, `{"path":"note.md","content":"forced"}`, ""); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "forced" {
		t.Fatalf("content mismatch: %q", got)
	}
}

func TestFile_WriteFile_IfMatchOnDeletedFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	stale := readETag(t, r, "note.md")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	rec := writeWithIfMatch(r, `{"path":"note.md","content":"resurrect"}`, stale)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale save recreated a deleted file")
	}
}

func TestFile_EtagMatches(t *testing.T) {
	const current = `W/"1a2b-10"`
	tests := []struct {
		name    string
		ifMatch string
		want    bool
	}{
		{"exact", `W/"1a2b-10"`, true},
		{"weak prefix stripped by client", `"1a2b-10"`, true},
		{"wildcard", "*", true},
		{"list containing current", `W/"dead-1", W/"1a2b-10"`, true},
		{"stale", `W/"dead-1"`, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := etagMatches(tt.ifMatch, current); got != tt.want {
				t.Fatalf("etagMatches(%q) = %v, want %v", tt.ifMatch, got, tt.want)
			}
		})
	}
}
