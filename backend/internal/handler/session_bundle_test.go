package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// stubLogBackend lets a test point SessionLogPath at a file it just wrote,
// standing in for the real per-CLI on-disk layout. Overriding only that one
// method keeps the rest of the agent.Backend surface on the shared fake.
type stubLogBackend struct {
	agenttest.ScriptedBackend
	path string
}

func (b stubLogBackend) SessionLogPath(string, string, string) string { return b.path }

// bundleFixture is one fully-wired export scenario: a conversation owned by a
// user bound to an account whose config dir and work dir both exist on disk.
type bundleFixture struct {
	handler  *SessionBundleHandler
	router   *gin.Engine
	store    *storetest.Fake
	workDir  string
	confDir  string
	logPath  string
	provider string
}

func newBundleFixture(t *testing.T, provider string) *bundleFixture {
	t.Helper()

	workDir := t.TempDir()
	confDir := t.TempDir()

	// The session log lives under the account's config dir, exactly like the
	// real thing — the point of the export is that this file is unreachable
	// through the work_dir-scoped file API.
	logPath := filepath.Join(confDir, "session.jsonl")
	writeFixtureFile(t, logPath, `{"type":"assistant"}`+"\n")

	cfg := &config.Config{Providers: []config.Provider{
		{Name: "acc1", Type: provider, ConfigDir: confDir},
	}}

	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{
		ID:               "u1",
		Username:         "alice",
		WorkDir:          workDir,
		ProviderBindings: map[string]string{provider: "acc1"},
		ProviderAccounts: map[string][]string{provider: {"acc1"}},
	})
	ms.Conversations = append(ms.Conversations, store.Conversation{
		ID:        "c1",
		UserID:    "u1",
		Title:     "broken bot",
		Provider:  provider,
		Model:     "some-model",
		SessionID: "sid-123",
		WorkDir:   workDir,
	})

	h := NewSessionBundleHandler(ms, cfg, service.NewPool(cfg), service.NewBackendRegistry(map[string]agent.Backend{
		provider: stubLogBackend{path: logPath},
	}, nil), "v1.2.3")

	r := gin.New()
	r.GET("/api/admin/conversations/:id/session-bundle", h.Download)

	return &bundleFixture{
		handler: h, router: r, store: ms,
		workDir: workDir, confDir: confDir, logPath: logPath, provider: provider,
	}
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func (f *bundleFixture) get(t *testing.T, convID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/conversations/"+convID+"/session-bundle", nil))
	return rec
}

// zipEntries decodes the response body as a zip and returns entry name →
// content, with the "daymug-session-<id>/" prefix stripped so assertions read
// against the layout the design doc describes.
func zipEntries(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		name := f.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		out[name] = string(data)
	}
	return out
}

func decodeBundleMeta(t *testing.T, entries map[string]string) sessionBundleMeta {
	t.Helper()
	raw, ok := entries["meta.json"]
	if !ok {
		t.Fatal("meta.json missing from bundle")
	}
	var meta sessionBundleMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("decode meta.json: %v", err)
	}
	return meta
}

func TestSessionBundleConversationNotFound(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	if rec := f.get(t, "nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A conversation that never sent a message has nothing on disk at all. 400
// beats an empty zip, which reads as "the log went missing".
func TestSessionBundleRefusesConversationWithoutSession(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	f.store.Conversations[0].SessionID = ""

	rec := f.get(t, "c1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestSessionBundleIncludesLogMetaAndWorkdirConfig(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	writeFixtureFile(t, filepath.Join(f.workDir, "CLAUDE.md"), "# project memory")
	writeFixtureFile(t, filepath.Join(f.workDir, ".claude", "settings.json"), `{"model":"x"}`)
	writeFixtureFile(t, filepath.Join(f.workDir, ".claude", "agents", "reviewer.md"), "reviewer agent")
	writeFixtureFile(t, filepath.Join(f.confDir, "settings.json"), `{"env":{}}`)

	entries := zipEntries(t, f.get(t, "c1"))

	for _, want := range []string{
		"session/session.jsonl",
		"meta.json",
		"MANIFEST.txt",
		"workdir/CLAUDE.md",
		"workdir/.claude/settings.json",
		"workdir/.claude/agents/reviewer.md",
		"config/settings.json",
	} {
		if _, ok := entries[want]; !ok {
			t.Errorf("bundle missing %q (have %v)", want, keysOf(entries))
		}
	}
	if got := entries["session/session.jsonl"]; !strings.Contains(got, `"type":"assistant"`) {
		t.Errorf("session log content = %q, want the raw jsonl", got)
	}

	meta := decodeBundleMeta(t, entries)
	if meta.Conversation.ID != "c1" || meta.Conversation.Provider != config.CLITypeClaude {
		t.Errorf("meta.conversation = %+v, want c1/claude", meta.Conversation)
	}
	if meta.Account.Name != "acc1" || meta.Account.ConfigDir != f.confDir {
		t.Errorf("meta.account = %+v, want acc1 at %s", meta.Account, f.confDir)
	}
	if meta.Server.Version != "v1.2.3" {
		t.Errorf("meta.server.version = %q, want v1.2.3", meta.Server.Version)
	}
	if !meta.Resolved.Exists || meta.Resolved.SessionLogPath != f.logPath {
		t.Errorf("meta.resolved = %+v, want exists at %s", meta.Resolved, f.logPath)
	}
}

// Credentials are excluded on purpose, but silence would read as "this deploy
// has no credential file here" — the MANIFEST has to say it was skipped.
func TestSessionBundleExcludesCredentialsButRecordsTheSkip(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	writeFixtureFile(t, filepath.Join(f.confDir, ".credentials.json"), `{"refresh_token":"SECRET"}`)

	entries := zipEntries(t, f.get(t, "c1"))

	for name, content := range entries {
		if strings.Contains(content, "SECRET") {
			t.Fatalf("credential content leaked into %s", name)
		}
	}
	if _, ok := entries["config/.credentials.json"]; ok {
		t.Fatal("bundle contains config/.credentials.json")
	}
	manifest := entries["MANIFEST.txt"]
	if !strings.Contains(manifest, ".credentials.json") || !strings.Contains(manifest, "credential") {
		t.Errorf("MANIFEST does not record the credential skip:\n%s", manifest)
	}
}

// The log path resolving to a file that isn't there is the cwd-drift failure
// (docs/architecture/working-directory.md) — the single case an admin most needs to export.
func TestSessionBundleExportsWhenLogFileMissing(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	if err := os.Remove(f.logPath); err != nil {
		t.Fatalf("remove log: %v", err)
	}

	entries := zipEntries(t, f.get(t, "c1"))

	if _, ok := entries["session/session.jsonl"]; ok {
		t.Error("bundle contains a session entry for a file that does not exist")
	}
	meta := decodeBundleMeta(t, entries)
	if meta.Resolved.Exists {
		t.Error("meta.resolved.exists = true, want false")
	}
	if meta.Resolved.SessionLogPath != f.logPath {
		t.Errorf("meta.resolved.session_log_path = %q, want %q", meta.Resolved.SessionLogPath, f.logPath)
	}
	if !strings.Contains(entries["MANIFEST.txt"], "session.jsonl") {
		t.Errorf("MANIFEST does not mention the missing log:\n%s", entries["MANIFEST.txt"])
	}
}

func TestSessionBundleTruncatesOversizeFile(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	writeFixtureFile(t, f.logPath, strings.Repeat("a", 100))
	f.handler.MaxFileBytes = 10

	entries := zipEntries(t, f.get(t, "c1"))

	if got := entries["session/session.jsonl"]; len(got) != 10 {
		t.Errorf("session log length = %d, want 10", len(got))
	}
	if !strings.Contains(entries["MANIFEST.txt"], "truncated") {
		t.Errorf("MANIFEST does not record the truncation:\n%s", entries["MANIFEST.txt"])
	}
}

func TestSessionBundleCodexCollectsItsOwnConfig(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeCodex)
	writeFixtureFile(t, filepath.Join(f.confDir, "config.toml"), "[mcp_servers.foo]\n")
	writeFixtureFile(t, filepath.Join(f.confDir, "auth.json"), `{"token":"SECRET"}`)
	writeFixtureFile(t, filepath.Join(f.workDir, "AGENTS.md"), "# codex memory")

	entries := zipEntries(t, f.get(t, "c1"))

	if _, ok := entries["config/config.toml"]; !ok {
		t.Errorf("bundle missing config/config.toml (have %v)", keysOf(entries))
	}
	if _, ok := entries["workdir/AGENTS.md"]; !ok {
		t.Errorf("bundle missing workdir/AGENTS.md (have %v)", keysOf(entries))
	}
	if _, ok := entries["config/auth.json"]; ok {
		t.Error("bundle contains codex auth.json")
	}
	if !strings.Contains(entries["MANIFEST.txt"], "auth.json") {
		t.Errorf("MANIFEST does not record the auth.json skip:\n%s", entries["MANIFEST.txt"])
	}
}

// A revoked binding is itself a bot failure; the export has to survive it and
// report the resolution error rather than refuse.
func TestSessionBundleSurvivesUnresolvableAccount(t *testing.T) {
	f := newBundleFixture(t, config.CLITypeClaude)
	f.store.Users[0].ProviderBindings = map[string]string{}

	entries := zipEntries(t, f.get(t, "c1"))

	meta := decodeBundleMeta(t, entries)
	if meta.Account.Error == "" {
		t.Error("meta.account.error is empty, want the resolution failure")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
