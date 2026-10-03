package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AdminHandler exposes admin-only operations: human user CRUD, password reset,
// enable/disable, and a read-only view of the YAML config bits the admin UI
// needs (claude accounts, available tools, default home root). Mounted under
// /api/admin/* with both RequireAuth and RequireAdmin.
//
// The write use-cases (invariant checks, provider validation, the store writes
// and their side effects) live in service.AdminUserOps
// (internal/service/admin_user_ops.go). What remains here is the HTTP adapter:
// bind the body, resolve the :id param and the auth context, delegate, render —
// plus the read-only config/database projections, which are transport-shaped
// views rather than use-cases.
type AdminHandler struct {
	Store          store.Store
	Cfg            *config.Config
	CurrentVersion string
}

func NewAdminHandler(s store.Store, cfg *config.Config) *AdminHandler {
	return &AdminHandler{
		Store: s,
		Cfg:   cfg,
	}
}

// ops assembles a service.AdminUserOps from the handler's current fields. Built
// lazily per call — routes.go assigns handler fields after NewAdminHandler
// returns, so a snapshot taken in the constructor could permanently capture a
// nil config and silently skip provider/model validation.
func (h *AdminHandler) ops() *service.AdminUserOps {
	return &service.AdminUserOps{Store: h.Store, Cfg: h.Cfg}
}

type adminCreateUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"is_admin"`
	WorkDir  string `json:"work_dir"`
	// ProviderBindings maps CLI type to provider name. Each entry is
	// validated against the live YAML config; unknown (name, type) pairs
	// reject the request. Empty values clear the slot.
	ProviderBindings map[string]string `json:"provider_bindings,omitempty"`
	// ProviderAccounts grants a SET of accounts per CLI type (the list is
	// default-first). When a type appears here it is authoritative for that
	// type and supersedes the single-value ProviderBindings path. Each
	// (name, type) is validated against the live config.
	ProviderAccounts map[string][]string `json:"provider_accounts,omitempty"`
	// SandboxMode is the agent isolation mode for the new user. Empty defaults
	// to jailed (CreateUser normalises it). Only "unrestricted" grants full
	// host reach.
	SandboxMode string `json:"sandbox_mode,omitempty"`
}

type adminUpdateUserRequest struct {
	Name    *string `json:"name,omitempty"`
	Email   *string `json:"email,omitempty"`
	WorkDir *string `json:"work_dir,omitempty"`
	// ProviderBindings patches one or more bindings. Each map entry maps
	// CLI type → provider name; missing keys mean "don't touch", empty
	// strings clear the slot, non-empty values upsert. Validated against
	// the live config before any write.
	ProviderBindings map[string]string `json:"provider_bindings,omitempty"`
	// ProviderAccounts patches the full account SET per CLI type (default
	// first). A type present here is authoritative and supersedes the
	// single-value ProviderBindings path for that type.
	ProviderAccounts map[string][]string `json:"provider_accounts,omitempty"`
	IsAdmin          *bool               `json:"is_admin,omitempty"`
	// DefaultModel patches the per-user default model new conversations
	// inherit. *string so nil = "don't touch", "" = clear. A non-empty value
	// must resolve to a configured provider (see validateDefaultModel).
	DefaultModel *string `json:"default_model,omitempty"`
	// SandboxMode patches the agent isolation mode. *string so nil = "don't
	// touch"; a non-empty value must be "jailed" or "unrestricted".
	SandboxMode *string `json:"sandbox_mode,omitempty"`
}

// params maps the create body onto the service-side field set. The JSON tags
// stay on adminCreateUserRequest so the wire format is owned by this layer.
func (req adminCreateUserRequest) params() service.AdminCreateUserParams {
	return service.AdminCreateUserParams{
		Username:         req.Username,
		Password:         req.Password,
		Name:             req.Name,
		Email:            req.Email,
		IsAdmin:          req.IsAdmin,
		WorkDir:          req.WorkDir,
		ProviderBindings: req.ProviderBindings,
		ProviderAccounts: req.ProviderAccounts,
		SandboxMode:      req.SandboxMode,
	}
}

// patch maps the update body onto the service-side patch. Pointer fields are
// carried through as pointers so "don't touch" stays distinguishable from
// "clear".
func (req adminUpdateUserRequest) patch() service.AdminUserPatch {
	return service.AdminUserPatch{
		Name:             req.Name,
		Email:            req.Email,
		WorkDir:          req.WorkDir,
		ProviderBindings: req.ProviderBindings,
		ProviderAccounts: req.ProviderAccounts,
		IsAdmin:          req.IsAdmin,
		DefaultModel:     req.DefaultModel,
		SandboxMode:      req.SandboxMode,
	}
}

type adminBatchDefaultModelRequest struct {
	UserIDs []string `json:"user_ids"`
	Model   string   `json:"model"`
}

type adminBatchProviderBindingRequest struct {
	UserIDs       []string `json:"user_ids"`
	ProviderType  string   `json:"provider_type"`
	ProviderNames []string `json:"provider_names"`
}

// BatchSetDefaultModel sets (or clears, when model is empty) the per-user
// default model for every user in user_ids in one call.
func (h *AdminHandler) BatchSetDefaultModel(c *gin.Context) {
	var req adminBatchDefaultModelRequest
	if !bindJSON(c, &req) {
		return
	}
	model, err := h.ops().BatchSetDefaultModel(c.Request.Context(), req.UserIDs, req.Model)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(req.UserIDs), "model": model})
}

type adminBatchSandboxModeRequest struct {
	UserIDs     []string `json:"user_ids"`
	SandboxMode string   `json:"sandbox_mode"`
}

// BatchSetSandboxMode sets the agent isolation mode (jailed / unrestricted) for
// every user in user_ids in one call. Unrestricted lets the user's agent reach
// the whole host filesystem; jailed confines it to the user's work_dir once the
// server-side bwrap sandbox is enabled.
func (h *AdminHandler) BatchSetSandboxMode(c *gin.Context) {
	var req adminBatchSandboxModeRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.ops().BatchSetSandboxMode(c.Request.Context(), req.UserIDs, req.SandboxMode); err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(req.UserIDs), "sandbox_mode": req.SandboxMode})
}

// BatchSetProviderBinding replaces the accounts for one provider type on
// selected human users. The first account is the default; an empty list clears
// the type.
func (h *AdminHandler) BatchSetProviderBinding(c *gin.Context) {
	var req adminBatchProviderBindingRequest
	if !bindJSON(c, &req) {
		return
	}
	providerType, providerNames, err := h.ops().BatchSetProviderBinding(
		c.Request.Context(), req.UserIDs, req.ProviderType, req.ProviderNames)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"updated":        len(req.UserIDs),
		"provider_type":  providerType,
		"provider_names": providerNames,
	})
}

type adminPasswordRequest struct {
	Password string `json:"password"`
}

// ListHumans returns all login-capable users (rows with a non-empty username).
// Agents (persona rows owned by humans) are excluded — those are listed in the
// per-human user UI, not the admin panel.
func (h *AdminHandler) ListHumans(c *gin.Context) {
	users, err := h.Store.ListUsers(c.Request.Context())
	if err != nil {
		respondInternalError(c, "AdminHandler.ListHumans", err)
		return
	}
	out := make([]store.User, 0, len(users))
	for _, u := range users {
		if u.Username != "" {
			u.Env = ""
			out = append(out, u)
		}
	}
	c.JSON(http.StatusOK, out)
}

// CreateHuman provisions a new login-capable user. work_dir defaults to
// default_home_root/<username> when not supplied; the directory is created on
// disk so the user has a valid root from their first session. Username is used
// (rather than the opaque uuid) so the directory layout stays human-readable.
//
// Enforces the username==email-prefix invariant: every login-capable row must
// have a non-empty email whose local-part equals the username. This is what
// lets `Store.GetOwner` resolve agents (which inherit the human's email) back
// to a unique human row without an explicit owner_id column.
func (h *AdminHandler) CreateHuman(c *gin.Context) {
	var req adminCreateUserRequest
	if !bindJSON(c, &req) {
		return
	}
	created, err := h.ops().CreateHuman(c.Request.Context(), req.params())
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

// UpdateHuman applies admin-only field changes. All fields are pointers so
// the caller can patch a single field without clobbering the rest.
func (h *AdminHandler) UpdateHuman(c *gin.Context) {
	// Loaded before the body is bound so a bad :id keeps answering 400/404
	// rather than whatever the body would have failed with.
	target, ok := h.loadHuman(c)
	if !ok {
		return
	}

	var req adminUpdateUserRequest
	if !bindJSON(c, &req) {
		return
	}

	updated, err := h.ops().UpdateHuman(c.Request.Context(), target, req.patch(), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// SetPassword resets the target user's password. Admin-only — the user can
// rotate their own password through a separate self-service flow (TODO).
func (h *AdminHandler) SetPassword(c *gin.Context) {
	target, ok := h.loadHuman(c)
	if !ok {
		return
	}
	var req adminPasswordRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.ops().SetPassword(c.Request.Context(), target, req.Password); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AdminHandler) Disable(c *gin.Context) {
	h.toggleDisabled(c, true)
}

func (h *AdminHandler) Enable(c *gin.Context) {
	h.toggleDisabled(c, false)
}

func (h *AdminHandler) toggleDisabled(c *gin.Context, disabled bool) {
	target, ok := h.loadHuman(c)
	if !ok {
		return
	}
	if err := h.ops().SetDisabled(c.Request.Context(), target, disabled, middleware.CurrentUserID(c)); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AdminHandler) Delete(c *gin.Context) {
	target, ok := h.loadHuman(c)
	if !ok {
		return
	}
	if err := h.ops().DeleteHuman(c.Request.Context(), target, middleware.CurrentUserID(c)); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// PublicConfig exposes the admin-relevant slice of config: provider account
// names + concurrency, the default home root, and the sandbox state. Secrets
// (env vars, config_dir) are stripped.
func (h *AdminHandler) PublicConfig(c *gin.Context) {
	if h.Cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config not loaded"})
		return
	}
	configured := h.Cfg.ProviderSnapshot()
	providers := make([]gin.H, 0, len(configured))
	for _, a := range configured {
		providers = append(providers, gin.H{
			"name":           a.Name,
			"type":           a.Type,
			"max_concurrent": a.MaxConcurrent,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"default_home_root": h.Cfg.Users.DefaultHomeRoot,
		"default_cli_type":  h.Cfg.DefaultProviderType(),
		"providers":         providers,
		"sandbox": gin.H{
			"enabled": h.Cfg.Sandbox.Enabled,
			"type":    h.Cfg.Sandbox.Type,
		},
		"upgrade": gin.H{
			// Always available — manifest URL is hard-coded server-side.
			"enabled": true,
		},
		"current_version": h.CurrentVersion,
		"backend":         h.Store.Backend(),
	})
}

// DatabaseSize reports the on-disk size of the database so the admin UI
// can display "currently X MB, click to optimize" without first running a
// VACUUM. Returns zero for non-sqlite or in-memory backends; the frontend
// renders zero as "unknown".
func (h *AdminHandler) DatabaseSize(c *gin.Context) {
	bytes, err := h.Store.DatabaseSize(c.Request.Context())
	if err != nil {
		respondInternalError(c, "AdminHandler.DatabaseSize", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"bytes": bytes})
}

// OptimizeDatabase reaps stale rows and rebuilds the database file so
// SQLite returns free pages to the OS. Gated to SQLite — for any future
// backend the admin UI will hide the button, but defense-in-depth here
// rejects the call even if a stale frontend tries to fire it.
func (h *AdminHandler) OptimizeDatabase(c *gin.Context) {
	if h.Store.Backend() != "sqlite" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "database optimization is only supported on the sqlite backend",
		})
		return
	}
	res, err := h.Store.OptimizeDatabase(c.Request.Context())
	if err != nil {
		respondInternalError(c, "AdminHandler.OptimizeDatabase", err)
		return
	}
	reclaimed := res.BeforeBytes - res.AfterBytes
	if reclaimed < 0 {
		// The post-VACUUM size can briefly exceed the pre-VACUUM size when
		// a fresh WAL/SHM is allocated on a previously-quiet database.
		// Report 0 rather than a negative number so the UI doesn't suggest
		// the optimizer made things worse.
		reclaimed = 0
	}
	c.JSON(http.StatusOK, gin.H{
		"before_bytes":             res.BeforeBytes,
		"after_bytes":              res.AfterBytes,
		"bytes_reclaimed":          reclaimed,
		"expired_sessions_deleted": res.ExpiredSessionsDeleted,
		"purged_conversations":     res.PurgedConversations,
		"purged_users":             res.PurgedUsers,
		"purged_agents":            res.PurgedAgents,
	})
}

// loadHuman fetches the user named by :id, ensures it has a username (i.e. is
// a real human, not an agent persona), and writes the appropriate error
// response on failure. Returns ok=false on any failure path.
func (h *AdminHandler) loadHuman(c *gin.Context) (store.User, bool) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing user id"})
		return store.User{}, false
	}
	user, err := h.Store.GetUser(c.Request.Context(), id)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return store.User{}, false
	}
	if user.Username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is not a human user"})
		return store.User{}, false
	}
	return user, true
}
