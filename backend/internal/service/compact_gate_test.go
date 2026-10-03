package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// drainProbeBackend records what the Drainer saw while the summary ran.
type drainProbeBackend struct {
	compactBackend
	drainer      *Drainer
	inFlightSeen int
	statusSeen   string
	ownerSeen    string
}

func (b *drainProbeBackend) RunOneshot(ctx context.Context, prompt, dir string, req agent.RunRequest) (string, error) {
	b.inFlightSeen = b.drainer.InFlight()
	if jobs := b.drainer.Jobs(); len(jobs) == 1 {
		b.statusSeen = jobs[0].Status
		b.ownerSeen = jobs[0].UserID
	}
	return b.compactBackend.RunOneshot(ctx, prompt, dir, req)
}

func TestCompactRefusedWhilePaused(t *testing.T) {
	const convID = "conv-paused"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, _ := newCompactRunner(convID, backend)
	runner.Pause = NewPauseGate()
	runner.Pause.SetPaused(true)

	_, err := runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want 503 ServiceError", err)
	}
	if backend.calls != 0 {
		t.Fatal("compact ran a summary while new tasks were paused")
	}
}

func TestCompactRefusedWhileDraining(t *testing.T) {
	const convID = "conv-draining"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, _ := newCompactRunner(convID, backend)
	runner.Drainer = NewDrainer()
	runner.Drainer.StartDrain(DrainReasonUpgrade)

	_, err := runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want 503 ServiceError", err)
	}
	if backend.calls != 0 {
		t.Fatal("compact ran a summary after the drain started")
	}
}

// A drain waits on Drainer.InFlight; a compact outside that set would be
// killed mid-rotation by the restart it didn't hold up.
func TestCompactIsTrackedByDrainerWhileRunning(t *testing.T) {
	const convID = "conv-tracked"
	drainer := NewDrainer()
	backend := &drainProbeBackend{
		compactBackend: compactBackend{summary: "digest", compaction: true},
		drainer:        drainer,
	}
	runner, ms := newCompactRunner(convID, backend)
	runner.Drainer = drainer
	// The conversation belongs to agent u1; the run is its owner's. The fake
	// store resolves an agent's owner by email.
	ms.Users[0].Email = "alice@example.com"
	ms.Users = append(ms.Users, store.User{ID: "human-1", Username: "alice", Email: "alice@example.com"})

	if _, err := runner.Compact(context.Background(), convID, "u1"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if backend.ownerSeen != "human-1" {
		t.Fatalf("drainer job owner = %q, want the agent's human owner", backend.ownerSeen)
	}
	if backend.inFlightSeen != 1 || backend.statusSeen != JobStatusRunning {
		t.Fatalf("during run: inFlight=%d status=%q, want 1/%q", backend.inFlightSeen, backend.statusSeen, JobStatusRunning)
	}
	if n := drainer.InFlight(); n != 0 {
		t.Fatalf("InFlight after compact = %d, want 0", n)
	}
}
