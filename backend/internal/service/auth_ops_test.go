package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// svcErrStatus pulls the HTTP status out of a service failure so a test can assert
// the contract the transport layer depends on without duplicating the mapping.
func svcErrStatus(t *testing.T, err error) int {
	t.Helper()
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	return svcErr.Status
}

func svcErrMessage(t *testing.T, err error) string {
	t.Helper()
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("expected *ServiceError, got %T: %v", err, err)
	}
	return svcErr.Message()
}

func TestAuthOpsSessionTTLPrefersConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{"nil config falls back", nil, DefaultSessionTTL},
		{"unset config falls back", &config.Config{}, DefaultSessionTTL},
		{
			name: "configured value wins",
			cfg:  &config.Config{Auth: config.AuthConfig{SessionTTL: config.Duration{Duration: time.Hour}}},
			want: time.Hour,
		},
		{
			name: "non-positive value falls back",
			cfg:  &config.Config{Auth: config.AuthConfig{SessionTTL: config.Duration{Duration: -time.Hour}}},
			want: DefaultSessionTTL,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := &AuthOps{Cfg: tc.cfg}
			if got := ops.SessionTTL(); got != tc.want {
				t.Errorf("SessionTTL() = %v, want %v", got, tc.want)
			}
		})
	}
}

// OpenSession is the single implementation behind password login, first-run
// bootstrap, and the OIDC callback, so it has to persist a session row whose
// expiry tracks the resolved TTL rather than a hard-coded 30 days.
func TestAuthOpsOpenSessionPersistsWithConfiguredTTL(t *testing.T) {
	ms := storetest.New()
	ops := &AuthOps{
		Store: ms,
		Cfg:   &config.Config{Auth: config.AuthConfig{SessionTTL: config.Duration{Duration: time.Hour}}},
	}

	before := time.Now()
	token, expiresAt, err := ops.OpenSession(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if token == "" {
		t.Fatal("OpenSession returned an empty token")
	}
	if delta := expiresAt.Sub(before); delta < 59*time.Minute || delta > 61*time.Minute {
		t.Errorf("expiry %v is not ~1h out from %v", expiresAt, before)
	}

	sess, err := ms.GetSession(context.Background(), token)
	if err != nil {
		t.Fatalf("session was not persisted: %v", err)
	}
	if sess.UserID != "user-1" {
		t.Errorf("session user = %q, want user-1", sess.UserID)
	}
	if !sess.ExpiresAt.Equal(expiresAt) {
		t.Errorf("stored expiry %v != returned %v", sess.ExpiresAt, expiresAt)
	}
}

func TestAuthOpsOpenSessionDefaultTTL(t *testing.T) {
	ms := storetest.New()
	ops := &AuthOps{Store: ms}

	before := time.Now()
	_, expiresAt, err := ops.OpenSession(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if delta := expiresAt.Sub(before); delta < DefaultSessionTTL-time.Minute {
		t.Errorf("expiry %v is short of the default TTL", delta)
	}
}

func TestAuthOpsLoginRejectsDisabledAccountAsInvalidCredentials(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{
		ID: "u1", Username: "alice", PasswordHash: hash, Disabled: true,
	})
	ops := &AuthOps{Store: ms, Cfg: &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}}}

	_, _, _, err = ops.Login(context.Background(), "alice", "secret")
	if got := svcErrStatus(t, err); got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
	// The whole point: a disabled account is indistinguishable from a wrong
	// password or an unknown username.
	if got := svcErrMessage(t, err); got != "invalid credentials" {
		t.Errorf("message = %q, want %q", got, "invalid credentials")
	}
}

func TestAuthOpsLoginSuccessOpensSession(t *testing.T) {
	hash, err := HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: hash})
	ops := &AuthOps{Store: ms, Cfg: &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}}}

	user, token, expiresAt, err := ops.Login(context.Background(), "alice", "secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if user.ID != "u1" {
		t.Errorf("user id = %q, want u1", user.ID)
	}
	if expiresAt.Before(time.Now()) {
		t.Errorf("session already expired at %v", expiresAt)
	}
	if _, err := ms.GetSession(context.Background(), token); err != nil {
		t.Errorf("login did not persist a session: %v", err)
	}
}

// A nil Cfg means "no policy configured" and must not read as "password login
// disabled" — several handlers are constructed without config in tests.
func TestAuthOpsLoginNilConfigAllowsPasswordLogin(t *testing.T) {
	ms := storetest.New()
	ops := &AuthOps{Store: ms}
	_, _, _, err := ops.Login(context.Background(), "", "")
	if got := svcErrStatus(t, err); got != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (not 403)", got)
	}
}

func TestAuthOpsLoginDisabledByConfig(t *testing.T) {
	ops := &AuthOps{Cfg: &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: false}}}
	_, _, _, err := ops.Login(context.Background(), "alice", "secret")
	if got := svcErrStatus(t, err); got != http.StatusForbidden {
		t.Errorf("status = %d, want 403", got)
	}
}

func TestAuthOpsUpdateNotifications(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		wantErr int // 0 means success
	}{
		{"empty channel", "", 0},
		{"bark", "bark", 0},
		{"pushdeer", "pushdeer", 0},
		{"unknown channel rejected", "telegram", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
			ops := &AuthOps{Store: ms}

			err := ops.UpdateNotifications(context.Background(), "u1", "https://bark.example/k", "pd-key", tc.channel)
			if tc.wantErr != 0 {
				if got := svcErrStatus(t, err); got != tc.wantErr {
					t.Fatalf("status = %d, want %d", got, tc.wantErr)
				}
				// A rejected enum must not have written any of the three
				// fields — validation happens before the first store call.
				if ms.Users[0].BarkURL != "" || ms.Users[0].PushDeerKey != "" {
					t.Errorf("rejected request still wrote fields: %+v", ms.Users[0])
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateNotifications: %v", err)
			}
			got := ms.Users[0]
			if got.BarkURL != "https://bark.example/k" || got.PushDeerKey != "pd-key" || got.NotificationChannel != tc.channel {
				t.Errorf("row not fully written: %+v", got)
			}
		})
	}
}

func TestAuthOpsUpdateNotificationsMissingUser(t *testing.T) {
	ops := &AuthOps{Store: storetest.New()}
	err := ops.UpdateNotifications(context.Background(), "ghost", "", "", "")
	if got := svcErrStatus(t, err); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
	if got := svcErrMessage(t, err); got != "user not found" {
		t.Errorf("message = %q, want %q", got, "user not found")
	}
	// Callers still get to test the cause with errors.Is.
	if !errors.Is(err, store.ErrNotFound) {
		t.Error("store.ErrNotFound should survive the ServiceError wrap")
	}
}

func TestAuthOpsChangePassword(t *testing.T) {
	hash, err := HashPassword("oldsecret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	newOps := func() (*storetest.Fake, *AuthOps) {
		ms := storetest.New()
		ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice", PasswordHash: hash})
		return ms, &AuthOps{Store: ms, Cfg: &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}}}
	}

	t.Run("success rotates the hash", func(t *testing.T) {
		ms, ops := newOps()
		if err := ops.ChangePassword(context.Background(), "u1", "oldsecret", "newsecret"); err != nil {
			t.Fatalf("ChangePassword: %v", err)
		}
		if !VerifyPassword(ms.Users[0].PasswordHash, "newsecret") {
			t.Error("new password does not verify against the stored hash")
		}
	})

	t.Run("wrong current password", func(t *testing.T) {
		_, ops := newOps()
		err := ops.ChangePassword(context.Background(), "u1", "wrong", "newsecret")
		if got := svcErrStatus(t, err); got != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", got)
		}
	})

	t.Run("identical passwords rejected", func(t *testing.T) {
		_, ops := newOps()
		err := ops.ChangePassword(context.Background(), "u1", "oldsecret", "oldsecret")
		if got := svcErrStatus(t, err); got != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", got)
		}
	})

	t.Run("sso-only row has no current password to verify", func(t *testing.T) {
		ms := storetest.New()
		ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
		ops := &AuthOps{Store: ms, Cfg: &config.Config{Auth: config.AuthConfig{PasswordLoginEnabled: true}}}
		err := ops.ChangePassword(context.Background(), "u1", "anything", "newsecret")
		if got := svcErrStatus(t, err); got != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", got)
		}
	})
}

func TestAuthOpsUpdateEnvironment(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	ops := &AuthOps{Store: ms}

	got, err := ops.UpdateEnvironment(context.Background(), "u1", "  FOO=bar\nBAZ=qux  ")
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if got != "FOO=bar\nBAZ=qux" {
		t.Errorf("returned env = %q, want the trimmed text", got)
	}
	if ms.Users[0].Env != got {
		t.Errorf("stored env %q != returned %q", ms.Users[0].Env, got)
	}
}

func TestAuthOpsUpdateEnvironmentRejectsInvalidLine(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Username: "alice"})
	ops := &AuthOps{Store: ms}

	if _, err := ops.UpdateEnvironment(context.Background(), "u1", "not-an-assignment"); svcErrStatus(t, err) != http.StatusBadRequest {
		t.Errorf("expected 400 for a malformed line")
	}
	if ms.Users[0].Env != "" {
		t.Errorf("invalid input must not be persisted, got %q", ms.Users[0].Env)
	}
}
