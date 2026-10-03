package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// ModelsHandler serves a read-only view of the per-provider model registry
// that backs the conversation model picker in the UI. The registry starts
// from service.ProviderModels and may be overridden by config.yaml; shipping
// it through an endpoint keeps the frontend from redeclaring the same list,
// surfaces server-side rules (e.g. the default provider) on a single
// round-trip, and now also exposes each backend's Capabilities so the UI
// can gate features (the /compact button, the thinking pane) without
// hardcoding provider-name string checks.
type ModelsHandler struct {
	Cfg *config.Config
	// Backends mirrors the runtime BackendRegistry: one entry per provider
	// name. We read each backend's self-reported Capabilities() to ship
	// per-provider feature flags down to the UI. Optional — nil falls
	// back to an empty Capabilities matrix (everything disabled), which
	// keeps tests that build the handler by hand from crashing.
	Backends *service.BackendRegistry
}

func NewModelsHandler(cfg *config.Config) *ModelsHandler {
	return &ModelsHandler{Cfg: cfg}
}

// providerCapabilities is the wire shape of agent.Capabilities — kept
// in snake_case to match the rest of the API and small enough that the
// frontend can pluck the bits it cares about without a typegen tool.
type providerCapabilities struct {
	SupportsCompaction      bool `json:"supports_compaction"`
	SupportsThinkingStream  bool `json:"supports_thinking_stream"`
	SupportsRateLimitEvents bool `json:"supports_rate_limit_events"`
	ReportsContextUsage     bool `json:"reports_context_usage"`
	ReportsCostUSD          bool `json:"reports_cost_usd"`
	// SupportsSteering means a message sent while a turn runs can be inserted
	// into that turn. The composer makes "insert" its default send action when
	// true and only offers "send after current" when false (CLI transports).
	SupportsSteering bool `json:"supports_steering"`
}

type providerEntry struct {
	Name         string               `json:"name"`
	Models       []string             `json:"models"`
	Latest       string               `json:"latest"`
	Capabilities providerCapabilities `json:"capabilities"`
	// Transport is the admin-selected transport ("cli", "agent-sdk",
	// "app-server") the provider type currently runs on.
	Transport string `json:"transport"`
}

// accountEntry is the per-account model view. The picker keys its account rows
// off these so each globally unique account surfaces exactly its own models —
// a stock `codex` account on gpt-5.x and a `codex-qwen` account on a local Qwen
// no longer collapse into one type-level list. Provider (the type) rides along
// so the frontend can pin both fields from a single selection and still resolve
// type-level capabilities.
type accountEntry struct {
	Provider string   `json:"provider"`
	Account  string   `json:"account"`
	Models   []string `json:"models"`
	Latest   string   `json:"latest"`
	// Specs carries the admin-declared per-model limits (context window,
	// text-only) so the composer can warn before an image goes to a model
	// that can't read it.
	Specs map[string]service.ModelSpec `json:"specs"`
}

type modelsResponse struct {
	Providers       []providerEntry `json:"providers"`
	Accounts        []accountEntry  `json:"accounts"`
	DefaultProvider string          `json:"default_provider"`
}

func (h *ModelsHandler) capabilitiesFor(name string) providerCapabilities {
	b, ok := h.Backends.Lookup(name)
	if !ok {
		return providerCapabilities{}
	}
	c := b.Capabilities()
	return providerCapabilities{
		SupportsCompaction:      c.SupportsCompaction,
		SupportsThinkingStream:  c.SupportsThinkingStream,
		SupportsRateLimitEvents: c.SupportsRateLimitEvents,
		ReportsContextUsage:     c.ReportsContextUsage,
		ReportsCostUSD:          c.ReportsCostUSD,
		SupportsSteering:        agent.OptionalCapabilitiesOf(b).Steering,
	}
}

// List serves GET /api/models. Mounted on the authenticated group — the
// registry isn't a secret but the rest of the conversation flow lives
// behind auth, so there is no reason to expose this anonymously.
func (h *ModelsHandler) List(c *gin.Context) {
	providerNames := service.ProvidersForConfig(h.Cfg)
	providers := make([]providerEntry, 0, len(providerNames))
	for _, name := range providerNames {
		out := service.ModelsForConfig(h.Cfg, name)
		providers = append(providers, providerEntry{
			Name:         name,
			Models:       out,
			Latest:       service.LatestModelForConfig(h.Cfg, name),
			Capabilities: h.capabilitiesFor(name),
			Transport:    service.TransportFor(name),
		})
	}

	// One entry per configured account, in YAML order, carrying that
	// account's own model list. The picker renders each globally unique account
	// name and looks up models by account, so sibling accounts of the same type
	// stay independent.
	var accounts []accountEntry
	if h.Cfg != nil {
		configured := h.Cfg.ProviderSnapshot()
		accounts = make([]accountEntry, 0, len(configured))
		for _, p := range configured {
			if !service.IsValidProvider(p.Type) {
				continue
			}
			accounts = append(accounts, accountEntry{
				Provider: p.Type,
				Account:  p.Name,
				Models:   service.ModelsForAccount(h.Cfg, p.Type, p.Name),
				Latest:   service.LatestModelForAccount(h.Cfg, p.Type, p.Name),
				Specs:    accountSpecs(p.Name),
			})
		}
	}

	c.JSON(http.StatusOK, modelsResponse{
		Providers:       providers,
		Accounts:        accounts,
		DefaultProvider: h.Cfg.DefaultProviderType(),
	})
}

func accountSpecs(account string) map[string]service.ModelSpec {
	entry, _ := service.ModelOverrideFor(account)
	return specsOrEmpty(entry.Specs)
}
