package handler

import (
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
)

// Bounds for the file-browser upload path. It has to allow a *batch*: a
// folder upload sends every selected file in a single request (frontend
// uploadFiles()), so the cap is a multiple of the per-file
// service.DefaultUploadMaxBytes rather than the same number.
const (
	fileBrowserUploadMaxBytes = 8 * service.DefaultUploadMaxBytes

	// fileBrowserUploadMemoryBytes bounds what the multipart parser keeps on
	// the heap. gin's default MaxMultipartMemory is 32 MiB and nothing in this
	// repo overrides it, so every concurrent upload could pin that much;
	// parts larger than this spill to temp files, which is all
	// SaveUploadedFile needs to copy them into place.
	fileBrowserUploadMemoryBytes = 4 << 20
)

type uploadConflictCheckRequest struct {
	Path  string   `json:"path"`
	Paths []string `json:"paths"`
}

// CheckUploadConflicts lets the browser ask about destination names before it
// sends file bodies. The upload handler still repeats these checks to close the
// race between this request and the eventual multipart upload.
func (h *FileHandler) CheckUploadConflicts(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	var body uploadConflictCheckRequest
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paths are required"})
		return
	}
	targetDir, err := access.safePath(body.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	info, err := os.Stat(targetDir)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target path is not a directory"})
		return
	}

	conflicts := make([]string, 0)
	for _, relName := range body.Paths {
		full, err := safePath(targetDir, filepath.Clean(relName))
		if err != nil || filepath.IsAbs(relName) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid upload path: %s", relName)})
			return
		}
		if existing, statErr := os.Stat(full); statErr == nil {
			if existing.IsDir() {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("destination is a directory: %s", relName)})
				return
			}
			conflicts = append(conflicts, relName)
		} else if !os.IsNotExist(statErr) {
			respondInternalError(c, "FileHandler.CheckUploadConflicts", statErr)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"conflicts": conflicts})
}

func (h *FileHandler) Upload(c *gin.Context) {
	// Cap the request before touching the body: the multipart parser must
	// never see more bytes than this, whatever Content-Length claims.
	maxBytes := h.UploadMaxBytes
	if maxBytes <= 0 {
		maxBytes = fileBrowserUploadMaxBytes
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)

	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPath := c.Query("path")
	targetDir, err := access.safePath(relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Stat(targetDir)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target path is not a directory"})
		return
	}

	// Parse with an explicit memory bound instead of c.MultipartForm(), which
	// would use the engine-wide gin default. Everything downstream
	// (c.PostForm, c.SaveUploadedFile) reuses the already-parsed form.
	if err := c.Request.ParseMultipartForm(fileBrowserUploadMemoryBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": fmt.Sprintf("upload exceeds %d bytes", maxBytes),
			})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid multipart form"})
		return
	}
	form := c.Request.MultipartForm

	files := form.File["files"]
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files provided"})
		return
	}

	// Optional parallel "paths" field carries relative paths for folder uploads
	paths := form.Value["paths"]

	// on_conflict mirrors the convention used by Move/Copy: empty/"error" =
	// surface a 409 so the UI can prompt; "overwrite" = truncate any
	// existing file at the destination; "rename" = pick a free name via
	// dedupName. Previously this handler silently truncated every time.
	onConflict := c.PostForm("on_conflict")
	switch onConflict {
	case "", "error", "overwrite", "rename":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid on_conflict value"})
		return
	}

	// Resolve destinations up-front so we can detect conflicts as a batch
	// and refuse the whole upload before writing anything when on_conflict
	// is unset. Partial success would leave the workspace in an in-between
	// state that the conflict prompt can't reason about.
	type plan struct {
		fh      *multipart.FileHeader
		relName string
		dst     string
	}
	resolved := make([]plan, 0, len(files))
	var conflicts []string
	for i, fh := range files {
		var relName string
		if i < len(paths) && paths[i] != "" {
			relName = filepath.Clean(paths[i])
		} else {
			relName = filepath.Base(fh.Filename)
		}

		// Reject absolute paths
		if filepath.IsAbs(relName) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("absolute path not allowed: %s", relName)})
			return
		}
		// Reject path traversal
		if strings.HasPrefix(relName, "..") || strings.Contains(relName, string(filepath.Separator)+"..") {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("path traversal not allowed: %s", relName)})
			return
		}

		dst, err := safePath(targetDir, relName)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid upload path: %s", relName)})
			return
		}

		// A *file* already at the destination is a conflict; an existing
		// *dir* at an intermediate path is fine (folder upload merges into
		// the existing tree), but a dir occupying the leaf slot can't be
		// overwritten and gets reported as a hard error below.
		if existing, statErr := os.Stat(dst); statErr == nil {
			if existing.IsDir() {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("destination is a directory: %s", relName)})
				return
			}
			conflicts = append(conflicts, relName)
		}

		resolved = append(resolved, plan{fh: fh, relName: relName, dst: dst})
	}

	if len(conflicts) > 0 && (onConflict == "" || onConflict == "error") {
		c.JSON(http.StatusConflict, gin.H{
			"error":     "destination already exists",
			"conflicts": conflicts,
		})
		return
	}

	for _, p := range resolved {
		dst := p.dst
		if onConflict == "rename" {
			if _, statErr := os.Stat(dst); statErr == nil {
				parent := filepath.Dir(dst)
				dst = filepath.Join(parent, dedupName(parent, filepath.Base(dst)))
			}
		}

		if dir := filepath.Dir(dst); dir != targetDir {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				respondInternalErrorMessage(c, "FileHandler.Upload mkdir", err, "failed to create directory for "+p.relName)
				return
			}
		}

		if err := c.SaveUploadedFile(p.fh, dst); err != nil {
			respondInternalErrorMessage(c, "FileHandler.Upload save", err, "failed to save "+p.relName)
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"uploaded": len(files)})
}
