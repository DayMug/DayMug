package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// runCommand runs an external command and returns its combined output. Every
// systemctl / loginctl / launchctl call made by bootstrap, doctor and
// uninstall goes through it so tests can script the host: on a developer box
// the real daymug.service is usually the production instance.
var runCommand = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput() // #nosec G204 -- fixed binaries, args built here.
}

// probeHealth reports whether healthURL answers 200 before ctx expires. A
// variable so tests never open sockets.
var probeHealth = func(ctx context.Context, healthURL string) error {
	return service.ProbeHealth(ctx, &http.Client{Timeout: 3 * time.Second}, healthURL)
}

const systemdUnitName = "daymug.service"

func systemdUserUnitPath(home string) string {
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
}

func systemctlUser(args ...string) (string, error) {
	out, err := runCommand("systemctl", append([]string{"--user"}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// firstLine keeps a multi-line systemctl error readable inside a one-line
// status message.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// serviceOutcome is what bootstrap managed to do with the service, rendered
// by the closing summary. Problem/Manual are set when the operator has to
// finish the job by hand; that is a degraded install, not a failed one.
type serviceOutcome struct {
	State   string
	Started bool
	Problem string
	Manual  []string
}

// activateLinuxUserService loads the freshly written unit and, unless
// noStart, enables it and gets the new binary running. A re-install replaces
// the binary under a live service, so an active unit is restarted rather than
// left serving the old executable.
//
// systemctl --user needs a user bus, which containers and `su` sessions often
// lack. That is not worth a fatal: the unit file is in place and the operator
// only has to run the same commands from a real login session.
func activateLinuxUserService(noStart bool) serviceOutcome {
	enableNow := "systemctl --user enable --now " + systemdUnitName
	if out, err := systemctlUser("daemon-reload"); err != nil {
		return serviceOutcome{
			State:   "unit written, not started",
			Problem: "systemctl --user is unavailable (" + firstLine(orErr(out, err)) + "); this happens without a user session bus, e.g. in containers or `su` shells",
			Manual:  []string{"systemctl --user daemon-reload", enableNow},
		}
	}
	if noStart {
		return serviceOutcome{
			State:  "installed, not started (--no-start)",
			Manual: []string{enableNow},
		}
	}
	if out, err := systemctlUser("enable", systemdUnitName); err != nil {
		return serviceOutcome{
			State:   "installed, not enabled",
			Problem: "systemctl --user enable failed: " + firstLine(orErr(out, err)),
			Manual:  []string{enableNow},
		}
	}
	// is-active exits non-zero for every state but "active"; only the word
	// matters here.
	active, _ := systemctlUser("is-active", systemdUnitName)
	verb, state := "start", "enabled, started"
	if active == "active" {
		verb, state = "restart", "enabled, restarted on the new binary"
	} else {
		// A unit parked in "failed" by the start limiter refuses a plain start.
		_, _ = systemctlUser("reset-failed", systemdUnitName)
	}
	if out, err := systemctlUser(verb, systemdUnitName); err != nil {
		return serviceOutcome{
			State:   "enabled, not running",
			Problem: "systemctl --user " + verb + " failed: " + firstLine(orErr(out, err)),
			Manual:  []string{"journalctl --user -u " + systemdUnitName + " -n 50", "systemctl --user " + verb + " " + systemdUnitName},
		}
	}
	return serviceOutcome{State: state, Started: true}
}

func orErr(out string, err error) string {
	if out != "" {
		return out
	}
	return err.Error()
}

// lingerState is whether systemd keeps this user's manager alive without a
// login session. Off means a systemd --user service dies with the last SSH
// session and does not come back at boot.
type lingerState int

const (
	lingerUnknown lingerState = iota
	lingerOn
	lingerOff
)

func currentUsername() string {
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func detectLinger(username string) lingerState {
	if username == "" {
		return lingerUnknown
	}
	out, err := runCommand("loginctl", "show-user", username, "-p", "Linger")
	if err != nil {
		return lingerUnknown
	}
	switch strings.TrimSpace(string(out)) {
	case "Linger=yes":
		return lingerOn
	case "Linger=no":
		return lingerOff
	}
	return lingerUnknown
}

func lingerCommand(username string) string {
	if username == "" {
		username = "$USER"
	}
	return "sudo loginctl enable-linger " + username
}

// linuxUnitStatus is the slice of `systemctl --user show` doctor reports on.
type linuxUnitStatus struct {
	LoadState     string // "loaded", "not-found", ...
	UnitFileState string // "enabled", "disabled", ...
	ActiveState   string // "active", "inactive", "failed", ...
}

// queryLinuxUnit asks systemd for the unit's state in one call. show exits 0
// even for an unknown unit, so an error here means the user bus itself is
// unreachable.
func queryLinuxUnit() (linuxUnitStatus, error) {
	out, err := systemctlUser("show", systemdUnitName, "-p", "LoadState", "-p", "UnitFileState", "-p", "ActiveState")
	if err != nil {
		return linuxUnitStatus{}, errors.New(orErr(out, err))
	}
	var st linuxUnitStatus
	for _, line := range strings.Split(out, "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "LoadState":
			st.LoadState = val
		case "UnitFileState":
			st.UnitFileState = val
		case "ActiveState":
			st.ActiveState = val
		}
	}
	return st, nil
}

func launchdTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + launchdAgentLabel
}

// browseURL is the address an operator on this host types into a browser for
// a server listening on addr. Wildcard binds are reached via loopback.
func browseURL(addr string) string {
	if addr == "" {
		addr = defaultListenAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// healthURLFor is the URL the upgrade watchdog polls, so doctor and bootstrap
// call a server healthy by the same rule.
func healthURLFor(cfg *config.Config) string {
	addr := cfg.ListenAddr()
	if addr == "" {
		addr = defaultListenAddr
	}
	return service.NewUpgrader(&cfg.Upgrade, Version, addr).HealthURL()
}
