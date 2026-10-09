package main

import (
	"fmt"
	"runtime"
)

func cmdStatus() {
	fmt.Print(serviceStatus(runtime.GOOS))
}

// serviceStatus asks the per-user service manager bootstrap installed into.
// A system-scope `systemctl status daymug` never finds the unit, which lives
// under ~/.config/systemd/user.
func serviceStatus(goos string) string {
	var out []byte
	switch goos {
	case "linux":
		out, _ = runCommand("systemctl", "--user", "status", systemdUnitName)
	case "darwin":
		out, _ = runCommand("launchctl", "print", launchdTarget())
	default:
		return fmt.Sprintf("No service manager on %s; DayMug runs only when started with `daymug serve`.\n", goos)
	}
	// Both tools exit non-zero for a stopped or missing service; their output
	// still says which, so it is shown either way.
	return string(out)
}
