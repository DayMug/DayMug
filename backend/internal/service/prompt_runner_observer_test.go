package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// recordingObserver captures whether the conversation already had a registered
// job at the moment ObservePrompt ran. The IM bridge acquires a cross-surface
// mutex there, so the answer decides which order the two surfaces contend in.
type recordingObserver struct {
	called      chan struct{}
	busyOnEntry bool
	broadcaster *Broadcaster
	convID      string
}

func (o *recordingObserver) ObservePrompt(context.Context, store.Message) PromptObservation {
	o.busyOnEntry = o.broadcaster.IsBusy(o.convID)
	close(o.called)
	return nil
}

func newRefusedJobRunner(t *testing.T, convID string) (*PromptRunner, *Broadcaster, chan []byte) {
	t.Helper()
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID, UserID: "u1", Provider: config.CLITypeClaude}}
	ms.Users = []store.User{{ID: "u1", Username: "u1"}}
	broadcaster := NewBroadcaster()
	frames := make(chan []byte, 16)
	broadcaster.Join(convID, "client1", frames)
	return &PromptRunner{
		Store:       ms,
		Broadcaster: broadcaster,
		// A pool with no binding for this user makes account resolution fail
		// right after the job registration, so the test exercises only the
		// observe/StartJob handshake.
		Pool: NewPool(&config.Config{Providers: []config.Provider{
			{Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1},
		}}),
	}, broadcaster, frames
}

func waitForBroadcastType(t *testing.T, frames <-chan []byte, want string) ServerMessage {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case data := <-frames:
			var msg ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode broadcast: %v", err)
			}
			if msg.Type == want {
				return msg
			}
		case <-deadline:
			t.Fatalf("no %q broadcast arrived", want)
		}
	}
}

// TestProcessPromptObservesBeforeRegisteringJob locks in the single lock order
// shared by the web and IM paths: observer lock first, Broadcaster job second,
// account-pool slot last. Registering the job first inverts it against the IM
// inbound path and turns a cross-surface collision into a lost prompt.
func TestProcessPromptObservesBeforeRegisteringJob(t *testing.T) {
	const convID = "conv-order"
	runner, broadcaster, _ := newRefusedJobRunner(t, convID)
	observer := &recordingObserver{called: make(chan struct{}), broadcaster: broadcaster, convID: convID}
	runner.Observer = observer

	runner.ProcessPrompt(context.Background(), store.Message{ID: "m1", ConversationID: convID, Content: "hi"})

	select {
	case <-observer.called:
	default:
		t.Fatal("observer was never consulted")
	}
	if observer.busyOnEntry {
		t.Fatal("the Broadcaster job was registered before the observer lock was taken; that inverts the IM inbound order")
	}
}

// A provider-initiated wakeup owns the room outside the dispatcher's worker.
// A prompt arriving then must remain with the blocked processor until the
// wakeup finishes; returning would make runWorker sweep its pending status.
func TestProcessPromptWaitsBehindConcurrentWakeup(t *testing.T) {
	const convID = "conv-refused"
	runner, broadcaster, frames := newRefusedJobRunner(t, convID)
	if !broadcaster.StartJob(convID, func() {}) {
		t.Fatal("StartJob refused a fresh conversation")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.ProcessPrompt(context.Background(), store.Message{ID: "m1", ConversationID: convID, Content: "hi"})
	}()
	select {
	case <-done:
		t.Fatal("prompt returned while the wakeup still owned the room")
	case data := <-frames:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decode broadcast: %v", err)
		}
		if msg.Type == "error" {
			t.Fatalf("prompt produced an error while waiting: %+v", msg)
		}
	case <-time.After(20 * time.Millisecond):
	}

	broadcaster.EndJob(convID)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("prompt did not resume after the wakeup released the room")
	}
	msg := waitForBroadcastType(t, frames, "error")
	if msg.Message == "another run is already in progress for this conversation" {
		t.Fatalf("concurrent wakeup error survived: %+v", msg)
	}
}

func TestProcessPromptBroadcastsQueuedActivityBeforeSlotGrant(t *testing.T) {
	const convID = "conv-queued-activity"
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: convID, UserID: "u1", Provider: config.CLITypeClaude, Model: "model-1",
	}}
	ms.Users = []store.User{{
		ID: "u1", Username: "u1", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}}
	pool := NewPool(&config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}})
	holder, err := pool.EnterForUser("", "acc1", "model-1")
	if err != nil {
		t.Fatalf("enter holder: %v", err)
	}
	if err := holder.Wait(context.Background()); err != nil {
		t.Fatalf("wait holder: %v", err)
	}
	defer holder.Release()

	broadcaster := NewBroadcaster()
	hub := NewUserHub()
	activityFrames := make(chan []byte, 4)
	hub.Join("u1", "client-1", activityFrames)
	runner := &PromptRunner{
		Store:       ms,
		Broadcaster: broadcaster,
		UserHub:     hub,
		Drainer:     NewDrainer(),
		Pool:        pool,
	}
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		runner.ProcessPrompt(context.Background(), store.Message{
			ID: "m1", ConversationID: convID, Content: "hi",
		})
	}()

	select {
	case data := <-activityFrames:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decode activity: %v", err)
		}
		if len(msg.QueuedConversations) != 1 ||
			msg.QueuedConversations[0].ConversationID != convID {
			t.Fatalf("queued activity = %+v", msg.QueuedConversations)
		}
		if len(msg.RunningConversations) != 0 {
			t.Fatalf("running activity before slot grant = %+v", msg.RunningConversations)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued activity was not broadcast before the slot was granted")
	}

	if !broadcaster.CancelJob(convID) {
		t.Fatal("queued job was not registered with the broadcaster")
	}
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("queued prompt did not stop after cancellation")
	}
}
