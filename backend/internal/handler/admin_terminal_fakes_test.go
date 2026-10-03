package handler

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// fakeSpawner records the SpawnRequest it receives and returns a
// fakeProcess the test drives directly. Substituted for the package-level
// adminTerminalSpawner in adminTerminalTestSetup so the WS handler talks to a
// scripted child instead of forking a real CLI.
type fakeSpawner struct {
	spawned chan *fakeProcess
}

func newFakeSpawner() *fakeSpawner {
	return &fakeSpawner{spawned: make(chan *fakeProcess, 16)}
}

func (f *fakeSpawner) Spawn(_ context.Context, req agent.SpawnRequest) (agent.RunningProcess, error) {
	proc := newFakeProcess(req)
	f.spawned <- proc
	return proc, nil
}

func (f *fakeSpawner) waitForSpawn(t *testing.T, d time.Duration) *fakeProcess {
	t.Helper()
	select {
	case p := <-f.spawned:
		return p
	case <-time.After(d):
		t.Fatalf("spawner did not receive a Spawn within %s", d)
		return nil
	}
}

// fakeProcess implements agent.RunningProcess. Output bytes the test
// pushes via Emit() reach the handler through Stdout(); stdin bytes the
// handler writes are captured for assertion; Wait blocks until Signal()
// or Exit() runs.
type fakeProcess struct {
	req      agent.SpawnRequest
	stdoutR  *io.PipeReader
	stdoutW  *io.PipeWriter
	mu       sync.Mutex
	stdinBuf []byte
	signals  []os.Signal
	exitedCh chan struct{}
	exitErr  error
	once     sync.Once
}

func newFakeProcess(req agent.SpawnRequest) *fakeProcess {
	r, w := io.Pipe()
	return &fakeProcess{
		req:      req,
		stdoutR:  r,
		stdoutW:  w,
		exitedCh: make(chan struct{}),
	}
}

func (p *fakeProcess) Emit(data string) {
	_, _ = p.stdoutW.Write([]byte(data))
}

func (p *fakeProcess) Exit(err error) {
	p.mu.Lock()
	if p.exitErr == nil && err != nil {
		p.exitErr = err
	}
	p.mu.Unlock()
	p.closeExit()
}

func (p *fakeProcess) closeExit() {
	p.once.Do(func() {
		_ = p.stdoutW.Close()
		close(p.exitedCh)
	})
}

func (p *fakeProcess) Stdout() io.Reader     { return p.stdoutR }
func (p *fakeProcess) Stderr() io.Reader     { return nil }
func (p *fakeProcess) Stdin() io.WriteCloser { return &fakeStdin{proc: p} }

func (p *fakeProcess) Wait() error {
	<-p.exitedCh
	return p.exitErr
}

func (p *fakeProcess) Signal(s os.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, s)
	p.mu.Unlock()
	p.closeExit()
	return nil
}

func (p *fakeProcess) StdinSnapshot() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]byte, len(p.stdinBuf))
	copy(out, p.stdinBuf)
	return out
}

func (p *fakeProcess) SignalsReceived() []os.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]os.Signal, len(p.signals))
	copy(out, p.signals)
	return out
}

type fakeStdin struct {
	proc *fakeProcess
}

func (s *fakeStdin) Write(p []byte) (int, error) {
	s.proc.mu.Lock()
	s.proc.stdinBuf = append(s.proc.stdinBuf, p...)
	s.proc.mu.Unlock()
	return len(p), nil
}

func (s *fakeStdin) Close() error { return nil }

func dialTerminal(t *testing.T, srv *httptest.Server) (*websocket.Conn, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	return conn, ctx, cancel
}
