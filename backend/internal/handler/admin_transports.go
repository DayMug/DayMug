package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudeagentsdk"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// AdminTransportsHandler lets an admin pick, per provider type, whether chat
// runs over the CLI or the structured transport (Agent SDK / app-server). The
// choice is type-wide because a conversation may land on any granted account
// of its type, and it applies from the next turn — a turn already running
// finishes on the transport it started with.
type AdminTransportsHandler struct {
	Store service.AppSettingStore
	// Probe checks the host can actually run a transport before it is saved,
	// so a missing SDK or CLI is refused on the settings page instead of
	// failing every chat afterwards. Nil skips the check (tests).
	Probe func(ctx context.Context, provider, transport string) error
}

func NewAdminTransportsHandler(store service.AppSettingStore) *AdminTransportsHandler {
	return &AdminTransportsHandler{Store: store, Probe: probeTransport}
}

type adminTransport struct {
	Provider  string   `json:"provider"`
	Transport string   `json:"transport"`
	Options   []string `json:"options"`
}

type adminTransportsResponse struct {
	Transports []adminTransport `json:"transports"`
}

type adminTransportsRequest struct {
	Transports map[string]string `json:"transports"`
}

func transportsSnapshot() adminTransportsResponse {
	out := adminTransportsResponse{Transports: make([]adminTransport, 0, len(config.SupportedCLITypes))}
	for _, provider := range config.SupportedCLITypes {
		out.Transports = append(out.Transports, adminTransport{
			Provider:  provider,
			Transport: service.TransportFor(provider),
			Options:   service.TransportOptions(provider),
		})
	}
	return out
}

// Get serves GET /api/admin/transports — one row per supported provider type.
func (h *AdminTransportsHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, transportsSnapshot())
}

// Put serves PUT /api/admin/transports. Types omitted from the body keep their
// current transport. Only types whose transport actually changes are probed.
func (h *AdminTransportsHandler) Put(c *gin.Context) {
	var req adminTransportsRequest
	if !bindJSON(c, &req) {
		return
	}
	if _, err := service.NormalizeTransports(req.Transports); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.Probe != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		for provider, transport := range req.Transports {
			if transport == "" || transport == service.TransportFor(provider) {
				continue
			}
			if err := h.Probe(ctx, provider, transport); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":    fmt.Sprintf("%s cannot run on %s: %v", provider, transport, err),
					"provider": provider,
				})
				return
			}
		}
	}
	if err := service.SaveTransports(c.Request.Context(), h.Store, req.Transports); err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, transportsSnapshot())
}

// probeTransport verifies the host has what a transport needs: the Agent SDK
// needs node plus the npm package, the Claude CLI transport needs a `claude`
// binary that honours CLAUDE_CONFIG_DIR, and both Codex transports need a
// codex binary new enough to ship app-server.
func probeTransport(ctx context.Context, provider, transport string) error {
	switch {
	case transport == service.TransportAgentSDK:
		if _, err := exec.LookPath("node"); err != nil {
			return errors.New("node.js 18+ is required by the Claude Agent SDK")
		}
		if _, _, found := claudeagentsdk.LocateSDK(); !found {
			return errors.New("agent SDK for Claude not found; install it with `npm install -g @anthropic-ai/claude-agent-sdk`")
		}
	case transport == service.TransportCLI && config.IsCodexFamily(provider):
		return agent.ProbeCodexAppServerSupport(ctx)
	case transport == service.TransportCLI:
		return agent.ProbeClaudeConfigDirSupport(ctx)
	case transport == service.TransportAppServer:
		return agent.ProbeCodexAppServerSupport(ctx)
	}
	return nil
}
