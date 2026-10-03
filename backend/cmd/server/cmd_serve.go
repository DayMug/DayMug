package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudeagentsdk"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/logfile"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

var (
	serveLookPath          = exec.LookPath
	serveLocateAgentSDK    = claudeagentsdk.LocateSDK
	serveProbeCodexApp     = agent.ProbeCodexAppServerSupport
	serveProbeClaudeConfig = agent.ProbeClaudeConfigDirSupport
)

const (
	httpShutdownTimeout = 30 * time.Second
	jobDrainTimeout     = 30 * time.Second
	// agentStopGrace bounds the SIGTERM→SIGKILL window given to agent child
	// processes that outlived the drain. Matches the per-child grace the
	// spawner applies on a normal cancel: enough for claude to flush its
	// session JSONL, short enough not to stall the restart.
	agentStopGrace = 5 * time.Second
	// postKillDrainTimeout is the second, short wait after the child
	// processes have been terminated. Killing the children makes their run
	// goroutines return, and those goroutines still persist a final result —
	// so we hold the process (and therefore the DB handle) open just long
	// enough for those writes to land instead of racing them with db.Close().
	postKillDrainTimeout = 10 * time.Second
)

func cmdServe() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to YAML config (overrides DAYMUG_CONFIG env)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		log.Fatalf("parse args: %v", err)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Mirror the standard logger to a file next to the database. stderr still
	// goes wherever the service manager puts it; this copy is the one DayMug
	// controls, and without it a failure the server chose only to log (an IM
	// attachment upload, say) is unrecoverable the moment the host's journal
	// stops persisting.
	if logWriter, logErr := logfile.Open(config.DefaultLogPath(), 0, 0); logErr != nil {
		log.Printf("file logging disabled: %v", logErr)
	} else {
		defer func() { _ = logWriter.Close() }()
		log.SetOutput(io.MultiWriter(os.Stderr, logWriter))
		log.Printf("mirroring log to %s", config.DefaultLogPath())
	}
	log.Printf(
		"task guardrail reminders: model_calls_per_turn=%d tool_calls_per_turn=%d cost_usd_per_turn=%.4f (runs stay uninterrupted; zero disables a threshold)",
		cfg.Guardrails.MaxModelCallsPerTurn,
		cfg.Guardrails.MaxToolCallsPerTurn,
		cfg.Guardrails.MaxCostUSDPerTurn,
	)

	// SQLite path is hard-coded relative to the running binary
	// (<binary_dir>/data/database.db) — no env var or YAML knob.
	app, err := buildApp(cfg, appOptions{
		DBPath:     config.DefaultDBPath(),
		FrontendFS: frontendFS(),
		Version:    Version,
	})
	if err != nil {
		log.Fatal(err) //nolint:gocritic // exitAfterDefer: only the log file's Close is skipped
	}
	defer app.Close()

	// Self-heal: if a previous upgrade left the state file at "validating"
	// (watchdog crashed/SIGKILLed before writing the success marker) and we
	// are already running the target version, mark it complete now.
	if binPath, berr := resolveBinPath(); berr == nil {
		service.SelfHealUpgradeState(binPath, Version)
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Server starting on %s", app.Addr)
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	if removePID, pidErr := writePIDFile(config.DefaultPIDPath(), os.Getpid()); pidErr != nil {
		log.Printf("pid file disabled, `daymug stop` will not find this instance: %v", pidErr)
	} else {
		defer removePID()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	if sig, err := awaitStop(serverErr, sigCh, liveStopPolicy(), log.Printf); sig == nil {
		if err != nil {
			// Process is dying; the deferred app.Close() will be skipped on
			// purpose — SQLite WAL is durable across abrupt exits.
			log.Fatalf("server failed: %v", err) //nolint:gocritic // exitAfterDefer: intentional
		}
		return
	}

	gracefulShutdown(app.Server, app.Drainer)
}

// validateAgentRuntimePrerequisites checks only the provider types this
// deployment configured, against the transport an admin selected for each.
// Claude on the Agent SDK needs Node + the SDK package; Claude on the CLI
// transport needs the `claude` binary instead. Codex is probed through its
// app-server subcommand on either transport, which also proves the binary
// runs. The two compatible types
// reuse those transports pointed at a third-party endpoint, so they need the
// same runtime — claude-compatible the Claude CLI, openai-compatible the
// codex app-server.
//
// transportFor is appSettings.transportFor: the probe must see the transports
// an admin saved, not the defaults, so it can only run after they are loaded.
func validateAgentRuntimePrerequisites(ctx context.Context, cfg *config.Config, transportFor func(provider string) string) error {
	providerTypes := make(map[string]bool)
	if cfg != nil {
		for _, provider := range cfg.ProviderSnapshot() {
			providerTypes[provider.Type] = true
		}
	}
	if providerTypes[config.CLITypeClaude] && transportFor(config.CLITypeClaude) == service.TransportCLI {
		if err := serveProbeClaudeConfig(ctx); err != nil {
			return fmt.Errorf("claude is set to the CLI transport, which requires the Claude CLI: %w", err)
		}
	} else if providerTypes[config.CLITypeClaude] {
		if _, err := serveLookPath("node"); err != nil {
			return errors.New("node.js 18+ is required by the Claude Agent SDK; install Node.js, then install the SDK with `npm install -g @anthropic-ai/claude-agent-sdk`")
		}
		if _, _, found := serveLocateAgentSDK(); !found {
			return errors.New("agent SDK for Claude not found; install it with `npm install -g @anthropic-ai/claude-agent-sdk`")
		}
	}
	if providerTypes[config.CLITypeCodex] || providerTypes[config.CLITypeOpenAICompatible] {
		if err := serveProbeCodexApp(ctx); err != nil {
			return err
		}
	}
	if providerTypes[config.CLITypeClaudeCompatible] {
		if err := serveProbeClaudeConfig(ctx); err != nil {
			return fmt.Errorf("a claude-compatible provider requires the Claude CLI: %w", err)
		}
	}
	return nil
}

// bootstrapAdmins flips is_admin=true on every existing user whose username is
// listed in admin.bootstrap_usernames. Missing rows are logged and ignored —
// the admin can create the user later (via UI or `daymug user add`) and
// the next restart will promote them. Idempotent.
func bootstrapAdmins(db store.Store, usernames []string) error {
	if len(usernames) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, u := range usernames {
		if u == "" {
			continue
		}
		user, err := db.GetUserByUsername(ctx, u)
		if errors.Is(err, store.ErrNotFound) {
			log.Printf("bootstrap admin: user %q not found, skipping (create via `daymug user add %s --email %s@example.com --admin` then restart)", u, u, u)
			continue
		}
		if err != nil {
			return err
		}
		if user.IsAdmin {
			continue
		}
		if err := db.SetUserAdmin(ctx, user.ID, true); err != nil {
			return err
		}
		log.Printf("bootstrap admin: promoted %q (id=%s)", u, user.ID)
	}
	return nil
}

// gracefulShutdown drains the server in stages:
//  1. Mark the drainer as draining so handlers refuse new long-running work.
//  2. server.Shutdown() in parallel — closes the listener and lets non-hijacked
//     HTTP requests finish.
//  3. drainer.WaitJobs() waits for in-flight Claude jobs (driven by WebSockets,
//     which are hijacked and so not covered by Shutdown) to wrap up.
//  4. Only if that times out: force-terminate the agent child process groups
//     and wait a second, short round for their run goroutines to persist.
//  5. server.Close() forces any remaining hijacked connections shut.
//
// Step 4 exists because every run's ctx is deliberately rooted in
// context.Background() (a drain must not cut off a reply that is mid-stream),
// so nothing in the process can cancel them once the drain window closes.
// Previously we just logged "forcing close" and returned: the children kept
// running, kept writing the very session JSONL the restarted server was about
// to `--resume`, and their PersistResult landed on a DB that cmdServe's
// deferred Close had already shut. systemd's KillMode=mixed papered over it on
// Linux service installs; macOS launchd and --skip-service had no backstop.
//
// Ordering matters and is load-bearing: everything here happens before
// gracefulShutdown returns, and cmdServe's deferred app.Close() — which closes
// the database — only runs after that, so a killed run still finds an open DB
// for its last write.
func gracefulShutdown(server *http.Server, drainer *service.Drainer) {
	drainer.StartDrain()

	httpCtx, httpCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer httpCancel()
	httpDone := make(chan error, 1)
	go func() {
		httpDone <- server.Shutdown(httpCtx)
	}()

	jobCtx, jobCancel := context.WithTimeout(context.Background(), jobDrainTimeout)
	defer jobCancel()
	if err := drainer.WaitJobs(jobCtx); err != nil {
		log.Printf("job drain timed out after %s: %v (terminating agent processes)", jobDrainTimeout, err)
		forceStopAgents(drainer)
	} else {
		log.Printf("all in-flight jobs completed")
	}

	// Pooled side-processes (the Codex app-server fleet) are attached to no job,
	// so the drain above never waited for them and forceStopAgents — which only
	// runs when that drain times out — never got the chance to kill them. Closing
	// them here covers the clean path too, which is the common one for a
	// self-upgrade restart.
	if n := agent.CloseRegistered(); n > 0 {
		log.Printf("closed %d pooled agent resource(s)", n)
	}

	if err := server.Close(); err != nil {
		log.Printf("force close: %v", err)
	}

	if err := <-httpDone; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http shutdown returned: %v", err)
	}
	log.Printf("server shutdown complete")
}

// forceStopAgents kills the agent child process groups that survived the drain
// window and then gives their run goroutines a bounded second chance to finish
// (the kill is what unblocks them: the child's streams hit EOF, Wait returns,
// and the run persists its partial result).
//
// A job that is still only *queued* on an account-pool slot cannot be unblocked
// from here — it is parked on a wait that no signal reaches — so the second
// wait is deliberately bounded and merely logs. Closing the DB a few seconds
// late is cheap; hanging a restart forever is not.
func forceStopAgents(drainer *service.Drainer) {
	if n := agent.TerminateAll(agentStopGrace); n > 0 {
		log.Printf("terminated %d agent process group(s)", n)
	}
	postCtx, postCancel := context.WithTimeout(context.Background(), postKillDrainTimeout)
	defer postCancel()
	if err := drainer.WaitJobs(postCtx); err != nil {
		log.Printf("%d job(s) still in flight %s after termination; closing the database anyway: %v",
			drainer.InFlight(), postKillDrainTimeout, err)
		return
	}
	log.Printf("all in-flight jobs settled after termination")
}
