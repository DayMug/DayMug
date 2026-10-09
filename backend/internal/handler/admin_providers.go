package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// AdminProvidersHandler owns the database-backed credential pool registry.
// Config remains the shared runtime read cache so existing execution paths do
// not query SQLite on every turn; saves hot-swap that cache and update Pool.
type AdminProvidersHandler struct {
	Cfg   *config.Config
	Store service.AppSettingStore
	Pool  *service.Pool
}

func NewAdminProvidersHandler(cfg *config.Config, store service.AppSettingStore, pool *service.Pool) *AdminProvidersHandler {
	return &AdminProvidersHandler{Cfg: cfg, Store: store, Pool: pool}
}

type adminProvidersResponse struct {
	Providers []config.Provider `json:"providers"`
}

func (h *AdminProvidersHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, adminProvidersResponse{Providers: h.Cfg.ProviderSnapshot()})
}

func (h *AdminProvidersHandler) Put(c *gin.Context) {
	var req adminProvidersResponse
	if !bindJSON(c, &req) {
		return
	}
	if err := service.SaveProviderSettings(c.Request.Context(), h.Store, h.Cfg, req.Providers); err != nil {
		respondServiceError(c, err)
		return
	}
	providers := h.Cfg.ProviderSnapshot()
	h.Pool.Reconfigure(providers)
	c.JSON(http.StatusOK, adminProvidersResponse{Providers: providers})
}
