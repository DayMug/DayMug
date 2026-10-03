// Package agent_test holds the tests that have to see the adapters. The
// adapters import agent, so a same-package test cannot import them back; an
// external test package can, and that is the only way to assert anything
// about the concrete backends from here.
package agent_test

import (
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudeagentsdk"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/codexapp"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcli"
)

// Optional features are wired by interface satisfaction, which no compiler
// check protects: rename SteerTurn and the adapter simply stops being a
// TurnSteeringBackend, every call site's type assertion starts returning
// false, and steering disappears from a running deployment with nothing
// failing to build. This pins the matrix so that rename fails here instead.
func TestShippedBackendsKeepTheirOptionalCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		backend agent.Backend
		want    agent.OptionalCapabilities
	}{
		{
			// The app-server transport drives a live thread, so it can take
			// both mid-turn input and answers to a provider-side question.
			name:    "codex-app-server",
			backend: codexapp.NewBackend(),
			want:    agent.OptionalCapabilities{Steering: true, UserQuestions: true},
		},
		{
			// `codex exec` is one process per turn: nothing to steer or
			// answer once it is running.
			name:    "codex-cli",
			backend: codexcli.NewBackend(),
			want:    agent.OptionalCapabilities{},
		},
		{
			name:    "claude-cli",
			backend: claudecli.NewBackend(),
			want:    agent.OptionalCapabilities{},
		},
		{
			// The SDK bridge steers and answers over its control channel.
			name:    "claude-agent-sdk",
			backend: claudeagentsdk.NewBackend(),
			want:    agent.OptionalCapabilities{Steering: true, UserQuestions: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := agent.OptionalCapabilitiesOf(tc.backend); got != tc.want {
				t.Fatalf("OptionalCapabilitiesOf(%s) = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

func TestOptionalCapabilitiesOfNilBackend(t *testing.T) {
	// Handlers look up a backend by provider name and can come back empty;
	// asking a missing backend what it can do must answer "nothing" rather
	// than panic.
	if got := agent.OptionalCapabilitiesOf(nil); got != (agent.OptionalCapabilities{}) {
		t.Fatalf("OptionalCapabilitiesOf(nil) = %+v, want the zero matrix", got)
	}
}
