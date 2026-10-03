package service

import (
	"slices"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AccountRotator spreads runs that are not pinned to a conversation across
// every account a user holds for one (provider, model) pair.
//
// Without it a bot with a fixed default model sends every thread to the single
// default binding: the sibling accounts granted to the same agent sit idle
// while the default one queues and eventually trips its rate limit. The cursor
// is process-local and per key, which is enough — it only has to advance, not
// survive a restart.
//
// The zero value is ready to use and safe for concurrent use.
type AccountRotator struct {
	mu      sync.Mutex
	cursors map[string]int
}

// Next returns the next account for key, advancing the cursor by one.
//
// usable (optional) filters candidates the caller knows cannot serve this run
// right now — a rate-limit cooldown, typically. Candidates are tried in order
// starting at the cursor; when every one of them is unusable the plain next
// candidate is returned anyway, so the caller still surfaces the real error
// from the pool instead of silently doing nothing.
func (r *AccountRotator) Next(key string, candidates []string, usable func(account string) bool) string {
	if len(candidates) == 0 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cursors == nil {
		r.cursors = map[string]int{}
	}
	start := r.cursors[key] % len(candidates)
	for i := range candidates {
		idx := (start + i) % len(candidates)
		if usable == nil || usable(candidates[idx]) {
			r.cursors[key] = (idx + 1) % len(candidates)
			return candidates[idx]
		}
	}
	r.cursors[key] = (start + 1) % len(candidates)
	return candidates[start]
}

// EligibleAccountsForModel lists the accounts a user may use for providerType
// that actually advertise model, in the user's own default-first order.
//
// Two accounts of the same type can expose disjoint model lists (see
// ModelsForAccount), so membership in the user's granted set is not enough:
// rotating a run onto an account that does not serve the requested model would
// just move the failure. An empty model matches every granted account of the
// type.
func EligibleAccountsForModel(cfg *config.Config, user store.User, providerType, model string) []string {
	out := make([]string, 0, len(user.ProviderAccounts[providerType]))
	for _, name := range user.ProviderAccounts[providerType] {
		if name == "" || slices.Contains(out, name) {
			continue
		}
		if cfg.FindAccountForType(name, providerType) == nil {
			continue
		}
		if model != "" && !IsValidModelForAccount(cfg, providerType, name, model) {
			continue
		}
		out = append(out, name)
	}
	return out
}
