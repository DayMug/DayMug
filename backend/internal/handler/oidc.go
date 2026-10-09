package handler

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// OIDCHandler wires the optional Casdoor / generic OIDC login flow into the
// HTTP API. When cfg.OIDC.Enabled is false, RegisterRoutes registers no OIDC
// routes and this handler is never instantiated. The auth.options endpoint
// reflects that state so the frontend hides the SSO button.
type OIDCHandler struct {
	Store store.Store
	Cfg   *config.Config
	Svc   *service.OIDCService
}

func NewOIDCHandler(s store.Store, cfg *config.Config, svc *service.OIDCService) *OIDCHandler {
	return &OIDCHandler{Store: s, Cfg: cfg, Svc: svc}
}

// authOps assembles a service.AuthOps from the handler's current fields so the
// SSO callback opens its session through the same code path as password login.
// Built lazily per call for the same reason as AuthHandler.authOps.
func (h *OIDCHandler) authOps() *service.AuthOps {
	return &service.AuthOps{Store: h.Store, Cfg: h.Cfg}
}

// userOps assembles the SSO auto-provisioning use case. Deliberately a distinct
// op from AdminUserOps.CreateHuman rather than a mode of it — see the note on
// service.OIDCUserOps. Built lazily for the same reason as authOps.
func (h *OIDCHandler) userOps() *service.OIDCUserOps {
	return &service.OIDCUserOps{Store: h.Store, Cfg: h.Cfg}
}

// Login builds an authorize URL with a fresh state+nonce and 302s the
// browser to the IdP.
func (h *OIDCHandler) Login(c *gin.Context) {
	if h.Svc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oidc not enabled"})
		return
	}
	c.Redirect(http.StatusFound, h.Svc.AuthCodeURL())
}

// Callback completes the auth code exchange, runs the access policy, finds
// or provisions the local user, and starts a session. On success the
// browser is 302'd to the SPA root with the session cookie set; on failure
// it's 302'd to /login with the error message in a query parameter so the
// LoginPage can render it.
func (h *OIDCHandler) Callback(c *gin.Context) {
	if h.Svc == nil {
		h.redirectWithError(c, "oidc not enabled")
		return
	}
	q := c.Request.URL.Query()
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg = e + ": " + d
		}
		h.redirectWithError(c, msg)
		return
	}

	id, err := h.Svc.HandleCallback(c.Request.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		h.redirectWithError(c, err.Error())
		return
	}
	if id.Email == "" {
		h.redirectWithError(c, "OIDC identity has no email; cannot bind to local user")
		return
	}

	user, err := h.Store.GetUserByEmail(c.Request.Context(), id.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.redirectWithError(c, "lookup user: "+err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		if !h.Cfg.OIDC.AutoProvision {
			h.redirectWithError(c, "no local user matches "+id.Email+" (auto_provision is disabled; ask an admin to provision)")
			return
		}
		user, err = h.userOps().ProvisionUser(c.Request.Context(), id)
		if err != nil {
			h.redirectWithError(c, "provision user: "+oidcErrorText(err))
			return
		}
	}

	if user.Disabled {
		h.redirectWithError(c, "account disabled")
		return
	}

	ops := h.authOps()
	token, _, err := ops.OpenSession(c.Request.Context(), user.ID)
	if err != nil {
		h.redirectWithError(c, oidcErrorText(err))
		return
	}
	setSessionCookie(c, h.Cfg, token, int(ops.SessionTTL().Seconds()))
	c.Redirect(http.StatusFound, oidcLandingPath(user.IsAdmin))
}

func oidcLandingPath(isAdmin bool) string {
	if isAdmin {
		return "/settings"
	}
	return "/"
}

// oidcErrorText renders a service-layer failure as the plain text that goes
// into the /login?oidc_error= query parameter. This is the OIDC-specific
// counterpart to respondServiceError: the callback is reached by a top-level
// browser navigation, so its errors travel as a redirect, never as a JSON 4xx —
// only the message survives, the ServiceError's status is dropped on purpose.
func oidcErrorText(err error) string {
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Message()
	}
	return err.Error()
}

// redirectWithError sends the browser back to /login with the failure
// rendered in a query parameter. We bounce through /login (rather than
// returning a JSON 4xx) because the callback is reached via a top-level
// browser navigation, not an XHR — there's no JS to render an error.
func (h *OIDCHandler) redirectWithError(c *gin.Context, msg string) {
	c.Redirect(http.StatusFound, "/login?oidc_error="+url.QueryEscape(msg))
}
