package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupBootstrapRouter(t *testing.T, h *BootstrapHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api")
	g.GET("/bootstrap/status", h.Status)
	g.POST("/bootstrap/admin", h.CreateAdmin)
	return r
}

// newBootstrapTestConfig is newTestConfig with password login on, as config.Load
// defaults it; the zero value leaves it off, which is a different setup mode.
func newBootstrapTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := newTestConfig(t)
	cfg.Auth.PasswordLoginEnabled = true
	return cfg
}

func TestBootstrap_StatusEmpty(t *testing.T) {
	ms := storetest.New()
	h := NewBootstrapHandler(ms, newBootstrapTestConfig(t))
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "GET", "/api/bootstrap/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["has_users"] != false {
		t.Errorf("has_users = %v, want false", body["has_users"])
	}
}

func TestBootstrap_StatusIgnoresAgents(t *testing.T) {
	// Agents are User rows with no username; they don't count for the
	// first-run gate because they can't log in. Until a real human exists
	// the install is still considered pre-bootstrap.
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "agent-1", Name: "Bot"})
	h := NewBootstrapHandler(ms, newBootstrapTestConfig(t))
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "GET", "/api/bootstrap/status", nil)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["has_users"] != false {
		t.Errorf("agent-only install should report has_users=false, got %v", body["has_users"])
	}
}

func TestBootstrap_StatusReportsHumans(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	h := NewBootstrapHandler(ms, newBootstrapTestConfig(t))
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "GET", "/api/bootstrap/status", nil)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["has_users"] != true {
		t.Errorf("has_users = %v, want true", body["has_users"])
	}
}

func TestBootstrap_CreateAdminSuccess(t *testing.T) {
	ms := storetest.New()
	cfg := newBootstrapTestConfig(t)
	h := NewBootstrapHandler(ms, cfg)
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "POST", "/api/bootstrap/admin", map[string]any{
		"username": "alice",
		"email":    "alice@example.com",
		"password": "secret123",
		"name":     "Alice",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(ms.Users) != 2 {
		t.Fatalf("expected a human and default Agent, got %d rows", len(ms.Users))
	}
	u := ms.Users[0]
	if !u.IsAdmin {
		t.Errorf("bootstrap user must be admin")
	}
	if u.Username != "alice" || u.Email != "alice@example.com" {
		t.Errorf("user fields off: %+v", u)
	}
	if !service.VerifyPassword(u.PasswordHash, "secret123") {
		t.Errorf("password hash does not verify")
	}
	defaultAgent := ms.Users[1]
	if defaultAgent.OwnerID != u.ID || defaultAgent.Name != u.Username || defaultAgent.WorkDir != u.WorkDir {
		t.Errorf("default Agent = %+v, want owner %q named %q", defaultAgent, u.ID, u.Username)
	}
	// A session is created so the new admin lands logged in.
	if len(ms.Sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(ms.Sessions))
	}
	// And the response includes the session cookie.
	if got := w.Header().Get("Set-Cookie"); got == "" {
		t.Errorf("expected Set-Cookie on bootstrap response")
	}
}

func TestBootstrap_CreateAdminDerivesUsernameFromEmail(t *testing.T) {
	// Username field omitted — the handler should derive it from the
	// email local-part to honour the username==email-prefix invariant.
	ms := storetest.New()
	cfg := newBootstrapTestConfig(t)
	h := NewBootstrapHandler(ms, cfg)
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "POST", "/api/bootstrap/admin", map[string]any{
		"email":    "bob@example.com",
		"password": "secret123",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if ms.Users[0].Username != "bob" {
		t.Errorf("expected username=bob, got %q", ms.Users[0].Username)
	}
	if ms.Users[0].Name != "bob" {
		t.Errorf("expected name to default to username, got %q", ms.Users[0].Name)
	}
}

func TestBootstrap_CreateAdminRejectsMismatch(t *testing.T) {
	ms := storetest.New()
	cfg := newBootstrapTestConfig(t)
	h := NewBootstrapHandler(ms, cfg)
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "POST", "/api/bootstrap/admin", map[string]any{
		"username": "carol",
		"email":    "carol-prime@example.com",
		"password": "secret123",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(ms.Users) != 0 {
		t.Errorf("user must not be created on validation failure")
	}
}

func TestBootstrap_CreateAdminRejectsWhenUsersExist(t *testing.T) {
	// Once any human user is present the endpoint closes — the install is
	// past first-run and further admins go through the authenticated admin
	// CRUD endpoints.
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	cfg := newBootstrapTestConfig(t)
	h := NewBootstrapHandler(ms, cfg)
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "POST", "/api/bootstrap/admin", map[string]any{
		"username": "bob",
		"email":    "bob@example.com",
		"password": "secret123",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body %s, want 409", w.Code, w.Body.String())
	}
	if len(ms.Users) != 1 {
		t.Errorf("expected user count unchanged, got %d", len(ms.Users))
	}
}

func TestBootstrap_CreateAdminMissingPassword(t *testing.T) {
	ms := storetest.New()
	cfg := newBootstrapTestConfig(t)
	h := NewBootstrapHandler(ms, cfg)
	r := setupBootstrapRouter(t, h)

	w := doJSON(r, "POST", "/api/bootstrap/admin", map[string]any{
		"email": "alice@example.com",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
}

func withSSOFirstAdmin(cfg *config.Config) *config.Config {
	cfg.OIDC = config.OIDCConfig{Enabled: true, AutoProvision: true}
	cfg.Admin.BootstrapUsernames = []string{"alice"}
	return cfg
}

func TestBootstrap_StatusSetupRequired(t *testing.T) {
	cases := []struct {
		name  string
		sso   bool
		users []store.User
		want  bool
	}{
		{name: "fresh install without sso", want: true},
		{name: "fresh install with sso first admin", sso: true, want: false},
		{name: "populated install", users: []store.User{{ID: "u1", Username: "alice"}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			ms.Users = append(ms.Users, tc.users...)
			cfg := newBootstrapTestConfig(t)
			if tc.sso {
				withSSOFirstAdmin(cfg)
			}
			r := setupBootstrapRouter(t, NewBootstrapHandler(ms, cfg))

			w := doJSON(r, "GET", "/api/bootstrap/status", nil)
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body["setup_required"] != tc.want {
				t.Errorf("setup_required = %v, want %v", body["setup_required"], tc.want)
			}
		})
	}
}

// The public form must stay closed when SSO provisions the first admin, or
// whoever reaches the site first could claim admin before the owner signs in.
func TestBootstrap_CreateAdminRejectedWhenSSOProvisionsAdmin(t *testing.T) {
	ms := storetest.New()
	r := setupBootstrapRouter(t, NewBootstrapHandler(ms, withSSOFirstAdmin(newBootstrapTestConfig(t))))

	w := doJSON(r, "POST", "/api/bootstrap/admin", map[string]any{
		"email":    "mallory@example.com",
		"password": "secret123",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", w.Code, w.Body.String())
	}
	if len(ms.Users) != 0 {
		t.Errorf("no user should be created, got %d rows", len(ms.Users))
	}
}

// setup_mode tells the form which variant to render; with no sign-in method at
// all it must still route to /setup (to show the fix), but refuse to create.
func TestBootstrap_SetupModes(t *testing.T) {
	cases := []struct {
		name         string
		cfg          func(*config.Config)
		wantMode     string
		wantRequired bool
		body         map[string]any
		wantCreate   int
	}{
		{
			name: "password", cfg: func(*config.Config) {}, wantMode: "password", wantRequired: true,
			body: map[string]any{"email": "alice@example.com", "password": "short"}, wantCreate: http.StatusBadRequest,
		},
		{
			name: "sso email", cfg: func(c *config.Config) {
				c.Auth.PasswordLoginEnabled = false
				c.OIDC = config.OIDCConfig{Enabled: true}
			}, wantMode: "sso_email", wantRequired: true,
			body: map[string]any{"email": "alice@example.com"}, wantCreate: http.StatusCreated,
		},
		{
			name: "unavailable", cfg: func(c *config.Config) { c.Auth.PasswordLoginEnabled = false },
			wantMode: "unavailable", wantRequired: true,
			body: map[string]any{"email": "alice@example.com", "password": "secret123"}, wantCreate: http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			cfg := newBootstrapTestConfig(t)
			tc.cfg(cfg)
			r := setupBootstrapRouter(t, NewBootstrapHandler(ms, cfg))

			w := doJSON(r, "GET", "/api/bootstrap/status", nil)
			var body map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body["setup_mode"] != tc.wantMode || body["setup_required"] != tc.wantRequired {
				t.Errorf("setup_mode=%v setup_required=%v, want %s/%v", body["setup_mode"], body["setup_required"], tc.wantMode, tc.wantRequired)
			}
			if body["min_password_length"] != float64(service.MinPasswordLength) {
				t.Errorf("min_password_length = %v", body["min_password_length"])
			}

			w = doJSON(r, "POST", "/api/bootstrap/admin", tc.body)
			if w.Code != tc.wantCreate {
				t.Fatalf("create status = %d, want %d; body %s", w.Code, tc.wantCreate, w.Body.String())
			}
		})
	}
}
