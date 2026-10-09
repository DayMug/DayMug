package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"
)

// ModelOverridesKey is the app_settings row holding the admin-edited model
// registry. One row for the whole registry (not one per account) so a save
// from the admin page is a single atomic write and the read path needs one
// query at boot instead of one per configured account.
const ModelOverridesKey = "models.accounts"

// AccountModels is the per-account model configuration an admin edits in the
// UI. It replaces the former providers[].models / providers[].summary_model
// YAML fields: config.yaml now only declares credentials and concurrency, and
// everything model-shaped lives in SQLite so it can change without a redeploy.
//
// An empty Models list means the account offers no models — that is how a
// freshly installed server behaves before an admin has touched the page.
type AccountModels struct {
	Models       []string `json:"models"`
	SummaryModel string   `json:"summary_model"`
	// Specs holds what DayMug cannot learn from the agent for a model id —
	// a third-party endpoint behind Claude Code or Codex reports the window
	// of whatever first-party model the CLI assumes. Keyed by model id; a
	// model without an entry keeps the agent-reported behaviour.
	Specs map[string]ModelSpec `json:"specs,omitempty"`
}

// ModelSpec is the admin-declared shape of one model.
type ModelSpec struct {
	// ContextWindow in tokens. Zero trusts the window the agent reports.
	ContextWindow int `json:"context_window,omitempty"`
	// NoImageInput marks a text-only model. Stored negatively so every model
	// configured before specs existed keeps accepting image attachments.
	NoImageInput bool `json:"no_image_input,omitempty"`
}

// modelOverrides is keyed by account name, which config validation already
// guarantees is unique across every provider type.
var (
	modelOverridesMu sync.RWMutex
	modelOverrides   = map[string]AccountModels{}
)

// ModelOverrides returns a deep copy of the active registry so callers can
// range over — or hand to a JSON encoder — without racing a concurrent save.
func ModelOverrides() map[string]AccountModels {
	modelOverridesMu.RLock()
	defer modelOverridesMu.RUnlock()
	return cloneModelOverrides(modelOverrides)
}

// ModelOverrideFor returns the stored configuration for one account. The bool
// distinguishes "no row for this account" from "a row that happens to be
// empty", which the resolution helpers treat the same way but the admin API
// reports differently.
func ModelOverrideFor(account string) (AccountModels, bool) {
	modelOverridesMu.RLock()
	defer modelOverridesMu.RUnlock()
	v, ok := modelOverrides[account]
	if !ok {
		return AccountModels{}, false
	}
	return cloneAccountModels(v), true
}

// ModelSpecFor returns the declared spec for one account's model, or the zero
// spec (agent-reported window, images allowed) when none is declared.
func ModelSpecFor(account, model string) ModelSpec {
	modelOverridesMu.RLock()
	defer modelOverridesMu.RUnlock()
	return modelOverrides[account].Specs[CanonicalModel(strings.TrimSpace(model))]
}

// SetModelOverrides atomically replaces the active registry. Exported for the
// save path and for tests that need a known registry without a database.
func SetModelOverrides(m map[string]AccountModels) {
	modelOverridesMu.Lock()
	defer modelOverridesMu.Unlock()
	modelOverrides = cloneModelOverrides(m)
}

// LoadModelOverrides reads the registry from app_settings into the in-memory
// cache. A missing row is the normal fresh-install state, not an error: every
// account then offers no models until an admin configures some.
//
// A nil store degrades to "nothing configured" rather than panicking, matching
// how pricing.Load tolerates unwired test and bootstrap paths.
func LoadModelOverrides(ctx context.Context, s AppSettingStore) error {
	if s == nil {
		return nil
	}
	raw, err := s.GetAppSetting(ctx, ModelOverridesKey)
	if err != nil {
		return fmt.Errorf("load model overrides: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		SetModelOverrides(nil)
		return nil
	}
	var parsed map[string]AccountModels
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("parse model overrides: %w", err)
	}
	SetModelOverrides(parsed)
	return nil
}

// SaveModelOverrides normalises, persists, and hot-swaps the registry. The
// in-memory cache is only updated once the write succeeds, so a failed save
// leaves the running server on the configuration it was already serving.
func SaveModelOverrides(ctx context.Context, s AppSettingStore, m map[string]AccountModels) error {
	if s == nil {
		return Internal("app setting store unavailable", nil)
	}
	normalised := NormalizeModelOverrides(m)
	if len(normalised) == 0 {
		// Clearing the row rather than storing "{}" keeps "never configured"
		// and "reset to defaults" as the same state on disk.
		if err := s.SetAppSetting(ctx, ModelOverridesKey, ""); err != nil {
			return Internal(err.Error(), err)
		}
		SetModelOverrides(nil)
		return nil
	}
	buf, err := json.Marshal(normalised)
	if err != nil {
		return Internal(err.Error(), err)
	}
	if err := s.SetAppSetting(ctx, ModelOverridesKey, string(buf)); err != nil {
		return Internal(err.Error(), err)
	}
	SetModelOverrides(normalised)
	return nil
}

// NormalizeModelOverrides trims every id, drops blank entries and
// de-duplicates each account's list. Accounts left with neither models nor a
// summary model are dropped entirely, so "never configured" and "cleared" are
// the same state.
func NormalizeModelOverrides(m map[string]AccountModels) map[string]AccountModels {
	out := make(map[string]AccountModels, len(m))
	for account, entry := range m {
		account = strings.TrimSpace(account)
		if account == "" {
			continue
		}
		seen := make(map[string]struct{}, len(entry.Models))
		models := make([]string, 0, len(entry.Models))
		for _, model := range entry.Models {
			model = CanonicalModel(strings.TrimSpace(model))
			if model == "" {
				continue
			}
			if _, dup := seen[model]; dup {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
		}
		summary := CanonicalModel(strings.TrimSpace(entry.SummaryModel))
		if len(models) == 0 && summary == "" {
			continue
		}
		out[account] = AccountModels{Models: models, SummaryModel: summary, Specs: normalizeSpecs(entry.Specs, seen, summary)}
	}
	return out
}

// normalizeSpecs keeps only meaningful specs for models the account still
// serves (its list or its summary model), so removing a model also forgets
// its spec instead of leaving a dead entry behind.
func normalizeSpecs(specs map[string]ModelSpec, served map[string]struct{}, summary string) map[string]ModelSpec {
	var out map[string]ModelSpec
	for model, spec := range specs {
		model = CanonicalModel(strings.TrimSpace(model))
		if _, ok := served[model]; !ok && model != summary {
			continue
		}
		spec.ContextWindow = max(spec.ContextWindow, 0)
		if spec == (ModelSpec{}) {
			continue
		}
		if out == nil {
			out = make(map[string]ModelSpec)
		}
		out[model] = spec
	}
	return out
}

func cloneAccountModels(v AccountModels) AccountModels {
	out := AccountModels{
		Models:       append([]string(nil), v.Models...),
		SummaryModel: v.SummaryModel,
	}
	if len(v.Specs) > 0 {
		out.Specs = maps.Clone(v.Specs)
	}
	return out
}

func cloneModelOverrides(m map[string]AccountModels) map[string]AccountModels {
	out := make(map[string]AccountModels, len(m))
	for k, v := range m {
		out[k] = cloneAccountModels(v)
	}
	return out
}
