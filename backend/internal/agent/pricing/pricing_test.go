package pricing

import (
	"context"
	"math"
	"testing"
)

// fakeAppSettings is a tiny AppSettingStore stub backed by a map. Used by
// the Save/Load round-trip tests so the pricing package doesn't pull a
// SQLite dependency just to exercise its persistence path.
type fakeAppSettings struct {
	rows map[string]string
}

func newFakeAppSettings() *fakeAppSettings {
	return &fakeAppSettings{rows: map[string]string{}}
}

func (f *fakeAppSettings) GetAppSetting(_ context.Context, key string) (string, error) {
	return f.rows[key], nil
}

func (f *fakeAppSettings) SetAppSetting(_ context.Context, key, value string) error {
	if value == "" {
		delete(f.rows, key)
		return nil
	}
	f.rows[key] = value
	return nil
}

func TestComputeCodexCost(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		in     int
		cached int
		out    int
		want   float64
		approx bool
	}{
		{name: "gpt-6-astra 1M in 1M out (no cache, no reasoning)", model: "gpt-6-astra", in: 1_000_000, out: 1_000_000, want: 60.0},
		{name: "gpt-6-astra with 80% cache hit", model: "gpt-6-astra", in: 1_000_000, cached: 800_000, out: 1_000_000, want: 52.8},
		{name: "gpt-5.6 1M in 1M out (no cache, no reasoning)", model: "gpt-5.6", in: 1_000_000, out: 1_000_000, want: 35.0},
		{name: "gpt-5.6-terra 1M in 1M out (no cache, no reasoning)", model: "gpt-5.6-terra", in: 1_000_000, out: 1_000_000, want: 17.5},
		{name: "gpt-5.6-luna 1M in 1M out (no cache, no reasoning)", model: "gpt-5.6-luna", in: 1_000_000, out: 1_000_000, want: 7.0},
		{name: "retired gpt-5.5 returns 0", model: "gpt-5.5", in: 1_000_000, out: 1_000_000, want: 0},
		{name: "unknown model returns 0", model: "gpt-future", in: 1_000_000, out: 1_000_000, want: 0},
		{name: "empty model returns 0", model: "", in: 1_000, out: 1_000, want: 0},
		{name: "small numbers stay accurate", model: "gpt-6-astra", in: 1_000, out: 1_000, want: 0.06, approx: true},
		{name: "zero tokens", model: "gpt-6-astra", in: 0, out: 0, want: 0},

		{name: "gpt-6-astra with 100% cache hit", model: "gpt-6-astra", in: 1_000_000, cached: 1_000_000, out: 1_000_000, want: 51.0},

		{name: "gpt-6-astra output already includes reasoning", model: "gpt-6-astra", in: 1_000_000, out: 1_000_000, want: 60.0},
		{name: "gpt-6-astra cache and output combined", model: "gpt-6-astra", in: 1_000_000, cached: 900_000, out: 100_000, want: 6.9},

		{name: "gpt-6-astra cached clamped to input", model: "gpt-6-astra", in: 500_000, cached: 1_000_000, want: 0.5},
		{name: "gpt-6-astra negative cached treated as zero", model: "gpt-6-astra", in: 1_000_000, cached: -100, out: 1_000_000, want: 60.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeCodexCost("codex", tt.model, tt.in, tt.cached, tt.out)
			diff := math.Abs(got - tt.want)
			if tt.approx && diff > 1e-9 {
				t.Errorf("ComputeCodexCost = %v, want ~%v (diff %v)", got, tt.want, diff)
			}
			if !tt.approx && got != tt.want {
				t.Errorf("ComputeCodexCost = %v, want %v", got, tt.want)
			}
		})
	}
}

// When an admin override row lacks a CachedInput field, ComputeCodexCost
// must fall back to half the Input rate so cached bytes still get a
// discount instead of silently being billed at full price.
func TestComputeCodexCost_CachedInputFallsBackToHalfInput(t *testing.T) {
	Set("codex", Table{"custom-no-cached": {Input: 8.0, Output: 24.0}})
	t.Cleanup(ResetForTest)

	got := ComputeCodexCost("codex", "custom-no-cached", 1_000_000, 500_000, 0)
	if math.Abs(got-6.0) > 1e-9 {
		t.Errorf("ComputeCodexCost with implicit cached rate = %v, want 6.0", got)
	}
}

func TestComputeClaudeCost(t *testing.T) {
	// Anchor the test on a fresh override so future tweaks to the
	// compile-time defaults don't quietly break the math expectations.
	Set("claude-compatible", Table{
		"compat-test": {Input: 3.0, CachedInput: 0.30, Output: 15.0, CacheCreation5m: 3.75, CacheCreation1h: 6.0},
	})
	t.Cleanup(ResetForTest)

	tests := []struct {
		name        string
		model       string
		in, cr      int
		c5m, c1h    int
		out         int
		want        float64
		approxFloat bool
	}{
		{name: "1M input only", model: "compat-test", in: 1_000_000, want: 3.0},
		{name: "1M output only", model: "compat-test", out: 1_000_000, want: 15.0},
		{name: "1M cache read only", model: "compat-test", cr: 1_000_000, want: 0.30},
		{name: "1M 5m cache write only", model: "compat-test", c5m: 1_000_000, want: 3.75},
		{name: "1M 1h cache write only", model: "compat-test", c1h: 1_000_000, want: 6.0},
		{
			name:  "full breakdown sums",
			model: "compat-test",
			in:    100_000, cr: 900_000, c5m: 50_000, c1h: 25_000, out: 200_000,
			// 0.3 + 0.27 + 0.1875 + 0.15 + 3.0 = 3.9075
			want:        3.9075,
			approxFloat: true,
		},
		{name: "unknown model returns 0", model: "compat-future", in: 1_000_000, out: 1_000_000, want: 0},
		{name: "negative tokens clamped", model: "compat-test", in: -500, out: 1_000_000, want: 15.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeClaudeCost("claude-compatible", tt.model, tt.in, tt.cr, tt.c5m, tt.c1h, tt.out)
			diff := math.Abs(got - tt.want)
			if tt.approxFloat && diff > 1e-9 {
				t.Errorf("ComputeClaudeCost = %v, want ~%v (diff %v)", got, tt.want, diff)
			}
			if !tt.approxFloat && got != tt.want {
				t.Errorf("ComputeClaudeCost = %v, want %v", got, tt.want)
			}
		})
	}
}

// Cache fields with no override must contribute zero, NOT fall back to
// any "half of input" guess. The Anthropic discount curve is much
// steeper than codex's, so a guess would massively over-bill — the
// admin UI surfaces the zero so the operator can set a real value.
func TestComputeClaudeCost_NoFallbackForMissingFields(t *testing.T) {
	Set("claude-compatible", Table{"bare": {Input: 3.0, Output: 15.0}})
	t.Cleanup(ResetForTest)

	got := ComputeClaudeCost("claude-compatible", "bare", 0, 1_000_000, 1_000_000, 1_000_000, 0)
	if got != 0 {
		t.Errorf("missing cache rates should contribute 0, got %v", got)
	}
}

// codex is the one locally-costed provider whose catalog DayMug can know up
// front, so it is the one that must carry a non-empty baseline. The two
// compatible types are intentionally empty — their model ids come from the
// operator's endpoint — but they must still be registered, or the admin
// pricing route would reject them and they could never be priced at all.
func TestDefaultsCoversConfiguredProviders(t *testing.T) {
	if len(Defaults("codex")) == 0 {
		t.Error(`Defaults("codex") is empty — the first-party catalog needs a baseline`)
	}
	for _, provider := range []string{"claude-compatible", "openai-compatible"} {
		if _, ok := defaultPrices[provider]; !ok {
			t.Errorf("provider %q has no registry entry, so /api/admin/pricing/%s would 400", provider, provider)
		}
	}
}

func TestProvidersWithDefaultsSorted(t *testing.T) {
	got := ProvidersWithDefaults()
	// Stability matters for the admin error message; assert the list is
	// the exact one a handler can compare against.
	want := []string{"claude-compatible", "codex", "openai-compatible"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ProvidersWithDefaults()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	t.Cleanup(ResetForTest)

	s := newFakeAppSettings()
	override := Table{
		"gpt-6-astra": {Input: 4.5, CachedInput: 0.5, Output: 18.0},
	}

	if err := Save(context.Background(), s, "codex", override); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Hot-swap: Get returns the override merged on top of defaults.
	got := Get("codex")
	if got["gpt-6-astra"].Input != 4.5 {
		t.Errorf("after Save, Get(codex)[gpt-6-astra].Input = %v, want 4.5", got["gpt-6-astra"].Input)
	}
	if _, ok := got["gpt-5.6-luna"]; !ok {
		t.Error("after Save, Get(codex) should still carry the untouched default rows (merge semantics)")
	}

	// Reset and verify Load recovers the override from app_settings.
	ResetForTest()
	if Get("codex")["gpt-6-astra"].Input == 4.5 {
		t.Fatal("ResetForTest did not clear the override")
	}
	if err := Load(context.Background(), s, "codex"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if Get("codex")["gpt-6-astra"].Input != 4.5 {
		t.Errorf("after Load, Get(codex)[gpt-6-astra].Input = %v, want 4.5",
			Get("codex")["gpt-6-astra"].Input)
	}
}

// A compatible provider starts with an empty table, so Save is the only thing
// that ever puts a row in it. The merge path must not swallow that row just
// because there is no compile-time baseline to merge it onto.
func TestSaveSeedsAProviderWithNoDefaults(t *testing.T) {
	t.Cleanup(ResetForTest)

	s := newFakeAppSettings()
	if err := Save(context.Background(), s, "openai-compatible", Table{
		"qwen3-max": {Input: 1.2, CachedInput: 0.12, Output: 6.0},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Get("openai-compatible")["qwen3-max"].Input; got != 1.2 {
		t.Errorf("Get(openai-compatible)[qwen3-max].Input = %v, want 1.2", got)
	}
	if got := ComputeCodexCost("openai-compatible", "qwen3-max", 1_000_000, 0, 0); got != 1.2 {
		t.Errorf("ComputeCodexCost on the compatible table = %v, want 1.2", got)
	}
	// The first-party table must not have picked the row up.
	if _, ok := Get("codex")["qwen3-max"]; ok {
		t.Error("a compatible provider's model leaked into the codex price table")
	}
}

func TestSaveEmptyClearsOverride(t *testing.T) {
	t.Cleanup(ResetForTest)

	s := newFakeAppSettings()
	if err := Save(context.Background(), s, "codex", Table{
		"gpt-6-astra": {Input: 99, Output: 99},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if Get("codex")["gpt-6-astra"].Input != 99 {
		t.Fatal("override not active after Save")
	}
	if err := Save(context.Background(), s, "codex", nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if Get("codex")["gpt-6-astra"].Input != 10 {
		t.Errorf("Save(nil) should restore default %v; got %v", 10.0, Get("codex")["gpt-6-astra"].Input)
	}
}
