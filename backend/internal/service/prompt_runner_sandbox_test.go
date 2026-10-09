package service_test

import (
	"context"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// Conversations run under an agent row whose own sandbox_mode is always the
// jailed default (no UI ever sets it). The sandbox tier must therefore inherit
// from the owning human, mirroring provider-binding inheritance — otherwise an
// admin who flips their account to "unrestricted" can never lift the jail for
// the agents that actually execute their turns.
func TestBuildRunOptions_SandboxTierInheritsFromOwner(t *testing.T) {
	tests := []struct {
		name        string
		ownerMode   string
		wantUnrestr bool
	}{
		{"owner unrestricted lifts the jail", store.SandboxModeUnrestricted, true},
		{"owner jailed keeps the jail", store.SandboxModeJailed, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSandboxTestStore(t)
			ctx := context.Background()

			if err := s.CreateUser(ctx, store.User{
				ID: "human1", Username: "alice", Name: "alice", SandboxMode: tc.ownerMode,
			}); err != nil {
				t.Fatalf("create human: %v", err)
			}
			// Agent persona: empty username, owner points at the human. Its own
			// sandbox_mode is forced to jailed by CreateUser, as in production.
			if err := s.CreateUser(ctx, store.User{ID: "agent1", OwnerID: "human1"}); err != nil {
				t.Fatalf("create agent: %v", err)
			}
			agent, err := s.GetUser(ctx, "agent1")
			if err != nil {
				t.Fatalf("get agent: %v", err)
			}

			r := &service.PromptRunner{Store: s}
			opts := r.BuildRunOptions(ctx, "", t.TempDir(), &agent, nil)

			if opts.Unrestricted != tc.wantUnrestr {
				t.Fatalf("Unrestricted = %v, want %v (owner mode %q)",
					opts.Unrestricted, tc.wantUnrestr, tc.ownerMode)
			}
		})
	}
}

// An admin owner is a trusted operator: their agents bypass the jail even when
// the per-user sandbox_mode is the default "jailed", so enabling bwrap never
// locks an admin out of their own host without them having to discover and flip
// the unrestricted toggle first.
func TestBuildRunOptions_AdminOwnerBypassesJail(t *testing.T) {
	s := newSandboxTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, store.User{
		ID: "human1", Username: "root", Name: "root", IsAdmin: true,
		SandboxMode: store.SandboxModeJailed, // explicitly jailed; admin still bypasses
	}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent1", OwnerID: "human1"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agent, err := s.GetUser(ctx, "agent1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}

	r := &service.PromptRunner{Store: s}
	opts := r.BuildRunOptions(ctx, "", t.TempDir(), &agent, nil)

	if !opts.Unrestricted {
		t.Fatal("admin-owned agent should bypass the jail, got Unrestricted=false")
	}
}

// An orphan agent (no resolvable owner) must fail safe to its own — always
// jailed — tier rather than leaking the host filesystem.
func TestBuildRunOptions_OrphanAgentStaysJailed(t *testing.T) {
	s := newSandboxTestStore(t)
	ctx := context.Background()

	if err := s.CreateUser(ctx, store.User{ID: "agent1"}); err != nil { // no owner_id
		t.Fatalf("create agent: %v", err)
	}
	agent, err := s.GetUser(ctx, "agent1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}

	r := &service.PromptRunner{Store: s}
	opts := r.BuildRunOptions(ctx, "", t.TempDir(), &agent, nil)

	if opts.Unrestricted {
		t.Fatal("orphan agent should stay jailed, got Unrestricted=true")
	}
}

func TestBuildRunOptions_UserEnvOverridesProviderEnv(t *testing.T) {
	s := newSandboxTestStore(t)
	ctx := context.Background()

	owner := store.User{
		ID:       "human1",
		Username: "alice",
		Name:     "alice",
		Env:      "GH_TOKEN=user-token\nGLAB_CONFIG_DIR=/home/alice/.config/glab-cli",
	}
	if err := s.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent1", OwnerID: "human1"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agent, err := s.GetUser(ctx, "agent1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}

	r := &service.PromptRunner{Store: s}
	opts := r.BuildRunOptions(ctx, "", t.TempDir(), &agent, &config.Provider{
		Name: "shared",
		Type: config.CLITypeCodex,
		Env: map[string]string{
			"GH_TOKEN": "provider-token",
		},
	})

	if got := opts.AccountEnv["GH_TOKEN"]; got != "user-token" {
		t.Fatalf("GH_TOKEN = %q, want user-token", got)
	}
	if got := opts.AccountEnv["GLAB_CONFIG_DIR"]; got != "/home/alice/.config/glab-cli" {
		t.Fatalf("GLAB_CONFIG_DIR = %q", got)
	}
}

func newSandboxTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
