package codexapp

import (
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// schemaSandboxModes / schemaSandboxPolicyTypes are transcribed from the
// app-server protocol schema (see sandbox.go for the regeneration command).
// They exist so a mapping that drifts back to the wrong casing fails here
// instead of at runtime, where Codex answers -32600 and the turn dies.
var (
	schemaSandboxModes = map[string]bool{
		"read-only": true, "workspace-write": true, "danger-full-access": true,
	}
	schemaSandboxPolicyTypes = map[string]bool{
		"dangerFullAccess": true, "readOnly": true, "externalSandbox": true, "workspaceWrite": true,
	}
)

func TestSandboxMappingsMatchProtocolSchema(t *testing.T) {
	noIsolation, err := agent.NewSandbox(nil)
	if err != nil {
		t.Fatal(err)
	}
	jailed := agent.RunRequest{Sandbox: noopSandbox{}}
	unrestrictedJail := agent.RunRequest{Sandbox: noopSandbox{}, Unrestricted: true}

	cases := []struct {
		name       string
		opts       agent.RunRequest
		wantMode   string
		wantPolicy string
	}{
		{"default", agent.RunRequest{}, "danger-full-access", "dangerFullAccess"},
		{"no-op sandbox", agent.RunRequest{Sandbox: noIsolation}, "danger-full-access", "dangerFullAccess"},
		{"read only", agent.RunRequest{ReadOnly: true}, "read-only", "readOnly"},
		{"jailed", jailed, "danger-full-access", "externalSandbox"},
		{"jail bypassed for unrestricted user", unrestrictedJail, "danger-full-access", "dangerFullAccess"},
		{"read only wins over jail", agent.RunRequest{Sandbox: noopSandbox{}, ReadOnly: true}, "read-only", "readOnly"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode := sandboxMode(tc.opts)
			if mode != tc.wantMode {
				t.Fatalf("sandboxMode = %q, want %q", mode, tc.wantMode)
			}
			if !schemaSandboxModes[mode] {
				t.Fatalf("sandboxMode %q is not a SandboxMode variant", mode)
			}
			policy := sandboxPolicy(tc.opts)
			gotType, _ := policy["type"].(string)
			if gotType != tc.wantPolicy {
				t.Fatalf("sandboxPolicy type = %q, want %q", gotType, tc.wantPolicy)
			}
			if !schemaSandboxPolicyTypes[gotType] {
				t.Fatalf("sandboxPolicy type %q is not a SandboxPolicy variant", gotType)
			}
		})
	}
}

type noopSandbox struct{}

func (noopSandbox) Wrap(argv []string, _ string, _ agent.WrapOpts) ([]string, []string, error) {
	return argv, nil, nil
}
