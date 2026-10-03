package agent_test

import (
	"fmt"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudeagentsdk"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/codexapp"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcli"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// The Claude family must keep its purpose-built title invocation: a Claude
// RunOneshot loads every tool and MCP schema, roughly seventy times the input
// of the minimal flag set. Codex has no such path and titles through
// RunOneshot. Losing the TitleGenerator method silently moves Claude titles
// onto the expensive path, so the matrix is pinned here.
func TestShippedBackendsTitleGenerators(t *testing.T) {
	tests := []struct {
		name    string
		backend agent.Backend
		want    string // %T of the purpose-built generator; "" = RunOneshot
	}{
		{"claude-cli", claudecli.NewBackend(), "*claudecli.titleGenerator"},
		{"claude-compatible-cli", claudecli.NewBackendForProvider(config.CLITypeClaudeCompatible), "*claudecli.titleGenerator"},
		{"claude-agent-sdk", claudeagentsdk.NewBackend(), "*claudeagentsdk.titleGenerator"},
		{"codex-cli", codexcli.NewBackend(), ""},
		{"codex-app-server", codexapp.NewBackend(), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, ok := agent.TitleGeneratorOf(tc.backend, "")
			got := ""
			if ok {
				got = fmt.Sprintf("%T", g)
			}
			if got != tc.want {
				t.Fatalf("TitleGeneratorOf(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// A transport switch titles on whichever transport chat is on right now, so
// a CLI-only deployment never reaches for the SDK bridge (or the reverse).
func TestTitleGeneratorOfFollowsTheSelectedTransport(t *testing.T) {
	selected := "cli"
	sw := agent.NewTransportSwitch(func() string { return selected }, "sdk", map[string]agent.Backend{
		"sdk": claudeagentsdk.NewBackend(),
		"cli": claudecli.NewBackend(),
	})
	for transport, want := range map[string]string{
		"cli": "*claudecli.titleGenerator",
		"sdk": "*claudeagentsdk.titleGenerator",
	} {
		selected = transport
		g, ok := agent.TitleGeneratorOf(sw, "")
		if !ok || fmt.Sprintf("%T", g) != want {
			t.Errorf("transport %s: generator = %T (ok=%v), want %s", transport, g, ok, want)
		}
	}
}

// claude-compatible runs the same CLI as claude; the provider it is bound to
// is what drops rate-limit events (an Anthropic subscription header a
// third-party endpoint never sends) and names it in logs.
func TestClaudeCLICapabilitiesFollowTheProvider(t *testing.T) {
	first := claudecli.NewBackend()
	compat := claudecli.NewBackendForProvider(config.CLITypeClaudeCompatible)
	if got := first.Name(); got != "claude-cli" {
		t.Errorf("claude Name() = %q", got)
	}
	if got := compat.Name(); got != "claude-compatible-cli" {
		t.Errorf("claude-compatible Name() = %q", got)
	}
	if !first.Capabilities().SupportsRateLimitEvents {
		t.Error("first-party claude must report rate-limit events")
	}
	wantCompat := agent.Capabilities{
		SupportsCompaction:      true,
		SupportsThinkingStream:  true,
		SupportsRateLimitEvents: false,
		ReportsContextUsage:     true,
		ReportsCostUSD:          true,
	}
	if got := compat.Capabilities(); got != wantCompat {
		t.Errorf("claude-compatible Capabilities() = %+v, want %+v", got, wantCompat)
	}
}
