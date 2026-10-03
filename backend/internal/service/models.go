package service

import (
	"fmt"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// ProviderModels is the set of provider types this binary can spawn. It ships
// no model ids: which models an account offers — and in what order — is
// entirely what an admin configures under Settings → Models (stored in
// SQLite, see model_overrides.go). A compiled-in list went stale with every
// model release and silently offered ids an account could not run.
//
// The keys must still exist — IsValidProvider is a lookup in this map, and a
// missing key would make the type invalid everywhere (ProvidersForConfig,
// conversation create, /api/models).
var ProviderModels = map[string][]string{
	config.CLITypeClaude:           {},
	config.CLITypeCodex:            {},
	config.CLITypeClaudeCompatible: {},
	config.CLITypeOpenAICompatible: {},
}

// ModelAliases maps a bare Opus id to the 1M-context variant it is stored and
// run as. Without it an admin typing "claude-opus-5-5" into the model list got
// the 200K tier in every chat, because --model passes the id through verbatim.
// The frontend applies the same map before saving; this copy covers callers
// that write the registry through the API directly.
var ModelAliases = map[string]string{
	"claude-opus-5-5": "claude-opus-5-5[1m]",
}

// CanonicalModel returns the id a model is stored and run as.
func CanonicalModel(model string) string {
	if alias, ok := ModelAliases[model]; ok {
		return alias
	}
	return model
}

// Providers returns the provider names in a stable order so the
// /api/models response is deterministic across calls. The order is
// config.SupportedCLITypes' rather than a second list here — two hand-kept
// orderings drift, and this one is what the model picker renders.
func Providers() []string {
	return append([]string(nil), config.SupportedCLITypes...)
}

// ProvidersForConfig returns the configured provider types in database order,
// de-duplicated. A nil config falls back to every built-in provider for
// hand-constructed tests; a real, empty runtime registry stays empty.
func ProvidersForConfig(cfg *config.Config) []string {
	if cfg == nil {
		return Providers()
	}
	providers := cfg.ProviderSnapshot()
	if len(providers) == 0 {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		if p.Type == "" {
			continue
		}
		if !IsValidProvider(p.Type) {
			continue
		}
		if _, ok := seen[p.Type]; ok {
			continue
		}
		seen[p.Type] = struct{}{}
		out = append(out, p.Type)
	}
	return out
}

// LatestModelForConfig returns the default model id for a provider after
// applying the admin-saved model overrides.
func LatestModelForConfig(cfg *config.Config, provider string) string {
	models := ModelsForConfig(cfg, provider)
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

// IsValidProvider reports whether the supplied provider is one the server
// knows how to spawn. Mirrors config.Config.Validate's accepted list so the
// per-conversation provider can't drift from the global CLIType universe.
func IsValidProvider(provider string) bool {
	_, ok := ProviderModels[provider]
	return ok
}

// IsValidProviderForConfig reports whether the provider is both supported by
// the binary and configured for this server. A nil config preserves the
// built-in-only behaviour used by isolated tests; an empty runtime registry
// has no valid providers.
func IsValidProviderForConfig(cfg *config.Config, provider string) bool {
	if !IsValidProvider(provider) {
		return false
	}
	if cfg == nil {
		return true
	}
	providers := cfg.ProviderSnapshot()
	if len(providers) == 0 {
		return false
	}
	for _, p := range providers {
		if p.Type == provider {
			return true
		}
	}
	return false
}

// IsValidModelForConfig reports whether the (provider, model) pair is in the
// registry after applying the admin-saved model overrides. Used by the
// conversation create/update handlers to reject caller-supplied combos that
// would silently fail at exec time.
func IsValidModelForConfig(cfg *config.Config, provider, model string) bool {
	for _, m := range ModelsForConfig(cfg, provider) {
		if m == model {
			return true
		}
	}
	return false
}

// ProviderForModelForConfig returns the configured provider a model id
// belongs to, or "" when no configured provider lists it. A model belongs to
// exactly one provider in the registry, so the first match is unambiguous.
// Used to resolve a per-user default model id back to its provider when
// seeding a new conversation.
func ProviderForModelForConfig(cfg *config.Config, model string) string {
	if model == "" {
		return ""
	}
	for _, provider := range ProvidersForConfig(cfg) {
		if IsValidModelForConfig(cfg, provider, model) {
			return provider
		}
	}
	return ""
}

// accountNamesForType returns the configured account names of a provider
// type, in YAML order. Used by the type-scoped helpers, which have to fold
// several accounts' registries together.
func accountNamesForType(cfg *config.Config, provider string) []string {
	if cfg == nil {
		return nil
	}
	providers := cfg.ProviderSnapshot()
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		if p.Type == provider {
			out = append(out, p.Name)
		}
	}
	return out
}

// ModelsForConfig returns the type-level model list for a provider: the union
// of every configured account's models for that type, in YAML order (first
// account first), de-duplicated.
//
// This is the permissive, type-scoped view used by callers that only know the
// provider type — the external OpenAI-compatible API (which maps a bare model
// id back to a provider) and new-conversation default seeding. The picker and
// per-conversation validation use the stricter, account-scoped ModelsForAccount
// instead, because two accounts of the same type can expose disjoint models
// (e.g. a stock `codex` account on gpt-5.x vs a `codex-qwen` account on a local
// Qwen). The returned slice is a defensive copy.
func ModelsForConfig(cfg *config.Config, provider string) []string {
	names := accountNamesForType(cfg, provider)
	if len(names) == 0 {
		return append([]string(nil), ProviderModels[provider]...)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, name := range names {
		for _, m := range ModelsForAccount(cfg, provider, name) {
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// ModelsForAccount returns the model list for a single configured account,
// identified by its (provider type, account name) pair. Account names are
// globally unique, but the type is threaded through so a stale name can't
// resolve a model list from the wrong family. An empty account selects the
// first account of that type (the type's default). An account an admin has
// not configured offers no models. The returned slice is a defensive copy.
func ModelsForAccount(cfg *config.Config, provider, account string) []string {
	if account == "" {
		if names := accountNamesForType(cfg, provider); len(names) > 0 {
			account = names[0]
		}
	}
	if account != "" {
		if entry, ok := ModelOverrideFor(account); ok && len(entry.Models) > 0 {
			return append([]string(nil), entry.Models...)
		}
	}
	return append([]string{}, ProviderModels[provider]...)
}

// LatestModelForAccount returns the default model id for a specific account —
// the first entry of ModelsForAccount.
func LatestModelForAccount(cfg *config.Config, provider, account string) string {
	models := ModelsForAccount(cfg, provider, account)
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

// NoModelsConfiguredError explains an empty model registry for a provider.
// No provider type ships a built-in catalog, so a freshly added account has
// nothing selectable until an admin fills the registry in. That
// state is indistinguishable from "you asked for a model this provider does
// not have" at the validation call site, and the two need very different
// fixes, so callers branch on it to say which one they mean.
func NoModelsConfiguredError(provider string) string {
	return "no models configured for provider " + provider +
		" — add them under Settings → Models"
}

// IsValidModelForAccount reports whether the model belongs to the specific
// account's list. Stricter than IsValidModelForConfig: it rejects a model that
// is valid for the type but belongs to a sibling account (e.g. pinning the
// stock `codex` account but asking for the `codex-qwen` account's Qwen model).
func IsValidModelForAccount(cfg *config.Config, provider, account, model string) bool {
	for _, m := range ModelsForAccount(cfg, provider, account) {
		if m == model {
			return true
		}
	}
	return false
}

// ConfiguredSummaryModel returns the admin-configured summary model for an
// account: its own setting if it has one, otherwise the first one configured
// on a sibling account of the same type. Setting it on a single account
// therefore moves the whole type, which is what the removed type-scoped YAML
// field did.
//
// Empty means nobody configured one. Callers decide what that implies:
// title generation falls back to its adapter default, /compact leaves the
// conversation model alone.
func ConfiguredSummaryModel(cfg *config.Config, provider, account string) string {
	if account != "" {
		if entry, ok := ModelOverrideFor(account); ok && entry.SummaryModel != "" {
			return entry.SummaryModel
		}
	}
	for _, name := range accountNamesForType(cfg, provider) {
		if entry, ok := ModelOverrideFor(name); ok && entry.SummaryModel != "" {
			return entry.SummaryModel
		}
	}
	return ""
}

// SummaryModelForConfig is the title-generation view of ConfiguredSummaryModel.
// Empty means nobody configured one for this type, and each adapter falls back
// to its own default summary model.
func SummaryModelForConfig(cfg *config.Config, provider string) string {
	if model := ConfiguredSummaryModel(cfg, provider, ""); model != "" {
		return model
	}
	return ""
}

// CheckModelAvailable reports whether a conversation may still run the model
// stored on its row, given what the account offers *now*.
//
// Create and update validation only gates the picker, so without this check an
// admin unchecking a model under Settings → Models applied to new
// conversations only: every thread already pinned to the withdrawn id kept
// passing it to --model forever, and removing a model never took it out of
// circulation.
//
// Refusing rather than substituting the account's default is deliberate. The
// conversation's history was produced by the model on its row; swapping in a
// different one mid-thread changes what the user is talking to without telling
// them. An explicit error names the model and leaves the picker showing it, so
// the user can see what to move off.
//
// An empty model is not a choice at all — the backend picks its own default —
// and passes. So does a nil cfg: that means no live server configuration to
// check against (hand-built tests, bootstrap paths), the same tolerance
// IsValidProviderForConfig and LoadModelOverrides already grant.
func CheckModelAvailable(cfg *config.Config, provider, account, model string) *ServiceError {
	if cfg == nil || model == "" {
		return nil
	}
	if IsValidModelForAccount(cfg, provider, account, model) {
		return nil
	}
	if len(ModelsForAccount(cfg, provider, account)) == 0 {
		return BadRequest(NoModelsConfiguredError(provider))
	}
	where := "this provider account"
	if account != "" {
		where = fmt.Sprintf("provider account %q", account)
	}
	return BadRequest(fmt.Sprintf(
		"model %q is no longer available — an administrator removed it from %s;"+
			" switch this conversation to another model to continue",
		model, where))
}
