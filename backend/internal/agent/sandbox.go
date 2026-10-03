package agent

import "github.com/DayMug/DayMug/backend/internal/config"

// BindMount describes one extra bind-mount the caller wants the sandbox to
// expose to the spawned child. Source is the host path; Target is the path
// the child sees (often the same, but not required). Writable selects
// between read-write and read-only when a future sandbox implementation
// honours it. The current noop implementation ignores BindMounts entirely.
type BindMount struct {
	Source   string
	Target   string
	Writable bool
}

// WrapOpts carries per-invocation knobs that vary between sessions even when
// the singleton Sandbox impl is shared. New fields can be added without
// breaking implementations.
type WrapOpts struct {
	// ExtraBinds are per-call bind mounts layered on top of JailRoot, used to
	// make directories the run needs but that sit outside its jail (an agent's
	// DayMug scope, for one) visible at their own paths. The noop sandbox
	// ignores them; bwrap maps each one.
	ExtraBinds []BindMount
	// Unrestricted, when true, tells an isolating sandbox to pass the command
	// through unchanged for this invocation — the caller decided this user is
	// trusted with the server user's full filesystem reach (e.g. admins). The
	// noop impl ignores it (everything is already unrestricted).
	Unrestricted bool
	// JailRoot is the single directory a jailed (non-Unrestricted) invocation
	// may read and write — normally the conversation work_dir. Empty while
	// non-Unrestricted is a misconfiguration an isolating sandbox must reject
	// rather than silently grant full access.
	JailRoot string
	// ConfigDir is the CLI's config/state directory (CLAUDE_CONFIG_DIR /
	// CODEX_HOME). A jailed sandbox must keep it reachable read-write so the
	// CLI can read its credentials and write the session log; without it the
	// agent can't start. Empty means "no separate config dir to bind".
	ConfigDir string
}

// Sandbox wraps a Claude CLI invocation in an extra isolation layer so the
// spawned process can't escape the user's work directory. The interface
// intentionally takes the unwrapped command line plus working directory and
// returns whatever should actually be exec'd.
//
// A nil Sandbox or one whose Wrap returns the inputs unchanged means "no
// extra isolation" — claude runs as the server's OS user, sees the entire
// host filesystem subject to OS permissions only. The only implementation
// shipped today is noopSandbox; the interface is preserved so additional
// sandbox technologies can be plugged in later without touching callers.
type Sandbox interface {
	// Wrap takes the planned argv (argv[0] is the binary name), the
	// working directory, and per-call options (e.g. extra bind mounts),
	// and returns the rewritten argv to execute plus any additional
	// environment entries to inject.
	//
	// Implementations must preserve the semantic meaning of the original
	// command — same prompt, same output channel — and may not mutate
	// the inputs.
	Wrap(argv []string, workDir string, opts WrapOpts) (newArgv []string, extraEnv []string, err error)
}

// SandboxIsolatesProcess reports whether this invocation is actually wrapped
// in an outer process sandbox. Unknown implementations are treated as
// isolating: sharing a process too narrowly only costs memory, while sharing
// one too broadly can cross a security boundary.
func SandboxIsolatesProcess(sandbox Sandbox, unrestricted bool) bool {
	if sandbox == nil || unrestricted {
		return false
	}
	if reporter, ok := sandbox.(interface{ isolatesProcess() bool }); ok {
		return reporter.isolatesProcess()
	}
	return true
}

// NewSandbox returns the Sandbox impl matching the given config.
func NewSandbox(cfg *config.SandboxConfig) (Sandbox, error) {
	if cfg == nil || !cfg.Enabled {
		return noopSandbox{}, nil
	}
	switch cfg.Type {
	case "", config.SandboxNoop:
		return noopSandbox{}, nil
	case config.SandboxBwrap:
		return newBwrapSandbox(cfg)
	default:
		// Unknown types are caught by Validate() at config load; this is a
		// belt-and-suspenders fallback in case a future type is added but
		// not wired here.
		return noopSandbox{}, nil
	}
}

// noopSandbox returns its inputs unchanged. Used when sandboxing is
// disabled in YAML or when the operator selected the "noop" type.
type noopSandbox struct{}

func (noopSandbox) isolatesProcess() bool { return false }

func (noopSandbox) Wrap(argv []string, _ string, _ WrapOpts) ([]string, []string, error) {
	return argv, nil, nil
}
