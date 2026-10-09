package handler

import (
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// previewContentTypes pins the types a rendered page depends on being right.
// mime.TypeByExtension consults /etc/mime.types and the Windows registry
// first, and a host whose table maps .js to text/plain (or omits .wasm) turns
// every script tag into a nosniff-blocked download. The page has no way to
// recover from that, so these six are decided here rather than by the host.
var previewContentTypes = map[string]string{
	".js":   "text/javascript; charset=utf-8",
	".mjs":  "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
	".wasm": "application/wasm",
}

func previewContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ct, ok := previewContentTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// PreviewFile serves one file from the user's workspace under a *path-shaped*
// URL: /users/:id/files/preview/<workspace path>.
//
// The shape is the whole point. /files/read carries the path in a query
// string, so a relative `./app.wasm` inside a previewed page resolves against
// /files/ and 404s; here the same reference resolves to the sibling file on
// disk, which is what makes an HTML file with its own assets (and its
// subdirectories) renderable in an iframe at all.
//
// Authorization is the same getUserFileAccess + safePath sandbox every other
// file route uses — this adds a URL shape, not a new reach into the
// filesystem.
func (h *FileHandler) PreviewFile(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	// Gin's wildcard keeps the leading "/"; safePath wants a clean relative
	// path. An empty remainder is the directory form of the route, which has
	// no document to render.
	rel := strings.TrimPrefix(c.Param("path"), "/")
	if rel == "" {
		c.Status(http.StatusNotFound)
		return
	}
	full, err := access.safePath(rel)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	// A directory falls back to its index.html — pages link to `./sub/` and
	// expect that. Resolved through safePath again rather than joined
	// directly, so a symlinked index.html is held to the same containment
	// check as any other path the client could have asked for.
	info, err := os.Stat(full)
	if err == nil && info.IsDir() {
		full, err = access.safePath(path.Join(rel, "index.html"))
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		info, err = os.Stat(full)
	}
	if err != nil || !info.Mode().IsRegular() {
		// No directory listings, no devices: anything that is not a plain
		// file simply isn't there as far as the preview is concerned.
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Content-Type", previewContentType(full))
	// Revalidate every time: the point of the preview is to show what is on
	// disk right now, and a reload triggered by a file change must not be
	// answered from the browser's cache. ServeContent still answers an
	// unchanged asset with a 304.
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	// The preview is framed by the app itself; nobody else has business
	// embedding a user's workspace file.
	c.Header("Content-Security-Policy", "frame-ancestors 'self'")

	f, err := os.Open(full)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()
	http.ServeContent(c.Writer, c.Request, filepath.Base(full), info.ModTime(), f)
}
