package service

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// adminOpsFixture wires a fake store to a config whose default_home_root is a
// scratch dir, so CreateHuman's mkdir side effect stays inside t.TempDir().
func adminOpsFixture(t *testing.T) (*AdminUserOps, *storetest.Fake) {
	t.Helper()
	fake := storetest.New()
	return &AdminUserOps{
		Store: fake,
		Cfg: &config.Config{
			Users: config.UsersConfig{DefaultHomeRoot: t.TempDir()},
			Providers: []config.Provider{
				{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1},
			},
		},
	}, fake
}

func TestAdminUserOpsCreateHumanDerivesUsernameAndHome(t *testing.T) {
	ops, fake := adminOpsFixture(t)

	created, err := ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email:            "  alice@example.com ",
		Password:         "hunter2",
		ProviderBindings: map[string]string{config.CLITypeClaude: "default"},
	})
	if err != nil {
		t.Fatalf("CreateHuman: %v", err)
	}
	if created.Username != "alice" {
		t.Errorf("username = %q, want alice", created.Username)
	}
	// Name defaults to the username, not to the empty string.
	if created.Name != "alice" {
		t.Errorf("name = %q, want alice", created.Name)
	}
	if created.Email != "alice@example.com" {
		t.Errorf("email = %q, want the trimmed address", created.Email)
	}
	want := filepath.Join(ops.Cfg.Users.DefaultHomeRoot, "alice")
	if created.WorkDir != want {
		t.Errorf("work_dir = %q, want %q", created.WorkDir, want)
	}
	if info, err := os.Stat(created.WorkDir); err != nil || !info.IsDir() {
		t.Errorf("work_dir not created on disk: %v", err)
	}
	// The response echoes the normalised mode, not the empty request value.
	if created.SandboxMode != store.SandboxModeJailed {
		t.Errorf("sandbox_mode = %q, want jailed", created.SandboxMode)
	}
	if created.PasswordHash == "" || created.PasswordHash == "hunter2" {
		t.Error("password must be persisted as a hash")
	}
	assertDefaultAgent(t, fake.Users, created)
}

// The username==email-prefix invariant is what lets GetOwner resolve an agent
// back to its human, so every way of breaking it must be rejected before any
// write happens.
func TestAdminUserOpsCreateHumanInvariants(t *testing.T) {
	cases := []struct {
		name   string
		params AdminCreateUserParams
		status int
		msg    string
	}{
		{
			name:   "email required",
			params: AdminCreateUserParams{Password: "pw"},
			status: http.StatusBadRequest,
			msg:    "email is required",
		},
		{
			name:   "email needs a local part",
			params: AdminCreateUserParams{Email: "@example.com", Password: "pw"},
			status: http.StatusBadRequest,
			msg:    "email must contain a local-part before '@'",
		},
		{
			name:   "username must match the local part",
			params: AdminCreateUserParams{Username: "bob", Email: "alice@example.com", Password: "pw"},
			status: http.StatusBadRequest,
			msg:    "username must equal the local-part of email (everything before '@')",
		},
		{
			name:   "password required",
			params: AdminCreateUserParams{Email: "alice@example.com"},
			status: http.StatusBadRequest,
			msg:    "password is required",
		},
		{
			name: "unknown provider binding",
			params: AdminCreateUserParams{
				Email: "alice@example.com", Password: "pw",
				ProviderBindings: map[string]string{"claude": "ghost"},
			},
			status: http.StatusBadRequest,
			msg:    "unknown provider ghost for type claude",
		},
		{
			name: "unknown provider account",
			params: AdminCreateUserParams{
				Email: "alice@example.com", Password: "pw",
				ProviderAccounts: map[string][]string{"claude": {"ghost"}},
			},
			status: http.StatusBadRequest,
			msg:    "unknown provider ghost for type claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops, fake := adminOpsFixture(t)
			_, err := ops.CreateHuman(context.Background(), tc.params)
			wantUserOpsStatus(t, err, tc.status, tc.msg)
			if len(fake.Users) != 0 {
				t.Errorf("a rejected create wrote %d rows", len(fake.Users))
			}
		})
	}
}

func TestAdminUserOpsCreateHumanRejectsDuplicates(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	fake.Users = []store.User{{ID: "u1", Username: "alice", Email: "alice@example.com"}}

	// Same username, different domain.
	_, err := ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "alice@other.com", Password: "pw",
		ProviderBindings: map[string]string{config.CLITypeClaude: "default"},
	})
	wantUserOpsStatus(t, err, http.StatusConflict, "username already exists")

	// Same email, and the username matches too — the email check is reached
	// only when the username one passes, so shadow the first row's username.
	fake.Users[0].Username = "someone-else"
	_, err = ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "alice@example.com", Password: "pw",
		ProviderBindings: map[string]string{config.CLITypeClaude: "default"},
	})
	wantUserOpsStatus(t, err, http.StatusConflict, "email already exists")
}

// Without a config there is no default_home_root to fall back to, so an
// explicit absolute work_dir becomes mandatory rather than being invented.
func TestAdminUserOpsCreateHumanWorkDirRules(t *testing.T) {
	ops := &AdminUserOps{Store: storetest.New()}

	_, err := ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "alice@example.com", Password: "pw",
	})
	wantUserOpsStatus(t, err, http.StatusBadRequest, "work_dir is required (no default_home_root configured)")

	_, err = ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "alice@example.com", Password: "pw", WorkDir: "relative/path",
	})
	wantUserOpsStatus(t, err, http.StatusBadRequest, "work_dir must be absolute")
}

// A configless deployment still validates the model against the built-in
// per-type registry — unlike UserOps.NormalizeDefaultModel, which skips the
// check entirely when Cfg is nil.
func TestAdminUserOpsValidatesDefaultModelWithoutConfig(t *testing.T) {
	ops := &AdminUserOps{Store: storetest.New()}
	if err := ops.validateDefaultModel("not-a-real-model"); err == nil {
		t.Fatal("a nil Cfg must not disable admin model validation")
	}
	if err := ops.validateDefaultModel(""); err != nil {
		t.Fatalf("empty model clears the default and must be accepted: %v", err)
	}
}

func TestAdminUserOpsUpdateHumanEmailInvariant(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	target := store.User{ID: "u1", Username: "alice", Email: "alice@example.com"}
	fake.Users = []store.User{target, {ID: "u2", Username: "bob", Email: "bob@example.com"}}

	cleared := ""
	_, err := ops.UpdateHuman(context.Background(), target, AdminUserPatch{Email: &cleared}, "admin")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "email cannot be cleared on a human user")

	renamed := "alice2@example.com"
	_, err = ops.UpdateHuman(context.Background(), target, AdminUserPatch{Email: &renamed}, "admin")
	wantUserOpsStatus(t, err, http.StatusBadRequest,
		"email local-part must equal the existing username (this binding is the invariant the upload feature relies on)")
}

// Demoting yourself is the one-way door that locks the last admin out.
func TestAdminUserOpsUpdateHumanRefusesSelfDemotion(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	target := store.User{ID: "u1", Username: "alice", Email: "alice@example.com", IsAdmin: true}
	fake.Users = []store.User{target}

	no := false
	_, err := ops.UpdateHuman(context.Background(), target, AdminUserPatch{IsAdmin: &no}, "u1")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "cannot revoke your own admin flag")

	// A different admin may still do it.
	if _, err := ops.UpdateHuman(context.Background(), target, AdminUserPatch{IsAdmin: &no}, "u2"); err != nil {
		t.Fatalf("demotion by another admin: %v", err)
	}
}

// provider_accounts is authoritative for the types it names: the
// single-value provider_bindings path must not also fire for those types.
func TestAdminUserOpsUpdateHumanAccountsSupersedeBindings(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	target := store.User{ID: "u1", Username: "alice", Email: "alice@example.com"}
	fake.Users = []store.User{target}

	updated, err := ops.UpdateHuman(context.Background(), target, AdminUserPatch{
		ProviderBindings: map[string]string{"claude": "alt"},
		ProviderAccounts: map[string][]string{"claude": {" default ", ""}},
	}, "admin")
	if err != nil {
		t.Fatalf("UpdateHuman: %v", err)
	}
	// Blank entries are stripped and the first survivor becomes the default.
	if got := updated.ProviderBindings["claude"]; got != "default" {
		t.Errorf("claude binding = %q, want default", got)
	}
	if got := fake.Users[0].ProviderAccounts["claude"]; len(got) != 1 || got[0] != "default" {
		t.Errorf("claude accounts = %v, want [default]", got)
	}
}

func TestAdminUserOpsSetDisabledAndDelete(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	target := store.User{ID: "u1", Username: "alice", Email: "alice@example.com"}
	fake.Users = []store.User{target}

	err := ops.SetDisabled(context.Background(), target, true, "u1")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "cannot disable yourself")

	err = ops.DeleteHuman(context.Background(), target, "u1")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "cannot delete yourself")

	// Enabling yourself is fine — it can't lock anyone out.
	if err := ops.SetDisabled(context.Background(), target, false, "u1"); err != nil {
		t.Fatalf("SetDisabled(false): %v", err)
	}
}

func TestAdminUserOpsSetPasswordRequiresOne(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	target := store.User{ID: "u1", Username: "alice"}
	fake.Users = []store.User{target}

	err := ops.SetPassword(context.Background(), target, "")
	wantUserOpsStatus(t, err, http.StatusBadRequest, "password is required")

	if err := ops.SetPassword(context.Background(), target, "hunter2"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if fake.Users[0].PasswordHash == "" || fake.Users[0].PasswordHash == "hunter2" {
		t.Error("password must be stored as a hash")
	}
}

// Every batch validates the whole id list before writing, so one bad id leaves
// the earlier targets untouched rather than half-applied.
func TestAdminUserOpsBatchesValidateBeforeWriting(t *testing.T) {
	newFixture := func(t *testing.T) (*AdminUserOps, *storetest.Fake) {
		ops, fake := adminOpsFixture(t)
		fake.Users = []store.User{
			{ID: "u1", Username: "alice", Email: "alice@example.com"},
			{ID: "agent", OwnerID: "u1"},
		}
		return ops, fake
	}

	t.Run("default model", func(t *testing.T) {
		ops, fake := newFixture(t)
		_, err := ops.BatchSetDefaultModel(context.Background(), []string{"u1", "ghost"}, "")
		wantUserOpsStatus(t, err, http.StatusNotFound, "user not found: ghost")
		if fake.Users[0].DefaultModel != "" {
			t.Error("a rejected batch must not write")
		}
		if _, err := ops.BatchSetDefaultModel(context.Background(), nil, ""); err == nil {
			t.Error("empty user_ids must be rejected")
		}
		// An agent row is not a login-capable user.
		if _, err := ops.BatchSetDefaultModel(context.Background(), []string{"agent"}, ""); err == nil {
			t.Error("agent rows must be rejected as batch targets")
		}
	})

	t.Run("sandbox mode", func(t *testing.T) {
		ops, fake := newFixture(t)
		err := ops.BatchSetSandboxMode(context.Background(), []string{"u1"}, "sorta-jailed")
		wantUserOpsStatus(t, err, http.StatusBadRequest, "sandbox_mode must be 'jailed' or 'unrestricted'")
		if err := ops.BatchSetSandboxMode(context.Background(), []string{"u1"}, store.SandboxModeUnrestricted); err != nil {
			t.Fatalf("BatchSetSandboxMode: %v", err)
		}
		if fake.Users[0].SandboxMode != store.SandboxModeUnrestricted {
			t.Errorf("sandbox_mode = %q, want unrestricted", fake.Users[0].SandboxMode)
		}
	})

	t.Run("provider binding replaces the account set", func(t *testing.T) {
		ops, fake := newFixture(t)
		ops.Cfg.Providers = append(ops.Cfg.Providers, config.Provider{Name: "alt", Type: config.CLITypeClaude, MaxConcurrent: 1})
		_, _, err := ops.BatchSetProviderBinding(context.Background(), []string{"u1"}, "", []string{"default"})
		wantUserOpsStatus(t, err, http.StatusBadRequest, "provider_type is required")

		_, _, err = ops.BatchSetProviderBinding(context.Background(), []string{"u1"}, "claude", []string{"ghost"})
		wantUserOpsStatus(t, err, http.StatusBadRequest, "unknown provider ghost for type claude")

		ptype, names, e := ops.BatchSetProviderBinding(context.Background(), []string{"u1"}, " claude ", []string{" alt ", " default "})
		if e != nil {
			t.Fatalf("BatchSetProviderBinding: %v", e)
		}
		if ptype != "claude" || len(names) != 2 || names[0] != "alt" || names[1] != "default" {
			t.Errorf("echo = (%q, %v), want (claude, [alt default])", ptype, names)
		}
		if got := fake.Users[0].ProviderAccounts["claude"]; len(got) != 2 || got[0] != "alt" || got[1] != "default" {
			t.Errorf("claude accounts = %v, want exactly [alt default]", got)
		}
	})
}

// A new human must start with at least one account whenever one is
// configured; otherwise every chat is refused until an admin binds one.
func TestAdminUserOpsCreateHumanRequiresAccount(t *testing.T) {
	ops, fake := adminOpsFixture(t)
	_, err := ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "alice@example.com", Password: "pw",
		ProviderBindings: map[string]string{config.CLITypeClaude: ""},
	})
	wantUserOpsStatus(t, err, http.StatusBadRequest, "bind at least one account")
	if len(fake.Users) != 0 {
		t.Errorf("a rejected create wrote %d rows", len(fake.Users))
	}

	// A multi-account set satisfies the rule on its own.
	if _, err := ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "alice@example.com", Password: "pw",
		ProviderAccounts: map[string][]string{config.CLITypeClaude: {"default"}},
	}); err != nil {
		t.Fatalf("CreateHuman with provider_accounts: %v", err)
	}

	// With no provider configured there is nothing to bind, so no rule.
	ops.Cfg.Providers = nil
	if _, err := ops.CreateHuman(context.Background(), AdminCreateUserParams{
		Email: "bob@example.com", Password: "pw",
	}); err != nil {
		t.Fatalf("CreateHuman without providers: %v", err)
	}
}
