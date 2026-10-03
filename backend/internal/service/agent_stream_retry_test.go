package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// errOverload is the shape the Agent SDK bridge actually produces: the status
// arrives as free text inside a process exit error, with no structured code.
var errOverload = errors.New("exit status 1: CAS bridge: Claude Code returned an error result: API Error: 529 Overloaded")

// scriptedBackend fails its first failures runs with failErr, then succeeds
// emitting reply. deltaBeforeFail makes it stream something first, which is
// what disqualifies a turn from being retried.
type scriptedBackend struct {
	failures        int
	failErr         error
	reply           string
	deltaBeforeFail bool
	runs            *int
}

func (scriptedBackend) Name() string                     { return "scripted" }
func (scriptedBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b scriptedBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	defer close(ch)
	*b.runs++
	if *b.runs <= b.failures {
		if b.deltaBeforeFail {
			ch <- agent.StreamEvent{Kind: agent.KindDelta, Content: "半句话"}
		}
		return b.failErr
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: b.reply}
	return nil
}
func (scriptedBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (scriptedBackend) SessionExists(string, string, string) bool    { return false }
func (scriptedBackend) SessionLogPath(string, string, string) string { return "" }

// skipRetryWaits collapses the backoff schedule so a test exercising three
// retries doesn't sit through nine minutes of it.
func skipRetryWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	prev := transientRetrySleep
	transientRetrySleep = func(_ context.Context, d time.Duration) bool {
		slept = append(slept, d)
		return true
	}
	t.Cleanup(func() { transientRetrySleep = prev })
	return &slept
}

func errorContents(ms *storetest.Fake, convID string) []string {
	var out []string
	for _, msg := range ms.SnapshotMessages(convID) {
		if msg.Role == "error" {
			out = append(out, msg.Content)
		}
	}
	return out
}

func TestIsTransientUpstreamError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a failure", nil, false},
		{"the real 529 bridge error", errOverload, true},
		{"bare overloaded_error", errors.New("overloaded_error"), true},
		{"gateway timeout", errors.New("API Error: 504 Gateway Timeout"), true},
		{"service unavailable", errors.New("http 503"), true},
		// A usage limit clears on the account's reset schedule, not in
		// minutes; retrying only delays the report the user must act on.
		{"session limit is not transient", errors.New("CAS bridge: You've hit your session limit"), false},
		{"429 is not transient", errors.New("API Error: 429 Too Many Requests"), false},
		{"auth failure is not transient", errors.New("authentication_error: invalid api key"), false},
		{"an ordinary crash is not transient", errors.New("TypeError: x is not a function"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientUpstreamError(tc.err); got != tc.want {
				t.Errorf("IsTransientUpstreamError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A 529 that killed the turn before it said anything is retried, and the user
// sees one notice explaining the wait — not one error row per attempt.
func TestRunRetriesTransientOverload(t *testing.T) {
	const convID = "conv"
	runs := 0
	slept := skipRetryWaits(t)
	ms := storetest.New()
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}

	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        scriptedBackend{failures: 2, failErr: errOverload, reply: "终于答上了", runs: &runs},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
	})

	if out.Err != nil {
		t.Fatalf("run error = %v, want nil after a successful retry", out.Err)
	}
	if runs != 3 {
		t.Errorf("backend runs = %d, want 3 (initial + 2 retries)", runs)
	}
	if want := []time.Duration{time.Minute, 3 * time.Minute}; len(*slept) != len(want) {
		t.Errorf("backoff waits = %v, want %v", *slept, want)
	} else if (*slept)[0] != want[0] || (*slept)[1] != want[1] {
		t.Errorf("backoff waits = %v, want %v", *slept, want)
	}
	errs := errorContents(ms, convID)
	if len(errs) != 1 {
		t.Fatalf("error rows = %v, want exactly one retry notice", errs)
	}
	if strings.Contains(errs[0], "529") {
		t.Errorf("notice leaked the raw upstream error: %q", errs[0])
	}
	var meta struct{ Notice Notice }
	for _, msg := range ms.SnapshotMessages(convID) {
		if msg.Role == "error" {
			if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
				t.Fatalf("retry notice metadata %q: %v", msg.Metadata, err)
			}
		}
	}
	if want := (Notice{Kind: NoticeKindTransientRetry, DelaySeconds: 60, MaxAttempts: 3}); !reflect.DeepEqual(meta.Notice, want) {
		t.Errorf("retry notice = %+v, want %+v", meta.Notice, want)
	}
	if got := assistantContents(t, ms, convID); len(got) != 1 || got[0] != "终于答上了" {
		t.Errorf("assistant rows = %v, want the retried reply once", got)
	}
}

// Once the schedule is exhausted the real error must surface — silently
// swallowing it would leave the turn looking like it just stopped.
func TestRunReportsFailureAfterScheduleExhausted(t *testing.T) {
	const convID = "conv"
	runs := 0
	slept := skipRetryWaits(t)
	ms := storetest.New()
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}

	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        scriptedBackend{failures: 99, failErr: errOverload, runs: &runs},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
	})

	if out.Err == nil {
		t.Fatal("run error = nil, want the upstream failure once retries ran out")
	}
	if want := len(transientRetryDelays) + 1; runs != want {
		t.Errorf("backend runs = %d, want %d (initial + full schedule)", runs, want)
	}
	if len(*slept) != len(transientRetryDelays) {
		t.Errorf("backoff waits = %v, want the whole schedule", *slept)
	}
	errs := errorContents(ms, convID)
	if len(errs) != 2 {
		t.Fatalf("error rows = %v, want the retry notice plus the final failure", errs)
	}
	if !strings.Contains(errs[1], "529") {
		t.Errorf("final error row = %q, want the upstream message", errs[1])
	}
}

// A turn that already streamed something must not be re-run: the retry would
// append a second copy rather than replace the first.
func TestRunDoesNotRetryAfterOutputReachedTheUser(t *testing.T) {
	const convID = "conv"
	runs := 0
	skipRetryWaits(t)
	ms := storetest.New()
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}

	streamer.Run(context.Background(), AgentStreamRequest{
		Backend: scriptedBackend{
			failures: 99, failErr: errOverload, deltaBeforeFail: true, runs: &runs,
		},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
	})

	if runs != 1 {
		t.Errorf("backend runs = %d, want 1 — a turn with partial output is not retryable", runs)
	}
}

// A usage limit is reported immediately rather than sat on for nine minutes.
func TestRunDoesNotRetryUsageLimit(t *testing.T) {
	const convID = "conv"
	runs := 0
	skipRetryWaits(t)
	ms := storetest.New()
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}

	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend: scriptedBackend{
			failures: 99,
			failErr:  errors.New("CAS bridge: You've hit your session limit"),
			runs:     &runs,
		},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
	})

	if runs != 1 {
		t.Errorf("backend runs = %d, want 1 — a usage limit is not an overload", runs)
	}
	if out.Err == nil {
		t.Fatal("run error = nil, want the usage limit reported straight away")
	}
}
