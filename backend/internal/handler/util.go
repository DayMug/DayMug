package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// maxJSONBodyBytes caps every JSON request body that goes through bindJSON.
// There is no global body-limit middleware, so without this cap a single
// request decided how much memory the process allocated: encoding/json holds
// the scan buffer *and* the decoded Go strings, so a 4 GiB POST cost roughly
// 2x that on the heap — and it cost it *before* any handler-level size check
// could reject it.
//
// The value sits deliberately above the handler-specific limits rather than
// at them. WriteFile's maxEditableFileBytes (10 MiB) applies to the *decoded*
// content while this applies to the *escaped* wire form, and JSON escaping
// doubles the size of text where every byte is a quote, backslash, tab or
// newline. Leaving 2x + envelope headroom means a legal editor save still
// reaches WriteFile and gets its specific "file too large" 413 instead of
// this generic one. Content dense in control characters (each one escapes to
// six bytes) can still trip the generic limit first; that response is also a
// 413, so the client's "too big" handling stays correct either way.
//
// Every other bindJSON caller is orders of magnitude below this: the largest
// are PutClaudeMd and the help-doc markdown, both hand-authored documents
// well under 1 MiB, and the batch admin endpoints whose bodies are lists of
// ~40-byte ids.
const maxJSONBodyBytes = 2*maxEditableFileBytes + 64*1024

// bindJSON decodes the request body into dest. On failure it writes the
// standard 400 response and returns false; callers should `return`
// immediately. Bodies over maxJSONBodyBytes are refused with 413 rather than
// 400 — the request is well-formed, it is just too big, and a 400 would send
// clients into a "fix your JSON" loop they can't win.
func bindJSON(c *gin.Context, dest any) bool {
	if c.Request != nil && c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBodyBytes)
	}
	if err := c.ShouldBindJSON(dest); err != nil {
		// MaxBytesReader's error surfaces through the json decoder unwrapped,
		// but match with errors.As so a future wrapping decoder can't silently
		// turn the 413 back into a 400.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return false
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}
