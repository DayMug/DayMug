package streamcommon

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// waitErrForSignal starts a child that will outlive the test, sends it sig, and
// returns whatever proc.Wait() reports. Going through a real process (rather
// than hand-building a WaitStatus) is the point: the whole helper exists to
// decode what the OS actually hands back, and a fabricated status would just
// re-assert our own assumptions.
func waitErrForSignal(t *testing.T, sig syscall.Signal) error {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal %v: %v", sig, err)
	}
	err := cmd.Wait()
	if err == nil {
		t.Fatalf("Wait returned nil after %v", sig)
	}
	return err
}

func TestExplainExitError_SIGKILLNamesOOM(t *testing.T) {
	err := waitErrForSignal(t, syscall.SIGKILL)

	// Guard the premise: this is the opaque string the user used to get, and
	// the reason this helper exists. If a future Go release changes it, the
	// message below matters even more, not less.
	if got := err.Error(); got != "signal: killed" {
		t.Logf("note: Wait error text is %q (was %q when this was written)", got, "signal: killed")
	}

	got := ExplainExitError(err)
	if got == "" {
		t.Fatal("ExplainExitError returned empty for a SIGKILLed child")
	}
	for _, want := range []string{"SIGKILL", "out of memory", "OOM", "journalctl"} {
		if !strings.Contains(got, want) {
			t.Errorf("explanation %q missing %q", got, want)
		}
	}
}

func TestExplainExitError_SIGTERMReadsAsRoutine(t *testing.T) {
	got := ExplainExitError(waitErrForSignal(t, syscall.SIGTERM))
	if !strings.Contains(got, "terminated") {
		t.Fatalf("explanation %q does not describe a deliberate termination", got)
	}
	// Shutdown and cancellation are expected control flow; naming OOM here
	// would send operators chasing a memory problem that never happened.
	if strings.Contains(got, "OOM") {
		t.Errorf("SIGTERM explanation should not mention OOM: %q", got)
	}
}

func TestExplainExitError_NonSignalFailuresYieldNothing(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 3").Run()
	if exitErr == nil {
		t.Fatal("expected a non-zero exit")
	}

	cases := map[string]error{
		"nil":              nil,
		"plain error":      errors.New("upstream unreachable"),
		"non-zero exit":    exitErr,
		"wrapped non-exit": errors.New("wrapped: no such file"),
	}
	for name, err := range cases {
		if got := ExplainExitError(err); got != "" {
			t.Errorf("%s: want no explanation, got %q", name, got)
		}
	}
}

func TestExitDiagnostic(t *testing.T) {
	if got := ExitDiagnostic(nil); got != "exit ok" {
		t.Errorf("nil: got %q", got)
	}

	killed := ExitDiagnostic(waitErrForSignal(t, syscall.SIGKILL))
	if !strings.Contains(killed, "signal") || !strings.Contains(killed, "9") {
		t.Errorf("SIGKILL diagnostic %q should name the signal and its number", killed)
	}

	exitErr := exec.Command("sh", "-c", "exit 3").Run()
	if got := ExitDiagnostic(exitErr); !strings.Contains(got, "3") {
		t.Errorf("exit-3 diagnostic %q should carry the exit code", got)
	}

	if got := ExitDiagnostic(errors.New("boom")); !strings.Contains(got, "boom") {
		t.Errorf("plain error diagnostic %q should keep the message", got)
	}
}
