package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// cmdDoctor answers "I installed it, why isn't it working?" without the
// operator having to know systemd, launchd or the config resolution rules. It
// is read-only: it probes the same prerequisites bootstrap reports, then the
// installed state, and exits non-zero when something DayMug cannot run
// without is missing so scripts/install.sh can surface it.
func cmdDoctor() {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to YAML config (overrides DAYMUG_CONFIG env)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fatalf("parse args: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot determine home directory: %v", err)
	}

	prereqs := detectPrerequisites()
	printPrerequisiteReport(os.Stdout, prereqs)
	checks := runDoctorChecks(doctorInput{
		Prereqs:    prereqs,
		OS:         runtime.GOOS,
		Home:       home,
		ConfigPath: *configPath,
		Username:   currentUsername(),
	})
	if !printDoctorChecklist(os.Stdout, checks) {
		os.Exit(1)
	}
}

type checkLevel int

const (
	checkPass checkLevel = iota
	// checkWarn is worth fixing but DayMug can run without it: a host may
	// legitimately skip the service, or serve only one provider family.
	checkWarn
	checkFail
)

type doctorCheck struct {
	Level  checkLevel
	Name   string
	Detail string
	Hint   string
}

type doctorInput struct {
	Prereqs    prerequisites
	OS         string
	Home       string
	ConfigPath string
	Username   string
}

// doctorHealthTimeout is short on purpose: doctor asks whether the server is
// up now, not whether it will come up.
const doctorHealthTimeout = 3 * time.Second

func runDoctorChecks(in doctorInput) []doctorCheck {
	checks := []doctorCheck{runtimeCheck(in.Prereqs)}
	cfg, cfgCheck := configCheck(in.ConfigPath, in.Home)
	checks = append(checks, cfgCheck)

	switch in.OS {
	case "linux":
		checks = append(checks, linuxServiceChecks(in)...)
	case "darwin":
		checks = append(checks, macServiceCheck(in))
	}

	if cfg != nil {
		checks = append(checks, healthCheck(cfg))
	}
	return checks
}

// runtimeCheck only fails when neither agent runtime works: which one is
// needed depends on the providers an admin configured in the UI, which live
// in the database rather than config.yaml. The pre-flight report above the
// checklist already lists what is missing and how to install it.
func runtimeCheck(p prerequisites) doctorCheck {
	claude := p.NodeFound && p.AgentSDKFound
	codex := p.CodexFound && p.CodexAppServerFound
	c := doctorCheck{Name: "agent runtime"}
	switch {
	case claude && codex:
		c.Detail = "Claude Agent SDK, Codex app-server"
	case claude:
		c.Detail = "Claude Agent SDK (no Codex app-server: codex providers won't run)"
	case codex:
		c.Detail = "Codex app-server (no Claude Agent SDK: claude providers won't run)"
	default:
		c.Level = checkFail
		c.Detail = "neither the Claude Agent SDK nor the Codex app-server is usable"
		c.Hint = "npm install -g @anthropic-ai/claude-agent-sdk   or   npm install -g @openai/codex"
	}
	return c
}

// configCheck resolves the config the way serve does. When that finds
// nothing — doctor run from a downloaded copy rather than the installed
// binary — it falls back to ~/.daymug/config.yaml, the file the installed
// service reads.
func configCheck(path, home string) (*config.Config, doctorCheck) {
	cfg, err := config.Load(path)
	if errors.Is(err, config.ErrNoConfigPath) && home != "" {
		installed := filepath.Join(home, installDirName, config.DefaultConfigFilename)
		if _, statErr := os.Stat(installed); statErr == nil {
			cfg, err = config.Load(installed)
		}
	}
	if err != nil {
		c := doctorCheck{Level: checkFail, Name: "config", Detail: err.Error()}
		if errors.Is(err, config.ErrNoConfigPath) {
			c.Detail = "no config.yaml next to the binary and DAYMUG_CONFIG is unset"
			c.Hint = "run `daymug bootstrap`, or point DAYMUG_CONFIG / --config at your config.yaml"
		} else {
			c.Hint = "fix the file, then re-run `daymug doctor`"
		}
		return nil, c
	}
	return cfg, doctorCheck{Name: "config", Detail: cfg.Path()}
}

func linuxServiceChecks(in doctorInput) []doctorCheck {
	const name = "service"
	if !in.Prereqs.ServiceManagerAvailable {
		return []doctorCheck{{
			Level: checkWarn, Name: name, Detail: "systemctl not found",
			Hint: "run `daymug serve` under a supervisor of your choice",
		}}
	}
	unitPath := systemdUserUnitPath(in.Home)
	if _, err := os.Stat(unitPath); err != nil {
		return []doctorCheck{{
			Level: checkWarn, Name: name, Detail: "no systemd --user unit at " + unitPath,
			Hint: "run `daymug bootstrap` to install it, or keep running `daymug serve` yourself",
		}}
	}
	st, err := queryLinuxUnit()
	if err != nil {
		return []doctorCheck{{
			Level: checkWarn, Name: name, Detail: "systemctl --user unreachable: " + firstLine(err.Error()),
			Hint: "run doctor from a regular login session (not `su`, not a container shell)",
		}}
	}
	if st.LoadState != "loaded" {
		return []doctorCheck{{
			Level: checkFail, Name: name, Detail: "unit file exists but systemd has not loaded it (" + st.LoadState + ")",
			Hint: "systemctl --user daemon-reload && systemctl --user enable --now " + systemdUnitName,
		}}
	}

	checks := make([]doctorCheck, 0, 3)
	enabled := doctorCheck{Name: "service enabled", Detail: st.UnitFileState}
	if st.UnitFileState != "enabled" {
		enabled.Level = checkWarn
		enabled.Hint = "systemctl --user enable " + systemdUnitName + "   (start with the user manager)"
	}
	checks = append(checks, enabled)

	active := doctorCheck{Name: "service active", Detail: st.ActiveState}
	if st.ActiveState != "active" {
		active.Level = checkFail
		active.Hint = "systemctl --user start " + systemdUnitName + "   (logs: journalctl --user -u " + systemdUnitName + " -n 50)"
	}
	checks = append(checks, active)

	linger := doctorCheck{Name: "linger"}
	switch detectLinger(in.Username) {
	case lingerOn:
		linger.Detail = "on: the service survives logout and starts at boot"
	case lingerOff:
		linger.Level = checkWarn
		linger.Detail = "off: the service stops when you log out of every session (including SSH)"
		linger.Hint = lingerCommand(in.Username)
	default:
		linger.Level = checkWarn
		linger.Detail = "unknown (loginctl show-user failed)"
		linger.Hint = "check with `loginctl show-user " + in.Username + " -p Linger`; enable with " + lingerCommand(in.Username)
	}
	return append(checks, linger)
}

func macServiceCheck(in doctorInput) doctorCheck {
	plistPath := launchdPlistPath(in.Home)
	if _, err := os.Stat(plistPath); err != nil {
		return doctorCheck{
			Level: checkWarn, Name: "service", Detail: "no LaunchAgent at " + plistPath,
			Hint: "run `daymug bootstrap` to install it, or keep running `daymug serve` yourself",
		}
	}
	if _, err := runCommand("launchctl", "print", launchdTarget()); err != nil {
		return doctorCheck{
			Level: checkFail, Name: "service", Detail: "LaunchAgent not loaded",
			Hint: "launchctl load -w " + plistPath,
		}
	}
	return doctorCheck{Name: "service", Detail: "LaunchAgent " + launchdAgentLabel + " loaded"}
}

func healthCheck(cfg *config.Config) doctorCheck {
	url := healthURLFor(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), doctorHealthTimeout)
	defer cancel()
	if err := probeHealth(ctx, url); err != nil {
		return doctorCheck{
			Level: checkFail, Name: "server", Detail: url + " not answering",
			Hint: "start the service (see above) or run `daymug serve` in a terminal to see why it exits",
		}
	}
	return doctorCheck{Name: "server", Detail: browseURL(cfg.ListenAddr()) + " is up"}
}

// printDoctorChecklist renders the checklist and reports whether every hard
// requirement passed.
func printDoctorChecklist(w io.Writer, checks []doctorCheck) bool {
	var b strings.Builder
	b.WriteString("==> Checks\n\n")
	failed, warned := 0, 0
	for _, c := range checks {
		mark := "✓"
		switch c.Level {
		case checkWarn:
			mark = "!"
			warned++
		case checkFail:
			mark = "✗"
			failed++
		}
		fmt.Fprintf(&b, "  %s %-16s %s\n", mark, c.Name, c.Detail)
		if c.Hint != "" {
			fmt.Fprintf(&b, "      fix: %s\n", c.Hint)
		}
	}
	b.WriteString("\n")
	switch {
	case failed > 0:
		fmt.Fprintf(&b, "%d problem(s) need fixing", failed)
		if warned > 0 {
			fmt.Fprintf(&b, ", %d warning(s)", warned)
		}
		b.WriteString(".\n")
	case warned > 0:
		fmt.Fprintf(&b, "DayMug can run; %d warning(s) above.\n", warned)
	default:
		b.WriteString("All good.\n")
	}
	_, _ = io.WriteString(w, b.String())
	return failed == 0
}
