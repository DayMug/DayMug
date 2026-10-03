package agent_test

import (
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcli"
)

// backendCases lists every adapter we ship plus its expected
// self-reported capability matrix. New adapters MUST be added here so
// the next compile catches both the "I forgot to set Capabilities"
// regression and any drift between the constructor and what the
// handler relies on.
var backendCases = []struct {
	name string
	want string // Backend.Name()
	new  func() agent.Backend
	caps agent.Capabilities
}{
	{
		name: "claudecli",
		want: "claude-cli",
		new:  claudecli.NewBackend,
		caps: agent.Capabilities{
			SupportsCompaction:      true,
			SupportsThinkingStream:  true,
			SupportsRateLimitEvents: true,
			ReportsContextUsage:     true,
			ReportsCostUSD:          true,
		},
	},
	{
		name: "codexcli",
		want: "codex-cli",
		new:  codexcli.NewBackend,
		caps: agent.Capabilities{
			SupportsCompaction:      true,
			SupportsThinkingStream:  true,
			SupportsRateLimitEvents: false,
			// codex's token_count envelope carries no model_context_window
			// (the CLI moved it onto task_started), so this adapter emits no
			// context_usage frames at all.
			ReportsContextUsage: false,
			ReportsCostUSD:      false,
			// codex picks its thread ids; the caller must not mint one.
			AssignsSessionID: true,
		},
	},
}

// TestBackend_Conformance is a small contract test that all shipped
// adapters must keep passing. It guards two failure modes:
//
//   - A constructor wiring regression: NewBackend silently returns
//     nil (typo in factory map, copy-paste from a stub).
//   - A capabilities drift: an adapter's Capabilities() drops to its
//     zero value, which would silently hide /compact and the
//     thinking/rate-limit panes everywhere the handler gates on it.
//     The expected matrix is duplicated here on purpose so a careless
//     capability flip in the adapter trips a noisy test instead of
//     quietly disabling a feature.
func TestBackend_Conformance(t *testing.T) {
	for _, tc := range backendCases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.new()
			if b == nil {
				t.Fatalf("%s: NewBackend returned nil", tc.name)
			}
			if got := b.Name(); got != tc.want {
				t.Errorf("Name() = %q, want %q", got, tc.want)
			}
			if got := b.Capabilities(); got != tc.caps {
				t.Errorf("Capabilities() = %+v, want %+v", got, tc.caps)
			}
		})
	}
}

// TestBackend_CapabilitiesDifferAcrossAdapters is a sanity probe: if
// every adapter ever ends up reporting the same Capabilities matrix,
// the field stops driving any UI gating and the whole capability
// system has degenerated into dead code. Tripped when someone
// accidentally copies one adapter's matrix verbatim into another.
func TestBackend_CapabilitiesDifferAcrossAdapters(t *testing.T) {
	if len(backendCases) < 2 {
		t.Skip("only one adapter; capability divergence test is vacuous")
	}
	first := backendCases[0].new().Capabilities()
	for _, tc := range backendCases[1:] {
		if tc.new().Capabilities() == first {
			t.Errorf("%s reports identical Capabilities to %s — capability matrix is no longer differentiating adapters",
				tc.name, backendCases[0].name)
		}
	}
}
