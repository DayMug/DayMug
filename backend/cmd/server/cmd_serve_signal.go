package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// honorSIGTERMEnv restores the old "any SIGTERM stops the server" behaviour
// for supervisors that can only send SIGTERM (supervisord, pm2, containers).
const honorSIGTERMEnv = "DAYMUG_HONOR_SIGTERM"

// Agent children run as the server's own OS user, so a stray `kill <pid>` or
// `pkill -f "daymug serve"` from an agent's shell can reach this process.
// That happened twice on the reference deployment (2026-09-26): a cleanup loop
// meant for a throwaway test instance matched production too, and SIGTERM took
// every in-flight conversation down with it. Both default kill tools send
// SIGTERM, so SIGTERM only stops the server when a service manager is
// verifiably stopping this very process. SIGINT (Ctrl-C, which the terminal
// delivers only to the foreground process group — agent children lead groups
// of their own) and `daymug stop` always work.
type stopPolicy struct {
	getenv func(string) string
	goos   string
	pid    int
	// systemdUnit reports the unit this process's cgroup belongs to; ok is
	// false when it is not a .service cgroup.
	systemdUnit func() (unit string, userManager bool, ok bool)
	// unitShow returns `systemctl show` output for ActiveState and MainPID.
	unitShow func(ctx context.Context, unit string, userManager bool) (string, error)
}

func liveStopPolicy() stopPolicy {
	return stopPolicy{
		getenv:      os.Getenv,
		goos:        runtime.GOOS,
		pid:         os.Getpid(),
		systemdUnit: selfSystemdUnit,
		unitShow:    systemctlShow,
	}
}

// decide reports whether sig should start a graceful shutdown, plus a short
// reason for the log line either way.
func (p stopPolicy) decide(sig os.Signal) (bool, string) {
	if sig != syscall.SIGTERM {
		return true, "interactive stop"
	}
	if p.getenv(honorSIGTERMEnv) == "1" {
		return true, honorSIGTERMEnv + "=1"
	}
	// launchd can only stop a job with SIGTERM and exposes no stop state to
	// check, so the LaunchAgent install keeps the old behaviour.
	if p.goos == "darwin" && p.getenv("XPC_SERVICE_NAME") == launchdAgentLabel {
		return true, "launchd stop"
	}
	if p.getenv("INVOCATION_ID") == "" {
		return false, "not started by systemd"
	}
	unit, userManager, ok := p.systemdUnit()
	if !ok {
		return false, "not running as a systemd service"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := p.unitShow(ctx, unit, userManager)
	if err != nil {
		// Failing closed here would make the unit unstoppable short of the
		// TimeoutStopSec SIGKILL; a stray kill landing exactly while systemd
		// is unreachable is the far smaller risk.
		return true, fmt.Sprintf("cannot query %s (%v)", unit, err)
	}
	state, mainPID := parseUnitShow(out)
	// An agent-launched test instance inherits INVOCATION_ID and sits in the
	// production unit's cgroup; only the unit's main process may follow the
	// unit's state.
	if mainPID != p.pid {
		return false, fmt.Sprintf("not the main process of %s", unit)
	}
	if state == "deactivating" {
		return true, unit + " is stopping"
	}
	return false, fmt.Sprintf("%s is %s, so the signal did not come from systemd", unit, state)
}

// awaitStop blocks until the HTTP server exits (returns its error, sig nil)
// or a signal the policy accepts arrives. Rejected signals are logged and
// ignored.
func awaitStop(serverErr <-chan error, sigCh <-chan os.Signal, policy stopPolicy, logf func(string, ...any)) (os.Signal, error) {
	for {
		select {
		case err := <-serverErr:
			return nil, err
		case sig := <-sigCh:
			stop, why := policy.decide(sig)
			if stop {
				logf("received %s (%s), starting graceful drain", sig, why)
				return sig, nil
			}
			logf("ignoring %s: %s. Stop the server with Ctrl-C, `daymug stop`, `kill -INT %d`, or its service manager (set %s=1 to honour SIGTERM)",
				sig, why, policy.pid, honorSIGTERMEnv)
		}
	}
}

// selfSystemdUnit reads the unified-hierarchy cgroup path; a systemd service
// lives at .../<name>.service, under user@<uid>.service for a user manager.
func selfSystemdUnit() (string, bool, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", false, false
	}
	return parseCgroupUnit(string(data))
}

func parseCgroupUnit(cgroup string) (string, bool, bool) {
	sc := bufio.NewScanner(strings.NewReader(cgroup))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		path := strings.TrimPrefix(line, "0::")
		unit := path[strings.LastIndex(path, "/")+1:]
		if !strings.HasSuffix(unit, ".service") {
			return "", false, false
		}
		return unit, strings.Contains(path, "/user@"), true
	}
	return "", false, false
}

func systemctlShow(ctx context.Context, unit string, userManager bool) (string, error) {
	args := []string{"show", unit, "-p", "ActiveState", "-p", "MainPID"}
	if userManager {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	return string(out), err
}

func parseUnitShow(out string) (state string, mainPID int) {
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			state = value
		case "MainPID":
			mainPID, _ = strconv.Atoi(value)
		}
	}
	return state, mainPID
}
