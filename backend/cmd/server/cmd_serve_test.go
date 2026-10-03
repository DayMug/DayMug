package main

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// TestForceStopAgentsKillsAndWaits covers the shutdown step that used to be
// missing entirely: after the drain window expires the server must actually
// terminate the agent children (nothing else can — their run ctx is rooted in
// Background) and then wait for their goroutines to unwind, because cmdServe's
// deferred db.Close() runs right after this returns.
func TestForceStopAgentsKillsAndWaits(t *testing.T) {
	drainer := service.NewDrainer()

	// A stand-in for a run that is still streaming when SIGTERM arrives:
	// bracketed by the drainer exactly like a real prompt, and only able to
	// finish once its child process dies.
	proc, err := agent.PipeSpawner{}.Spawn(context.Background(), agent.SpawnRequest{
		Argv: []string{"sleep", "60"},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	done := drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})
	go func() {
		_ = proc.Wait()
		done()
	}()

	drainer.StartDrain()
	// The drain gate can never close on its own here — that is the bug's
	// precondition, and why forceStopAgents has to do the killing.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer waitCancel()
	if err := drainer.WaitJobs(waitCtx); err == nil {
		t.Fatal("job unexpectedly drained on its own; test no longer reproduces the shutdown shape")
	}

	start := time.Now()
	forceStopAgents(drainer)
	if elapsed := time.Since(start); elapsed > postKillDrainTimeout {
		t.Fatalf("forceStopAgents took %s, longer than its own %s budget", elapsed, postKillDrainTimeout)
	}
	if n := drainer.InFlight(); n != 0 {
		t.Fatalf("in-flight jobs after forceStopAgents = %d, want 0 (db.Close would race them)", n)
	}
}
