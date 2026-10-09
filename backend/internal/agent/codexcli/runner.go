// Package codexcli is the Backend adapter for the OpenAI Codex CLI
// (`codex exec --json` / `codex exec resume`). Codex picks its own
// session id at runtime and writes the rollout file under a
// YYYY/MM/DD tree, so the adapter's SessionExists / SessionLogPath
// have to walk the tree rather than predict a path the way claudecli
// does. The shared interface and event types live in the parent
// `agent` package.
package codexcli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/sessionlog"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

// codexBinary is the executable name we exec. Kept as a package var
// so tests can override it without touching $PATH; production runs
// the value through agent.ResolveAgentBinary which tries $PATH first
// and then falls back to common npm-style install dirs (nvm,
// npm-global, pnpm). Setting this to an absolute path bypasses the
// resolver entirely.
var codexBinary = "codex"

// runner implements agent.Backend by spawning the local `codex` CLI.
// Unlike the claude adapter (where we pre-generate a UUID and pass it
// via --session-id), codex assigns its own session_id on the first
// run and writes a rollout file under
// ~/.codex/sessions/.../<sid>.jsonl. We capture that id from the
// `session_configured` event and the caller persists it upstream so
// subsequent runs invoke `codex exec resume <sid>`.
type runner struct{}

// NewBackend returns a Backend driving the Codex CLI.
func NewBackend() agent.Backend {
	return &runner{}
}

// Name reports the stable backend identifier used in logs.
func (r *runner) Name() string { return "codex-cli" }

// Capabilities lists which optional adapter features handlers can
// rely on. Codex supports DayMug's /compact flow through RunOneshot:
// summarize the current session, save that summary as an assistant
// message, then rotate DayMug's stored session id so the next turn starts
// a fresh codex session. It emits no rate-limit frames.
//
// SupportsThinkingStream is true: RunWithSession passes
// `-c model_reasoning_summary=auto` on every turn, so codex emits a
// `reasoning` item the stream parser surfaces as KindThinkingDelta.
// Unlike claude's token-level thinking_delta, codex delivers each
// reasoning segment as one completed block, so the pill fills in a
// chunk at a time rather than character by character — still "live"
// at the granularity of the model's reasoning steps.
func (r *runner) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsCompaction:      true,
		SupportsThinkingStream:  true,
		SupportsRateLimitEvents: false,
		ReportsContextUsage:     false,
		ReportsCostUSD:          false,
		AssignsSessionID:        true,
	}
}

// SessionExists and SessionLogPath answer from the rollout tree codex writes
// whichever transport drove it; see sessionlog.CodexRollout.
func (r *runner) SessionExists(_, sessionID, claudeConfigDir string) bool {
	return sessionlog.CodexRollout(sessionID, claudeConfigDir) != ""
}

func (r *runner) SessionLogPath(_, sessionID, claudeConfigDir string) string {
	return sessionlog.CodexRollout(sessionID, claudeConfigDir)
}

// RunOneshot mirrors claudeRunner.RunOneshot — drain the stream via the
// shared streamcommon.DrainOneshot (prefer agent.KindResult over
// concatenated agent.KindDelta). Sub-agent frames are skipped even though
// codex doesn't currently spawn sub-agents; keeps the behaviour identical
// to claude's path so future codex sub-agent support drops in cleanly.
func (r *runner) RunOneshot(ctx context.Context, prompt, workDir string, opts agent.RunRequest) (string, error) {
	ch := make(chan agent.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.RunWithSession(ctx, prompt, workDir, opts, ch)
	}()
	return streamcommon.DrainOneshot(ch, errCh)
}

// RunWithSession builds the codex argv, spawns the child, and pipes its
// stdout through the codex-flavoured stream-json parser.
//
// The shared agent.RunRequest carries fields claude consumes via per-call
// CLI flags (SystemPrompt, McpConfigPath) that codex spells differently.
// We bridge what we can:
//
//   - SystemPrompt becomes `-c developer_instructions=...`, codex's
//     equivalent of claude's --append-system-prompt. Nothing is written to
//     disk; see systemPromptArgs.
//   - McpConfigPath is merged into <CODEX_HOME>/config.toml before
//     startup because codex only discovers MCP servers from that file.
func (r *runner) RunWithSession(ctx context.Context, prompt, workDir string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)

	seedMcpServers(opts.ConfigDir, opts.McpConfigPath)

	// Base flags. `--json` is the streaming-NDJSON output.
	// `--skip-git-repo-check` matches our usage pattern: daymug work_dirs are
	// not necessarily git repos (an agent's home, an arbitrary directory).
	// `--dangerously-bypass-approvals-and-sandbox` disables codex's sandbox
	// AND its approval prompts — required for a fully-detached server-side
	// runner, since there is no human at a TTY to confirm. We use this
	// instead of `--sandbox danger-full-access` because the latter is only
	// accepted on `codex exec` and rejected on `codex exec resume` (clap
	// errors with "unexpected argument '--sandbox' found"); the bypass flag
	// is the one path codex exposes on both subcommands.
	// reasoningArgs turns on codex's reasoning summaries so the model
	// emits a `reasoning` item per thinking segment (parsed into
	// KindThinkingDelta). Without this override codex defaults to no
	// summary and the thinking pill stays empty. `auto` is the lightest
	// mode that still produces a summary; `detailed` would cost more
	// summary tokens for marginal UI benefit.
	reasoningArgs := []string{"-c", "model_reasoning_summary=auto"}
	if opts.ThinkLevel != "" {
		effort := opts.ThinkLevel
		if effort == "max" {
			effort = "xhigh"
		}
		reasoningArgs = append(reasoningArgs, "-c", "model_reasoning_effort="+effort)
	}
	if opts.ContextWindow > 0 {
		// Codex only knows the windows of OpenAI's own models; without this a
		// third-party model would auto-compact against the wrong limit.
		reasoningArgs = append(reasoningArgs, "-c", "model_context_window="+strconv.Itoa(opts.ContextWindow))
	}
	promptArgs := systemPromptArgs(opts.SystemPrompt)

	// sandboxArgs selects codex's permission posture. The default is the
	// fully-detached bypass (no human at a TTY to approve). ReadOnly chat
	// turns instead pass `--sandbox read-only`, which lets the model read but
	// blocks writes, command side effects, and network — a "pure
	// conversation". Note: `--sandbox` is only accepted on `codex exec`, not
	// on `codex exec resume` (clap rejects it), so the resume branch below
	// always keeps the bypass and leans on the caller's isolated cwd instead.
	sandboxArgs := []string{"--dangerously-bypass-approvals-and-sandbox"}
	if opts.ReadOnly {
		sandboxArgs = []string{"--sandbox", "read-only"}
	}

	args := append([]string{
		"exec",
		"--json",
		"--skip-git-repo-check",
	}, sandboxArgs...)
	args = append(args, reasoningArgs...)
	args = append(args, promptArgs...)
	if opts.SessionID != "" && opts.IsResume {
		// codex exec resume [OPTIONS] [SESSION_ID] [PROMPT]. Put the
		// session id after all flags so clap matches it positionally
		// instead of trying to parse a UUID-looking string as a flag
		// value belonging to whichever option came right before.
		args = append([]string{
			"exec",
			"resume",
			"--json",
			"--skip-git-repo-check",
			"--dangerously-bypass-approvals-and-sandbox",
		}, reasoningArgs...)
		args = append(args, promptArgs...)
		args = append(args, opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// Resolve the binary, then let the sandbox optionally wrap. Same seam
	// claude uses — keeps any future container/jail backend a single
	// integration point.
	argv := append([]string{agent.ResolveAgentBinary(codexBinary)}, args...)
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
	processor := NewStreamProcessor(opts.Model)
	if opts.IsResume && opts.SessionID != "" {
		rolloutPath := r.SessionLogPath(workDir, opts.SessionID, opts.ConfigDir)
		previousUsage, err := readLatestCodexTotalUsage(rolloutPath)
		if err != nil {
			log.Printf("[codex] read usage baseline for session %s: %v", opts.SessionID, err)
		}
		processor.resumeUsage(previousUsage)
		if previousUsage == nil {
			// Without the previous cumulative counter there is no safe way to
			// distinguish this turn from the whole resumed thread. The processor
			// will drop cumulative-only usage for this run rather than inflate
			// the account; the assistant response still streams normally.
			log.Printf("[codex] usage baseline unavailable for resumed session %s; cumulative-only usage will not be recorded", opts.SessionID)
		}
	}

	log.Printf("[codex] argv=%q prompt=%s workDir=%q runner=%T",
		agent.RedactedArgv(argv, nil, []string{developerInstructionsKey + "="}), agent.PromptLogValue(prompt), workDir, spawner)
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
		return fmt.Errorf("start codex: %w", err)
	}

	stderrBuf, stderrWait := streamcommon.DrainStderr(proc.Stderr())
	defer stderrWait()

	// streamLines writes into an internal channel; a forwarder
	// goroutine peels off agent.KindError frames into capturedErr (so the
	// final wait error can carry the model-side reason) and forwards
	// everything else to outputCh unchanged. The synchronous close +
	// drain pair guarantees capturedErr is fully written before
	// proc.Wait() runs, so no mutex is needed.
	internalCh := make(chan agent.StreamEvent, 32)
	var capturedErr string
	var forwardErr error
	forwarderDone := make(chan struct{})
	go func() {
		defer close(forwarderDone)
		em := streamcommon.NewEmitter(runCtx, outputCh, "codex CLI")
		for evt := range internalCh {
			if evt.Kind == agent.KindError {
				if evt.Content != "" {
					capturedErr = evt.Content
				}
				continue
			}
			if em.Err() != nil {
				// Keep draining rather than returning: an undrained
				// internalCh would leave StreamLines blocked on a full
				// channel until its own emitter times out, delaying the
				// teardown by a second timeout for no gain.
				continue
			}
			if err := em.Emit(evt); err != nil {
				if em.Stalled() {
					// Nobody is reading this turn's output, so the child is
					// producing tokens for a stream that ends here. Kill it
					// now instead of paying for the rest of the turn.
					forwardErr = err
					runCancel()
				}
			}
		}
	}()
	activityProbe := agent.NewProcessActivityProbe(proc, func() []string {
		if opts.SessionID == "" {
			return nil
		}
		return []string{r.SessionLogPath(workDir, opts.SessionID, opts.ConfigDir)}
	})
	cfg := lineStream(processor)
	cfg.StallTimeout = opts.StallTimeout // zero → streamcommon.DefaultStallTimeout
	cfg.MaxSilentTimeout = opts.MaxSilentTimeout
	cfg.ActivityProbe = activityProbe
	streamErr := streamcommon.StreamLines(runCtx, proc.Stdout(), internalCh, cfg)
	if streamErr != nil {
		// Whatever ended the stream (stall watchdog, IO error, parent ctx
		// cancel) — make sure the child is killed before we block on
		// proc.Wait(), or we'll hang waiting for an upstream that already
		// stopped responding.
		runCancel()
	}
	close(internalCh)
	<-forwarderDone

	waitErr := proc.Wait()
	if waitErr != nil {
		// Log how the child ended regardless of which message wins in
		// resolveRunError: the chat row gets one sentence, but an operator
		// correlating a bad turn against dmesg needs the exit code / signal
		// spelled out somewhere.
		log.Printf("[codex] child exited: %s", streamcommon.ExitDiagnostic(waitErr))
	}
	if forwardErr != nil {
		// The consumer stalled and we killed the child over it, so every
		// other signal here — "signal: killed", a truncated stream — is a
		// consequence of that decision rather than the reason for it.
		return forwardErr
	}
	return resolveRunError(waitErr, capturedErr, stderrBuf.String(), streamErr)
}

// resolveRunError picks which of a run's competing failure signals the user
// should actually see.
//
// waitErr is the child's exit status, capturedErr the message codex emitted
// on stdout (agent.KindError, peeled off the stream so it never renders as a
// chat bubble mid-turn), stderrStr the child's stderr, and streamErr whatever
// ended the stdout pump.
//
// The key case is the last one: codex reports a failed turn ("usage limit
// hit", "model not supported") on stdout and still exits 0. That combination
// used to produce no error at all — the KindError frame was consumed
// in-runner and a happy exit left nothing behind — so the browser simply went
// quiet while the CLI had plainly failed. Returning capturedErr routes it
// through the caller's normal failure path: persisted as an error row and
// broadcast to the room.
func resolveRunError(waitErr error, capturedErr, stderrStr string, streamErr error) error {
	if waitErr != nil {
		// Prefer the codex-emitted message — it explains why the run
		// failed ("usage limit hit", "model X not supported"). Fall
		// back to stderr (which usually only carries the startup
		// banner) and finally to the bare exit-status error.
		if capturedErr != "" {
			return fmt.Errorf("%w: %s", waitErr, capturedErr)
		}
		if stderrStr != "" {
			return fmt.Errorf("%w: %s", waitErr, stderrStr)
		}
		// A stall-induced kill surfaces as the streamLines error rather
		// than a generic "signal: killed" from proc.Wait — prefer the
		// upstream-aware message so the user sees "upstream unreachable"
		// instead of an opaque exit code.
		if streamErr != nil {
			return streamErr
		}
		// Nothing above explained it, which is precisely the shape an OOM
		// kill takes: clean EOF on stdout, empty stderr, and a Wait error
		// that stringifies to "signal: killed". Decode the signal rather
		// than handing that back verbatim.
		if why := streamcommon.ExplainExitError(waitErr); why != "" {
			return fmt.Errorf("%w: %s", waitErr, why)
		}
		return waitErr
	}
	if capturedErr != "" {
		return errors.New(capturedErr)
	}
	return streamErr
}

// configDirEnv is the variable codex reads to locate its config/credentials
// dir. Codex has no exact analogue of CLAUDE_CONFIG_DIR — its config lives in
// ~/.codex by default, configurable via CODEX_HOME — so DayMug's per-account
// "ConfigDir" YAML field is exported through this name and one account block
// can serve both CLIs without renaming. The env composition itself is shared
// with claudecli (streamcommon.RunEnv).
const configDirEnv = "CODEX_HOME"

// lineStream binds codex's per-line parser to the shared stall-aware NDJSON
// pump. Same loop claudecli uses; codex differs only in the CLI name and in
// needing an EOF Flush, because its parser buffers cross-line state (the
// pending-result merge). Callers fill in the watchdog fields.
func lineStream(processor *StreamProcessor) streamcommon.LineStream {
	return streamcommon.LineStream{
		CLIName: "codex",
		Process: processor.Process,
		Flush:   processor.Flush,
	}
}
