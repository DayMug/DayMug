package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestFile_Upload(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "uploaded.txt")
	_, _ = io.WriteString(part, "hello upload")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "uploaded.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "hello upload" {
		t.Errorf("expected 'hello upload', got '%s'", string(data))
	}
}

func TestFile_Upload_RelativePath(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "file.txt")
	_, _ = io.WriteString(part, "nested content")
	_ = w.WriteField("paths", "sub/file.txt")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "sub", "file.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "nested content" {
		t.Errorf("expected 'nested content', got '%s'", string(data))
	}
}

func TestFile_Upload_Conflict_DefaultErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "report.txt")
	_, _ = io.WriteString(part, "new content")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error     string   `json:"error"`
		Conflicts []string `json:"conflicts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Conflicts) != 1 || body.Conflicts[0] != "report.txt" {
		t.Errorf("expected conflicts=[report.txt], got %v", body.Conflicts)
	}
	// Original must survive the rejected upload.
	data, _ := os.ReadFile(filepath.Join(dir, "report.txt"))
	if string(data) != "original" {
		t.Errorf("expected original content preserved, got %q", string(data))
	}
}

func TestFile_CheckUploadConflicts_DetectsConflictWithoutFileBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := bytes.NewBufferString(`{"path":".","paths":["report.txt","new.txt"]}`)
	req := httptest.NewRequest("POST", "/api/users/u1/files/upload/check", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Conflicts []string `json:"conflicts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response.Conflicts) != 1 || response.Conflicts[0] != "report.txt" {
		t.Fatalf("expected conflicts=[report.txt], got %v", response.Conflicts)
	}
}

func TestFile_Upload_Conflict_Overwrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "report.txt")
	_, _ = io.WriteString(part, "new content")
	_ = w.WriteField("on_conflict", "overwrite")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "report.txt"))
	if string(data) != "new content" {
		t.Errorf("expected file overwritten, got %q", string(data))
	}
}

func TestFile_Upload_Conflict_Rename(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("original"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "report.txt")
	_, _ = io.WriteString(part, "second copy")
	_ = w.WriteField("on_conflict", "rename")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Original keeps its content; the new file lands under a dedup'd name.
	orig, _ := os.ReadFile(filepath.Join(dir, "report.txt"))
	if string(orig) != "original" {
		t.Errorf("expected original untouched, got %q", string(orig))
	}
	dup, err := os.ReadFile(filepath.Join(dir, "report (copy).txt"))
	if err != nil {
		t.Fatalf("read dedup'd file: %v", err)
	}
	if string(dup) != "second copy" {
		t.Errorf("expected 'second copy', got %q", string(dup))
	}
}

func TestFile_Upload_Conflict_InvalidOnConflict(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "file.txt")
	_, _ = io.WriteString(part, "x")
	_ = w.WriteField("on_conflict", "yolo")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Upload_PathTraversal_Rejected(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "evil.txt")
	_, _ = io.WriteString(part, "malicious")
	_ = w.WriteField("paths", "../../evil.txt")
	_ = w.Close()

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// setupCappedUploadRouter mounts the upload route with a shrunken request cap
// so the oversize path can be exercised without POSTing hundreds of MB.
func setupCappedUploadRouter(ms *storetest.Fake, maxBytes int64) *gin.Engine {
	r := gin.New()
	h := &FileHandler{Store: ms, UploadMaxBytes: maxBytes}
	r.POST("/api/users/:id/files/upload", h.Upload)
	return r
}

// multipartUpload builds a one-file multipart body of the given payload size
// and returns it along with its content type.
func multipartUpload(t *testing.T, name string, payloadSize int) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("files", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), payloadSize)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

func TestFile_Upload_RejectsOversizeRequestWith413(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	const limit = 4 * 1024
	r := setupCappedUploadRouter(ms, limit)

	body, ctype := multipartUpload(t, "big.bin", limit*2)
	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	// Nothing may land on disk: the cap has to bite before any file is saved.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no files written, got %d", len(entries))
	}
}

func TestFile_Upload_AcceptsRequestJustUnderCap(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	// Build the body first, then set the cap to exactly its length: that is
	// the boundary case, the largest request the handler must still accept.
	body, ctype := multipartUpload(t, "ok.bin", 32*1024)
	exact := int64(body.Len())
	r := setupCappedUploadRouter(ms, exact)

	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at exactly the cap (%d bytes), got %d: %s", exact, rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "ok.bin"))
	if err != nil {
		t.Fatalf("stat uploaded file: %v", err)
	}
	if info.Size() != 32*1024 {
		t.Errorf("wrote %d bytes, want %d", info.Size(), 32*1024)
	}
}

// Files larger than the in-memory bound spill to temp files rather than being
// buffered whole; this keeps that path covered so a future refactor back to
// c.MultipartForm() (engine-wide 32 MiB) is noticed.
func TestFile_Upload_LargerThanMemoryBoundStillSaves(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	size := fileBrowserUploadMemoryBytes + 1024
	r := setupCappedUploadRouter(ms, int64(size)*4)

	body, ctype := multipartUpload(t, "spill.bin", size)
	req := httptest.NewRequest("POST", "/api/users/u1/files/upload?path=", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "spill.bin"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(size) {
		t.Errorf("wrote %d bytes, want %d", info.Size(), size)
	}
}

// The default cap is a multiple of the chat-attachment limit because this
// endpoint takes a whole folder in one request; if someone drops it to the
// per-file value, batch uploads silently start failing.
func TestFileBrowserUploadCap_ExceedsChatAttachmentCap(t *testing.T) {
	if fileBrowserUploadMaxBytes <= service.DefaultUploadMaxBytes {
		t.Fatalf("batch cap %d must exceed the single-attachment cap %d",
			fileBrowserUploadMaxBytes, service.DefaultUploadMaxBytes)
	}
	if fileBrowserUploadMemoryBytes >= fileBrowserUploadMaxBytes {
		t.Fatalf("memory bound %d must be below the request cap %d",
			fileBrowserUploadMemoryBytes, fileBrowserUploadMaxBytes)
	}
}
