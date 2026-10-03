package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupChangePasswordRouter(t *testing.T, h *AuthHandler, callerID string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api")
	g.Use(adminAuthCtx(callerID, false))
	g.POST("/auth/password", h.ChangePassword)
	return r
}

func TestChangePasswordSuccess(t *testing.T) {
	ms := storetest.New()
	hash, err := service.HashPassword("oldsecret")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ms.Users = append(ms.Users, store.User{
		ID:           "u1",
		Username:     "alice",
		PasswordHash: hash,
	})
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}})
	r := setupChangePasswordRouter(t, h, "u1")

	w := doJSON(r, "POST", "/api/auth/password", map[string]any{
		"current_password": "oldsecret",
		"new_password":     "newsecret",
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	updated := ms.Users[0]
	if !service.VerifyPassword(updated.PasswordHash, "newsecret") {
		t.Fatalf("password not updated to new value")
	}
	if service.VerifyPassword(updated.PasswordHash, "oldsecret") {
		t.Fatalf("old password still works")
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	ms := storetest.New()
	hash, _ := service.HashPassword("oldsecret")
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: hash})
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}})
	r := setupChangePasswordRouter(t, h, "u1")

	w := doJSON(r, "POST", "/api/auth/password", map[string]any{
		"current_password": "wrong",
		"new_password":     "newsecret",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body %s", w.Code, w.Body.String())
	}
	if !service.VerifyPassword(ms.Users[0].PasswordHash, "oldsecret") {
		t.Fatalf("password should be unchanged after failed attempt")
	}
}

func TestChangePasswordRejectsEmpty(t *testing.T) {
	ms := storetest.New()
	hash, _ := service.HashPassword("oldsecret")
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: hash})
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}})
	r := setupChangePasswordRouter(t, h, "u1")

	cases := []map[string]any{
		{"current_password": "", "new_password": "newsecret"},
		{"current_password": "oldsecret", "new_password": ""},
		{"current_password": "oldsecret", "new_password": "oldsecret"},
	}
	for _, body := range cases {
		w := doJSON(r, "POST", "/api/auth/password", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%v expected 400, got %d body=%s", body, w.Code, w.Body.String())
		}
	}
}

func TestChangePasswordDisabledByConfig(t *testing.T) {
	ms := storetest.New()
	hash, _ := service.HashPassword("oldsecret")
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: hash})
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: false}})
	r := setupChangePasswordRouter(t, h, "u1")

	w := doJSON(r, "POST", "/api/auth/password", map[string]any{
		"current_password": "oldsecret",
		"new_password":     "newsecret",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body %s", w.Code, w.Body.String())
	}
}

// The feature gate must be evaluated before the body is parsed, so a client
// sending garbage to a disabled endpoint still learns it is disabled (403)
// rather than that its JSON was bad (400). Sinking the gate into the service
// layer once flipped this, hence the explicit lock.
func TestChangePasswordDisabledByConfigOutranksMalformedBody(t *testing.T) {
	ms := storetest.New()
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: false}})
	r := setupChangePasswordRouter(t, h, "u1")

	req := httptest.NewRequest("POST", "/api/auth/password", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a disabled endpoint, got %d body %s", w.Code, w.Body.String())
	}
}

func TestChangePasswordSSOOnlyUserRejected(t *testing.T) {
	ms := storetest.New()
	// SSO-only user has no password hash.
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: ""})
	h := NewAuthHandler(ms, &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}})
	r := setupChangePasswordRouter(t, h, "u1")

	w := doJSON(r, "POST", "/api/auth/password", map[string]any{
		"current_password": "anything",
		"new_password":     "newsecret",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body %s", w.Code, w.Body.String())
	}
}

func setupNotificationsRouter(t *testing.T, h *AuthHandler, callerID string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api")
	g.Use(adminAuthCtx(callerID, false))
	g.GET("/auth/me", h.Me)
	g.GET("/auth/environment", h.Environment)
	g.PUT("/auth/environment", h.UpdateEnvironment)
	g.PUT("/auth/notifications", h.UpdateNotifications)
	return r
}

func TestEnvironment_SelfServiceRoundTrip(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "PUT", "/api/auth/environment", map[string]any{
		"env": "GH_TOKEN=secret\nEMPTY=\nURL=https://example.com?a=b",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update status %d body %s", w.Code, w.Body.String())
	}
	if got := ms.Users[0].Env; got != "GH_TOKEN=secret\nEMPTY=\nURL=https://example.com?a=b" {
		t.Fatalf("stored env = %q", got)
	}

	w = doJSON(r, "GET", "/api/auth/environment", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["env"] != ms.Users[0].Env {
		t.Fatalf("response env = %q", body["env"])
	}
}

func TestEnvironment_RejectsInvalidLine(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "PUT", "/api/auth/environment", map[string]any{"env": "MISSING_EQUALS"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", w.Code, w.Body.String())
	}
	if ms.Users[0].Env != "" {
		t.Fatalf("invalid environment was stored: %q", ms.Users[0].Env)
	}
}

// A value the runtime cannot parse is returned verbatim, so the editor shows
// what is stored and the user can fix it, instead of the page failing to load.
func TestEnvironment_ReturnsUnparsableValueVerbatim(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{
		ID: "u1", Username: "alice", Env: "  {\"KEY\":\"line1\\nline2\"}\n",
	})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "GET", "/api/auth/environment", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := `{"KEY":"line1\nline2"}`; body["env"] != want {
		t.Fatalf("env = %q, want %q", body["env"], want)
	}
}

func TestMe_IncludesBarkURL(t *testing.T) {
	// /auth/me hydrates the notifications settings page on first paint, so
	// it has to surface bark_url alongside the existing identity fields.
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{
		ID:       "u1",
		Username: "alice",
		Name:     "Alice",
		WorkDir:  "/home/alice",
		BarkURL:  "https://bark.example/key",
	})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "GET", "/api/auth/me", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["bark_url"] != "https://bark.example/key" {
		t.Errorf("expected bark_url in /me payload, got %v", body["bark_url"])
	}
	// work_dir is what the agent-creation form uses to root its DirPicker —
	// must round-trip through /me so the picker's jail matches the
	// backend home-jail.
	if body["work_dir"] != "/home/alice" {
		t.Errorf("expected work_dir in /me payload, got %v", body["work_dir"])
	}
}

func TestUpdateNotifications_PersistsBarkURL(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "PUT", "/api/auth/notifications", map[string]any{
		"bark_url": "https://bark.example/abc",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if got := ms.Users[0].BarkURL; got != "https://bark.example/abc" {
		t.Errorf("expected stored bark_url to update, got %q", got)
	}
}

func TestUpdateNotifications_AllowsClearing(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", BarkURL: "https://bark.example/old"})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "PUT", "/api/auth/notifications", map[string]any{"bark_url": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if got := ms.Users[0].BarkURL; got != "" {
		t.Errorf("expected empty bark_url after clearing, got %q", got)
	}
}

func TestMe_IncludesPushDeerAndChannel(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{
		ID:                  "u1",
		Username:            "alice",
		Name:                "Alice",
		WorkDir:             "/home/alice",
		BarkURL:             "https://bark.example/key",
		PushDeerKey:         "pdkey",
		NotificationChannel: "pushdeer",
	})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "GET", "/api/auth/me", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["pushdeer_key"] != "pdkey" {
		t.Errorf("expected pushdeer_key in /me payload, got %v", body["pushdeer_key"])
	}
	if body["notification_channel"] != "pushdeer" {
		t.Errorf("expected notification_channel in /me payload, got %v", body["notification_channel"])
	}
}

func TestUpdateNotifications_PersistsAllChannelFields(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "PUT", "/api/auth/notifications", map[string]any{
		"bark_url":             "  https://bark.example/abc  ",
		"pushdeer_key":         "  pdkey  ",
		"notification_channel": "pushdeer",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if got := ms.Users[0].BarkURL; got != "https://bark.example/abc" {
		t.Errorf("expected trimmed bark_url, got %q", got)
	}
	if got := ms.Users[0].PushDeerKey; got != "pdkey" {
		t.Errorf("expected trimmed pushdeer_key, got %q", got)
	}
	if got := ms.Users[0].NotificationChannel; got != "pushdeer" {
		t.Errorf("expected notification_channel=pushdeer, got %q", got)
	}
}

func TestUpdateNotifications_RejectsUnknownChannel(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	h := NewAuthHandler(ms, nil)
	r := setupNotificationsRouter(t, h, "u1")

	w := doJSON(r, "PUT", "/api/auth/notifications", map[string]any{
		"notification_channel": "telegram",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown channel, got %d body %s", w.Code, w.Body.String())
	}
}
