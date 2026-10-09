package codexapp

import "github.com/DayMug/DayMug/backend/internal/agent"

// The app-server protocol names its isolation levels twice, in two different
// casing conventions, and mixing them up is not a soft failure: `sandbox` is
// deserialized as a plain enum, so an unknown variant makes serde reject the
// whole request with -32600 and the turn dies on its first RPC.
//
//	thread/start .sandbox            → SandboxMode   (kebab-case)
//	thread/resume .sandbox           → SandboxMode   (kebab-case)
//	turn/start .sandboxPolicy.type   → SandboxPolicy (camelCase, tagged union)
//
// The values below are transcribed from the protocol schema. Regenerate with:
//
//	codex app-server generate-json-schema --out <dir>
//	jq '.definitions.SandboxMode' <dir>/v2/ThreadStartParams.json
//	jq '.definitions.SandboxPolicy' <dir>/v2/TurnStartParams.json
const (
	sandboxModeReadOnly         = "read-only"
	sandboxModeWorkspaceWrite   = "workspace-write"
	sandboxModeDangerFullAccess = "danger-full-access"

	sandboxPolicyReadOnly         = "readOnly"
	sandboxPolicyExternalSandbox  = "externalSandbox"
	sandboxPolicyDangerFullAccess = "dangerFullAccess"
)

// sandboxMode maps a run request onto the thread-level SandboxMode enum.
//
// There is deliberately no "externalSandbox" here — SandboxMode has no such
// variant. When DayMug already wrapped the process in its own jail (bwrap),
// Codex must not add a second sandbox on top: its own bubblewrap cannot nest
// inside ours, and the boundary that matters is the outer one. So a jailed
// invocation asks Codex to stand down (danger-full-access at the thread level)
// and declares the real arrangement through the turn's sandboxPolicy, where
// externalSandbox does exist.
func sandboxMode(opts agent.RunRequest) string {
	if opts.ReadOnly {
		return sandboxModeReadOnly
	}
	return sandboxModeDangerFullAccess
}

// sandboxPolicy maps a run request onto the turn-level SandboxPolicy union.
//
// externalSandbox is claimed only when an isolation layer is actually in
// force: Sandbox.Wrap returns argv untouched for an unrestricted user, so a
// non-nil Sandbox alone does not mean the process is jailed.
func sandboxPolicy(opts agent.RunRequest) map[string]any {
	return map[string]any{"type": sandboxPolicyType(opts)}
}

func sandboxPolicyType(opts agent.RunRequest) string {
	if opts.ReadOnly {
		return sandboxPolicyReadOnly
	}
	if agent.SandboxIsolatesProcess(opts.Sandbox, opts.Unrestricted) {
		return sandboxPolicyExternalSandbox
	}
	return sandboxPolicyDangerFullAccess
}
