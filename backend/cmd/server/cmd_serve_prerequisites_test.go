package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

func stubServeRuntimeProbes(t *testing.T) {
	t.Helper()
	origLookPath := serveLookPath
	origLocateSDK := serveLocateAgentSDK
	origProbeCodex := serveProbeCodexApp
	origProbeClaude := serveProbeClaudeConfig
	t.Cleanup(func() {
		serveLookPath = origLookPath
		serveLocateAgentSDK = origLocateSDK
		serveProbeCodexApp = origProbeCodex
		serveProbeClaudeConfig = origProbeClaude
	})
	serveLookPath = func(string) (string, error) { return "/usr/bin/node", nil }
	serveLocateAgentSDK = func() (string, string, bool) { return "/sdk/sdk.mjs", "1.0.0", true }
	serveProbeCodexApp = func(context.Context) error { return nil }
	serveProbeClaudeConfig = func(context.Context) error { return nil }
}

func TestValidateAgentRuntimePrerequisitesChecksConfiguredTransports(t *testing.T) {
	stubServeRuntimeProbes(t)
	var codexChecked bool
	serveProbeCodexApp = func(context.Context) error {
		codexChecked = true
		return nil
	}
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "codex", Type: config.CLITypeCodex},
	}}
	if err := validateAgentRuntimePrerequisites(context.Background(), cfg, service.TransportFor); err != nil {
		t.Fatal(err)
	}
	if !codexChecked {
		t.Fatal("Codex provider did not probe app-server")
	}
}

func TestValidateAgentRuntimePrerequisitesExplainsMissingAgentSDK(t *testing.T) {
	stubServeRuntimeProbes(t)
	serveLocateAgentSDK = func() (string, string, bool) { return "", "", false }
	cfg := &config.Config{Providers: []config.Provider{{Name: "default", Type: config.CLITypeClaude}}}
	err := validateAgentRuntimePrerequisites(context.Background(), cfg, service.TransportFor)
	if err == nil || !strings.Contains(err.Error(), "npm install -g @anthropic-ai/claude-agent-sdk") {
		t.Fatalf("error = %v, want Agent SDK install hint", err)
	}
}

func TestValidateAgentRuntimePrerequisitesExplainsMissingCodexAppServer(t *testing.T) {
	stubServeRuntimeProbes(t)
	serveProbeCodexApp = func(context.Context) error {
		return errors.New("codex app-server is unavailable; install with `npm install -g @openai/codex`")
	}
	cfg := &config.Config{Providers: []config.Provider{{Name: "default", Type: config.CLITypeCodex}}}
	err := validateAgentRuntimePrerequisites(context.Background(), cfg, service.TransportFor)
	if err == nil || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Fatalf("error = %v, want Codex install hint", err)
	}
}

func TestValidateAgentRuntimePrerequisitesSkipsUnconfiguredCodex(t *testing.T) {
	stubServeRuntimeProbes(t)
	serveProbeCodexApp = func(context.Context) error { return errors.New("must not run") }
	cfg := &config.Config{Providers: []config.Provider{{Name: "default", Type: config.CLITypeClaude}}}
	if err := validateAgentRuntimePrerequisites(context.Background(), cfg, service.TransportFor); err != nil {
		t.Fatalf("unconfigured Codex should not block startup: %v", err)
	}
}

// A Claude deployment switched to the CLI transport needs the claude binary,
// not the Agent SDK — a host that removed the SDK after the switch must still
// start.
func TestValidateAgentRuntimePrerequisitesFollowsTheClaudeTransport(t *testing.T) {
	stubServeRuntimeProbes(t)
	service.SetTransports(map[string]string{config.CLITypeClaude: service.TransportCLI})
	t.Cleanup(func() { service.SetTransports(nil) })
	serveLocateAgentSDK = func() (string, string, bool) { return "", "", false }
	cfg := &config.Config{Providers: []config.Provider{{Name: "default", Type: config.CLITypeClaude}}}
	if err := validateAgentRuntimePrerequisites(context.Background(), cfg, service.TransportFor); err != nil {
		t.Fatalf("CLI transport must not require the Agent SDK: %v", err)
	}
	serveProbeClaudeConfig = func(context.Context) error { return errors.New("claude not found") }
	if err := validateAgentRuntimePrerequisites(context.Background(), cfg, service.TransportFor); err == nil {
		t.Fatal("CLI transport without a claude binary must fail the probe")
	}
}
