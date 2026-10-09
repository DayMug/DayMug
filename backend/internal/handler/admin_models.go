package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AdminModelsHandler exposes the admin-editable model registry. config.yaml
// declares which accounts exist (credentials, concurrency); which models each
// of those accounts offers is edited here and stored in app_settings, so an
// operator adding a model no longer has to touch a file and restart.
//
// A save takes effect on the next turn: the model picker reads /api/models,
// and the resolution helpers in service read the same hot-swapped cache.
type AdminModelsHandler struct {
	Cfg   *config.Config
	Store store.Store
}

func NewAdminModelsHandler(cfg *config.Config, s store.Store) *AdminModelsHandler {
	return &AdminModelsHandler{Cfg: cfg, Store: s}
}

// adminModelsAccount is one editable row. Models is what the account serves,
// in picker order (the first is the default for new conversations). There is
// no built-in list to inherit, so an account nobody configured has none.
// Overridden reports whether a registry row exists for the account.
type adminModelsAccount struct {
	Account      string   `json:"account"`
	Provider     string   `json:"provider"`
	Models       []string `json:"models"`
	SummaryModel string   `json:"summary_model"`
	// Specs is always an object (never null) so the editor can index it.
	Specs      map[string]service.ModelSpec `json:"specs"`
	Overridden bool                         `json:"overridden"`
}

type adminModelsResponse struct {
	Accounts []adminModelsAccount `json:"accounts"`
}

// snapshot builds the response from config (which accounts exist) joined with
// the active override cache (what each one serves).
func (h *AdminModelsHandler) snapshot() adminModelsResponse {
	out := adminModelsResponse{Accounts: []adminModelsAccount{}}
	if h.Cfg == nil {
		return out
	}
	for _, p := range h.Cfg.ProviderSnapshot() {
		if !service.IsValidProvider(p.Type) {
			continue
		}
		entry, overridden := service.ModelOverrideFor(p.Name)
		out.Accounts = append(out.Accounts, adminModelsAccount{
			Account:      p.Name,
			Provider:     p.Type,
			Models:       service.ModelsForAccount(h.Cfg, p.Type, p.Name),
			SummaryModel: entry.SummaryModel,
			Specs:        specsOrEmpty(entry.Specs),
			Overridden:   overridden,
		})
	}
	return out
}

// Get serves GET /api/admin/models — one row per configured account.
func (h *AdminModelsHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, h.snapshot())
}

type adminModelsRequest struct {
	// Accounts is keyed by account name. It replaces the whole registry
	// rather than patching it: the admin page always submits every row, and
	// a full replace is what makes "remove this account's override"
	// expressible without a separate delete verb.
	Accounts map[string]service.AccountModels `json:"accounts"`
}

// Put replaces the registry. Unknown account names are rejected rather than
// stored, so a typo surfaces immediately instead of becoming a dead row that
// silently never applies to anything.
func (h *AdminModelsHandler) Put(c *gin.Context) {
	var req adminModelsRequest
	if !bindJSON(c, &req) {
		return
	}
	known := map[string]struct{}{}
	if h.Cfg != nil {
		for _, p := range h.Cfg.ProviderSnapshot() {
			known[p.Name] = struct{}{}
		}
	}
	for account, entry := range req.Accounts {
		for model, spec := range entry.Specs {
			if spec.ContextWindow < 0 {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":   "model " + model + " of account " + account + " has a negative context window",
					"account": account,
				})
				return
			}
		}
	}
	for account, entry := range service.NormalizeModelOverrides(req.Accounts) {
		if _, ok := known[account]; !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "unknown account " + account,
				"account": account,
			})
			return
		}
		// Titles and /compact summaries run on the summary model. With no
		// built-in fallback list any more, an account that serves models must
		// name one explicitly rather than have the server guess an id.
		if len(entry.Models) > 0 && entry.SummaryModel == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "account " + account + " needs a summary model",
				"account": account,
			})
			return
		}
	}
	if err := service.SaveModelOverrides(c.Request.Context(), h.Store, req.Accounts); err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, h.snapshot())
}

func specsOrEmpty(specs map[string]service.ModelSpec) map[string]service.ModelSpec {
	if specs == nil {
		return map[string]service.ModelSpec{}
	}
	return specs
}
