package handler

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/filewatch"
	"github.com/DayMug/DayMug/backend/internal/store"
)

type FileHandler struct {
	Store store.Store
	// UploadMaxBytes caps one file-browser upload request. Set per-handler so
	// tests can shrink it instead of having to POST hundreds of megabytes;
	// zero means fileBrowserUploadMaxBytes. Mirrors UploadHandler.MaxBytes.
	UploadMaxBytes int64
	// Watcher backs the /watch WebSocket. Optional: a nil watcher means
	// fsnotify was unavailable at boot, and the endpoint tells clients so
	// rather than going silent, so they fall back to polling.
	Watcher *filewatch.Watcher
}

func NewFileHandler(s store.Store) *FileHandler {
	return &FileHandler{Store: s}
}

type fileEntry struct {
	Name     string    `json:"name"`
	IsDir    bool      `json:"is_dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type dirListing struct {
	Path    string      `json:"path"`
	Entries []fileEntry `json:"entries"`
}

type fileInspection struct {
	Path        string    `json:"path"`
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	Size        int64     `json:"size"`
	Modified    time.Time `json:"modified"`
	ContentType string    `json:"content_type"`
	IsText      bool      `json:"is_text"`
}

// safePath resolves relPath under workDir and ensures no path traversal.
//
// Containment is decided on the symlink-resolved form of both sides, so a
// link planted inside the workspace (by the agent itself, by an extracted
// archive, by a git checkout) cannot redirect a read or a write to the
// server's config or database. The path handed back is the unresolved join
// — callers that delete or rename should act on the name asked for, not on
// whatever it points at.
func safePath(workDir, relPath string) (string, error) {
	return safePathFrom(workDir, workDir, relPath)
}

// safePathFrom resolves relPath from workDir while using rootDir as the
// authorization boundary. An agent's paths stay relative to its own work dir,
// but links and ".." paths may reach shared files elsewhere in its human
// owner's work dir.
func safePathFrom(rootDir, workDir, relPath string) (string, error) {
	full, err := service.ResolveFromWithin(rootDir, workDir, relPath)
	if err != nil {
		if errors.Is(err, service.ErrPathEscapes) {
			return "", fmt.Errorf("path traversal not allowed")
		}
		return "", err
	}
	return full, nil
}

type userFileAccess struct {
	workDir string
	rootDir string
}

func (a userFileAccess) safePath(relPath string) (string, error) {
	return safePathFrom(a.rootDir, a.workDir, relPath)
}

func (h *FileHandler) getUserFileAccess(c *gin.Context) (userFileAccess, bool) {
	id := c.Param("id")
	user, err := h.Store.GetUser(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return userFileAccess{}, false
	}
	if !service.CanAccessOwner(c.Request.Context(), h.Store, middleware.CurrentUserID(c), user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return userFileAccess{}, false
	}
	if user.WorkDir == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user has no work directory"})
		return userFileAccess{}, false
	}

	rootDir := user.WorkDir
	if user.OwnerID != "" {
		owner, err := h.Store.GetOwner(c.Request.Context(), user)
		if err != nil {
			respondStoreError(c, err, "owner not found")
			return userFileAccess{}, false
		}
		if owner.WorkDir != "" {
			rootDir = owner.WorkDir
		}
	}
	return userFileAccess{workDir: user.WorkDir, rootDir: rootDir}, true
}

func (h *FileHandler) ListDir(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPath := c.Query("path")
	full, err := access.safePath(relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	entries, err := os.ReadDir(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "directory not found"})
		} else {
			respondInternalError(c, "FileHandler.ListDir", err)
		}
		return
	}

	result := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, fileEntry{
			Name:     e.Name(),
			IsDir:    e.IsDir(),
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}

	displayPath := relPath
	if displayPath == "" {
		displayPath = "."
	}
	c.JSON(http.StatusOK, dirListing{Path: displayPath, Entries: result})
}

func (h *FileHandler) InspectFile(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPath := c.Query("path")
	full, err := access.safePath(relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		} else {
			respondInternalError(c, "FileHandler.InspectFile", err)
		}
		return
	}
	contentType, isText := inspectFileContent(full, info)
	c.JSON(http.StatusOK, fileInspection{
		Path:        relPath,
		Name:        filepath.Base(full),
		IsDir:       info.IsDir(),
		Size:        info.Size(),
		Modified:    info.ModTime(),
		ContentType: contentType,
		IsText:      isText,
	})
}

func inspectFileContent(path string, info os.FileInfo) (string, bool) {
	if info.IsDir() {
		return "inode/directory", false
	}
	if info.Size() == 0 {
		return "text/plain; charset=utf-8", true
	}
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		return contentType, isTextContentType(contentType)
	}

	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream", false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "application/octet-stream", false
	}
	sample := buf[:n]
	contentType := http.DetectContentType(sample)
	return contentType, isTextSample(sample) || isTextContentType(contentType)
}

func isTextContentType(contentType string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(base, "text/") {
		return true
	}
	switch base {
	case "application/json",
		"application/javascript",
		"application/x-javascript",
		"application/xml",
		"application/yaml",
		"application/x-yaml",
		"application/toml",
		"application/x-sh":
		return true
	default:
		return false
	}
}

func isTextSample(sample []byte) bool {
	if len(sample) == 0 {
		return true
	}
	if !utf8.Valid(sample) {
		return false
	}
	for _, b := range sample {
		if b == 0 {
			return false
		}
	}
	return true
}

// workspaceFileETag derives a validator from the file's stat. ModTime uses
// nanosecond precision because Last-Modified is only precise to one second;
// agents can rewrite a file more than once inside that window.
func workspaceFileETag(info os.FileInfo) string {
	return fmt.Sprintf(`W/"%x-%x"`, info.ModTime().UnixNano(), info.Size())
}

// setWorkspaceFileCacheHeaders lets private browser caches retain file bodies
// while requiring a cheap validator check before reuse.
func setWorkspaceFileCacheHeaders(c *gin.Context, info os.FileInfo) {
	c.Header("Cache-Control", "private, no-cache")
	c.Header("ETag", workspaceFileETag(info))
}

// etagMatches compares a client-supplied If-Match value against the current
// validator. The header may carry a comma-separated list, and RFC 9110 lets
// the weak "W/" prefix be dropped, so both forms are accepted — a browser
// that echoes back exactly what ReadFile sent and one that strips the prefix
// must both be treated as up to date.
func etagMatches(ifMatch, current string) bool {
	current = strings.TrimPrefix(current, "W/")
	for candidate := range strings.SplitSeq(ifMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == current {
			return true
		}
	}
	return false
}

func (h *FileHandler) ReadFile(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPath := c.Query("path")
	full, err := access.safePath(relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		} else {
			respondInternalError(c, "FileHandler.ReadFile", err)
		}
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is a directory"})
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(full))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	setWorkspaceFileCacheHeaders(c, info)
	c.File(full)
}

func (h *FileHandler) DownloadFile(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPath := c.Query("path")
	full, err := access.safePath(relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		} else {
			respondInternalError(c, "FileHandler.DownloadFile", err)
		}
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is a directory, use download-zip"})
		return
	}

	setWorkspaceFileCacheHeaders(c, info)
	c.FileAttachment(full, filepath.Base(full))
}

// maxEditableFileBytes caps in-browser editor saves. 10 MiB is well past
// any realistic source-file size and short of what the JSON request body
// would balloon into for true binaries — a hard literal here rather than a
// per-handler knob because the editor flow is single-purpose.
const maxEditableFileBytes = 10 * 1024 * 1024

// WriteFile overwrites (or creates) the file at the given relative path
// with the supplied UTF-8 text content. Used by the in-browser editor;
// directories and oversize payloads are rejected up front so partial writes
// can't leave the workspace in a half-state.
func (h *FileHandler) WriteFile(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if body.Path == "" || body.Path == "." {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}
	if int64(len(body.Content)) > maxEditableFileBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large"})
		return
	}

	full, err := access.safePath(body.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	// An agent may be rewriting this very file while a human has it open in
	// the editor. When the client sends the validator it read the file at,
	// a mismatch means the buffer is stale and saving it would silently
	// discard whatever landed in between — refuse and let the UI resolve it.
	// Absent header keeps the pre-existing last-write-wins behaviour.
	ifMatch := strings.TrimSpace(c.GetHeader("If-Match"))

	// Preserve mode if the file already exists; refuse to overwrite a
	// directory (the path looks file-shaped to safePath but a stat tells
	// us otherwise) — silently truncating one would be catastrophic.
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(full); statErr == nil {
		if info.IsDir() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "path is a directory"})
			return
		}
		if current := workspaceFileETag(info); ifMatch != "" && !etagMatches(ifMatch, current) {
			c.JSON(http.StatusPreconditionFailed, gin.H{
				"error": "file was modified since it was read",
				"etag":  current,
			})
			return
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		respondInternalError(c, "FileHandler.WriteFile", statErr)
		return
	} else {
		// If-Match on a path that no longer exists is still a precondition
		// failure: the client is saving against a version that has since
		// been deleted, and recreating it silently would undo that delete.
		if ifMatch != "" {
			c.JSON(http.StatusPreconditionFailed, gin.H{
				"error": "file no longer exists",
			})
			return
		}
		// Parent dir must already exist; we don't auto-mkdir on save
		// because the editor only opens existing files today.
		if _, err := os.Stat(filepath.Dir(full)); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "parent directory does not exist"})
			return
		}
	}

	// Write to a sibling temp file and rename so a crash mid-write
	// leaves the original intact instead of a truncated copy.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".daymug-write-*")
	if err != nil {
		respondInternalError(c, "FileHandler.WriteFile", err)
		return
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(body.Content); err != nil {
		_ = tmp.Close()
		respondInternalError(c, "FileHandler.WriteFile", err)
		return
	}
	if err := tmp.Close(); err != nil {
		respondInternalError(c, "FileHandler.WriteFile", err)
		return
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		respondInternalError(c, "FileHandler.WriteFile", err)
		return
	}
	if err := os.Rename(tmpName, full); err != nil {
		respondInternalError(c, "FileHandler.WriteFile", err)
		return
	}
	cleanup = false

	info, err := os.Stat(full)
	if err != nil {
		respondInternalError(c, "FileHandler.WriteFile", err)
		return
	}
	// Hand back the new validator so the editor can keep saving without a
	// round-trip through ReadFile after every write.
	c.JSON(http.StatusOK, gin.H{
		"path":     body.Path,
		"size":     info.Size(),
		"modified": info.ModTime(),
		"etag":     workspaceFileETag(info),
	})
}

func (h *FileHandler) Rename(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	}
	if !bindJSON(c, &body) {
		return
	}

	if body.OldPath == "" || body.OldPath == "." {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot rename root directory"})
		return
	}
	if body.NewPath == "" || body.NewPath == "." {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot rename to root directory"})
		return
	}

	oldFull, err := access.safePath(body.OldPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	newFull, err := access.safePath(body.NewPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if _, err := os.Stat(oldFull); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		} else {
			respondInternalError(c, "FileHandler.Rename", err)
		}
		return
	}

	if _, err := os.Stat(newFull); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "destination already exists"})
		return
	}

	if err := os.Rename(oldFull, newFull); err != nil {
		respondInternalError(c, "FileHandler.Rename", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *FileHandler) Delete(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPath := c.Query("path")
	if relPath == "" || relPath == "." {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot delete root directory"})
		return
	}
	full, err := access.safePath(relPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if _, err := os.Stat(full); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		} else {
			respondInternalError(c, "FileHandler.Delete", err)
		}
		return
	}

	if err := os.RemoveAll(full); err != nil {
		respondInternalError(c, "FileHandler.Delete", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *FileHandler) Mkdir(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		Path string `json:"path"`
	}
	if !bindJSON(c, &body) {
		return
	}

	if body.Path == "" || body.Path == "." {
		c.JSON(http.StatusForbidden, gin.H{"error": "path is required and cannot be root"})
		return
	}

	full, err := access.safePath(body.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if _, err := os.Stat(full); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "directory already exists"})
		return
	}

	if err := os.MkdirAll(full, 0o755); err != nil {
		respondInternalError(c, "FileHandler.Mkdir", err)
		return
	}
	c.Status(http.StatusCreated)
}

func (h *FileHandler) Move(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		SrcPath    string `json:"src_path"`
		DstPath    string `json:"dst_path"`
		OnConflict string `json:"on_conflict"`
	}
	if !bindJSON(c, &body) {
		return
	}

	switch body.OnConflict {
	case "", "error", "overwrite", "rename":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid on_conflict value"})
		return
	}

	srcFull, err := access.safePath(body.SrcPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	dstDirFull, err := access.safePath(body.DstPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	if _, err := os.Stat(srcFull); err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		} else {
			respondInternalError(c, "FileHandler.Move", err)
		}
		return
	}

	dstInfo, err := os.Stat(dstDirFull)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "destination directory not found"})
		} else {
			respondInternalError(c, "FileHandler.Move", err)
		}
		return
	}
	if !dstInfo.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "destination is not a directory"})
		return
	}

	finalDst := filepath.Join(dstDirFull, filepath.Base(srcFull))
	if _, err := os.Stat(finalDst); err == nil {
		switch body.OnConflict {
		case "overwrite":
			// Refuse to overwrite the source itself (e.g. moving within the same dir).
			if finalDst == srcFull {
				c.JSON(http.StatusConflict, gin.H{"error": "source and destination are the same"})
				return
			}
			if err := os.RemoveAll(finalDst); err != nil {
				respondInternalError(c, "FileHandler.Move", err)
				return
			}
		case "rename":
			finalDst = filepath.Join(dstDirFull, dedupName(dstDirFull, filepath.Base(srcFull)))
		default:
			c.JSON(http.StatusConflict, gin.H{"error": "destination file already exists"})
			return
		}
	}

	if err := os.Rename(srcFull, finalDst); err != nil {
		respondInternalError(c, "FileHandler.Move", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// dedupName returns a unique name in dir based on baseName by appending
// " (copy)", " (copy 2)", etc. when the name already exists.
func dedupName(dir, baseName string) string {
	candidate := baseName
	if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
		return candidate
	}

	ext := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)

	candidate = stem + " (copy)" + ext
	if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
		return candidate
	}

	for n := 2; ; n++ {
		candidate = fmt.Sprintf("%s (copy %d)%s", stem, n, ext)
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}

// copyDir recursively copies the directory at src into dstDir, preserving structure.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, fi.Mode())
		}
		return copyFile(path, target, fi.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck

	_, err = io.Copy(out, in)
	return err
}

func (h *FileHandler) Copy(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		SrcPath string `json:"src_path"`
		DstPath string `json:"dst_path"`
	}
	if !bindJSON(c, &body) {
		return
	}

	srcFull, err := access.safePath(body.SrcPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	dstDirFull, err := access.safePath(body.DstPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	srcInfo, err := os.Stat(srcFull)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		} else {
			respondInternalError(c, "FileHandler.Copy", err)
		}
		return
	}

	dstInfo, err := os.Stat(dstDirFull)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "destination directory not found"})
		} else {
			respondInternalError(c, "FileHandler.Copy", err)
		}
		return
	}
	if !dstInfo.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "destination is not a directory"})
		return
	}

	finalName := dedupName(dstDirFull, filepath.Base(srcFull))
	finalDst := filepath.Join(dstDirFull, finalName)

	if srcInfo.IsDir() {
		if err := copyDir(srcFull, finalDst); err != nil {
			respondInternalError(c, "FileHandler.Copy", err)
			return
		}
	} else {
		if err := copyFile(srcFull, finalDst, srcInfo.Mode()); err != nil {
			respondInternalError(c, "FileHandler.Copy", err)
			return
		}
	}

	rel, err := filepath.Rel(access.workDir, finalDst)
	if err != nil {
		rel = finalName
	}
	c.JSON(http.StatusCreated, gin.H{"path": rel})
}
