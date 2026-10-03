package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// Cancelling must survive a concurrent commit. DeletePendingPrompts reads the
// rows it is about to delete, so a write landing between its SELECT and its
// DELETE invalidates the transaction's snapshot — SQLite reports that as
// SQLITE_BUSY_SNAPSHOT immediately and deliberately skips the busy handler, so
// the DSN's busy_timeout does nothing. Pre-fix the cancel returned an error the
// WS handler only logged, and the prompt the user just cancelled ran anyway.
//
// Needs a real file: every :memory: connection in the modernc driver is its own
// database, so there is nothing to contend over.
func TestDeletePendingPrompts_SurvivesConcurrentCommits(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	ctx := context.Background()
	for _, cid := range []string{"target", "noisy"} {
		if err := s.CreateConversation(ctx, cid, "T", "u1", "/tmp", "", ""); err != nil {
			t.Fatalf("create conversation %s: %v", cid, err)
		}
	}

	// One racing commit per round, not a continuous stream: a writer that never
	// pauses can starve any bounded retry, and that is not the shape of the
	// real workload (a streaming turn persists context_usage periodically).
	// A single commit can only invalidate one snapshot, so the retry is
	// guaranteed to win — while the unpatched code fails whenever the commit
	// lands in the window between the SELECT and the DELETE.
	const rounds = 100
	for i := 0; i < rounds; i++ {
		if _, err := s.EnqueuePrompt(ctx, "target", fmt.Sprintf("p-%d", i)); err != nil {
			t.Fatalf("round %d enqueue: %v", i, err)
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.EnqueuePrompt(ctx, "noisy", fmt.Sprintf("noise-%d", i))
		}()

		_, err := s.DeletePendingPrompts(ctx, "target")
		wg.Wait()
		if err != nil {
			t.Fatalf("round %d cancel: %v", i, err)
		}
	}
}

// A prompt that keeps killing the server before it can finish (e.g. one that
// restarts the daymug service) must not be resumed forever. After
// maxPromptRecoveries demotions it is abandoned instead of demoted, breaking
// the crash-recover-rerun loop. recovery_count survives the claim re-insert,
// so the counter actually accumulates across attempts.
func TestResetProcessingToPending_AbandonsPoisonPrompt(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateConversation(ctx, "c1", "", "u1", "/tmp", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	msg, err := s.EnqueuePrompt(ctx, "c1", "systemctl --user restart daymug")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Simulate the loop: each round claims the prompt (worker picks it up),
	// then the server dies mid-run and startup recovery demotes it again.
	for round := 1; round <= maxPromptRecoveries-1; round++ {
		if _, err := s.ClaimPendingPromptByID(ctx, msg.ID); err != nil {
			t.Fatalf("round %d claim: %v", round, err)
		}
		recovered, abandoned, err := s.ResetProcessingToPending(ctx)
		if err != nil {
			t.Fatalf("round %d reset: %v", round, err)
		}
		if recovered != 1 || abandoned != 0 {
			t.Fatalf("round %d: recovered=%d abandoned=%d, want 1/0", round, recovered, abandoned)
		}
	}

	// The next recovery crosses the cap: the prompt is abandoned, not resumed.
	if _, err := s.ClaimPendingPromptByID(ctx, msg.ID); err != nil {
		t.Fatalf("final claim: %v", err)
	}
	recovered, abandoned, err := s.ResetProcessingToPending(ctx)
	if err != nil {
		t.Fatalf("final reset: %v", err)
	}
	if recovered != 0 || abandoned != 1 {
		t.Fatalf("final: recovered=%d abandoned=%d, want 0/1", recovered, abandoned)
	}

	// Abandoned prompt drops out of the pending set — the dispatcher won't
	// resume it, so the loop is broken.
	convs, err := s.ListConversationsWithPending(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(convs) != 0 {
		t.Fatalf("expected no conversations with pending work, got %v", convs)
	}
}

func TestExclusiveAttachmentPaths_SkipsPathsLiveConversationsShare(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	save := func(convID, msgID, metadata string) {
		t.Helper()
		if err := s.SaveMessage(ctx, Message{ID: msgID, ConversationID: convID, Role: "user", Metadata: json.RawMessage(metadata)}); err != nil {
			t.Fatalf("save %s: %v", msgID, err)
		}
	}
	for _, id := range []string{"target", "live", "gone"} {
		if err := s.CreateConversation(ctx, id, "T", "u1", "/tmp", "claude", ""); err != nil {
			t.Fatal(err)
		}
	}
	save("target", "t1", `{"attachments":[{"path":"up/own_1.png"},{"path":"up/shared.png"}]}`)
	save("target", "t2", `{"attachments":[{"path":"up/own_1.png"},{"path":"up/also-gone.png"},{"name":"no path"}]}`)
	save("target", "t3", `not json`)
	save("live", "l1", `{"attachments":[{"path":"up/shared.png"}]}`)
	// `_` must not act as a wildcard: own_1 is not shared by ownX1.
	save("live", "l2", `{"attachments":[{"path":"up/ownX1.png"}]}`)
	save("gone", "g1", `{"attachments":[{"path":"up/also-gone.png"}]}`)
	for _, id := range []string{"target", "gone"} {
		if err := s.DeleteConversation(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ExclusiveAttachmentPaths(ctx, "target")
	if err != nil {
		t.Fatalf("exclusive paths: %v", err)
	}
	sort.Strings(got)
	want := []string{"up/also-gone.png", "up/own_1.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}
