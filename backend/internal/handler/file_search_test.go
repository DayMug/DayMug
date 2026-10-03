package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// seedSearchTree lays down a small workspace and returns its root plus a
// router bound to it as user u1.
func seedSearchTree(t *testing.T, files map[string]string) (string, *gin.Engine) {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s: %v", rel, err)
		}
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	return dir, setupFileRouter(ms)
}

func doSearch(t *testing.T, r *gin.Engine, query url.Values) searchResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/users/u1/files/search?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func resultPaths(resp searchResponse) []string {
	paths := make([]string, 0, len(resp.Results))
	for _, r := range resp.Results {
		paths = append(paths, r.Path)
	}
	return paths
}

func TestFileSearch_NameMode(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"cmd/main.go":         "package main",
		"internal/handler.go": "package internal",
		"README.md":           "# hi",
	})

	got := resultPaths(doSearch(t, r, url.Values{"q": {"main"}}))
	if len(got) != 1 || got[0] != "cmd/main.go" {
		t.Fatalf("expected [cmd/main.go], got %v", got)
	}
}

// Matching the relative path, not just the basename, is what makes a Cmd+P
// picker usable — "cmd/main" is how people actually narrow a search.
func TestFileSearch_NameModeMatchesPathSegments(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"cmd/main.go": "package main",
		"web/main.go": "package web",
	})

	got := resultPaths(doSearch(t, r, url.Values{"q": {"cmd/main"}}))
	if len(got) != 1 || got[0] != "cmd/main.go" {
		t.Fatalf("expected [cmd/main.go], got %v", got)
	}
}

func TestFileSearch_NameModeRanksBasenameHitsFirst(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"handler/deeply/nested/other.go": "x",
		"top.go":                         "x",
		"handler/x.go":                   "x",
	})

	got := resultPaths(doSearch(t, r, url.Values{"q": {"handler"}}))
	// handler/x.go only matches via its directory; nothing has "handler" in
	// its basename, so the tie-break is path length.
	if len(got) != 2 {
		t.Fatalf("expected 2 results, got %v", got)
	}
	if got[0] != "handler/x.go" {
		t.Fatalf("expected shortest path first, got %v", got)
	}
}

func TestFileSearch_ContentMode(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"a.go": "package a\nfunc Target() {}\n",
		"b.go": "package b\n",
	})

	resp := doSearch(t, r, url.Values{"q": {"target"}, "mode": {"content"}})
	if len(resp.Results) != 1 || resp.Results[0].Path != "a.go" {
		t.Fatalf("expected a.go, got %v", resultPaths(resp))
	}
	matches := resp.Results[0].Matches
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Line != 2 {
		t.Fatalf("expected line 2, got %d", matches[0].Line)
	}
	if matches[0].Text != "func Target() {}" {
		t.Fatalf("snippet mismatch: %q", matches[0].Text)
	}
}

// A hit inside a compiled artifact is noise the user cannot act on, and the
// snippet would be binary garbage.
func TestFileSearch_ContentModeSkipsBinaryFile(t *testing.T) {
	dir, r := seedSearchTree(t, map[string]string{"good.txt": "needle here"})
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("needle\x00\x01\x02"), 0o644); err != nil {
		t.Fatalf("seed blob: %v", err)
	}

	got := resultPaths(doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}}))
	if len(got) != 1 || got[0] != "good.txt" {
		t.Fatalf("expected only good.txt, got %v", got)
	}
}

func TestFileSearch_SkipsNoiseDirs(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"src/app.js":                  "needle",
		"node_modules/dep/index.js":   "needle",
		".git/objects/thing":          "needle",
		"__pycache__/mod.cpython.pyc": "needle",
	})

	got := resultPaths(doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}}))
	if len(got) != 1 || got[0] != "src/app.js" {
		t.Fatalf("expected only src/app.js, got %v", got)
	}
}

func TestFileSearch_ScopedToSubdirectory(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"src/app.js":  "needle",
		"docs/README": "needle",
	})

	resp := doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}, "path": {"src"}})
	got := resultPaths(resp)
	if len(got) != 1 || got[0] != "src/app.js" {
		t.Fatalf("expected only src/app.js, got %v", got)
	}
}

// Paths must stay relative to the work dir even when the search is scoped to
// a subdirectory, or the frontend cannot turn a result into an open-file link.
func TestFileSearch_ScopedResultsStayWorkdirRelative(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{"src/deep/app.js": "needle"})

	resp := doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}, "path": {"src"}})
	if len(resp.Results) != 1 || resp.Results[0].Path != "src/deep/app.js" {
		t.Fatalf("expected src/deep/app.js, got %v", resultPaths(resp))
	}
}

func TestFileSearch_LimitTruncates(t *testing.T) {
	files := map[string]string{}
	for i := range 5 {
		files[string(rune('a'+i))+".txt"] = "needle"
	}
	_, r := seedSearchTree(t, files)

	resp := doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}, "limit": {"2"}})
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if !resp.Truncated {
		t.Fatal("expected truncated=true")
	}
}

func TestFileSearch_PerFileMatchCap(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"many.txt": strings.Repeat("needle\n", searchMaxMatchesPerFile+10),
	})

	resp := doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}})
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if len(resp.Results[0].Matches) != searchMaxMatchesPerFile {
		t.Fatalf("expected %d matches, got %d", searchMaxMatchesPerFile, len(resp.Results[0].Matches))
	}
}

func TestFileSearch_TruncatesLongSnippet(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{
		"long.txt": "needle" + strings.Repeat("x", searchSnippetMaxBytes*2),
	})

	resp := doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}})
	text := resp.Results[0].Matches[0].Text
	if !strings.HasSuffix(text, "…") {
		t.Fatalf("expected an ellipsis on the truncated snippet: %q", text[:40])
	}
	if len(text) > searchSnippetMaxBytes+len("…") {
		t.Fatalf("snippet too long: %d bytes", len(text))
	}
}

func TestFileSearch_RejectsMissingQuery(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{"a.txt": "x"})

	req := httptest.NewRequest("GET", "/api/users/u1/files/search", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestFileSearch_RejectsUnknownMode(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{"a.txt": "x"})

	req := httptest.NewRequest("GET", "/api/users/u1/files/search?q=x&mode=regex", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestFileSearch_RejectsTraversal(t *testing.T) {
	_, r := seedSearchTree(t, map[string]string{"a.txt": "x"})

	req := httptest.NewRequest("GET", "/api/users/u1/files/search?q=x&path=../..", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// WalkDir lstats rather than following links, so a symlink aimed outside the
// workspace is never descended into — the containment safePath gives the
// single-file endpoints has to hold for the walk too.
func TestFileSearch_DoesNotFollowEscapingSymlink(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle"), 0o644); err != nil {
		t.Fatalf("seed outside: %v", err)
	}
	dir, r := seedSearchTree(t, map[string]string{"inside.txt": "nothing here"})
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got := resultPaths(doSearch(t, r, url.Values{"q": {"needle"}, "mode": {"content"}}))
	if len(got) != 0 {
		t.Fatalf("search escaped the workspace: %v", got)
	}
}
