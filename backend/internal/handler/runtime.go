package handler

import (
	"context"
	"log"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudeagentsdk"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/codexapp"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcli"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// buildRuntime is the production assembly of service.Runtime. It lives in the
// handler layer only because this is where the concrete agent adapters may be
// imported; everything it builds is handed to the service layer as interfaces.
//
// With a nil cfg (focused tests) the config-derived collaborators — pool,
// sandbox, usage timezone — stay unset; RegisterRoutes only validates the
// runtime when a config is present.
func buildRuntime(s store.Store, drainer *service.Drainer, cfg *config.Config) *service.Runtime {
	rt := service.NewRuntime(s, newBackendRegistry(cfg))
	rt.Cfg = cfg
	rt.Drainer = drainer
	rt.TitleGen, rt.TitleGens = buildTitleGenerators(cfg, rt.Backends)
	// Hold the title CLI back a few seconds so it doesn't race the main turn's
	// OAuth token rewrite (notably codex). See service.AutoTitleStartDelay.
	rt.AutoTitleDelay = service.AutoTitleStartDelay
	rt.BarkSender = service.NewHTTPBarkSender()
	rt.PushDeerSender = service.NewHTTPPushDeerSender()
	if cfg == nil {
		return rt
	}
	rt.UsageLoc = cfg.UsageLocation()
	rt.Pool = service.NewPool(cfg)
	// cfg validation guarantees Sandbox.Type is supported when Enabled;
	// building the impl can still fail at runtime (bwrap missing from PATH).
	// We degrade instead of aborting so a missing optional binary can't take a
	// live deployment offline — but that means the operator asked for a jail
	// and did not get one, so say so loudly.
	sandbox, err := agent.NewSandbox(&cfg.Sandbox)
	if err != nil {
		log.Printf("SECURITY: sandbox %q requested but unavailable (%v) — starting anyway with NO isolation; agents run with the server user's full filesystem reach",
			cfg.Sandbox.Type, err)
	} else {
		rt.Sandbox = sandbox
	}
	return rt
}

// newBackendRegistry pairs the per-provider backends with the fallback used for
// unrouted conversations: the backend of the first configured provider, so a
// codex-only deployment doesn't carry a claude fallback that would silently
// spawn the wrong CLI for an unrouted request.
func newBackendRegistry(cfg *config.Config) *service.BackendRegistry {
	backends := buildBackends(cfg)
	fallback := backends[config.CLITypeClaude]
	if cfg != nil {
		if t := cfg.DefaultProviderType(); t != "" {
			if backend, ok := backends[t]; ok {
				fallback = backend
			}
		}
	}
	return service.NewBackendRegistry(backends, fallback)
}

// buildTitleGenerators gives every registered provider a title generator that
// runs on that provider's own backend. The summary model is resolved per call,
// not once at wiring time: it comes from the database, so an admin editing it
// must take effect on the next title without a restart. The generators are
// stateless structs, so rebuilding one per title is cheap next to the CLI turn
// it wraps.
func buildTitleGenerators(cfg *config.Config, backends *service.BackendRegistry) (agent.TitleGenerator, map[string]agent.TitleGenerator) {
	perProvider := make(map[string]agent.TitleGenerator)
	for _, provider := range backends.Providers() {
		backend, _ := backends.Lookup(provider)
		perProvider[provider] = lazyTitleGenerator{
			build: func(model string) agent.TitleGenerator { return titleGeneratorFor(backend, model) },
			model: func() string { return service.SummaryModelForConfig(cfg, provider) },
		}
	}
	// Legacy rows with no provider (and no configured default) keep titling
	// through Claude, as they always have.
	return perProvider[config.CLITypeClaude], perProvider
}

// titleGeneratorFor lets the backend pick how it summarizes. The Claude family
// supplies a minimal purpose-built invocation — resolved through the transport
// switch, so a CLI deployment without the SDK (or the reverse) titles on the
// runtime chat is known to have. Everything else runs RunOneshot on its own
// backend; codexcli's generator is that generic path, with the codex summary
// model as its default.
func titleGeneratorFor(backend agent.Backend, model string) agent.TitleGenerator {
	if g, ok := agent.TitleGeneratorOf(backend, model); ok {
		return g
	}
	return codexcli.NewTitleGeneratorWithBackend(backend, model)
}

// lazyTitleGenerator defers picking the summary model until a title is
// actually generated, so admin edits to the DB-backed model registry apply to
// the very next conversation instead of the next process restart.
type lazyTitleGenerator struct {
	build func(model string) agent.TitleGenerator
	model func() string
}

func (g lazyTitleGenerator) GenerateTitle(ctx context.Context, userPrompts []string, account agent.TitleAccount) (string, error) {
	return g.build(g.model()).GenerateTitle(ctx, userPrompts, account)
}

// buildBackends returns one backend per supported provider. Conversations are
// routed through the service.BackendRegistry built from this map.
func buildBackends(cfg *config.Config) map[string]agent.Backend {
	// Claude and Codex each run on whichever transport the admin selected
	// under Settings → Models; the switch resolves it per call so a change
	// applies from the next turn without a restart.
	claudeBackend := agent.NewTransportSwitch(
		func() string { return service.TransportFor(config.CLITypeClaude) },
		service.TransportAgentSDK,
		map[string]agent.Backend{
			service.TransportAgentSDK: claudeagentsdk.NewBackend(),
			service.TransportCLI:      claudecli.NewBackend(),
		},
	)
	// A claude-compatible endpoint runs Claude Code (the CLI, not the Agent
	// SDK) against whatever ANTHROPIC_BASE_URL the account's env sets, so it
	// inherits claude's session/tool plumbing wholesale; the adapter derives
	// its name and capability matrix from the provider it is bound to.
	claudeCompatBackend := claudecli.NewBackendForProvider(config.CLITypeClaudeCompatible)
	codexBackend := agent.NewTransportSwitch(
		func() string { return service.TransportFor(config.CLITypeCodex) },
		service.TransportAppServer,
		map[string]agent.Backend{
			service.TransportAppServer: codexapp.NewBackend(),
			service.TransportCLI:       codexcli.NewBackend(),
		},
	)
	openAICompatBackend := codexapp.NewBackendForProvider(config.CLITypeOpenAICompatible)
	return map[string]agent.Backend{
		config.CLITypeClaude:           claudeBackend,
		config.CLITypeCodex:            codexBackend,
		config.CLITypeClaudeCompatible: claudeCompatBackend,
		config.CLITypeOpenAICompatible: openAICompatBackend,
	}
}
