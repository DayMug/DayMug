package agent

import (
	"os"
	"syscall"
)

// killSignal returns the OS-specific signal used by tests to force-kill
// a child that has wedged. Linux uses SIGKILL; the constant lives behind
// this helper so a future Windows-aware build can override it without
// editing every test that needs an unconditional kill.
func killSignal() os.Signal {
	return syscall.SIGKILL
}
