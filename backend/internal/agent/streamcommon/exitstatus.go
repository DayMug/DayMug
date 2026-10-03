package streamcommon

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// ExplainExitError turns a child process's exit error into something a user can
// act on.
//
// The case that motivated this: when the kernel's OOM killer picks off a
// claude/codex child, the CLI's stdout pipe closes cleanly (so the NDJSON pump
// reports io.EOF, i.e. no error), stderr is empty (an OOM'd process never gets
// to write a parting message), and all that survives is proc.Wait()'s
// *exec.ExitError — whose Error() text is the bare string "signal: killed".
// That same five-word string is what a `ulimit` kill, an operator's `kill -9`,
// and our own TerminateAll leave behind, so the one failure mode that actually
// requires an operator response was indistinguishable from routine ones. Whole
// incidents got spent re-deriving "the box ran out of memory" from scratch.
//
// Returns "" when err is nil or isn't a death-by-signal, so callers can keep
// their existing precedence rules and only reach for this as the fallback.
func ExplainExitError(err error) string {
	sig, ok := terminatingSignal(err)
	if !ok {
		return ""
	}
	switch sig {
	case syscall.SIGKILL:
		// SIGKILL can't be trapped, so the child had no chance to explain
		// itself and there is genuinely nothing else to go on. Name the
		// likely causes in the order they actually occur in production, and
		// point at the log that discriminates between them.
		return "agent process was killed (SIGKILL) — most often the host ran out of memory " +
			"and the kernel OOM killer chose this process; it can also be an external " +
			"`kill -9` or a resource limit. Check `journalctl -k | grep -i oom` / `dmesg` " +
			"around the time of this turn."
	case syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP:
		// These are the signals we (or systemd) send on purpose during
		// shutdown and cancellation, so they read as routine.
		return fmt.Sprintf("agent process was terminated by %v (shutdown or cancellation)", sig)
	default:
		return fmt.Sprintf("agent process died on signal %v", sig)
	}
}

// terminatingSignal reports the signal that killed the child, if it died on one.
// A normal non-zero exit yields ok=false: WaitStatus.Signaled() is what
// separates "the program decided to fail" from "something outside it pulled the
// plug", and only the latter needs the attribution above.
func terminatingSignal(err error) (syscall.Signal, bool) {
	if err == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0, false
	}
	return ws.Signal(), true
}

// ExitDiagnostic renders a log-friendly summary of how a child ended. Runners
// log this on every non-nil Wait error: the chat message is necessarily a
// single sentence, but the log is where an operator correlating against
// dmesg needs the exit code and signal spelled out.
func ExitDiagnostic(err error) string {
	if err == nil {
		return "exit ok"
	}
	if sig, ok := terminatingSignal(err); ok {
		return fmt.Sprintf("killed by signal %v (%d)", sig, int(sig))
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit code %d", ee.ExitCode())
	}
	return "wait failed: " + err.Error()
}
