package service

import (
	"slices"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func TestAccountRotatorNextCyclesThroughEveryCandidate(t *testing.T) {
	var r AccountRotator
	candidates := []string{"acc1", "acc2", "acc3"}

	var got []string
	for range 7 {
		got = append(got, r.Next("agent|claude|opus", candidates, nil))
	}

	want := []string{"acc1", "acc2", "acc3", "acc1", "acc2", "acc3", "acc1"}
	if !slices.Equal(got, want) {
		t.Fatalf("rotation = %v, want %v — turns are piling onto one account", got, want)
	}
}

func TestAccountRotatorKeepsSeparateCursorsPerKey(t *testing.T) {
	var r AccountRotator
	candidates := []string{"acc1", "acc2"}

	if got := r.Next("agent|claude|opus", candidates, nil); got != "acc1" {
		t.Fatalf("first opus pick = %q, want acc1", got)
	}
	// A different model must not inherit the opus cursor: its own first run
	// should still start at the head of the list.
	if got := r.Next("agent|claude|fable", candidates, nil); got != "acc1" {
		t.Fatalf("first fable pick = %q, want acc1", got)
	}
	if got := r.Next("agent|claude|opus", candidates, nil); got != "acc2" {
		t.Fatalf("second opus pick = %q, want acc2", got)
	}
}

func TestAccountRotatorSkipsUnusableCandidates(t *testing.T) {
	var r AccountRotator
	candidates := []string{"acc1", "acc2", "acc3"}
	usable := func(account string) bool { return account != "acc2" }

	got := []string{
		r.Next("k", candidates, usable),
		r.Next("k", candidates, usable),
		r.Next("k", candidates, usable),
	}
	want := []string{"acc1", "acc3", "acc1"}
	if !slices.Equal(got, want) {
		t.Fatalf("rotation = %v, want %v — a rate-limited account is still being handed out", got, want)
	}
}

// Every account cooling down is not a reason to return nothing: the caller has
// no other account to try, and the pool's own error is the useful one.
func TestAccountRotatorFallsBackWhenNothingIsUsable(t *testing.T) {
	var r AccountRotator
	candidates := []string{"acc1", "acc2"}
	none := func(string) bool { return false }

	if got := r.Next("k", candidates, none); got != "acc1" {
		t.Fatalf("pick = %q, want acc1", got)
	}
	if got := r.Next("k", candidates, none); got != "acc2" {
		t.Fatalf("pick = %q, want acc2 — the cursor stalled", got)
	}
}

// The candidate list shrinks whenever an admin revokes an account, and the
// stored cursor then points past its end.
func TestAccountRotatorSurvivesShrinkingCandidateList(t *testing.T) {
	var r AccountRotator
	for range 3 {
		r.Next("k", []string{"acc1", "acc2", "acc3"}, nil)
	}
	if got := r.Next("k", []string{"acc1"}, nil); got != "acc1" {
		t.Fatalf("pick = %q, want acc1", got)
	}
}

func TestAccountRotatorNextWithoutCandidates(t *testing.T) {
	var r AccountRotator
	if got := r.Next("k", nil, nil); got != "" {
		t.Fatalf("pick = %q, want empty", got)
	}
}

func TestEligibleAccountsForModel(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "claude-a", Type: config.CLITypeClaude},
		{Name: "claude-b", Type: config.CLITypeClaude},
		{Name: "claude-c", Type: config.CLITypeClaude},
		{Name: "codex-a", Type: config.CLITypeCodex},
	}}
	withAccountModels(t, map[string]AccountModels{
		"claude-a": {Models: []string{"opus", "fable"}},
		"claude-b": {Models: []string{"opus"}},
		"claude-c": {Models: []string{"fable"}},
		"codex-a":  {Models: []string{"gpt-5"}},
	})
	user := store.User{ProviderAccounts: map[string][]string{
		config.CLITypeClaude: {"claude-b", "claude-a", "claude-c", "claude-b", "gone"},
		config.CLITypeCodex:  {"codex-a"},
	}}

	tests := []struct {
		name     string
		provider string
		model    string
		want     []string
	}{
		{"model served by two of three", config.CLITypeClaude, "opus", []string{"claude-b", "claude-a"}},
		{"model served by one sibling", config.CLITypeClaude, "fable", []string{"claude-a", "claude-c"}},
		{"unknown model has no candidates", config.CLITypeClaude, "sonnet", nil},
		{"empty model matches every granted account", config.CLITypeCodex, "", []string{"codex-a"}},
		{"wrong type is never a candidate", config.CLITypeCodex, "opus", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EligibleAccountsForModel(cfg, user, tt.provider, tt.model)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("EligibleAccountsForModel(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
