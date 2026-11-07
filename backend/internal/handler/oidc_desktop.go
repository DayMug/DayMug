package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// Browser authorization and desktop claims use independent secrets. The OAuth
// state goes to the browser; only the initiating webview can claim the login.
type desktopLogin struct {
	claim    [32]byte
	expires  time.Time
	userID   string
	approval string
	approved bool
	err      string
}

func (h *OIDCHandler) pruneDesktop() {
	for state, attempt := range h.desktop {
		if time.Now().After(attempt.expires) {
			delete(h.desktop, state)
		}
	}
}

func (h *OIDCHandler) DesktopStart(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.Svc == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	h.desktopMu.Lock()
	defer h.desktopMu.Unlock()
	h.pruneDesktop()
	if len(h.desktop) >= 1024 {
		c.Status(http.StatusTooManyRequests)
		return
	}
	authURL := h.Svc.AuthCodeURL()
	parsed, _ := url.Parse(authURL)
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	claim := hex.EncodeToString(secret)
	if h.desktop == nil {
		h.desktop = make(map[string]*desktopLogin)
	}
	h.desktop[parsed.Query().Get("state")] = &desktopLogin{claim: sha256.Sum256([]byte(claim)), expires: time.Now().Add(5 * time.Minute)}
	c.JSON(http.StatusOK, gin.H{"auth_url": authURL, "claim": claim})
}

func (h *OIDCHandler) finishDesktopLogin(c *gin.Context, userID string) bool {
	h.desktopMu.Lock()
	defer h.desktopMu.Unlock()
	h.pruneDesktop()
	attempt := h.desktop[c.Query("state")]
	if attempt == nil {
		return false
	}
	attempt.userID = userID
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		c.Status(http.StatusInternalServerError)
		return true
	}
	attempt.approval = hex.EncodeToString(secret)
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
	c.Header("Referrer-Policy", "no-referrer")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!doctype html><meta charset="utf-8"><title>DayMug</title><h1>Sign in to DayMug desktop / 登录 DayMug 客户端</h1><p>Continue only if you started this login in your own DayMug client. / 仅当你在自己的客户端发起本次登录时继续。</p><form method="post" action="/api/auth/oidc/desktop/approve"><input type="hidden" name="state" value="`+html.EscapeString(c.Query("state"))+`"><input type="hidden" name="approval" value="`+attempt.approval+`"><button>Continue / 继续</button></form>`))
	return true
}

func (h *OIDCHandler) DesktopApprove(c *gin.Context) {
	h.desktopMu.Lock()
	defer h.desktopMu.Unlock()
	h.pruneDesktop()
	attempt := h.desktop[c.PostForm("state")]
	if attempt == nil || attempt.userID == "" || attempt.approved || attempt.approval == "" || c.PostForm("approval") != attempt.approval {
		c.Status(http.StatusBadRequest)
		return
	}
	attempt.approved = true
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusOK, "Signed in. Return to the DayMug client. / 登录成功，请返回 DayMug 客户端。")
}

func (h *OIDCHandler) desktopError(c *gin.Context, message string) bool {
	h.desktopMu.Lock()
	defer h.desktopMu.Unlock()
	h.pruneDesktop()
	attempt := h.desktop[c.Query("state")]
	if attempt == nil {
		return false
	}
	attempt.err = message
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusBadRequest, "Sign-in failed. Return to the DayMug client. / 登录失败，请返回客户端。")
	return true
}

func (h *OIDCHandler) DesktopClaim(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var body struct {
		Claim string `json:"claim"`
	}
	if c.ShouldBindJSON(&body) != nil || len(body.Claim) != 64 {
		c.Status(http.StatusBadRequest)
		return
	}
	digest := sha256.Sum256([]byte(body.Claim))
	h.desktopMu.Lock()
	h.pruneDesktop()
	var found *desktopLogin
	for state, attempt := range h.desktop {
		if attempt.claim == digest {
			snapshot := *attempt
			found = &snapshot
			if attempt.approved || attempt.err != "" {
				delete(h.desktop, state)
			}
			break
		}
	}
	h.desktopMu.Unlock()
	if found == nil {
		c.Status(http.StatusGone)
		return
	}
	if found.err != "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": found.err})
		return
	}
	if !found.approved {
		c.Status(http.StatusAccepted)
		return
	}
	ops := h.authOps()
	token, _, err := ops.OpenSession(c.Request.Context(), found.userID)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	setSessionCookie(c, h.Cfg, token, int(ops.SessionTTL().Seconds()))
	c.Status(http.StatusNoContent)
}
