package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func newIntakeFixture(t *testing.T, processed chan<- string) (*PromptIntake, *storetest.Fake) {
	t.Helper()
	ms := storetest.New()
	ctx := context.Background()
	// The fake resolves an agent's owner through the shared email.
	_ = ms.CreateUser(ctx, store.User{ID: "owner-1", Username: "alice", Email: "a@example.com", WorkDir: t.TempDir()})
	_ = ms.CreateUser(ctx, store.User{ID: "agent-1", OwnerID: "owner-1", Email: "a@example.com", WorkDir: t.TempDir()})
	_ = ms.CreateConversation(ctx, "c1", "", "agent-1", t.TempDir(), "", "")
	d := NewDispatcher(ms, func(_ context.Context, prompt store.Message) { processed <- prompt.Content })
	if _, _, _, err := d.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(d.Stop)
	return &PromptIntake{Store: ms, Dispatcher: d, Broadcaster: NewBroadcaster(), UserHub: NewUserHub()}, ms
}

// The transport's echo and ack must reach the client before the worker can
// start the turn, or peer tabs render the reply ahead of the prompt.
func TestPromptIntakeQueuesBeforeKickingTheWorker(t *testing.T) {
	order := make(chan string, 4)
	intake, _ := newIntakeFixture(t, order)
	saved, err := intake.Submit(context.Background(), PromptSubmission{
		ConversationID: "c1",
		Content:        "hello",
		OnQueued: func(saved store.Message) {
			if saved.QueueStatus != "pending" {
				t.Errorf("OnQueued saw queue_status %q, want pending", saved.QueueStatus)
			}
			order <- "queued"
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if saved.Content != "hello" {
		t.Fatalf("saved = %+v", saved)
	}
	for _, want := range []string{"queued", "hello"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("event = %q, want %q (echo must precede the run)", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("never saw %q", want)
		}
	}
}

func TestPromptIntakeRefusesBeforePersisting(t *testing.T) {
	intake, ms := newIntakeFixture(t, make(chan string, 1))
	for _, tc := range []struct {
		name string
		sub  PromptSubmission
		want string
	}{
		{"no conversation joined", PromptSubmission{Content: "x"}, "init required before input"},
		{"unknown conversation", PromptSubmission{ConversationID: "missing", Content: "x"}, "conversation not found"},
		{"bad attachments", PromptSubmission{
			ConversationID: "c1", Content: "x",
			AttachmentMetadata: func(string, string) (json.RawMessage, error) { return nil, context.Canceled },
		}, "invalid attachment metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queued := false
			tc.sub.OnQueued = func(store.Message) { queued = true }
			_, err := intake.Submit(context.Background(), tc.sub)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if queued {
				t.Fatal("a refused prompt reached OnQueued")
			}
		})
	}
	if msgs := ms.SnapshotMessages("c1"); len(msgs) != 0 {
		t.Fatalf("refused prompts were persisted: %+v", msgs)
	}
}

// Attachment metadata is scoped to the conversation's agent and anchored at
// that agent's human owner, both derived server-side.
func TestPromptIntakeResolvesAttachmentScope(t *testing.T) {
	intake, _ := newIntakeFixture(t, make(chan string, 1))
	var gotAgent, gotOwner string
	saved, err := intake.Submit(context.Background(), PromptSubmission{
		ConversationID: "c1", Content: "see file",
		AttachmentMetadata: func(agentID, homeOwnerID string) (json.RawMessage, error) {
			gotAgent, gotOwner = agentID, homeOwnerID
			return json.RawMessage(`{"attachments":[]}`), nil
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if gotAgent != "agent-1" || gotOwner != "owner-1" {
		t.Fatalf("attachment scope = %q/%q, want agent-1/owner-1", gotAgent, gotOwner)
	}
	if string(saved.Metadata) != `{"attachments":[]}` {
		t.Fatalf("metadata = %s", saved.Metadata)
	}
}

func TestPromptIntakeAppendsAttachmentRefWhenClientOmitsIt(t *testing.T) {
	processed := make(chan string, 1)
	intake, ms := newIntakeFixture(t, processed)
	uploadsRel, err := AgentUploadsDir("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	attachmentPath := uploadsRel + "/shot.png"
	saved, err := intake.Submit(context.Background(), PromptSubmission{
		ConversationID: "c1",
		Content:        "inspect the image",
		AttachmentMetadata: func(string, string) (json.RawMessage, error) {
			return json.Marshal(map[string]any{
				"attachments": []map[string]string{{"path": attachmentPath}},
			})
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	owner, err := ms.GetUser(context.Background(), "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "inspect the image\n\n" + DaymugPath(owner.WorkDir, attachmentPath)
	if saved.Content != want {
		t.Fatalf("saved content = %q, want %q", saved.Content, want)
	}
	select {
	case got := <-processed:
		if got != want {
			t.Fatalf("processed content = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prompt was not processed")
	}
}

func TestAppendWebAttachmentRefsDoesNotDuplicateClientRef(t *testing.T) {
	homeRoot := t.TempDir()
	uploadsRel, err := AgentUploadsDir("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	attachmentPath := uploadsRel + "/shot.png"
	ref := DaymugPath(homeRoot, attachmentPath)
	metadata, err := json.Marshal(map[string]any{
		"attachments": []map[string]string{{"path": attachmentPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := "inspect the image\n\n" + ref
	if got := appendWebAttachmentRefs(content, metadata, "agent-1", homeRoot); got != content {
		t.Fatalf("content = %q, want unchanged %q", got, content)
	}
}

func TestAppendWebAttachmentRefsRejectsForeignScope(t *testing.T) {
	homeRoot := t.TempDir()
	foreignRel, err := AgentUploadsDir("agent-2")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"attachments": []map[string]string{{"path": foreignRel + "/shot.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const content = "inspect the image"
	if got := appendWebAttachmentRefs(content, metadata, "agent-1", homeRoot); got != content {
		t.Fatalf("content = %q, want unchanged %q", got, content)
	}
}
