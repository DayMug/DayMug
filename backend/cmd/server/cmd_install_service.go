package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// userServiceUnitTemplate is the systemd unit runInstallService installs
// on Linux. Lives under ~/.config/systemd/user/, runs as the invoking user,
// and uses WantedBy=default.target (user-bus equivalent of multi-user).
//
// No --config flag — config.yaml is auto-resolved from the binary's own
// directory (config.DefaultConfigFilename), so `daymug bootstrap` is the
// only thing that has to know about the layout.
//
// PATH is captured from the operator's interactive shell at install time
// and pinned here. Without this systemd --user gets a minimal PATH that
// usually omits ~/.local/bin and node_modules/.bin, so Node, the Agent SDK,
// or Codex may be invisible even though they work in the operator's terminal.
//
// StartLimitIntervalSec/StartLimitBurst bound the Restart=on-failure loop.
// serve() has several hard log.Fatalf paths that a bad environment makes
// permanent (Agent SDK missing, Codex app-server missing, OIDC discovery
// unreachable, corrupt
// config), and systemd's defaults — 5 starts per 10s window — can never
// trip with RestartSec=5, so such a binary would respawn forever instead
// of parking in `failed` where `systemctl --user status` shows it. 5
// starts per 300s means a genuine crash loop gives up after ~25s, while
// an occasional runtime crash (well under one per minute) still recovers.
// systemd ≥ 229 wants these keys in [Unit], not [Service].
//
// KillSignal=SIGINT because serve ignores a SIGTERM it cannot attribute to a
// stopping unit (see stopPolicy). Units installed before that still send
// SIGTERM, which the unit-state check accepts, so they need no rewrite.
const userServiceUnitTemplate = `[Unit]
Description=DayMug - Chat interface for Claude Code
After=network.target
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
ExecStart={{.ExecPath}} serve
WorkingDirectory={{.WorkDir}}
Restart=on-failure
RestartSec=5
KillMode=mixed
KillSignal=SIGINT
TimeoutStopSec=75
Environment=GIN_MODE=release
Environment=PATH={{.Path}}

[Install]
WantedBy=default.target
`

// launchdPlistTemplate is the LaunchAgent runInstallService installs on
// macOS. RunAtLoad starts it when the user logs in and KeepAlive lets launchd
// restart it after crashes and self-upgrades.
//
// No --config flag — config.yaml auto-resolves from the binary directory.
const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.ExecPath}}</string>
        <string>serve</string>
    </array>
    <key>WorkingDirectory</key>
    <string>{{.WorkDir}}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>GIN_MODE</key>
        <string>release</string>
        <key>PATH</key>
        <string>{{.Path}}</string>
    </dict>
    <key>StandardOutPath</key>
    <string>{{.StdoutLog}}</string>
    <key>StandardErrorPath</key>
    <string>{{.StderrLog}}</string>
</dict>
</plist>
`

type unitFields struct {
	ExecPath string
	WorkDir  string
	Path     string
}

type plistFields struct {
	Label     string
	ExecPath  string
	WorkDir   string
	StdoutLog string
	StderrLog string
	Path      string
}

const launchdAgentLabel = "com.daymug.daymug"

func renderUserServiceUnit(f unitFields) (string, error) {
	tmpl, err := template.New("unit").Parse(userServiceUnitTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, f); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func renderLaunchdPlist(f plistFields) (string, error) {
	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, f); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func launchdPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdAgentLabel+".plist")
}

// runInstallService registers the user-level system service and reports
// what state it was left in. Assumes runInstallLayout has already populated
// ~/.daymug/. noStart only matters on Linux: the macOS LaunchAgent is
// RunAtLoad, so loading it is starting it.
func runInstallService(noStart bool) serviceOutcome {
	// The system service is always wired to ~/.daymug/, no
	// matter where the current binary happens to live. That way running
	// bootstrap from a freshly-built dev binary, an unzipped release
	// tarball, or the installed copy itself all produce the same unit
	// pointing at the canonical layout.
	home, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot determine home directory: %v", err)
	}
	workDir := filepath.Join(home, installDirName)
	execPath := filepath.Join(workDir, "daymug")
	configPath := filepath.Join(workDir, "config.yaml")

	if _, statErr := os.Stat(execPath); statErr != nil {
		fatalf("binary %s not found: %v\n  hint: run `daymug bootstrap` to lay out ~/.daymug/ first", execPath, statErr)
	}
	if _, statErr := os.Stat(configPath); statErr != nil {
		fatalf("config %s not found: %v\n  hint: run `daymug bootstrap` to lay out ~/.daymug/ first", configPath, statErr)
	}

	// systemd --user (Linux) and launchd LaunchAgents (macOS) both run
	// with a minimal PATH that excludes ~/.local/bin, ~/.npm-global/bin,
	// nvm/volta shims, etc. Capture the operator's full PATH so the service can
	// resolve Node/Agent SDK and Codex exactly as the bootstrap probes did.
	servicePath := resolveServicePath()

	switch runtime.GOOS {
	case "linux":
		return installLinuxUserService(home, execPath, workDir, servicePath, noStart)
	case "darwin":
		return installMacLaunchAgent(home, execPath, workDir, servicePath)
	default:
		fatalf("service installation is only supported on linux and darwin (got %s)", runtime.GOOS)
		return serviceOutcome{}
	}
}

// resolveServicePath captures the interactive PATH for the service manager.
// Runtime prerequisites were already checked by bootstrap; there is no
// standalone Claude CLI to locate now that chat exclusively uses Agent SDK.
func resolveServicePath() string {
	current := os.Getenv("PATH")
	if current == "" {
		current = "/usr/local/bin:/usr/bin:/bin"
	}
	return current
}

func installLinuxUserService(home, execPath, workDir, servicePath string, noStart bool) serviceOutcome {
	unitPath := systemdUserUnitPath(home)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		fatalf("create %s: %v", filepath.Dir(unitPath), err)
	}

	content, err := renderUserServiceUnit(unitFields{
		ExecPath: execPath,
		WorkDir:  workDir,
		Path:     servicePath,
	})
	if err != nil {
		fatalf("render unit file: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		fatalf("write %s: %v", unitPath, err)
	}

	fmt.Printf("Installed user systemd unit: %s\n", unitPath)
	return activateLinuxUserService(noStart)
}

func installMacLaunchAgent(home, execPath, workDir, servicePath string) serviceOutcome {
	plistPath := launchdPlistPath(home)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		fatalf("create %s: %v", filepath.Dir(plistPath), err)
	}

	logDir := filepath.Join(workDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		fatalf("create %s: %v", logDir, err)
	}
	stdoutLog := filepath.Join(logDir, "daymug.out.log")
	stderrLog := filepath.Join(logDir, "daymug.err.log")

	content, err := renderLaunchdPlist(plistFields{
		Label:     launchdAgentLabel,
		ExecPath:  execPath,
		WorkDir:   workDir,
		StdoutLog: stdoutLog,
		StderrLog: stderrLog,
		Path:      servicePath,
	})
	if err != nil {
		fatalf("render plist: %v", err)
	}
	// Unload the previous copy before replacing it so launchd picks up the
	// new definition.
	_, _ = runCommand("launchctl", "unload", "-w", plistPath)
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		fatalf("write %s: %v", plistPath, err)
	}
	if out, err := runCommand("launchctl", "load", "-w", plistPath); err != nil {
		fatalf("launchctl load -w %s: %v\n%s", plistPath, err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("Installed launchd LaunchAgent: %s\n", plistPath)
	return serviceOutcome{State: "loaded, starts at login", Started: true}
}
