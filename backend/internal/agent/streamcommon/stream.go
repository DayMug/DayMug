package streamcommon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// DefaultStallTimeout bounds how long StreamLines waits between bytes on the
// child CLI's stdout before treating the turn as stuck. Gateways have been
// observed to accept a request and then silently hold the connection when
// the upstream model is degraded (e.g. the Anthropic-compatible MiMo gateway
// with mimo-v2.5-pro returning 502 from openresty after a long pause);
// without this watchdog the user sees an infinite "thinking" spinner with no
// error.
//
// The budget is generous because legitimately-silent stretches are routine:
// a single tool runs locally with no stdout heartbeat (a slow `ssh`, or
// `uv run --with <pkgs> ... | tail` that installs deps and buffers to EOF),
// and a large opus-4-8 turn can take minutes to emit its first token while
// the server prefills a near-1M context. A tighter budget killed healthy
// turns in both phases. 10 minutes only fires once a stall is unambiguous.
const DefaultStallTimeout = 10 * time.Minute

// LineStream configures the shared stall-aware NDJSON read loop. The line
// framing is identical across adapters; only the per-line parser (and, for
// codex, a cross-line Flush) differs, so those are injected.
type LineStream struct {
	// CLIName appears in the stall / read-error messages ("claude",
	// "codex") so the user sees which child CLI went silent.
	CLIName string
	// StallTimeout is the max silence between bytes before the loop gives
	// up. A field (rather than the const above) so tests can inject a
	// short budget instead of waiting 10 minutes. Zero means
	// DefaultStallTimeout: an adapter can pass an operator-configured
	// budget straight through without restating the fallback, and the
	// watchdog can never be switched off by an unset config field.
	StallTimeout time.Duration
	// MaxSilentTimeout caps one stdout-silent stretch even when ActivityProbe
	// keeps reporting progress. Zero means no absolute cap.
	MaxSilentTimeout time.Duration
	// ActivityProbe is called when StallTimeout expires. If it reports
	// activity, the stall timer is reset instead of failing the run.
	ActivityProbe func() (bool, string)
	// StallExempt suppresses both silence budgets while it reports true.
	// An adapter whose process outlives a single turn uses it to say
	// "nothing is waiting on this stream right now": a bridge parked
	// between turns is silent by design, and a background watcher can sit
	// quiet for far longer than any stall budget without being stuck.
	// Unlike ActivityProbe this also resets the silence clock, so the
	// budget starts fresh the moment a turn is attached again rather than
	// inheriting however long the process idled.
	StallExempt func() bool
	// Process turns one trimmed NDJSON line into zero or more events.
	Process func(line []byte) []agent.StreamEvent
	// Flush is invoked once at clean EOF for parsers that buffer
	// cross-line state (codex's pending-result merge); nil for parsers
	// that emit everything per-line (claude).
	Flush func() []agent.StreamEvent
}

// readResult carries one ReadBytes result from the reader goroutine to the
// stall-monitoring select loop. err is sticky: once non-nil the goroutine
// closes the channel and exits.
type readResult struct {
	line []byte
	err  error
}

// StreamLines reads NDJSON one line at a time from r, feeds each line
// through cfg.Process, and forwards the resulting events to outputCh.
// Returns nil at EOF, ctx.Err() on cancel, a wrapped IO error, or an
// upstream-unreachable error when no bytes arrive for cfg.StallTimeout.
//
// We use bufio.Reader rather than bufio.Scanner because stream-json lines
// have no upper bound — a single tool_result block can carry an entire PDF
// or image as base64 — and Scanner's per-line cap silently truncates the
// rest of the stream once any one line exceeds it (claude.go's previous
// 1 MiB cap caused exactly that, swallowing the assistant's text response
// after a Read on a ~1 MiB PDF). ReadBytes grows on demand and any IO error
// propagates.
//
// The read happens on a goroutine so the main loop can multiplex it with a
// stall timer. Without that watchdog a hung upstream (e.g. MiMo's gateway
// holding the connection open while the model is degraded) leaves the user
// staring at a "thinking" spinner indefinitely.
func StreamLines(ctx context.Context, r io.Reader, outputCh chan<- agent.StreamEvent, cfg LineStream) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	lineCh := make(chan readResult, 4)

	go func() {
		defer close(lineCh)
		for {
			line, err := reader.ReadBytes('\n')
			select {
			case lineCh <- readResult{line: line, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	emit := NewEmitter(ctx, outputCh, cfg.CLIName+" CLI").EmitAll

	watchdog := NewWatchdog(WatchdogConfig{
		Source:           cfg.CLIName + " CLI",
		StallTimeout:     cfg.StallTimeout,
		MaxSilentTimeout: cfg.MaxSilentTimeout,
		ActivityProbe:    cfg.ActivityProbe,
		StallExempt:      cfg.StallExempt,
	})
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-watchdog.C():
			if err := watchdog.Expired(); err != nil {
				return err
			}
		case res, ok := <-lineCh:
			if !ok {
				return nil
			}
			watchdog.Activity()
			if len(res.line) > 0 {
				line := bytes.TrimRight(res.line, "\r\n")
				if len(line) > 0 {
					if err := emit(cfg.Process(line)); err != nil {
						return err
					}
				}
			}
			if errors.Is(res.err, io.EOF) {
				if cfg.Flush != nil {
					if err := emit(cfg.Flush()); err != nil {
						return err
					}
				}
				return nil
			}
			if res.err != nil {
				return fmt.Errorf("read %s stdout: %w", cfg.CLIName, res.err)
			}
		}
	}
}
