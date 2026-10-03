package handler

import (
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/store"
)

type BrowseHandler struct {
	Store store.Store
}

func NewBrowseHandler(s store.Store) *BrowseHandler {
	return &BrowseHandler{Store: s}
}

type browseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type browseResult struct {
	Current string        `json:"current"`
	Parent  string        `json:"parent"`
	Dirs    []browseEntry `json:"dirs"`
}

// browseScope is where a caller's picker opens and whether it is confined
// there.
type browseScope struct {
	home   string
	jailed bool
}

// callerScope resolves the picker scope for the authenticated caller. A
// regular user is jailed to their own work_dir: new agents must inherit a
// work_dir within their human owner's home, so the picker that backs that
// form is confined to the same root. Admins open at their work_dir but may
// walk the whole host — they assign other users' work_dirs, which can live
// anywhere, and already hold a host shell via the admin terminal.
//
// Returns ok=false (and no JSON written) when no auth context is set —
// production always wires this behind RequireAuth, the bypass is for unit
// tests that exercise the handler in isolation.
func (h *BrowseHandler) callerScope(c *gin.Context) (browseScope, bool) {
	authedUID := middleware.CurrentUserID(c)
	if authedUID == "" {
		return browseScope{}, false
	}
	if h.Store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store unavailable"})
		return browseScope{}, false
	}
	caller, err := h.Store.GetUser(c.Request.Context(), authedUID)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return browseScope{}, false
	}
	if caller.WorkDir == "" {
		if caller.IsAdmin {
			return browseScope{home: serverHome()}, true
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "your account has no work_dir"})
		return browseScope{}, false
	}
	abs, err := filepath.Abs(filepath.Clean(caller.WorkDir))
	if err != nil {
		respondStoreError(c, err, "user not found")
		return browseScope{}, false
	}
	return browseScope{home: abs, jailed: !caller.IsAdmin}, true
}

func serverHome() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return "/"
}

// BrowseDirs lists subdirectories at a given absolute path. Used for the
// directory picker that drives agent creation and admin work_dir edits.
// The empty default lands on the caller's home; for jailed callers, walks
// are confined to that home and requests outside it are rejected.
func (h *BrowseHandler) BrowseDirs(c *gin.Context) {
	dirPath := c.Query("path")
	scope, ok := h.callerScope(c)
	if !ok {
		if c.Writer.Written() {
			return
		}
		// No auth context — legacy unjailed "show $HOME" behavior so unit
		// tests that bypass middleware keep working.
		scope = browseScope{home: serverHome()}
	}
	if dirPath == "" {
		dirPath = scope.home
	}
	abs, err := filepath.Abs(filepath.Clean(dirPath))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if scope.jailed && !isPathWithin(scope.home, abs) {
		c.JSON(http.StatusForbidden, gin.H{"error": "path is outside your working directory"})
		return
	}
	dirPath = abs

	dirPath = filepath.Clean(dirPath)

	info, err := os.Stat(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "directory not found"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}
	if !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is not a directory"})
		return
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot read directory"})
		return
	}

	dirs := make([]browseEntry, 0)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip hidden directories
		if e.Name() != "" && e.Name()[0] == '.' {
			continue
		}
		dirs = append(dirs, browseEntry{
			Name: e.Name(),
			Path: filepath.Join(dirPath, e.Name()),
		})
	}
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name < dirs[j].Name
	})

	parent := filepath.Dir(dirPath)
	if parent == dirPath {
		parent = ""
	}
	// Don't expose paths above the caller's root; the UI uses this to
	// disable the "Up" button at the top of the jail.
	if scope.jailed && parent != "" && !isPathWithin(scope.home, parent) {
		parent = ""
	}

	c.JSON(http.StatusOK, browseResult{
		Current: dirPath,
		Parent:  parent,
		Dirs:    dirs,
	})
}

// MkdirBrowseDir creates a new directory at {path}/{name}. Confined the
// same way the picker walks — a jailed user can scaffold a new agent's home
// but never plant a folder elsewhere on the host. The name is rejected if it contains path separators or starts
// with a dot, so it can never escape the parent and never produces a
// hidden folder we'd then hide from the listing.
func (h *BrowseHandler) MkdirBrowseDir(c *gin.Context) {
	var body struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if !bindJSON(c, &body) {
		return
	}

	parent := strings.TrimSpace(body.Path)
	name := strings.TrimSpace(body.Name)
	if parent == "" || name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path and name are required"})
		return
	}
	// Reject anything that could escape the parent or land us in a hidden
	// folder. The picker UI also filters dotfiles, so a "." prefix would
	// create an invisible directory — confusing for the user.
	if strings.ContainsAny(name, `/\`) ||
		name == "." || name == ".." ||
		strings.HasPrefix(name, ".") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid folder name"})
		return
	}

	parent = filepath.Clean(parent)
	if scope, ok := h.callerScope(c); ok {
		abs, err := filepath.Abs(parent)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if scope.jailed && !isPathWithin(scope.home, abs) {
			c.JSON(http.StatusForbidden, gin.H{"error": "path is outside your working directory"})
			return
		}
		parent = abs
	} else if c.Writer.Written() {
		return
	}

	parentInfo, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "parent directory not found"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		}
		return
	}
	if !parentInfo.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parent is not a directory"})
		return
	}

	target := filepath.Join(parent, name)
	if _, err := os.Stat(target); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "directory already exists"})
		return
	} else if !os.IsNotExist(err) {
		respondInternalError(c, "BrowseHandler.MkdirBrowseDir", err)
		return
	}

	if err := os.Mkdir(target, 0o755); err != nil {
		respondInternalError(c, "BrowseHandler.MkdirBrowseDir", err)
		return
	}

	c.JSON(http.StatusCreated, browseEntry{Name: name, Path: target})
}
