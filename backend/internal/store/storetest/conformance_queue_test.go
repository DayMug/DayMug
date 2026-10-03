package storetest

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// The prompt queue is where the Fake re-implements the most SQL: claim,
// cancel, recovery and the soft-delete filter are all hand-written under
// QueueMu. Dispatcher and handler tests lean on those semantics, so these
// cases pin every one of them against SQLite.

func newQueueConversation(t *testing.T, s conformanceStore, id string) {
	t.Helper()
	if err := s.CreateConversation(context.Background(), id, "", "a1", "", "claude", ""); err != nil {
		t.Fatalf("create conversation %s: %v", id, err)
	}
}

func enqueue(t *testing.T, s conformanceStore, convID, content string) store.Message {
	t.Helper()
	m, err := s.EnqueuePrompt(context.Background(), convID, content)
	if err != nil {
		t.Fatalf("enqueue %q: %v", content, err)
	}
	return m
}

func listAll(t *testing.T, s conformanceStore, convID string) []store.Message {
	t.Helper()
	msgs, err := s.ListMessages(context.Background(), convID, 1000, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	return msgs
}

// A queued prompt is written when the user hits send, before the running
// turn's reply is saved. Claiming it must move it after that reply so history
// reads "reply → next prompt", not "next prompt → reply".
func TestPromptClaimReordersAfterEarlierReply(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")

			queued := enqueue(t, s, "c1", "next question")
			if err := s.SaveMessage(ctx, store.Message{ID: "reply", ConversationID: "c1", Role: "assistant", Content: "answer"}); err != nil {
				t.Fatalf("save reply: %v", err)
			}
			claimed, err := s.ClaimPendingPromptByID(ctx, queued.ID)
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if claimed.ID != queued.ID || claimed.ConversationID != "c1" || claimed.Role != "user" ||
				claimed.Content != "next question" || claimed.QueueStatus != "processing" {
				t.Errorf("claimed = %+v", claimed)
			}
			msgs := listAll(t, s, "c1")
			if got, want := messageIDs(msgs), []string{"reply", queued.ID}; !reflect.DeepEqual(got, want) {
				t.Errorf("history after claim = %v, want %v", got, want)
			}
			if msgs[1].QueueStatus != "processing" {
				t.Errorf("claimed row queue_status = %q, want processing", msgs[1].QueueStatus)
			}
		})
	}
}

// Only a pending row can be claimed; anything else — unknown, already running,
// finished, or pool-queued IM turns — is ErrNotFound and left untouched.
func TestPromptClaimRefusesNonPendingRows(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")

			running := enqueue(t, s, "c1", "running")
			if _, err := s.ClaimPendingPromptByID(ctx, running.ID); err != nil {
				t.Fatalf("first claim: %v", err)
			}
			done := enqueue(t, s, "c1", "done")
			if _, err := s.ClaimPendingPromptByID(ctx, done.ID); err != nil {
				t.Fatalf("claim done: %v", err)
			}
			if err := s.MarkPromptDone(ctx, done.ID); err != nil {
				t.Fatalf("mark done: %v", err)
			}
			if err := s.SaveMessage(ctx, store.Message{
				ID: "im", ConversationID: "c1", Role: "user", Content: "from IM", QueueStatus: store.QueueStatusPoolQueued,
			}); err != nil {
				t.Fatalf("save pool-queued: %v", err)
			}

			for _, id := range []string{"missing", running.ID, done.ID, "im"} {
				if _, err := s.ClaimPendingPromptByID(ctx, id); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("claim %s = %v, want ErrNotFound", id, err)
				}
			}
			status := map[string]string{}
			for _, m := range listAll(t, s, "c1") {
				status[m.ID] = m.QueueStatus
			}
			want := map[string]string{running.ID: "processing", done.ID: "", "im": store.QueueStatusPoolQueued}
			if !reflect.DeepEqual(status, want) {
				t.Errorf("queue statuses = %v, want %v", status, want)
			}
			if _, err := s.PeekNextPendingPrompt(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("peek with nothing pending = %v, want ErrNotFound", err)
			}
		})
	}
}

// Several dispatcher workers may race to claim the same row (e.g. a restart
// recovery pass overlapping a live enqueue); exactly one must win.
func TestPromptConcurrentClaimHasSingleWinner(t *testing.T) {
	impls := []implCase{
		{name: "fake", build: func(*testing.T) conformanceStore { return New() }},
		// On disk rather than :memory: — each pooled connection to :memory:
		// would open its own empty database, and this test needs several.
		{name: "sqlite", build: func(t *testing.T) conformanceStore {
			t.Helper()
			s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
			if err != nil {
				t.Fatalf("new sqlite store: %v", err)
			}
			if err := s.Init(); err != nil {
				t.Fatalf("init: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	}
	for _, impl := range impls {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")
			queued := enqueue(t, s, "c1", "once")

			const workers = 8
			var (
				wg       sync.WaitGroup
				mu       sync.Mutex
				wins     int
				otherErr []error
			)
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := s.ClaimPendingPromptByID(ctx, queued.ID)
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						wins++
					case !errors.Is(err, store.ErrNotFound):
						otherErr = append(otherErr, err)
					}
				}()
			}
			wg.Wait()
			if wins != 1 || len(otherErr) != 0 {
				t.Fatalf("wins = %d (want 1), unexpected errors = %v", wins, otherErr)
			}
			if n := len(listAll(t, s, "c1")); n != 1 {
				t.Errorf("history holds %d rows after racing claims, want 1", n)
			}
		})
	}
}

// Recalling a single prompt must not steal one that a worker already claimed:
// that row owns whatever has streamed against it.
func TestDeletePendingPromptSparesClaimedRow(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")

			running := enqueue(t, s, "c1", "running")
			if _, err := s.ClaimPendingPromptByID(ctx, running.ID); err != nil {
				t.Fatalf("claim: %v", err)
			}
			waiting := enqueue(t, s, "c1", "waiting")

			if _, err := s.DeletePendingPrompt(ctx, running.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("delete claimed = %v, want ErrNotFound", err)
			}
			got, err := s.DeletePendingPrompt(ctx, waiting.ID)
			if err != nil || got.ID != waiting.ID || got.Content != "waiting" || got.ConversationID != "c1" || got.QueueStatus != "pending" {
				t.Errorf("delete pending = %+v, %v", got, err)
			}
			if _, err := s.DeletePendingPrompt(ctx, waiting.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("delete twice = %v, want ErrNotFound", err)
			}
			if got := messageIDs(listAll(t, s, "c1")); !reflect.DeepEqual(got, []string{running.ID}) {
				t.Errorf("history = %v, want only the running prompt", got)
			}
		})
	}
}

// Cancel hands the pending prompts back to the editor oldest-first, and only
// for the conversation asked about.
func TestDeletePendingPromptsIsFIFOAndScoped(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")
			newQueueConversation(t, s, "c2")

			a := enqueue(t, s, "c1", "a")
			other := enqueue(t, s, "c2", "other")
			b := enqueue(t, s, "c1", "b")
			c := enqueue(t, s, "c1", "c")

			deleted, err := s.DeletePendingPrompts(ctx, "c1")
			if err != nil || !reflect.DeepEqual(messageIDs(deleted), []string{a.ID, b.ID, c.ID}) {
				t.Fatalf("deleted = %v, %v; want [a b c]", messageIDs(deleted), err)
			}
			for _, m := range deleted {
				if m.QueueStatus != "pending" || m.Role != "user" {
					t.Errorf("deleted row %+v not reported as a pending user prompt", m)
				}
			}
			if again, err := s.DeletePendingPrompts(ctx, "c1"); err != nil || len(again) != 0 {
				t.Errorf("second cancel = %v, %v; want nothing", messageIDs(again), err)
			}
			if peeked, err := s.PeekNextPendingPrompt(ctx, "c2"); err != nil || peeked.ID != other.ID {
				t.Errorf("c2 peek = %+v, %v; want its prompt untouched", peeked, err)
			}
		})
	}
}

// Startup recovery re-queues interrupted prompts, but gives up on one that
// keeps killing the server so it cannot restart-loop forever.
func TestResetProcessingToPendingAbandonsPoisonPrompt(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")
			poison := enqueue(t, s, "c1", "restart the service")

			for pass := 1; pass <= 2; pass++ {
				if _, err := s.ClaimPendingPromptByID(ctx, poison.ID); err != nil {
					t.Fatalf("pass %d claim: %v", pass, err)
				}
				recovered, abandoned, err := s.ResetProcessingToPending(ctx)
				if err != nil || recovered != 1 || abandoned != 0 {
					t.Fatalf("pass %d reset = (%d, %d, %v), want (1, 0)", pass, recovered, abandoned, err)
				}
				if peeked, err := s.PeekNextPendingPrompt(ctx, "c1"); err != nil || peeked.ID != poison.ID {
					t.Fatalf("pass %d peek = %+v, %v; want the recovered prompt", pass, peeked, err)
				}
			}
			if _, err := s.ClaimPendingPromptByID(ctx, poison.ID); err != nil {
				t.Fatalf("third claim: %v", err)
			}
			recovered, abandoned, err := s.ResetProcessingToPending(ctx)
			if err != nil || recovered != 0 || abandoned != 1 {
				t.Fatalf("third reset = (%d, %d, %v), want (0, 1)", recovered, abandoned, err)
			}
			if convs, err := s.ListConversationsWithPending(ctx); err != nil || len(convs) != 0 {
				t.Errorf("with pending after abandon = %v, %v; want none", convs, err)
			}
			if got := listAll(t, s, "c1"); len(got) != 1 || got[0].QueueStatus != "" {
				t.Errorf("abandoned prompt should stay in history with no queue status, got %+v", got)
			}

			// A pending (never claimed) row is not touched by recovery.
			fresh := enqueue(t, s, "c1", "fresh")
			if recovered, abandoned, err := s.ResetProcessingToPending(ctx); err != nil || recovered != 0 || abandoned != 0 {
				t.Errorf("reset with only pending rows = (%d, %d, %v), want (0, 0)", recovered, abandoned, err)
			}
			if peeked, err := s.PeekNextPendingPrompt(ctx, "c1"); err != nil || peeked.ID != fresh.ID {
				t.Errorf("peek = %+v, %v; want the fresh prompt", peeked, err)
			}
		})
	}
}

// Every non-empty queue status wakes a worker at boot, pool-queued included;
// ClearPoolQueuedPrompts is what removes those first.
func TestPendingConversationsAndPoolQueuedClear(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			for _, id := range []string{"pending", "processing", "pool", "idle"} {
				newQueueConversation(t, s, id)
			}
			enqueue(t, s, "pending", "p")
			if _, err := s.ClaimPendingPromptByID(ctx, enqueue(t, s, "processing", "r").ID); err != nil {
				t.Fatalf("claim: %v", err)
			}
			if err := s.SaveMessage(ctx, store.Message{
				ID: "im", ConversationID: "pool", Role: "user", Content: "q", QueueStatus: store.QueueStatusPoolQueued,
			}); err != nil {
				t.Fatalf("save pool-queued: %v", err)
			}
			if err := s.SaveMessage(ctx, store.Message{ID: "plain", ConversationID: "idle", Role: "user", Content: "x"}); err != nil {
				t.Fatalf("save plain: %v", err)
			}

			convs, err := s.ListConversationsWithPending(ctx)
			sort.Strings(convs)
			if err != nil || !reflect.DeepEqual(convs, []string{"pending", "pool", "processing"}) {
				t.Errorf("with pending = %v, %v", convs, err)
			}

			if n, err := s.ClearPoolQueuedPrompts(ctx); err != nil || n != 1 {
				t.Errorf("clear pool-queued = %d, %v; want 1", n, err)
			}
			if n, err := s.ClearPoolQueuedPrompts(ctx); err != nil || n != 0 {
				t.Errorf("second clear = %d, %v; want 0", n, err)
			}
			convs, err = s.ListConversationsWithPending(ctx)
			sort.Strings(convs)
			if err != nil || !reflect.DeepEqual(convs, []string{"pending", "processing"}) {
				t.Errorf("with pending after clear = %v, %v", convs, err)
			}
		})
	}
}

// A deleted conversation's leftover prompts must never wake the dispatcher.
func TestDeletedConversationPromptsStayAsleep(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")
			enqueue(t, s, "c1", "orphan")
			if err := s.DeleteConversation(ctx, "c1"); err != nil {
				t.Fatalf("delete conversation: %v", err)
			}
			if _, err := s.PeekNextPendingPrompt(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("peek deleted conversation = %v, want ErrNotFound", err)
			}
			if convs, err := s.ListConversationsWithPending(ctx); err != nil || len(convs) != 0 {
				t.Errorf("with pending = %v, %v; want none", convs, err)
			}
		})
	}
}

// ClaimErrFn is a Fake-only hook standing in for SQLITE_BUSY. The real claim
// runs in a transaction, so a failure leaves the row pending; the hook must
// model that, or callers' retry paths are tested against a state production
// never produces.
func TestFakeClaimErrFnLeavesRowPendingLikeRolledBackTx(t *testing.T) {
	f := New()
	ctx := context.Background()
	createHumanAndAgent(t, f)
	newQueueConversation(t, f, "c1")
	queued := enqueue(t, f, "c1", "retry me")

	busy := errors.New("database is locked")
	f.ClaimErrFn = func(string) error { return busy }
	if _, err := f.ClaimPendingPromptByID(ctx, queued.ID); !errors.Is(err, busy) {
		t.Fatalf("claim = %v, want the injected error", err)
	}
	if peeked, err := f.PeekNextPendingPrompt(ctx, "c1"); err != nil || peeked.ID != queued.ID {
		t.Fatalf("peek after failed claim = %+v, %v; want still pending", peeked, err)
	}
	f.ClaimErrFn = nil
	if claimed, err := f.ClaimPendingPromptByID(ctx, queued.ID); err != nil || claimed.QueueStatus != "processing" {
		t.Fatalf("retried claim = %+v, %v", claimed, err)
	}
}
