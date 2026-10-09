package handler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/service/imbridge"
)

type BotHandler struct {
	Store          store.Store
	Bots           store.BotStore
	Manager        *imbot.Manager
	TestConnection func(context.Context, imbot.BotConfig) imbot.ConnectionTestResult
	// WeChatGateway overrides the QR-login endpoint in tests. Empty means the
	// real Weixin gateway.
	WeChatGateway string
}

type botRequest struct {
	Name                    string `json:"name"`
	Platform                string `json:"platform"`
	Enabled                 bool   `json:"enabled"`
	Model                   string `json:"model"`
	MaxConversationDuration string `json:"max_conversation_duration"`
	// The credential fields are write-only and blank-preserving: an empty (or
	// whitespace-only) value means "keep whatever is stored", so the UI can let
	// someone retune channel rules without re-pasting a token it can no longer
	// read back. Only a non-empty value replaces the stored secret.
	BotToken          string `json:"bot_token"`
	BotAppToken       string `json:"bot_app_token"`
	BotAppID          string `json:"bot_app_id"`
	BotAppSecret      string `json:"bot_app_secret"`
	Channels          string `json:"channels"`
	UnconfiguredReply string `json:"unconfigured_reply"`
	UnauthorizedReply string `json:"unauthorized_reply"`
	// BotID is only read by Test: it names the saved bot whose stored
	// credentials should back-fill the blanks in this draft.
	BotID string `json:"bot_id"`
}

// botResponse is the outbound projection of store.Bot. Credentials leave the
// server only as "is it set" booleans; everything else round-trips verbatim.
type botResponse struct {
	ID                      string `json:"id"`
	AgentID                 string `json:"agent_id"`
	Name                    string `json:"name"`
	Platform                string `json:"platform"`
	Enabled                 bool   `json:"enabled"`
	Model                   string `json:"model"`
	MaxConversationDuration string `json:"max_conversation_duration"`

	BotTokenConfigured     bool `json:"bot_token_configured"`
	BotAppTokenConfigured  bool `json:"bot_app_token_configured"`
	BotAppIDConfigured     bool `json:"bot_app_id_configured"`
	BotAppSecretConfigured bool `json:"bot_app_secret_configured"`
	// CredentialsConfigured is true when every credential the bot's platform
	// needs to connect is present, i.e. when the bot could be enabled as-is.
	CredentialsConfigured bool `json:"credentials_configured"`

	Channels          string    `json:"channels"`
	UnconfiguredReply string    `json:"unconfigured_reply"`
	UnauthorizedReply string    `json:"unauthorized_reply"`
	CreatedAt         time.Time `json:"created_at"`
}

func newBotResponse(bot store.Bot) botResponse {
	resp := botResponse{
		ID: bot.ID, AgentID: bot.AgentID, Name: bot.Name, Platform: bot.Platform,
		Enabled: bot.Enabled, Model: bot.Model, MaxConversationDuration: bot.MaxConversationDuration,
		Channels:          bot.Channels,
		UnconfiguredReply: bot.UnconfiguredReply, UnauthorizedReply: bot.UnauthorizedReply,
		CreatedAt:              bot.CreatedAt,
		BotTokenConfigured:     strings.TrimSpace(bot.BotToken) != "",
		BotAppTokenConfigured:  strings.TrimSpace(bot.BotAppToken) != "",
		BotAppIDConfigured:     strings.TrimSpace(bot.BotAppID) != "",
		BotAppSecretConfigured: strings.TrimSpace(bot.BotAppSecret) != "",
	}
	resp.CredentialsConfigured = imbot.CredentialsComplete(bot.Platform, imbot.BotConfig{
		BotToken: bot.BotToken, AppToken: bot.BotAppToken, AppID: bot.BotAppID, AppSecret: bot.BotAppSecret,
	})
	return resp
}

func newBotResponses(bots []store.Bot) []botResponse {
	out := make([]botResponse, 0, len(bots))
	for _, bot := range bots {
		out = append(out, newBotResponse(bot))
	}
	return out
}

// params projects the request body onto the service-side field set. The json
// tags stay on botRequest so the wire format is described in one place.
func (r botRequest) params() service.BotParams {
	return service.BotParams{
		Name: r.Name, Platform: r.Platform, Enabled: r.Enabled, Model: r.Model,
		BotToken: r.BotToken, BotAppToken: r.BotAppToken,
		BotAppID: r.BotAppID, BotAppSecret: r.BotAppSecret,
		Channels: r.Channels, MaxConversationDuration: r.MaxConversationDuration,
		UnconfiguredReply: r.UnconfiguredReply,
		UnauthorizedReply: r.UnauthorizedReply,
	}
}

// ops assembles a service.BotOps from the handler's current fields. Built
// lazily per call for the same reason as the other handlers' factories:
// routes.go assigns Bots after NewBotHandler-style construction, so a snapshot
// taken earlier would capture a nil store.
func (h *BotHandler) ops() *service.BotOps {
	return &service.BotOps{Bots: h.Bots}
}

func (h *BotHandler) loadAgent(c *gin.Context) (store.User, bool) {
	agentUser, ok := loadOwnedUser(c, h.Store, c.Param("id"))
	if !ok {
		return store.User{}, false
	}
	if agentUser.Username != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bots can only be attached to agents"})
		return store.User{}, false
	}
	if h.Bots == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bot store is unavailable"})
		return store.User{}, false
	}
	return agentUser, true
}

// reload re-applies the IM bot topology after a mutation. It runs detached
// from the request on purpose: reconnecting sockets can take seconds and must
// not be charged to (or cancelled by) the HTTP call that triggered it, which
// has already been answered by the time this fires.
func (h *BotHandler) reload() {
	if h.Manager == nil || h.Store == nil || h.Bots == nil {
		return
	}
	go func() {
		cfg, err := imbridge.LoadIMBotConfig(context.Background(), h.Store, h.Bots)
		if err != nil {
			log.Printf("imbot: reload agent bots: %v", err)
			return
		}
		h.Manager.Apply(cfg)
	}()
}

func (h *BotHandler) List(c *gin.Context) {
	agentUser, ok := h.loadAgent(c)
	if !ok {
		return
	}
	bots, err := h.Bots.ListBots(c.Request.Context(), agentUser.ID)
	if err != nil {
		respondInternalError(c, "BotHandler.List", err)
		return
	}
	c.JSON(http.StatusOK, newBotResponses(bots))
}

func (h *BotHandler) Requirements(c *gin.Context) {
	if _, ok := h.loadAgent(c); !ok {
		return
	}
	c.JSON(http.StatusOK, imbot.PermissionRequirements())
}

func (h *BotHandler) Test(c *gin.Context) {
	agentUser, ok := h.loadAgent(c)
	if !ok {
		return
	}
	var req botRequest
	if !bindJSON(c, &req) {
		return
	}
	// Testing a saved bot: the UI never received its credentials, so blanks are
	// filled from storage. Scoped to the caller's own agent, which loadAgent has
	// already proven they own.
	p := req.params()
	if botID := strings.TrimSpace(req.BotID); botID != "" {
		existing, err := h.ops().LoadOwnedBot(c.Request.Context(), agentUser.ID, botID)
		if err != nil {
			respondServiceError(c, err)
			return
		}
		p = service.MergeStoredCredentials(p, existing)
	}
	cfg := imbot.BotConfig{
		Name: strings.TrimSpace(p.Name), Platform: strings.TrimSpace(p.Platform),
		BotToken: p.BotToken, AppToken: p.BotAppToken,
		AppID: p.BotAppID, AppSecret: p.BotAppSecret,
	}
	if _, ok := imbot.CapabilitiesFor(cfg.Platform); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "platform must be one of " + strings.Join(imbot.SupportedPlatforms(), ", ")})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	tester := h.TestConnection
	if tester == nil {
		tester = imbot.TestConnection
	}
	c.JSON(http.StatusOK, tester(ctx, cfg))
}

func (h *BotHandler) Create(c *gin.Context) {
	agentUser, ok := h.loadAgent(c)
	if !ok {
		return
	}
	var req botRequest
	if !bindJSON(c, &req) {
		return
	}
	created, err := h.ops().Create(c.Request.Context(), agentUser.ID, req.params())
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.reload()
	c.JSON(http.StatusCreated, newBotResponse(created))
}

func (h *BotHandler) Update(c *gin.Context) {
	agentUser, ok := h.loadAgent(c)
	if !ok {
		return
	}
	var req botRequest
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().Update(c.Request.Context(), agentUser.ID, c.Param("botId"), req.params())
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.reload()
	c.JSON(http.StatusOK, newBotResponse(updated))
}

func (h *BotHandler) Delete(c *gin.Context) {
	agentUser, ok := h.loadAgent(c)
	if !ok {
		return
	}
	if err := h.ops().Delete(c.Request.Context(), agentUser.ID, c.Param("botId")); err != nil {
		respondServiceError(c, err)
		return
	}
	h.reload()
	c.Status(http.StatusNoContent)
}
