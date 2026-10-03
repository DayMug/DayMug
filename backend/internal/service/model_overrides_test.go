package service

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestSaveModelOverridesRoundTripsThroughAppSettings(t *testing.T) {
	withAccountModels(t, nil)
	ctx := context.Background()
	ms := storetest.New()

	err := SaveModelOverrides(ctx, ms, map[string]AccountModels{
		"codex-qwen": {Models: []string{" Qwen/Qwen3 ", "Qwen/Qwen3", ""}, SummaryModel: " Qwen/Qwen3 "},
	})
	if err != nil {
		t.Fatalf("SaveModelOverrides: %v", err)
	}

	// Saving must normalise before it persists, not only before it serves:
	// a reload from disk has to produce the same registry as the hot swap.
	raw := ms.AppSettings[ModelOverridesKey]
	var persisted map[string]AccountModels
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatalf("stored value is not the registry JSON (%q): %v", raw, err)
	}
	if got := persisted["codex-qwen"]; !slices.Equal(got.Models, []string{"Qwen/Qwen3"}) || got.SummaryModel != "Qwen/Qwen3" {
		t.Fatalf("persisted entry = %#v, want trimmed and de-duplicated", got)
	}

	SetModelOverrides(nil)
	if err := LoadModelOverrides(ctx, ms); err != nil {
		t.Fatalf("LoadModelOverrides: %v", err)
	}
	entry, ok := ModelOverrideFor("codex-qwen")
	if !ok || !slices.Equal(entry.Models, []string{"Qwen/Qwen3"}) {
		t.Fatalf("after reload entry = %#v ok=%v", entry, ok)
	}
}

func TestSaveModelOverridesClearsTheRowWhenNothingIsConfigured(t *testing.T) {
	withAccountModels(t, map[string]AccountModels{"acc": {Models: []string{"m"}}})
	ctx := context.Background()
	ms := storetest.New()
	ms.AppSettings = map[string]string{ModelOverridesKey: `{"acc":{"models":["m"]}}`}

	// An account whose lists are all blank is not a pinned-to-empty account,
	// it is an account the admin cleared — the row goes away so it inherits.
	if err := SaveModelOverrides(ctx, ms, map[string]AccountModels{
		"acc": {Models: []string{"  "}, SummaryModel: " "},
	}); err != nil {
		t.Fatalf("SaveModelOverrides: %v", err)
	}
	if got := ms.AppSettings[ModelOverridesKey]; got != "" {
		t.Fatalf("setting = %q, want cleared", got)
	}
	if _, ok := ModelOverrideFor("acc"); ok {
		t.Fatal("cleared account should have no registry entry")
	}
}

func TestLoadModelOverridesRejectsMalformedJSONWithoutClobberingTheCache(t *testing.T) {
	withAccountModels(t, map[string]AccountModels{"acc": {Models: []string{"keep-me"}}})
	ms := storetest.New()
	ms.AppSettings = map[string]string{ModelOverridesKey: "{not json"}

	if err := LoadModelOverrides(context.Background(), ms); err == nil {
		t.Fatal("expected a parse error")
	}
	// The running server keeps serving what it already had; a bad row must not
	// silently demote every account to the built-in list mid-flight.
	entry, ok := ModelOverrideFor("acc")
	if !ok || !slices.Equal(entry.Models, []string{"keep-me"}) {
		t.Fatalf("cache = %#v ok=%v, want untouched", entry, ok)
	}
}

func TestModelResolutionFallsBackToBuiltInsWithoutAnOverride(t *testing.T) {
	withAccountModels(t, nil)
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
	}}
	got := ModelsForAccount(cfg, config.CLITypeClaude, "default")
	if !slices.Equal(got, ProviderModels[config.CLITypeClaude]) {
		t.Fatalf("ModelsForAccount = %#v, want the built-in claude list", got)
	}
}

func TestConfiguredSummaryModelPrefersTheAccountThenItsSiblings(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "claude-a", Type: config.CLITypeClaude},
		{Name: "claude-b", Type: config.CLITypeClaude},
	}}
	withAccountModels(t, map[string]AccountModels{
		"claude-a": {SummaryModel: "from-a"},
		"claude-b": {SummaryModel: "from-b"},
	})

	if got := ConfiguredSummaryModel(cfg, config.CLITypeClaude, "claude-b"); got != "from-b" {
		t.Errorf("account's own summary model = %q, want from-b", got)
	}
	// An account with none of its own inherits the first configured sibling,
	// which is how the removed type-scoped YAML field behaved.
	withAccountModels(t, map[string]AccountModels{"claude-b": {SummaryModel: "from-b"}})
	if got := ConfiguredSummaryModel(cfg, config.CLITypeClaude, "claude-a"); got != "from-b" {
		t.Errorf("inherited summary model = %q, want from-b", got)
	}
	// Claude has no adapter default, so an unconfigured type stays empty and
	// /compact keeps the conversation model.
	withAccountModels(t, nil)
	if got := ConfiguredSummaryModel(cfg, config.CLITypeClaude, "claude-a"); got != "" {
		t.Errorf("unconfigured summary model = %q, want empty", got)
	}
}

func TestNormalizeModelOverridesKeepsSpecsOnlyForServedModels(t *testing.T) {
	got := NormalizeModelOverrides(map[string]AccountModels{
		"acc": {
			Models:       []string{"claude-opus-5-5", " qwen "},
			SummaryModel: "mini",
			Specs: map[string]ModelSpec{
				// Specs follow the canonical id the model list is stored under.
				"claude-opus-5-5": {NoImageInput: true},
				"qwen":            {ContextWindow: 131072},
				"mini":            {ContextWindow: 32000},
				"removed":         {ContextWindow: 64000},
				"blank":           {},
			},
		},
	})["acc"]
	want := map[string]ModelSpec{
		"claude-opus-5-5[1m]": {NoImageInput: true},
		"qwen":                {ContextWindow: 131072},
		"mini":                {ContextWindow: 32000},
	}
	if len(got.Specs) != len(want) {
		t.Fatalf("specs = %#v, want %#v", got.Specs, want)
	}
	for model, spec := range want {
		if got.Specs[model] != spec {
			t.Fatalf("specs[%s] = %#v, want %#v", model, got.Specs[model], spec)
		}
	}
}

func TestModelSpecForResolvesAliases(t *testing.T) {
	withAccountModels(t, map[string]AccountModels{"acc": {
		Models: []string{"claude-opus-5-5[1m]"},
		Specs:  map[string]ModelSpec{"claude-opus-5-5[1m]": {ContextWindow: 1_000_000}},
	}})
	if got := ModelSpecFor("acc", "claude-opus-5-5").ContextWindow; got != 1_000_000 {
		t.Fatalf("ModelSpecFor(alias) window = %d, want 1000000", got)
	}
	if got := ModelSpecFor("other", "claude-opus-5-5"); got != (ModelSpec{}) {
		t.Fatalf("ModelSpecFor(unknown account) = %#v, want zero spec", got)
	}
}
