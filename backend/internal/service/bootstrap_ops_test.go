package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func newBootstrapTestOps(t *testing.T, users ...store.User) (*BootstrapOps, *storetest.Fake) {
	t.Helper()
	ms := storetest.New()
	ms.Users = append(ms.Users, users...)
	return &BootstrapOps{
		Store: ms,
		Cfg: &config.Config{
			Users: config.UsersConfig{DefaultHomeRoot: t.TempDir()},
			Auth:  config.AuthConfig{PasswordLoginEnabled: true},
		},
	}, ms
}

// TestBootstrapHasHumanUsers — agents cannot log in, so an agent-only database
// is still pre-bootstrap and must keep the setup form reachable.
func TestBootstrapHasHumanUsers(t *testing.T) {
	cases := []struct {
		name  string
		users []store.User
		want  bool
	}{
		{name: "empty database", want: false},
		{name: "agents only", users: []store.User{{ID: "a1", Name: "Bot"}}, want: false},
		{name: "one human", users: []store.User{{ID: "u1", Username: "alice"}}, want: true},
		{
			name:  "human alongside agents",
			users: []store.User{{ID: "a1", Name: "Bot"}, {ID: "u1", Username: "alice"}},
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := newBootstrapTestOps(t, tc.users...)
			got, err := o.HasHumanUsers(context.Background())
			if err != nil {
				t.Fatalf("HasHumanUsers: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBootstrapCreateFirstAdmin(t *testing.T) {
	o, ms := newBootstrapTestOps(t)
	user, err := o.CreateFirstAdmin(context.Background(), BootstrapAdminParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "secret123",
		Name:     "Alice",
	})
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if !user.IsAdmin {
		t.Error("the first account must be an admin — nothing else could grant it")
	}
	if !VerifyPassword(user.PasswordHash, "secret123") {
		t.Error("stored password hash does not verify")
	}
	if want := filepath.Join(o.Cfg.Users.DefaultHomeRoot, "alice"); user.WorkDir != want {
		t.Errorf("work_dir = %q, want %q", user.WorkDir, want)
	}
	if _, err := os.Stat(user.WorkDir); err != nil {
		t.Errorf("work_dir not materialised: %v", err)
	}
	assertDefaultAgent(t, ms.Users, user)
}

// TestBootstrapCreateFirstAdminDefaults — the form may omit the username and
// the display name; both fall back rather than bouncing a request that already
// supplied a valid email.
func TestBootstrapCreateFirstAdminDefaults(t *testing.T) {
	o, _ := newBootstrapTestOps(t)
	user, err := o.CreateFirstAdmin(context.Background(), BootstrapAdminParams{
		Email:    "bob@example.com",
		Password: "secret123",
	})
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if user.Username != "bob" || user.Name != "bob" {
		t.Errorf("username/name = %q/%q, want bob/bob", user.Username, user.Name)
	}
}

func TestBootstrapCreateFirstAdminRejects(t *testing.T) {
	cases := []struct {
		name   string
		params BootstrapAdminParams
	}{
		{
			name:   "no email",
			params: BootstrapAdminParams{Username: "alice", Password: "secret123"},
		},
		{
			name:   "email without a local-part",
			params: BootstrapAdminParams{Email: "@example.com", Password: "secret123"},
		},
		{
			name:   "username disagrees with the local-part",
			params: BootstrapAdminParams{Username: "carol", Email: "carol-prime@example.com", Password: "secret123"},
		},
		{
			name:   "no password",
			params: BootstrapAdminParams{Username: "alice", Email: "alice@example.com"},
		},
		{
			name:   "password shorter than the minimum",
			params: BootstrapAdminParams{Email: "alice@example.com", Password: "1234567"},
		},
		{
			name:   "password beyond bcrypt's limit",
			params: BootstrapAdminParams{Email: "alice@example.com", Password: strings.Repeat("x", 73)},
		},
		{
			name:   "local-part climbs out of the home root",
			params: BootstrapAdminParams{Email: "../escape@example.com", Password: "secret123"},
		},
		{
			name:   "local-part with a path separator",
			params: BootstrapAdminParams{Email: "a/b@example.com", Password: "secret123"},
		},
		{
			name:   "local-part with a leading dot",
			params: BootstrapAdminParams{Email: ".hidden@example.com", Password: "secret123"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, ms := newBootstrapTestOps(t)
			_, err := o.CreateFirstAdmin(context.Background(), tc.params)
			if svcStatus(err) != 400 {
				t.Fatalf("status = %d, want 400 (err=%v)", svcStatus(err), err)
			}
			if len(ms.Users) != 0 {
				t.Error("nothing may be persisted when validation fails")
			}
			parent := filepath.Dir(o.Cfg.Users.DefaultHomeRoot)
			if entries, _ := os.ReadDir(o.Cfg.Users.DefaultHomeRoot); len(entries) != 0 {
				t.Errorf("home root should stay empty, got %d entries", len(entries))
			}
			if _, err := os.Stat(filepath.Join(parent, "escape")); err == nil {
				t.Error("a directory was created outside the home root")
			}
		})
	}
}

// TestBootstrapCreateFirstAdminClosed — once a human exists the store refuses
// the insert; the call reports 409 and takes back the directory it made, so a
// losing concurrent submission leaves nothing behind.
func TestBootstrapCreateFirstAdminClosed(t *testing.T) {
	o, ms := newBootstrapTestOps(t, store.User{ID: "u1", Username: "alice"})
	_, err := o.CreateFirstAdmin(context.Background(), BootstrapAdminParams{
		Email:    "mallory@example.com",
		Password: "secret123",
	})
	if svcStatus(err) != 409 {
		t.Fatalf("status = %d, want 409 (err=%v)", svcStatus(err), err)
	}
	if len(ms.Users) != 1 {
		t.Errorf("rows = %d, want only the existing human", len(ms.Users))
	}
	if _, err := os.Stat(filepath.Join(o.Cfg.Users.DefaultHomeRoot, "mallory")); !os.IsNotExist(err) {
		t.Errorf("the losing submission's home directory must be removed (stat err=%v)", err)
	}
}

// TestBootstrapCreateFirstAdminSSOEmail — with password login off and SSO on,
// the form records only the email: SSO login finds the row by email, and a
// password would be one the login endpoint always refuses.
func TestBootstrapCreateFirstAdminSSOEmail(t *testing.T) {
	o, ms := newBootstrapTestOps(t)
	o.Cfg.Auth.PasswordLoginEnabled = false
	o.Cfg.OIDC = config.OIDCConfig{Enabled: true}
	user, err := o.CreateFirstAdmin(context.Background(), BootstrapAdminParams{Email: "alice@example.com"})
	if err != nil {
		t.Fatalf("CreateFirstAdmin: %v", err)
	}
	if !user.IsAdmin || user.PasswordHash != "" {
		t.Errorf("is_admin=%v password_hash=%q, want admin with no password", user.IsAdmin, user.PasswordHash)
	}
	assertDefaultAgent(t, ms.Users, user)
}

// TestBootstrapCreateFirstAdminFormClosed — no form when SSO provisions the
// admin, and none when no sign-in method exists to use the account with.
func TestBootstrapCreateFirstAdminFormClosed(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(*config.Config)
	}{
		{name: "sso provisions the admin", cfg: func(c *config.Config) {
			c.OIDC = config.OIDCConfig{Enabled: true, AutoProvision: true}
			c.Admin.BootstrapUsernames = []string{"alice"}
		}},
		{name: "no sign-in method", cfg: func(c *config.Config) { c.Auth.PasswordLoginEnabled = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, ms := newBootstrapTestOps(t)
			tc.cfg(o.Cfg)
			_, err := o.CreateFirstAdmin(context.Background(), BootstrapAdminParams{
				Email: "alice@example.com", Password: "secret123",
			})
			if svcStatus(err) != 403 {
				t.Fatalf("status = %d, want 403 (err=%v)", svcStatus(err), err)
			}
			if len(ms.Users) != 0 {
				t.Error("nothing may be persisted")
			}
		})
	}
}

func TestFirstAdminSetup(t *testing.T) {
	cases := []struct {
		name     string
		password bool
		oidc     config.OIDCConfig
		admins   []string
		want     FirstAdminSetupMode
	}{
		{name: "password login", password: true, want: SetupModePassword},
		{name: "password login beside plain sso", password: true, oidc: config.OIDCConfig{Enabled: true}, want: SetupModePassword},
		{name: "sso provisions admin", password: true, oidc: config.OIDCConfig{Enabled: true, AutoProvision: true}, admins: []string{"alice"}, want: SetupModeSSO},
		{name: "sso only, cannot make an admin", oidc: config.OIDCConfig{Enabled: true, AutoProvision: true}, want: SetupModeSSOEmail},
		{name: "nothing enabled", want: SetupModeUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Auth:  config.AuthConfig{PasswordLoginEnabled: tc.password},
				OIDC:  tc.oidc,
				Admin: config.AdminConfig{BootstrapUsernames: tc.admins},
			}
			if got := FirstAdminSetup(cfg); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
	if got := FirstAdminSetup(nil); got != SetupModePassword {
		t.Errorf("nil config = %q, want password", got)
	}
}

func TestValidateUsername(t *testing.T) {
	for _, ok := range []string{"alice", "a", "john.doe", "j_doe-2", "A1", strings.Repeat("a", 64)} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, ".hidden", "-x", "a b", "a+b", "名字", strings.Repeat("a", 65)} {
		if err := ValidateUsername(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestBootstrapCreateFirstAdminWithoutHomeRoot — with no configured home root
// there is no directory to provision the admin under, and inventing one would
// scatter user data wherever the process happens to run.
func TestBootstrapCreateFirstAdminWithoutHomeRoot(t *testing.T) {
	o, _ := newBootstrapTestOps(t)
	o.Cfg = nil
	_, err := o.CreateFirstAdmin(context.Background(), BootstrapAdminParams{
		Email:    "alice@example.com",
		Password: "secret123",
	})
	if svcStatus(err) != 500 {
		t.Fatalf("status = %d, want 500 (err=%v)", svcStatus(err), err)
	}
}

// TestOIDCProvisionsFirstAdmin — SSO replaces the setup form only when a first
// login is both auto-provisioned and promoted; any missing piece would leave a
// fresh install with no way to get an admin.
func TestOIDCProvisionsFirstAdmin(t *testing.T) {
	cases := []struct {
		name      string
		enabled   bool
		provision bool
		admins    []string
		want      bool
	}{
		{name: "sso provisions admin", enabled: true, provision: true, admins: []string{"alice"}, want: true},
		{name: "oidc disabled", enabled: false, provision: true, admins: []string{"alice"}, want: false},
		{name: "no auto provision", enabled: true, provision: false, admins: []string{"alice"}, want: false},
		{name: "no bootstrap admin", enabled: true, provision: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				OIDC:  config.OIDCConfig{Enabled: tc.enabled, AutoProvision: tc.provision},
				Admin: config.AdminConfig{BootstrapUsernames: tc.admins},
			}
			if got := OIDCProvisionsFirstAdmin(cfg); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
	if OIDCProvisionsFirstAdmin(nil) {
		t.Errorf("nil config must not skip setup")
	}
}
