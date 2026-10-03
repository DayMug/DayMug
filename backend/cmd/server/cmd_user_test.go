package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func newCmdTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// cfgWithHomeRoot builds a minimal *config.Config that satisfies the fields
// createHumanUser actually reads. Avoids running the YAML loader so tests
// stay self-contained and don't depend on the binary-relative resolver.
func cfgWithHomeRoot(homeRoot string, accounts []string) *config.Config {
	cfg := &config.Config{
		Users: config.UsersConfig{DefaultHomeRoot: homeRoot},
	}
	for _, name := range accounts {
		cfg.Providers = append(cfg.Providers, config.Provider{Name: name})
	}
	return cfg
}

func TestCreateHumanUser_HappyPath(t *testing.T) {
	db := newCmdTestStore(t)
	homeRoot := t.TempDir()
	cfg := cfgWithHomeRoot(homeRoot, nil)

	user, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter2",
		Admin:    true,
	})
	if err != nil {
		t.Fatalf("createHumanUser: %v", err)
	}

	if user.Username != "alice" || user.Email != "alice@example.com" {
		t.Errorf("unexpected identity: username=%q email=%q", user.Username, user.Email)
	}
	if !user.IsAdmin {
		t.Error("expected IsAdmin=true")
	}
	wantWorkDir := filepath.Join(homeRoot, "alice")
	if user.WorkDir != wantWorkDir {
		t.Errorf("work_dir = %q, want %q", user.WorkDir, wantWorkDir)
	}
	if info, statErr := os.Stat(wantWorkDir); statErr != nil || !info.IsDir() {
		t.Errorf("expected work_dir %s to be created", wantWorkDir)
	}
	if !service.VerifyPassword(user.PasswordHash, "hunter2") {
		t.Error("password hash does not verify against plain")
	}

	// Persisted row should be reachable via the same lookups the rest of
	// the codebase uses (login + GetOwner-by-email).
	got, err := db.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("round-tripped id = %q, want %q", got.ID, user.ID)
	}
	if _, err := db.GetUserByEmail(context.Background(), "alice@example.com"); err != nil {
		t.Errorf("GetUserByEmail: %v", err)
	}
	users, err := db.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var defaultAgents []store.User
	for _, candidate := range users {
		if candidate.OwnerID == user.ID {
			defaultAgents = append(defaultAgents, candidate)
		}
	}
	if len(defaultAgents) != 1 {
		t.Fatalf("default Agents = %+v, want exactly one", defaultAgents)
	}
	if agent := defaultAgents[0]; agent.Name != "alice" || agent.WorkDir != wantWorkDir || agent.Username != "" {
		t.Errorf("default Agent = %+v, want name alice and work_dir %q", agent, wantWorkDir)
	}
}

func TestCreateHumanUser_RejectsUsernameEmailMismatch(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "bob@example.com",
		Password: "hunter2",
	})
	if err == nil {
		t.Fatal("expected error when username != email local-part")
	}
	if !strings.Contains(err.Error(), "must equal the local-part") {
		t.Errorf("error %q does not mention the invariant", err.Error())
	}
}

// The username names the home directory, so one that is not a single plain
// path segment must be refused before anything touches the filesystem.
func TestCreateHumanUser_RejectsUnsafeUsername(t *testing.T) {
	db := newCmdTestStore(t)
	root := filepath.Join(t.TempDir(), "users")
	cfg := cfgWithHomeRoot(root, nil)

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "../escape",
		Email:    "../escape@example.com",
		Password: "hunter2",
	})
	if err == nil {
		t.Fatal("expected error for a username that climbs out of the home root")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escape")); statErr == nil {
		t.Error("a directory was created outside the home root")
	}
}

func TestCreateHumanUser_RejectsMissingEmail(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Password: "hunter2",
	})
	if err == nil || !strings.Contains(err.Error(), "--email is required") {
		t.Fatalf("expected --email required error, got %v", err)
	}
}

func TestCreateHumanUser_RejectsEmptyPassword(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "password must not be empty") {
		t.Fatalf("expected empty-password error, got %v", err)
	}
}

func TestCreateHumanUser_RejectsDuplicateUsername(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)

	if _, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter2",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "different",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already-exists error, got %v", err)
	}
}

func TestCreateHumanUser_RequiresAbsoluteWorkDir(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter2",
		WorkDir:  "relative/path",
	})
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestCreateHumanUser_RequiresHomeRootWhenWorkDirOmitted(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot("", nil)

	_, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter2",
	})
	if err == nil || !strings.Contains(err.Error(), "default_home_root") {
		t.Fatalf("expected default_home_root required error, got %v", err)
	}
}

func TestCreateHumanUser_HonorsExplicitWorkDir(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)
	custom := filepath.Join(t.TempDir(), "elsewhere")

	user, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter2",
		WorkDir:  custom,
	})
	if err != nil {
		t.Fatalf("createHumanUser: %v", err)
	}
	if user.WorkDir != custom {
		t.Errorf("WorkDir = %q, want %q", user.WorkDir, custom)
	}
	if info, statErr := os.Stat(custom); statErr != nil || !info.IsDir() {
		t.Errorf("expected custom work_dir %s to be created", custom)
	}
}

func TestParseUserAddArgs_PositionalThenFlags(t *testing.T) {
	// The command form the user actually typed in the wild — username before
	// the flags. Go's stock flag.Parse stops at the first non-flag, so this
	// case is the bug regression test.
	params, configPath, err := parseUserAddArgs([]string{"admin", "--email", "admin@example.com", "--admin"})
	if err != nil {
		t.Fatalf("parseUserAddArgs: %v", err)
	}
	if params.Username != "admin" || params.Email != "admin@example.com" || !params.Admin {
		t.Errorf("got %+v, want username=admin email=admin@example.com admin=true", params)
	}
	if configPath != "" {
		t.Errorf("configPath = %q, want empty", configPath)
	}
}

func TestParseUserAddArgs_FlagsThenPositional(t *testing.T) {
	params, _, err := parseUserAddArgs([]string{"--email", "admin@example.com", "--admin", "admin"})
	if err != nil {
		t.Fatalf("parseUserAddArgs: %v", err)
	}
	if params.Username != "admin" || params.Email != "admin@example.com" || !params.Admin {
		t.Errorf("got %+v, want username=admin email=admin@example.com admin=true", params)
	}
}

func TestParseUserAddArgs_FlagsAroundPositional(t *testing.T) {
	params, configPath, err := parseUserAddArgs([]string{
		"--config", "/tmp/c.yaml", "admin", "--email", "admin@example.com",
		"--work-dir", "/srv/admin", "--admin",
	})
	if err != nil {
		t.Fatalf("parseUserAddArgs: %v", err)
	}
	if params.Username != "admin" {
		t.Errorf("Username = %q", params.Username)
	}
	if params.Email != "admin@example.com" {
		t.Errorf("Email = %q", params.Email)
	}
	if !params.Admin {
		t.Error("Admin = false")
	}
	if params.WorkDir != "/srv/admin" {
		t.Errorf("WorkDir = %q", params.WorkDir)
	}
	if configPath != "/tmp/c.yaml" {
		t.Errorf("configPath = %q", configPath)
	}
}

func TestParseUserAddArgs_RejectsMissingUsername(t *testing.T) {
	_, _, err := parseUserAddArgs([]string{"--email", "admin@example.com"})
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestParseUserAddArgs_RejectsExtraPositional(t *testing.T) {
	_, _, err := parseUserAddArgs([]string{"admin", "extra", "--email", "admin@example.com"})
	if err == nil || !strings.Contains(err.Error(), "unexpected extra arguments") {
		t.Fatalf("expected extra-args error, got %v", err)
	}
}

func TestEmailLocalPart(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alice@example.com", "alice"},
		{"with.dot@a.b.c", "with.dot"},
		{"@only-domain", ""},
		{"no-at-sign", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := emailLocalPart(tc.in); got != tc.want {
			t.Errorf("emailLocalPart(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A CLI-created user starts bound to the "default" provider when one exists,
// otherwise to the first configured provider — same rule as OIDC provisioning.
func TestCreateHumanUser_BindsInitialAccount(t *testing.T) {
	db := newCmdTestStore(t)
	cfg := cfgWithHomeRoot(t.TempDir(), nil)
	cfg.Providers = []config.Provider{
		{Name: "alt", Type: config.CLITypeClaude},
		{Name: "default", Type: config.CLITypeClaude},
	}

	user, err := createHumanUser(context.Background(), db, cfg, addUserParams{
		Username: "alice", Email: "alice@example.com", Password: "hunter2",
	})
	if err != nil {
		t.Fatalf("createHumanUser: %v", err)
	}
	got, err := db.GetUserProviderBindings(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("GetUserProviderBindings: %v", err)
	}
	if got[config.CLITypeClaude] != "default" || len(got) != 1 {
		t.Errorf("bindings = %v, want claude=default", got)
	}
}
