package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// ServerInfoHandler exposes a small slice of server identity / capability to
// every authenticated user: the running version and whether the sandbox is
// active.
//
// Kept separate from the admin /config/public endpoint because that one is
// admin-only by design (it leaks tooling lists, account names, etc.); this
// handler returns only fields safe for any logged-in user to see.
type ServerInfoHandler struct {
	Cfg            *config.Config
	CurrentVersion string
}

// NewServerInfoHandler returns a handler bound to the given config and
// build-time version. cfg may be nil in unit tests; the handler treats nil
// the same way it would treat a config with sandboxing disabled.
func NewServerInfoHandler(cfg *config.Config, version string) *ServerInfoHandler {
	return &ServerInfoHandler{Cfg: cfg, CurrentVersion: version}
}

// Get serves GET /api/server-info. Response shape is intentionally flat and
// minimal — every field is added because the frontend has a concrete use for
// it. Adding a new field for "this looked nice" inflates the public surface.
func (h *ServerInfoHandler) Get(c *gin.Context) {
	sandboxEnabled := false
	sandboxType := ""
	if h.Cfg != nil {
		sandboxEnabled = h.Cfg.Sandbox.Enabled
		sandboxType = h.Cfg.Sandbox.Type
	}
	c.JSON(http.StatusOK, gin.H{
		"version":         h.CurrentVersion,
		"sandbox_enabled": sandboxEnabled,
		"sandbox_type":    sandboxType,
		// DefaultManifestURL is hard-coded server-side; surfaced so clients
		// can link to release notes without a second round-trip.
		"manifest_url": service.DefaultManifestURL,
	})
}
