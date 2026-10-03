package handler

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Bounds for a workspace search. A user's work dir is an arbitrary tree that
// may hold a checked-out monorepo, so an unbounded walk is a trivial way to
// pin a core and stall every other request the server is serving. Every limit
// here exists to make the worst case cheap rather than to be generous in the
// common one — real queries land far under all of them.
const (
	searchDefaultLimit = 50
	searchMaxLimit     = 200
	// Per-file match cap. A query like "err" in a Go file legitimately hits
	// hundreds of lines; nobody reads past the first handful, and shipping
	// them all is what turns one result into a megabyte of JSON.
	searchMaxMatchesPerFile = 20
	// Files larger than this are skipped in content mode. Minified bundles
	// and lock files blow past it; hand-written source effectively never does.
	searchMaxFileBytes = 2 * 1024 * 1024
	// Hard ceiling on tree entries visited, independent of how many matched.
	// Guards the pathological case where the skip list misses a huge vendored
	// directory.
	searchMaxEntries = 50000
	// Matched lines are truncated to this many bytes. One 50 MB single-line
	// minified file would otherwise become one 50 MB snippet.
	searchSnippetMaxBytes = 400
)

// searchSkipDirs are directory names never descended into. Deliberately short:
// each entry is a place that reliably holds machine-generated content in
// volumes that dominate a walk. Build outputs like dist/ and target/ are left
// in on purpose — they are small enough to survive the entry cap, and a user
// searching for a string in their own generated output should find it.
var searchSkipDirs = map[string]bool{
	".git":          true,
	"node_modules":  true,
	".venv":         true,
	"venv":          true,
	"__pycache__":   true,
	".mypy_cache":   true,
	".pytest_cache": true,
}

type searchMatch struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type searchResult struct {
	Path     string        `json:"path"`
	Name     string        `json:"name"`
	Size     int64         `json:"size"`
	Modified time.Time     `json:"modified"`
	Matches  []searchMatch `json:"matches"`
}

type searchResponse struct {
	Query     string         `json:"query"`
	Mode      string         `json:"mode"`
	Truncated bool           `json:"truncated"`
	Results   []searchResult `json:"results"`
}

// Search walks the workspace under ?path= looking for ?q=.
//
// mode=name matches the query against each entry's path relative to the work
// dir, which is what a Cmd+P style file picker wants — typing "handler/file"
// should find it just as "file.go" does. mode=content greps line-wise through
// text files. Both are case-insensitive substring matches: over-matching is
// recoverable by typing more, whereas a case-sensitive miss looks like the
// file simply isn't there.
func (h *FileHandler) Search(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	workDir := access.workDir

	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q is required"})
		return
	}

	mode := c.DefaultQuery("mode", "name")
	if mode != "name" && mode != "content" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be name or content"})
		return
	}

	limit := searchDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
		limit = min(n, searchMaxLimit)
	}

	scope, err := access.safePath(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	if info, statErr := os.Stat(scope); statErr != nil || !info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "search scope is not a directory"})
		return
	}

	needle := strings.ToLower(query)
	results := make([]searchResult, 0, limit)
	truncated := false
	entries := 0

	// WalkDir lstats rather than following links, so a symlink pointing out
	// of the workspace is reported as an entry and never descended into —
	// the same containment safePath gives the single-file endpoints, for free.
	walkErr := filepath.WalkDir(scope, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is not a reason to fail the whole
			// search; skip it and keep going.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctxErr := c.Request.Context().Err(); ctxErr != nil {
			return ctxErr
		}

		if d.IsDir() {
			if full == scope {
				return nil
			}
			if searchSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}

		entries++
		if entries > searchMaxEntries {
			truncated = true
			return filepath.SkipAll
		}

		rel, relErr := filepath.Rel(workDir, full)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		var matches []searchMatch
		if mode == "name" {
			if !strings.Contains(strings.ToLower(rel), needle) {
				return nil
			}
			matches = []searchMatch{}
		} else {
			matches = grepFile(full, needle)
			if len(matches) == 0 {
				return nil
			}
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		results = append(results, searchResult{
			Path:     rel,
			Name:     d.Name(),
			Size:     info.Size(),
			Modified: info.ModTime(),
			Matches:  matches,
		})
		if len(results) >= limit {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && c.Request.Context().Err() != nil {
		// Client hung up or the request deadline passed — nothing useful to
		// send, and writing a body to a dead connection just logs noise.
		return
	}

	if mode == "name" {
		rankNameResults(results, needle)
	}

	c.JSON(http.StatusOK, searchResponse{
		Query:     query,
		Mode:      mode,
		Truncated: truncated,
		Results:   results,
	})
}

// rankNameResults puts basename hits above directory-only hits, then prefers
// shorter paths. Typing "file" should surface handler/file.go before
// some/file/deeply/nested/unrelated.go, which is the difference between a
// usable file picker and a list you have to read.
func rankNameResults(results []searchResult, needle string) {
	sort.SliceStable(results, func(i, j int) bool {
		bi := strings.Contains(strings.ToLower(results[i].Name), needle)
		bj := strings.Contains(strings.ToLower(results[j].Name), needle)
		if bi != bj {
			return bi
		}
		return len(results[i].Path) < len(results[j].Path)
	})
}

// grepFile returns up to searchMaxMatchesPerFile matching lines. Binary and
// oversize files yield nothing: the caller wants source, and a hit inside a
// compiled artifact is noise it cannot act on.
func grepFile(full, needle string) []searchMatch {
	info, err := os.Stat(full)
	if err != nil || info.Size() == 0 || info.Size() > searchMaxFileBytes {
		return nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	if !isTextSample(sample) {
		return nil
	}

	var matches []searchMatch
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		matches = append(matches, searchMatch{
			Line: i + 1,
			Text: truncateSnippet(strings.TrimRight(line, "\r")),
		})
		if len(matches) >= searchMaxMatchesPerFile {
			break
		}
	}
	return matches
}

func truncateSnippet(line string) string {
	if len(line) <= searchSnippetMaxBytes {
		return line
	}
	// Back off to a rune boundary so the JSON encoder doesn't have to
	// substitute U+FFFD for a chopped multi-byte character.
	cut := searchSnippetMaxBytes
	for cut > 0 && !utf8ValidCut(line, cut) {
		cut--
	}
	return line[:cut] + "…"
}

func utf8ValidCut(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	// A continuation byte is 10xxxxxx; cutting before one splits a rune.
	return s[i]&0xC0 != 0x80
}
