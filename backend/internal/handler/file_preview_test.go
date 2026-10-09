package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupPreviewRouter(ms *storetest.Fake) *gin.Engine {
	r := gin.New()
	h := NewFileHandler(ms)
	r.GET("/api/users/:id/files/preview/*path", h.PreviewFile)
	return r
}

// previewWorkspace lays out the shape the feature exists for: a page, an
// asset beside it and one in a subdirectory.
func previewWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "site", "assets"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(filepath.Join("site", "index.html"), "<html><body>hi</body></html>")
	write(filepath.Join("site", "app.js"), "console.log(1)")
	write(filepath.Join("site", "assets", "app.wasm"), "\x00asm")
	return dir
}

func getPreview(t *testing.T, r *gin.Engine, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", url, http.NoBody))
	return rec
}

func TestFile_Preview_ServesPageAndAssets(t *testing.T) {
	dir := previewWorkspace(t)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupPreviewRouter(ms)

	cases := []struct {
		name        string
		url         string
		wantBody    string
		wantType    string
		description string
	}{
		{
			name:     "html",
			url:      "/api/users/u1/files/preview/site/index.html",
			wantBody: "<html><body>hi</body></html>",
			wantType: "text/html; charset=utf-8",
		},
		{
			name:     "sibling script",
			url:      "/api/users/u1/files/preview/site/app.js",
			wantBody: "console.log(1)",
			wantType: "text/javascript; charset=utf-8",
		},
		{
			name:     "asset in a subdirectory",
			url:      "/api/users/u1/files/preview/site/assets/app.wasm",
			wantBody: "\x00asm",
			wantType: "application/wasm",
		},
		{
			name:     "directory falls back to index.html",
			url:      "/api/users/u1/files/preview/site/",
			wantBody: "<html><body>hi</body></html>",
			wantType: "text/html; charset=utf-8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := getPreview(t, r, tc.url)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := rec.Body.String(); got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
			if got := rec.Header().Get("Content-Type"); got != tc.wantType {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
			}
		})
	}
}

// Served straight from disk on every request: a page reloaded after an edit
// must not be answered from the browser's cache.
func TestFile_Preview_SetsNoCacheAndNosniff(t *testing.T) {
	dir := previewWorkspace(t)
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupPreviewRouter(ms)

	rec := getPreview(t, r, "/api/users/u1/files/preview/site/index.html")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Errorf("CSP = %q, want frame-ancestors 'self'", got)
	}
}

func TestFile_Preview_RejectsEscapesAndNonFiles(t *testing.T) {
	dir := previewWorkspace(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "site", "leak.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupPreviewRouter(ms)

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"traversal", "/api/users/u1/files/preview/../../etc/passwd", http.StatusForbidden},
		{"symlink out of the workspace", "/api/users/u1/files/preview/site/leak.txt", http.StatusForbidden},
		{"missing file", "/api/users/u1/files/preview/site/nope.js", http.StatusNotFound},
		{"directory without an index", "/api/users/u1/files/preview/site/assets/", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := getPreview(t, r, tc.url); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestFile_Preview_ForbiddenForOtherUsersWorkspace(t *testing.T) {
	dir := previewWorkspace(t)
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Name: "One", Username: "one", Email: "one@x", WorkDir: dir},
		{ID: "u2", Name: "Two", Username: "two", Email: "two@x", WorkDir: t.TempDir()},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "u2")
		c.Next()
	})
	h := NewFileHandler(ms)
	r.GET("/api/users/:id/files/preview/*path", h.PreviewFile)

	if rec := getPreview(t, r, "/api/users/u1/files/preview/site/index.html"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}
