package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/service"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupUploadRouter(t *testing.T, ms *storetest.Fake, callerID string, maxBytes int64) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", callerID)
		c.Set("auth_is_admin", false)
		c.Next()
	})
	h := NewUploadHandler(ms)
	h.MaxBytes = maxBytes
	r.POST("/api/uploads", h.Create)
	return r
}

// uploadsDirFor is where an agent's web uploads must land: inside its scope
// under the owning human's home, never under the conversation's own cwd.
func uploadsDirFor(t *testing.T, homeRoot, agentID string) string {
	t.Helper()
	rel, err := service.AgentUploadsDir(agentID)
	if err != nil {
		t.Fatalf("AgentUploadsDir: %v", err)
	}
	return service.DaymugPath(homeRoot, rel)
}

// pngBytes returns a minimal valid PNG so the http.DetectContentType sniffer
// classifies the upload as image/png. Anything smaller than the PNG signature
// (8 bytes) gets misidentified, so we hand-roll a 1x1 white image.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Pix[0], img.Pix[1], img.Pix[2], img.Pix[3] = 0xff, 0xff, 0xff, 0xff
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func multipartBody(t *testing.T, fieldName, filename string, content []byte, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	fw, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	_ = w.Close()
	return &buf, w.FormDataContentType()
}

// TestUpload_WritesUnderTheAgentScope is the happy path: the file lands in the
// conversation agent's scope under the owning human's home — not in the
// project the conversation is running in — and the API returns both the
// absolute ref the composer embeds and the home-root-relative path the file
// API reads back.
func TestUpload_WritesUnderTheAgentScope(t *testing.T) {
	homeRoot := t.TempDir()
	convDir := filepath.Join(homeRoot, "project")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
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

	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Ref  string `json:"ref"`
		Path string `json:"path"`
		URL  string `json:"url"`
		Name string `json:"name"`
		Mime string `json:"mime"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	uploadsDir := uploadsDirFor(t, homeRoot, "u1")
	if filepath.Dir(resp.Ref) != uploadsDir {
		t.Errorf("ref = %q, want a file in %q", resp.Ref, uploadsDir)
	}
	// The ref is what the composer pastes into the message, so it has to be a
	// path the agent can open from any cwd.
	if !filepath.IsAbs(resp.Ref) {
		t.Errorf("ref %q is relative; the agent's cwd is not the uploads dir", resp.Ref)
	}
	wantRel, err := service.AgentUploadsDir("u1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Path, wantRel+"/") {
		t.Errorf("path = %q, want it under %q", resp.Path, wantRel)
	}
	if !strings.Contains(resp.URL, "/api/users/u1/files/read?path=") {
		t.Errorf("url = %q, want it addressed to the home owner", resp.URL)
	}
	if resp.Mime != "image/png" {
		t.Errorf("mime: got %q want image/png", resp.Mime)
	}
	if resp.Name != "screenshot.png" {
		t.Errorf("name: got %q want screenshot.png", resp.Name)
	}
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file under %s, got %d", uploadsDir, len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), "-screenshot.png") {
		t.Errorf("name should end with '-screenshot.png', got %q", entries[0].Name())
	}
	if _, err := os.Stat(filepath.Join(convDir, service.DaymugDir)); !os.IsNotExist(err) {
		t.Errorf("a .daymug directory appeared in the project dir (stat err %v)", err)
	}
}

// TestUpload_AgentConversationUsesTheOwnerHome covers a conversation owned by
// an agent persona whose work_dir is a project below its human's home: the
// bytes still belong in the human's home, under the agent's own scope, and the
// read URL must address the human rather than the agent — the file API resolves
// a scope path against the home root, not against the agent's narrower dir.
func TestUpload_AgentConversationUsesTheOwnerHome(t *testing.T) {
	homeRoot := t.TempDir()
	agentDir := filepath.Join(homeRoot, "project")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ms := storetest.New()
	human := store.User{ID: "owner-1", Username: "alice", Email: "alice@example.com", WorkDir: homeRoot}
	agent := store.User{ID: "agent-1", Email: "alice@example.com", Name: "agent", OwnerID: "owner-1", WorkDir: agentDir}
	if err := ms.CreateUser(context.Background(), human); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if err := ms.CreateUser(context.Background(), agent); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// Conversation owned by the agent persona, no locked work_dir yet.
	if err := ms.CreateConversation(context.Background(), "conv-1", "t", "agent-1", "", "", ""); err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	r := setupUploadRouter(t, ms, "owner-1", 0)

	body, ct := multipartBody(t, "file", "shot.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(uploadsDirFor(t, homeRoot, "agent-1")); err != nil {
		t.Errorf("expected the file under the agent scope in the owner home: %v", err)
	}
	var resp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.URL, "/api/users/owner-1/files/read?path=") {
		t.Errorf("url = %q, want it addressed to the owning human", resp.URL)
	}
}

// An agent whose owner cannot be resolved has no home to write into, and must
// not quietly fall back to its own work_dir where no read path looks.
func TestUpload_OrphanAgentRefused(t *testing.T) {
	ms := storetest.New()
	human := store.User{ID: "owner-1", Username: "alice", Email: "alice@example.com", WorkDir: t.TempDir()}
	orphan := store.User{ID: "agent-1", Email: "ghost@example.com", Name: "agent", WorkDir: t.TempDir()}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateUser(context.Background(), orphan)
	_ = ms.CreateConversation(context.Background(), "conv-1", "t", "agent-1", "", "", "")
	r := setupUploadRouter(t, ms, "owner-1", 0)

	body, ct := multipartBody(t, "file", "shot.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusCreated {
		t.Fatalf("expected the orphan agent's upload to be refused, got 201 body=%s", w.Body.String())
	}
}

// TestUpload_RejectsMissingConversationID checks the boundary: without
// conversation_id the handler has no way to pick a target dir and must
// refuse rather than silently routing to a default.
func TestUpload_RejectsMissingConversationID(t *testing.T) {
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: t.TempDir()}
	_ = ms.CreateUser(context.Background(), human)
	r := setupUploadRouter(t, ms, "u1", 0)

	body, ct := multipartBody(t, "file", "x.png", pngBytes(t), nil)
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing conversation_id, got %d", w.Code)
	}
}

// TestUpload_RejectsCrossUserConversation ensures the access check fires:
// uploading to a conversation owned by an unrelated human (no shared owner)
// must not write anywhere.
func TestUpload_RejectsCrossUserConversation(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	alice := store.User{ID: "alice", Username: "alice", Email: "alice@example.com", WorkDir: dir}
	bob := store.User{ID: "bob", Username: "bob", Email: "bob@example.com", WorkDir: dir}
	_ = ms.CreateUser(context.Background(), alice)
	_ = ms.CreateUser(context.Background(), bob)
	if err := ms.CreateConversation(context.Background(), "conv-bob", "t", "bob", dir, "", ""); err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	r := setupUploadRouter(t, ms, "alice", 0)

	body, ct := multipartBody(t, "file", "x.png", pngBytes(t), map[string]string{"conversation_id": "conv-bob"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-user upload, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestUpload_AcceptsArbitraryFile covers the chat-composer attachment
// flow: the endpoint takes any MIME (PDFs, logs, archives, source files,
// …) and stores it under the conversation's cwd alongside images. Path
// safety still comes from the filename sanitiser and the conversation
// access check — type isn't a security boundary here.
func TestUpload_AcceptsArbitraryFile(t *testing.T) {
	homeRoot := t.TempDir()
	convDir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: homeRoot}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateConversation(context.Background(), "conv-1", "t", "u1", convDir, "", "")
	r := setupUploadRouter(t, ms, "u1", 0)

	// 600 bytes of plain text — well above the 512-byte sniff window so
	// the detector classifies it as text/plain.
	body, ct := multipartBody(t, "file", "notes.txt", bytes.Repeat([]byte("hello world\n"), 60), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Ref  string `json:"ref"`
		Name string `json:"name"`
		Mime string `json:"mime"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Mime, "text/plain") {
		t.Errorf("mime should be text/plain*, got %q", resp.Mime)
	}
	if resp.Name != "notes.txt" {
		t.Errorf("name: got %q want notes.txt", resp.Name)
	}
	uploadsDir := uploadsDirFor(t, homeRoot, "u1")
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-notes.txt") {
		t.Fatalf("expected one *-notes.txt file under %s, got %v", uploadsDir, entries)
	}
}

// TestUpload_PreservesExtensionForCJKFilename guards the fallback path
// where the entire stem of the filename is non-ASCII (Chinese clipboard
// paste, etc.). The original extension should survive even though the
// stem gets stripped by the charset filter.
func TestUpload_PreservesExtensionForCJKFilename(t *testing.T) {
	homeRoot := t.TempDir()
	convDir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: homeRoot}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateConversation(context.Background(), "conv-1", "t", "u1", convDir, "", "")
	r := setupUploadRouter(t, ms, "u1", 0)

	body, ct := multipartBody(t, "file", "图片.pdf", []byte("%PDF-1.4\n%\xc7\xec\xcf\xa7\n1 0 obj\n<<>>\nendobj\n"), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(uploadsDirFor(t, homeRoot, "u1"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("read dir: %v entries=%v", err, entries)
	}
	if !strings.HasSuffix(entries[0].Name(), ".pdf") {
		t.Errorf("expected .pdf suffix on %q", entries[0].Name())
	}
}

// TestUpload_RejectsOversize ensures MaxBytes is honoured. The
// http.MaxBytesReader wraps the request body, so an over-cap upload is
// rejected during multipart parsing — not after the file is fully read.
func TestUpload_RejectsOversize(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: dir}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateConversation(context.Background(), "conv-1", "t", "u1", dir, "", "")
	r := setupUploadRouter(t, ms, "u1", 64) // 64 bytes is well under the PNG header

	body, ct := multipartBody(t, "file", "big.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusCreated {
		t.Fatalf("expected non-201 for oversize upload, got 201 body=%s", w.Body.String())
	}
}

// TestUpload_NoDefaultCap: chat attachments are not size-limited, so a file
// above the old 25 MiB cap lands when MaxBytes is unset.
func TestUpload_NoDefaultCap(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: dir}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateConversation(context.Background(), "conv-1", "t", "u1", dir, "", "")
	r := setupUploadRouter(t, ms, "u1", 0)

	big := make([]byte, service.DefaultUploadMaxBytes+1)
	body, ct := multipartBody(t, "file", "big.bin", big, map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 for large upload, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestUpload_LocksConversationWorkDir checks the upload-side lock: when
// a conversation arrives with no work_dir set, a successful upload
// persists the cwd it landed in. Without this an unlocked conversation
// could have its dir switched between upload and the first message,
// stranding the just-written attachment under the old directory.
func TestUpload_LocksConversationWorkDir(t *testing.T) {
	ownerDir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: ownerDir}
	_ = ms.CreateUser(context.Background(), human)
	// Empty work_dir — the upload handler should fall back to owner.WorkDir
	// AND persist that choice on conv.work_dir.
	if err := ms.CreateConversation(context.Background(), "conv-1", "t", "u1", "", "", ""); err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	r := setupUploadRouter(t, ms, "u1", 0)

	body, ct := multipartBody(t, "file", "shot.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	conv, err := ms.GetConversation(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("get conv: %v", err)
	}
	if conv.WorkDir != ownerDir {
		t.Errorf("conv.work_dir not locked: got %q want %q", conv.WorkDir, ownerDir)
	}

	var resp struct {
		WorkDir       string `json:"work_dir"`
		WorkDirLocked bool   `json:"work_dir_locked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WorkDir != ownerDir {
		t.Errorf("response work_dir: got %q want %q", resp.WorkDir, ownerDir)
	}
	if !resp.WorkDirLocked {
		t.Errorf("response work_dir_locked should be true")
	}
}

// TestUpload_PreservesLockedWorkDir guards against the upload handler
// rewriting an already-locked conv.work_dir. Once a conversation has its cwd
// set (by an earlier upload or the first message), an upload must leave it
// alone — switching it mid-conversation would move the agent to a different
// project between one message and the next.
func TestUpload_PreservesLockedWorkDir(t *testing.T) {
	convDir := t.TempDir()
	ownerDir := t.TempDir() // intentionally different
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: ownerDir}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateConversation(context.Background(), "conv-1", "t", "u1", convDir, "", "")
	r := setupUploadRouter(t, ms, "u1", 0)

	body, ct := multipartBody(t, "file", "shot.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	conv, _ := ms.GetConversation(context.Background(), "conv-1")
	if conv.WorkDir != convDir {
		t.Errorf("conv.work_dir should remain %q, got %q", convDir, conv.WorkDir)
	}
	if _, err := os.Stat(uploadsDirFor(t, ownerDir, "u1")); err != nil {
		t.Errorf("upload should have landed in the agent scope under the owner home: %v", err)
	}
}

// TestUpload_RequiresAuth guards against accidentally exposing the
// upload endpoint to anonymous traffic — without a session, every
// upload would be a path-disclosure on the host filesystem.
func TestUpload_RequiresAuth(t *testing.T) {
	ms := storetest.New()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewUploadHandler(ms)
	r.POST("/api/uploads", h.Create)

	body, ct := multipartBody(t, "file", "x.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
	req := httptest.NewRequest("POST", "/api/uploads", body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthed upload, got %d", w.Code)
	}
}

// TestUpload_TimestampNamingAndCollision exercises the YYYYMMDDHHMMSS-<base>
// scheme: a fresh upload gets the timestamp prefix, and two pastes that hit
// the same second resolve via the "-N" suffix instead of overwriting.
func TestUpload_TimestampNamingAndCollision(t *testing.T) {
	homeRoot := t.TempDir()
	convDir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", WorkDir: homeRoot}
	if err := ms.CreateUser(context.Background(), human); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := ms.CreateConversation(context.Background(), "conv-1", "t", "u1", convDir, "", ""); err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	r := setupUploadRouter(t, ms, "u1", 0)

	post := func() string {
		body, ct := multipartBody(t, "file", "shot.png", pngBytes(t), map[string]string{"conversation_id": "conv-1"})
		req := httptest.NewRequest("POST", "/api/uploads", body)
		req.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp.Ref
	}

	first := post()
	second := post()
	if first == second {
		t.Fatalf("collision should have produced a distinct name; both = %q", first)
	}

	entries, err := os.ReadDir(uploadsDirFor(t, homeRoot, "u1"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 files, got %d", len(entries))
	}
	// Each file name must start with a 14-digit timestamp and end with
	// the original basename (or the suffixed variant).
	tsRe := regexp.MustCompile(`^\d{14}-`)
	for _, e := range entries {
		if !tsRe.MatchString(e.Name()) {
			t.Errorf("name %q lacks YYYYMMDDHHMMSS- prefix", e.Name())
		}
	}
}

// helper to keep golangci-lint quiet about unused imports across test files
// — io.Discard is referenced when running with -race so the multipart parser
// can be drained in failure paths.
var _ = io.Discard
