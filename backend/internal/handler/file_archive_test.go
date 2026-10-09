package handler

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestFile_DownloadZip(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mydir")
	_ = os.Mkdir(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "a.txt"), []byte("aaa"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/download-zip?path=mydir", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify it's a valid zip
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("expected 1 file in zip, got %d", len(zr.File))
	}
	if zr.File[0].Name != "a.txt" {
		t.Errorf("expected 'a.txt', got '%s'", zr.File[0].Name)
	}
}

func TestFile_DownloadZip_MultiplePaths(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	docs := filepath.Join(dir, "docs")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/download-zip?path=src&path=docs", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"src/a.txt", "docs/b.txt"} {
		if !names[want] {
			t.Fatalf("expected %q in zip, got %#v", want, names)
		}
	}
}

// A symlink inside the directory must be archived as a symlink entry (mode
// bits set, body = link target), not as the full target file under a symlink
// header — the latter is the corruption that makes macOS' ditto abort with
// "… is too large" when unpacking the downloaded zip.
func TestFile_DownloadZip_Symlink(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "mydir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "target.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(sub, "link.txt")); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("GET", "/api/users/u1/files/download-zip?path=mydir", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}

	var link *zip.File
	for _, f := range zr.File {
		if f.Name == "link.txt" {
			link = f
		}
	}
	if link == nil {
		t.Fatal("link.txt missing from archive")
		return
	}
	if link.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link.txt should be flagged as a symlink, mode = %v", link.Mode())
	}

	rc, err := link.Open()
	if err != nil {
		t.Fatalf("open link entry: %v", err)
	}
	defer rc.Close() //nolint:errcheck
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read link entry: %v", err)
	}
	if string(body) != "target.txt" {
		t.Errorf("symlink body should be the link target %q, got %q", "target.txt", string(body))
	}
}

// writeTestZip builds a minimal zip on disk containing the given entries
// (name => content). Used by the Extract handler tests so they don't need a
// fixture file checked into the tree.
func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	defer f.Close() //nolint:errcheck
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create entry: %v", err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatalf("zip write entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

func TestFile_Extract_ZipSuccess(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	writeTestZip(t, zipPath, map[string]string{
		"hello.txt":     "hi",
		"nested/a.txt":  "alpha",
		"nested/b.json": `{"k":1}`,
	})

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"bundle.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Path      string `json:"path"`
		Extracted int    `json:"extracted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Path != "bundle" {
		t.Fatalf("expected path=bundle, got %q", body.Path)
	}
	if body.Extracted != 3 {
		t.Fatalf("expected extracted=3, got %d", body.Extracted)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "bundle", "hello.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("hello.txt: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "bundle", "nested", "a.txt")); err != nil || string(got) != "alpha" {
		t.Fatalf("nested/a.txt: got=%q err=%v", got, err)
	}
}

func TestFile_Extract_TraversalRejected(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "evil.zip")
	// Entry tries to escape the extraction root via "..". safePath must
	// catch this and the handler must roll back the half-extracted dir.
	writeTestZip(t, zipPath, map[string]string{
		"../escape.txt": "pwn",
	})

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"evil.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape.txt should not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
		t.Fatalf("partial extract dir should be cleaned up: %v", err)
	}
}

func TestFile_Extract_UnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "foo.rar"), []byte("RAR"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"foo.rar"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Extract_DedupesExistingDir(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bundle.zip")
	// Multiple top-level entries force the fallback path (no archive-shipped
	// wrapper to promote), which is where dedupName runs on the archive stem.
	writeTestZip(t, zipPath, map[string]string{"a.txt": "1", "b.txt": "2"})
	// Pre-existing target dir forces the handler to pick "bundle (copy)".
	if err := os.Mkdir(filepath.Join(dir, "bundle"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"bundle.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Path != "bundle (copy)" {
		t.Fatalf("expected path=bundle (copy), got %q", body.Path)
	}
}

// TestFile_Extract_DoesNotNestSingleTopLevelDir locks in the fix for the
// russian-doll problem: Compress prefixes every entry with the source's
// basename, so the zip for folder `src/` already contains its own `src/`
// wrapper at the top. The previous Extract always added a *second* wrapper
// outside, so each compress→extract round-trip left the user one level
// deeper (`src/src/main.go`, then `src/src/src/main.go`, …). Now the
// shipped wrapper is promoted directly.
func TestFile_Extract_DoesNotNestSingleTopLevelDir(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "src.zip")
	writeTestZip(t, zipPath, map[string]string{
		"src/main.go":  "package main",
		"src/lib/a.go": "package lib",
	})

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"src.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Path      string `json:"path"`
		Extracted int    `json:"extracted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Path != "src" {
		t.Fatalf("expected path=src (promoted wrapper), got %q", body.Path)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "src", "main.go")); err != nil || string(got) != "package main" {
		t.Fatalf("src/main.go: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "src", "lib", "a.go")); err != nil || string(got) != "package lib" {
		t.Fatalf("src/lib/a.go: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "src")); !os.IsNotExist(err) {
		t.Fatalf("nested src/src should not exist (russian-doll regression): %v", err)
	}
}

// TestFile_Extract_SingleTopLevelDirDedupedOnCollision covers the
// promotion-with-collision case: when the archive's own wrapper would
// overwrite a sibling, dedupName picks a fresh name instead of clobbering
// or surfacing an error.
func TestFile_Extract_SingleTopLevelDirDedupedOnCollision(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "src.zip")
	writeTestZip(t, zipPath, map[string]string{
		"src/main.go": "package main",
	})
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"src.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Path != "src (copy)" {
		t.Fatalf("expected path=src (copy), got %q", body.Path)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "src (copy)", "main.go")); err != nil || string(got) != "package main" {
		t.Fatalf("src (copy)/main.go: got=%q err=%v", got, err)
	}
}

// TestFile_Extract_IgnoresMacOSMetadata locks in the fix for the
// Finder-zipped folder case: macOS injects a sibling `__MACOSX/` resource-
// fork dump at the top level, so without filtering the archive looks
// multi-top-level and the real wrapper gets nested inside an extra layer
// (e.g. `foo/foo/...`). The `__MACOSX/` tree must be dropped entirely and
// the single real top-level dir must be promoted directly.
func TestFile_Extract_IgnoresMacOSMetadata(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "src.zip")
	writeTestZip(t, zipPath, map[string]string{
		"src/main.go":            "package main",
		"src/lib/a.go":           "package lib",
		"__MACOSX/._src":         "junk",
		"__MACOSX/src/._main.go": "junk",
	})

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"src.zip"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Path      string `json:"path"`
		Extracted int    `json:"extracted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Path != "src" {
		t.Fatalf("expected path=src (no extra wrapper), got %q", body.Path)
	}
	if body.Extracted != 2 {
		t.Fatalf("expected extracted=2 (macOS metadata filtered), got %d", body.Extracted)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "src", "main.go")); err != nil || string(got) != "package main" {
		t.Fatalf("src/main.go: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "src")); !os.IsNotExist(err) {
		t.Fatalf("nested src/src must not exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "__MACOSX")); !os.IsNotExist(err) {
		t.Fatalf("__MACOSX must be filtered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "__MACOSX")); !os.IsNotExist(err) {
		t.Fatalf("__MACOSX must not surface at parent: %v", err)
	}
}

func TestFile_Extract_TarGzSuccess(t *testing.T) {
	dir := t.TempDir()
	tgzPath := filepath.Join(dir, "bundle.tar.gz")
	// Hand-build a tiny .tar.gz so we exercise both the tar reader and the
	// gzip wrapper without a fixture file.
	f, err := os.Create(tgzPath)
	if err != nil {
		t.Fatalf("create tgz: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("hello tar")
	if err := tw.WriteHeader(&tar.Header{Name: "greet.txt", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("file close: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/extract",
		strings.NewReader(`{"path":"bundle.tar.gz"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// Single-entry archive: the lone file is promoted into the parent
	// directly — no synthetic `bundle/` wrapper added on top.
	if got, err := os.ReadFile(filepath.Join(dir, "greet.txt")); err != nil || string(got) != "hello tar" {
		t.Fatalf("greet.txt: got=%q err=%v", got, err)
	}
}

// readZipNames opens a zip and returns its entry names so tests can assert on
// the archive's top-level shape without spelling out the binary format.
func readZipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close() //nolint:errcheck
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func TestFile_Compress_FileAndDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644)
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("alpha"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"paths":["hello.txt","src"],"target_dir":"."}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "Archive.zip" {
		t.Fatalf("expected name=Archive.zip, got %q", resp.Name)
	}

	names := readZipNames(t, filepath.Join(dir, resp.Name))
	// Entries are prefixed with each source's basename so the archive's
	// top level mirrors what the user actually selected.
	want := map[string]bool{
		"hello.txt": true,
		"src/":      true,
		"src/a.txt": true,
	}
	if len(names) != len(want) {
		t.Fatalf("expected %d entries, got %d: %v", len(want), len(names), names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("unexpected zip entry %q", n)
		}
	}
}

func TestFile_Compress_SingleFileDefaultName(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "report.md"), []byte("# hi"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"paths":["report.md"]}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// Single source reuses its basename — matches Finder's "Compress 'X'"
	// behaviour. Multi-source falls back to the generic "Archive.zip".
	if resp.Name != "report.md.zip" {
		t.Fatalf("expected name=report.md.zip, got %q", resp.Name)
	}
}

func TestFile_Compress_DedupesExistingZip(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	// Pre-existing Archive.zip forces the handler to pick a free name.
	_ = os.WriteFile(filepath.Join(dir, "Archive.zip"), []byte("PK"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"paths":["a.txt","b.txt"]}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Name != "Archive (copy).zip" {
		t.Fatalf("expected dedupName Archive (copy).zip, got %q", resp.Name)
	}
}

func TestFile_Compress_TraversalRejected(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"paths":["../../etc/passwd"]}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Compress_MissingSource(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "exists.txt"), []byte("x"), 0o644)

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"paths":["exists.txt","missing.txt"]}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
	// Partial archive must not land on disk when one source is missing.
	for _, n := range []string{"Archive.zip", "exists.txt.zip"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Fatalf("partial archive %s should not exist: %v", n, err)
		}
	}
}

func TestFile_Compress_EmptyPaths(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/u1/files/compress",
		strings.NewReader(`{"paths":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFile_Compress_NameWithSeparatorRejected(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	body := `{"paths":["a.txt"],"name":"../escape.zip"}`
	req := httptest.NewRequest("POST", "/api/users/u1/files/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
