package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcli"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

func TestModelsHandler_List(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewModelsHandler(&config.Config{Providers: []config.Provider{
		{Name: "default-codex", Type: config.CLITypeCodex},
		{Name: "default", Type: config.CLITypeClaude},
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/models", http.NoBody)

	h.List(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	var resp modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.DefaultProvider != config.CLITypeCodex {
		t.Errorf("default_provider = %q, want %q", resp.DefaultProvider, config.CLITypeCodex)
	}
	if len(resp.Providers) != 2 {
		t.Fatalf("want 2 providers, got %d", len(resp.Providers))
	}
	wantClaudeLatest := "claude-opus-5-5[1m]"
	for _, p := range resp.Providers {
		if p.Name == config.CLITypeClaude && p.Latest != wantClaudeLatest {
			t.Errorf("claude latest = %q, want %q", p.Latest, wantClaudeLatest)
		}
		if len(p.Models) == 0 {
			t.Errorf("provider %q has no models", p.Name)
		}
	}
}

// When no providers are configured the response advertises an empty
// default_provider so the UI surfaces "no backend available" instead of
// silently picking claude on a deployment that doesn't have any claude
// credentials wired up.
func TestModelsHandler_EmptyDefaultWhenNoProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewModelsHandler(nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/models", http.NoBody)

	h.List(c)

	var resp modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.DefaultProvider != "" {
		t.Errorf("default_provider = %q, want empty", resp.DefaultProvider)
	}
}

// TestModelsHandler_ShipsCapabilities verifies the per-provider
// Capabilities() values are forwarded to the UI. Without this the
// frontend's slash-command autocomplete can't tell which providers
// support /compact and falls back to either showing the command for
// everyone or hiding it everywhere.
func TestModelsHandler_ShipsCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewModelsHandler(&config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "default-codex", Type: config.CLITypeCodex},
	}})
	h.Backends = service.NewBackendRegistry(map[string]agent.Backend{
		config.CLITypeClaude: claudecli.NewBackend(),
		config.CLITypeCodex:  codexcli.NewBackend(),
	}, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/models", http.NoBody)
	h.List(c)

	var resp modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	caps := map[string]providerCapabilities{}
	for _, p := range resp.Providers {
		caps[p.Name] = p.Capabilities
	}
	if !caps[config.CLITypeClaude].SupportsCompaction {
		t.Errorf("claude should report SupportsCompaction=true, got %+v", caps[config.CLITypeClaude])
	}
	if !caps[config.CLITypeCodex].SupportsCompaction {
		t.Errorf("codex should report SupportsCompaction=true, got %+v", caps[config.CLITypeCodex])
	}
}

// useAccountModels registers what each account serves for one test. Model
// lists used to ride on config.Provider; they now live in the process-global,
// database-backed registry, so each test installs its own and the previous one
// is restored afterwards to keep cases independent.
func useAccountModels(t *testing.T, models map[string]service.AccountModels) {
	t.Helper()
	prev := service.ModelOverrides()
	service.SetModelOverrides(models)
	t.Cleanup(func() { service.SetModelOverrides(prev) })
}

func TestModelsHandler_UsesConfiguredProviderModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useAccountModels(t, map[string]service.AccountModels{
		"default": {Models: []string{"glm-5", "glm-5-air"}, SummaryModel: "glm-5-air"},
	})
	h := NewModelsHandler(&config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaudeCompatible},
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/models", http.NoBody)
	h.List(c)

	var resp modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Providers) != 1 {
		t.Fatalf("providers len = %d, want 1", len(resp.Providers))
	}
	p := resp.Providers[0]
	if p.Name != config.CLITypeClaudeCompatible {
		t.Fatalf("provider name = %q, want %q", p.Name, config.CLITypeClaudeCompatible)
	}
	if p.Latest != "glm-5" {
		t.Errorf("latest = %q, want glm-5", p.Latest)
	}
	if len(p.Models) != 2 || p.Models[1] != "glm-5-air" {
		t.Errorf("models = %#v", p.Models)
	}
}

func TestModelsHandler_PerAccountModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Two codex accounts with disjoint models — the per-account picker depends
	// on /api/models exposing each account's own list.
	useAccountModels(t, map[string]service.AccountModels{
		"codex-qwen": {Models: []string{"Qwen/Qwen3"}},
	})
	h := NewModelsHandler(&config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "codex", Type: config.CLITypeCodex},
		{Name: "codex-qwen", Type: config.CLITypeCodex},
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/models", http.NoBody)
	h.List(c)

	var resp modelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byAccount := map[string]accountEntry{}
	for _, a := range resp.Accounts {
		byAccount[a.Account] = a
	}
	if len(resp.Accounts) != 3 {
		t.Fatalf("accounts len = %d, want 3 (%#v)", len(resp.Accounts), resp.Accounts)
	}
	qwen, ok := byAccount["codex-qwen"]
	if !ok {
		t.Fatalf("missing codex-qwen account entry")
	}
	if qwen.Provider != config.CLITypeCodex {
		t.Errorf("codex-qwen provider = %q, want codex", qwen.Provider)
	}
	if len(qwen.Models) != 1 || qwen.Models[0] != "Qwen/Qwen3" || qwen.Latest != "Qwen/Qwen3" {
		t.Errorf("codex-qwen models = %#v latest=%q", qwen.Models, qwen.Latest)
	}
	stock := byAccount["codex"]
	wantStock := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra"}
	if !slices.Equal(stock.Models, wantStock) || stock.Latest != "gpt-5.6-sol" {
		t.Errorf("stock codex models = %#v latest=%q, want %#v latest=gpt-5.6-sol", stock.Models, stock.Latest, wantStock)
	}
}
