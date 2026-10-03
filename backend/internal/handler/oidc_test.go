package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestOIDCLoginDeniesWhenServiceMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewOIDCHandler(nil, &config.Config{}, nil)
	r.GET("/api/auth/oidc/login", h.Login)

	req := httptest.NewRequest("GET", "/api/auth/oidc/login", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not enabled") {
		t.Errorf("body should mention not enabled: %q", w.Body.String())
	}
}

func TestOIDCCallbackRedirectsWithErrorOnIdpError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewOIDCHandler(nil, &config.Config{}, nil)
	r.GET("/api/auth/oidc/callback", h.Callback)

	req := httptest.NewRequest("GET", "/api/auth/oidc/callback", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?oidc_error=") {
		t.Errorf("expected /login?oidc_error= redirect, got %q", loc)
	}
}

func TestOIDCLandingPathSeparatesAdminsAndRegularUsers(t *testing.T) {
	tests := []struct {
		name    string
		isAdmin bool
		want    string
	}{
		{name: "admin", isAdmin: true, want: "/settings"},
		{name: "regular user", isAdmin: false, want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oidcLandingPath(tt.isAdmin); got != tt.want {
				t.Fatalf("oidcLandingPath(%v) = %q, want %q", tt.isAdmin, got, tt.want)
			}
		})
	}
}

func TestAuthOptionsReflectsConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	cfg := &config.Config{
		Auth: config.AuthConfig{PasswordLoginEnabled: false},
		OIDC: config.OIDCConfig{Enabled: true, ButtonLabel: "Sign in with Casdoor"},
	}
	h := NewAuthHandler(nil, cfg)
	r.GET("/api/auth/options", h.Options)

	req := httptest.NewRequest("GET", "/api/auth/options", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["password_login_enabled"] != false {
		t.Errorf("password_login_enabled: %v", resp["password_login_enabled"])
	}
	oidc, _ := resp["oidc"].(map[string]any)
	if oidc["enabled"] != true {
		t.Errorf("oidc.enabled: %v", oidc["enabled"])
	}
	if oidc["button_label"] != "Sign in with Casdoor" {
		t.Errorf("oidc.button_label: %v", oidc["button_label"])
	}
}

func TestAuthOptionsNilConfigDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewAuthHandler(nil, nil)
	r.GET("/api/auth/options", h.Options)

	req := httptest.NewRequest("GET", "/api/auth/options", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["password_login_enabled"] != true {
		t.Errorf("nil cfg should default password_login_enabled=true: %v", resp["password_login_enabled"])
	}
}

func TestPasswordLoginDisabledRefuses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	cfg := &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: false}}
	h := NewAuthHandler(nil, cfg)
	r.POST("/api/auth/login", h.Login)

	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"x","password":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d body=%s", w.Code, w.Body.String())
	}
}
