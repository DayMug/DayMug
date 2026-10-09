// Package pricing carries the admin-editable token price tables for every
// provider whose cost has to be computed locally: the Codex family never
// reports a per-turn cost at all, and a claude-compatible endpoint's CLI
// reports one computed from Anthropic's catalog, which is not the catalog
// that endpoint bills against.
//
// One in-memory registry keyed by provider id (matching config.CLIType*)
// powers all of:
//   - the per-turn cost stamped onto agent.KindUsage events,
//   - the per-(user, model, day) token_usage rollup in the DB,
//   - the admin UI's "edit pricing" round-trip.
//
// Hot-swap is thread-safe (RWMutex); persistence rides on the app_settings
// store and lazy-loads at server start (Load).
package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// AppSettingStore is the narrowed slice of store.Store that pricing
// needs. Declared as a local interface (rather than depending on
// store.Store directly) so the pricing package has no compile-time
// dependency on the store implementation and unit tests can stub it
// with a half-dozen lines.
type AppSettingStore interface {
	GetAppSetting(ctx context.Context, key string) (string, error)
	SetAppSetting(ctx context.Context, key, value string) error
}

// ModelPrice carries per-million-token USD rates for a single model.
// Providers populate the subset that maps to their billing model:
//
//   - codex: Input / CachedInput / Output. CachedInput == OpenAI's
//     "cached_input" rate (prompt-prefix cache hit). The 5m/1h fields
//     stay at zero.
//   - claude-compatible: Input is the uncached prompt rate; CachedInput is
//     cache_read; CacheCreation5m / CacheCreation1h are the ephemeral
//     cache-write rates Anthropic publishes per TTL bucket; Output is the
//     assistant-side rate.
//
// JSON tags use snake_case so the admin UI can round-trip without alias
// shims; `cached_input` is kept as the shared field name because both
// providers reuse the same semantic ("discounted re-feed of cached prompt
// bytes") even though codex calls it cached_input and Anthropic calls it
// cache_read.
type ModelPrice struct {
	Input           float64 `json:"input,omitempty"`
	CachedInput     float64 `json:"cached_input,omitempty"`
	Output          float64 `json:"output,omitempty"`
	CacheCreation5m float64 `json:"cache_creation_5m,omitempty"`
	CacheCreation1h float64 `json:"cache_creation_1h,omitempty"`
}

// Table is a model id → ModelPrice map for a single provider.
type Table map[string]ModelPrice

// defaultPrices is the compile-time baseline per provider id. Numbers are
// list-price estimates at the time of writing — they drift, which is
// exactly why the admin UI exists. New provider/model -> add an entry
// here (or configure via the admin UI on first deploy); a missing entry
// makes ComputeXxxCost return 0 and the UI hides the cost chip.
var defaultPrices = map[string]Table{
	config.CLITypeCodex: {
		"gpt-6-astra":   {Input: 10.00, CachedInput: 1.00, Output: 50.00},
		"gpt-5.6":       {Input: 5.00, CachedInput: 2.50, Output: 30.00},
		"gpt-5.6-sol":   {Input: 5.00, CachedInput: 2.50, Output: 30.00},
		"gpt-5.6-terra": {Input: 2.50, CachedInput: 1.25, Output: 15.00},
		"gpt-5.6-luna":  {Input: 1.00, CachedInput: 0.50, Output: 6.00},
	},
	// The two compatible types ship empty: their model ids come from
	// whichever endpoint the operator pointed them at, so there is nothing
	// to seed. The keys still have to exist — ProvidersWithDefaults gates
	// /api/admin/pricing/<provider>, and without a row here an operator
	// could configure a compatible provider but never price it. The admin
	// UI offers a line per model from the model registry.
	//
	// Note this is also why a compatible provider's cost chip stays hidden
	// until someone fills the table in: ComputeXxxCost returns 0 for an
	// unpriced model rather than borrowing the vendor's rate, which would
	// be wrong by whole multiples for most third-party endpoints.
	config.CLITypeClaudeCompatible: {},
	config.CLITypeOpenAICompatible: {},
}

var (
	activeMu     sync.RWMutex
	activePrices = cloneRegistry(defaultPrices)
)

// SettingKey returns the app_settings row key that holds the operator
// override for a provider's pricing.
func SettingKey(provider string) string {
	return provider + ".pricing"
}

// ProvidersWithDefaults returns the provider ids that have a compile-time
// baseline, sorted alphabetically so the admin route validator's error
// message is stable. Used to reject /admin/pricing/<bad> at the boundary.
func ProvidersWithDefaults() []string {
	out := make([]string, 0, len(defaultPrices))
	for p := range defaultPrices {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Defaults returns a copy of the compile-time baseline for a provider.
// Unknown provider returns an empty (non-nil) table so callers can range
// over the result safely.
func Defaults(provider string) Table {
	return cloneTable(defaultPrices[provider])
}

// Get returns a snapshot of the active rates for a provider. The returned
// table is a deep copy — callers can mutate without racing Set.
func Get(provider string) Table {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return cloneTable(activePrices[provider])
}

// Set replaces the active table for a provider atomically. Pass nil to
// revert to the compile-time baseline.
func Set(provider string, t Table) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if t == nil {
		activePrices[provider] = cloneTable(defaultPrices[provider])
		return
	}
	activePrices[provider] = cloneTable(t)
}

// Load reads the override row from app_settings and merges it on top of
// the compile-time defaults. Missing key is a normal "no override" state
// — defaults stay in effect.
func Load(ctx context.Context, s AppSettingStore, provider string) error {
	if s == nil {
		return nil
	}
	raw, err := s.GetAppSetting(ctx, SettingKey(provider))
	if err != nil {
		return fmt.Errorf("load %s pricing: %w", provider, err)
	}
	base := cloneTable(defaultPrices[provider])
	if raw == "" {
		Set(provider, base)
		return nil
	}
	var override Table
	if err := json.Unmarshal([]byte(raw), &override); err != nil {
		return fmt.Errorf("parse %s pricing: %w", provider, err)
	}
	maps.Copy(base, override)
	Set(provider, base)
	return nil
}

// Save serialises the override and persists it under SettingKey(provider),
// then hot-swaps the in-memory table. Empty input clears the override row,
// reverting to compile-time defaults — match the codex behaviour callers
// already rely on for the "reset" admin button.
func Save(ctx context.Context, s AppSettingStore, provider string, t Table) error {
	if s == nil {
		return errors.New("pricing: nil store")
	}
	key := SettingKey(provider)
	if len(t) == 0 {
		if err := s.SetAppSetting(ctx, key, ""); err != nil {
			return fmt.Errorf("clear %s pricing: %w", provider, err)
		}
		Set(provider, nil)
		return nil
	}
	buf, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal %s pricing: %w", provider, err)
	}
	if err := s.SetAppSetting(ctx, key, string(buf)); err != nil {
		return fmt.Errorf("save %s pricing: %w", provider, err)
	}
	base := cloneTable(defaultPrices[provider])
	maps.Copy(base, t)
	Set(provider, base)
	return nil
}

// ComputeCodexCost returns the estimated dollar cost for one Codex-family
// turn. `provider` selects the table: a `codex` conversation bills against
// OpenAI's catalog, an `openai-compatible` one against whatever the operator
// entered for that endpoint, and the two never share a row even when the
// model ids collide.
// `model` MUST be DayMug's UI label (RunRequest.Model), NOT the bare
// upstream id codex sometimes emits — see the runner doc for why.
// `cachedInputTokens` is the subset of inputTokens that hit OpenAI's
// prompt-prefix cache. outputTokens already includes both visible and
// reasoning output, so callers must not add reasoning tokens separately.
//
// Backward-compat: when a custom admin row omits CachedInput, fall back
// to half the Input rate (matches OpenAI's published 50% discount and
// keeps existing rows behaving the same as before the refactor).
func ComputeCodexCost(provider, model string, inputTokens, cachedInputTokens, outputTokens int) float64 {
	activeMu.RLock()
	p, ok := activePrices[provider][model]
	activeMu.RUnlock()
	if !ok {
		return 0
	}
	const million = 1_000_000.0
	cached := max(cachedInputTokens, 0)
	cached = min(cached, inputTokens)
	uncached := inputTokens - cached
	cachedRate := p.CachedInput
	if cachedRate <= 0 {
		cachedRate = p.Input / 2
	}
	return (float64(uncached)/million)*p.Input +
		(float64(cached)/million)*cachedRate +
		(float64(outputTokens)/million)*p.Output
}

// ComputeClaudeCost returns the estimated dollar cost for one
// Anthropic-compatible turn. Fields map to the Anthropic streaming API's
// usage envelope:
//
//   - inputTokens          = usage.input_tokens (uncached, this turn only)
//   - cacheReadTokens      = usage.cache_read_input_tokens
//   - cacheCreation5m      = usage.cache_creation.ephemeral_5m_input_tokens
//   - cacheCreation1h      = usage.cache_creation.ephemeral_1h_input_tokens
//   - outputTokens         = usage.output_tokens
//
// When the CLI only reports the aggregate `cache_creation_input_tokens`
// without the 5m/1h split, callers should pass it as cacheCreation5m and
// 0 for cacheCreation1h — that's the safer assumption since 5m is the
// cheaper TTL and "split unknown" should not surprise the operator with
// a higher bill.
//
// Cache fields with no override fall back to zero contribution rather than
// "half of input" (the Anthropic discount curve is much steeper than
// codex's, so a 0.5× fallback would massively over-bill cache reads).
// Operators who set Input but leave CachedInput at zero get an explicit
// "this slice didn't bill" rather than a guess; the admin UI surfaces
// this so it's discoverable.
func ComputeClaudeCost(provider, model string, inputTokens, cacheReadTokens, cacheCreation5m, cacheCreation1h, outputTokens int) float64 {
	activeMu.RLock()
	p, ok := activePrices[provider][model]
	activeMu.RUnlock()
	if !ok {
		return 0
	}
	const million = 1_000_000.0
	clamp := func(v int) int { return max(v, 0) }
	return (float64(clamp(inputTokens))/million)*p.Input +
		(float64(clamp(cacheReadTokens))/million)*p.CachedInput +
		(float64(clamp(cacheCreation5m))/million)*p.CacheCreation5m +
		(float64(clamp(cacheCreation1h))/million)*p.CacheCreation1h +
		(float64(clamp(outputTokens))/million)*p.Output
}

// ResetForTest restores the in-memory registry to the compile-time
// defaults for every provider. Test-only helper — used by package-level
// t.Cleanup so a Set/Save in one test doesn't leak into the next.
func ResetForTest() {
	activeMu.Lock()
	defer activeMu.Unlock()
	activePrices = cloneRegistry(defaultPrices)
}

func cloneTable(in Table) Table {
	out := make(Table, len(in))
	maps.Copy(out, in)
	return out
}

func cloneRegistry(in map[string]Table) map[string]Table {
	out := make(map[string]Table, len(in))
	for k, v := range in {
		out[k] = cloneTable(v)
	}
	return out
}
