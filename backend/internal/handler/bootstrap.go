package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// BootstrapHandler powers the first-run admin setup flow. The frontend probes
// Status on its login screen; when no login-capable users exist yet it diverts
// the visitor to a public form that hits CreateAdmin to provision the very
// first admin account. Both endpoints are unauthenticated by necessity (no
// session can exist before the first user does) but CreateAdmin self-disables
// the moment any human user is on file, so the public surface closes as soon
// as the install is past first-run.
type BootstrapHandler struct {
	Store store.Store
	Cfg   *config.Config
}

func NewBootstrapHandler(s store.Store, cfg *config.Config) *BootstrapHandler {
	return &BootstrapHandler{Store: s, Cfg: cfg}
}

// authOps assembles a service.AuthOps from the handler's current fields, so
// the first-run admin gets a session through the same code path as a password
// login or an OIDC callback. Built lazily per call for the same reason as
// AuthHandler.authOps.
func (h *BootstrapHandler) authOps() *service.AuthOps {
	return &service.AuthOps{Store: h.Store, Cfg: h.Cfg}
}

// ops assembles a service.BootstrapOps the same way and for the same reason.
func (h *BootstrapHandler) ops() *service.BootstrapOps {
	return &service.BootstrapOps{Store: h.Store, Cfg: h.Cfg}
}

// Status reports whether any login-capable user exists and whether the
// first-run form is needed. setup_required drives the login page's redirect to
// /setup/admin; it stays false on a fresh install whose SSO provisions the
// first admin by itself. setup_mode tells the form which variant to render
// (see service.FirstAdminSetupMode), including "unavailable", where it shows
// the config fix instead of fields.
func (h *BootstrapHandler) Status(c *gin.Context) {
	ops := h.ops()
	hasUsers, err := ops.HasHumanUsers(c.Request.Context())
	if err != nil {
		respondInternalError(c, "BootstrapHandler.Status", err)
		return
	}
	mode := service.FirstAdminSetup(h.Cfg)
	c.JSON(http.StatusOK, gin.H{
		"has_users":           hasUsers,
		"setup_required":      !hasUsers && mode != service.SetupModeSSO,
		"setup_mode":          mode,
		"min_password_length": service.MinPasswordLength,
	})
}

type bootstrapAdminRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// CreateAdmin provisions the first admin account on a fresh install and
// immediately opens a session for them so the SPA can bounce straight into
// the chat surface without a separate login round-trip. Rejects with 409 once
// any human user already exists — the endpoint is meant for first-run only —
// and with 403 when SSO provisions the first admin instead or no sign-in
// method is enabled at all.
func (h *BootstrapHandler) CreateAdmin(c *gin.Context) {
	hasUsers, err := h.ops().HasHumanUsers(c.Request.Context())
	if err != nil {
		respondInternalError(c, "BootstrapHandler.CreateAdmin", err)
		return
	}
	if hasUsers {
		c.JSON(http.StatusConflict, gin.H{"error": "an admin user already exists"})
		return
	}
	if err := service.FirstAdminSetup(h.Cfg).FormClosedError(); err != nil {
		respondServiceError(c, err)
		return
	}

	var req bootstrapAdminRequest
	if !bindJSON(c, &req) {
		return
	}
	user, err := h.ops().CreateFirstAdmin(c.Request.Context(), service.BootstrapAdminParams{
		Username: req.Username,
		Email:    req.Email,
		Password: req.Password,
		Name:     req.Name,
	})
	if err != nil {
		respondServiceError(c, err)
		return
	}

	ops := h.authOps()
	token, _, err := ops.OpenSession(c.Request.Context(), user.ID)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	setSessionCookie(c, h.Cfg, token, int(ops.SessionTTL().Seconds()))

	c.JSON(http.StatusCreated, gin.H{
		"id":       user.ID,
		"username": user.Username,
		"name":     user.Name,
		"is_admin": user.IsAdmin,
	})
}
