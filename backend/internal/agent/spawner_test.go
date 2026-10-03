package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPipeSpawner_OneShot drives the historical "pipe in, pipe out, EOF
// when stdin reader is drained" flow. We pick `cat` as the child so the
// test runs without any project-local binary; on Linux it is always on
// PATH and faithfully echoes whatever stdin delivered before EOF.
func TestPipeSpawner_OneShot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := PipeSpawner{}.Spawn(ctx, SpawnRequest{
		Argv:         []string{"cat"},
		InitialStdin: "hello pipe\n",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := string(out); got != "hello pipe\n" {
		t.Fatalf("stdout = %q, want %q", got, "hello pipe\n")
	}
	if proc.Stdin() != nil {
		t.Fatalf("Stdin() should be nil in one-shot mode")
	}
	if proc.Stderr() == nil {
		t.Fatalf("Stderr() should be non-nil in pipe mode")
	}
}

// TestPipeSpawner_Interactive verifies the stdin pipe stays open and the
// initial-stdin prologue is delivered without closing it.
func TestPipeSpawner_Interactive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := PipeSpawner{}.Spawn(ctx, SpawnRequest{
		Argv:         []string{"cat"},
		InitialStdin: "first\n",
		Interactive:  true,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	stdin := proc.Stdin()
	if stdin == nil {
		t.Fatalf("Stdin() should be non-nil in interactive mode")
	}
	// Read the prologue, then send a second line and close stdin so cat exits.
	go func() {
		// Give the prologue goroutine a moment, then send a second line.
		time.Sleep(50 * time.Millisecond)
		_, _ = stdin.Write([]byte("second\n"))
		_ = stdin.Close()
	}()
	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("stdout = %q, want both 'first' and 'second'", got)
	}
}

// TestPtySpawner_OneShot checks that a one-shot prompt reaches the child over
// a pipe while stdout stays a terminal: `codex exec` rejects a TTY stdin, the
// PTY would echo the prompt into the output stream, and canonical mode would
// truncate a line past 4095 bytes.
func TestPtySpawner_OneShot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prompt := strings.Repeat("x", 10000)
	proc, err := PtySpawner{}.Spawn(ctx, SpawnRequest{
		Argv: []string{"sh", "-c",
			`[ -t 0 ] && echo stdin=tty || echo stdin=pipe; [ -t 1 ] && echo stdout=tty; wc -c`},
		InitialStdin: prompt,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	out, err := io.ReadAll(proc.Stdout())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	got := string(out)
	for _, want := range []string{"stdin=pipe", "stdout=tty", "10000"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "xxx") {
		t.Fatalf("stdout = %q, prompt was echoed into the output stream", got)
	}
	if proc.Stdin() != nil {
		t.Fatalf("Stdin() should be nil in PTY one-shot mode")
	}
	if proc.Stderr() != nil {
		t.Fatalf("Stderr() should be nil in PTY mode (merged into Stdout)")
	}
}

// TestPtySpawner_Interactive opens a PTY and verifies the master fd is
// usable both as a reader and a writer.
func TestPtySpawner_Interactive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := PtySpawner{}.Spawn(ctx, SpawnRequest{
		Argv:        []string{"cat"},
		Interactive: true,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	stdin := proc.Stdin()
	if stdin == nil {
		t.Fatalf("Stdin() should be non-nil in PTY interactive mode")
	}
	go func() {
		_, _ = stdin.Write([]byte("interactive pty\n"))
		// EOT closes the line on the slave side; closing the master is
		// what actually ends cat in PTY mode (see ptyProcess.Wait).
		time.Sleep(50 * time.Millisecond)
		_, _ = stdin.Write([]byte{0x04})
	}()
	// Read with a deadline so a buggy PTY teardown doesn't hang forever.
	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(proc.Stdout())
		done <- string(out)
	}()
	var got string
	select {
	case got = <-done:
	case <-time.After(3 * time.Second):
		_ = proc.Signal(killSignal())
		<-done
		t.Fatal("PTY interactive read hung past 3s")
	}
	_ = proc.Wait()
	if !strings.Contains(got, "interactive pty") {
		t.Fatalf("stdout = %q, want substring %q", got, "interactive pty")
	}
}

// TestPtySpawner_InitialSize verifies InitialRows/InitialCols reach the
// child's terminal at spawn time. `stty size` prints "rows cols" read from
// the controlling TTY, so a clean "40 120" proves StartWithSize wired the
// winsize through before the child ran.
func TestPtySpawner_InitialSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := PtySpawner{}.Spawn(ctx, SpawnRequest{
		Argv:        []string{"sh", "-c", "stty size"},
		InitialRows: 40,
		InitialCols: 120,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	out, _ := io.ReadAll(proc.Stdout())
	_ = proc.Wait()
	if !strings.Contains(string(out), "40 120") {
		t.Fatalf("stty size = %q, want substring %q", string(out), "40 120")
	}
}

// TestPtySpawner_Setsize verifies a post-spawn resize lands before the child
// reads its window size. The child blocks on `read` until we've called
// Setsize, so `stty size` afterwards must report the new 50x100 geometry.
func TestPtySpawner_Setsize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := PtySpawner{}.Spawn(ctx, SpawnRequest{
		Argv:        []string{"sh", "-c", "read x; stty size"},
		InitialRows: 24,
		InitialCols: 80,
		Interactive: true,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	rz, ok := proc.(Resizable)
	if !ok {
		t.Fatalf("PTY process should implement Resizable")
	}
	if err := rz.Setsize(0, 80); err == nil {
		t.Fatalf("Setsize(0,80) should reject a zero dimension")
	}
	if err := rz.Setsize(50, 100); err != nil {
		t.Fatalf("Setsize(50,100): %v", err)
	}
	// Unblock the child's read so stty runs against the resized TTY.
	_, _ = proc.Stdin().Write([]byte("go\n"))

	done := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(proc.Stdout())
		done <- string(out)
	}()
	select {
	case got := <-done:
		_ = proc.Wait()
		if !strings.Contains(got, "50 100") {
			t.Fatalf("stty size after resize = %q, want substring %q", got, "50 100")
		}
	case <-time.After(3 * time.Second):
		_ = proc.Signal(killSignal())
		<-done
		t.Fatal("PTY resize read hung past 3s")
	}
}

// TestPipeSpawner_GracefulStopKillsProcessGroup verifies that cancelling the
// ctx tears down the child's whole process group, not just the leader. The
// shell backgrounds a long sleep (the stand-in for a claude-spawned MCP server
// or tool subprocess) and prints its PID; after cancel the group-wide SIGTERM
// must reap that grandchild too.
func TestPipeSpawner_GracefulStopKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	script := `sleep 300 & echo "GRANDCHILD:$!"; wait`
	proc, err := PipeSpawner{}.Spawn(ctx, SpawnRequest{
		Argv:      []string{"sh", "-c", script},
		StopGrace: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	br := bufio.NewReader(proc.Stdout())
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid := parseGrandchildPID(t, line)

	cancel()
	_ = proc.Wait()

	if !waitProcessGone(pid, 3*time.Second) {
		t.Fatalf("grandchild pid %d survived the group stop", pid)
	}
}

// TestPipeSpawner_GracefulStopEscalatesToSIGKILL verifies the hard-kill
// backstop: a child that traps and ignores SIGTERM is still force-killed once
// the StopGrace window elapses, so Wait can never hang on a wedged process.
func TestPipeSpawner_GracefulStopEscalatesToSIGKILL(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proc, err := PipeSpawner{}.Spawn(ctx, SpawnRequest{
		Argv:      []string{"sh", "-c", "trap '' TERM; sleep 300"},
		StopGrace: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	// Drain stdout so Wait isn't gated on an unread pipe.
	go func() { _, _ = io.Copy(io.Discard, proc.Stdout()) }()

	cancel()
	start := time.Now()
	_ = proc.Wait()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("SIGTERM-ignoring child took %v to die; SIGKILL escalation didn't fire", elapsed)
	}
}

func parseGrandchildPID(t *testing.T, line string) int {
	t.Helper()
	const prefix = "GRANDCHILD:"
	_, rest, ok := strings.Cut(line, prefix)
	if !ok {
		t.Fatalf("stdout line %q missing %q prefix", line, prefix)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		t.Fatalf("parse pid from %q: %v", line, err)
	}
	return pid
}

// waitProcessGone polls signal-0 liveness until the pid no longer names a
// process we can signal (ESRCH), or the timeout elapses.
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

func TestNewSpawnerAlwaysUsesPTY(t *testing.T) {
	if _, ok := NewSpawner().(*PtySpawner); !ok {
		t.Fatalf("NewSpawner() -> %T, want *PtySpawner", NewSpawner())
	}
}

// TestSpawn_MissingWorkDirNamesTheDirectory pins the diagnosis a deleted
// conversation work_dir must produce. Both spawners set SysProcAttr, which
// suppresses os.startProcess's chdir disambiguation, so without checkWorkDir
// the failure reads "fork/exec /usr/local/bin/node: no such file or
// directory" — blaming the interpreter for a missing directory.
func TestSpawn_MissingWorkDirNamesTheDirectory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	gone := filepath.Join(t.TempDir(), "deleted-work-dir")
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	spawners := map[string]ProcessSpawner{"pipe": PipeSpawner{}, "pty": PtySpawner{}}
	dirs := map[string]string{"missing": gone, "not a directory": notADir}
	for name, spawner := range spawners {
		for label, dir := range dirs {
			t.Run(name+"/"+label, func(t *testing.T) {
				proc, err := spawner.Spawn(ctx, SpawnRequest{Argv: []string{"cat"}, WorkDir: dir})
				if err == nil {
					_ = proc.Wait()
					t.Fatalf("spawn should fail when WorkDir is %s", label)
				}
				if !errors.Is(err, ErrWorkDirUnavailable) {
					t.Fatalf("err = %v, want ErrWorkDirUnavailable", err)
				}
				if !strings.Contains(err.Error(), dir) {
					t.Fatalf("err = %v, want it to name %s", err, dir)
				}
				// ENOENT is SpawnWithRetry's self-upgrade-race signal; a
				// deleted work_dir must not burn those retries.
				if errors.Is(err, syscall.ENOENT) {
					t.Fatalf("err = %v, must not report as ENOENT", err)
				}
			})
		}
	}
}

// TestSpawn_EmptyWorkDirInheritsParent guards the "" case: an unset WorkDir
// means "inherit the parent's cwd", not "stat the empty path".
func TestSpawn_EmptyWorkDirInheritsParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proc, err := PipeSpawner{}.Spawn(ctx, SpawnRequest{Argv: []string{"cat"}, InitialStdin: "ok\n"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	_, _ = io.ReadAll(proc.Stdout())
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}
