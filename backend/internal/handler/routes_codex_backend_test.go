package handler

import (
	"fmt"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// Claude and Codex default to their structured transports and follow the
// admin's selection from the next call on, with no rebuild of the map.
func TestBuildBackendsFollowTheSelectedTransport(t *testing.T) {
	t.Cleanup(func() { service.SetTransports(nil) })
	service.SetTransports(nil)
	backends := buildBackends(nil)
	cases := []struct {
		provider, transport string
		wantName            string
		wantSteering        bool
	}{
		{config.CLITypeClaude, "", "claude-agent-sdk", true},
		{config.CLITypeClaude, service.TransportCLI, "claude-cli", false},
		{config.CLITypeCodex, "", "codex-app-server", true},
		{config.CLITypeCodex, service.TransportCLI, "codex-cli", false},
	}
	for _, tc := range cases {
		service.SetTransports(map[string]string{tc.provider: tc.transport})
		b := backends[tc.provider]
		if got := b.Name(); got != tc.wantName {
			t.Errorf("%s on %q: backend = %q, want %q", tc.provider, tc.transport, got, tc.wantName)
		}
		if got := agent.OptionalCapabilitiesOf(b).Steering; got != tc.wantSteering {
			t.Errorf("%s on %q: steering = %v, want %v", tc.provider, tc.transport, got, tc.wantSteering)
		}
	}
}

// A provider type an operator can configure but that buildBackends never
// registers is a conversation that accepts messages and then dispatches to
// the wrong CLI (backendFor falls through to the default backend). Config
// validation and the backend map have to agree on exactly the same set.
func TestBuildBackendsCoversEverySupportedProviderType(t *testing.T) {
	backends := buildBackends(nil)
	for _, cliType := range config.SupportedCLITypes {
		if backends[cliType] == nil {
			t.Errorf("no backend registered for supported provider type %q", cliType)
		}
	}
	if len(backends) != len(config.SupportedCLITypes) {
		t.Errorf("buildBackends registered %d backends for %d supported types",
			len(backends), len(config.SupportedCLITypes))
	}
}

// The compatible types reuse the first-party transports. Pinning which one
// each reuses is what stops a future edit from, say, routing
// claude-compatible through the Agent SDK — whose bridge resolves a globally
// installed npm package rather than honouring ANTHROPIC_BASE_URL the way the
// CLI does.
func TestCompatibleBackendsReuseTheirTransports(t *testing.T) {
	backends := buildBackends(nil)
	if got := backends[config.CLITypeClaudeCompatible].Name(); got != "claude-compatible-cli" {
		t.Errorf("claude-compatible backend = %q, want the Claude CLI", got)
	}
	if got := backends[config.CLITypeOpenAICompatible].Name(); got != "openai-compatible-app-server" {
		t.Errorf("openai-compatible backend = %q, want the codex app-server", got)
	}
	// Cost is computed locally for both: their endpoints bill against their
	// own catalogs, so the UI must be told a cost will arrive.
	if !backends[config.CLITypeClaudeCompatible].Capabilities().ReportsCostUSD {
		t.Error("claude-compatible must report a locally-computed cost")
	}
}

// Every registered provider titles through its own backend. The Claude family
// keeps its minimal purpose-built invocation (resolved through the transport
// switch); the codex family goes through RunOneshot on the same backend chat
// uses.
func TestTitleGeneratorsRunOnEachProvidersBackend(t *testing.T) {
	t.Cleanup(func() { service.SetTransports(nil) })
	registry := newBackendRegistry(nil)
	fallback, perProvider := buildTitleGenerators(nil, registry)
	if len(perProvider) != len(config.SupportedCLITypes) {
		t.Fatalf("title generators for %d providers, want %d", len(perProvider), len(config.SupportedCLITypes))
	}
	service.SetTransports(nil)
	if lazy, ok := fallback.(lazyTitleGenerator); !ok || fmt.Sprintf("%T", lazy.build("")) != "*claudeagentsdk.titleGenerator" {
		t.Errorf("rows with no provider must keep titling through Claude, got %T", fallback)
	}
	cases := []struct {
		provider, transport, want string
	}{
		{config.CLITypeClaude, "", "*claudeagentsdk.titleGenerator"},
		{config.CLITypeClaude, service.TransportCLI, "*claudecli.titleGenerator"},
		{config.CLITypeClaudeCompatible, "", "*claudecli.titleGenerator"},
		{config.CLITypeCodex, "", "*codexcli.titleGenerator"},
		{config.CLITypeOpenAICompatible, "", "*codexcli.titleGenerator"},
	}
	for _, tc := range cases {
		service.SetTransports(map[string]string{tc.provider: tc.transport})
		backend, _ := registry.Lookup(tc.provider)
		if got := fmt.Sprintf("%T", titleGeneratorFor(backend, "")); got != tc.want {
			t.Errorf("%s on %q: title generator = %s, want %s", tc.provider, tc.transport, got, tc.want)
		}
	}
}
