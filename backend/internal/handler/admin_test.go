package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func newTestConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{
		Users: config.UsersConfig{DefaultHomeRoot: root},
		Providers: []config.Provider{
			{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1},
			{Name: "alt", Type: config.CLITypeClaude, MaxConcurrent: 1},
		},
	}
}

// adminAuthCtx returns a gin handler that injects a fake authenticated admin
// (or non-admin) into the request context, so handlers can be tested without
// running the real session middleware.
func adminAuthCtx(userID string, isAdmin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("auth_user_id", userID)
		c.Set("auth_is_admin", isAdmin)
		c.Next()
	}
}

func setupAdminRouter(t *testing.T, h *AdminHandler, callerID string, isAdmin bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/admin")
	g.Use(adminAuthCtx(callerID, isAdmin))
	g.Use(middleware.RequireAdmin())
	g.GET("/users", h.ListHumans)
	g.POST("/users", h.CreateHuman)
	g.POST("/users/default-model", h.BatchSetDefaultModel)
	g.POST("/users/sandbox-mode", h.BatchSetSandboxMode)
	g.POST("/users/provider-binding", h.BatchSetProviderBinding)
	g.PUT("/users/:id", h.UpdateHuman)
	g.DELETE("/users/:id", h.Delete)
	g.POST("/users/:id/password", h.SetPassword)
	g.POST("/users/:id/disable", h.Disable)
	g.POST("/users/:id/enable", h.Enable)
	g.GET("/config/public", h.PublicConfig)
	g.GET("/db/size", h.DatabaseSize)
	g.POST("/db/optimize", h.OptimizeDatabase)
	return r
}

func doJSON(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAdminBatchSetDefaultModel(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Username: "alice", Email: "alice@example.com"},
		{ID: "u2", Username: "bob", Email: "bob@example.com"},
	}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/default-model", map[string]any{
		"user_ids": []string{"u1", "u2"},
		"model":    "claude-haiku-4-5",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, u := range ms.Users {
		if u.DefaultModel != "claude-haiku-4-5" {
			t.Errorf("user %s default_model = %q, want claude-haiku-4-5", u.ID, u.DefaultModel)
		}
	}
}

func TestAdminBatchSetDefaultModel_ClearsOnEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Email: "a@b.com", DefaultModel: "claude-haiku-4-5"}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/default-model", map[string]any{
		"user_ids": []string{"u1"},
		"model":    "",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if ms.Users[0].DefaultModel != "" {
		t.Errorf("default_model = %q, want cleared", ms.Users[0].DefaultModel)
	}
}

func TestAdminBatchSetDefaultModel_RejectsUnknownModel(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Email: "a@b.com"}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/default-model", map[string]any{
		"user_ids": []string{"u1"},
		"model":    "gpt-5.5", // codex model, but test config only configures claude
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
	if ms.Users[0].DefaultModel != "" {
		t.Errorf("default_model = %q, want unchanged on rejection", ms.Users[0].DefaultModel)
	}
}

func TestAdminBatchSetDefaultModel_RejectsUnknownUser(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Email: "a@b.com"}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/default-model", map[string]any{
		"user_ids": []string{"u1", "ghost"},
		"model":    "claude-haiku-4-5",
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body=%s)", w.Code, w.Body.String())
	}
	// Pre-validation must run before any write, so u1 stays untouched.
	if ms.Users[0].DefaultModel != "" {
		t.Errorf("default_model = %q, want unchanged when a sibling id is invalid", ms.Users[0].DefaultModel)
	}
}

func TestAdminBatchSetSandboxMode(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Username: "alice", Email: "alice@example.com", SandboxMode: store.SandboxModeJailed},
		{ID: "u2", Username: "bob", Email: "bob@example.com", SandboxMode: store.SandboxModeJailed},
	}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/sandbox-mode", map[string]any{
		"user_ids":     []string{"u1", "u2"},
		"sandbox_mode": store.SandboxModeUnrestricted,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, u := range ms.Users {
		if u.SandboxMode != store.SandboxModeUnrestricted {
			t.Errorf("user %s sandbox_mode = %q, want unrestricted", u.ID, u.SandboxMode)
		}
	}
}

func TestAdminBatchSetSandboxMode_RejectsBadMode(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Email: "a@b.com", SandboxMode: store.SandboxModeJailed}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/sandbox-mode", map[string]any{
		"user_ids":     []string{"u1"},
		"sandbox_mode": "wideopen",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
	if ms.Users[0].SandboxMode != store.SandboxModeJailed {
		t.Errorf("rejected request must not mutate the user, got %q", ms.Users[0].SandboxMode)
	}
}

func TestAdminBatchSetSandboxMode_RejectsUnknownUser(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Email: "a@b.com", SandboxMode: store.SandboxModeJailed}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/sandbox-mode", map[string]any{
		"user_ids":     []string{"u1", "ghost"},
		"sandbox_mode": store.SandboxModeUnrestricted,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", w.Code, w.Body.String())
	}
	// Reject before any write so the valid user is untouched too.
	if ms.Users[0].SandboxMode != store.SandboxModeJailed {
		t.Errorf("partial batch must not write, got %q", ms.Users[0].SandboxMode)
	}
}

func TestAdminBatchSetProviderBinding(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Username: "alice", Email: "alice@example.com"},
		{ID: "u2", Username: "bob", Email: "bob@example.com"},
	}
	cfg := newTestConfig(t)
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name: "default-codex", Type: config.CLITypeCodex, MaxConcurrent: 1,
	}, config.Provider{Name: "backup-codex", Type: config.CLITypeCodex, MaxConcurrent: 1})
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/provider-binding", map[string]any{
		"user_ids":       []string{"u1", "u2"},
		"provider_type":  "codex",
		"provider_names": []string{"backup-codex", "default-codex"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, u := range ms.Users {
		if got := u.ProviderBindings["codex"]; got != "backup-codex" {
			t.Errorf("user %s codex binding = %q, want backup-codex", u.ID, got)
		}
		if got := u.ProviderAccounts["codex"]; len(got) != 2 || got[0] != "backup-codex" || got[1] != "default-codex" {
			t.Errorf("user %s codex accounts = %v, want [backup-codex default-codex]", u.ID, got)
		}
	}
}

// A batch bind for a type the user already has an account on must replace the
// set, not accumulate — otherwise the stale account keeps showing up in the
// user's chat account switcher.
func TestAdminBatchSetProviderBinding_ReplacesExistingAccount(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{
			ID: "u1", Username: "alice", Email: "alice@example.com",
			ProviderAccounts: map[string][]string{"codex": {"old-codex"}},
			ProviderBindings: map[string]string{"codex": "old-codex"},
		},
	}
	cfg := newTestConfig(t)
	cfg.Providers = append(cfg.Providers,
		config.Provider{Name: "old-codex", Type: config.CLITypeCodex, MaxConcurrent: 1},
		config.Provider{Name: "new-codex", Type: config.CLITypeCodex, MaxConcurrent: 1},
	)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/provider-binding", map[string]any{
		"user_ids":       []string{"u1"},
		"provider_type":  "codex",
		"provider_names": []string{"new-codex"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := ms.Users[0].ProviderAccounts["codex"]; len(got) != 1 || got[0] != "new-codex" {
		t.Errorf("codex accounts = %v, want [new-codex] only (stale old-codex must be dropped)", got)
	}
	if got := ms.Users[0].ProviderBindings["codex"]; got != "new-codex" {
		t.Errorf("codex default = %q, want new-codex", got)
	}
}

func TestAdminBatchSetProviderBinding_ClearsOnEmpty(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Username: "alice", Email: "alice@example.com", ProviderBindings: map[string]string{"codex": "default-codex"}},
	}
	cfg := newTestConfig(t)
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name: "default-codex", Type: config.CLITypeCodex, MaxConcurrent: 1,
	})
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/provider-binding", map[string]any{
		"user_ids":       []string{"u1"},
		"provider_type":  "codex",
		"provider_names": []string{},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := ms.Users[0].ProviderBindings["codex"]; got != "" {
		t.Errorf("codex binding = %q, want cleared", got)
	}
}

func TestAdminBatchSetProviderBinding_RejectsUnknownProvider(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Username: "alice", Email: "alice@example.com"},
		{ID: "u2", Username: "bob", Email: "bob@example.com"},
	}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/provider-binding", map[string]any{
		"user_ids":       []string{"u1", "u2"},
		"provider_type":  "codex",
		"provider_names": []string{"missing-codex"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
	for _, u := range ms.Users {
		if len(u.ProviderBindings) != 0 {
			t.Errorf("user %s bindings = %+v, want unchanged empty", u.ID, u.ProviderBindings)
		}
	}
}

func TestAdminCreateHumanAutoWorkdir(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users", map[string]any{
		"username":          "alice",
		"email":             "alice@example.com",
		"password":          "secret123",
		"name":              "Alice",
		"provider_bindings": map[string]string{"claude": "default"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", w.Code, w.Body.String())
	}
	if len(ms.Users) != 2 {
		t.Fatalf("expected a human and default Agent, got %d rows", len(ms.Users))
	}
	created := ms.Users[0]
	if created.Username != "alice" || created.Name != "Alice" {
		t.Errorf("got username=%q name=%q", created.Username, created.Name)
	}
	if created.PasswordHash == "" {
		t.Errorf("password hash should be set")
	}
	expectedRoot := filepath.Join(cfg.Users.DefaultHomeRoot, created.Username)
	if created.WorkDir != expectedRoot {
		t.Errorf("expected workdir %q, got %q", expectedRoot, created.WorkDir)
	}
	defaultAgent := ms.Users[1]
	if defaultAgent.OwnerID != created.ID || defaultAgent.Name != created.Username || defaultAgent.WorkDir != created.WorkDir {
		t.Errorf("default Agent = %+v, want owner %q named %q", defaultAgent, created.ID, created.Username)
	}
}

func TestAdminCreateHumanRejectsDuplicateUsername(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, mustHumanUser("u1", "alice"))
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users", map[string]any{
		"username": "alice", "email": "alice@example.com", "password": "x", "name": "A",
		"provider_bindings": map[string]string{"claude": "default"},
	})
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 on duplicate username, got %d body %s", w.Code, w.Body.String())
	}
}

func TestAdminCreateHumanRejectsUnknownProviderBinding(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users", map[string]any{
		"username": "bob", "email": "bob@example.com", "password": "x",
		"provider_bindings": map[string]string{config.CLITypeClaude: "no-such"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on unknown provider binding, got %d", w.Code)
	}
}

func TestAdminListHumansFiltersAgents(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, mustHumanUser("u1", "alice"))
	_ = ms.CreateUser(ctx, mustAgent("a1", "agent-1"))
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "GET", "/api/admin/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 human (agents filtered), got %d", len(got))
	}
}

func TestAdminListHumansRedactsEnvironment(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Env: "GH_TOKEN=secret"}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "GET", "/api/admin/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var users []store.User
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(users) != 1 || users[0].Env != "" {
		t.Fatalf("admin response exposed environment: %#v", users)
	}
}

func TestAdminUpdateHumanIgnoresAndRedactsEnvironment(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", Email: "alice@example.com", Env: "TOKEN=owned"}}
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{"env": "TOKEN=admin"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if ms.Users[0].Env != "TOKEN=owned" {
		t.Fatalf("admin changed user environment to %q", ms.Users[0].Env)
	}
	var user store.User
	if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user.Env != "" {
		t.Fatalf("admin response exposed environment: %q", user.Env)
	}
}

func TestAdminUpdateHumanWorkDir(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	u := mustHumanUser("u1", "alice")
	_ = ms.CreateUser(ctx, u)
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	newWD := filepath.Join(cfg.Users.DefaultHomeRoot, "custom-alice")
	w := doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"work_dir": newWD,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	got, _ := ms.GetUser(ctx, "u1")
	if got.WorkDir != newWD {
		t.Errorf("expected work_dir %q, got %q", newWD, got.WorkDir)
	}
}

func TestAdminUpdateHumanRejectsRelativeWorkDir(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, mustHumanUser("u1", "alice"))
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"work_dir": "relative/path",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on relative workdir, got %d body %s", w.Code, w.Body.String())
	}
}

func TestAdminCannotDemoteSelf(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	admin := mustHumanUser("admin-1", "boss")
	admin.IsAdmin = true
	_ = ms.CreateUser(ctx, admin)
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	f := false
	w := doJSON(r, "PUT", "/api/admin/users/admin-1", map[string]any{
		"is_admin": &f,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on self-demote, got %d", w.Code)
	}
}

func TestAdminCannotDisableSelf(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, mustHumanUser("admin-1", "boss"))
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/admin-1/disable", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on self-disable, got %d", w.Code)
	}
}

func TestAdminSetPassword(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, mustHumanUser("u1", "alice"))
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/u1/password", map[string]any{"password": "newpw"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("set password: %d %s", w.Code, w.Body.String())
	}
	got, _ := ms.GetUser(ctx, "u1")
	if got.PasswordHash == "" {
		t.Errorf("password hash should have been written")
	}
}

func TestAdminPublicConfig(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "GET", "/api/admin/config/public", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("config: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"default_home_root", "providers", "default_cli_type", "default"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in body: %s", want, body)
		}
	}
	if strings.Contains(body, "claude_accounts") {
		t.Errorf("legacy claude_accounts key is still served: %s", body)
	}
	if strings.Contains(body, "runner_mode") {
		t.Errorf("removed runner mode leaked into public config: %s", body)
	}
}

// PublicConfig lists every provider with its type so the admin UI can render
// per-CLI-type pickers.
func TestAdminPublicConfigListsTypedProviders(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name: "default-codex", Type: config.CLITypeCodex, MaxConcurrent: 1, ConfigDir: "/tmp/codex",
	})
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "GET", "/api/admin/config/public", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("config: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Providers []struct {
			Name, Type string
		} `json:"providers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// providers carries every entry with the type tag exposed.
	hasCodex := false
	for _, p := range resp.Providers {
		if p.Name == "default-codex" && p.Type == "codex" {
			hasCodex = true
		}
	}
	if !hasCodex {
		t.Errorf("codex entry missing from typed providers list: %+v", resp.Providers)
	}
}

// Admin update patches several provider bindings at once.
func TestAdminUpdateHumanProviderBindings(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	u := mustHumanUser("u1", "alice")
	_ = ms.CreateUser(ctx, u)

	cfg := newTestConfig(t)
	cfg.Providers = append(cfg.Providers, config.Provider{
		Name: "default-codex", Type: config.CLITypeCodex, MaxConcurrent: 1, ConfigDir: "/tmp/codex",
	})
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"provider_bindings": map[string]string{
			"claude": "default",
			"codex":  "default-codex",
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	got, _ := ms.GetUserProviderBindings(ctx, "u1")
	if got["claude"] != "default" {
		t.Errorf("claude binding = %q, want %q", got["claude"], "default")
	}
	if got["codex"] != "default-codex" {
		t.Errorf("codex binding = %q, want %q", got["codex"], "default-codex")
	}

	// Unknown (name, type) pair must fail validation cleanly. A codex
	// account name pointed at type=claude is the canonical "stale
	// binding" mistake.
	w = doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"provider_bindings": map[string]string{
			"claude": "default-codex", // wrong type
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for cross-type binding, got %d %s", w.Code, w.Body.String())
	}
}

func TestAdminEndpointsDenyNonAdmin(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "user-1", false)

	w := doJSON(r, "GET", "/api/admin/users", nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", w.Code)
	}
}

func TestAdminTargetingAgentRejected(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, mustAgent("a1", "agent-name"))
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users/a1/disable", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 (target is agent, not human), got %d", w.Code)
	}
}

func TestAdminPublicConfigIncludesBackend(t *testing.T) {
	ms := storetest.New()
	ms.BackendName = "sqlite"
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "GET", "/api/admin/config/public", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("config: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["backend"] != "sqlite" {
		t.Errorf("expected backend=sqlite, got %v", got["backend"])
	}
}

func TestAdminDatabaseSize(t *testing.T) {
	ms := storetest.New()
	ms.DBSize = 12345
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "GET", "/api/admin/db/size", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("size: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["bytes"].(float64) != 12345 {
		t.Errorf("expected bytes=12345, got %v", got["bytes"])
	}
}

func TestAdminOptimizeDatabase_RejectsNonSQLite(t *testing.T) {
	ms := storetest.New()
	ms.BackendName = "postgres" // pretend the operator swapped engines
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/db/optimize", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 on non-sqlite backend, got %d body %s", w.Code, w.Body.String())
	}
}

func TestAdminOptimizeDatabase_Success(t *testing.T) {
	ms := storetest.New()
	ms.BackendName = "sqlite"
	ms.OptimizeFn = func() (store.DBOptimizeResult, error) {
		return store.DBOptimizeResult{
			BeforeBytes:            10_000,
			AfterBytes:             4_000,
			ExpiredSessionsDeleted: 2,
			PurgedConversations:    3,
			PurgedUsers:            1,
			PurgedAgents:           2,
		}, nil
	}
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/db/optimize", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("optimize: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["before_bytes"].(float64) != 10_000 {
		t.Errorf("before_bytes: got %v", got["before_bytes"])
	}
	if got["after_bytes"].(float64) != 4_000 {
		t.Errorf("after_bytes: got %v", got["after_bytes"])
	}
	if got["bytes_reclaimed"].(float64) != 6_000 {
		t.Errorf("bytes_reclaimed: got %v", got["bytes_reclaimed"])
	}
	if got["expired_sessions_deleted"].(float64) != 2 {
		t.Errorf("expired_sessions_deleted: got %v", got["expired_sessions_deleted"])
	}
	if got["purged_conversations"].(float64) != 3 {
		t.Errorf("purged_conversations: got %v", got["purged_conversations"])
	}
	if got["purged_users"].(float64) != 1 {
		t.Errorf("purged_users: got %v", got["purged_users"])
	}
	if got["purged_agents"].(float64) != 2 {
		t.Errorf("purged_agents: got %v", got["purged_agents"])
	}
}

// TestAdminOptimizeDatabase_ClampsNegativeReclaim covers the corner case
// where VACUUM allocates a fresh WAL/SHM and the post-size briefly exceeds
// the pre-size. The handler must clamp bytes_reclaimed to 0 so the UI
// doesn't claim the optimizer made the database larger.
func TestAdminOptimizeDatabase_ClampsNegativeReclaim(t *testing.T) {
	ms := storetest.New()
	ms.BackendName = "sqlite"
	ms.OptimizeFn = func() (store.DBOptimizeResult, error) {
		return store.DBOptimizeResult{BeforeBytes: 1000, AfterBytes: 1500}, nil
	}
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/db/optimize", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("optimize: %d %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["bytes_reclaimed"].(float64) != 0 {
		t.Errorf("expected clamped bytes_reclaimed=0, got %v", got["bytes_reclaimed"])
	}
}

// TestAdminCreateHumanRequiresEmail enforces the upload-feature invariant
// at the API surface: a human row without an email cannot resolve agent
// ownership later, so we reject it at create time rather than letting
// the insert succeed and break GetOwner downstream.
func TestAdminCreateHumanRequiresEmail(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users", map[string]any{
		"username": "alice", "password": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when email missing, got %d body %s", w.Code, w.Body.String())
	}
}

// TestAdminCreateHumanRejectsUsernameEmailMismatch — username MUST equal
// the email's local-part. Suffixed disambiguation isn't allowed even for
// well-meaning admins; the partial unique index on email + username being
// the same key is what makes Store.GetOwner deterministic.
func TestAdminCreateHumanRejectsUsernameEmailMismatch(t *testing.T) {
	ms := storetest.New()
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users", map[string]any{
		"username": "bob", "email": "alice@example.com", "password": "x",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when username != email-prefix, got %d body %s", w.Code, w.Body.String())
	}
}

// TestAdminCreateHumanRejectsDuplicateEmail — second human with same email
// must 409, otherwise GetOwner would have to choose arbitrarily between
// two humans for an agent inheriting that email.
func TestAdminCreateHumanRejectsDuplicateEmail(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	existing := mustHumanUser("u1", "alice")
	existing.Email = "alice@example.com"
	_ = ms.CreateUser(ctx, existing)
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "POST", "/api/admin/users", map[string]any{
		"username": "alice", "email": "alice@example.com", "password": "x",
		"provider_bindings": map[string]string{"claude": "default"},
	})
	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 on duplicate email, got %d body %s", w.Code, w.Body.String())
	}
}

// TestAdminUpdateHumanEmailMustMatchUsername — updating email is allowed
// only when the new local-part still equals the existing username, since
// we don't currently support renaming the username column.
func TestAdminUpdateHumanEmailMustMatchUsername(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	u := mustHumanUser("u1", "alice")
	u.Email = "alice@example.com"
	_ = ms.CreateUser(ctx, u)
	cfg := newTestConfig(t)
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	// Local-part 'alice' matches existing username — should succeed.
	w := doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"email": "alice@other.com",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("matching local-part should be accepted, got %d body %s", w.Code, w.Body.String())
	}

	// Local-part 'bob' does NOT match — should be rejected.
	w = doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"email": "bob@example.com",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("mismatched local-part should be rejected, got %d body %s", w.Code, w.Body.String())
	}
}

// TestAdminUpdateHumanDoesNotMigrateOnAccountSwitch pins the deliberate
// behaviour change: switching a human's claude account flips the binding
// but never migrates on-disk session logs and never emits a migration
// summary. Each conversation stays pinned to the account it first ran on,
// so its history is left in place rather than dragged to the new account.
func TestAdminUpdateHumanDoesNotMigrateOnAccountSwitch(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	u := mustHumanUser("u1", "alice")
	u.WorkDir = "/home/alice/proj"
	u.ProviderBindings = map[string]string{config.CLITypeClaude: "default"}
	_ = ms.CreateUser(ctx, u)

	cfg := newTestConfig(t)
	cfg.Providers = []config.Provider{
		{Name: "default", Type: config.CLITypeClaude, ConfigDir: "/old/cfg"},
		{Name: "alt", Type: config.CLITypeClaude, ConfigDir: "/new/cfg"},
	}
	h := NewAdminHandler(ms, cfg)
	r := setupAdminRouter(t, h, "admin-1", true)

	w := doJSON(r, "PUT", "/api/admin/users/u1", map[string]any{
		"provider_bindings": map[string]string{config.CLITypeClaude: "alt"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	row, _ := ms.GetUser(ctx, "u1")
	if got := row.ProviderBindings[config.CLITypeClaude]; got != "alt" {
		t.Errorf("DB account should be flipped to alt, got %q", got)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["migration"]; ok {
		t.Errorf("account switch must not emit a migration summary, body=%s", w.Body.String())
	}
}

// --- helpers ---

func mustHumanUser(id, username string) store.User {
	return store.User{ID: id, Username: username, Name: username}
}

func mustAgent(id, name string) store.User {
	return store.User{ID: id, Name: name}
}
