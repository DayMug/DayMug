package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func newOIDCTestOps(t *testing.T, users ...store.User) *OIDCUserOps {
	t.Helper()
	ms := storetest.New()
	ms.Users = append(ms.Users, users...)
	return &OIDCUserOps{
		Store: ms,
		Cfg:   &config.Config{Users: config.UsersConfig{DefaultHomeRoot: t.TempDir()}},
	}
}

// TestDeriveUsername locks in the seed derivation: only the email local-part is
// honoured, sanitized. preferred_username and sub are ignored even when present
// so a display-name change at the IdP cannot rename a provisioned user's home
// directory. Note the "+tag" case is lossy on purpose — uniqueUsername owns
// resolving the collisions that creates.
func TestDeriveUsername(t *testing.T) {
	cases := []struct {
		name string
		id   OIDCIdentity
		want string
	}{
		{
			name: "email local-part",
			id:   OIDCIdentity{Email: "bob@example.com"},
			want: "bob",
		},
		{
			name: "preferred_username ignored when email present",
			id:   OIDCIdentity{PreferredUsername: "alternate", Email: "alice@example.com"},
			want: "alice",
		},
		{
			name: "preferred_username alone is not enough (no email)",
			id:   OIDCIdentity{PreferredUsername: "alice"},
			want: "",
		},
		{
			name: "sub alone is not enough (no email)",
			id:   OIDCIdentity{Sub: "abc-123"},
			want: "",
		},
		{
			name: "email local-part sanitized",
			id:   OIDCIdentity{Email: "first.last+tag@example.com"},
			want: "first.last-tag",
		},
		{
			name: "empty all",
			id:   OIDCIdentity{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveUsername(&tc.id); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestIsBootstrapAdmin(t *testing.T) {
	cfg := &config.Config{Admin: config.AdminConfig{BootstrapUsernames: []string{"alice", "bob"}}}
	if !IsBootstrapAdmin(cfg, "alice") {
		t.Error("alice should be a bootstrap admin")
	}
	if IsBootstrapAdmin(cfg, "carol") {
		t.Error("carol should not be a bootstrap admin")
	}
	if IsBootstrapAdmin(nil, "alice") {
		t.Error("nil config should promote nobody")
	}
}

// TestUniqueUsername covers the P4 fix: a collision must yield a suffixed
// username, never a refusal. Sanitising the local-part is lossy, so a login the
// operator cannot influence would otherwise be rejected outright.
func TestUniqueUsername(t *testing.T) {
	cases := []struct {
		name      string
		existing  []string
		candidate string
		want      string
	}{
		{name: "free candidate is returned verbatim", candidate: "alice", want: "alice"},
		{
			name:      "distinct emails sharing a local-part",
			existing:  []string{"ada"},
			candidate: "ada",
			want:      "ada-2",
		},
		{
			name:      "sanitising collision (alice+test vs alice-test)",
			existing:  []string{"alice-test"},
			candidate: "alice-test",
			want:      "alice-test-2",
		},
		{
			name:      "walks past an occupied suffix",
			existing:  []string{"bob", "bob-2", "bob-3"},
			candidate: "bob",
			want:      "bob-4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var users []store.User
			for i, u := range tc.existing {
				users = append(users, store.User{ID: fmt.Sprintf("u%d", i), Username: u})
			}
			got, err := newOIDCTestOps(t, users...).uniqueUsername(context.Background(), tc.candidate)
			if err != nil {
				t.Fatalf("uniqueUsername: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// TestUniqueUsername_ExhaustionIsBounded keeps the disambiguation loop from
// becoming an unauthenticated hot loop: past maxUsernameSuffix it gives up with
// an error rather than scanning forever.
func TestUniqueUsername_ExhaustionIsBounded(t *testing.T) {
	users := []store.User{{ID: "u1", Username: "dup"}}
	for i := 2; i <= maxUsernameSuffix; i++ {
		users = append(users, store.User{
			ID:       fmt.Sprintf("u%d", i),
			Username: fmt.Sprintf("dup-%d", i),
		})
	}
	if _, err := newOIDCTestOps(t, users...).uniqueUsername(context.Background(), "dup"); err == nil {
		t.Fatal("expected an error once every suffix is taken")
	}
}

// TestProvisionUser_PlusAddressedEmail is the end-to-end P4 case: an SSO
// identity whose local-part cannot round-trip through the username character
// set must still get an account. The email is stored verbatim (it is the key
// the callback re-finds the row by) even though the username differs from it,
// and the work_dir follows the stored username so it stays distinct from the
// alice-test@ account that already owns the alice-test home.
func TestProvisionUser_PlusAddressedEmail(t *testing.T) {
	o := newOIDCTestOps(t, store.User{
		ID: "u1", Username: "alice-test", Email: "alice-test@example.com",
	})
	ctx := context.Background()

	user, err := o.ProvisionUser(ctx, &OIDCIdentity{
		Email: "alice+test@example.com",
		Name:  "Alice Test",
	})
	if err != nil {
		t.Fatalf("ProvisionUser: %v", err)
	}
	if user.Username != "alice-test-2" {
		t.Errorf("username = %q, want alice-test-2 (suffixed away from the incumbent)", user.Username)
	}
	if user.Email != "alice+test@example.com" {
		t.Errorf("email = %q, want the address the IdP issued, verbatim", user.Email)
	}
	if want := filepath.Join(o.Cfg.Users.DefaultHomeRoot, "alice-test-2"); user.WorkDir != want {
		t.Errorf("work_dir = %q, want %q — sharing the incumbent's home would leak files", user.WorkDir, want)
	}
	if _, err := os.Stat(user.WorkDir); err != nil {
		t.Errorf("work_dir not materialised: %v", err)
	}

	// The row must be re-findable by email, which is how every later login for
	// this identity resolves. A username-keyed lookup would miss it.
	found, err := o.Store.GetUserByEmail(ctx, "alice+test@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail after provisioning: %v", err)
	}
	if found.ID != user.ID {
		t.Errorf("email lookup resolved to %q, want %q", found.ID, user.ID)
	}
	users, err := o.Store.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers after provisioning: %v", err)
	}
	assertDefaultAgent(t, users, user)
}

// TestProvisionUser_PromotesBootstrapAdmin — the first SSO login of a
// bootstrap-listed username must land as an admin, otherwise a deployment with
// password login disabled has no way to reach the admin surface at all.
func TestProvisionUser_PromotesBootstrapAdmin(t *testing.T) {
	o := newOIDCTestOps(t)
	o.Cfg.Admin.BootstrapUsernames = []string{"root"}

	user, err := o.ProvisionUser(context.Background(), &OIDCIdentity{Email: "root@example.com"})
	if err != nil {
		t.Fatalf("ProvisionUser: %v", err)
	}
	if !user.IsAdmin {
		t.Error("bootstrap-listed username should be provisioned as admin")
	}
	// Name falls back to the username when the IdP supplies none.
	if user.Name != "root" {
		t.Errorf("name = %q, want the username as fallback", user.Name)
	}
}

// TestProvisionUser_RequiresEmail — an IdP that returns no email cannot
// provision, because the email is the identity key every later login uses.
func TestProvisionUser_RequiresEmail(t *testing.T) {
	if _, err := newOIDCTestOps(t).ProvisionUser(context.Background(), &OIDCIdentity{Sub: "abc"}); err == nil {
		t.Fatal("expected provisioning to fail without an email")
	}
}

// TestProvisionUser_BindsInitialAccount — an SSO user is bound to the provider
// named "default" when one exists, otherwise to the first configured provider.
func TestProvisionUser_BindsInitialAccount(t *testing.T) {
	cases := []struct {
		name      string
		providers []config.Provider
		want      map[string]string
	}{
		{
			name: "prefers default by name",
			providers: []config.Provider{
				{Name: "alt", Type: config.CLITypeCodex},
				{Name: "default", Type: config.CLITypeClaude},
			},
			want: map[string]string{config.CLITypeClaude: "default"},
		},
		{
			name: "falls back to the first provider",
			providers: []config.Provider{
				{Name: "alt", Type: config.CLITypeCodex},
				{Name: "other", Type: config.CLITypeClaude},
			},
			want: map[string]string{config.CLITypeCodex: "alt"},
		},
		{name: "no provider configured", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOIDCTestOps(t)
			o.Cfg.Providers = tc.providers
			user, err := o.ProvisionUser(context.Background(), &OIDCIdentity{Email: "carol@example.com"})
			if err != nil {
				t.Fatalf("ProvisionUser: %v", err)
			}
			got, err := o.Store.GetUserProviderBindings(context.Background(), user.ID)
			if err != nil {
				t.Fatalf("GetUserProviderBindings: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("bindings = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("bindings = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
