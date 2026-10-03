package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// These tests pin how a browser prompt is admitted — which frame each refusal
// broadcasts, and that every refusal hands back the room, the drainer entry and
// the account slot it took. They exist so the admission sequence can be moved
// without anyone having to re-derive what it used to do.

const admissionTestAccount = "acc1"

// blockingBackend holds RunWithSession open until release is closed, so a test
// can look at what a turn holds while it is running.
type blockingBackend struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingBackend() *blockingBackend {
	return &blockingBackend{started: make(chan struct{}), release: make(chan struct{})}
}

func (*blockingBackend) Name() string                     { return "blocking" }
func (*blockingBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *blockingBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	defer close(ch)
	close(b.started)
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "done"}
	return nil
}
func (*blockingBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*blockingBackend) SessionExists(string, string, string) bool    { return false }
func (*blockingBackend) SessionLogPath(string, string, string) string { return "" }

type admissionFixture struct {
	runner *PromptRunner
	store  *storetest.Fake
	pool   *Pool
	frames chan []byte
	convID string
}

func newAdmissionFixture(t *testing.T, backend agent.Backend) *admissionFixture {
	t.Helper()
	const convID = "conv-admission"
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: convID, UserID: "u1", Provider: config.CLITypeClaude, Model: "model-1", WorkDir: t.TempDir(),
	}}
	ms.Users = []store.User{{
		ID: "u1", Username: "u1", ProviderBindings: map[string]string{config.CLITypeClaude: admissionTestAccount},
	}}
	ms.Messages = map[string][]store.Message{convID: {{
		ID: "m1", ConversationID: convID, Role: "user", Content: "hi", QueueStatus: "pending",
	}}}
	cfg := &config.Config{Providers: []config.Provider{{
		Name: admissionTestAccount, Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	pool := NewPool(cfg)
	broadcaster := NewBroadcaster()
	frames := make(chan []byte, 64)
	broadcaster.Join(convID, "client-1", frames)
	runner := &PromptRunner{
		Store:       ms,
		Broadcaster: broadcaster,
		UserHub:     NewUserHub(),
		Drainer:     NewDrainer(),
		Pool:        pool,
		Persist:     &MessagePersister{Store: ms},
		Backends:    NewBackendRegistry(nil, backend),
	}
	return &admissionFixture{runner: runner, store: ms, pool: pool, frames: frames, convID: convID}
}

func (f *admissionFixture) process() {
	f.runner.ProcessPrompt(context.Background(), store.Message{ID: "m1", ConversationID: f.convID, Content: "hi"})
}

// assertAllReleased is the invariant every exit of ProcessPrompt shares.
func (f *admissionFixture) assertAllReleased(t *testing.T) {
	t.Helper()
	if f.runner.Broadcaster.IsBusy(f.convID) {
		t.Error("conversation room still marked busy")
	}
	if n := f.runner.Drainer.InFlight(); n != 0 {
		t.Errorf("drainer in-flight = %d, want 0", n)
	}
	if inUse, queued, _ := f.pool.Stats(admissionTestAccount); inUse != 0 || queued != 0 {
		t.Errorf("pool inUse=%d queued=%d, want 0/0", inUse, queued)
	}
}

// frameTypes drains whatever the room has broadcast so far.
func (f *admissionFixture) drainFrames(t *testing.T) []ServerMessage {
	t.Helper()
	var out []ServerMessage
	for {
		select {
		case data := <-f.frames:
			var msg ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			out = append(out, msg)
		default:
			return out
		}
	}
}

func (f *admissionFixture) errorRows(t *testing.T) []store.Message {
	t.Helper()
	msgs, err := f.store.ListMessages(context.Background(), f.convID, 50, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var rows []store.Message
	for _, m := range msgs {
		if m.Role == "error" {
			rows = append(rows, m)
		}
	}
	return rows
}

func holdAccountSlot(t *testing.T, pool *Pool) *Ticket {
	t.Helper()
	holder, err := pool.EnterForUser("", admissionTestAccount, "model-1")
	if err != nil {
		t.Fatalf("enter holder: %v", err)
	}
	if err := holder.Wait(context.Background()); err != nil {
		t.Fatalf("wait holder: %v", err)
	}
	return holder
}

func TestProcessPromptAdmissionRefusals(t *testing.T) {
	tests := []struct {
		name string
		// arrange puts the fixture into the refusing state and returns cleanup.
		arrange func(t *testing.T, f *admissionFixture) func()
		// wantStatus is the status frame expected instead of an error; empty
		// means an error frame (and a persisted error row) is expected.
		wantStatus string
		wantError  string
	}{
		{
			name: "draining server",
			arrange: func(_ *testing.T, f *admissionFixture) func() {
				f.runner.Drainer.StartDrain(DrainReasonUpgrade)
				return func() {}
			},
			wantStatus: "shutdown",
		},
		{
			name: "account cooling down",
			arrange: func(_ *testing.T, f *admissionFixture) func() {
				f.pool.Cooldown(admissionTestAccount, "model-1", time.Now().Add(time.Hour))
				return func() {}
			},
			wantError: `account "acc1" model "model-1" is rate-limited`,
		},
		{
			name: "no binding",
			arrange: func(_ *testing.T, f *admissionFixture) func() {
				f.store.Users[0].ProviderBindings = nil
				return func() {}
			},
			wantError: `no account bound for "claude"`,
		},
		{
			name: "queue wait gives up",
			arrange: func(t *testing.T, f *admissionFixture) func() {
				holder := holdAccountSlot(t, f.pool)
				prev := promptQueueWait
				promptQueueWait = 50 * time.Millisecond
				return func() {
					promptQueueWait = prev
					holder.Release()
				}
			},
			wantError: `account "acc1" has no free slot: gave up waiting after 50ms`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAdmissionFixture(t, newBlockingBackend())
			cleanup := tt.arrange(t, f)
			f.process()
			cleanup()

			frames := f.drainFrames(t)
			rows := f.errorRows(t)
			if tt.wantStatus != "" {
				if len(frames) == 0 || frames[len(frames)-1].Type != "status" || frames[len(frames)-1].Status != tt.wantStatus {
					t.Fatalf("frames = %+v, want a final %q status", frames, tt.wantStatus)
				}
				if len(rows) != 0 {
					t.Fatalf("error rows = %+v, want none", rows)
				}
			} else {
				var got *ServerMessage
				for i := range frames {
					if frames[i].Type == "error" {
						got = &frames[i]
					}
				}
				if got == nil || !strings.Contains(got.Message, tt.wantError) {
					t.Fatalf("frames = %+v, want an error containing %q", frames, tt.wantError)
				}
				if len(rows) != 1 || rows[0].Content != got.Message || got.MessageID != rows[0].ID {
					t.Fatalf("error rows = %+v, want the broadcast error persisted once", rows)
				}
			}
			f.assertAllReleased(t)
			// The prompt never ran, so it must still be the pending row the
			// dispatcher hands back to the user.
			if f.store.Messages[f.convID][0].QueueStatus != "pending" {
				t.Errorf("prompt status = %q, want it left pending", f.store.Messages[f.convID][0].QueueStatus)
			}
		})
	}
}

// A prompt recalled while it waits for a slot is not a failure: the staging
// area expects a quiet "ready", no error row.
func TestProcessPromptRecallWhileQueuedIsQuiet(t *testing.T) {
	f := newAdmissionFixture(t, newBlockingBackend())
	holder := holdAccountSlot(t, f.pool)
	defer holder.Release()

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.process()
	}()
	waitForBroadcastType(t, f.frames, "queue_status")
	if !f.runner.Broadcaster.CancelJob(f.convID) {
		t.Fatal("queued prompt was not registered with the broadcaster")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recalled prompt never returned")
	}
	frames := f.drainFrames(t)
	if len(frames) == 0 || frames[len(frames)-1].Status != "ready" {
		t.Fatalf("frames = %+v, want a final ready status", frames)
	}
	if rows := f.errorRows(t); len(rows) != 0 {
		t.Fatalf("error rows = %+v, want none for a recall", rows)
	}
	holder.Release()
	f.assertAllReleased(t)
}

// While running, a turn holds the room, one running drainer entry and one
// account slot; afterwards it holds nothing.
func TestProcessPromptHoldsThenReleasesItsAdmission(t *testing.T) {
	backend := newBlockingBackend()
	f := newAdmissionFixture(t, backend)

	done := make(chan struct{})
	go func() {
		defer close(done)
		f.process()
	}()
	select {
	case <-backend.started:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never started")
	}
	if !f.runner.Broadcaster.IsBusy(f.convID) {
		t.Error("room not held while the turn runs")
	}
	jobs := f.runner.Drainer.Jobs()
	if len(jobs) != 1 || jobs[0].Status != JobStatusRunning || jobs[0].ConversationID != f.convID ||
		jobs[0].AccountName != admissionTestAccount || jobs[0].ProviderType != config.CLITypeClaude || jobs[0].UserID != "u1" {
		t.Errorf("drainer jobs = %+v, want one running job for the conversation", jobs)
	}
	if inUse, _, _ := f.pool.Stats(admissionTestAccount); inUse != 1 {
		t.Errorf("pool inUse = %d, want 1 while running", inUse)
	}
	close(backend.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn never finished")
	}
	frames := f.drainFrames(t)
	if len(frames) == 0 || frames[len(frames)-1].Status != "ready" {
		t.Fatalf("frames = %+v, want the turn to end on ready", frames)
	}
	f.assertAllReleased(t)
}
