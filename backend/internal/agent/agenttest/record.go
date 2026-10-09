package agenttest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// Record / Replay exist because every adapter's behaviour is dictated by an
// upstream nobody can call from a unit test. The alternative to a recording is
// a hand-written fixture, and a hand-written fixture encodes what someone
// believed the provider emits — which is exactly the belief that turns out to
// be wrong when a release changes the stream. Recording moves the fixture's
// authority from memory to a real run: capture one session, keep the file, and
// the adapter's quirks are pinned to something that actually happened.
//
// The file is JSONL, one Frame per line, with the offset from the first event
// so a replay can reproduce timing when a test cares (stall watchdogs, ordering
// races) and ignore it when it does not.

// Frame is one recorded event and when it arrived, relative to the start of
// the recording.
type Frame struct {
	AtMS  int64             `json:"at_ms"`
	Event agent.StreamEvent `json:"event"`
	// UsageCumulative keeps agent.UsageReport.Cumulative, the one part of a
	// usage event its JSON Content does not carry. Without it a replayed
	// Claude session would be billed as if every result were per-turn.
	UsageCumulative bool `json:"usage_cumulative,omitempty"`
}

// Record forwards in to the returned channel unchanged while writing every
// event to w. Insert it between a real backend and its consumer, run a
// session, and keep the output as a fixture.
func Record(in <-chan agent.StreamEvent, w io.Writer) <-chan agent.StreamEvent {
	out := make(chan agent.StreamEvent, cap(in))
	go func() {
		defer close(out)
		start := time.Now()
		enc := json.NewEncoder(w)
		for evt := range in {
			// A recording failure must not disturb the path being observed:
			// the point of recording is that this run behaves exactly as it
			// would without it.
			frame := Frame{AtMS: time.Since(start).Milliseconds(), Event: evt}
			if report, ok := agent.UsageOf(evt); ok {
				frame.UsageCumulative = report.Cumulative
			}
			if err := enc.Encode(frame); err != nil {
				fmt.Fprintf(os.Stderr, "agenttest: recording failed: %v\n", err)
			}
			out <- evt
		}
	}()
	return out
}

// LoadFrames reads a recording and validates every event in it.
//
// Validation on load is the half that makes recordings worth keeping: a
// fixture captured from a build that emitted a malformed payload would
// otherwise become the definition of correct, and every test replaying it
// would agree with the bug.
func LoadFrames(path string) ([]Frame, error) {
	f, err := os.Open(path) //nolint:gosec // test fixture path supplied by the caller
	if err != nil {
		return nil, fmt.Errorf("open recording %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var frames []Frame
	scanner := bufio.NewScanner(f)
	// Tool results carry whole files; the default 64 KiB line cap would
	// truncate exactly the frames worth recording.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var frame Frame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		if err := frame.Event.Validate(); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		if report, ok := agent.UsageOf(frame.Event); ok {
			report.Cumulative = frame.UsageCumulative
			frame.Event.Usage = &report
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return frames, nil
}

// ReplayBackend is an agent.Backend that re-emits a recording. It stands in
// for a real provider in tests of everything above the adapter — the stream
// consumer, persistence, the broadcast path — which otherwise have to be fed
// by a fake that only emits what its author remembered to include.
type ReplayBackend struct {
	// Frames is the recording to replay.
	Frames []Frame
	// Realtime honours each frame's AtMS instead of emitting as fast as the
	// consumer accepts. Off by default: tests should not sleep unless they
	// are specifically about timing.
	Realtime bool
	// Caps is what Capabilities reports.
	Caps agent.Capabilities
	// RunErr, when set, makes RunWithSession fail without emitting anything —
	// the "this turn never started" case.
	RunErr error

	mu       sync.Mutex
	requests []agent.RunRequest
}

var _ agent.Backend = (*ReplayBackend)(nil)

// FromFile builds a ReplayBackend from a recording, failing the test if the
// file is unreadable or contains an invalid event.
func FromFile(t *testing.T, path string) *ReplayBackend {
	t.Helper()
	frames, err := LoadFrames(path)
	if err != nil {
		t.Fatalf("load recording: %v", err)
	}
	return &ReplayBackend{Frames: frames}
}

func (*ReplayBackend) Name() string { return "replay" }

func (b *ReplayBackend) Capabilities() agent.Capabilities { return b.Caps }

func (*ReplayBackend) SessionExists(string, string, string) bool    { return false }
func (*ReplayBackend) SessionLogPath(string, string, string) string { return "" }

func (b *ReplayBackend) RunWithSession(ctx context.Context, _, _ string, opts agent.RunRequest, out chan<- agent.StreamEvent) error {
	defer close(out)
	b.mu.Lock()
	b.requests = append(b.requests, opts)
	b.mu.Unlock()
	if b.RunErr != nil {
		return b.RunErr
	}

	var elapsed time.Duration
	for _, frame := range b.Frames {
		if b.Realtime {
			if wait := time.Duration(frame.AtMS)*time.Millisecond - elapsed; wait > 0 {
				select {
				case <-time.After(wait):
					elapsed += wait
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		select {
		case out <- frame.Event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (b *ReplayBackend) RunOneshot(ctx context.Context, prompt, workDir string, opts agent.RunRequest) (string, error) {
	ch := make(chan agent.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() { errCh <- b.RunWithSession(ctx, prompt, workDir, opts, ch) }()

	var last string
	for evt := range ch {
		if evt.Kind == agent.KindResult && !evt.Subagent {
			last = evt.Content
		}
	}
	return last, <-errCh
}

// Requests returns the RunRequests this backend was called with, so a test can
// assert what the layer above asked for.
func (b *ReplayBackend) Requests() []agent.RunRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]agent.RunRequest(nil), b.requests...)
}
