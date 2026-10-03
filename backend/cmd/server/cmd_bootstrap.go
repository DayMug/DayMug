package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudeagentsdk"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// cmdBootstrap is the single user-facing setup command: the operator runs
// `daymug bootstrap` once and it lays out ~/.daymug and registers the
// systemd / launchd service, after first surfacing what's already on the
// host and what they still need to install.
//
// Order of operations:
//
//  1. Print a prerequisite report — the Node.js + Agent SDK pair Claude chat
//     executes on, Codex app-server availability, and the system service
//     manager (systemd on Linux, launchd on darwin).
//  2. Report missing runtimes with installation instructions. Runtime needs are
//     provider-specific, so bootstrap still permits a Codex-only host without
//     Agent SDK and a Claude-only host without Codex; `serve` enforces the
//     requirements of the providers actually configured.
//  3. Run runInstallLayout — copy the running binary into ~/.daymug/,
//     scaffold config.yaml + data/ + users/ when missing.
//  4. Run runInstallService unless --skip-service was passed or the
//     platform's service manager isn't available. On Linux the unit is
//     enabled and started (restarted when already running, so a re-install
//     serves the new binary) unless --no-start; the macOS LaunchAgent is
//     RunAtLoad and always starts.
//  5. Print a short summary: what was installed, the service state, the URL
//     from the installed config, and the linger caveat on Linux.
//
// Re-running is safe: the binary is overwritten, config.yaml is preserved
// unless --force-config is set. scripts/install.sh relies on the
// --skip-service / --no-start flags and on `daymug doctor` existing.
func cmdBootstrap() {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	forceConfig := fs.Bool("force-config", false, "Regenerate config.yaml even if one already exists")
	skipService := fs.Bool("skip-service", false, "Skip systemd / launchd registration")
	noStart := fs.Bool("no-start", false, "Install the Linux systemd unit but don't enable or start it")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fatalf("parse args: %v", err)
	}

	prereqs := detectPrerequisites()
	printPrerequisiteReport(os.Stdout, prereqs)

	fmt.Println("==> Installing layout under ~/.daymug/")
	execPath, configPath := runInstallLayout(*forceConfig)

	sum := bootstrapSummary{ExecPath: execPath, ConfigPath: configPath}
	switch {
	case *skipService:
		sum.Service = serviceOutcome{State: "not installed (--skip-service)", Manual: []string{execPath + " serve"}}
	case !prereqs.ServiceManagerAvailable:
		sum.Service = serviceOutcome{
			State:  "not installed (no service manager found on " + runtime.GOOS + ")",
			Manual: []string{execPath + " serve"},
		}
	default:
		fmt.Println()
		fmt.Println("==> Registering system service")
		sum.Service = runInstallService(*noStart)
		sum.ServiceName = prereqs.ServiceManagerKind
		if runtime.GOOS == "linux" {
			sum.ServiceName += " " + systemdUnitName
			sum.LingerUser = currentUsername()
			sum.Linger = detectLinger(sum.LingerUser)
		}
	}

	if cfg, err := config.Load(configPath); err != nil {
		sum.ConfigErr = err.Error()
	} else {
		sum.URL = browseURL(cfg.ListenAddr())
		if sum.Service.Started {
			fmt.Printf("\nWaiting for %s to answer...\n", sum.URL)
			ctx, cancel := context.WithTimeout(context.Background(), bootstrapHealthWait)
			sum.HealthChecked = true
			sum.Healthy = probeHealth(ctx, healthURLFor(cfg)) == nil
			cancel()
		}
	}

	fmt.Println()
	printBootstrapSummary(os.Stdout, sum)
}

// bootstrapHealthWait covers serve's own startup probes (Agent SDK, Codex
// app-server, OIDC discovery, up to 15s each) with room to spare.
const bootstrapHealthWait = 45 * time.Second

type bootstrapSummary struct {
	ExecPath      string
	ConfigPath    string
	ServiceName   string
	Service       serviceOutcome
	URL           string
	ConfigErr     string
	HealthChecked bool
	Healthy       bool
	LingerUser    string
	Linger        lingerState
}

// printBootstrapSummary is the last thing bootstrap prints, so it carries
// only what the operator acts on next. The pre-flight report above it
// already covers missing runtimes.
func printBootstrapSummary(w io.Writer, s bootstrapSummary) {
	var b strings.Builder
	b.WriteString("==> Done\n")
	fmt.Fprintf(&b, "  binary:   %s\n", s.ExecPath)
	fmt.Fprintf(&b, "  config:   %s\n", s.ConfigPath)
	if s.ServiceName != "" {
		fmt.Fprintf(&b, "  service:  %s: %s\n", s.ServiceName, s.Service.State)
	} else {
		fmt.Fprintf(&b, "  service:  %s\n", s.Service.State)
	}
	switch {
	case s.ConfigErr != "":
		fmt.Fprintf(&b, "  URL:      unknown, config does not load: %s\n", s.ConfigErr)
	case s.HealthChecked && !s.Healthy:
		fmt.Fprintf(&b, "  URL:      %s (not answering yet)\n", s.URL)
	default:
		fmt.Fprintf(&b, "  URL:      %s\n", s.URL)
	}

	if s.Service.Problem != "" {
		fmt.Fprintf(&b, "\n! %s\n", s.Service.Problem)
	}
	if len(s.Service.Manual) > 0 {
		b.WriteString("\nTo start DayMug, run:\n")
		for _, cmd := range s.Service.Manual {
			fmt.Fprintf(&b, "  %s\n", cmd)
		}
	}
	if s.Linger == lingerOff {
		fmt.Fprintf(&b, "\n! Linger is off for %s: the service stops when you log out of every\n", s.LingerUser)
		b.WriteString("  session (including SSH) and does not start at boot. To keep it running:\n")
		fmt.Fprintf(&b, "    %s\n", lingerCommand(s.LingerUser))
	}
	fmt.Fprintf(&b, "\nTroubleshoot: %s doctor\n", s.ExecPath)
	_, _ = io.WriteString(w, b.String())
}

// prerequisites captures what the bootstrap pre-flight detected on the
// host.
type prerequisites struct {
	OS                      string
	NodeFound               bool
	NodePath                string
	AgentSDKFound           bool
	AgentSDKPath            string
	AgentSDKVersion         string
	CodexFound              bool
	CodexPath               string
	CodexAppServerFound     bool
	CodexAppServerError     string
	ServiceManagerAvailable bool
	ServiceManagerKind      string // "systemd --user" on Linux, "launchd" on darwin
}

// detectPrerequisites runs the host probes used by the report and bootstrap
// dispatch. The only process probe is `codex app-server --help`; it does not
// open an account session or mutate Codex state.
func detectPrerequisites() prerequisites {
	p := prerequisites{OS: runtime.GOOS}
	if path, err := exec.LookPath("node"); err == nil {
		p.NodeFound = true
		if resolved, resErr := filepath.EvalSymlinks(path); resErr == nil {
			p.NodePath = resolved
		} else {
			p.NodePath = path
		}
	}
	p.AgentSDKPath, p.AgentSDKVersion, p.AgentSDKFound = claudeagentsdk.LocateSDK()
	resolvedCodex := agent.ResolveAgentBinary("codex")
	if resolvedCodex != "codex" {
		p.CodexFound = true
		p.CodexPath = resolvedCodex
	} else if path, err := exec.LookPath("codex"); err == nil {
		p.CodexFound = true
		p.CodexPath = path
	}
	if p.CodexFound {
		if resolved, err := filepath.EvalSymlinks(p.CodexPath); err == nil {
			p.CodexPath = resolved
		}
		probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := agent.ProbeCodexAppServerSupport(probeCtx)
		cancel()
		p.CodexAppServerFound = err == nil
		if err != nil {
			p.CodexAppServerError = err.Error()
		}
	}
	switch runtime.GOOS {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err == nil {
			p.ServiceManagerAvailable = true
			p.ServiceManagerKind = "systemd --user"
		}
	case "darwin":
		// launchctl ships with macOS — assume present, but be defensive.
		if _, err := exec.LookPath("launchctl"); err == nil {
			p.ServiceManagerAvailable = true
			p.ServiceManagerKind = "launchd"
		}
	}
	return p
}

// printPrerequisiteReport renders the pre-flight summary the operator
// sees before any filesystem mutation. Built into a single string so
// the OS-write happens once (matches the pattern in cmd_install.go and
// keeps errcheck quiet) and tests can assert on the rendered output.
func printPrerequisiteReport(w io.Writer, p prerequisites) {
	var b strings.Builder
	b.WriteString("==> Pre-flight\n\n")
	b.WriteString("Claude runtime (required when a claude provider is configured):\n")
	if p.NodeFound && p.AgentSDKFound {
		fmt.Fprintf(&b, "  [ok]  node:              %s\n", p.NodePath)
		fmt.Fprintf(&b, "  [ok]  claude-agent-sdk:  %s %s\n", p.AgentSDKVersion, p.AgentSDKPath)
	} else {
		if p.NodeFound {
			fmt.Fprintf(&b, "  [ok]  node:              %s\n", p.NodePath)
		} else {
			b.WriteString("  [!!]  node:              not found on PATH (Node.js 18+ required)\n")
		}
		if !p.AgentSDKFound {
			b.WriteString("  [!!]  claude-agent-sdk:  not found\n")
			b.WriteString("        install:           npm install -g @anthropic-ai/claude-agent-sdk\n")
		}
		b.WriteString("        Claude chat only supports the Agent SDK; there is no CLI fallback.\n")
	}

	b.WriteString("\nCodex (required when a codex provider is configured):\n")
	switch {
	case !p.CodexFound:
		b.WriteString("  [!!]  codex:             not found\n")
		b.WriteString("        install:           npm install -g @openai/codex\n")
		b.WriteString("        verify:            codex app-server --help\n")
	case !p.CodexAppServerFound:
		fmt.Fprintf(&b, "  [!!]  codex:             %s\n", p.CodexPath)
		b.WriteString("  [!!]  codex app-server:  unavailable\n")
		b.WriteString("        upgrade:           npm install -g @openai/codex\n")
		b.WriteString("        verify:            codex app-server --help\n")
		if p.CodexAppServerError != "" {
			fmt.Fprintf(&b, "        detail:            %s\n", p.CodexAppServerError)
		}
	default:
		fmt.Fprintf(&b, "  [ok]  codex:             %s\n", p.CodexPath)
		b.WriteString("  [ok]  codex app-server\n")
	}

	b.WriteString("\nSystem service manager:\n")
	if p.ServiceManagerAvailable {
		fmt.Fprintf(&b, "  [ok]  %s\n", p.ServiceManagerKind)
	} else {
		switch p.OS {
		case "linux":
			b.WriteString("  [--]  systemd --user:    not found — service install will be skipped.\n")
		case "darwin":
			b.WriteString("  [--]  launchd:           launchctl not found — service install will be skipped.\n")
		default:
			b.WriteString("  [--]  service manager:   not found — service install will be skipped.\n")
		}
	}
	b.WriteString("\n")
	_, _ = io.WriteString(w, b.String())
}
