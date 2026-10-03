package service_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

type activityStore struct {
	*queueStore
	users         map[string]store.User
	conversations map[string]store.Conversation
}

func (s *activityStore) GetUser(_ context.Context, id string) (store.User, error) {
	user, ok := s.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return user, nil
}

func (s *activityStore) GetConversation(_ context.Context, id string) (store.Conversation, error) {
	conversation, ok := s.conversations[id]
	if !ok {
		return store.Conversation{}, store.ErrNotFound
	}
	return conversation, nil
}

func TestBroadcastConversationActivityDeliversGlobalSnapshotToAllUsers(t *testing.T) {
	ctx := context.Background()
	s := &activityStore{
		queueStore: newQueueStore(),
		users: map[string]store.User{
			"owner-1": {ID: "owner-1", Name: "Alice User", Username: "alice"},
			"owner-2": {ID: "owner-2", Name: "Bob User", Username: "bob"},
			"agent-1": {ID: "agent-1", Name: "Agent One", OwnerID: "owner-1"},
			"agent-2": {ID: "agent-2", Name: "Agent Two", OwnerID: "owner-2"},
		},
		conversations: map[string]store.Conversation{
			"conv-1": {ID: "conv-1", UserID: "agent-1", Model: "model-1"},
			"conv-2": {ID: "conv-2", UserID: "agent-2", Model: "model-2"},
		},
	}
	drainer := service.NewDrainer()
	defer drainer.JobStartWithInfo(service.DrainerJob{
		UserID: "owner-1", AccountName: "alice-claude", ConversationID: "conv-1", Status: service.JobStatusRunning,
	})()
	defer drainer.JobStartWithInfo(service.DrainerJob{
		UserID: "owner-2", AccountName: "bob-codex", ConversationID: "conv-2", Status: service.JobStatusRunning,
	})()

	hub := service.NewUserHub()
	first := make(chan []byte, 1)
	second := make(chan []byte, 1)
	hub.Join("owner-1", "client-1", first)
	hub.Join("owner-2", "client-2", second)

	service.BroadcastConversationActivity(ctx, hub, drainer, s, "owner-1")

	for label, ch := range map[string]chan []byte{"owner-1": first, "owner-2": second} {
		select {
		case data := <-ch:
			var msg service.ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("%s decode activity: %v", label, err)
			}
			if len(msg.RunningConversations) != 2 {
				t.Fatalf("%s got %d running conversations, want 2", label, len(msg.RunningConversations))
			}
			if msg.RunningConversations[1].AgentName != "Agent Two" {
				t.Fatalf("%s missing cross-owner agent: %+v", label, msg.RunningConversations)
			}
			if msg.RunningConversations[1].OwnerUsername != "bob" {
				t.Fatalf("%s missing cross-owner username: %+v", label, msg.RunningConversations)
			}
			if msg.RunningConversations[1].OwnerName != "Bob User" {
				t.Fatalf("%s missing cross-owner name: %+v", label, msg.RunningConversations)
			}
			if msg.RunningConversations[1].AccountName != "bob-codex" {
				t.Fatalf("%s missing resolved account: %+v", label, msg.RunningConversations)
			}
		default:
			t.Fatalf("%s did not receive global activity snapshot", label)
		}
	}
}

func TestConversationActivitySnapshotSeparatesQueuedConversations(t *testing.T) {
	ctx := context.Background()
	s := &activityStore{
		queueStore: newQueueStore(),
		users: map[string]store.User{
			"agent-running": {ID: "agent-running", Name: "Running Agent"},
			"agent-queued":  {ID: "agent-queued", Name: "Queued Agent"},
		},
		conversations: map[string]store.Conversation{
			"conv-running": {ID: "conv-running", UserID: "agent-running", Model: "model-running"},
			"conv-queued":  {ID: "conv-queued", UserID: "agent-queued", Model: "model-queued"},
		},
	}
	drainer := service.NewDrainer()
	defer drainer.JobStartWithInfo(service.DrainerJob{
		UserID: "owner", AccountName: "account-running", ConversationID: "conv-running", Status: service.JobStatusRunning,
	})()
	defer drainer.JobStartWithInfo(service.DrainerJob{
		UserID: "owner", AccountName: "account-queued", ConversationID: "conv-queued", Status: service.JobStatusQueued,
	})()

	msg := service.ConversationActivitySnapshot(ctx, drainer, s, "owner")

	if len(msg.RunningConversations) != 1 || msg.RunningConversations[0].ConversationID != "conv-running" {
		t.Fatalf("running conversations = %+v", msg.RunningConversations)
	}
	if len(msg.QueuedConversations) != 1 || msg.QueuedConversations[0].ConversationID != "conv-queued" {
		t.Fatalf("queued conversations = %+v", msg.QueuedConversations)
	}
	if !reflect.DeepEqual(msg.RunningAgentIDs, []string{"agent-running"}) {
		t.Fatalf("running agent ids = %v", msg.RunningAgentIDs)
	}
	if !reflect.DeepEqual(msg.RunningConversationIDs, []string{"conv-running"}) {
		t.Fatalf("running conversation ids = %v", msg.RunningConversationIDs)
	}
}

func TestConversationActivitySnapshotIncludesResidentBackgroundWork(t *testing.T) {
	ctx := context.Background()
	s := &activityStore{
		queueStore: newQueueStore(),
		users: map[string]store.User{
			"owner": {ID: "owner", Name: "Alice", Username: "alice"},
			"agent": {ID: "agent", Name: "Background Agent", OwnerID: "owner"},
		},
		conversations: map[string]store.Conversation{
			"conv-resident": {ID: "conv-resident", UserID: "agent", Model: "claude-test"},
		},
	}
	drainer := service.NewDrainer()
	resident := service.DrainerJob{
		UserID: "owner", Username: "alice", AccountName: "claude-main", ConversationID: "conv-resident",
	}
	drainer.SetResidentActivity("bridge-1", resident, true)

	msg := service.ConversationActivitySnapshot(ctx, drainer, s, "owner")
	if !reflect.DeepEqual(msg.RunningConversationIDs, []string{"conv-resident"}) ||
		!reflect.DeepEqual(msg.RunningAgentIDs, []string{"agent"}) {
		t.Fatalf("resident activity snapshot = %+v", msg)
	}
	if len(msg.RunningConversations) != 1 || msg.RunningConversations[0].AccountName != "claude-main" ||
		!msg.RunningConversations[0].Background {
		t.Fatalf("resident detail = %+v", msg.RunningConversations)
	}

	// A follow-up prompt can overlap the same resident process. The rail still
	// gets one row, with the foreground job taking precedence in the union.
	done := drainer.JobStartWithInfo(service.DrainerJob{
		UserID: "owner", AccountName: "foreground-account", ConversationID: "conv-resident",
	})
	defer done()
	msg = service.ConversationActivitySnapshot(ctx, drainer, s, "owner")
	if len(msg.RunningConversations) != 1 || msg.RunningConversations[0].AccountName != "foreground-account" ||
		msg.RunningConversations[0].Background {
		t.Fatalf("foreground/resident de-duplication = %+v", msg.RunningConversations)
	}
}

func TestConversationActivitySnapshotIncludesPersistedPendingConversation(t *testing.T) {
	ctx := context.Background()
	queuedAt := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	s := &activityStore{
		queueStore: newQueueStore(),
		users: map[string]store.User{
			"owner": {ID: "owner", Name: "Alice", Username: "alice"},
			"agent": {
				ID: "agent", Name: "Paused Agent", OwnerID: "owner",
				ProviderBindings: map[string]string{"codex": "codex-account"},
			},
		},
		conversations: map[string]store.Conversation{
			"conv-paused": {ID: "conv-paused", UserID: "agent", Provider: "codex", Model: "gpt-test"},
		},
	}
	s.messages = append(s.messages, store.Message{
		ID: "prompt-1", ConversationID: "conv-paused", Role: "user",
		QueueStatus: "pending", CreatedAt: queuedAt,
	})

	msg := service.ConversationActivitySnapshot(ctx, service.NewDrainer(), s, "owner")

	if len(msg.QueuedConversations) != 1 {
		t.Fatalf("queued conversations = %+v", msg.QueuedConversations)
	}
	got := msg.QueuedConversations[0]
	if got.ConversationID != "conv-paused" || got.AgentName != "Paused Agent" ||
		got.OwnerUsername != "alice" || got.AccountName != "codex-account" ||
		got.Model != "gpt-test" || !got.StartedAt.Equal(queuedAt) {
		t.Fatalf("queued activity = %+v", got)
	}
}

// A turn parked on an AskUserQuestion prompt is burning no account capacity, so
// it must not show up as running work — and it isn't queued for a slot either,
// because nothing will ask for one until the human answers. It is reported on
// its own so the navigation can flag it as needing a reply.
func TestConversationActivitySnapshotSeparatesTurnsWaitingOnTheUser(t *testing.T) {
	ctx := context.Background()
	s := &activityStore{
		queueStore: newQueueStore(),
		users: map[string]store.User{
			"agent-waiting": {ID: "agent-waiting", Name: "Waiting Agent"},
		},
		conversations: map[string]store.Conversation{
			"conv-waiting": {ID: "conv-waiting", UserID: "agent-waiting", Model: "model-waiting"},
		},
	}
	drainer := service.NewDrainer()
	defer drainer.JobStartWithInfo(service.DrainerJob{
		UserID: "owner", AccountName: "account-waiting", ConversationID: "conv-waiting", Status: service.JobStatusWaiting,
	})()

	msg := service.ConversationActivitySnapshot(ctx, drainer, s, "owner")

	if len(msg.RunningConversations) != 0 {
		t.Fatalf("running conversations = %+v, want none", msg.RunningConversations)
	}
	if len(msg.QueuedConversations) != 0 {
		t.Fatalf("queued conversations = %+v, want none", msg.QueuedConversations)
	}
	if len(msg.WaitingConversations) != 1 ||
		msg.WaitingConversations[0].ConversationID != "conv-waiting" ||
		msg.WaitingConversations[0].AgentID != "agent-waiting" {
		t.Fatalf("waiting conversations = %+v", msg.WaitingConversations)
	}
}

// The snapshot that drops a failed job must already name it as failed, and a
// later turn in the same conversation clears the stale failure.
func TestConversationActivitySnapshotReportsFailedTurnsUntilTheNextTurn(t *testing.T) {
	ctx := context.Background()
	s := &activityStore{
		queueStore: newQueueStore(),
		users:      map[string]store.User{"agent": {ID: "agent", Name: "Agent"}},
		conversations: map[string]store.Conversation{
			"conv": {ID: "conv", UserID: "agent"},
		},
	}
	drainer := service.NewDrainer()
	done := drainer.JobStartWithInfo(service.DrainerJob{UserID: "owner", ConversationID: "conv"})
	drainer.MarkConversationFailed("conv")
	done()

	msg := service.ConversationActivitySnapshot(ctx, drainer, s, "owner")
	if !reflect.DeepEqual(msg.FailedConversationIDs, []string{"conv"}) {
		t.Fatalf("failed conversation ids = %v, want [conv]", msg.FailedConversationIDs)
	}

	defer drainer.JobStartWithInfo(service.DrainerJob{UserID: "owner", ConversationID: "conv"})()
	msg = service.ConversationActivitySnapshot(ctx, drainer, s, "owner")
	if len(msg.FailedConversationIDs) != 0 {
		t.Fatalf("failed conversation ids = %v after a new turn, want none", msg.FailedConversationIDs)
	}
}
