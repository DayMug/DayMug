package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"
)

// stubSpawner returns the queued (proc, err) pairs in order, recording how
// many times Spawn was called.
type stubSpawner struct {
	results []error
	calls   int
}

func (s *stubSpawner) Spawn(_ context.Context, _ SpawnRequest) (RunningProcess, error) {
	i := s.calls
	s.calls++
	if i < len(s.results) && s.results[i] != nil {
		return nil, s.results[i]
	}
	return &pipeProcess{}, nil
}

func withFastBackoff(t *testing.T) {
	t.Helper()
	prev := spawnRetryBackoff
	spawnRetryBackoff = time.Millisecond
	t.Cleanup(func() { spawnRetryBackoff = prev })
}

func TestSpawnWithRetry_RetriesTransientENOENT(t *testing.T) {
	withFastBackoff(t)
	enoent := &os.PathError{Op: "fork/exec", Path: "/x/claude", Err: syscall.ENOENT}
	s := &stubSpawner{results: []error{enoent, enoent, nil}}

	proc, err := SpawnWithRetry(context.Background(), s, SpawnRequest{Argv: []string{"/x/claude"}})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if proc == nil {
		t.Fatal("expected a process")
	}
	if s.calls != 3 {
		t.Errorf("expected 3 spawn attempts, got %d", s.calls)
	}
}

func TestSpawnWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	withFastBackoff(t)
	enoent := &os.PathError{Op: "fork/exec", Path: "/x/claude", Err: syscall.ENOENT}
	s := &stubSpawner{results: []error{enoent, enoent, enoent, enoent, enoent}}

	_, err := SpawnWithRetry(context.Background(), s, SpawnRequest{Argv: []string{"/x/claude"}})
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
	if s.calls != spawnRetryAttempts+1 {
		t.Errorf("expected %d attempts, got %d", spawnRetryAttempts+1, s.calls)
	}
}

func TestSpawnWithRetry_DoesNotRetryOtherErrors(t *testing.T) {
	withFastBackoff(t)
	other := fmt.Errorf("permission denied: %w", syscall.EACCES)
	s := &stubSpawner{results: []error{other}}

	_, err := SpawnWithRetry(context.Background(), s, SpawnRequest{Argv: []string{"/x/claude"}})
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("expected EACCES, got %v", err)
	}
	if s.calls != 1 {
		t.Errorf("expected 1 attempt (no retry), got %d", s.calls)
	}
}

func TestSpawnWithRetry_StopsOnContextCancel(t *testing.T) {
	withFastBackoff(t)
	enoent := &os.PathError{Op: "fork/exec", Path: "/x/claude", Err: syscall.ENOENT}
	s := &stubSpawner{results: []error{enoent, enoent, enoent, enoent}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := SpawnWithRetry(ctx, s, SpawnRequest{Argv: []string{"/x/claude"}})
	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("expected ENOENT, got %v", err)
	}
	// First Spawn runs, then the cancelled ctx aborts before a second attempt.
	if s.calls != 1 {
		t.Errorf("expected 1 attempt before cancel, got %d", s.calls)
	}
}
