package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestLatestModelForConfigBuiltins(t *testing.T) {
	cases := map[string]string{
		config.CLITypeClaude: "claude-opus-5-5[1m]",
		config.CLITypeCodex:  "gpt-5.6-sol",
		// The compatible types ship no built-in catalog, so they have no
		// latest model until an admin configures one.
		config.CLITypeClaudeCompatible: "",
		config.CLITypeOpenAICompatible: "",
		"unknown":                      "",
	}
	for provider, want := range cases {
		if got := LatestModelForConfig(nil, provider); got != want {
			t.Errorf("LatestModelForConfig(nil, %q) = %q, want %q", provider, got, want)
		}
	}
}

func TestIsValidProvider(t *testing.T) {
	cases := map[string]bool{
		config.CLITypeClaude: true,
		config.CLITypeCodex:  true,
		// Valid despite an empty built-in model list — validity is
		// "the server knows how to spawn this", not "it has models".
		config.CLITypeClaudeCompatible: true,
		config.CLITypeOpenAICompatible: true,
		"":                             false,
		"mimo":                         false,
		"openai":                       false,
	}
	for provider, want := range cases {
		if got := IsValidProvider(provider); got != want {
			t.Errorf("IsValidProvider(%q) = %v, want %v", provider, got, want)
		}
	}
}

func TestIsValidModelForConfigBuiltins(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		want     bool
	}{
		{config.CLITypeClaude, "claude-fable-5-1", true},
		{config.CLITypeClaude, "claude-fable-5", false},
		{config.CLITypeClaude, "claude-opus-5", true},
		{config.CLITypeClaude, "claude-sonnet-5", true},
		{config.CLITypeClaude, "claude-sonnet-4-6", false},
		{config.CLITypeClaude, "gpt-5.5", false},
		{config.CLITypeCodex, "gpt-5.6", false},
		{config.CLITypeCodex, "gpt-5.6-sol", true},
		{config.CLITypeCodex, "gpt-5.6-terra", true},
		{config.CLITypeCodex, "gpt-5.6-luna", true},
		{config.CLITypeCodex, "gpt-6-astra", true},
		{config.CLITypeCodex, "gpt-5.5", false},
		{config.CLITypeCodex, "gpt-5.4", false},
		{config.CLITypeCodex, "gpt-5.4-mini", false},
		{config.CLITypeCodex, "claude-opus-5", false},
		{config.CLITypeClaudeCompatible, "claude-opus-5", false},
		{config.CLITypeOpenAICompatible, "gpt-5.6-sol", false},
		{"unknown", "claude-opus-5", false},
		{config.CLITypeClaude, "", false},
	}
	for _, tc := range cases {
		if got := IsValidModelForConfig(nil, tc.provider, tc.model); got != tc.want {
			t.Errorf("IsValidModelForConfig(nil, %q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestCodexModelsAreOrderedForDefaultDropdown(t *testing.T) {
	got := ModelsForConfig(nil, config.CLITypeCodex)
	want := []string{
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-6-astra",
	}
	if len(got) != len(want) {
		t.Fatalf("ModelsForConfig(codex) = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ModelsForConfig(codex) = %#v, want %#v", got, want)
		}
	}
}

func TestProviders(t *testing.T) {
	got := Providers()
	want := []string{
		config.CLITypeClaude,
		config.CLITypeCodex,
		config.CLITypeClaudeCompatible,
		config.CLITypeOpenAICompatible,
	}
	if len(got) != len(want) {
		t.Fatalf("Providers() length = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Providers()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// withAccountModels installs a model registry for one test and restores the
// previous one afterwards. The registry is process-global (it mirrors a single
// app_settings row), so without the restore one test's accounts would leak
// into the next.
func withAccountModels(t *testing.T, m map[string]AccountModels) {
	t.Helper()
	prev := ModelOverrides()
	SetModelOverrides(m)
	t.Cleanup(func() { SetModelOverrides(prev) })
}

func TestModelsForConfigUnionsAccountsOfType(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "glm", Type: config.CLITypeClaudeCompatible},
		{Name: "mimo", Type: config.CLITypeClaudeCompatible},
	}}
	withAccountModels(t, map[string]AccountModels{
		"glm":  {Models: []string{"glm-5", "glm-5-air"}},
		"mimo": {Models: []string{"glm-5-air", "mimo-v2.5"}},
	})
	if got := ProvidersForConfig(cfg); len(got) != 2 || got[0] != config.CLITypeClaude || got[1] != config.CLITypeClaudeCompatible {
		t.Fatalf("ProvidersForConfig = %#v", got)
	}
	// Type-level view unions every account's models in YAML order, de-duped, so
	// the external-API model→provider lookup can still resolve any account's id.
	models := ModelsForConfig(cfg, config.CLITypeClaudeCompatible)
	want := []string{"glm-5", "glm-5-air", "mimo-v2.5"}
	if len(models) != len(want) {
		t.Fatalf("ModelsForConfig = %#v, want %#v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("ModelsForConfig = %#v, want %#v", models, want)
		}
	}
	if got := LatestModelForConfig(cfg, config.CLITypeClaudeCompatible); got != "glm-5" {
		t.Errorf("LatestModelForConfig = %q, want glm-5", got)
	}
	if !IsValidProviderForConfig(cfg, config.CLITypeClaudeCompatible) {
		t.Errorf("IsValidProviderForConfig should accept a configured claude-compatible")
	}
	if IsValidProviderForConfig(cfg, config.CLITypeCodex) {
		t.Errorf("IsValidProviderForConfig should reject unconfigured codex")
	}
	// Permissive type-level check accepts either account's model.
	if !IsValidModelForConfig(cfg, config.CLITypeClaudeCompatible, "mimo-v2.5") {
		t.Errorf("IsValidModelForConfig should accept any account's model at type level")
	}
}

// A compatible provider has no built-in catalog, so the admin registry is the
// only source of models for it. Without a configured account it must resolve
// to an empty list rather than inheriting a sibling type's ids.
func TestModelsForCompatibleProviderComeOnlyFromTheRegistry(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "qwen", Type: config.CLITypeOpenAICompatible, ConfigDir: "/srv/qwen"},
	}}
	if got := ModelsForConfig(cfg, config.CLITypeOpenAICompatible); len(got) != 0 {
		t.Fatalf("unconfigured openai-compatible models = %#v, want none", got)
	}
	withAccountModels(t, map[string]AccountModels{
		"qwen": {Models: []string{"qwen3-max", "qwen3-coder"}},
	})
	got := ModelsForConfig(cfg, config.CLITypeOpenAICompatible)
	if len(got) != 2 || got[0] != "qwen3-max" {
		t.Fatalf("ModelsForConfig = %#v", got)
	}
	if !IsValidModelForConfig(cfg, config.CLITypeOpenAICompatible, "qwen3-coder") {
		t.Error("a registry model should validate for its own provider")
	}
	if IsValidModelForConfig(cfg, config.CLITypeOpenAICompatible, "gpt-5.6-sol") {
		t.Error("openai-compatible must not inherit the first-party codex catalog")
	}
}

func TestModelsForAccountIsScopedToTheAccount(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		// Two codex accounts with disjoint model lists: a stock account that
		// inherits the built-in gpt list, and a local Qwen account.
		{Name: "codex", Type: config.CLITypeCodex},
		{Name: "codex-qwen", Type: config.CLITypeCodex},
	}}
	withAccountModels(t, map[string]AccountModels{
		"codex-qwen": {Models: []string{"Qwen/Qwen3"}},
	})

	// Stock account → built-in codex models; Qwen account → only Qwen.
	stock := ModelsForAccount(cfg, config.CLITypeCodex, "codex")
	if len(stock) == 0 || stock[0] != "gpt-5.6-sol" {
		t.Fatalf("ModelsForAccount(codex) = %#v, want built-in gpt list", stock)
	}
	qwen := ModelsForAccount(cfg, config.CLITypeCodex, "codex-qwen")
	if len(qwen) != 1 || qwen[0] != "Qwen/Qwen3" {
		t.Fatalf("ModelsForAccount(codex-qwen) = %#v, want [Qwen/Qwen3]", qwen)
	}

	if got := LatestModelForAccount(cfg, config.CLITypeCodex, "codex-qwen"); got != "Qwen/Qwen3" {
		t.Errorf("LatestModelForAccount(codex-qwen) = %q, want Qwen/Qwen3", got)
	}

	// Strict per-account validation: each account rejects the other's model.
	if IsValidModelForAccount(cfg, config.CLITypeCodex, "codex", "Qwen/Qwen3") {
		t.Errorf("stock codex must reject the Qwen account's model")
	}
	if IsValidModelForAccount(cfg, config.CLITypeCodex, "codex-qwen", "gpt-6-astra") {
		t.Errorf("codex-qwen must reject the stock account's gpt model")
	}
	if !IsValidModelForAccount(cfg, config.CLITypeCodex, "codex-qwen", "Qwen/Qwen3") {
		t.Errorf("codex-qwen must accept its own Qwen model")
	}

	// Empty account → first account of the type (stock codex here).
	first := ModelsForAccount(cfg, config.CLITypeCodex, "")
	if len(first) == 0 || first[0] != "gpt-5.6-sol" {
		t.Fatalf("ModelsForAccount(\"\") = %#v, want stock codex list", first)
	}
}

func TestSummaryModelForConfigKeepsCodexAdapterDefault(t *testing.T) {
	if got := SummaryModelForConfig(nil, config.CLITypeCodex); got != "" {
		t.Fatalf("SummaryModelForConfig(codex) = %q, want empty adapter default", got)
	}
}

func TestCheckModelAvailable(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "codex", Type: config.CLITypeCodex},
		{Name: "qwen", Type: config.CLITypeOpenAICompatible},
	}}
	// The admin dropped gpt-6-astra from the account's list; the qwen account
	// has no list at all, which is the fresh *-compatible state.
	withAccountModels(t, map[string]AccountModels{
		"codex": {Models: []string{"gpt-5.6-sol", "gpt-5.6-terra"}},
	})

	cases := []struct {
		name     string
		cfg      *config.Config
		provider string
		account  string
		model    string
		wantErr  string
	}{
		{name: "still offered", cfg: cfg, provider: config.CLITypeCodex, account: "codex", model: "gpt-5.6-terra"},
		{name: "empty model defers to the backend", cfg: cfg, provider: config.CLITypeCodex, account: "codex"},
		{name: "no live config", provider: config.CLITypeCodex, account: "codex", model: "gpt-6-astra"},
		{
			name: "withdrawn by admin", cfg: cfg, provider: config.CLITypeCodex, account: "codex",
			model: "gpt-6-astra", wantErr: "no longer available",
		},
		{
			// Aged out of the built-in catalog by a DayMug upgrade rather than
			// by an admin — same refusal, the id is still not on offer.
			name: "withdrawn by upgrade", cfg: cfg, provider: config.CLITypeCodex, account: "codex",
			model: "gpt-5.5", wantErr: "no longer available",
		},
		{
			name: "account has no catalog", cfg: cfg, provider: config.CLITypeOpenAICompatible, account: "qwen",
			model: "Qwen/Qwen3", wantErr: "no models configured",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckModelAvailable(tc.cfg, tc.provider, tc.account, tc.model)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckModelAvailable = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckModelAvailable = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Message(), tc.wantErr) {
				t.Errorf("message = %q, want it to contain %q", err.Message(), tc.wantErr)
			}
			if err.Status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", err.Status)
			}
		})
	}
}

// Saving a bare Opus 5.5 id must land on the 1M tier: --model passes the id
// through verbatim, so the bare spelling would cap every chat at 200K.
func TestNormalizeModelOverrides_OpusRunsAt1M(t *testing.T) {
	got := NormalizeModelOverrides(map[string]AccountModels{
		"default": {
			Models:       []string{"claude-sonnet-5", " claude-opus-5-5 ", "claude-opus-5-5[1m]"},
			SummaryModel: "claude-opus-5-5",
		},
	})
	entry := got["default"]
	want := []string{"claude-sonnet-5", "claude-opus-5-5[1m]"}
	if strings.Join(entry.Models, ",") != strings.Join(want, ",") {
		t.Errorf("models = %v, want %v", entry.Models, want)
	}
	if entry.SummaryModel != "claude-opus-5-5[1m]" {
		t.Errorf("summary model = %q, want claude-opus-5-5[1m]", entry.SummaryModel)
	}
}
