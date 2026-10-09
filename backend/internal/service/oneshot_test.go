package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

type stubSandbox struct{}

func (stubSandbox) Wrap(argv []string, _ string, _ agent.WrapOpts) ([]string, []string, error) {
	return argv, nil, nil
}

// The security knobs are constructor arguments precisely so they cannot be
// forgotten; this pins that they actually land on the request.
func TestNewOneshotRunRequestCarriesSecurityKnobs(t *testing.T) {
	cfg := &config.Config{
		RunnerStallTimeout:     config.Duration{Duration: 7 * time.Minute},
		RunnerMaxSilentTimeout: config.Duration{Duration: 3 * time.Hour},
	}
	opts, cleanup := NewOneshotRunRequest(context.Background(), nil, store.User{ID: "u1"}, nil, cfg,
		stubSandbox{}, "/srv/jail", false)
	defer cleanup()

	if _, ok := opts.Sandbox.(stubSandbox); !ok {
		t.Errorf("Sandbox = %#v, want the passed sandbox", opts.Sandbox)
	}
	if opts.JailRoot != "/srv/jail" {
		t.Errorf("JailRoot = %q", opts.JailRoot)
	}
	if opts.StallTimeout != 7*time.Minute {
		t.Errorf("StallTimeout = %v", opts.StallTimeout)
	}
	if opts.MaxSilentTimeout != 3*time.Hour {
		t.Errorf("MaxSilentTimeout = %v", opts.MaxSilentTimeout)
	}
	if _, ok := opts.Spawner.(*agent.PtySpawner); !ok {
		t.Errorf("Spawner = %T, want *agent.PtySpawner", opts.Spawner)
	}
}

func TestSandboxUnrestrictedFor(t *testing.T) {
	admin := store.User{ID: "u1", Username: "root", IsAdmin: true}
	trusted := store.User{ID: "u2", Username: "bob", SandboxMode: store.SandboxModeUnrestricted}
	plain := store.User{ID: "u3", Username: "carol", SandboxMode: store.SandboxModeJailed}
	orphan := store.User{ID: "a1"} // agent row with no resolvable owner

	ms := storetest.New()
	ms.Users = []store.User{admin, trusted, plain}

	tests := []struct {
		name  string
		store store.Store
		user  store.User
		want  bool
	}{
		{name: "admin bypasses the jail", store: ms, user: admin, want: true},
		{name: "explicit unrestricted tier", store: ms, user: trusted, want: true},
		{name: "jailed tier stays jailed", store: ms, user: plain, want: false},
		{name: "orphan agent fails safe", store: ms, user: orphan, want: false},
		{name: "nil store fails safe", store: nil, user: admin, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SandboxUnrestrictedFor(context.Background(), tc.store, tc.user); got != tc.want {
				t.Fatalf("SandboxUnrestrictedFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// sessionProbeBackend answers the resume probe and declares whether it picks
// its own session ids.
type sessionProbeBackend struct {
	compactBackend
	assigns bool
	exists  bool
	probed  string
}

func (b *sessionProbeBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{AssignsSessionID: b.assigns}
}
func (b *sessionProbeBackend) SessionExists(_, _, configDir string) bool {
	b.probed = configDir
	return b.exists
}

func TestPrepareRunSession(t *testing.T) {
	tests := []struct {
		name       string
		assigns    bool
		exists     bool
		sessionID  string
		wantMinted bool
		wantEmpty  bool
		wantResume bool
	}{
		{name: "new session on a caller-id backend is minted", wantMinted: true},
		{name: "new session on a self-assigning backend stays empty", assigns: true, wantEmpty: true},
		{name: "existing session on disk resumes", sessionID: "s1", exists: true, wantResume: true},
		{name: "existing id without a log starts fresh under it", sessionID: "s1"},
		{name: "self-assigning backend still resumes a known id", assigns: true, sessionID: "t1", exists: true, wantResume: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &sessionProbeBackend{assigns: tt.assigns, exists: tt.exists}
			opts := agent.RunRequest{ConfigDir: "/cfg/acc1"}
			got := PrepareRunSession(backend, "/work", tt.sessionID, &opts)
			switch {
			case tt.wantEmpty:
				if got != "" || opts.SessionID != "" {
					t.Fatalf("session = %q, want none minted", got)
				}
			case tt.wantMinted:
				if got == "" || got == tt.sessionID || opts.SessionID != got {
					t.Fatalf("session = %q (opts %q), want a freshly minted id", got, opts.SessionID)
				}
			default:
				if got != tt.sessionID || opts.SessionID != tt.sessionID {
					t.Fatalf("session = %q, want %q kept", got, tt.sessionID)
				}
			}
			if opts.IsResume != tt.wantResume {
				t.Fatalf("IsResume = %v, want %v", opts.IsResume, tt.wantResume)
			}
			if got != "" && backend.probed != "/cfg/acc1" {
				t.Fatalf("resume probed under %q, want the account's config dir", backend.probed)
			}
		})
	}
}

// Every run inherits its owner's self-service environment on top of the
// account's, whichever transport builds the request — the agent row's own
// stored copy may be stale.
func TestNewOneshotRunRequestUsesOwnerEnvironment(t *testing.T) {
	ms := storetest.New()
	owner := store.User{ID: "human1", Username: "alice", Email: "alice@example.com", Env: "GH_TOKEN=owner-token"}
	agentRow := store.User{ID: "agent1", OwnerID: "human1", Email: "alice@example.com", Env: "GH_TOKEN=stale"}
	ms.Users = []store.User{owner, agentRow}
	account := &config.Provider{Name: "acc1", Type: config.CLITypeClaude, Env: map[string]string{
		"GH_TOKEN": "account-token", "ANTHROPIC_BASE_URL": "https://example.invalid",
	}}

	opts, cleanup := NewOneshotRunRequest(context.Background(), ms, agentRow, account, nil, nil, "/w", false)
	defer cleanup()

	if got := opts.AccountEnv["GH_TOKEN"]; got != "owner-token" {
		t.Fatalf("GH_TOKEN = %q, want the owner's value over the account's and the stale row's", got)
	}
	if got := opts.AccountEnv["ANTHROPIC_BASE_URL"]; got != "https://example.invalid" {
		t.Fatalf("account env lost: ANTHROPIC_BASE_URL = %q", got)
	}
}

// The browser path builds on the same base, so it carries the same security
// knobs as IM.
func TestBuildRunOptionsCarriesSecurityKnobs(t *testing.T) {
	cfg := &config.Config{
		RunnerStallTimeout:     config.Duration{Duration: 7 * time.Minute},
		RunnerMaxSilentTimeout: config.Duration{Duration: 3 * time.Hour},
	}
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: config.CLITypeClaude, SessionID: "s1"}}
	backend := &sessionProbeBackend{exists: true}
	runner := &PromptRunner{Store: ms, Cfg: cfg, Sandbox: stubSandbox{}, Backends: NewBackendRegistry(nil, backend)}

	opts := runner.BuildRunOptions(context.Background(), "c1", "/srv/conv", &store.User{ID: "u1"},
		&config.Provider{Name: "acc1", Type: config.CLITypeClaude, ConfigDir: "/cfg/acc1"})

	if _, ok := opts.Sandbox.(stubSandbox); !ok {
		t.Errorf("Sandbox = %#v, want the runner's sandbox", opts.Sandbox)
	}
	if opts.JailRoot != "/srv/conv" || opts.Unrestricted {
		t.Errorf("JailRoot = %q Unrestricted = %v, want the conversation dir, jailed", opts.JailRoot, opts.Unrestricted)
	}
	if opts.StallTimeout != 7*time.Minute || opts.MaxSilentTimeout != 3*time.Hour {
		t.Errorf("watchdogs = %v / %v", opts.StallTimeout, opts.MaxSilentTimeout)
	}
	if opts.ConfigDir != "/cfg/acc1" || opts.SessionID != "s1" || !opts.IsResume {
		t.Errorf("account/session = %q %q resume=%v", opts.ConfigDir, opts.SessionID, opts.IsResume)
	}
	if !strings.Contains(opts.SystemPrompt, WebArtifactSystemPrompt) {
		t.Error("web artifact prompt missing")
	}
}

func TestNewOneshotRunRequestUserEnvOverridesProviderEnv(t *testing.T) {
	user := store.User{ID: "u1", Env: "SHARED=user\nGLAB_CONFIG_DIR=/home/alice/glab"}
	provider := &config.Provider{Env: map[string]string{
		"SHARED":        "provider",
		"PROVIDER_ONLY": "kept",
	}}

	opts, cleanup := NewOneshotRunRequest(context.Background(), nil, user, provider, nil, nil, "", false)
	defer cleanup()

	if got := opts.AccountEnv["SHARED"]; got != "user" {
		t.Fatalf("SHARED = %q, want user", got)
	}
	if got := opts.AccountEnv["GLAB_CONFIG_DIR"]; got != "/home/alice/glab" {
		t.Fatalf("GLAB_CONFIG_DIR = %q", got)
	}
	if got := opts.AccountEnv["PROVIDER_ONLY"]; got != "kept" {
		t.Fatalf("PROVIDER_ONLY = %q, want kept", got)
	}
}
