// Package claudecli is the Backend adapter for the Anthropic Claude
// CLI (`claude -p --output-format=stream-json`). It owns argv shape,
// stream-json parsing, and the on-disk session log layout
// (~/.claude/projects/<encoded-cwd>/<sid>.jsonl). The shared interface
// and event types live in the parent `agent` package.
package claudecli

import (
	"context"
	"fmt"
	"log"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/sessionlog"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// ProjectsEncoder is how Claude Code maps a cwd to a directory name under
// ~/.claude/projects; see sessionlog.ClaudeProjectsEncoder.
var ProjectsEncoder = sessionlog.ClaudeProjectsEncoder

// runner implements agent.Backend by spawning the local `claude` CLI.
// All state for one turn flows through agent.RunRequest; no per-turn
// state is retained between calls.
//
// `provider` is DayMug's CLIType for this Backend instance ("claude" or
// "claude-compatible"). Both run the same CLI binary, but a
// claude-compatible conversation has to re-bill against the local price
// table: Claude Code's own total_cost_usd is computed for Anthropic's
// catalog, which is not what a third-party endpoint charges.
type runner struct {
	provider string
}

// NewBackend returns a Backend driving the Claude CLI for a Claude
// conversation. Cost injection only fires when the provider is explicitly
// "claude-compatible", so this instance passes the CLI's own cost through.
func NewBackend() agent.Backend {
	return &runner{provider: "claude"}
}

// NewBackendForProvider returns a runner pre-bound to a specific DayMug
// CLIType. The same Claude CLI serves "claude" and "claude-compatible"
// conversations; the provider decides its name, its capability matrix, and
// whether the CLI's own cost is passed through or re-billed locally.
func NewBackendForProvider(provider string) agent.Backend {
	return &runner{provider: provider}
}

// firstParty reports whether this runner talks to Anthropic itself rather
// than a third-party endpoint behind ANTHROPIC_BASE_URL.
func (r *runner) firstParty() bool {
	return r.provider == "" || r.provider == config.CLITypeClaude
}

// Name reports the stable backend identifier used in logs and
// per-conversation provider routing.
func (r *runner) Name() string {
	if r.firstParty() {
		return "claude-cli"
	}
	return r.provider + "-cli"
}

// Capabilities lists the optional adapter features handlers can gate
// UI on. /compact is supported because we pre-generate session ids
// and can rotate them. A compatible endpoint speaks the same stream-json, so
// thinking, context usage and a (locally computed) cost all still arrive.
func (r *runner) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsCompaction:     true,
		SupportsThinkingStream: true,
		// Rate-limit events are an Anthropic subscription header; a
		// third-party endpoint doesn't send them, so don't promise the UI a
		// signal it will never receive.
		SupportsRateLimitEvents: r.firstParty(),
		ReportsContextUsage:     true,
		// A compatible endpoint bills against its own catalog, so the CLI's
		// total_cost_usd is wrong for it; the StreamProcessor substitutes a
		// locally-computed cost (admin-editable price table) instead.
		ReportsCostUSD: true,
	}
}

// SessionExists and SessionLogPath answer from the on-disk layout Claude Code
// shares with the Agent SDK transport; see sessionlog.ClaudePath.
func (r *runner) SessionExists(workDir, sessionID, claudeConfigDir string) bool {
	return sessionlog.Claude{}.SessionExists(workDir, sessionID, claudeConfigDir)
}

func (r *runner) SessionLogPath(workDir, sessionID, claudeConfigDir string) string {
	return sessionlog.ClaudePath(workDir, sessionID, claudeConfigDir)
}

// configDirEnv is the variable claude reads to locate the ~/.claude
// credentials dir for one account; DayMug's per-account "ConfigDir" YAML
// field is exported through it. The env composition itself is shared with
// codex (streamcommon.RunEnv) — only this name differs.
const configDirEnv = "CLAUDE_CONFIG_DIR"

// RunOneshot drains the stream-json output of a single claude invocation
// and returns just the final assistant text; see streamcommon.DrainOneshot
// for the result-vs-delta preference and sub-agent skipping.
func (r *runner) RunOneshot(ctx context.Context, prompt, workDir string, opts agent.RunRequest) (string, error) {
	ch := make(chan agent.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.RunWithSession(ctx, prompt, workDir, opts, ch)
	}()
	return streamcommon.DrainOneshot(ch, errCh)
}

func (r *runner) RunWithSession(ctx context.Context, prompt, workDir string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)

	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
		// DayMug surfaces clarifying questions through the chat UI as normal
		// assistant text, not through claude's structured AskUserQuestion tool —
		// the latter renders in the terminal only, so an invocation here would
		// silently stall waiting for a TTY response we can't deliver.
		"--disallowedTools", "AskUserQuestion",
	}
	if opts.SessionID != "" {
		if opts.IsResume {
			args = append(args, "--resume", opts.SessionID)
		} else {
			args = append(args, "--session-id", opts.SessionID)
		}
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	args = append(args, thinkLevelArgs(opts.ThinkLevel)...)
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.ReadOnly {
		// Chat-only API turns expose no built-in tools. This is an internal
		// runtime posture, not a configurable per-agent permission policy.
		args = append(args, "--tools", "")
	}
	if opts.McpConfigPath != "" {
		args = append(args, "--mcp-config", opts.McpConfigPath)
	}

	// Resolve the binary up front, then let the sandbox optionally wrap the
	// argv. argv[0] is the binary name; a wrapping sandbox replaces it with
	// its own and appends the original argv after a separator. With no
	// sandbox configured (or the noop impl), argv passes through unchanged.
	argv := append([]string{agent.ResolveAgentBinary("claude")}, args...)
	extraEnv := []string(nil)
	if opts.Sandbox != nil {
		var err error
		argv, extraEnv, err = opts.Sandbox.Wrap(argv, workDir, agent.WrapOpts{
			ExtraBinds:   opts.ExtraBinds,
			Unrestricted: opts.Unrestricted,
			JailRoot:     opts.JailRoot,
			ConfigDir:    opts.ConfigDir,
		})
		if err != nil {
			return fmt.Errorf("sandbox wrap: %w", err)
		}
	}
	spawner := opts.Spawner
	if spawner == nil {
		spawner = agent.NewSpawner()
		opts.Spawner = spawner
	}

	log.Printf("[claude] argv=%q prompt=%s workDir=%q account_cfg=%q runner=%T",
		agent.RedactedArgv(argv, []string{"--append-system-prompt"}, nil), agent.PromptLogValue(prompt), workDir, opts.ConfigDir, spawner)
	// Derive a cancellable ctx so a stall-detected return from streamLines
	// can immediately kill the child instead of letting proc.Wait() block
	// forever on the hung CLI process. exec.CommandContext on the spawner's
	// side sends SIGKILL when this fires.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	proc, err := agent.SpawnWithRetry(runCtx, spawner, agent.SpawnRequest{
		Argv:         argv,
		Env:          streamcommon.RunEnv(opts, extraEnv, configDirEnv),
		WorkDir:      workDir,
		InitialStdin: prompt,
	})
	if err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// PTY mode merges stderr into stdout; in that case stderrReader is
	// nil and we forward whatever bytes claude writes through the single
	// NDJSON parser. Pipe mode keeps stderr separate so a non-zero exit
	// code can carry the diagnostic blob along with the wrapped error.
	stderrBuf, stderrWait := streamcommon.DrainStderr(proc.Stderr())
	defer stderrWait()

	activityProbe := agent.NewProcessActivityProbe(proc, func() []string {
		if opts.SessionID == "" {
			return nil
		}
		return []string{r.SessionLogPath(workDir, opts.SessionID, opts.ConfigDir)}
	})
	cfg := lineStream(NewStreamProcessorForRun(r.provider, opts.Model))
	cfg.StallTimeout = opts.StallTimeout // zero → streamcommon.DefaultStallTimeout
	cfg.MaxSilentTimeout = opts.MaxSilentTimeout
	cfg.ActivityProbe = activityProbe
	streamErr := streamcommon.StreamLines(runCtx, proc.Stdout(), outputCh, cfg)
	if streamErr != nil {
		// Whatever ended the stream (stall watchdog, IO error, parent ctx
		// cancel) — make sure the child is killed before we block on
		// proc.Wait(), or we'll hang waiting for an upstream that already
		// stopped responding.
		runCancel()
	}

	if err := proc.Wait(); err != nil {
		// Log how the child ended regardless of which message wins below: the
		// chat row gets one sentence, but an operator correlating a bad turn
		// against dmesg needs the exit code / signal spelled out somewhere.
		log.Printf("[claude] child exited: %s", streamcommon.ExitDiagnostic(err))
		if stderrStr := stderrBuf.String(); stderrStr != "" {
			return fmt.Errorf("%w: %s", err, stderrStr)
		}
		// A stall-induced kill surfaces as the streamLines error rather
		// than a generic "signal: killed" from proc.Wait — prefer the
		// upstream-aware message so the user sees "upstream unreachable"
		// instead of an opaque exit code.
		if streamErr != nil {
			return streamErr
		}
		// Nothing above explained it, which is precisely the shape an
		// OOM kill takes: clean EOF on stdout, empty stderr, and a Wait
		// error that stringifies to "signal: killed". Decode the signal
		// rather than handing that back verbatim.
		if why := streamcommon.ExplainExitError(err); why != "" {
			return fmt.Errorf("%w: %s", err, why)
		}
		return err
	}
	return streamErr
}

func thinkLevelArgs(level string) []string {
	if level == "" {
		return nil
	}
	return []string{"--effort", level}
}

// lineStream binds claude's per-line parser to the shared stall-aware NDJSON
// pump. Everything else about the read loop — framing, stall watchdog,
// default budget, error wording — lives in streamcommon; claude only differs
// in the CLI name and the parser, and unlike codex it emits every event
// per-line, so it needs no EOF Flush. Callers fill in the watchdog fields.
func lineStream(processor *StreamProcessor) streamcommon.LineStream {
	return streamcommon.LineStream{
		CLIName: "claude",
		Process: processor.Process,
	}
}
