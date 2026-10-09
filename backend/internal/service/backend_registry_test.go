package service

import (
	"slices"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
)

func TestBackendRegistryRouting(t *testing.T) {
	claude := &agenttest.ScriptedBackend{Result: "claude"}
	codex := &agenttest.ScriptedBackend{Result: "codex"}
	fallback := &agenttest.ScriptedBackend{Result: "fallback"}
	r := NewBackendRegistry(map[string]agent.Backend{"claude": claude, "codex": codex, "broken": nil}, fallback)

	tests := []struct {
		provider  string
		wantFor   agent.Backend
		wantExact bool
	}{
		{"codex", codex, true},
		{"claude", claude, true},
		// Legacy rows with no provider, types this process has no adapter for,
		// and a nil entry all run on the fallback — but only For substitutes.
		{"", fallback, false},
		{"unknown", fallback, false},
		{"broken", fallback, false},
	}
	for _, tc := range tests {
		if got := r.For(tc.provider); got != tc.wantFor {
			t.Errorf("For(%q) = %v, want %v", tc.provider, got, tc.wantFor)
		}
		if _, ok := r.Lookup(tc.provider); ok != tc.wantExact {
			t.Errorf("Lookup(%q) ok = %v, want %v", tc.provider, ok, tc.wantExact)
		}
	}
	if got := r.Providers(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Errorf("Providers() = %v, want the non-nil entries sorted", got)
	}
}

func TestNilBackendRegistryIsEmpty(t *testing.T) {
	var r *BackendRegistry
	if r.For("claude") != nil || r.Providers() != nil {
		t.Fatal("a nil registry must resolve nothing")
	}
	if _, ok := r.Lookup("claude"); ok {
		t.Fatal("a nil registry must look up nothing")
	}
}
