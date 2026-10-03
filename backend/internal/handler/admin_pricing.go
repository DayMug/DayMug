package handler

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AdminPricingHandler exposes the admin-editable token price table for
// every provider whose cost we compute locally: codex, plus the two
// compatible types. The active rates feed ComputeXxxCost on every matching
// usage event, so a save here changes what subsequent turns get billed —
// historical rows are NOT retroactively reweighted.
type AdminPricingHandler struct {
	Store store.Store
	// Cfg supplies the configured model registry. The compatible provider
	// types ship no built-in price rows, so without it their editor would
	// render an empty table and an operator could never price the endpoint
	// they just configured.
	Cfg *config.Config
}

func NewAdminPricingHandler(s store.Store, cfg *config.Config) *AdminPricingHandler {
	return &AdminPricingHandler{Store: s, Cfg: cfg}
}

type adminPricingResponse struct {
	Provider string                        `json:"provider"`
	Models   []string                      `json:"models"`
	Rates    map[string]pricing.ModelPrice `json:"rates"`
	Defaults map[string]pricing.ModelPrice `json:"defaults"`
}

// resolveProvider validates the `:provider` path parameter against the
// set of providers with a built-in price table. Returns the provider id
// and true on success; writes a 400 + false otherwise so the handler
// body can early-return.
func resolveProvider(c *gin.Context) (string, bool) {
	provider := strings.TrimSpace(c.Param("provider"))
	for _, p := range pricing.ProvidersWithDefaults() {
		if p == provider {
			return provider, true
		}
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"error":     "unsupported pricing provider",
		"provider":  provider,
		"supported": pricing.ProvidersWithDefaults(),
	})
	return "", false
}

// modelsForProvider returns the sorted pricing catalog for a provider: the
// union of the built-in defaults, any model an operator already saved a rate
// for, and the models currently selectable for that provider type.
//
// The first two are deliberately kept even when the selectable registry drops
// a model — retiring a model from the picker must not silently erase the rate
// that existing conversations are still billed at. The third is what makes a
// freshly configured compatible endpoint priceable at all: it has no defaults
// and no saved rows, so the first two sources are both empty.
func (h *AdminPricingHandler) modelsForProvider(provider string) []string {
	seen := map[string]struct{}{}
	for m := range pricing.Defaults(provider) {
		seen[m] = struct{}{}
	}
	for m := range pricing.Get(provider) {
		seen[m] = struct{}{}
	}
	for _, m := range service.ModelsForConfig(h.Cfg, provider) {
		seen[m] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Get returns the active rates merged from defaults + the app_settings
// override, alongside the model list and the unmodified defaults for UI
// "reset to default" hinting.
func (h *AdminPricingHandler) Get(c *gin.Context) {
	provider, ok := resolveProvider(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, adminPricingResponse{
		Provider: provider,
		Models:   h.modelsForProvider(provider),
		Rates:    pricing.Get(provider),
		Defaults: pricing.Defaults(provider),
	})
}

type adminPricingRequest struct {
	Rates map[string]pricing.ModelPrice `json:"rates"`
}

// Put replaces the override row in app_settings for the given provider.
// Each rate must be non-negative — the UI surfaces negative numbers but
// we refuse them at the boundary to keep ComputeXxxCost's contract
// ("returns a non-negative dollar figure") intact. An empty body clears
// the override, reverting to compile-time defaults.
func (h *AdminPricingHandler) Put(c *gin.Context) {
	provider, ok := resolveProvider(c)
	if !ok {
		return
	}
	var req adminPricingRequest
	if !bindJSON(c, &req) {
		return
	}
	for model, p := range req.Rates {
		if p.Input < 0 || p.Output < 0 || p.CachedInput < 0 ||
			p.CacheCreation5m < 0 || p.CacheCreation1h < 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "negative rate for model " + model,
			})
			return
		}
	}
	if err := pricing.Save(c.Request.Context(), h.Store, provider, req.Rates); err != nil {
		respondInternalError(c, "AdminPricingHandler.Put", err)
		return
	}
	c.JSON(http.StatusOK, adminPricingResponse{
		Provider: provider,
		Models:   h.modelsForProvider(provider),
		Rates:    pricing.Get(provider),
		Defaults: pricing.Defaults(provider),
	})
}
