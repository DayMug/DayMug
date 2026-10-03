package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestContextUsageRatio(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    float64
		wantOK  bool
	}{
		{
			name:    "typical frame",
			payload: `{"cache_creation":513,"cache_read":69023,"input_tokens":2,"total":1000000,"used":500000}`,
			want:    0.5,
			wantOK:  true,
		},
		{
			// Empty on a fresh conversation and after every /compact — no turn
			// has reported usage yet.
			name:    "empty payload",
			payload: "",
			wantOK:  false,
		},
		{
			name:    "malformed json",
			payload: `{"used":`,
			wantOK:  false,
		},
		{
			// A backend that doesn't report a window size can't be measured.
			// Not an error — just "never compact".
			name:    "zero total",
			payload: `{"used":100,"total":0}`,
			wantOK:  false,
		},
		{
			name:    "zero used",
			payload: `{"used":0,"total":1000000}`,
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := contextUsageRatio(tt.payload)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("ratio = %v, want %v", got, tt.want)
			}
		})
	}
}

// A ratio at or above the full window can never fire. Rejecting it at load time
// beats accepting a setting that presents as "configured but never runs".
func TestValidateAutoCompactRatioBounds(t *testing.T) {
	for _, ratio := range []float64{-0.1, 1, 1.5} {
		cfg := &config.Config{
			Providers:        []config.Provider{{Name: "default", Type: config.CLITypeClaude}},
			AutoCompactRatio: ratio,
		}
		cfg.Normalize()
		if err := cfg.Validate(); err == nil {
			t.Fatalf("auto_compact_ratio %v accepted, want rejection", ratio)
		}
	}
	for _, ratio := range []float64{0, 0.6, 0.99} {
		cfg := &config.Config{
			Providers:        []config.Provider{{Name: "default", Type: config.CLITypeClaude}},
			AutoCompactRatio: ratio,
		}
		cfg.Normalize()
		if err := cfg.Validate(); err != nil {
			t.Fatalf("auto_compact_ratio %v rejected: %v", ratio, err)
		}
	}
}

// asyncCompactBackend counts RunOneshot calls across goroutines. compactBackend
// in compact_test.go is only ever driven synchronously, so its plain int
// counter races once MaybeAutoCompact moves the run onto its own goroutine.
type asyncCompactBackend struct {
	compaction   bool
	contextUsage bool
	calls        atomic.Int32
	fired        chan struct{}
	once         sync.Once
}

func newAsyncCompactBackend(compaction bool) *asyncCompactBackend {
	return &asyncCompactBackend{compaction: compaction, contextUsage: true, fired: make(chan struct{})}
}

func (*asyncCompactBackend) Name() string { return "auto-compact-fake" }
func (b *asyncCompactBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: b.compaction, ReportsContextUsage: b.contextUsage}
}
func (*asyncCompactBackend) RunWithSession(context.Context, string, string, agent.RunRequest, chan<- agent.StreamEvent) error {
	return nil
}
func (b *asyncCompactBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	b.calls.Add(1)
	b.once.Do(func() { close(b.fired) })
	return "digest", nil
}
func (*asyncCompactBackend) SessionExists(string, string, string) bool    { return true }
func (*asyncCompactBackend) SessionLogPath(string, string, string) string { return "" }

// compactFired reports whether the background compact ran. Waits on the signal
// for the positive case; for the negative case a short grace period is the only
// way to distinguish "never runs" from "hasn't run yet".
func (b *asyncCompactBackend) compactFired(within time.Duration) bool {
	select {
	case <-b.fired:
		return true
	case <-time.After(within):
		return false
	}
}

// The whole point: a turn that leaves the window over the configured share
// rotates the session without anyone asking.
func TestMaybeAutoCompactFiresAboveThreshold(t *testing.T) {
	const convID = "conv-over"
	backend := newAsyncCompactBackend(true)
	runner, ms := newCompactRunner(convID, backend)
	runner.Cfg = &config.Config{AutoCompactRatio: 0.6}
	ms.Conversations[0].LastContextUsage = `{"used":900000,"total":1000000}`

	runner.MaybeAutoCompact(convID)

	if !backend.compactFired(2 * time.Second) {
		t.Fatal("compact did not run above the threshold")
	}
	// Rotation is Compact's own contract (covered in compact_test.go); asserting
	// it here would mean reading the fake's slice while the goroutine may still
	// be writing it. The call count is the behaviour this test owns.
}

// Under the line nothing happens — this runs after every single turn, so a
// false positive costs a full CLI child and an account slot.
func TestMaybeAutoCompactSkipsBelowThreshold(t *testing.T) {
	const convID = "conv-under"
	backend := newAsyncCompactBackend(true)
	runner, ms := newCompactRunner(convID, backend)
	runner.Cfg = &config.Config{AutoCompactRatio: 0.6}
	ms.Conversations[0].LastContextUsage = `{"used":100000,"total":1000000}`

	runner.MaybeAutoCompact(convID)

	if backend.compactFired(150 * time.Millisecond) {
		t.Fatalf("compact ran under the threshold (calls=%d)", backend.calls.Load())
	}
}

// A backend that never writes last_context_usage can only ever be judged on a
// row some other transport left behind. Codex-over-CLI is the live case: rows
// written by an older estimating build sit at used==total, which would read as
// 100% and compact the conversation after every single turn.
func TestMaybeAutoCompactSkipsBackendsThatDontReportContextUsage(t *testing.T) {
	const convID = "conv-stale"
	backend := newAsyncCompactBackend(true)
	backend.contextUsage = false
	runner, ms := newCompactRunner(convID, backend)
	runner.Cfg = &config.Config{AutoCompactRatio: 0.6}
	ms.Conversations[0].LastContextUsage = `{"used":400000,"total":400000}`

	runner.MaybeAutoCompact(convID)

	if backend.compactFired(150 * time.Millisecond) {
		t.Fatalf("compacted off a stale row from a backend that doesn't report context usage (calls=%d)", backend.calls.Load())
	}
}

// Ratio 0 is the documented "keep compaction manual" default and must not even
// look at usage.
func TestMaybeAutoCompactDisabledByDefault(t *testing.T) {
	const convID = "conv-off"
	backend := newAsyncCompactBackend(true)
	runner, ms := newCompactRunner(convID, backend)
	runner.Cfg = &config.Config{}
	ms.Conversations[0].LastContextUsage = `{"used":990000,"total":1000000}`

	runner.MaybeAutoCompact(convID)

	if backend.compactFired(150 * time.Millisecond) {
		t.Fatalf("compact ran with auto_compact_ratio=0 (calls=%d)", backend.calls.Load())
	}
}

// A backend without compaction support would fail with a 400 on every turn once
// its context crossed the line; the check has to bail before Compact.
func TestMaybeAutoCompactSkipsBackendWithoutCompaction(t *testing.T) {
	const convID = "conv-nocompact"
	backend := newAsyncCompactBackend(false)
	runner, ms := newCompactRunner(convID, backend)
	runner.Cfg = &config.Config{AutoCompactRatio: 0.6}
	ms.Conversations[0].LastContextUsage = `{"used":900000,"total":1000000}`

	runner.MaybeAutoCompact(convID)

	if backend.compactFired(150 * time.Millisecond) {
		t.Fatalf("compact attempted on a backend that cannot compact (calls=%d)", backend.calls.Load())
	}
}

// Nil config is the focused-test / partially-wired shape. It must not panic on
// a path that runs after every turn.
func TestMaybeAutoCompactToleratesNilConfig(t *testing.T) {
	const convID = "conv-nilcfg"
	backend := newAsyncCompactBackend(true)
	runner, _ := newCompactRunner(convID, backend)
	runner.Cfg = nil

	runner.MaybeAutoCompact(convID)
	runner.MaybeAutoCompact("")
}
