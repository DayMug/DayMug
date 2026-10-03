package service

import (
	"sort"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// BackendRegistry is the single answer to "which agent.Backend drives a
// conversation stored under provider X". Every entry point that starts a turn
// (web chat, /compact, cron, the IM bridge) resolves through
// the same instance, so a provider routed one way in chat cannot be routed
// another way over IM.
//
// The concrete adapters are wired by the handler layer (service never imports
// them); the registry only holds the result.
type BackendRegistry struct {
	byProvider map[string]agent.Backend
	fallback   agent.Backend
}

// NewBackendRegistry pairs the per-provider map with the backend used for rows
// that carry no provider (legacy conversations) or one this process has no
// adapter for. Either may be nil in focused tests.
func NewBackendRegistry(byProvider map[string]agent.Backend, fallback agent.Backend) *BackendRegistry {
	return &BackendRegistry{byProvider: byProvider, fallback: fallback}
}

// For returns the backend for provider, falling back to the default for an
// empty or unknown provider. Nil-safe so test wiring that never reaches a run
// can leave the registry out.
func (r *BackendRegistry) For(provider string) agent.Backend {
	if r == nil {
		return nil
	}
	if b, ok := r.Lookup(provider); ok {
		return b
	}
	return r.fallback
}

// Lookup is the exact match with no fallback, for callers that must not
// silently substitute another provider's CLI (capability matrices, diagnostic
// bundles, account checks).
func (r *BackendRegistry) Lookup(provider string) (agent.Backend, bool) {
	if r == nil || provider == "" {
		return nil, false
	}
	b, ok := r.byProvider[provider]
	return b, ok && b != nil
}

// Providers lists the provider ids with a registered backend, sorted so
// callers that build per-provider tables do so deterministically.
func (r *BackendRegistry) Providers() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byProvider))
	for provider, b := range r.byProvider {
		if b != nil {
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out
}
