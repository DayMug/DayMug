package streamcommon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// echoProcess turns each line into one delta event carrying the line text,
// standing in for the adapter-specific parsers.
func echoProcess(line []byte) []agent.StreamEvent {
	return []agent.StreamEvent{{Kind: agent.KindDelta, Content: string(line)}}
}

func collect(t *testing.T, run func(ch chan agent.StreamEvent) error) ([]agent.StreamEvent, error) {
	t.Helper()
	ch := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ch)
		close(ch)
	}()
	var got []agent.StreamEvent
	for evt := range ch {
		got = append(got, evt)
	}
	return got, <-errCh
}

func TestStreamLines_EmitsPerLineAndFlushAtEOF(t *testing.T) {
	got, err := collect(t, func(ch chan agent.StreamEvent) error {
		return StreamLines(context.Background(), strings.NewReader("a\r\nb\n"), ch, LineStream{
			CLIName:      "codex",
			StallTimeout: time.Second,
			Process:      echoProcess,
			Flush: func() []agent.StreamEvent {
				return []agent.StreamEvent{{Kind: agent.KindResult, Content: "flushed"}}
			},
		})
	})
	if err != nil {
		t.Fatalf("StreamLines: %v", err)
	}
	want := []string{"a", "b", "flushed"}
	if len(got) != len(want) {
		t.Fatalf("expected %d events, got %+v", len(want), got)
	}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("event %d: want %q, got %q", i, w, got[i].Content)
		}
	}
}

func TestStreamLines_NilFlushIsClean(t *testing.T) {
	got, err := collect(t, func(ch chan agent.StreamEvent) error {
		return StreamLines(context.Background(), strings.NewReader("x\n"), ch, LineStream{
			CLIName:      "claude",
			StallTimeout: time.Second,
			Process:      echoProcess,
		})
	})
	if err != nil {
		t.Fatalf("StreamLines: %v", err)
	}
	if len(got) != 1 || got[0].Content != "x" {
		t.Fatalf("unexpected events: %+v", got)
	}
}

// stalledReader never returns from Read — simulates a hung upstream.
type stalledReader struct{}

func (stalledReader) Read(_ []byte) (int, error) {
	select {}
}

func TestStreamLines_StallTimeoutNamesTheCLI(t *testing.T) {
	_, err := collect(t, func(ch chan agent.StreamEvent) error {
		return StreamLines(context.Background(), stalledReader{}, ch, LineStream{
			CLIName:      "codex",
			StallTimeout: 50 * time.Millisecond,
			Process:      echoProcess,
		})
	})
	if err == nil || !strings.Contains(err.Error(), "upstream unreachable") || !strings.Contains(err.Error(), "codex CLI") {
		t.Fatalf("expected an upstream-unreachable error naming codex, got %v", err)
	}
}

func TestStreamLines_ActivityProbeExtendsStallTimeout(t *testing.T) {
	ch := make(chan agent.StreamEvent, 1)
	probes := 0
	errCh := make(chan error, 1)
	go func() {
		errCh <- StreamLines(context.Background(), stalledReader{}, ch, LineStream{
			CLIName:          "test",
			StallTimeout:     25 * time.Millisecond,
			MaxSilentTimeout: 200 * time.Millisecond,
			ActivityProbe: func() (bool, string) {
				probes++
				return probes == 1, "active once"
			},
			Process: func([]byte) []agent.StreamEvent { return nil },
		})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("probe-active stall returned too early: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "upstream unreachable") {
			t.Fatalf("expected upstream-unreachable error after inactive probe, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StreamLines did not return after inactive probe")
	}
}

func TestStreamLines_MaxSilentTimeoutCapsActiveProbe(t *testing.T) {
	ch := make(chan agent.StreamEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- StreamLines(context.Background(), stalledReader{}, ch, LineStream{
			CLIName:          "test",
			StallTimeout:     20 * time.Millisecond,
			MaxSilentTimeout: 70 * time.Millisecond,
			ActivityProbe: func() (bool, string) {
				return true, "still active"
			},
			Process: func([]byte) []agent.StreamEvent { return nil },
		})
	}()

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "maximum silent duration") {
			t.Fatalf("expected maximum silent duration error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StreamLines did not enforce max silent timeout")
	}
}

// failReader returns a non-EOF error immediately.
type failReader struct{ err error }

func (f failReader) Read(_ []byte) (int, error) { return 0, f.err }

func TestStreamLines_WrapsReadErrorWithCLIName(t *testing.T) {
	want := fmt.Errorf("synthetic read failure")
	_, err := collect(t, func(ch chan agent.StreamEvent) error {
		return StreamLines(context.Background(), failReader{err: want}, ch, LineStream{
			CLIName:      "claude",
			StallTimeout: time.Second,
			Process:      echoProcess,
		})
	})
	if err == nil || !errors.Is(err, want) || !strings.Contains(err.Error(), "read claude stdout") {
		t.Fatalf("expected wrapped read error naming claude, got %v", err)
	}
}

func TestStreamLines_StallExemptSuppressesWatchdogUntilCleared(t *testing.T) {
	ch := make(chan agent.StreamEvent, 1)
	errCh := make(chan error, 1)
	var exempt atomic.Bool
	exempt.Store(true)
	go func() {
		errCh <- StreamLines(context.Background(), stalledReader{}, ch, LineStream{
			CLIName:      "claude",
			StallTimeout: 20 * time.Millisecond,
			StallExempt:  exempt.Load,
			Process:      func([]byte) []agent.StreamEvent { return nil },
		})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("exempt stream returned while nothing was waiting on it: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	exempt.Store(false)
	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "upstream unreachable") {
			t.Fatalf("expected upstream-unreachable error once the exemption lifted, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StreamLines did not return after the exemption lifted")
	}
}

// A parked bridge idles far past MaxSilentTimeout by design, so the exemption
// has to outrank the absolute cap too — and the cap must then measure from the
// moment the exemption lifted, not from the last byte before the park.
func TestStreamLines_StallExemptOutranksMaxSilentTimeout(t *testing.T) {
	ch := make(chan agent.StreamEvent, 1)
	errCh := make(chan error, 1)
	var exempt atomic.Bool
	exempt.Store(true)
	go func() {
		errCh <- StreamLines(context.Background(), stalledReader{}, ch, LineStream{
			CLIName:          "claude",
			StallTimeout:     20 * time.Millisecond,
			MaxSilentTimeout: 60 * time.Millisecond,
			StallExempt:      exempt.Load,
			Process:          func([]byte) []agent.StreamEvent { return nil },
		})
	}()

	select {
	case err := <-errCh:
		t.Fatalf("exempt stream tripped the absolute cap: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	exempt.Store(false)
	select {
	case err := <-errCh:
		// The silence clock restarted when the exemption lifted, so the
		// per-turn stall budget fires first — not the cap that the 150ms
		// park would already have exceeded.
		if err == nil || strings.Contains(err.Error(), "maximum silent duration") {
			t.Fatalf("expected the stall budget to fire on a fresh clock, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("StreamLines did not return after the exemption lifted")
	}
}
