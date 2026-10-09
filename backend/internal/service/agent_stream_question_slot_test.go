package service

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// questioningBackend asks one question, blocks until the answer is delivered,
// then finishes the turn — the shape of a real AskUserQuestion round trip.
type questioningBackend struct {
	asked           chan struct{}
	answered        chan map[string][]string
	questionPayload string
}

func newQuestioningBackend() *questioningBackend {
	return &questioningBackend{
		asked:           make(chan struct{}),
		answered:        make(chan map[string][]string, 1),
		questionPayload: `{"request_id":"ask-1","questions":[]}`,
	}
}

func (*questioningBackend) Name() string                     { return "questioning" }
func (*questioningBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (b *questioningBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindUserQuestion, Content: b.questionPayload}
	close(b.asked)
	select {
	case <-b.answered:
	case <-ctx.Done():
		close(ch)
		return ctx.Err()
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "done"}
	close(ch)
	return nil
}

func (*questioningBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*questioningBackend) SessionExists(string, string, string) bool    { return false }
func (*questioningBackend) SessionLogPath(string, string, string) string { return "" }

func (b *questioningBackend) AnswerUserQuestion(_ context.Context, _, _ string, answers map[string][]string) error {
	b.answered <- answers
	return nil
}

type questionNotificationCall struct {
	credential string
	title      string
	body       string
}

type questionBarkCapture struct {
	calls chan questionNotificationCall
}

func (c *questionBarkCapture) Send(_ context.Context, credential, title, body, _ string) error {
	c.calls <- questionNotificationCall{credential: credential, title: title, body: body}
	return nil
}

type questionPushDeerCapture struct {
	calls chan questionNotificationCall
}

func (c *questionPushDeerCapture) Send(_ context.Context, credential, title, body string) error {
	c.calls <- questionNotificationCall{credential: credential, title: title, body: body}
	return nil
}

func TestAgentStreamerNotifiesWhenWaitingForUserAnswer(t *testing.T) {
	tests := []struct {
		name           string
		owner          store.User
		wantChannel    string
		wantCredential string
	}{
		{
			name:           "bark",
			owner:          store.User{ID: "owner", Username: "owner", BarkURL: "https://bark.example/key"},
			wantChannel:    "bark",
			wantCredential: "https://bark.example/key",
		},
		{
			name:           "pushdeer",
			owner:          store.User{ID: "owner", Username: "owner", PushDeerKey: "push-key"},
			wantChannel:    "pushdeer",
			wantCredential: "push-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const convID = "conv-question-notify"
			ms := storetest.New()
			ms.Users = []store.User{tt.owner}
			ms.Conversations = []store.Conversation{{
				ID: convID, UserID: tt.owner.ID, Title: "Release deployment", NotificationsEnabled: true,
			}}
			bark := &questionBarkCapture{calls: make(chan questionNotificationCall, 1)}
			pushDeer := &questionPushDeerCapture{calls: make(chan questionNotificationCall, 1)}
			backend := newQuestioningBackend()
			backend.questionPayload = `{"request_id":"ask-1","questions":[{"id":"environment","question":"Which environment should I deploy to?"}]}`

			broadcaster := NewBroadcaster()
			ctx, cancel := context.WithCancel(context.Background())
			if !broadcaster.StartJob(convID, cancel) {
				t.Fatal("StartJob refused a fresh conversation")
			}
			defer broadcaster.EndJob(convID)

			streamer := &AgentStreamer{
				Store:       ms,
				Broadcaster: broadcaster,
				Persist: &MessagePersister{
					Store: ms, BarkSender: bark, PushDeerSender: pushDeer,
				},
			}
			done := make(chan AgentStreamResult, 1)
			go func() {
				done <- streamer.Run(ctx, AgentStreamRequest{
					Backend: backend, ConversationID: convID,
					Opts: agent.RunRequest{EnableUserQuestions: true, ControlID: convID},
					Mode: AgentStreamPerResult, Notify: true,
				})
			}()

			var calls <-chan questionNotificationCall
			var unexpected <-chan questionNotificationCall
			if tt.wantChannel == "bark" {
				calls, unexpected = bark.calls, pushDeer.calls
			} else {
				calls, unexpected = pushDeer.calls, bark.calls
			}
			select {
			case call := <-calls:
				if call.credential != tt.wantCredential {
					t.Errorf("credential = %q, want %q", call.credential, tt.wantCredential)
				}
				if call.title != "Release deployment" {
					t.Errorf("title = %q, want %q", call.title, "Release deployment")
				}
				if call.body != "Your reply is needed:\nWhich environment should I deploy to?" {
					t.Errorf("body = %q", call.body)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s notification was not sent", tt.wantChannel)
			}
			select {
			case call := <-unexpected:
				t.Fatalf("notification also sent through the unselected channel: %+v", call)
			default:
			}

			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("turn did not stop after cancellation")
			}
		})
	}
}

// A turn blocked on a user question must not keep charging the account's
// concurrency limit, and the answer must re-enter the queue rather than resume
// the turn ahead of whoever started while the question sat unanswered.
func TestAgentStreamerReleasesTheSlotWhileAQuestionIsUnanswered(t *testing.T) {
	const convID = "conv-question"
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID}}
	broadcaster := NewBroadcaster()
	pool := newTestPool(1, 0)
	backend := newQuestioningBackend()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !broadcaster.StartJob(convID, cancel) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer broadcaster.EndJob(convID)

	ticket, err := pool.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	gate := NewTurnSlotGate(ticket, poolAcquire(pool, "default"), nil, nil)
	defer gate.Close()

	streamer := &AgentStreamer{Store: ms, Broadcaster: broadcaster, Pool: pool}
	done := make(chan AgentStreamResult, 1)
	go func() {
		done <- streamer.Run(ctx, AgentStreamRequest{
			Backend:        backend,
			WorkDir:        t.TempDir(),
			ConversationID: convID,
			Opts:           agent.RunRequest{EnableUserQuestions: true, ControlID: convID},
			Mode:           AgentStreamPerResult,
			SlotGate:       gate,
			Broadcast:      func(ServerMessage) {},
		})
	}()

	select {
	case <-backend.asked:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never asked its question")
	}
	waitUntil(t, "parked turn to release its slot", func() bool { return inUse(t, pool) == 0 })

	// Another turn takes the freed slot before the human gets round to
	// answering: the answer now has to queue behind it.
	other, err := pool.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter other: %v", err)
	}
	if err := other.Wait(ctx); err != nil {
		t.Fatalf("wait other: %v", err)
	}

	answerCtx, answerCancel := context.WithTimeout(ctx, 2*time.Second)
	defer answerCancel()
	attempted, err := broadcaster.AnswerJobQuestion(answerCtx, convID, "ask-1", map[string][]string{"q": {"yes"}})
	if !attempted || err != nil {
		t.Fatalf("AnswerJobQuestion attempted=%v err=%v", attempted, err)
	}

	select {
	case answers := <-backend.answered:
		t.Fatalf("answer %v reached the backend while the account was full", answers)
	case <-time.After(50 * time.Millisecond):
	}

	other.Release()
	select {
	case result := <-done:
		if result.Err != nil {
			t.Fatalf("run: %v", result.Err)
		}
		if result.Content != "done" {
			t.Fatalf("content = %q, want %q", result.Content, "done")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn never resumed after the slot freed")
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
