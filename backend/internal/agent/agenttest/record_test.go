package agenttest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestRecordForwardsAndCaptures(t *testing.T) {
	in := make(chan agent.StreamEvent, 3)
	var recorded bytes.Buffer
	out := Record(in, &recorded)

	in <- agent.StreamEvent{Kind: agent.KindDelta, Content: "hi"}
	in <- agent.StreamEvent{Kind: agent.KindResult, Content: "hi there"}
	close(in)

	var seen []string
	for evt := range out {
		seen = append(seen, evt.Content)
	}
	if strings.Join(seen, "|") != "hi|hi there" {
		t.Fatalf("forwarded %v, want the stream unchanged", seen)
	}
	if lines := strings.Count(strings.TrimSpace(recorded.String()), "\n") + 1; lines != 2 {
		t.Fatalf("recorded %d lines, want one per event", lines)
	}
}

func TestLoadFramesRejectsARecordingOfABrokenStream(t *testing.T) {
	// A fixture captured from a build that emitted a malformed payload would
	// otherwise become the definition of correct.
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	body := `{"at_ms":0,"event":{"Kind":"delta","Content":"fine"}}` + "\n" +
		`{"at_ms":5,"event":{"Kind":"tool_result","Content":"{\"id\":\"t1\",\"p\":\"C:\\work\"}"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFrames(path)
	if err == nil {
		t.Fatal("LoadFrames accepted a recording containing a malformed event")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error %v does not point at the offending line", err)
	}
}

func TestReplayBackendReproducesARecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turn.jsonl")
	body := `{"at_ms":0,"event":{"Kind":"system_init","Content":"{\"model\":\"m\"}"}}` + "\n" +
		`{"at_ms":10,"event":{"Kind":"delta","Content":"par"}}` + "\n" +
		`{"at_ms":20,"event":{"Kind":"delta","Content":"tial"}}` + "\n" +
		`{"at_ms":30,"event":{"Kind":"result","Content":"partial"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := FromFile(t, path)
	ch := make(chan agent.StreamEvent, 8)
	if err := backend.RunWithSession(context.Background(), "p", "/w", agent.RunRequest{Model: "m"}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}

	var kinds []string
	for evt := range ch {
		kinds = append(kinds, evt.Kind)
	}
	if got := strings.Join(kinds, ","); got != "system_init,delta,delta,result" {
		t.Fatalf("replayed %s, want the recorded order", got)
	}
	if reqs := backend.Requests(); len(reqs) != 1 || reqs[0].Model != "m" {
		t.Fatalf("Requests() = %+v, want the one call with model m", reqs)
	}
}

func TestReplayBackendRespectsCancellation(t *testing.T) {
	backend := &ReplayBackend{
		Realtime: true,
		Frames: []Frame{
			{AtMS: 0, Event: agent.StreamEvent{Kind: agent.KindDelta, Content: "a"}},
			{AtMS: 60_000, Event: agent.StreamEvent{Kind: agent.KindResult, Content: "a"}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan agent.StreamEvent, 4)

	done := make(chan error, 1)
	go func() { done <- backend.RunWithSession(ctx, "p", "/w", agent.RunRequest{}, ch) }()
	<-ch // the first frame lands immediately
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWithSession = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled realtime replay kept sleeping")
	}
}

func TestReplayBackendReportsAFailedStart(t *testing.T) {
	sentinel := errors.New("provider refused the turn")
	backend := &ReplayBackend{RunErr: sentinel, Frames: []Frame{
		{Event: agent.StreamEvent{Kind: agent.KindDelta, Content: "never sent"}},
	}}

	ch := make(chan agent.StreamEvent, 4)
	err := backend.RunWithSession(context.Background(), "p", "/w", agent.RunRequest{}, ch)
	if !errors.Is(err, sentinel) {
		t.Fatalf("RunWithSession = %v, want the configured failure", err)
	}
	if len(ch) != 0 {
		t.Fatal("a turn that never started must not emit events")
	}
}

// A usage event's JSON does not say whether its figures are running totals;
// a recording that dropped that would replay a Claude session as per-turn
// usage and bill its whole history again on every result.
func TestRecordingKeepsUsageCumulative(t *testing.T) {
	evt, err := agent.NewUsageEvent(agent.UsageReport{InputTokens: 1, OutputTokens: 2, Cumulative: true})
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan agent.StreamEvent, 1)
	var recorded bytes.Buffer
	out := Record(in, &recorded)
	in <- evt
	close(in)
	for range out {
	}

	path := filepath.Join(t.TempDir(), "usage.jsonl")
	if err := os.WriteFile(path, recorded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	frames, err := LoadFrames(path)
	if err != nil {
		t.Fatal(err)
	}
	report, ok := agent.UsageOf(frames[0].Event)
	if !ok || !report.Cumulative || report.OutputTokens != 2 {
		t.Fatalf("replayed usage = %+v (ok=%v), want the cumulative report back", report, ok)
	}
}
