package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// SessionTTL is how long a session stays valid when auth.session_ttl is not
// configured. The real lifetime is resolved per request by
// service.AuthOps.SessionTTL, which prefers the configured value.
const SessionTTL = service.DefaultSessionTTL

type AuthHandler struct {
	Store store.Store
	Cfg   *config.Config
}

func NewAuthHandler(s store.Store, cfg *config.Config) *AuthHandler {
	return &AuthHandler{Store: s, Cfg: cfg}
}

// authOps assembles a service.AuthOps from the handler's current fields. Built
// lazily per call — production wiring (routes.go) and tests set handler fields
// after construction, so a snapshot taken in NewAuthHandler could miss them.
func (h *AuthHandler) authOps() *service.AuthOps {
	return &service.AuthOps{Store: h.Store, Cfg: h.Cfg}
}

// Options is the only public auth endpoint that returns server policy: which
// login methods are enabled, and the SSO button label when OIDC is on. The
// SPA hits this on first load to decide which login UI to render.
func (h *AuthHandler) Options(c *gin.Context) {
	resp := gin.H{
		"password_login_enabled": true,
		"oidc": gin.H{
			"enabled":      false,
			"button_label": "",
		},
	}
	if h.Cfg != nil {
		resp["password_login_enabled"] = h.Cfg.Auth.PasswordLoginEnabled
		resp["oidc"] = gin.H{
			"enabled":      h.Cfg.OIDC.Enabled,
			"button_label": h.Cfg.OIDC.ButtonLabel,
		}
	}
	c.JSON(http.StatusOK, resp)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login validates username + password, opens a session, sets the session
// cookie, and returns a small user payload (no password hash).
func (h *AuthHandler) Login(c *gin.Context) {
	// The feature gate stays ahead of bindJSON: a disabled endpoint must answer
	// 403 regardless of body shape, so a malformed body can't downgrade it to 400.
	if h.Cfg != nil && !h.Cfg.Auth.PasswordLoginEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "password login disabled"})
		return
	}
	var body loginRequest
	if !bindJSON(c, &body) {
		return
	}
	ops := h.authOps()
	user, token, _, err := ops.Login(c.Request.Context(), body.Username, body.Password)
	if err != nil {
		respondServiceError(c, err)
		return
	}

	setSessionCookie(c, h.Cfg, token, int(ops.SessionTTL().Seconds()))
	c.JSON(http.StatusOK, gin.H{
		"id":       user.ID,
		"username": user.Username,
		"name":     user.Name,
		"is_admin": user.IsAdmin,
	})
}

// Logout invalidates the current session (if any) and clears the cookie. It
// always returns 204, regardless of whether a session was actually present.
func (h *AuthHandler) Logout(c *gin.Context) {
	if token, err := c.Cookie(middleware.SessionCookieName); err == nil && token != "" {
		_ = h.Store.DeleteSession(c.Request.Context(), token)
	}
	clearSessionCookie(c, h.Cfg)
	c.Status(http.StatusNoContent)
}

// Me returns the currently logged-in user. Wired behind RequireAuth so reaching
// the handler implies a valid session. Includes the notification channel
// fields so the notifications settings page and the conversation list can
// hydrate (and gate the bell icon) without a separate request.
func (h *AuthHandler) Me(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	user, err := h.Store.GetUser(c.Request.Context(), uid)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":                   user.ID,
		"username":             user.Username,
		"name":                 user.Name,
		"is_admin":             user.IsAdmin,
		"bark_url":             user.BarkURL,
		"pushdeer_key":         user.PushDeerKey,
		"notification_channel": user.NotificationChannel,
		// Surfaced so the agent-creation form can scope its DirPicker
		// to the caller's home — matching the backend home-jail in
		// requireWorkDirWithinCaller.
		"work_dir": user.WorkDir,
	})
}

// Environment returns the signed-in human user's process environment as
// stored. It is not validated here: a value the runtime cannot parse is shown
// as is, so the user can see and fix it in the editor.
func (h *AuthHandler) Environment(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	user, err := h.Store.GetUser(c.Request.Context(), uid)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"env": strings.TrimSpace(user.Env)})
}

type environmentRequest struct {
	Env string `json:"env"`
}

// UpdateEnvironment stores the signed-in user's process environment and echoes
// back the normalized text. The target id always comes from the authenticated
// session, so a user cannot edit another user's environment.
func (h *AuthHandler) UpdateEnvironment(c *gin.Context) {
	var body environmentRequest
	if !bindJSON(c, &body) {
		return
	}
	env, err := h.authOps().UpdateEnvironment(c.Request.Context(), middleware.CurrentUserID(c), body.Env)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"env": env})
}

type notificationsRequest struct {
	BarkURL             string `json:"bark_url"`
	PushDeerKey         string `json:"pushdeer_key"`
	NotificationChannel string `json:"notification_channel"`
}

// UpdateNotifications writes the signed-in user's notification settings and
// echoes them back. See service.AuthOps.UpdateNotifications for the contract —
// notably which notification_channel values are accepted.
func (h *AuthHandler) UpdateNotifications(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	var body notificationsRequest
	if !bindJSON(c, &body) {
		return
	}
	// Trimmed here rather than in the service because the response echoes the
	// normalized values back and AuthOps.UpdateNotifications returns only an
	// error — the boundary has to hold what it will render.
	body.BarkURL = strings.TrimSpace(body.BarkURL)
	body.PushDeerKey = strings.TrimSpace(body.PushDeerKey)
	if err := h.authOps().UpdateNotifications(c.Request.Context(), uid, body.BarkURL, body.PushDeerKey, body.NotificationChannel); err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"bark_url":             body.BarkURL,
		"pushdeer_key":         body.PushDeerKey,
		"notification_channel": body.NotificationChannel,
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword lets the signed-in user rotate their own password. See
// service.AuthOps.ChangePassword for the re-auth requirement and the cases it
// refuses.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	// Gate before bindJSON, same as Login: shape of the body must not affect
	// the status code of a disabled endpoint.
	if h.Cfg != nil && !h.Cfg.Auth.PasswordLoginEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "password login disabled"})
		return
	}
	var body changePasswordRequest
	if !bindJSON(c, &body) {
		return
	}
	if err := h.authOps().ChangePassword(c.Request.Context(), middleware.CurrentUserID(c), body.CurrentPassword, body.NewPassword); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// sessionCookieSecure decides whether the session cookie carries the Secure
// flag. cfg 只能表达「一定要 Secure」，不能表达「一定不要」——所以两个来源取或：
//
//   - auth.cookie_secure: true 是运维的强制开关（例如反代没有透传协议头，
//     但确实全站 HTTPS）；
//   - 否则看这次请求本身是不是 HTTPS：直连 TLS 看 r.TLS，反代后看
//     X-Forwarded-Proto。
//
// 之所以不把默认值直接改成 true：Secure cookie 在明文 HTTP 上会被浏览器
// 静默丢弃，登录会彻底失败。局域网 / 本地开发的 http 部署占多数，硬改默认
// 值等于给它们制造一次宕机级回归。反过来，按请求协议推断在 HTTPS 部署上
// 天然为真，所以「照文档配了 HTTPS 却忘了开开关」这个真实故障也被兜住了。
//
// 伪造 X-Forwarded-Proto: https 只会让攻击者自己那条明文请求的 cookie 被
// 浏览器丢弃（自伤），不会降低任何人的安全等级，所以这里不要求可信代理表。
func sessionCookieSecure(c *gin.Context, cfg *config.Config) bool {
	if cfg != nil && cfg.Auth.CookieSecure {
		return true
	}
	if c.Request == nil {
		return false
	}
	if c.Request.TLS != nil {
		return true
	}
	proto := c.Request.Header.Get("X-Forwarded-Proto")
	// 反代可能透传成 "https, http" 这样的链路列表，只认第一跳。
	if i := strings.Index(proto, ","); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

func setSessionCookie(c *gin.Context, cfg *config.Config, token string, maxAgeSec int) {
	// SameSite=Lax + HttpOnly 是这种「前后端同源」应用的正确基线：Lax 仍允许
	// OIDC 回调那种顶层导航带上 cookie，HttpOnly 挡住 XSS 读取 token。
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, token, maxAgeSec, "/", "", sessionCookieSecure(c, cfg), true)
}

// clearSessionCookie 必须和 setSessionCookie 用同一套属性：属性不一致时，
// 浏览器可能不认为这是同一个 cookie，退出登录就清不掉。
func clearSessionCookie(c *gin.Context, cfg *config.Config) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(middleware.SessionCookieName, "", -1, "/", "", sessionCookieSecure(c, cfg), true)
}
