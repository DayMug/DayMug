package handler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// auth.cookie_secure 曾经是死配置（结构体里有、文档里让 HTTPS 部署打开、
// 但没有任何读取点），session cookie 永远不带 Secure。这些用例锁定它真的
// 被读到了，以及「按请求协议自动推断」的兜底不会误伤明文 HTTP 部署。
func TestSessionCookieSecureSources(t *testing.T) {
	tests := []struct {
		name         string
		cookieSecure bool
		tls          bool
		forwarded    string
		want         bool
	}{
		{name: "plain http keeps cookie usable", want: false},
		{name: "config flag forces secure", cookieSecure: true, want: true},
		{name: "direct tls infers secure", tls: true, want: true},
		{name: "reverse proxy header infers secure", forwarded: "https", want: true},
		{name: "header casing is irrelevant", forwarded: "HTTPS", want: true},
		{name: "only the first proxy hop counts", forwarded: "https, http", want: true},
		{name: "http forwarded stays insecure", forwarded: "http", want: false},
		{name: "nil config falls back to the request", tls: true, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
			if tc.tls {
				c.Request.TLS = &tls.ConnectionState{}
			}
			if tc.forwarded != "" {
				c.Request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			var cfg *config.Config
			if tc.name != "nil config falls back to the request" {
				cfg = &config.Config{Auth: config.AuthConfig{CookieSecure: tc.cookieSecure}}
			}
			if got := sessionCookieSecure(c, cfg); got != tc.want {
				t.Fatalf("sessionCookieSecure = %v, want %v", got, tc.want)
			}
		})
	}
}

func loginRouter(t *testing.T, cookieSecure bool) *gin.Engine {
	t.Helper()
	ms := storetest.New()
	hash, err := service.HashPassword("secret123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: hash})
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{
		PasswordLoginEnabled: true,
		CookieSecure:         cookieSecure,
	}})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/auth/login", h.Login)
	r.POST("/api/auth/logout", h.Logout)
	return r
}

func doLoginRequest(t *testing.T, r *gin.Engine, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": "alice", "password": "secret123"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func sessionSetCookie(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, raw := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(raw, middleware.SessionCookieName+"=") {
			return raw
		}
	}
	t.Fatalf("no %s cookie in %v", middleware.SessionCookieName, w.Result().Header)
	return ""
}

func TestLoginCookieSecureFromConfig(t *testing.T) {
	w := doLoginRequest(t, loginRouter(t, true), "/api/auth/login", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if got := sessionSetCookie(t, w); !strings.Contains(got, "Secure") {
		t.Fatalf("cookie_secure: true must set Secure, got %q", got)
	}
}

func TestLoginCookieSecureFromForwardedProto(t *testing.T) {
	w := doLoginRequest(t, loginRouter(t, false), "/api/auth/login", map[string]string{"X-Forwarded-Proto": "https"})
	if got := sessionSetCookie(t, w); !strings.Contains(got, "Secure") {
		t.Fatalf("HTTPS request must set Secure even with cookie_secure: false, got %q", got)
	}
}

// 明文 HTTP 上带 Secure 的 cookie 会被浏览器直接丢弃，登录就废了 —— 这是
// 默认值必须保持 false 的原因，也是这条用例守的东西。
func TestLoginCookieNotSecureOverPlainHTTP(t *testing.T) {
	w := doLoginRequest(t, loginRouter(t, false), "/api/auth/login", nil)
	got := sessionSetCookie(t, w)
	if strings.Contains(got, "Secure") {
		t.Fatalf("plain HTTP must not set Secure, got %q", got)
	}
	if !strings.Contains(got, "HttpOnly") || !strings.Contains(got, "SameSite=Lax") {
		t.Fatalf("expected HttpOnly + SameSite=Lax baseline, got %q", got)
	}
}

// 清除 cookie 必须与写入时属性一致，否则浏览器可能不认为是同一个 cookie。
func TestLogoutCookieMatchesLoginAttributes(t *testing.T) {
	w := doLoginRequest(t, loginRouter(t, true), "/api/auth/logout", nil)
	got := sessionSetCookie(t, w)
	if !strings.Contains(got, "Secure") || !strings.Contains(got, "HttpOnly") || !strings.Contains(got, "SameSite=Lax") {
		t.Fatalf("logout cookie lost an attribute: %q", got)
	}
}
