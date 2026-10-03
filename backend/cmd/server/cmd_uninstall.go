package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"
)

// cmdUninstall reverses bootstrap's service registration. Data is the
// default survivor: conversations, users and config live in the install dir
// and are not something a reinstall should cost, so only --purge removes it.
func cmdUninstall() {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	purge := fs.Bool("purge", false, "Also delete the install directory (config, database, user homes)")
	yes := fs.Bool("yes", false, "Don't ask for confirmation before --purge deletes anything")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fatalf("parse args: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot determine home directory: %v", err)
	}
	exe, err := resolveBinPath()
	if err != nil {
		fatalf("resolve binary path: %v", err)
	}
	installDir := filepath.Dir(exe)

	// Validate before touching the service, so a refused purge leaves the
	// install exactly as it was.
	if *purge {
		if err := checkPurgeTarget(installDir, home); err != nil {
			fatalf("refusing to purge %s: %v", installDir, err)
		}
	}

	switch runtime.GOOS {
	case "linux":
		removeLinuxUserService(os.Stdout, home)
	case "darwin":
		removeMacLaunchAgent(os.Stdout, home)
	default:
		fmt.Printf("No service to remove on %s.\n", runtime.GOOS)
	}

	if !*purge {
		fmt.Printf("\nKept %s (config, database, user homes).\n", installDir)
		fmt.Println("Delete it with `daymug uninstall --purge`, or reinstall to pick up where you left off.")
		return
	}
	if pid, running := serverRunningFrom(installDir, exe); running {
		fatalf("a DayMug server (pid %d) is still running from %s; stop it with `%s stop` first", pid, installDir, exe)
	}
	if !*yes {
		ok, err := confirmPurge(installDir)
		if err != nil {
			fatalf("%v (pass --yes to skip the prompt)", err)
		}
		if !ok {
			fmt.Printf("Kept %s.\n", installDir)
			return
		}
	}
	if err := os.RemoveAll(installDir); err != nil {
		fatalf("remove %s: %v", installDir, err)
	}
	fmt.Printf("Removed %s.\n", installDir)
}

// checkPurgeTarget is the guard between --purge and os.RemoveAll. The target
// is always the running binary's own directory, never a path from the
// command line; on top of that it must look like a DayMug install and must
// not be a directory whose loss would take unrelated files with it — a
// binary copied into $HOME or a source checkout would otherwise qualify.
func checkPurgeTarget(dir, home string) error {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		return errors.New("not an absolute path")
	}
	if dir == filepath.Dir(dir) {
		return errors.New("it is the filesystem root")
	}
	if home != "" {
		home = filepath.Clean(home)
		if rel, err := filepath.Rel(dir, home); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
			return errors.New("it is your home directory or contains it")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		return errors.New("no config.yaml in it, so it does not look like a DayMug install directory")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return errors.New("it is a git checkout")
	}
	return nil
}

// serverRunningFrom reports a server still serving out of dir, e.g. one
// started by hand with `daymug serve` that the service removal didn't stop.
func serverRunningFrom(dir, exe string) (int, bool) {
	pid, err := readPIDFile(filepath.Join(dir, "data", "daymug.pid"))
	if err != nil {
		return 0, false
	}
	return pid, pidRunsBinary(pid, exe)
}

// confirmPurge asks on the terminal. install scripts are commonly piped
// (curl | sh), which leaves stdin on the pipe, so the question goes to
// /dev/tty in that case rather than reading the script's own bytes.
func confirmPurge(dir string) (bool, error) {
	in := io.Reader(os.Stdin)
	if !term.IsTerminal(int(os.Stdin.Fd())) { // #nosec G115 -- fd fits in int.
		tty, err := os.Open("/dev/tty")
		if err != nil {
			return false, errors.New("no terminal to confirm on")
		}
		defer func() { _ = tty.Close() }()
		in = tty
	}
	fmt.Printf("Permanently delete %s, including the database and every user home under it? [y/N] ", dir)
	return readConfirmation(in), nil
}

func readConfirmation(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// removeLinuxUserService stops, disables and deletes the user unit. Each step
// tolerates failure: the goal is that nothing of ours is left registered, and
// a missing user bus must not keep the unit file around.
func removeLinuxUserService(w io.Writer, home string) {
	unitPath := systemdUserUnitPath(home)
	if _, err := os.Stat(unitPath); err != nil {
		_, _ = fmt.Fprintf(w, "No systemd --user unit at %s.\n", unitPath)
		return
	}
	busOK := true
	// disable --now also stops; serve drains in-flight turns first, which is
	// why this can take up to the unit's TimeoutStopSec.
	if out, err := systemctlUser("disable", "--now", systemdUnitName); err != nil {
		busOK = false
		_, _ = fmt.Fprintf(w, "! systemctl --user disable --now failed: %s\n", firstLine(orErr(out, err)))
	}
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		fatalf("remove %s: %v", unitPath, err)
	}
	// Earlier upgrade watchdogs added a start-limit drop-in to units that
	// predated the limit; such installs still carry it, and an empty .d
	// directory is all that would be left of the unit otherwise.
	dropIn := unitPath + ".d"
	_ = os.Remove(filepath.Join(dropIn, "50-start-limit.conf"))
	_ = os.Remove(dropIn)
	if busOK {
		_, _ = systemctlUser("daemon-reload")
		_, _ = systemctlUser("reset-failed", systemdUnitName)
	}
	_, _ = fmt.Fprintf(w, "Removed %s.\n", unitPath)
	if !busOK {
		_, _ = fmt.Fprintln(w, "  If it is still running, stop it from a login session: systemctl --user stop "+systemdUnitName+" && systemctl --user daemon-reload")
	}
}

func removeMacLaunchAgent(w io.Writer, home string) {
	plistPath := launchdPlistPath(home)
	if _, err := os.Stat(plistPath); err != nil {
		_, _ = fmt.Fprintf(w, "No LaunchAgent at %s.\n", plistPath)
		return
	}
	// unload -w stops the job and keeps launchd from loading it at login.
	_, _ = runCommand("launchctl", "unload", "-w", plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		fatalf("remove %s: %v", plistPath, err)
	}
	_, _ = fmt.Fprintf(w, "Removed %s.\n", plistPath)
}
