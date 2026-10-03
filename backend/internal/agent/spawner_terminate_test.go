package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTerminateAllKillsDetachedChild is the shutdown regression: a child
// spawned on a context that never gets cancelled (which is every chat run —
// the run ctx is rooted in Background on purpose so a drain can't cut off a
// streaming reply) used to survive the server exiting and keep writing the
// session JSONL the restarted server was about to --resume.
func TestTerminateAllKillsDetachedChild(t *testing.T) {
	// Background, not t.Context(): reproducing the real shutdown shape means
	// no ctx anywhere can stop this child.
	proc, err := PipeSpawner{}.Spawn(context.Background(), SpawnRequest{
		Argv: []string{"sleep", "60"},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- proc.Wait() }()

	info, ok := proc.(ProcessInfo)
	if !ok {
		t.Fatal("pipeProcess should expose ProcessInfo")
	}
	pgid := info.PGID()
	if pgid <= 1 {
		t.Fatalf("PGID = %d, want a real process group", pgid)
	}
	if !hasLiveGroup(pgid) {
		t.Fatalf("child pgid %d was not registered as live", pgid)
	}

	// Lower bound, not equality: a sibling test's child may still be
	// unwinding in the background.
	if n := TerminateAll(2 * time.Second); n < 1 {
		t.Fatalf("TerminateAll signalled %d groups, want at least 1", n)
	}

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("child never exited after TerminateAll")
	}
	if hasLiveGroup(pgid) {
		t.Errorf("pgid %d still registered after the child was reaped", pgid)
	}
	// The whole group must be gone, not just the leader: signal 0 probes
	// liveness without delivering anything.
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("process group %d still alive after TerminateAll (kill probe: %v)", pgid, err)
	}
}

// TestTerminateAllIgnoresReapedChildren guards the dangerous half of the
// registry: a pgid left behind after Wait could be recycled by the OS and
// TerminateAll would then signal a stranger's process group.
func TestTerminateAllIgnoresReapedChildren(t *testing.T) {
	proc, err := PipeSpawner{}.Spawn(context.Background(), SpawnRequest{Argv: []string{"true"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	info, ok := proc.(ProcessInfo)
	if !ok {
		t.Fatal("pipeProcess should expose ProcessInfo")
	}
	pgid := info.PGID()
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if hasLiveGroup(pgid) {
		t.Errorf("pgid %d still registered after Wait reaped it", pgid)
	}
}

// TestGracefulStopKillsSigtermResistantGroup is the cancel-path counterpart of
// the shutdown regression above. exec.Cmd's WaitDelay SIGKILLs only the leader,
// so a descendant that ignores SIGTERM used to outlive it — and since Wait()
// returning unregisters the group, TerminateAll could never reach the orphan
// either. Symptom: the web UI reports the turn finished while the agent's tool
// subprocesses keep running.
func TestGracefulStopKillsSigtermResistantGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Both the leader and the descendant trap SIGTERM, so nothing short of a
	// group SIGKILL can end this tree — exactly the shape of an MCP server or
	// a bash tool subprocess that doesn't honour a polite stop. The `while`
	// loop matters: a bare `sleep` would exit on its own the moment the grace
	// window elapsed, passing the test for the wrong reason.
	//
	// The descendant echoes "ready" only after installing its trap, and the
	// leader installs its own before forking, so receiving that line proves
	// the whole group is SIGTERM-proof. Cancelling before it arrives would
	// deliver SIGTERM to a tree still running under default dispositions,
	// which dies to the polite signal and never exercises the escalation.
	proc, err := PipeSpawner{}.Spawn(ctx, SpawnRequest{
		Argv:      []string{"sh", "-c", `trap "" TERM; sh -c 'trap "" TERM; echo ready; while true; do sleep 60; done' & wait`},
		StopGrace: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	info, ok := proc.(ProcessInfo)
	if !ok {
		t.Fatal("pipeProcess should expose ProcessInfo")
	}
	pgid := info.PGID()
	if pgid <= 1 {
		t.Fatalf("PGID = %d, want a real process group", pgid)
	}

	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(proc.Stdout()).ReadString('\n')
		if err != nil {
			ready <- fmt.Errorf("read readiness line: %w", err)
			return
		}
		if strings.TrimSpace(line) != "ready" {
			ready <- fmt.Errorf("readiness line = %q, want %q", strings.TrimSpace(line), "ready")
			return
		}
		ready <- nil
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("child never became SIGTERM-proof: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the child to install its SIGTERM trap")
	}

	waited := make(chan error, 1)
	go func() { waited <- proc.Wait() }()

	cancel()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("leader never exited after ctx cancel")
	}

	// The descendant reparents to init on the leader's death, so give the
	// kernel a moment to finish tearing the group down before probing. Signal
	// 0 delivers nothing; it only reports whether anyone is left in the group.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after cancel (kill probe: %v)", pgid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func hasLiveGroup(pgid int) bool {
	for _, g := range liveChildGroups() {
		if g == pgid {
			return true
		}
	}
	return false
}
