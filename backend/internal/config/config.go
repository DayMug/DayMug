package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
)

// ErrNoConfigPath is returned by Load when no config can be located. The
// resolution order is: explicit path argument → DAYMUG_CONFIG env →
// <binary_dir>/config.yaml. If none of those resolve to a readable file the
// operator gets this error so we never silently boot against a stale or
// unintended location.
var ErrNoConfigPath = fmt.Errorf("config path is required: pass --config <path>, set DAYMUG_CONFIG, or place config.yaml next to the binary")

// DefaultConfigFilename is the filename Load looks for next to the running
// binary when neither a CLI flag nor DAYMUG_CONFIG points elsewhere.
// `daymug bootstrap` lays the binary out so that this exact path resolves.
const DefaultConfigFilename = "config.yaml"

// DefaultUserMaxConcurrent is the process-wide fallback for the number of
// tasks one user may run at the same time across all provider accounts.
const DefaultUserMaxConcurrent = 4

const (
	DefaultMaxModelCallsPerTurn = 50
	DefaultMaxToolCallsPerTurn  = 100
)

// DefaultDBPath returns the SQLite path the server uses for its
// `database.db` file. Hard-coded relative to the running binary so a
// production install (binary at ~/.daymug/daymug) always points at
// ~/.daymug/data/database.db, no env var or YAML field required.
//
// Falls back to a bare relative path if os.Executable() fails — only
// possible on exotic platforms; the SQLite open will surface a friendly
// error from the wrong cwd.
func DefaultDBPath() string {
	if dir, err := resolveExecutableDir(); err == nil {
		return filepath.Join(dir, "data", "database.db")
	}
	return filepath.Join("data", "database.db")
}

// DefaultLogPath returns the file the server mirrors its own log to, laid out
// like DefaultDBPath so a production install writes ~/.daymug/logs/daymug.log.
//
// The process also logs to stderr, which is where a service manager collects
// it; this file exists because that collection is not guaranteed to survive.
// On the reference deployment journald silently stopped persisting and took a
// fortnight of IM failures with it, so the one durable copy is DayMug's own.
func DefaultLogPath() string {
	if dir, err := resolveExecutableDir(); err == nil {
		return filepath.Join(dir, "logs", "daymug.log")
	}
	return filepath.Join("logs", "daymug.log")
}

// DefaultPIDPath returns the file `daymug serve` records its pid in, laid out
// like DefaultDBPath so `daymug stop` run from the same binary finds the
// instance that binary started — and only that one, which is what keeps a
// throwaway copy under /tmp from ever addressing the production install.
func DefaultPIDPath() string {
	if dir, err := resolveExecutableDir(); err == nil {
		return filepath.Join(dir, "data", "daymug.pid")
	}
	return filepath.Join("data", "daymug.pid")
}

// resolveExecutableDir returns the directory holding the currently-running
// binary. Indirected so tests can point Load at a temp dir without touching
// the real test binary's location.
var resolveExecutableDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// Config is the parsed YAML configuration that drives the server. Loaded once
// at startup; never reloaded — except the provider cache, which admin edits
// hot-swap through ReplaceProviders.
//
// Load builds it in four stages, each with one job:
//
//	parse     defaultConfig() overlaid with the YAML file
//	applyEnv  the DAYMUG_* process-environment overrides
//	Normalize fill defaults, canonicalise spellings (never fails)
//	Validate  report the first broken invariant (never writes)
//
// A Config built by hand (tests) must run Normalize before Validate, exactly
// like Load does.
type Config struct {
	// sourcePath is the file Load parsed; see Path.
	sourcePath string

	Server ServerConfig `yaml:"server"`
	Auth   AuthConfig   `yaml:"auth"`
	Admin  AdminConfig  `yaml:"admin"`
	Users  UsersConfig  `yaml:"users"`
	// RunnerStallTimeout is the stdout silence window before the runner
	// probes child-process activity. The probe avoids killing healthy
	// long-running tool/sub-agent work that is quiet on stdout.
	RunnerStallTimeout Duration `yaml:"runner_stall_timeout"`
	// RunnerMaxSilentTimeout caps one stdout-silent stretch even when the
	// activity probe keeps extending RunnerStallTimeout.
	RunnerMaxSilentTimeout Duration `yaml:"runner_max_silent_timeout"`
	// IMRunTimeout optionally caps the total execution time of one IM-triggered
	// agent turn. Zero disables the wall-clock cap so healthy long-running tasks
	// are governed by the activity watchdog and explicit cancellation instead.
	IMRunTimeout Duration `yaml:"im_run_timeout"`
	// AutoCompactRatio triggers /compact automatically once a finished turn
	// leaves the session's context window at or above this fraction (used /
	// total). 0 — the default — keeps compaction manual.
	//
	// Why a ratio and not an absolute token count: the threshold has to hold
	// across backends whose windows differ by an order of magnitude (a 200k
	// claude session and a 1M codex session should compact at the same
	// *relative* pressure, not the same absolute one).
	//
	// The cost this exists to cut is cache_read: every tool step inside a turn
	// re-reads the whole resident context, so a session sitting at 500k tokens
	// pays that on each step. Measured on the production agent pair, ~88% of
	// spend was cache_read at a steady ~11M tokens per turn regardless of how
	// the agents were told to search — rotating the session is the only lever
	// that moves it.
	AutoCompactRatio float64 `yaml:"auto_compact_ratio"`
	// CaseRotateContextRatio is the fraction of the model's context window at
	// which a case-mode IM thread retires the session behind it and assembles
	// the next turn fresh from the case. Empty/0 falls back to
	// DefaultCaseRotateContextRatio.
	//
	// Deliberately not zero-disableable, unlike AutoCompactRatio: a case-mode
	// thread that never rotates is the failure this knob exists to tune, not a
	// mode anyone would choose. Turning rotation off is done by turning case
	// mode off on the agent.
	//
	// Lower is cheaper but not free. Rotation is what stops per-turn cache_read
	// from scaling with turn count, so pushing it down cuts the dominant cost;
	// push it too far and the agent no longer has the working context it needs
	// to write a decent case before being cut off, and the case is all the next
	// session gets. 0.70 is the shipped compromise — see
	// imbridge.caseRotateContextRatio for why it isn't 0.9.
	CaseRotateContextRatio float64 `yaml:"case_rotate_context_ratio"`
	// Providers is the runtime provider cache, loaded from SQLite at startup
	// and edited from the admin UI; it never comes from YAML. All production
	// readers use ProviderSnapshot so admin edits can hot-swap the cache
	// without racing active requests.
	Providers []Provider `yaml:"-"`
	// providersMu guards Providers. A value rather than a pointer so that
	// every Config — Load's, or a test's struct literal — has a usable lock
	// from the moment it exists; nothing has to create it later.
	providersMu sync.RWMutex
	Sandbox     SandboxConfig    `yaml:"sandbox"`
	OIDC        OIDCConfig       `yaml:"oidc"`
	Upgrade     UpgradeConfig    `yaml:"upgrade"`
	Usage       UsageConfig      `yaml:"usage"`
	Guardrails  GuardrailsConfig `yaml:"guardrails"`
	Retention   RetentionConfig  `yaml:"retention"`

	// resolvedUsageLoc is the parsed Usage.Timezone. Normalize populates it so
	// the request path doesn't re-parse the IANA name on every call. Read via
	// UsageLocation() — the field stays unexported so it's never YAML-bound.
	resolvedUsageLoc *time.Location

	// listenAddrEnv is DAYMUG_ADDR as applyEnv saw it. Kept apart from
	// Server.Addr rather than written over it so the YAML value stays what the
	// file says. Read via ListenAddr().
	listenAddrEnv string
}

// Environment variables DayMug reads for itself. Load is the only reader:
// EnvConfigPath picks which file to parse, EnvListenAddr is applied on top of
// what that file says.
const (
	EnvConfigPath = "DAYMUG_CONFIG"
	EnvListenAddr = "DAYMUG_ADDR"
)

// GuardrailsConfig sets the soft-reminder thresholds for one top-level user
// input. A task is never interrupted: crossing a threshold schedules a notice
// for the next user message. Zero disables an individual threshold.
type GuardrailsConfig struct {
	MaxModelCallsPerTurn int     `yaml:"max_model_calls_per_turn"`
	MaxToolCallsPerTurn  int     `yaml:"max_tool_calls_per_turn"`
	MaxCostUSDPerTurn    float64 `yaml:"max_cost_usd_per_turn"`
}

// UsageConfig tunes the per-user/per-day token usage rollup. The day
// boundary used by `token_usage` rows and the default range in the
// settings UI are computed in this timezone. IANA names ("Asia/Shanghai",
// "America/New_York", "Europe/Berlin"), or "UTC" / "Local". Empty falls
// back to defaultUsageTimezone.
//
// Switching the timezone after data exists doesn't migrate older rows;
// they keep the day label they were stamped with on the day they
// landed. Operators changing this value should expect a one-day overlap
// of differently-bucketed rows in the chart.
type UsageConfig struct {
	Timezone             string  `yaml:"timezone"`
	HighModelRequests    int64   `yaml:"high_model_requests"`
	HighToolCalls        int64   `yaml:"high_tool_calls"`
	HighConversationCost float64 `yaml:"high_conversation_cost_usd"`
	ContextWarningRatio  float64 `yaml:"context_warning_ratio"`
}

// UsageLocation returns the resolved *time.Location for usage rollups.
// Never nil — callers don't have to nil-check.
func (c *Config) UsageLocation() *time.Location {
	if c.resolvedUsageLoc != nil {
		return c.resolvedUsageLoc
	}
	return time.UTC
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
	// PublicURL is the externally-reachable base URL users open the app at,
	// e.g. "https://daymug.example.com". Used to build absolute deep links in
	// outbound notifications (tapping a Bark push opens the conversation in the
	// browser). Optional: when empty, notifications omit the link and behave as
	// before. Any trailing slash is tolerated — link builders trim it.
	PublicURL string `yaml:"public_url"`
}

// ListenAddr is the address `serve` binds and the upgrade watchdog probes:
// DAYMUG_ADDR when it was set at Load, otherwise server.addr. It is empty
// only when the YAML blanks server.addr and the variable is unset; each
// caller keeps its own fallback for that case.
func (c *Config) ListenAddr() string {
	if c.listenAddrEnv != "" {
		return c.listenAddrEnv
	}
	return c.Server.Addr
}

type AuthConfig struct {
	SessionTTL           Duration `yaml:"session_ttl"`
	CookieSecure         bool     `yaml:"cookie_secure"`
	PasswordLoginEnabled bool     `yaml:"password_login_enabled"`
}

// RetentionConfig opts into automatic deletion of old data. Every field
// defaults to zero, which disables that deletion: nothing is erased unless the
// operator sets a duration.
type RetentionConfig struct {
	// InactiveConversations is how long a conversation may go without any
	// update before the background sweep deletes it (soft delete, as if the
	// owner had). Pinned conversations are kept.
	InactiveConversations Duration `yaml:"inactive_conversations"`
	// DeletedConversations is how long users, agents, and conversations stay
	// soft-deleted before the background sweep erases them and their messages.
	DeletedConversations Duration `yaml:"deleted_conversations"`
	// Uploads is the age past which files in an uploads directory (web and IM
	// attachments) are removed the next time that directory is written to.
	Uploads Duration `yaml:"uploads"`
}

type AdminConfig struct {
	BootstrapUsernames []string `yaml:"bootstrap_usernames"`
}

type UsersConfig struct {
	DefaultHomeRoot string `yaml:"default_home_root"`
	MaxConcurrent   int    `yaml:"max_concurrent"`
}

// Provider represents one database-backed credential pool. The name is
// historical: each entry is a login for one of the four CLI types, and the
// Type field discriminates which backend receives this account. The two
// Claude-family types run through Claude Code, so ConfigDir maps to
// CLAUDE_CONFIG_DIR for either; the two Codex-family types map ConfigDir to
// CODEX_HOME. A `*-compatible` entry points its transport at a third-party
// endpoint purely through Env (ANTHROPIC_BASE_URL / OPENAI_BASE_URL and the
// matching key), so nothing else in the pipeline has to know about it.
type Provider struct {
	Name string `json:"name"`
	// Type picks which CLI this account is bound to; one of SupportedCLITypes.
	Type          string `json:"type"`
	MaxConcurrent int    `json:"max_concurrent"`
	// ConfigDir is the on-disk credentials directory exported to the
	// child process as CLAUDE_CONFIG_DIR (Claude family) or CODEX_HOME
	// (Codex family). Empty is permitted for the Claude family (falls back
	// to the OS user's ~/.claude); the Codex family requires an explicit
	// path.
	ConfigDir string            `json:"config_dir"`
	Env       map[string]string `json:"env"`
}

// SandboxConfig selects the per-process isolation layer wrapped around each
// claude invocation. Today only the no-op implementation ships, so enabling
// the sandbox is effectively a no-op as well — the struct and the Sandbox
// interface are preserved so a real isolation backend can be added later
// without churning every caller.
type SandboxConfig struct {
	Enabled bool   `yaml:"enabled"`
	Type    string `yaml:"type"`
	// Network controls whether jailed agents keep host network access. The
	// bwrap sandbox shares the host net namespace when true and unshares it
	// (offline) when false. Only meaningful for Type == bwrap. Defaults to true:
	// agents need the network to reach their own model API, so an omitted key
	// must not silently cut them off.
	Network bool `yaml:"network"`
	// ExtraROBinds / ExtraRWBinds are additional host paths every jailed agent
	// may read (RO) or read-write (RW) on top of its work_dir — e.g. a shared
	// assets dir. Absolute host paths; ignored by the noop sandbox.
	ExtraROBinds []string `yaml:"extra_ro_binds"`
	ExtraRWBinds []string `yaml:"extra_rw_binds"`
}

// UpgradeConfig tunes the always-on self-upgrade flow. The manifest URL
// itself is hard-coded in service.DefaultManifestURL — operators never
// need to point this at anything; only service-runtime knobs live here.
//
// Service is the systemd unit name (default "daymug") and ServiceMode
// is "user" or "system" — the watchdog uses these to restart the
// running service after swapping the binary. HealthPath is the URL
// path the post-upgrade watchdog polls (default "/api/health"); the
// host:port half is auto-derived from the running server's listen
// address. If the path doesn't return 200 within HealthTimeout the
// watchdog rolls back to the previous binary and restarts again.
type UpgradeConfig struct {
	Service       string   `yaml:"service"`
	ServiceMode   string   `yaml:"service_mode"`
	HealthPath    string   `yaml:"health_path"`
	HealthTimeout Duration `yaml:"health_timeout"`
}

// OIDCConfig drives the optional Casdoor / generic-OIDC login flow. When
// Enabled is false (the default) every other field is ignored and the only
// way to log in is username + password. When Enabled is true, Issuer /
// ClientID / ClientSecret / RedirectURI are mandatory and validated at
// startup.
//
// Required{Groups,Roles,Permissions} implement OR-across-dimensions access
// control: every non-empty list must be matched by at least one entry from
// the user's claim set, OR'd together. All three empty = any authenticated
// IdP user is allowed in (gated only by AutoProvision below).
type OIDCConfig struct {
	Enabled         bool     `yaml:"enabled"`
	Issuer          string   `yaml:"issuer"`
	ClientID        string   `yaml:"client_id"`
	ClientSecret    string   `yaml:"client_secret"`
	RedirectURI     string   `yaml:"redirect_uri"`
	Scopes          []string `yaml:"scopes"`
	GroupClaim      string   `yaml:"group_claim"`
	RoleClaim       string   `yaml:"role_claim"`
	PermissionClaim string   `yaml:"permission_claim"`

	RequiredGroups      []string `yaml:"required_groups"`
	RequiredRoles       []string `yaml:"required_roles"`
	RequiredPermissions []string `yaml:"required_permissions"`

	// AutoProvision controls what happens when a successful OIDC login has
	// no matching local user (matched by email). true = create a fresh
	// non-admin user on the fly; false = deny with "user not provisioned".
	// Default true to mirror the typical "OIDC is the source of truth"
	// model. Admins who want a closed allowlist set this false and create
	// users via the admin UI ahead of time.
	AutoProvision bool `yaml:"auto_provision"`

	// ButtonLabel is rendered on the SSO button in the login page. Empty
	// falls back to "Sign in with SSO".
	ButtonLabel string `yaml:"button_label"`
}

// Duration wraps time.Duration so we can parse YAML strings like "720h".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	if s == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// DefaultUsersHomeRoot is the fallback for users.default_home_root when the
// YAML omits it. The path is the literal string fed through expandHomePath
// at validate time, so it lands under the server user's $HOME.
const DefaultUsersHomeRoot = "~/.daymug/users"

// SandboxNoop disables the sandbox layer entirely (claude runs as the server's
// OS user with no extra isolation).
const SandboxNoop = "noop"

// SandboxBwrap wraps each jailed agent invocation in bubblewrap (bwrap),
// confining its filesystem reach to the user's work_dir. Users whose
// sandbox_mode is "unrestricted" bypass the wrapper. Linux-only; the bwrap
// binary must be on PATH.
const SandboxBwrap = "bwrap"

// CLI type values for a provider. Two of them name a first-party vendor
// login — `claude` (Claude Code via the Agent SDK) and `codex` (the OpenAI
// Codex CLI) — and two name the same two transports pointed at somebody
// else's API-compatible endpoint through the provider's `env:` block:
// `claude-compatible` (Anthropic Messages API) and `openai-compatible`
// (OpenAI Chat/Responses API). The compatible pair keeps the session and
// tool plumbing of its transport while staying a separate type in user
// bindings, the model picker and the price table — an endpoint that bills
// against its own catalog must not inherit the vendor's rates.
const (
	CLITypeClaude           = "claude"
	CLITypeCodex            = "codex"
	CLITypeClaudeCompatible = "claude-compatible"
	CLITypeOpenAICompatible = "openai-compatible"
)

// SupportedCLITypes lists every accepted provider type in the order the
// admin UI presents them: first-party first, compatible endpoints after.
var SupportedCLITypes = []string{
	CLITypeClaude,
	CLITypeCodex,
	CLITypeClaudeCompatible,
	CLITypeOpenAICompatible,
}

// IsCodexFamily reports whether the type runs through the Codex CLI, and so
// maps ConfigDir to CODEX_HOME and assigns its own session id at runtime
// instead of accepting a caller-chosen one.
func IsCodexFamily(cliType string) bool {
	return cliType == CLITypeCodex || cliType == CLITypeOpenAICompatible
}

// DefaultCaseRotateContextRatio is the built-in value for
// Config.CaseRotateContextRatio, applied when the key is absent from YAML.
const DefaultCaseRotateContextRatio = 0.70

// DefaultProvider returns the first entry in Providers — the implicit
// default CLI/account for new conversations whose request omits a provider
// (or whose user has no per-CLI binding yet). Returns ok=false when no
// providers are configured; callers MUST treat that as a hard failure
// (user message refused, no backend registered, etc.) rather than picking
// a baked-in fallback, since silently defaulting to "claude" would mask
// misconfiguration on a codex-only deployment.
//
// The database ordering is therefore load-bearing: the first entry wins.
func (c *Config) DefaultProvider() (Provider, bool) {
	providers := c.ProviderSnapshot()
	if len(providers) == 0 {
		return Provider{}, false
	}
	return providers[0], true
}

// ProviderSnapshot returns a defensive copy of the database-backed provider
// cache. Tests may still initialise Providers directly; production replaces
// it through ReplaceProviders after loading app_settings.
func (c *Config) ProviderSnapshot() []Provider {
	if c == nil {
		return nil
	}
	c.providersMu.RLock()
	defer c.providersMu.RUnlock()
	return cloneProviders(c.Providers)
}

// ReplaceProviders atomically swaps the runtime provider cache after applying
// the same validation used by the admin API.
func (c *Config) ReplaceProviders(providers []Provider) error {
	normalized, err := NormalizeProviders(providers)
	if err != nil {
		return err
	}
	c.providersMu.Lock()
	defer c.providersMu.Unlock()
	c.Providers = cloneProviders(normalized)
	return nil
}

// DefaultProviderType is a convenience for callers that only need the
// provider type (one of SupportedCLITypes) of the implicit default.
// Returns "" when no providers are configured.
func (c *Config) DefaultProviderType() string {
	p, ok := c.DefaultProvider()
	if !ok {
		return ""
	}
	return p.Type
}

// Load reads, normalizes and validates the YAML config at path. Resolution
// order: caller-supplied path → DAYMUG_CONFIG env → <binary_dir>/config.yaml.
// Returns ErrNoConfigPath when none of those point at an existing file so
// the operator sees a clear "no config" failure instead of a confusing
// downstream error.
func Load(path string) (*Config, error) {
	path = resolveConfigPath(path, os.Getenv)
	if path == "" {
		return nil, ErrNoConfigPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := defaultConfig()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyEnv(os.Getenv)
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}
	cfg.sourcePath = path
	return cfg, nil
}

// Path is the file this config was loaded from, or "" for a config built in
// code. The upgrade preflight re-validates this exact file with the incoming
// release, since the running server may have been pointed at it by --config
// or DAYMUG_CONFIG rather than the default location.
func (c *Config) Path() string {
	if c == nil {
		return ""
	}
	return c.sourcePath
}

// resolveConfigPath picks the file Load parses, or "" when there is none.
func resolveConfigPath(path string, getenv func(string) string) string {
	if path != "" {
		return path
	}
	if path = getenv(EnvConfigPath); path != "" {
		return path
	}
	// Fall back to a config.yaml sitting next to the binary. Operators who
	// deploy with `daymug bootstrap` (or any drop-the-binary-and-go flow) get a
	// zero-env-var setup as long as they place the config alongside the
	// executable.
	if dir, err := resolveExecutableDir(); err == nil {
		candidate := filepath.Join(dir, DefaultConfigFilename)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	return ""
}

// applyEnv layers the process-environment overrides onto the parsed YAML.
// getenv is injected so tests don't have to mutate the real environment.
func (c *Config) applyEnv(getenv func(string) string) {
	c.listenAddrEnv = getenv(EnvListenAddr)
}

// defaultConfig returns the baseline that YAML values overlay onto. Anything
// the YAML omits keeps the default; anything the YAML sets wins.
func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Auth: AuthConfig{
			SessionTTL:           Duration{Duration: 30 * 24 * time.Hour},
			CookieSecure:         false,
			PasswordLoginEnabled: true,
		},
		Users: UsersConfig{
			MaxConcurrent: DefaultUserMaxConcurrent,
		},
		Sandbox: SandboxConfig{
			Enabled: false,
			Type:    SandboxNoop,
			Network: true,
		},
		OIDC: OIDCConfig{
			Enabled:         false,
			Scopes:          []string{"openid", "profile", "email"},
			GroupClaim:      "groups",
			RoleClaim:       "roles",
			PermissionClaim: "permissions",
			AutoProvision:   true,
			ButtonLabel:     "Sign in with SSO",
		},
		Upgrade: UpgradeConfig{
			Service:       "daymug",
			ServiceMode:   "user",
			HealthPath:    "/api/health",
			HealthTimeout: Duration{Duration: 60 * time.Second},
		},
		RunnerStallTimeout:     Duration{Duration: 10 * time.Minute},
		RunnerMaxSilentTimeout: Duration{Duration: 2 * time.Hour},
		Guardrails: GuardrailsConfig{
			MaxModelCallsPerTurn: DefaultMaxModelCallsPerTurn,
			MaxToolCallsPerTurn:  DefaultMaxToolCallsPerTurn,
		},
	}
}

// Normalize fills every default the YAML may omit and canonicalises values
// that have more than one accepted spelling. It never fails: a value it cannot
// resolve (an unexpandable "~", an unknown timezone) is left for Validate to
// report, so Validate alone decides which error an operator sees first.
func (c *Config) Normalize() {
	if c.RunnerStallTimeout.Duration <= 0 {
		c.RunnerStallTimeout = Duration{Duration: 10 * time.Minute}
	}
	if c.RunnerMaxSilentTimeout.Duration <= 0 {
		c.RunnerMaxSilentTimeout = Duration{Duration: 2 * time.Hour}
	}
	// 0 means "unset" for this ratio (unlike auto_compact_ratio, where it
	// means "disabled"), so it is filled in rather than rejected.
	if c.CaseRotateContextRatio == 0 {
		c.CaseRotateContextRatio = DefaultCaseRotateContextRatio
	}
	if c.Users.DefaultHomeRoot == "" {
		c.Users.DefaultHomeRoot = DefaultUsersHomeRoot
	}
	if expanded, err := expandHomePath(c.Users.DefaultHomeRoot); err == nil {
		c.Users.DefaultHomeRoot = expanded
	}
	if c.Users.MaxConcurrent <= 0 {
		c.Users.MaxConcurrent = DefaultUserMaxConcurrent
	}
	c.providersMu.Lock()
	c.Providers = canonicalProviders(c.Providers)
	c.providersMu.Unlock()
	// noop is a passthrough; "enabled: true" with no real wrapper isolates
	// nothing. Flip it back to disabled so downstream code doesn't have to
	// special-case it.
	if c.Sandbox.Enabled && (c.Sandbox.Type == "" || c.Sandbox.Type == SandboxNoop) {
		c.Sandbox.Type = SandboxNoop
		c.Sandbox.Enabled = false
	}
	c.OIDC.normalize()
	c.Upgrade.normalize()
	c.Usage.normalize()
	c.resolvedUsageLoc = nil
	if loc, err := time.LoadLocation(c.Usage.Timezone); err == nil {
		c.resolvedUsageLoc = loc
	}
}

// Validate reports the first invariant the Config breaks, checking in a fixed
// order so an operator with several mistakes always sees the same one first.
// It only reads; defaults are Normalize's job and must already be applied.
func (c *Config) Validate() error {
	if c.Guardrails.MaxModelCallsPerTurn < 0 {
		return fmt.Errorf("guardrails.max_model_calls_per_turn must be >= 0")
	}
	if c.Guardrails.MaxToolCallsPerTurn < 0 {
		return fmt.Errorf("guardrails.max_tool_calls_per_turn must be >= 0")
	}
	if c.Guardrails.MaxCostUSDPerTurn < 0 {
		return fmt.Errorf("guardrails.max_cost_usd_per_turn must be >= 0")
	}
	if c.RunnerMaxSilentTimeout.Duration < c.RunnerStallTimeout.Duration {
		return fmt.Errorf("runner_max_silent_timeout must be >= runner_stall_timeout")
	}
	// Reject >=1 rather than clamping: a ratio at or above the full window can
	// never fire, so silently accepting it would present as "auto-compact is
	// configured but never runs" — the hardest kind of misconfiguration to
	// notice. Negative is the same class of mistake. 0 stays valid: it is the
	// documented way to keep compaction manual.
	if c.AutoCompactRatio < 0 || c.AutoCompactRatio >= 1 {
		return fmt.Errorf("auto_compact_ratio must be in [0, 1), got %v (0 disables auto-compaction)", c.AutoCompactRatio)
	}
	// Same reasoning as auto_compact_ratio for rejecting >=1.
	if c.CaseRotateContextRatio < 0 || c.CaseRotateContextRatio >= 1 {
		return fmt.Errorf("case_rotate_context_ratio must be in (0, 1), got %v (omit it to use the %v default)",
			c.CaseRotateContextRatio, DefaultCaseRotateContextRatio)
	}
	// Normalize leaves a "~" it could not expand in place; expanding again
	// surfaces the reason. On an already-expanded path this is a no-op.
	if _, err := expandHomePath(c.Users.DefaultHomeRoot); err != nil {
		return fmt.Errorf("users.default_home_root: %w", err)
	}
	if !filepath.IsAbs(c.Users.DefaultHomeRoot) {
		return fmt.Errorf("users.default_home_root must be absolute, got %q", c.Users.DefaultHomeRoot)
	}
	if err := validateProviders(c.ProviderSnapshot()); err != nil {
		return err
	}
	// Normalize has already turned an enabled noop sandbox off, so an enabled
	// sandbox here must be a real isolation layer. NewSandbox builds the bwrap
	// wrapper and fails fast if the binary is missing.
	if c.Sandbox.Enabled && c.Sandbox.Type != SandboxBwrap {
		return fmt.Errorf("sandbox.type %q not supported (only %q and %q are implemented)", c.Sandbox.Type, SandboxNoop, SandboxBwrap)
	}
	if err := c.OIDC.validate(); err != nil {
		return err
	}
	if err := c.Upgrade.validate(); err != nil {
		return err
	}
	return c.Usage.validate()
}

// NormalizeProviders validates and canonicalises database-backed provider
// accounts. An empty list is valid so a fresh installation can start and let
// its first administrator create the initial account from the web UI.
func NormalizeProviders(providers []Provider) ([]Provider, error) {
	out := canonicalProviders(providers)
	if err := validateProviders(out); err != nil {
		return nil, err
	}
	return out, nil
}

// canonicalProviders returns a copy with whitespace trimmed and
// max_concurrent defaulted to 1.
func canonicalProviders(providers []Provider) []Provider {
	out := cloneProviders(providers)
	for i := range out {
		acc := &out[i]
		acc.Name = strings.TrimSpace(acc.Name)
		acc.Type = strings.TrimSpace(acc.Type)
		acc.ConfigDir = strings.TrimSpace(acc.ConfigDir)
		// The dir reaches the CLI as CLAUDE_CONFIG_DIR / CODEX_HOME, where no
		// shell expands "~": the CLI resolves it against each conversation's
		// cwd while DayMug's resume probe resolves it against its own, so the
		// probe never finds the session log and every second turn fails with
		// "Session ID is already in use".
		if expanded, err := expandHomePath(acc.ConfigDir); err == nil {
			acc.ConfigDir = expanded
		}
		if acc.MaxConcurrent <= 0 {
			acc.MaxConcurrent = 1
		}
	}
	return out
}

// validateProviders checks a canonicalised provider list.
func validateProviders(providers []Provider) error {
	seenAccounts := map[string]string{}
	for i := range providers {
		acc := &providers[i]
		if acc.Name == "" {
			return fmt.Errorf("providers[%d].name is required", i)
		}
		if previousType, dup := seenAccounts[acc.Name]; dup {
			return fmt.Errorf("providers: account name %q must be unique across all provider types (already used by type %q)", acc.Name, previousType)
		}
		seenAccounts[acc.Name] = acc.Type
		switch acc.Type {
		case CLITypeClaude, CLITypeClaudeCompatible:
			// Empty config_dir falls back to the OS user's ~/.claude at
			// runtime for Claude-family providers; preserved for backward
			// compatibility with the very first deployments that pre-date
			// this field.
		case CLITypeCodex, CLITypeOpenAICompatible:
			if acc.ConfigDir == "" {
				return fmt.Errorf("providers[%d] (%q): config_dir is required for type=%q", i, acc.Name, acc.Type)
			}
		case "":
			return fmt.Errorf("providers[%d] (%q): type is required (use one of %s)", i, acc.Name, strings.Join(SupportedCLITypes, ", "))
		default:
			return fmt.Errorf("providers[%d] (%q): type %q not supported (use one of %s)", i, acc.Name, acc.Type, strings.Join(SupportedCLITypes, ", "))
		}
	}
	return nil
}

func cloneProviders(providers []Provider) []Provider {
	out := make([]Provider, len(providers))
	for i, provider := range providers {
		out[i] = provider
		if provider.Env != nil {
			out[i].Env = make(map[string]string, len(provider.Env))
			for key, value := range provider.Env {
				out[i].Env[key] = value
			}
		}
	}
	return out
}

// defaultUsageTimezone is the day-boundary timezone applied when the
// operator leaves usage.timezone unset. UTC+8 (Asia/Shanghai) so usage
// rows bucket and the settings UI labels days against the operators'
// local wall clock rather than UTC, which otherwise splits a single
// working day across two buckets for anyone east of Greenwich.
const defaultUsageTimezone = "Asia/Shanghai"

// normalize defaults the timezone name and the anomaly thresholds. Normalize
// resolves the name into Config.resolvedUsageLoc right after.
func (u *UsageConfig) normalize() {
	u.Timezone = strings.TrimSpace(u.Timezone)
	if u.Timezone == "" {
		u.Timezone = defaultUsageTimezone
	}
	if u.HighModelRequests <= 0 {
		u.HighModelRequests = 20
	}
	if u.HighToolCalls <= 0 {
		u.HighToolCalls = 30
	}
	if u.HighConversationCost <= 0 {
		u.HighConversationCost = 10
	}
	if u.ContextWarningRatio <= 0 {
		u.ContextWarningRatio = 0.8
	}
}

// validate rejects an unrecognised IANA name at startup so the operator
// notices the typo before any usage row gets stamped with the wrong bucket.
func (u *UsageConfig) validate() error {
	if _, err := time.LoadLocation(u.Timezone); err != nil {
		return fmt.Errorf("usage.timezone %q: %w", u.Timezone, err)
	}
	if u.ContextWarningRatio > 1 {
		return fmt.Errorf("usage.context_warning_ratio must be between 0 and 1")
	}
	return nil
}

// normalize applies fallbacks for empty optional fields. The whole block is
// inert while oidc.enabled is false, so it is left untouched then.
func (o *OIDCConfig) normalize() {
	if !o.Enabled {
		return
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{"openid", "profile", "email"}
	}
	if o.GroupClaim == "" {
		o.GroupClaim = "groups"
	}
	if o.RoleClaim == "" {
		o.RoleClaim = "roles"
	}
	if o.PermissionClaim == "" {
		o.PermissionClaim = "permissions"
	}
	if o.ButtonLabel == "" {
		o.ButtonLabel = "Sign in with SSO"
	}
}

// validate enforces the required fields (issuer / client_id / client_secret /
// redirect_uri), only when oidc.enabled is true.
func (o *OIDCConfig) validate() error {
	if !o.Enabled {
		return nil
	}
	if o.Issuer == "" {
		return fmt.Errorf("oidc.issuer is required when oidc.enabled is true")
	}
	if o.ClientID == "" {
		return fmt.Errorf("oidc.client_id is required when oidc.enabled is true")
	}
	if o.ClientSecret == "" {
		return fmt.Errorf("oidc.client_secret is required when oidc.enabled is true")
	}
	if o.RedirectURI == "" {
		return fmt.Errorf("oidc.redirect_uri is required when oidc.enabled is true")
	}
	return nil
}

// normalize fills in the defaults. The upgrade flow is always on (manifest URL
// is hard-coded in service); only knobs that vary by deployment live here.
func (u *UpgradeConfig) normalize() {
	if u.Service == "" {
		u.Service = "daymug"
	}
	if u.ServiceMode == "" {
		u.ServiceMode = "user"
	}
	if u.HealthPath == "" {
		u.HealthPath = "/api/health"
	}
	if u.HealthTimeout.Duration <= 0 {
		u.HealthTimeout = Duration{Duration: 60 * time.Second}
	}
}

func (u *UpgradeConfig) validate() error {
	switch u.ServiceMode {
	case "user", "system":
		// OK.
	default:
		return fmt.Errorf("upgrade.service_mode %q not supported (use user or system)", u.ServiceMode)
	}
	if !strings.HasPrefix(u.HealthPath, "/") {
		return fmt.Errorf("upgrade.health_path must start with '/', got %q", u.HealthPath)
	}
	return nil
}

// InitialBindingProvider picks the account a freshly provisioned user is bound
// to when nobody chose one: the provider named "default" if the registry has
// one, otherwise the first provider in registry order (the
// lowest-numbered entry). ok is false only when no provider is configured.
func (c *Config) InitialBindingProvider() (Provider, bool) {
	providers := c.ProviderSnapshot()
	for _, p := range providers {
		if p.Name == "default" {
			return p, true
		}
	}
	if len(providers) == 0 {
		return Provider{}, false
	}
	return providers[0], true
}

// FindAccount returns the account by name, or the default account when name is
// empty. Returns nil if name is non-empty and no matching account exists.
// Callers that need per-CLI-type resolution should use FindAccountForType.
func (c *Config) FindAccount(name string) *Provider {
	if name == "" {
		provider, ok := c.DefaultProvider()
		if !ok {
			return nil
		}
		return &provider
	}
	for _, provider := range c.ProviderSnapshot() {
		if provider.Name == name {
			return &provider
		}
	}
	return nil
}

// FindAccountForType resolves an account by (name, cliType). The binding is
// mandatory: name is required and must match a provider of the requested
// type. An empty name returns nil — there is deliberately NO implicit
// fallback to the "default" provider (nor to the first provider of the
// type). Every job must run against an explicitly bound account so that an
// unbound user is refused rather than silently funnelled onto the shared
// default ~/.claude login (which is how unbound users previously piled onto
// the default account and tripped its rate limit). Returns nil when name is
// empty or no provider matches the (name, type) pair.
func (c *Config) FindAccountForType(name, cliType string) *Provider {
	if c == nil || name == "" {
		return nil
	}
	for _, provider := range c.ProviderSnapshot() {
		if provider.Name == name && provider.Type == cliType {
			return &provider
		}
	}
	return nil
}

// EnsureDefaultHomeRoot creates the configured root directory if it does not
// exist. Called once at startup; failure is fatal (the operator wanted users
// rooted there but the path is unusable).
func (c *Config) EnsureDefaultHomeRoot() error {
	return os.MkdirAll(c.Users.DefaultHomeRoot, 0o755)
}

// expandHomePath expands a leading "~" or "~/" to the running OS user's home
// directory so config knobs can use shell-style shorthand. Anything that does
// not start with "~" is returned unchanged. Errors only on os.UserHomeDir
// failure (no $HOME and no /etc/passwd entry — exotic).
func expandHomePath(p string) (string, error) {
	if p == "" || (p != "~" && !strings.HasPrefix(p, "~/")) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
