package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadMinimal(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/daymug-users
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := cfg.ReplaceProviders([]Provider{{Name: "default", Type: CLITypeClaude, MaxConcurrent: 2}}); err != nil {
		t.Fatalf("ReplaceProviders: %v", err)
	}
	if cfg.Server.Addr != ":8080" {
		t.Errorf("addr default: got %q want :8080", cfg.Server.Addr)
	}
	if cfg.Auth.SessionTTL.Duration != 30*24*time.Hour {
		t.Errorf("session_ttl default: got %v", cfg.Auth.SessionTTL.Duration)
	}
	if !cfg.Auth.PasswordLoginEnabled {
		t.Errorf("password_login_enabled default should be true")
	}
	if cfg.Sandbox.Enabled || cfg.Sandbox.Type != SandboxNoop {
		t.Errorf("sandbox default: got enabled=%v type=%q", cfg.Sandbox.Enabled, cfg.Sandbox.Type)
	}
	if got := cfg.DefaultProviderType(); got != CLITypeClaude {
		t.Errorf("DefaultProviderType: got %q want %q", got, CLITypeClaude)
	}
	if got := cfg.FindAccount(""); got == nil || got.Name != "default" {
		t.Errorf("FindAccount(empty) should resolve to default")
	}
	if cfg.FindAccount("does-not-exist") != nil {
		t.Errorf("FindAccount unknown should return nil")
	}
}

// DefaultProviderType returns the CLI type of the first provider. Operators
// who want codex to be the default for new conversations list a codex
// provider first.
func TestDefaultProviderTypeFollowsProviderOrder(t *testing.T) {
	cfg := &Config{}
	if err := cfg.ReplaceProviders([]Provider{
		{Name: "default", Type: CLITypeCodex, ConfigDir: "/tmp/codex-home", MaxConcurrent: 1},
		{Name: "secondary", Type: CLITypeClaude, MaxConcurrent: 1},
	}); err != nil {
		t.Fatalf("ReplaceProviders: %v", err)
	}
	if got := cfg.DefaultProviderType(); got != CLITypeCodex {
		t.Errorf("DefaultProviderType: got %q want %q", got, CLITypeCodex)
	}
	if p, ok := cfg.DefaultProvider(); !ok || p.Name != "default" {
		t.Errorf("DefaultProvider: got %+v ok=%v, want first provider", p, ok)
	}
}

// Providers live in the database, never in YAML. A config file still
// carrying the old `providers` or `claude_accounts` blocks must keep loading
// — the loader is non-strict — but neither block may reach the provider cache.
func TestLoadIgnoresYAMLProviderBlocks(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
providers:
  - name: default
    type: claude
    max_concurrent: 3
claude_accounts:
  - name: old
    max_concurrent: 2
    claude_config_dir: /opt/claude-default
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.ProviderSnapshot(); len(got) != 0 {
		t.Fatalf("YAML provider blocks reached the cache: %+v", got)
	}
}

func TestNormalizeProvidersRejects(t *testing.T) {
	cases := []struct {
		name      string
		providers []Provider
		expect    string
	}{
		{
			// Codex has no implicit ~/.codex fallback to match Claude's.
			name: "codex without config_dir",
			providers: []Provider{
				{Name: "default", Type: CLITypeClaude},
				{Name: "codex-acc", Type: CLITypeCodex},
			},
			expect: "config_dir is required",
		},
		{
			name:      "missing type",
			providers: []Provider{{Name: "default"}},
			expect:    "type is required",
		},
		{
			name:      "retired mimo type",
			providers: []Provider{{Name: "default", Type: "mimo"}},
			expect:    `type "mimo" not supported`,
		},
		{
			name: "duplicate account name within one provider type",
			providers: []Provider{
				{Name: "default", Type: CLITypeClaude},
				{Name: "default", Type: CLITypeClaude},
			},
			expect: "must be unique across all provider types",
		},
		{
			name: "duplicate account name across provider types",
			providers: []Provider{
				{Name: "default", Type: CLITypeClaude},
				{Name: "default", Type: CLITypeCodex, ConfigDir: "/tmp/codex"},
			},
			expect: "must be unique across all provider types",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeProviders(tc.providers)
			if err == nil || !strings.Contains(err.Error(), tc.expect) {
				t.Fatalf("NormalizeProviders error = %v, want containing %q", err, tc.expect)
			}
		})
	}
}

func TestNormalizeProvidersAcceptsCompatibleTypes(t *testing.T) {
	// openai-compatible inherits codex's config_dir requirement: the
	// app-server has to have a CODEX_HOME to keep its rollout logs in.
	if _, err := NormalizeProviders([]Provider{
		{Name: "qwen", Type: CLITypeOpenAICompatible},
	}); err == nil {
		t.Error("openai-compatible without config_dir should be refused")
	}
	got, err := NormalizeProviders([]Provider{
		{Name: "glm", Type: CLITypeClaudeCompatible},
		{Name: "qwen", Type: CLITypeOpenAICompatible, ConfigDir: "/srv/qwen"},
	})
	if err != nil {
		t.Fatalf("NormalizeProviders: %v", err)
	}
	if got[0].Type != CLITypeClaudeCompatible || got[1].Type != CLITypeOpenAICompatible {
		t.Fatalf("types = %q, %q", got[0].Type, got[1].Type)
	}
	// Claude-family entries may omit config_dir (they fall back to ~/.claude).
	if got[0].ConfigDir != "" {
		t.Errorf("config_dir = %q, want empty", got[0].ConfigDir)
	}
}

func TestNormalizeProvidersExpandsHomeInConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := NormalizeProviders([]Provider{
		{Name: "default", Type: CLITypeClaude, ConfigDir: " ~/.claude "},
		{Name: "codex", Type: CLITypeCodex, ConfigDir: "~"},
		{Name: "abs", Type: CLITypeCodex, ConfigDir: "/srv/codex"},
	})
	if err != nil {
		t.Fatalf("NormalizeProviders: %v", err)
	}
	for i, want := range []string{filepath.Join(home, ".claude"), home, "/srv/codex"} {
		if got[i].ConfigDir != want {
			t.Errorf("providers[%d].config_dir = %q, want %q", i, got[i].ConfigDir, want)
		}
	}
}

func TestCodexCLITypes(t *testing.T) {
	for _, tt := range []struct {
		cliType string
		want    bool
	}{
		{CLITypeClaude, false},
		{CLITypeClaudeCompatible, false},
		{CLITypeCodex, true},
		{CLITypeOpenAICompatible, true},
		{"", false},
	} {
		if got := IsCodexFamily(tt.cliType); got != tt.want {
			t.Errorf("IsCodexFamily(%q) = %v, want %v", tt.cliType, got, tt.want)
		}
	}
}

func TestLoadFullOverride(t *testing.T) {
	path := writeTempConfig(t, `
server:
  addr: "127.0.0.1:9000"
auth:
  session_ttl: 1h
  password_login_enabled: false
admin:
  bootstrap_usernames: [alice, bob]
users:
  default_home_root: /var/lib/daymug
sandbox:
  enabled: false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != "127.0.0.1:9000" {
		t.Errorf("addr: %q", cfg.Server.Addr)
	}
	if cfg.Auth.SessionTTL.Duration != time.Hour {
		t.Errorf("session_ttl: %v", cfg.Auth.SessionTTL.Duration)
	}
	if cfg.Auth.PasswordLoginEnabled {
		t.Errorf("password_login_enabled should be false")
	}
	if len(cfg.Admin.BootstrapUsernames) != 2 || cfg.Admin.BootstrapUsernames[0] != "alice" {
		t.Errorf("bootstrap_usernames: %v", cfg.Admin.BootstrapUsernames)
	}
	if cfg.Sandbox.Enabled {
		t.Errorf("sandbox should be disabled")
	}
}

// Configs written before `daymug auth` was removed still carry
// server.client_id; the key must keep loading so upgrades don't break.
func TestLoadIgnoresLegacyServerClientID(t *testing.T) {
	path := writeTempConfig(t, `
server:
  addr: ":7777"
  client_id: "fixed-client-uuid"
users:
  default_home_root: /tmp/daymug-users
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		expect string
	}{
		{
			name:   "relative default_home_root",
			body:   "users: {default_home_root: relative/path}\n",
			expect: "must be absolute",
		},
		{
			name: "unsupported sandbox type",
			body: `users:
  default_home_root: /tmp/x
sandbox:
  enabled: true
  type: docker
`,
			expect: "sandbox.type",
		},
		{
			name: "oidc enabled missing issuer",
			body: `users:
  default_home_root: /tmp/x
oidc:
  enabled: true
  client_id: c
  client_secret: s
  redirect_uri: https://example.com/cb
`,
			expect: "oidc.issuer",
		},
		{
			name: "oidc enabled missing client_id",
			body: `users:
  default_home_root: /tmp/x
oidc:
  enabled: true
  issuer: https://issuer.example.com
  client_secret: s
  redirect_uri: https://example.com/cb
`,
			expect: "oidc.client_id",
		},
		{
			name: "oidc enabled missing client_secret",
			body: `users:
  default_home_root: /tmp/x
oidc:
  enabled: true
  issuer: https://issuer.example.com
  client_id: c
  redirect_uri: https://example.com/cb
`,
			expect: "oidc.client_secret",
		},
		{
			name: "oidc enabled missing redirect_uri",
			body: `users:
  default_home_root: /tmp/x
oidc:
  enabled: true
  issuer: https://issuer.example.com
  client_id: c
  client_secret: s
`,
			expect: "oidc.redirect_uri",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.expect)
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("expected %q, got %v", tc.expect, err)
			}
		})
	}
}

func TestValidateAllowsProvidersToBeManagedAfterStartup(t *testing.T) {
	path := writeTempConfig(t, "users: {default_home_root: /tmp/x}\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config without YAML providers: %v", err)
	}
	if got := cfg.ProviderSnapshot(); len(got) != 0 {
		t.Fatalf("providers = %#v, want empty runtime cache", got)
	}
	if err := cfg.ReplaceProviders([]Provider{{Name: "primary", Type: CLITypeClaude, MaxConcurrent: 2}}); err != nil {
		t.Fatalf("replace runtime providers without a specially named default: %v", err)
	}
	if got := cfg.DefaultProviderType(); got != CLITypeClaude {
		t.Fatalf("default provider type = %q, want %q", got, CLITypeClaude)
	}
	if got := cfg.FindAccount(""); got == nil || got.Name != "primary" {
		t.Fatalf("empty account lookup = %#v, want first database row", got)
	}
}

func TestDefaultHomeRootFallsBackToTildeDaymug(t *testing.T) {
	path := writeTempConfig(t, `
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir on this platform: %v", err)
	}
	want := filepath.Join(home, ".daymug", "users")
	if cfg.Users.DefaultHomeRoot != want {
		t.Errorf("default_home_root: got %q want %q", cfg.Users.DefaultHomeRoot, want)
	}
}

func TestDefaultHomeRootExpandsTilde(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: ~/custom-daymug
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir on this platform: %v", err)
	}
	want := filepath.Join(home, "custom-daymug")
	if cfg.Users.DefaultHomeRoot != want {
		t.Errorf("default_home_root: got %q want %q", cfg.Users.DefaultHomeRoot, want)
	}
}

func TestEnsureDefaultHomeRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home-root", "nested")
	cfg := &Config{Users: UsersConfig{DefaultHomeRoot: root}}
	if err := cfg.EnsureDefaultHomeRoot(); err != nil {
		t.Fatalf("EnsureDefaultHomeRoot: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Errorf("expected dir %s, err=%v info=%v", root, err, info)
	}
}

func TestSandboxNoopExplicit(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
sandbox:
  enabled: true
  type: noop
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sandbox.Enabled {
		t.Errorf("sandbox.type=noop should disable sandbox even when enabled=true")
	}
}

func TestSandboxBwrapStaysEnabled(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
sandbox:
  enabled: true
  type: bwrap
  network: false
  extra_ro_binds: [/srv/assets]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Sandbox.Enabled || cfg.Sandbox.Type != SandboxBwrap {
		t.Errorf("bwrap sandbox should stay enabled: enabled=%v type=%q", cfg.Sandbox.Enabled, cfg.Sandbox.Type)
	}
	if len(cfg.Sandbox.ExtraROBinds) != 1 || cfg.Sandbox.ExtraROBinds[0] != "/srv/assets" {
		t.Errorf("extra_ro_binds not parsed: %v", cfg.Sandbox.ExtraROBinds)
	}
}

func TestSandboxNetwork(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"omitted defaults to online", "sandbox:\n  enabled: true\n  type: bwrap\n", true},
		{"explicit false stays offline", "sandbox:\n  enabled: true\n  type: bwrap\n  network: false\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeTempConfig(t, "users:\n  default_home_root: /tmp/x\n"+tc.yaml))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Sandbox.Network != tc.want {
				t.Errorf("sandbox.network = %v, want %v", cfg.Sandbox.Network, tc.want)
			}
		})
	}
}

func TestOIDCDisabledIgnoresEmptyFields(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDC.Enabled {
		t.Errorf("oidc.enabled should default to false")
	}
	// Defaults populated even when disabled, so handlers can still reference them.
	if cfg.OIDC.GroupClaim != "groups" || cfg.OIDC.RoleClaim != "roles" || cfg.OIDC.PermissionClaim != "permissions" {
		t.Errorf("default claim names lost: %+v", cfg.OIDC)
	}
}

func TestOIDCEnabledFillsDefaults(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
oidc:
  enabled: true
  issuer: https://auth.example.com
  client_id: app
  client_secret: secret
  redirect_uri: https://app.example.com/api/auth/oidc/callback
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.OIDC.Enabled {
		t.Fatalf("oidc.enabled should be true")
	}
	if got := cfg.OIDC.Scopes; len(got) != 3 || got[0] != "openid" {
		t.Errorf("scope defaults: %v", got)
	}
	if cfg.OIDC.ButtonLabel != "Sign in with SSO" {
		t.Errorf("button label default: %q", cfg.OIDC.ButtonLabel)
	}
	if !cfg.OIDC.AutoProvision {
		t.Errorf("auto_provision default should be true")
	}
}

func TestLoadRequiresExplicitPath(t *testing.T) {
	t.Setenv("DAYMUG_CONFIG", "")
	// Point the binary-dir fallback at an empty temp dir so this test
	// doesn't accidentally pick up a config.yaml on the real test binary's
	// location (which would silently mask the "no config" failure).
	dir := t.TempDir()
	orig := resolveExecutableDir
	resolveExecutableDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { resolveExecutableDir = orig })

	_, err := Load("")
	if !errors.Is(err, ErrNoConfigPath) {
		t.Fatalf("expected ErrNoConfigPath, got %v", err)
	}
}

func TestLoadFallsBackToBinaryDirConfigYml(t *testing.T) {
	t.Setenv("DAYMUG_CONFIG", "")
	dir := t.TempDir()
	body := []byte(`
users:
  default_home_root: /tmp/daymug-fallback
`)
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFilename), body, 0o600); err != nil {
		t.Fatalf("write fallback config: %v", err)
	}
	orig := resolveExecutableDir
	resolveExecutableDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { resolveExecutableDir = orig })

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with binary-dir fallback: %v", err)
	}
	if cfg.Users.DefaultHomeRoot != "/tmp/daymug-fallback" {
		t.Errorf("loaded wrong config: %+v", cfg.Users)
	}
}

func TestLoadCallerPathBeatsBinaryDirFallback(t *testing.T) {
	dir := t.TempDir()
	// A "wrong" config.yaml next to the binary that would fail validation
	// — Load must never reach it when the caller passed an explicit path.
	if err := os.WriteFile(filepath.Join(dir, DefaultConfigFilename), []byte("not yaml: ::: ::"), 0o600); err != nil {
		t.Fatalf("write decoy config: %v", err)
	}
	orig := resolveExecutableDir
	resolveExecutableDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { resolveExecutableDir = orig })

	good := writeTempConfig(t, `
users:
  default_home_root: /tmp/daymug-explicit
`)
	cfg, err := Load(good)
	if err != nil {
		t.Fatalf("Load with explicit path: %v", err)
	}
	if cfg.Users.DefaultHomeRoot != "/tmp/daymug-explicit" {
		t.Errorf("explicit path was not honored: %+v", cfg.Users)
	}
}

func TestDefaultDBPathSiblingOfBinary(t *testing.T) {
	// DefaultDBPath should anchor under the running binary's directory so
	// a `daymug bootstrap`-style layout always resolves to
	// <bin_dir>/data/database.db without any env var or YAML knob.
	got := DefaultDBPath()
	wantSuffix := filepath.Join("data", "database.db")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("DefaultDBPath: got %q, expected suffix %q", got, wantSuffix)
	}
}

func TestUpgradeFillsDefaults(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Upgrade.Service != "daymug" || cfg.Upgrade.ServiceMode != "user" {
		t.Errorf("service defaults: got service=%q mode=%q", cfg.Upgrade.Service, cfg.Upgrade.ServiceMode)
	}
	if cfg.Upgrade.HealthTimeout.Duration != 60*time.Second {
		t.Errorf("health_timeout default: got %v", cfg.Upgrade.HealthTimeout.Duration)
	}
	if cfg.Upgrade.HealthPath != "/api/health" {
		t.Errorf("health_path default: got %q", cfg.Upgrade.HealthPath)
	}
}

func TestUpgradeRejectsNonAbsoluteHealthPath(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
upgrade:
  health_path: api/health
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "health_path") {
		t.Fatalf("expected health_path error, got %v", err)
	}
}

func TestUpgradeRejectsBadServiceMode(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
upgrade:
  service_mode: bogus
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "service_mode") {
		t.Fatalf("expected service_mode error, got %v", err)
	}
}

func TestUsageTimezoneDefaultsToShanghai(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Usage.Timezone != "Asia/Shanghai" {
		t.Errorf("timezone default: got %q want Asia/Shanghai", cfg.Usage.Timezone)
	}
	loc := cfg.UsageLocation()
	if loc == nil || loc.String() != "Asia/Shanghai" {
		t.Errorf("UsageLocation default: got %v want Asia/Shanghai", loc)
	}
}

func TestUsageTimezoneCustom(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
usage:
  timezone: Asia/Shanghai
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	loc := cfg.UsageLocation()
	if loc == nil || loc.String() != "Asia/Shanghai" {
		t.Errorf("UsageLocation: got %v want Asia/Shanghai", loc)
	}
}

func TestUsageAnomalyThresholdsDefaultAndConfigure(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
usage:
  high_model_requests: 12
  high_tool_calls: 18
  high_conversation_cost_usd: 4.5
  context_warning_ratio: 0.75
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Usage.HighModelRequests != 12 || cfg.Usage.HighToolCalls != 18 ||
		cfg.Usage.HighConversationCost != 4.5 || cfg.Usage.ContextWarningRatio != 0.75 {
		t.Fatalf("thresholds = %+v", cfg.Usage)
	}
}

func TestUsageRejectsInvalidContextWarningRatio(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
usage:
  context_warning_ratio: 1.2
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "context_warning_ratio") {
		t.Fatalf("expected context_warning_ratio error, got %v", err)
	}
}

func TestUsageTimezoneRejectsBadName(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
usage:
  timezone: Atlantis/Lost
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "usage.timezone") {
		t.Fatalf("expected usage.timezone error, got %v", err)
	}
}

func TestMaxConcurrentClampedToOne(t *testing.T) {
	got, err := NormalizeProviders([]Provider{{Name: "default", Type: CLITypeClaude, MaxConcurrent: 0}})
	if err != nil {
		t.Fatalf("NormalizeProviders: %v", err)
	}
	if got[0].MaxConcurrent != 1 {
		t.Errorf("max_concurrent should clamp to 1, got %d", got[0].MaxConcurrent)
	}
}

func TestUserMaxConcurrentDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  int
	}{
		{name: "default", want: DefaultUserMaxConcurrent},
		{name: "configured", value: "  max_concurrent: 7\n", want: 7},
		{name: "non-positive falls back", value: "  max_concurrent: 0\n", want: DefaultUserMaxConcurrent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
`+tc.value+`
`)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Users.MaxConcurrent != tc.want {
				t.Fatalf("users.max_concurrent = %d, want %d", cfg.Users.MaxConcurrent, tc.want)
			}
		})
	}
}

func TestRunnerWatchdogDefaults(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/x
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RunnerStallTimeout.Duration != 10*time.Minute {
		t.Errorf("runner_stall_timeout default: got %v", cfg.RunnerStallTimeout.Duration)
	}
	if cfg.RunnerMaxSilentTimeout.Duration != 2*time.Hour {
		t.Errorf("runner_max_silent_timeout default: got %v", cfg.RunnerMaxSilentTimeout.Duration)
	}
}

// Transports are chosen per provider type in the admin UI. Configs written
// when they were YAML keys — including the long-removed `cli` value — and
// the older runner_mode key must still load, ignored.
func TestLoadIgnoresRemovedTransportKeys(t *testing.T) {
	path := writeTempConfig(t, `
claude_backend: cli
codex_backend: cli
runner_mode: cli
users:
  default_home_root: /tmp/x
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestRunnerWatchdogTimeoutsParse(t *testing.T) {
	path := writeTempConfig(t, `
runner_stall_timeout: 30m
runner_max_silent_timeout: 3h
users:
  default_home_root: /tmp/x
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RunnerStallTimeout.Duration != 30*time.Minute {
		t.Errorf("runner_stall_timeout: got %v", cfg.RunnerStallTimeout.Duration)
	}
	if cfg.RunnerMaxSilentTimeout.Duration != 3*time.Hour {
		t.Errorf("runner_max_silent_timeout: got %v", cfg.RunnerMaxSilentTimeout.Duration)
	}
}

func TestIMRunTimeoutDefaultsDisabledAndParsesOverride(t *testing.T) {
	tests := []struct {
		name string
		line string
		want time.Duration
	}{
		{name: "disabled by default", want: 0},
		{name: "configured", line: "im_run_timeout: 45m\n", want: 45 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.line+`
users:
  default_home_root: /tmp/x
`)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.IMRunTimeout.Duration != tt.want {
				t.Fatalf("im_run_timeout = %v, want %v", cfg.IMRunTimeout.Duration, tt.want)
			}
		})
	}
}

func TestCaseRotateContextRatioDefaultsAndParsesOverride(t *testing.T) {
	tests := []struct {
		name string
		line string
		want float64
	}{
		{name: "absent falls back to the built-in", want: DefaultCaseRotateContextRatio},
		{name: "configured lower", line: "case_rotate_context_ratio: 0.55\n", want: 0.55},
		{name: "configured higher", line: "case_rotate_context_ratio: 0.85\n", want: 0.85},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.line+`
users:
  default_home_root: /tmp/x
`)
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.CaseRotateContextRatio != tt.want {
				t.Fatalf("case_rotate_context_ratio = %v, want %v", cfg.CaseRotateContextRatio, tt.want)
			}
		})
	}
}

// A ratio at or above the full window can never fire, so accepting it would
// present as "rotation is configured but never runs" — the same silent failure
// this whole knob was added to end.
func TestCaseRotateContextRatioRejectsUnreachableValues(t *testing.T) {
	for _, raw := range []string{"1", "1.5", "-0.2"} {
		t.Run(raw, func(t *testing.T) {
			path := writeTempConfig(t, "case_rotate_context_ratio: "+raw+`
users:
  default_home_root: /tmp/x
`)
			if _, err := Load(path); err == nil {
				t.Fatalf("case_rotate_context_ratio: %s was accepted", raw)
			}
		})
	}
}

func TestRunnerWatchdogRejectsMaxBelowStall(t *testing.T) {
	path := writeTempConfig(t, `
runner_stall_timeout: 30m
runner_max_silent_timeout: 10m
users:
  default_home_root: /tmp/x
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "runner_max_silent_timeout") {
		t.Fatalf("expected runner_max_silent_timeout validation error, got %v", err)
	}
}

// FindAccountForType requires an explicit binding: an empty name must not
// fall back to the "default" provider (nor any other), and a non-empty name
// must match both the name and the requested CLI type.
func TestFindAccountForType_RequiresExplicitBinding(t *testing.T) {
	c := &Config{
		Providers: []Provider{
			{Name: "default", Type: CLITypeClaude},
			{Name: "hailing", Type: CLITypeClaude},
			{Name: "default-codex", Type: CLITypeCodex},
		},
	}

	// Empty name never resolves, even though a "default" provider exists.
	if acc := c.FindAccountForType("", CLITypeClaude); acc != nil {
		t.Errorf("empty name claude: expected nil (no implicit default), got %q", acc.Name)
	}
	if acc := c.FindAccountForType("", CLITypeCodex); acc != nil {
		t.Errorf("empty name codex: expected nil, got %q", acc.Name)
	}

	// Explicit, correct binding resolves.
	if acc := c.FindAccountForType("hailing", CLITypeClaude); acc == nil || acc.Name != "hailing" {
		t.Errorf("explicit hailing: expected hailing, got %+v", acc)
	}

	// Explicit name with a mismatched type does not resolve.
	if acc := c.FindAccountForType("hailing", CLITypeCodex); acc != nil {
		t.Errorf("cross-type hailing: expected nil, got %q", acc.Name)
	}
}

func TestGuardrailsDefaultsAndExplicitZeroDisable(t *testing.T) {
	base := `
users:
  default_home_root: /tmp/x
`
	cfg, err := Load(writeTempConfig(t, base))
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if cfg.Guardrails.MaxModelCallsPerTurn != 50 || cfg.Guardrails.MaxToolCallsPerTurn != 100 || cfg.Guardrails.MaxCostUSDPerTurn != 0 {
		t.Fatalf("guardrail defaults = %+v", cfg.Guardrails)
	}

	disabled, err := Load(writeTempConfig(t, `
guardrails:
  max_model_calls_per_turn: 0
  max_tool_calls_per_turn: 0
  max_cost_usd_per_turn: 0
`+base))
	if err != nil {
		t.Fatalf("Load explicit zeros: %v", err)
	}
	if disabled.Guardrails.MaxModelCallsPerTurn != 0 || disabled.Guardrails.MaxToolCallsPerTurn != 0 || disabled.Guardrails.MaxCostUSDPerTurn != 0 {
		t.Fatalf("explicit zero thresholds were not preserved: %+v", disabled.Guardrails)
	}
}

// model_calls_extension, tool_calls_extension and confirmation_timeout date
// from when guardrails paused a task for approval. Deployed configs still
// carry them, so they must load — ignored, whatever their value — and must
// not disturb the thresholds around them.
func TestGuardrailsIgnoreRemovedKeys(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
guardrails:
  max_model_calls_per_turn: 70
  model_calls_extension: -1
  max_tool_calls_per_turn: 120
  tool_calls_extension: 20
  confirmation_timeout: -1s
users:
  default_home_root: /tmp/x
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Guardrails.MaxModelCallsPerTurn != 70 || cfg.Guardrails.MaxToolCallsPerTurn != 120 {
		t.Fatalf("guardrails = %+v", cfg.Guardrails)
	}
}

func TestGuardrailsRejectNegativeValues(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "model calls", line: "max_model_calls_per_turn: -1", want: "max_model_calls_per_turn"},
		{name: "tool calls", line: "max_tool_calls_per_turn: -1", want: "max_tool_calls_per_turn"},
		{name: "cost", line: "max_cost_usd_per_turn: -0.01", want: "max_cost_usd_per_turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, "guardrails:\n  "+tt.line+`
users:
  default_home_root: /tmp/x
`)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("negative %s validation error = %v", tt.name, err)
			}
		})
	}
}

// Load reports only the first problem it finds, so the order checks run in is
// what an operator with several mistakes sees first. Every case below carries
// at least two errors and pins the one that must win, with its full text.
func TestLoadReportsTheFirstErrorInCheckOrder(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "guardrails before runner timeouts",
			body: "guardrails: {max_model_calls_per_turn: -1}\nrunner_stall_timeout: 2h\nrunner_max_silent_timeout: 1h\n",
			want: "guardrails.max_model_calls_per_turn must be >= 0",
		},
		{
			name: "runner timeouts before ratios",
			body: "runner_stall_timeout: 2h\nrunner_max_silent_timeout: 1h\nauto_compact_ratio: 2\n",
			want: "runner_max_silent_timeout must be >= runner_stall_timeout",
		},
		{
			name: "auto compact before case rotation",
			body: "auto_compact_ratio: 1\ncase_rotate_context_ratio: 1\n",
			want: "auto_compact_ratio must be in [0, 1), got 1 (0 disables auto-compaction)",
		},
		{
			name: "case rotation before home root",
			body: "case_rotate_context_ratio: -0.5\nusers: {default_home_root: relative}\n",
			want: "case_rotate_context_ratio must be in (0, 1), got -0.5 (omit it to use the 0.7 default)",
		},
		{
			name: "home root before sandbox",
			body: "users: {default_home_root: relative}\nsandbox: {enabled: true, type: docker}\n",
			want: `users.default_home_root must be absolute, got "relative"`,
		},
		{
			name: "sandbox before oidc",
			body: "users: {default_home_root: /tmp/x}\nsandbox: {enabled: true, type: docker}\noidc: {enabled: true}\n",
			want: `sandbox.type "docker" not supported (only "noop" and "bwrap" are implemented)`,
		},
		{
			name: "oidc before upgrade",
			body: "users: {default_home_root: /tmp/x}\noidc: {enabled: true}\nupgrade: {service_mode: bogus}\n",
			want: "oidc.issuer is required when oidc.enabled is true",
		},
		{
			name: "upgrade mode before health path and usage",
			body: "users: {default_home_root: /tmp/x}\nupgrade: {service_mode: bogus, health_path: nope}\nusage: {timezone: Not/AZone}\n",
			want: `upgrade.service_mode "bogus" not supported (use user or system)`,
		},
		{
			name: "health path before usage",
			body: "users: {default_home_root: /tmp/x}\nupgrade: {health_path: nope}\nusage: {timezone: Not/AZone}\n",
			want: `upgrade.health_path must start with '/', got "nope"`,
		},
		{
			name: "timezone before context warning ratio",
			body: "users: {default_home_root: /tmp/x}\nusage: {timezone: \"  Not/AZone \", context_warning_ratio: 2}\n",
			want: `usage.timezone "Not/AZone": unknown time zone Not/AZone`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTempConfig(t, tc.body)
			_, err := Load(path)
			want := "validate config " + path + ": " + tc.want
			if err == nil || err.Error() != want {
				t.Fatalf("Load error:\n got %v\nwant %s", err, want)
			}
		})
	}
}

// DAYMUG_CONFIG only applies when the caller passed no path.
func TestResolveConfigPathPrefersCallerThenEnv(t *testing.T) {
	orig := resolveExecutableDir
	resolveExecutableDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { resolveExecutableDir = orig })
	env := func(v string) func(string) string {
		return func(key string) string {
			if key == EnvConfigPath {
				return v
			}
			return ""
		}
	}
	if got := resolveConfigPath("/explicit.yaml", env("/env.yaml")); got != "/explicit.yaml" {
		t.Errorf("caller path: got %q", got)
	}
	if got := resolveConfigPath("", env("/env.yaml")); got != "/env.yaml" {
		t.Errorf("env path: got %q", got)
	}
	if got := resolveConfigPath("", env("")); got != "" {
		t.Errorf("nothing configured: got %q, want empty", got)
	}
}

func TestListenAddrAppliesTheEnvOverride(t *testing.T) {
	cases := []struct {
		name, yamlAddr, env, want string
	}{
		{name: "yaml only", yamlAddr: ":9000", want: ":9000"},
		{name: "env wins", yamlAddr: ":9000", env: "127.0.0.1:9100", want: "127.0.0.1:9100"},
		{name: "both blank stays blank", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Server: ServerConfig{Addr: tc.yamlAddr}}
			cfg.applyEnv(func(key string) string {
				if key == EnvListenAddr {
					return tc.env
				}
				return ""
			})
			if got := cfg.ListenAddr(); got != tc.want {
				t.Errorf("ListenAddr = %q, want %q", got, tc.want)
			}
			// The env override must not rewrite the YAML-sourced value.
			if cfg.Server.Addr != tc.yamlAddr {
				t.Errorf("Server.Addr rewritten to %q", cfg.Server.Addr)
			}
		})
	}
}

func TestLoadReadsDaymugAddr(t *testing.T) {
	path := writeTempConfig(t, "server: {addr: \":9000\"}\nusers: {default_home_root: /tmp/x}\n")
	t.Setenv(EnvListenAddr, ":9200")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.ListenAddr(); got != ":9200" {
		t.Errorf("ListenAddr = %q, want :9200", got)
	}
}

// Validate must accept a Config that Normalize would still change, and leave
// it exactly as it found it.
func TestValidateOnlyReads(t *testing.T) {
	cfg := &Config{
		Users:     UsersConfig{DefaultHomeRoot: "/abs"},
		Providers: []Provider{{Name: "a", Type: CLITypeClaude}},
		Upgrade:   UpgradeConfig{ServiceMode: "user", HealthPath: "/health"},
		Usage:     UsageConfig{Timezone: "UTC"},
	}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	after, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("Validate mutated the config:\nbefore %s\nafter  %s", before, after)
	}
	if cfg.resolvedUsageLoc != nil {
		t.Fatal("Validate resolved the usage location; that is Normalize's job")
	}
}

func TestNormalizeFillsWhatValidateLeaves(t *testing.T) {
	cfg := &Config{
		Users:     UsersConfig{DefaultHomeRoot: "/abs"},
		Providers: []Provider{{Name: " a ", Type: " claude-compatible "}},
		Sandbox:   SandboxConfig{Enabled: true},
		Usage:     UsageConfig{Timezone: " UTC "},
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate after Normalize: %v", err)
	}
	if got := cfg.ProviderSnapshot()[0]; got.Name != "a" || got.Type != CLITypeClaudeCompatible || got.MaxConcurrent != 1 {
		t.Errorf("provider not canonicalised: %+v", got)
	}
	if cfg.Sandbox.Enabled || cfg.Sandbox.Type != SandboxNoop {
		t.Errorf("enabled noop sandbox not switched off: %+v", cfg.Sandbox)
	}
	if cfg.Usage.Timezone != "UTC" || cfg.UsageLocation() != time.UTC {
		t.Errorf("usage timezone = %q / %v", cfg.Usage.Timezone, cfg.UsageLocation())
	}
	if cfg.Upgrade.ServiceMode != "user" || cfg.Upgrade.HealthPath != "/api/health" {
		t.Errorf("upgrade defaults missing: %+v", cfg.Upgrade)
	}
}

// Normalize cannot fail, so a value it cannot resolve must survive for
// Validate to report instead of being silently replaced.
func TestNormalizeLeavesUnresolvableValuesForValidate(t *testing.T) {
	cfg := &Config{Users: UsersConfig{DefaultHomeRoot: "/abs"}, Usage: UsageConfig{Timezone: "Not/AZone"}}
	cfg.Normalize()
	if cfg.Usage.Timezone != "Not/AZone" {
		t.Fatalf("timezone replaced with %q", cfg.Usage.Timezone)
	}
	if cfg.UsageLocation() != time.UTC {
		t.Fatalf("UsageLocation = %v, want the UTC fallback", cfg.UsageLocation())
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `usage.timezone "Not/AZone"`) {
		t.Fatalf("Validate = %v, want the timezone error", err)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	path := writeTempConfig(t, "users: {default_home_root: /tmp/x}\noidc: {enabled: true, issuer: i, client_id: c, client_secret: s, redirect_uri: r}\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first, _ := json.Marshal(cfg)
	cfg.Normalize()
	second, _ := json.Marshal(cfg)
	if string(first) != string(second) {
		t.Fatalf("second Normalize changed the config:\n%s\n%s", first, second)
	}
}

// The provider lock exists from construction, so a Config that never went
// through Load is as safe to hot-swap as one that did.
func TestProviderCacheIsLockedWithoutLoad(t *testing.T) {
	cfg := &Config{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = cfg.ReplaceProviders([]Provider{{Name: fmt.Sprintf("p%d", i), Type: CLITypeClaude}})
		}(i)
		go func() {
			defer wg.Done()
			_ = cfg.ProviderSnapshot()
		}()
	}
	wg.Wait()
	if n := len(cfg.ProviderSnapshot()); n != 1 {
		t.Fatalf("providers = %d, want 1", n)
	}
}

func TestRetentionDefaultsToNeverDelete(t *testing.T) {
	base := `
users:
  default_home_root: /tmp/x
`
	cfg, err := Load(writeTempConfig(t, base))
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if cfg.Retention.InactiveConversations.Duration != 0 || cfg.Retention.DeletedConversations.Duration != 0 || cfg.Retention.Uploads.Duration != 0 {
		t.Fatalf("retention defaults = %+v, want both disabled", cfg.Retention)
	}

	set, err := Load(writeTempConfig(t, `
retention:
  inactive_conversations: 336h
  deleted_conversations: 168h
  uploads: 2160h
`+base))
	if err != nil {
		t.Fatalf("Load retention: %v", err)
	}
	if set.Retention.InactiveConversations.Duration != 14*24*time.Hour || set.Retention.DeletedConversations.Duration != 7*24*time.Hour || set.Retention.Uploads.Duration != 90*24*time.Hour {
		t.Fatalf("configured retention = %+v", set.Retention)
	}
}

// The upgrade preflight re-validates the file the server actually loaded,
// which may come from --config rather than the default location.
func TestLoadRecordsSourcePath(t *testing.T) {
	path := writeTempConfig(t, `
users:
  default_home_root: /tmp/daymug-path
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path() != path {
		t.Fatalf("Path() = %q, want %q", cfg.Path(), path)
	}
	var nilCfg *Config
	if nilCfg.Path() != "" {
		t.Fatal("nil config must report an empty path")
	}
}
