package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupPricingRouter(t *testing.T, ms *storetest.Fake, isAdmin bool) *gin.Engine {
	return setupPricingRouterWithConfig(t, ms, isAdmin, nil)
}

func setupPricingRouterWithConfig(t *testing.T, ms *storetest.Fake, isAdmin bool, cfg *config.Config) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(adminAuthCtx("admin-1", isAdmin))
	h := NewAdminPricingHandler(ms, cfg)
	// Mount without RequireAdmin here — the per-handler tests focus on
	// the handler's own logic; admin gating is covered by the routes.go
	// integration in upgrade/help tests.
	r.GET("/api/admin/pricing/:provider", h.Get)
	r.PUT("/api/admin/pricing/:provider", h.Put)
	return r
}

// Cross-test mutation guard: any test that calls pricing.Save modifies a
// process-global table. Reset to defaults between cases so order doesn't
// matter.
func resetPricing(t *testing.T) {
	t.Helper()
	pricing.ResetForTest()
}

func TestAdminPricingGetReturnsDefaults(t *testing.T) {
	resetPricing(t)
	ms := storetest.New()
	r := setupPricingRouter(t, ms, true)

	w := doJSON(r, "GET", "/api/admin/pricing/codex", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Provider string                        `json:"provider"`
		Models   []string                      `json:"models"`
		Rates    map[string]pricing.ModelPrice `json:"rates"`
		Defaults map[string]pricing.ModelPrice `json:"defaults"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Provider != "codex" {
		t.Errorf("provider = %q, want codex", body.Provider)
	}
	wantModels := []string{"gpt-5.6", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra"}
	if !slices.Equal(body.Models, wantModels) {
		t.Errorf("models = %v, want %v", body.Models, wantModels)
	}
	if got := body.Rates["gpt-6-astra"]; got != (pricing.ModelPrice{Input: 10, CachedInput: 1, Output: 50}) {
		t.Errorf("gpt-6-astra rate = %+v", got)
	}
	for _, retired := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini"} {
		if _, ok := body.Rates[retired]; ok {
			t.Errorf("retired model %q remains in pricing rates", retired)
		}
	}
}

// A compatible provider ships no default rates, so the only way its editor
// can offer a row to fill in is by reading the configured model registry.
// Without that the admin UI renders an empty table and the endpoint can never
// be priced — which silently means every one of its turns bills at zero.
func TestAdminPricingGetCompatibleListsConfiguredModels(t *testing.T) {
	resetPricing(t)
	ms := storetest.New()
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "qwen", Type: config.CLITypeOpenAICompatible, ConfigDir: "/srv/qwen"},
	}}
	prev := service.ModelOverrides()
	service.SetModelOverrides(map[string]service.AccountModels{
		"qwen": {Models: []string{"qwen3-max", "qwen3-coder"}},
	})
	t.Cleanup(func() { service.SetModelOverrides(prev) })

	r := setupPricingRouterWithConfig(t, ms, true, cfg)
	w := doJSON(r, "GET", "/api/admin/pricing/openai-compatible", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Provider string                        `json:"provider"`
		Models   []string                      `json:"models"`
		Rates    map[string]pricing.ModelPrice `json:"rates"`
		Defaults map[string]pricing.ModelPrice `json:"defaults"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Provider != "openai-compatible" {
		t.Errorf("provider = %q, want openai-compatible", body.Provider)
	}
	if !slices.Equal(body.Models, []string{"qwen3-coder", "qwen3-max"}) {
		t.Errorf("models = %v, want the configured registry sorted", body.Models)
	}
	if len(body.Defaults) != 0 {
		t.Errorf("compatible providers ship no default rates; got %v", body.Defaults)
	}
	// The first-party catalog must not bleed in — those ids belong to a
	// different endpoint with different rates.
	if _, ok := body.Rates["gpt-6-astra"]; ok {
		t.Error("codex models leaked into the openai-compatible table")
	}
}

func TestAdminPricingRejectsUnknownProvider(t *testing.T) {
	resetPricing(t)
	ms := storetest.New()
	r := setupPricingRouter(t, ms, true)
	w := doJSON(r, "GET", "/api/admin/pricing/bogus", nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown provider, got %d", w.Code)
	}
}

func TestAdminPricingPutPersistsAndHotSwaps(t *testing.T) {
	resetPricing(t)
	t.Cleanup(func() { resetPricing(t) })

	ms := storetest.New()
	r := setupPricingRouter(t, ms, true)

	w := doJSON(r, "PUT", "/api/admin/pricing/codex", map[string]any{
		"rates": map[string]any{
			"gpt-6-astra": map[string]float64{"input": 12.5, "output": 37.5},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", w.Code, w.Body.String())
	}

	stored, _ := ms.GetAppSetting(context.Background(), pricing.SettingKey("codex"))
	if stored == "" {
		t.Errorf("expected app_settings row to be written")
	}

	got := pricing.Get("codex")["gpt-6-astra"]
	if got.Input != 12.5 || got.Output != 37.5 {
		t.Errorf("active rate = %+v, want {12.5, 37.5}", got)
	}

	defaults := pricing.Defaults("codex")
	if pricing.Get("codex")["gpt-5.6"] != defaults["gpt-5.6"] {
		t.Errorf("non-overridden model should retain default rate")
	}
}

func TestAdminPricingPutPersistsClaudeCompatible(t *testing.T) {
	resetPricing(t)
	t.Cleanup(func() { resetPricing(t) })

	ms := storetest.New()
	r := setupPricingRouter(t, ms, true)

	w := doJSON(r, "PUT", "/api/admin/pricing/claude-compatible", map[string]any{
		"rates": map[string]any{
			"glm-5": map[string]float64{
				"input":             5.5,
				"cached_input":      0.55,
				"output":            22.0,
				"cache_creation_5m": 6.875,
				"cache_creation_1h": 11.0,
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", w.Code, w.Body.String())
	}

	stored, _ := ms.GetAppSetting(context.Background(), pricing.SettingKey("claude-compatible"))
	if stored == "" {
		t.Errorf("expected claude-compatible app_settings row to be written")
	}

	got := pricing.Get("claude-compatible")["glm-5"]
	if got.Input != 5.5 || got.CachedInput != 0.55 || got.CacheCreation5m != 6.875 || got.CacheCreation1h != 11.0 {
		t.Errorf("active rate = %+v", got)
	}
}

func TestAdminPricingPutRejectsNegative(t *testing.T) {
	resetPricing(t)
	ms := storetest.New()
	r := setupPricingRouter(t, ms, true)

	for _, field := range []string{"input", "output", "cached_input", "cache_creation_5m", "cache_creation_1h"} {
		t.Run(field, func(t *testing.T) {
			w := doJSON(r, "PUT", "/api/admin/pricing/claude-compatible", map[string]any{
				"rates": map[string]any{
					"glm-5": map[string]float64{field: -1},
				},
			})
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for negative %s, got %d", field, w.Code)
			}
		})
	}
}

func TestAdminPricingPutEmptyClearsOverride(t *testing.T) {
	resetPricing(t)
	t.Cleanup(func() { resetPricing(t) })

	ms := storetest.New()
	_ = ms.SetAppSetting(context.Background(), pricing.SettingKey("codex"), `{"gpt-6-astra":{"input":99,"output":99}}`)

	r := setupPricingRouter(t, ms, true)
	w := doJSON(r, "PUT", "/api/admin/pricing/codex", map[string]any{"rates": map[string]any{}})
	if w.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", w.Code, w.Body.String())
	}

	stored, _ := ms.GetAppSetting(context.Background(), pricing.SettingKey("codex"))
	if stored != "" {
		t.Errorf("expected override row to be cleared, got %q", stored)
	}
	if pricing.Get("codex")["gpt-6-astra"] != pricing.Defaults("codex")["gpt-6-astra"] {
		t.Errorf("active rate should revert to defaults")
	}
}
