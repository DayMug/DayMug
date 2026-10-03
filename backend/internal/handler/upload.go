package handler

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/service"
)

// UploadHandler accepts user-attached files of any format from the chat
// composer and stores them under the conversation agent's DayMug scope in
// its owner's home directory — `<home>/.daymug/agents/<agent id>/uploads/`.
//
// Two earlier layouts are worth not repeating. The first pooled every agent
// of one human into a single owner-rooted directory and bind-mounted it into
// each sandbox at a fixed path, so the host path did not match what the CLI
// saw and references were easy to write but impossible to open. The second
// wrote into the conversation's own cwd, which made the path literally true
// everywhere but scattered a `.daymug/` folder across every project the user
// ever opened a chat in.
//
// This layout keeps the honesty of the second — the path handed to the agent
// is an absolute path that exists, unrewritten, on the host and inside the
// bwrap jail (see prompt_runner's ExtraBinds) — while the agent-id segment
// supplies the isolation the first one lacked.
type UploadHandler struct {
	Store store.Store
	// MaxBytes caps a single upload. Set per-handler so tests can shrink
	// it without touching production code; zero means use DefaultMaxBytes.
	MaxBytes int64
}

func NewUploadHandler(s store.Store) *UploadHandler {
	return &UploadHandler{Store: s}
}

// Create accepts a single file in the multipart form field "file" and
// writes it to the conversation agent's uploads directory. Form fields:
//   - file: the upload itself (required, any MIME type)
//   - conversation_id: which chat the upload belongs to (required;
//     resolves the agent, and through it the owner's home root)
//
// Returns two paths, because the two consumers need different anchors:
// `ref` is the absolute path the composer embeds in the outgoing message
// (the agent's cwd is a project directory, so nothing relative would
// resolve), and `path` is the home-root-relative form the file API reads
// back. Bounded only by MaxBytes; the per-agent scope isolates one agent's
// uploads from another's.
func (h *UploadHandler) Create(c *gin.Context) {
	maxBytes := h.MaxBytes
	if maxBytes <= 0 {
		maxBytes = service.DefaultUploadMaxBytes
	}
	// LimitReader on the request body: protects the multipart parser from
	// a hostile client that lies about Content-Length.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)

	uid := middleware.CurrentUserID(c)
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	caller, err := h.Store.GetUser(c.Request.Context(), uid)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return
	}

	// Resolve the target directory: prefer the conversation's locked
	// work_dir (set on first input run), fall back to the conversation's
	// agent's user.work_dir for chats that haven't sent a message yet.
	// Without conversation_id we have no way to know which agent's cwd
	// owns the upload — refuse rather than guess (the previous
	// owner-rooted path collapsed all agents into one shared dir, which
	// is exactly what we're moving away from).
	convID := strings.TrimSpace(c.PostForm("conversation_id"))
	if convID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'conversation_id' form field"})
		return
	}
	conv, err := h.Store.GetConversation(c.Request.Context(), convID)
	if err != nil {
		respondStoreError(c, err, "conversation not found")
		return
	}
	// Same access rule the WS init handler uses: a human can only upload
	// into a conversation they own (or one owned by an agent under the
	// same human). Reused via canAccessOwner so any future tightening
	// flows through one place.
	if !service.CanAccessOwner(c.Request.Context(), h.Store, caller.ID, conv.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "conversation not accessible"})
		return
	}
	// The agent owning this conversation supplies both halves of the write
	// target: its id names the scope directory, its owner's work_dir is the
	// home root that scope hangs off.
	agentUser, err := h.Store.GetUser(c.Request.Context(), conv.UserID)
	if err != nil {
		respondStoreError(c, err, "conversation owner not found")
		return
	}
	homeOwner, err := service.AgentHomeOwner(c.Request.Context(), h.Store, agentUser)
	if err != nil {
		log.Printf("[upload] resolve home owner for conv %s: %v", conv.ID, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation has no home directory configured"})
		return
	}
	uploadsRel, err := service.AgentUploadsDir(agentUser.ID)
	if err != nil {
		log.Printf("[upload] scope conv %s: %v", conv.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent has no upload directory"})
		return
	}
	// Where the file lands no longer depends on the conversation's cwd, but
	// the first upload still pins an unlocked conversation to the directory
	// it will run in — that pinning is what the composer's work-dir picker
	// expects (ChatPage.lockWorkDirAfterUpload) and it decides which project
	// the agent is about to read the attachment *into*.
	needsLock := conv.WorkDir == ""
	convWorkDir := conv.WorkDir
	if convWorkDir == "" {
		convWorkDir = agentUser.WorkDir
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'file' form field"})
		return
	}
	if fileHeader.Size <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty file"})
		return
	}
	if fileHeader.Size > maxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("file exceeds %d bytes", maxBytes)})
		return
	}

	// Sniff MIME from the first 512 bytes of content so the response can
	// report what the server actually thinks the file is — useful for the
	// composer chip and for debugging "claude can't read this attachment"
	// reports. We don't gate on type any more: chat is general-purpose and
	// users legitimately attach PDFs, logs, archives, source files, etc.
	src, err := fileHeader.Open()
	if err != nil {
		respondInternalErrorMessage(c, "UploadHandler.Create", err, "failed to open uploaded file")
		return
	}
	defer func() { _ = src.Close() }()

	sniff := make([]byte, 512)
	n, _ := io.ReadFull(src, sniff)
	mime := http.DetectContentType(sniff[:n])
	// Rewind for the actual write — we already consumed the sniff window.
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		respondInternalErrorMessage(c, "UploadHandler.Create", err, "failed to rewind uploaded file")
		return
	}

	uploadsDir := service.DaymugPath(homeOwner.WorkDir, uploadsRel)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		respondInternalErrorMessage(c, "UploadHandler.Create", err, "failed to create uploads dir")
		return
	}
	// Same jail escape the IM ingest path guards: MkdirAll accepts an
	// existing symlink, so the scope directory could point anywhere. Check
	// the resolved form before writing a byte.
	if within, err := service.PathWithin(homeOwner.WorkDir, uploadsDir); err != nil || !within {
		c.JSON(http.StatusForbidden, gin.H{"error": "uploads directory resolves outside the home directory"})
		return
	}
	// Same opportunistic retention sweep the IM ingest path runs: this
	// directory only ever grows, and a write is the one moment we know it
	// exists without walking every agent's scope.
	service.MaybePruneUploads(uploadsDir)

	// Filename: <YYYYMMDDHHMMSS>-<sanitized basename>. The timestamp prefix
	// puts the upload time front-and-centre when the user lists the
	// directory, gives the file a chronological sort, and keeps the
	// human-meaningful basename intact. Sub-second collisions (two pastes
	// in the same second) get suffixed with -2, -3, … inside the
	// O_EXCL retry loop below.
	rawBase := filepath.Base(fileHeader.Filename)
	if rawBase == "" || rawBase == "." || rawBase == "/" {
		rawBase = "file"
	}
	// Capture the original extension *before* sanitization so a name like
	// "图片.pdf" still ends up with `.pdf` once the regex strips the CJK
	// stem. The extension itself runs through the same charset filter to
	// stop a hostile filename from smuggling shell-meaningful characters
	// in via the dotted suffix.
	rawExt := filepath.Ext(rawBase)
	if rawExt != "" && service.HasUnsafeUploadChars(rawExt) {
		rawExt = ""
	}
	cleanBase := service.SanitizeUploadSegment(rawBase)
	cleanBase = strings.Trim(cleanBase, "-.")
	if cleanBase == "" {
		// Fallback when the original name is entirely outside the safe
		// charset (e.g. a Chinese clipboard filename). Prefer the
		// original extension; fall back to a MIME-derived guess so the
		// stored file is at least openable by a default app.
		ext := rawExt
		if ext == "" {
			ext = service.ExtensionFromMIME(mime)
		}
		cleanBase = "file" + ext
	} else if filepath.Ext(cleanBase) == "" {
		if rawExt != "" {
			cleanBase += rawExt
		} else {
			cleanBase += service.ExtensionFromMIME(mime)
		}
	}
	timestamp := time.Now().Format("20060102150405")
	storedName, dst, out, err := service.OpenUniqueUpload(uploadsDir, timestamp, cleanBase)
	if err != nil {
		respondInternalErrorMessage(c, "UploadHandler.Create", err, "failed to create file")
		return
	}
	defer func() { _ = out.Close() }()

	written, err := io.Copy(out, src)
	if err != nil {
		// Best-effort cleanup on partial write so a failed upload doesn't
		// leave a torn file lying around. The error is intentionally
		// swallowed — the original copy error is more useful.
		_ = os.Remove(dst)
		respondInternalErrorMessage(c, "UploadHandler.Create", err, "failed to write file")
		return
	}

	// Pin the conversation to the cwd it is about to run in. Mirrors
	// LockConversationWorkDir's "first writer wins" semantics — failure here
	// is logged but does not fail the upload itself; the file is on disk and
	// the user has already seen the chip light up green.
	if needsLock && convWorkDir != "" {
		if err := h.Store.UpdateConversationWorkDir(c.Request.Context(), conv.ID, convWorkDir); err != nil {
			log.Printf("[upload] lock conv %s work_dir to %s: %v", conv.ID, convWorkDir, err)
		}
	}

	// `ref` is absolute because the agent's cwd is a project directory while
	// the file sits in the owner's home — no relative form is true from both
	// ends, and a relative one computed for the current cwd would break the
	// moment the conversation is pointed somewhere else. The composer inserts
	// it verbatim; the CLI opens exactly this path, on the host and inside the
	// jail alike.
	//
	// `path` is the same file addressed for the file API, which resolves
	// against the home owner — hence the owner's id in the URL rather than the
	// agent's.
	wirePath := uploadsRel + "/" + storedName
	c.JSON(http.StatusCreated, gin.H{
		"ref":             dst,
		"path":            wirePath,
		"name":            rawBase,
		"size":            written,
		"mime":            mime,
		"url":             "/api/users/" + url.PathEscape(homeOwner.ID) + "/files/read?path=" + url.QueryEscape(wirePath),
		"work_dir":        convWorkDir,
		"work_dir_locked": true,
	})
}
