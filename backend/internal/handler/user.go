package handler

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/service/imbridge"
)

// The agent CRUD rules that used to live in this file (field validation, the
// home-jail work_dir check, owner-id derivation, the store writes and their
// read-backs, and the sidebar visibility filter) moved to service.UserOps
// (internal/service/user_ops.go). What is left here is the HTTP adapter: bind
// the body, resolve the auth context and the :id param, delegate, render — plus
// reloadIntegrations, which is imbot orchestration rather than user domain.

type UserHandler struct {
	Store   store.Store
	Bots    store.BotStore
	Manager *imbot.Manager
	Cfg     *config.Config
}

func NewUserHandler(s store.Store) *UserHandler {
	return &UserHandler{Store: s}
}

// ops assembles a service.UserOps from the handler's current fields. Built
// lazily per call — routes.go and the tests set Cfg (and Bots/Manager) after
// NewUserHandler returns, so a snapshot taken in the constructor would
// permanently see a nil config and silently skip model validation.
func (h *UserHandler) ops() *service.UserOps {
	return &service.UserOps{Store: h.Store, Cfg: h.Cfg}
}

// userRequest is the agent-edit request body. Notification settings (BarkURL)
// are deliberately absent here — they live on the human owner now and are
// managed via /api/auth/notifications, not per-agent.
type userRequest struct {
	Name            string `json:"name"`
	WorkDir         string `json:"work_dir"`
	Avatar          string `json:"avatar"`
	RoleDefinition  string `json:"role_definition"`
	McpConfig       string `json:"mcp_config"`
	ClaudeMdContent string `json:"claude_md_content"`
	ManageClaudeMd  bool   `json:"manage_claude_md"`
	DefaultModel    string `json:"default_model"`
	ThinkLevel      string `json:"think_level"`
	CaseMode        bool   `json:"case_mode"`
}

// params maps the wire body onto the service-side field set. The JSON tags stay
// on userRequest so the transport format is owned by this layer alone.
func (req userRequest) params() service.AgentParams {
	return service.AgentParams{
		Name:            req.Name,
		WorkDir:         req.WorkDir,
		Avatar:          req.Avatar,
		RoleDefinition:  req.RoleDefinition,
		McpConfig:       req.McpConfig,
		ClaudeMdContent: req.ClaudeMdContent,
		ManageClaudeMd:  req.ManageClaudeMd,
		DefaultModel:    req.DefaultModel,
		ThinkLevel:      req.ThinkLevel,
		CaseMode:        req.CaseMode,
	}
}

// reloadIntegrations re-applies the IM bot topology after an agent mutation.
// Detached from the request context on purpose — same reasoning as
// BotHandler.reload: the reconnect outlives the HTTP response, so binding it
// to the request ctx would abort the reload the moment the client got its 200.
func (h *UserHandler) reloadIntegrations() {
	if h.Manager == nil || h.Store == nil || h.Bots == nil {
		return
	}
	go func() {
		cfg, err := imbridge.LoadIMBotConfig(context.Background(), h.Store, h.Bots)
		if err != nil {
			log.Printf("imbot: reload agent integrations: %v", err)
			return
		}
		h.Manager.Apply(cfg)
	}()
}

func (h *UserHandler) Create(c *gin.Context) {
	var req userRequest
	if !bindJSON(c, &req) {
		return
	}
	created, err := h.ops().CreateAgent(c.Request.Context(), req.params(), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.reloadIntegrations()
	h.attachBotPlatformsOne(c.Request.Context(), &created)
	c.JSON(http.StatusCreated, created)
}

// loadOwnedUser reads the :id path param and delegates to the package-level
// loadOwnedUser, which writes the standard error responses on failure.
func (h *UserHandler) loadOwnedUser(c *gin.Context) (store.User, bool) {
	return loadOwnedUser(c, h.Store, c.Param("id"))
}

// IntegrationStatus returns live connection state for every bot owned by one
// agent. Tokens never leave the existing owner-scoped agent response.
func (h *UserHandler) IntegrationStatus(c *gin.Context) {
	user, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	if user.Username != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "integrations belong to agents"})
		return
	}
	statuses := []imbot.BotStatus{}
	if h.Manager != nil {
		statuses = h.Manager.Statuses(user.ID)
	}
	c.JSON(http.StatusOK, gin.H{"bots": statuses})
}

// List returns the rows the caller may act on: their own user plus every
// agent they own (agent.OwnerID == caller.ID). Other human users and
// agents owned by someone else are filtered out so the frontend can't even
// discover them — matches canAccessOwner semantics and prevents the
// "selectUser succeeds, file API 403s" UX trap.
//
// Strict ownership: admins are NOT special-cased here. The chat page's
// agent picker is per-user; admins manage the broader system through
// dedicated /api/admin/* endpoints rather than seeing other people's
// agents/users via this list.
//
// When no auth middleware ran (authedUID == ""), the unfiltered list is
// returned. Production always wires this behind RequireAuth; the bypass
// only exists for unit tests that exercise the handler in isolation.
func (h *UserHandler) List(c *gin.Context) {
	users, err := h.ops().VisibleUsers(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.attachBotPlatforms(c.Request.Context(), users)
	c.JSON(http.StatusOK, users)
}

// attachBotPlatforms decorates agent rows with the platforms of their attached
// bots so the chat sidebar can distinguish bot-capable agents without one
// request per agent. Disabled bots still count as attachments: enabled controls
// the transport runtime, not whether the bot belongs to the agent. Decoration
// only — the sidebar still renders if the lookup fails.
func (h *UserHandler) attachBotPlatforms(ctx context.Context, users []store.User) {
	decorateBotPlatforms(ctx, h.Bots, users)
}

// attachBotPlatformsOne decorates a single row in place. Mutation responses
// need it because the frontend swaps the whole cached row for the payload it
// gets back (useUsers.handleUpdateUser); an undecorated response therefore
// erases the bot badge of an agent that does have bots, until the next
// cold-start aggregate rebuilds the roster.
func (h *UserHandler) attachBotPlatformsOne(ctx context.Context, user *store.User) {
	rows := []store.User{*user}
	decorateBotPlatforms(ctx, h.Bots, rows)
	*user = rows[0]
}

// decorateBotPlatforms is shared by the dedicated users endpoint and the
// cold-start app-state aggregate so the first chat paint cannot lose bot
// identity metadata by taking the faster bootstrap path.
func decorateBotPlatforms(ctx context.Context, bots store.BotStore, users []store.User) {
	if bots == nil || len(users) == 0 {
		return
	}
	attached, err := bots.ListBots(ctx, "")
	if err != nil {
		return
	}
	byAgent := map[string][]string{}
	for _, bot := range attached {
		byAgent[bot.AgentID] = append(byAgent[bot.AgentID], bot.Platform)
	}
	for i := range users {
		users[i].BotPlatforms = byAgent[users[i].ID]
	}
}

// callerOwnerID resolves the human-owner id of the authenticated caller. Empty
// when no auth middleware ran (unit tests). Writes the standard error response
// and returns ok=false when the caller row can't be loaded.
func (h *UserHandler) callerOwnerID(c *gin.Context) (string, bool) {
	ownerID, err := h.ops().CallerOwnerID(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return "", false
	}
	return ownerID, true
}

// Reorder persists the manual sidebar order of the caller's rows from a
// drag-to-reorder gesture. The body carries the full ordered id list — the
// caller's own human row plus its agents, freely interleaved; the store treats
// it as authoritative for the caller's rows and skips any id the caller doesn't
// own. Returns the refreshed owned list.
func (h *UserHandler) Reorder(c *gin.Context) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !bindJSON(c, &req) {
		return
	}
	users, err := h.ops().Reorder(c.Request.Context(), middleware.CurrentUserID(c), req.IDs)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.attachBotPlatforms(c.Request.Context(), users)
	c.JSON(http.StatusOK, users)
}

// Archive hides one of the caller's agents from the sidebar. Login users
// (humans) cannot be archived through this endpoint.
func (h *UserHandler) Archive(c *gin.Context) {
	target, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	if err := h.ops().Archive(c.Request.Context(), target); err != nil {
		respondServiceError(c, err)
		return
	}
	h.reloadIntegrations()
	c.Status(http.StatusNoContent)
}

// Unarchive restores a previously archived agent to the sidebar and returns the
// refreshed row.
func (h *UserHandler) Unarchive(c *gin.Context) {
	target, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	updated, err := h.ops().Unarchive(c.Request.Context(), target)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.reloadIntegrations()
	h.attachBotPlatformsOne(c.Request.Context(), &updated)
	c.JSON(http.StatusOK, updated)
}

// ListArchived returns the caller's archived agents for the settings restore
// view.
func (h *UserHandler) ListArchived(c *gin.Context) {
	ownerID, ok := h.callerOwnerID(c)
	if !ok {
		return
	}
	users, err := h.Store.ListArchivedAgents(c.Request.Context(), ownerID)
	if err != nil {
		respondInternalError(c, "UserHandler.ListArchived", err)
		return
	}
	if users == nil {
		users = []store.User{}
	}
	h.attachBotPlatforms(c.Request.Context(), users)
	c.JSON(http.StatusOK, users)
}

func (h *UserHandler) Update(c *gin.Context) {
	target, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	var req userRequest
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().UpdateAgent(c.Request.Context(), target, req.params(), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	h.reloadIntegrations()
	h.attachBotPlatformsOne(c.Request.Context(), &updated)
	c.JSON(http.StatusOK, updated)
}

// Duplicate copies an existing agent (all config fields) into a new user
// with a fresh UUID. The display name gets a " (copy)" suffix; everything
// else — work_dir, role, tool lists, MCP config, CLAUDE.md
// content — is mirrored verbatim. Username/password are intentionally NOT
// copied: a duplicated agent has no login until the admin sets credentials
// in the editor. BarkURL is also not copied — notification config lives on
// the human owner now, so the agent row's column is unused.
func (h *UserHandler) Duplicate(c *gin.Context) {
	src, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	dup, err := h.ops().DuplicateAgent(c.Request.Context(), src, middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, dup)
}

func (h *UserHandler) Delete(c *gin.Context) {
	target, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	if err := h.ops().Delete(c.Request.Context(), target); err != nil {
		respondServiceError(c, err)
		return
	}
	h.reloadIntegrations()
	c.Status(http.StatusNoContent)
}

func (h *UserHandler) GetClaudeMd(c *gin.Context) {
	user, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	if user.WorkDir == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user has no work_dir"})
		return
	}

	content, err := service.ReadClaudeMd(user.WorkDir)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "CLAUDE.md not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"content": content})
}

func (h *UserHandler) PutClaudeMd(c *gin.Context) {
	user, ok := h.loadOwnedUser(c)
	if !ok {
		return
	}
	if user.WorkDir == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user has no work_dir"})
		return
	}

	var req struct {
		Content string `json:"content"`
	}
	if !bindJSON(c, &req) {
		return
	}

	if err := service.SyncClaudeMd(user.WorkDir, req.Content); err != nil {
		respondInternalError(c, "UserHandler.PutClaudeMd", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"content": req.Content})
}
