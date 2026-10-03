package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
)

const (
	// DefaultUploadMaxBytes is the per-file cap for uploads when the
	// handler hasn't set MaxBytes explicitly. 25 MiB is well above what
	// a clipboard screenshot will produce while still bounding worst-case
	// memory on the multipart parser.
	DefaultUploadMaxBytes = 25 << 20
	// DaymugDir is the single namespace for everything DayMug manages on
	// behalf of an agent: web uploads, inbound IM attachments, thread cases.
	//
	// It hangs off the owning human's home directory, not off whatever
	// directory the conversation happens to be running in. A user with twenty
	// project directories otherwise accumulates twenty `.daymug/` folders,
	// each holding a handful of screenshots, and every one of them shows up as
	// noise in the project it was dropped into — including in `git status`.
	DaymugDir = ".daymug"

	// agentsSegment separates the per-agent scopes from anything the home
	// root's `.daymug/` might hold directly, so the namespace can grow a
	// non-agent entry later without ambiguity.
	agentsSegment = "agents"

	// uploadsSegment holds files pasted, dropped, or picked in the web
	// composer.
	uploadsSegment = "uploads"

	// CaseSegment holds the materialized thread cases (`<channel>-<thread>.md`
	// plus their `.version` siblings). Exported because the IM bridge owns the
	// file format but not the directory layout.
	CaseSegment = "case"

	// imAttachmentsFallbackPlatform names the directory for a connector that
	// reports no platform. Sorting such files into the agent scope itself
	// would collide with the per-platform directories.
	imAttachmentsFallbackPlatform = "im"
)

// ErrNoAgentScope reports an agent id that yields no usable path segment.
// Callers must fail the write rather than fall back to a shared directory:
// silently pooling two agents' files is the bug this scoping exists to avoid.
var ErrNoAgentScope = errors.New("agent id has no usable path segment")

// AgentScopeDir returns the home-root-relative, slash-separated directory that
// holds everything DayMug manages for one agent: `.daymug/agents/<agent id>`.
//
// The scope is keyed by the agent's id rather than its name because the ids
// are immutable: a rename would otherwise strand every path already recorded
// in message history in a directory nothing writes to any more. Ids are UUIDs,
// which reads badly, but this lives under a dot directory the file browser
// hides — nobody navigates it by eye.
func AgentScopeDir(agentID string) (string, error) {
	segment := SanitizeUploadSegment(strings.TrimSpace(agentID))
	segment = strings.Trim(segment, ".-")
	if segment == "" {
		return "", fmt.Errorf("%w: %q", ErrNoAgentScope, agentID)
	}
	return DaymugDir + "/" + agentsSegment + "/" + segment, nil
}

// AgentUploadsDir returns the agent-scoped directory where pasted/dropped/
// uploaded files from the web composer land.
func AgentUploadsDir(agentID string) (string, error) {
	scope, err := AgentScopeDir(agentID)
	if err != nil {
		return "", err
	}
	return scope + "/" + uploadsSegment, nil
}

// AgentIMAttachmentsDir returns the agent-scoped directory inbound attachments
// from platform land in (`…/slack`, `…/feishu`, …).
//
// Split per platform rather than pooled with web uploads: these are the inbound
// half of a channel, and an agent reasoning about "what did Slack send me"
// wants them grouped by where they came from.
//
// The platform is sanitized because it reaches this function from connector
// configuration, and it is the only caller-supplied path segment here.
func AgentIMAttachmentsDir(agentID, platform string) (string, error) {
	scope, err := AgentScopeDir(agentID)
	if err != nil {
		return "", err
	}
	segment := SanitizeUploadSegment(strings.ToLower(strings.TrimSpace(platform)))
	segment = strings.Trim(segment, ".-")
	if segment == "" {
		segment = imAttachmentsFallbackPlatform
	}
	return scope + "/" + segment, nil
}

// DaymugPath joins a home-root-relative directory from the helpers above onto
// an absolute home root. Separate from the helpers so the slash-to-separator
// conversion happens in exactly one place rather than at each call site.
func DaymugPath(homeRoot, relDir string) string {
	return filepath.Join(homeRoot, filepath.FromSlash(relDir))
}

// AgentHomeOwner returns the human whose home directory anchors an agent's
// DayMug scope. Its work_dir is the authorization boundary the file API
// already enforces (see handler.getUserFileAccess), and its id is what the
// read URLs for those files must address — a scope path resolved against the
// agent's own narrower work_dir would not be found.
//
// An agent whose owner cannot be resolved — a dangling owner_id — gets an
// error rather than a fallback. Writing into the agent's own work_dir instead
// would put files somewhere no read path looks for them, which is worse than
// refusing the write and saying so.
func AgentHomeOwner(ctx context.Context, s store.Store, agentUser store.User) (store.User, error) {
	if s == nil {
		return store.User{}, errors.New("no store to resolve the agent's home owner")
	}
	owner, err := s.GetOwner(ctx, agentUser)
	if err != nil {
		return store.User{}, fmt.Errorf("resolve owner of agent %s: %w", agentUser.ID, err)
	}
	if strings.TrimSpace(owner.WorkDir) == "" {
		return store.User{}, fmt.Errorf("owner %s of agent %s has no work_dir", owner.ID, agentUser.ID)
	}
	return owner, nil
}

// AttachAgentScopeBind makes an agent's DayMug scope reachable from inside its
// jail.
//
// A jailed run only sees its JailRoot, which is the conversation's own working
// directory — a project somewhere below the owner's home. The scope holding
// that turn's attachments and its thread case sits beside that project, not
// inside it, so without an explicit bind every path DayMug hands the agent
// would be an ENOENT in the namespace even though it exists on the host. The
// bind maps the directory at its own path, so the absolute paths in the prompt
// stay literally true on both sides.
//
// The directory is created first: bwrap's --bind-try silently skips a source
// that does not exist, which would turn "the jail can reach it" into "the jail
// quietly cannot" on an agent's very first turn.
//
// Best effort throughout. Every failure is logged and leaves the run otherwise
// intact — an unreachable attachment degrades one turn, while refusing to start
// the agent degrades all of them.
func AttachAgentScopeBind(ctx context.Context, st store.Store, opts *agent.RunRequest, agentUser store.User) {
	if opts == nil || st == nil || opts.Unrestricted {
		return
	}
	homeOwner, err := AgentHomeOwner(ctx, st, agentUser)
	if err != nil {
		log.Printf("[sandbox] no DayMug scope bind for agent %s: %v", agentUser.ID, err)
		return
	}
	scope, err := AgentScopeDir(agentUser.ID)
	if err != nil {
		log.Printf("[sandbox] no DayMug scope bind for agent %s: %v", agentUser.ID, err)
		return
	}
	dir := DaymugPath(homeOwner.WorkDir, scope)
	// Already inside the jail (the conversation runs straight out of the home
	// root): binding it again would be redundant.
	if opts.JailRoot != "" {
		if within, err := PathWithin(opts.JailRoot, dir); err == nil && within {
			return
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[sandbox] create DayMug scope %s: %v", dir, err)
		return
	}
	opts.ExtraBinds = append(opts.ExtraBinds, agent.BindMount{Source: dir, Writable: true})
}

// safeNamePattern keeps a conservative subset of the file name so the on-disk
// path can't break out of the uploads dir or confuse a downstream tool. The
// timestamp prefix already guarantees ordering, and we keep dots so the
// extension drives MIME detection later.
var safeNamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// SanitizeUploadSegment replaces every run of characters outside the safe set
// with a single dash, yielding a name that is safe to use as one path segment.
func SanitizeUploadSegment(s string) string {
	return safeNamePattern.ReplaceAllString(s, "-")
}

// HasUnsafeUploadChars reports whether s contains any character that
// SanitizeUploadSegment would rewrite.
func HasUnsafeUploadChars(s string) bool {
	return safeNamePattern.MatchString(s)
}

// OpenUniqueUpload creates a new file under uploadsDir following the
// "<timestamp>-<base>" naming scheme, suffixing "-2", "-3", … before the
// extension on collision until O_EXCL succeeds. Returns the on-disk name,
// full path, and the open writer; the caller closes the writer.
func OpenUniqueUpload(uploadsDir, timestamp, cleanBase string) (string, string, *os.File, error) {
	ext := filepath.Ext(cleanBase)
	stem := strings.TrimSuffix(cleanBase, ext)
	for attempt := 1; attempt < 1024; attempt++ {
		name := timestamp + "-" + cleanBase
		if attempt > 1 {
			name = fmt.Sprintf("%s-%s-%d%s", timestamp, stem, attempt, ext)
		}
		dst := filepath.Join(uploadsDir, name)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err == nil {
			return name, dst, f, nil
		}
		if !os.IsExist(err) {
			return "", "", nil, err
		}
	}
	return "", "", nil, fmt.Errorf("upload name space exhausted under %s", uploadsDir)
}

// ExtensionFromMIME returns a canonical file extension for the MIMEs we
// expect to see most often when the original filename has been stripped to
// nothing by the charset filter. Falls back to the empty string when the
// mime is unrecognised — the caller will end up with a name like
// "<timestamp>-file" which is harmless but odd-looking.
func ExtensionFromMIME(mime string) string {
	// http.DetectContentType emits the MIME with charset/parameter
	// suffixes (e.g. "text/plain; charset=utf-8") — strip them so the
	// switch keys stay clean.
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/bmp":
		return ".bmp"
	case "application/pdf":
		return ".pdf"
	case "application/zip":
		return ".zip"
	case "application/json":
		return ".json"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/csv":
		return ".csv"
	case "text/markdown":
		return ".md"
	default:
		return ""
	}
}
