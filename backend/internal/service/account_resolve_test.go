package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func TestResolveConversationAccount(t *testing.T) {
	user := store.User{
		ProviderBindings: map[string]string{"claude": "default", "codex": "codex-main"},
		ProviderAccounts: map[string][]string{
			"claude": {"default", "ollama"},
			"codex":  {"codex-main"},
		},
	}
	tests := []struct {
		name     string
		provider string
		pinned   string
		want     string
		wantErr  bool
	}{
		{"unpinned falls back to default", "claude", "", "default", false},
		{"pinned non-default account in set is honoured", "claude", "ollama", "ollama", false},
		{"pinned default account is honoured", "claude", "default", "default", false},
		{"pinned account no longer in set errors (no fallback)", "claude", "revoked", "", true},
		{"unpinned other type uses its default", "codex", "", "codex-main", false},
		{"unknown type resolves to empty", "claude-compatible", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveConversationAccount(user, tt.provider, tt.pinned)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveConversationAccount(%q, %q) err = %v, wantErr %v", tt.provider, tt.pinned, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ResolveConversationAccount(%q, %q) = %q, want %q", tt.provider, tt.pinned, got, tt.want)
			}
		})
	}
}

// A conversation pinned to an account that the user never had (or had revoked)
// must fail loudly rather than silently re-home onto the user's default — the
// history and rate-limit live on the pinned account.
func TestResolveConversationAccountRevokedPinDoesNotFallBack(t *testing.T) {
	user := store.User{
		ProviderBindings: map[string]string{"claude": "account-b"},
		ProviderAccounts: map[string][]string{"claude": {"account-b"}},
	}
	got, err := ResolveConversationAccount(user, "claude", "account-a")
	if err == nil {
		t.Fatalf("expected error for revoked pin, got account %q", got)
	}
	if got != "" {
		t.Errorf("revoked pin should not resolve to any account, got %q", got)
	}
}

func TestResolveConversationAccountNoBindings(t *testing.T) {
	// Unbound user with no account set for the type: a stale pin still errors
	// because the account is not in the (empty) allowed set.
	if got, err := ResolveConversationAccount(store.User{}, "claude", "ollama"); err == nil {
		t.Errorf("stale pin on unbound user should error, got %q", got)
	}
	// Unpinned unbound user resolves to empty without error (caller surfaces
	// "no account bound").
	if got, err := ResolveConversationAccount(store.User{}, "claude", ""); err != nil || got != "" {
		t.Errorf("unpinned unbound user should resolve to empty, got %q err %v", got, err)
	}
}

func TestResolveRunAccount(t *testing.T) {
	pool := NewPool(&config.Config{Providers: []config.Provider{
		{Name: "acc1", Type: config.CLITypeClaude, ConfigDir: "/cfg/acc1"},
		{Name: "codex-main", Type: config.CLITypeCodex},
	}})
	user := store.User{
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1", config.CLITypeCodex: "gone"},
		ProviderAccounts: map[string][]string{config.CLITypeClaude: {"acc1"}},
	}
	tests := []struct {
		name     string
		pool     *Pool
		provider string
		pinned   string
		want     string
		wantKind error
		wantText string
	}{
		{name: "binding resolves", pool: pool, provider: config.CLITypeClaude, want: "acc1"},
		{name: "pin resolves", pool: pool, provider: config.CLITypeClaude, pinned: "acc1", want: "acc1"},
		{name: "revoked pin", pool: pool, provider: config.CLITypeClaude, pinned: "old",
			wantKind: ErrRunAccountRevoked, wantText: `bound to the "claude" account "old", which is no longer available`},
		{name: "unbound", pool: pool, provider: config.CLITypeClaudeCompatible,
			wantKind: ErrRunAccountUnbound, wantText: `no account bound for "claude-compatible": ask an administrator to bind a provider to your account`},
		{name: "stale binding", pool: pool, provider: config.CLITypeCodex,
			wantKind: ErrRunAccountUnresolvable, wantText: "provider binding cannot be resolved: provider account not found"},
		{name: "no pool", pool: nil, provider: config.CLITypeClaude,
			wantKind: ErrRunAccountUnresolvable, wantText: "no account pool wired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acc, err := ResolveRunAccount(tt.pool, user, tt.provider, tt.pinned)
			if tt.wantKind == nil {
				if err != nil || acc == nil || acc.Name != tt.want {
					t.Fatalf("ResolveRunAccount = %+v, %v; want %q", acc, err, tt.want)
				}
				return
			}
			var runErr *RunAccountError
			if acc != nil || !errors.As(err, &runErr) || !errors.Is(err, tt.wantKind) {
				t.Fatalf("ResolveRunAccount = %+v, %v; want a %v RunAccountError", acc, err, tt.wantKind)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("message %q, want it to contain %q", err.Error(), tt.wantText)
			}
		})
	}
}
