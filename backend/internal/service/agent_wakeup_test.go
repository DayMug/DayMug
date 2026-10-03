package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// residentBackend keeps its "process" alive past the turn: it reports a live
// background task, ends the turn, and never exits — the shape a real resident
// bridge presents to the streamer.
type residentBackend struct {
	logPath string
	parked  chan struct{}
}

func (*residentBackend) Name() string                     { return "resident" }
func (*residentBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *residentBackend) RunWithSession(ctx context.Context, _, _ string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	defer close(ch)
	if b.logPath != "" {
		_ = os.WriteFile(b.logPath, []byte(`{"role":"user","text":"turn"}`+"\n"), 0o600)
	}
	ch <- agent.StreamEvent{Kind: agent.KindBackgroundTasks, Content: `{"tasks":[{"task_id":"t1"}]}`}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "armed"}
	if opts.Residency != nil && opts.Residency.Permit != nil {
		if _, ok := opts.Residency.Permit(); ok {
			if opts.Residency.Parked != nil {
				opts.Residency.Parked()
			}
			if b.parked != nil {
				close(b.parked)
			}
		}
	}
	<-ctx.Done()
	return nil
}
func (*residentBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*residentBackend) SessionExists(string, string, string) bool { return false }
func (b *residentBackend) SessionLogPath(string, string, string) string {
	return b.logPath
}

// A resident bridge keeps the same live session after Stop, so its provider
// history must remain intact just like a non-resident session log.
func TestRunPreservesResidentSessionLogOnCancel(t *testing.T) {
	const convID = "conv-resident"
	logPath := filepath.Join(t.TempDir(), "session.jsonl")
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID}}
	broadcaster := NewBroadcaster()
	backend := &residentBackend{logPath: logPath, parked: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !broadcaster.StartJob(convID, cancel) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer broadcaster.EndJob(convID)

	streamer := &AgentStreamer{Store: ms, Broadcaster: broadcaster, Pool: newTestPool(1, 0)}
	done := make(chan AgentStreamResult, 1)
	go func() {
		done <- streamer.Run(ctx, AgentStreamRequest{
			Backend:        backend,
			WorkDir:        t.TempDir(),
			ConversationID: convID,
			AccountName:    "default",
			Opts:           agent.RunRequest{SessionID: "s1"},
			Mode:           AgentStreamPerResult,
			AllowResidency: true,
			Wake:           func() (chan<- agent.StreamEvent, func(error)) { return nil, nil },
			Broadcast:      func(ServerMessage) {},
		})
	}()

	select {
	case <-backend.parked:
	case <-time.After(3 * time.Second):
		t.Fatal("the backend never asked to park")
	}
	if !broadcaster.CancelJob(convID) {
		t.Fatal("CancelJob found no registered job")
	}

	var out AgentStreamResult
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !out.Resident {
		t.Fatal("result does not report the turn as resident")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("a resident turn's session log was removed after cancel: %v", err)
	}
}

// Permit must be refused, not granted-and-leaked, when the account has no
// live-process capacity. A refusal is a normal outcome, never an error.
func TestResidencyPermitRefusedWhenAccountHasNoLiveCapacity(t *testing.T) {
	const convID = "conv-refused"
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID}}
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster(), Pool: NewPool(nil)}

	var permitted bool
	backend := &residencyProbeBackend{onResidency: func(res *agent.Residency) {
		if res == nil {
			return
		}
		_, permitted = res.Permit()
	}}
	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        backend,
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		AccountName:    "an-account-the-pool-never-heard-of",
		Mode:           AgentStreamPerResult,
		AllowResidency: true,
		Wake:           func() (chan<- agent.StreamEvent, func(error)) { return nil, nil },
		Broadcast:      func(ServerMessage) {},
	})
	if out.Err != nil {
		t.Fatalf("a refused permit must not fail the turn: %v", out.Err)
	}
	if permitted {
		t.Fatal("permit was granted for an account with no live slot")
	}
	if out.Resident {
		t.Fatal("result claims residency the pool never granted")
	}
}

// Residency is a capability, not a default: without AllowResidency the backend
// must see nothing on the request at all.
func TestResidencyIsAbsentUnlessAllowed(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-plain"}}
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster(), Pool: NewPool(nil)}

	var saw *agent.Residency
	backend := &residencyProbeBackend{onResidency: func(res *agent.Residency) { saw = res }}
	streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        backend,
		WorkDir:        t.TempDir(),
		ConversationID: "conv-plain",
		Mode:           AgentStreamPerResult,
		Broadcast:      func(ServerMessage) {},
	})
	if saw != nil {
		t.Fatal("the backend was offered residency it was never granted")
	}
}

func TestAgentStreamerPassesResidentActivityLifecycleToBackend(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "conv-activity"}}
	streamer := &AgentStreamer{
		Store: ms, Broadcaster: NewBroadcaster(), Pool: newTestPool(1, 0),
	}

	var states []bool
	backend := &residencyProbeBackend{onResidency: func(res *agent.Residency) {
		if res == nil || res.Activity == nil {
			t.Fatal("backend did not receive the resident activity callback")
		}
		res.Activity(true)
		res.Activity(false)
	}}
	streamer.Run(context.Background(), AgentStreamRequest{
		Backend:          backend,
		WorkDir:          t.TempDir(),
		ConversationID:   "conv-activity",
		AccountName:      "default",
		Mode:             AgentStreamPerResult,
		AllowResidency:   true,
		Wake:             func() (chan<- agent.StreamEvent, func(error)) { return nil, nil },
		ResidentActivity: func(active bool) { states = append(states, active) },
		Broadcast:        func(ServerMessage) {},
	})
	if len(states) != 2 || !states[0] || states[1] {
		t.Fatalf("resident activity states = %v, want [true false]", states)
	}
}

type residencyProbeBackend struct {
	onResidency func(*agent.Residency)
}

func (*residencyProbeBackend) Name() string                     { return "probe" }
func (*residencyProbeBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *residencyProbeBackend) RunWithSession(_ context.Context, _, _ string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	defer close(ch)
	if b.onResidency != nil {
		b.onResidency(opts.Residency)
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "done"}
	return nil
}
func (*residencyProbeBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*residencyProbeBackend) SessionExists(string, string, string) bool    { return false }
func (*residencyProbeBackend) SessionLogPath(string, string, string) string { return "" }

// A provider-initiated turn arrives with no prompt and no RunWithSession call,
// but must persist and broadcast exactly like a prompted one — plus the origin
// marker that lets the UI tell them apart after a refresh.
func TestWakeupTurnPersistsWithOriginMarker(t *testing.T) {
	const convID = "conv-wake"
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID}}
	broadcaster := NewBroadcaster()
	streamer := &AgentStreamer{Store: ms, Broadcaster: broadcaster}

	frames := make(chan ServerMessage, 32)
	runner := &WakeupRunner{
		Streamer:  streamer,
		Broadcast: func(_ string, msg ServerMessage) { frames <- msg },
		Resolve: func(string) (WakeupTarget, bool) {
			return WakeupTarget{Backend: &residencyProbeBackend{}, WorkDir: t.TempDir(), Model: "m"}, true
		},
	}

	sink, settle := runner.Sink(convID)()
	sink <- agent.StreamEvent{Kind: agent.KindResult, Content: "the codex leg finished"}
	settle(nil)

	// The status pair is what un-freezes the composer and fires the tab
	// notification; without it a wakeup lands invisibly. `ready` is also the
	// signal that the turn has fully unwound, so wait for it before reading
	// the persisted rows.
	var sawThinking, sawReady bool
	deadline := time.After(5 * time.Second)
	for !sawReady {
		select {
		case f := <-frames:
			if f.Type == "status" && f.Status == "thinking" {
				sawThinking = true
			}
			if f.Type == "status" && f.Status == "ready" {
				sawReady = true
			}
		case <-deadline:
			t.Fatalf("status frames thinking=%v ready=%v, want both", sawThinking, sawReady)
		}
	}
	if !sawThinking {
		t.Fatal("the wakeup turn never announced itself as thinking")
	}

	rows := assistantRows(t, ms, convID)
	if len(rows) != 1 {
		t.Fatalf("assistant rows = %d, want 1", len(rows))
	}
	if rows[0].Content != "the codex leg finished" {
		t.Fatalf("row content = %q", rows[0].Content)
	}
	var meta map[string]any
	if err := json.Unmarshal(rows[0].Metadata, &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta["origin"] != WakeupOrigin {
		t.Fatalf("origin = %v, want %q", meta["origin"], WakeupOrigin)
	}

}

// A parked bridge can outlive its conversation. The turn must be refused and
// its frames drained, not delivered into a row that no longer has an owner.
func TestWakeupRefusesADeletedConversation(t *testing.T) {
	ms := storetest.New()
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
	runner := &WakeupRunner{
		Streamer:  streamer,
		Broadcast: func(string, ServerMessage) {},
		Resolve:   func(string) (WakeupTarget, bool) { return WakeupTarget{}, false },
	}

	sink, settle := runner.Sink("gone")()
	// The adapter's router sends while holding its lock, so an undeliverable
	// turn still has to be drained or the whole bridge wedges.
	done := make(chan struct{})
	go func() {
		sink <- agent.StreamEvent{Kind: agent.KindResult, Content: "nobody home"}
		settle(nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("an undeliverable wakeup blocked its producer")
	}
	if rows := assistantRows(t, ms, "gone"); len(rows) != 0 {
		t.Fatalf("a refused wakeup persisted %d row(s)", len(rows))
	}
}

func TestAttachFramesForwardsThenReturnsTheSettleError(t *testing.T) {
	frames := make(chan agent.StreamEvent, 4)
	settled := make(chan error, 1)
	out := make(chan agent.StreamEvent, 4)

	frames <- agent.StreamEvent{Kind: agent.KindDelta, Content: "a"}
	frames <- agent.StreamEvent{Kind: agent.KindDelta, Content: "b"}
	want := errors.New("boom")
	settled <- want

	err := attachFrames(frames, settled)(context.Background(), out)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	close(out)
	var got string
	for evt := range out {
		got += evt.Content
	}
	// Both frames were queued before the settle signal; neither may be lost.
	if got != "ab" {
		t.Fatalf("forwarded %q, want \"ab\"", got)
	}
}

func assistantRows(t *testing.T, ms *storetest.Fake, convID string) []store.Message {
	t.Helper()
	var out []store.Message
	for _, m := range ms.SnapshotMessages(convID) {
		if m.Role == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

// questionProbeBackend is a resident bridge's answer half: it never runs a turn
// (a wakeup arrives via Attach) but it can take an answer for a question its
// process asked.
type questionProbeBackend struct {
	residencyProbeBackend
	answered chan string
}

func (b *questionProbeBackend) AnswerUserQuestion(_ context.Context, controlID, requestID string, _ map[string][]string) error {
	b.answered <- controlID + "/" + requestID
	return nil
}

// A resident bridge is spawned with questions enabled, so the model can still
// call AskUserQuestion on a turn it started by itself. The wakeup turn has to
// register the room's answer hook too, or the question card renders with
// nothing listening and every reply comes back "no longer active".
func TestWakeupTurnAcceptsAnswersToItsQuestions(t *testing.T) {
	const convID = "conv-wake-question"
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID}}
	broadcaster := NewBroadcaster()
	backend := &questionProbeBackend{answered: make(chan string, 1)}
	runner := &WakeupRunner{
		Streamer:  &AgentStreamer{Store: ms, Broadcaster: broadcaster},
		Broadcast: func(string, ServerMessage) {},
		Resolve: func(string) (WakeupTarget, bool) {
			return WakeupTarget{Backend: backend, WorkDir: t.TempDir(), Model: "m"}, true
		},
	}

	sink, settle := runner.Sink(convID)()
	defer settle(nil)
	sink <- agent.StreamEvent{
		Kind:    agent.KindUserQuestion,
		Content: `{"request_id":"q1","questions":[{"question":"which?","options":[{"label":"a"}]}]}`,
	}

	// The responder is installed before the turn's frames are drained, but
	// both happen on their own goroutines — poll rather than race them.
	deadline := time.After(5 * time.Second)
	for {
		attempted, err := broadcaster.AnswerJobQuestion(context.Background(), convID, "q1", map[string][]string{"which?": {"a"}})
		if attempted && err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the wakeup turn never accepted an answer (attempted=%v err=%v)", attempted, err)
		case <-time.After(10 * time.Millisecond):
		}
	}

	select {
	case got := <-backend.answered:
		// The bridge is keyed by conversation id, so that is the control id
		// the answer has to travel back on.
		if got != convID+"/q1" {
			t.Fatalf("answer routed to %q, want %q", got, convID+"/q1")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the backend")
	}
}
