package handler

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// SessionBundleHandler exports one conversation's on-disk evidence as a zip:
// the CLI's own session log plus the environment that produced it. It exists
// because the messages DayMug stores are a *subtraction* — they keep what the
// chat UI renders. Tool call arguments, raw tool results, thinking blocks and
// MCP attribution only ever live in the CLI's JSONL, outside the database and
// outside work_dir.
//
// Admin-only, and the gate is the route group (RequireAdmin), not a check in
// here. Two reasons the export cannot be opened up:
//
//   - it reads the account-level config dir, and codex's config.toml lists
//     every project path on the host — i.e. every other user's directory;
//   - it exports *other people's* conversations by design, so canAccessOwner
//     is the wrong question to ask.
//
// Pure credential stores are excluded even from admins: the bundle's whole
// purpose is to be handed to an AI, which takes it outside the trust boundary,
// and a refresh token has no diagnostic value. See bundleCredentialFiles.
type SessionBundleHandler struct {
	Store    store.Store
	Cfg      *config.Config
	Pool     *service.Pool
	Backends *service.BackendRegistry
	Version  string

	// MaxFileBytes caps every single file copied into the archive. Zero uses
	// defaultBundleFileMax. Oversize files are truncated from the *head*
	// rather than dropped: a JSONL grows by append, so the head holds the
	// session's opening turns — the part that explains how it went wrong.
	MaxFileBytes int64
}

func NewSessionBundleHandler(s store.Store, cfg *config.Config, pool *service.Pool, backends *service.BackendRegistry, version string) *SessionBundleHandler {
	return &SessionBundleHandler{Store: s, Cfg: cfg, Pool: pool, Backends: backends, Version: version}
}

// defaultBundleFileMax bounds a single archived file. Session logs for long
// conversations genuinely reach tens of MB, so the cap is generous; it exists
// to stop one pathological file from turning a download into an outage.
const defaultBundleFileMax = 64 << 20

// bundleWorkdirFiles are the project-level config files an agent reads from
// the conversation's work_dir. Both CLI types get the same list on purpose: a
// claude conversation with a stray AGENTS.md is itself worth seeing.
var bundleWorkdirFiles = []string{
	"CLAUDE.md",
	"AGENTS.md",
	".mcp.json",
	".claude/settings.json",
	".claude/settings.local.json",
}

var bundleWorkdirGlobs = []string{
	".claude/agents/*.md",
	".claude/commands/*.md",
}

// bundleConfigFiles are the account-level files worth exporting, per CLI type.
// This is the admin-only half of the bundle.
var bundleConfigFiles = map[string][]string{
	config.CLITypeClaude:           {"settings.json", ".claude.json"},
	config.CLITypeClaudeCompatible: {"settings.json", ".claude.json"},
	config.CLITypeCodex:            {"config.toml"},
	config.CLITypeOpenAICompatible: {"config.toml"},
}

// bundleCredentialFiles never enter the archive. They are enumerated rather
// than simply omitted so the MANIFEST can report "present, excluded" — silence
// would read as "this deploy has no credential file here", which is a very
// different conclusion for someone debugging an auth failure.
var bundleCredentialFiles = map[string][]string{
	config.CLITypeClaude:           {".credentials.json"},
	config.CLITypeClaudeCompatible: {".credentials.json"},
	config.CLITypeCodex:            {"auth.json"},
	config.CLITypeOpenAICompatible: {"auth.json"},
}

type sessionBundleMeta struct {
	Conversation sessionBundleConversation `json:"conversation"`
	User         sessionBundleUser         `json:"user"`
	Account      sessionBundleAccount      `json:"account"`
	Server       sessionBundleServer       `json:"server"`
	Resolved     sessionBundleResolved     `json:"resolved"`
}

type sessionBundleConversation struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	UserID       string    `json:"user_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	SessionID    string    `json:"session_id"`
	AccountName  string    `json:"account_name"`
	WorkDir      string    `json:"work_dir"`
	Pinned       bool      `json:"pinned"`
	Shared       bool      `json:"shared"`
	UserMessages int       `json:"user_messages"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type sessionBundleUser struct {
	ID               string              `json:"id"`
	Username         string              `json:"username"`
	IsAdmin          bool                `json:"is_admin"`
	SandboxMode      string              `json:"sandbox_mode"`
	WorkDir          string              `json:"work_dir"`
	ProviderBindings map[string]string   `json:"provider_bindings,omitempty"`
	ProviderAccounts map[string][]string `json:"provider_accounts,omitempty"`
}

// sessionBundleAccount reports the credentials the conversation actually runs
// under, resolved through the same chain the prompt path walks. Error is
// populated instead of the rest when the binding is broken — a revoked account
// is itself a class of bot failure, so it belongs in the bundle rather than
// aborting it.
type sessionBundleAccount struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	ConfigDir string `json:"config_dir"`
	Error     string `json:"error,omitempty"`
}

type sessionBundleServer struct {
	Version        string `json:"version"`
	SandboxEnabled bool   `json:"sandbox_enabled"`
}

// sessionBundleResolved pairs the path the adapter *computes* with what is
// actually on disk. When these disagree the conversation has drifted off its
// original cwd and `--resume` is failing (docs/architecture/working-directory.md) — the
// single most valuable fact in the bundle, and invisible from the log itself.
type sessionBundleResolved struct {
	SessionLogPath string     `json:"session_log_path"`
	Exists         bool       `json:"exists"`
	Size           int64      `json:"size"`
	ModTime        *time.Time `json:"mod_time,omitempty"`
}

// bundleEntry is one file that made it into the archive.
type bundleEntry struct {
	zipPath   string
	source    string
	size      int64
	truncated int64
}

// bundleSkip is something expected but absent from the archive. Every skip
// must reach the MANIFEST; a quietly incomplete bundle sends an investigation
// in the wrong direction.
type bundleSkip struct {
	source string
	reason string
}

type bundleSource struct {
	zipPath string
	disk    string
}

type bundlePlan struct {
	root    string
	meta    sessionBundleMeta
	sources []bundleSource
	skips   []bundleSkip
	maxFile int64
}

// Download serves GET /api/admin/conversations/:id/session-bundle.
//
// The archive streams straight into c.Writer, so once the first header is out
// there is no way back to a JSON error body. Everything that can fail the
// request — lookup, validation, account resolution, path discovery — happens
// in resolve() before a single byte is written.
func (h *SessionBundleHandler) Download(c *gin.Context) {
	plan, ok := h.resolve(c)
	if !ok {
		return
	}

	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, plan.root+".zip"))
	writeBundle(c.Writer, plan)
}

func (h *SessionBundleHandler) resolve(c *gin.Context) (*bundlePlan, bool) {
	convID := c.Param("id")
	if convID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation id required"})
		return nil, false
	}
	conv, err := h.Store.GetConversation(c.Request.Context(), convID)
	if err != nil {
		respondStoreError(c, err, "conversation not found")
		return nil, false
	}
	// No session id and no work dir means the conversation never sent a
	// message, so nothing was ever written to disk. This is the ONLY 400:
	// a computed-but-missing log file still gets exported, because that is
	// exactly what cwd drift looks like.
	if conv.SessionID == "" || conv.WorkDir == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "conversation has no on-disk session yet (no message has been sent)",
		})
		return nil, false
	}

	plan := &bundlePlan{
		root:    "daymug-session-" + conv.ID,
		maxFile: h.MaxFileBytes,
	}
	if plan.maxFile <= 0 {
		plan.maxFile = defaultBundleFileMax
	}

	user, userErr := h.Store.GetUser(c.Request.Context(), conv.UserID)
	acct, configDir := h.resolveAccount(user, conv, userErr)

	logPath, logSkip := h.sessionLogPath(conv, configDir)
	resolved := sessionBundleResolved{SessionLogPath: logPath}
	if logPath != "" {
		if info, statErr := os.Stat(logPath); statErr == nil {
			resolved.Exists = true
			resolved.Size = info.Size()
			mod := info.ModTime()
			resolved.ModTime = &mod
			plan.sources = append(plan.sources, bundleSource{
				zipPath: "session/" + filepath.Base(logPath),
				disk:    logPath,
			})
		} else {
			plan.skips = append(plan.skips, bundleSkip{
				source: logPath,
				reason: "session log not found on disk (conversation may have drifted off its original work_dir)",
			})
		}
	} else if logSkip != "" {
		plan.skips = append(plan.skips, bundleSkip{source: conv.SessionID, reason: logSkip})
	}

	plan.sources, plan.skips = appendWorkdirSources(plan.sources, plan.skips, conv.WorkDir)
	plan.sources, plan.skips = appendConfigSources(plan.sources, plan.skips, configDir, conv.Provider)

	msgCount, _ := h.Store.CountUserMessages(c.Request.Context(), conv.ID)

	plan.meta = sessionBundleMeta{
		Conversation: sessionBundleConversation{
			ID: conv.ID, Title: conv.Title, UserID: conv.UserID,
			Provider: conv.Provider, Model: conv.Model, SessionID: conv.SessionID,
			AccountName: conv.AccountName, WorkDir: conv.WorkDir,
			Pinned: conv.Pinned, Shared: conv.Shared, UserMessages: msgCount,
			CreatedAt: conv.CreatedAt, UpdatedAt: conv.UpdatedAt,
		},
		User: sessionBundleUser{
			ID: user.ID, Username: user.Username, IsAdmin: user.IsAdmin,
			SandboxMode: user.SandboxMode, WorkDir: user.WorkDir,
			ProviderBindings: user.ProviderBindings, ProviderAccounts: user.ProviderAccounts,
		},
		Account:  acct,
		Server:   sessionBundleServer{Version: h.Version, SandboxEnabled: h.Cfg != nil && h.Cfg.Sandbox.Enabled},
		Resolved: resolved,
	}
	return plan, true
}

// resolveAccount re-walks the chain the prompt path uses, so the bundle
// reports the account the conversation genuinely runs on rather than a guess.
// Any failure becomes Account.Error and the export continues without a config
// dir — an unresolvable binding is a finding, not a reason to refuse.
func (h *SessionBundleHandler) resolveAccount(user store.User, conv store.Conversation, userErr error) (sessionBundleAccount, string) {
	out := sessionBundleAccount{Type: conv.Provider}
	if userErr != nil {
		out.Error = fmt.Sprintf("load owner %s: %v", conv.UserID, userErr)
		return out, ""
	}
	if h.Pool == nil {
		out.Error = "no account pool wired"
		return out, ""
	}
	acc, err := service.ResolveRunAccount(h.Pool, user, conv.Provider, conv.AccountName)
	if err != nil {
		out.Error = err.Error()
		return out, ""
	}
	out.Name = acc.Name
	out.Type = acc.Type
	out.ConfigDir = acc.ConfigDir
	return out, acc.ConfigDir
}

// sessionLogPath asks the conversation's adapter where its log lives. The
// handler stays ignorant of claude's per-cwd encoding and codex's dated
// rollout tree; both answer through the same interface method.
func (h *SessionBundleHandler) sessionLogPath(conv store.Conversation, configDir string) (path, skipReason string) {
	backend, ok := h.Backends.Lookup(conv.Provider)
	if !ok {
		return "", fmt.Sprintf("no backend registered for provider %q", conv.Provider)
	}
	p := backend.SessionLogPath(conv.WorkDir, conv.SessionID, configDir)
	if p == "" {
		// Codex cannot predict its rollout path until the first line lands.
		return "", "adapter could not locate a session log for this session id"
	}
	return p, ""
}

func appendWorkdirSources(sources []bundleSource, skips []bundleSkip, workDir string) ([]bundleSource, []bundleSkip) {
	for _, rel := range bundleWorkdirFiles {
		full := filepath.Join(workDir, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err != nil {
			continue
		}
		sources = append(sources, bundleSource{zipPath: "workdir/" + rel, disk: full})
	}
	for _, pattern := range bundleWorkdirGlobs {
		matches, err := filepath.Glob(filepath.Join(workDir, filepath.FromSlash(pattern)))
		if err != nil {
			skips = append(skips, bundleSkip{source: pattern, reason: "glob failed: " + err.Error()})
			continue
		}
		sort.Strings(matches)
		dir := filepath.ToSlash(filepath.Dir(pattern))
		for _, m := range matches {
			sources = append(sources, bundleSource{
				zipPath: "workdir/" + dir + "/" + filepath.Base(m),
				disk:    m,
			})
		}
	}
	return sources, skips
}

func appendConfigSources(sources []bundleSource, skips []bundleSkip, configDir, provider string) ([]bundleSource, []bundleSkip) {
	if configDir == "" {
		skips = append(skips, bundleSkip{
			source: "(account config dir)",
			reason: "account could not be resolved — no account-level config collected",
		})
		return sources, skips
	}
	for _, name := range bundleConfigFiles[provider] {
		full := filepath.Join(configDir, name)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		sources = append(sources, bundleSource{zipPath: "config/" + name, disk: full})
	}
	for _, name := range bundleCredentialFiles[provider] {
		full := filepath.Join(configDir, name)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		skips = append(skips, bundleSkip{
			source: full,
			reason: "credential file, excluded by policy (no diagnostic value, and the bundle is meant to be shared)",
		})
	}
	return sources, skips
}

// writeBundle streams the archive. Files go first so the MANIFEST can report
// what actually landed — including truncations, which are only knowable after
// the copy. A single file failing to read is recorded and skipped; it must not
// abort a download the rest of which is still useful.
func writeBundle(w io.Writer, plan *bundlePlan) {
	zw := zip.NewWriter(w)
	defer func() { _ = zw.Close() }()

	entries := make([]bundleEntry, 0, len(plan.sources))
	skips := plan.skips
	for _, src := range plan.sources {
		entry, err := writeBundleFile(zw, plan.root+"/"+src.zipPath, src.disk, plan.maxFile)
		if err != nil {
			skips = append(skips, bundleSkip{source: src.disk, reason: "read failed: " + err.Error()})
			continue
		}
		entries = append(entries, entry)
	}

	if raw, err := json.MarshalIndent(plan.meta, "", "  "); err == nil {
		_ = writeBundleBlob(zw, plan.root+"/meta.json", raw)
	}
	_ = writeBundleBlob(zw, plan.root+"/MANIFEST.txt", []byte(renderManifest(plan, entries, skips)))
}

func writeBundleFile(zw *zip.Writer, zipPath, disk string, limit int64) (bundleEntry, error) {
	f, err := os.Open(disk)
	if err != nil {
		return bundleEntry{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return bundleEntry{}, err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return bundleEntry{}, err
	}
	header.Name = zipPath
	header.Method = zip.Deflate
	dst, err := zw.CreateHeader(header)
	if err != nil {
		return bundleEntry{}, err
	}
	n, err := io.Copy(dst, io.LimitReader(f, limit))
	if err != nil {
		return bundleEntry{}, err
	}
	entry := bundleEntry{zipPath: zipPath, source: disk, size: n}
	if info.Size() > n {
		entry.truncated = info.Size() - n
	}
	return entry, nil
}

func writeBundleBlob(zw *zip.Writer, zipPath string, data []byte) error {
	dst, err := zw.Create(zipPath)
	if err != nil {
		return err
	}
	_, err = dst.Write(data)
	return err
}

func renderManifest(plan *bundlePlan, entries []bundleEntry, skips []bundleSkip) string {
	var b strings.Builder
	b.WriteString("DayMug session diagnostic bundle\n")
	fmt.Fprintf(&b, "conversation: %s\n", plan.meta.Conversation.ID)
	fmt.Fprintf(&b, "provider:     %s\n", plan.meta.Conversation.Provider)
	fmt.Fprintf(&b, "session id:   %s\n", plan.meta.Conversation.SessionID)
	fmt.Fprintf(&b, "server:       %s\n", plan.meta.Server.Version)

	b.WriteString("\nincluded:\n")
	if len(entries) == 0 {
		b.WriteString("  (nothing)\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s\n      from %s (%d bytes)", e.zipPath, e.source, e.size)
		if e.truncated > 0 {
			fmt.Fprintf(&b, " truncated: %d further bytes not included", e.truncated)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nskipped:\n")
	if len(skips) == 0 {
		b.WriteString("  (nothing)\n")
	}
	for _, s := range skips {
		fmt.Fprintf(&b, "  %s\n      %s\n", s.source, s.reason)
	}
	return b.String()
}
