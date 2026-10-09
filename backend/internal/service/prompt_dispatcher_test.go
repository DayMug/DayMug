package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// queueStore is a minimal in-memory store satisfying the slice of the
// store.Store interface that the dispatcher actually touches: the
// prompt-queue methods. Methods the dispatcher never calls return zero
// values / no-op; we panic loudly on anything unexpected so a future
// change to dispatcher dependencies doesn't silently slip through.
type queueStore struct {
	// Store is left nil: domains with no stub below (bots, cron, marketplace,
	// usage insights) panic with a nil dereference if the dispatcher ever
	// starts calling them — the same loud failure as the explicit stubs.
	store.Store

	mu       sync.Mutex
	messages []store.Message
	// claimDelay is injected per-call so a test can simulate a slow
	// claim; mirrors how a real DB might lag.
	claimDelay time.Duration
}

func newQueueStore() *queueStore { return &queueStore{} }

func (q *queueStore) EnqueuePrompt(_ context.Context, conversationID, content string) (store.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	m := store.Message{
		ID:             uuid.New().String(),
		ConversationID: conversationID,
		Role:           "user",
		Content:        content,
		CreatedAt:      time.Now(),
		QueueStatus:    "pending",
	}
	q.messages = append(q.messages, m)
	return m, nil
}

func (q *queueStore) PeekNextPendingPrompt(_ context.Context, conversationID string) (store.Message, error) {
	if q.claimDelay > 0 {
		time.Sleep(q.claimDelay)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, m := range q.messages {
		if m.ConversationID != conversationID || m.QueueStatus != "pending" {
			continue
		}
		return m, nil
	}
	return store.Message{}, store.ErrNotFound
}

func (q *queueStore) ClaimPendingPromptByID(_ context.Context, messageID string) (store.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, m := range q.messages {
		if m.ID != messageID {
			continue
		}
		if m.QueueStatus != "pending" {
			return store.Message{}, store.ErrNotFound
		}
		q.messages[i].QueueStatus = "processing"
		return q.messages[i], nil
	}
	return store.Message{}, store.ErrNotFound
}

func (q *queueStore) MarkPromptDone(_ context.Context, messageID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, m := range q.messages {
		if m.ID == messageID {
			q.messages[i].QueueStatus = ""
			return nil
		}
	}
	return nil
}

func (q *queueStore) ClearPoolQueuedPrompts(context.Context) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cleared := 0
	for i, m := range q.messages {
		if m.QueueStatus == store.QueueStatusPoolQueued {
			q.messages[i].QueueStatus = ""
			cleared++
		}
	}
	return cleared, nil
}

func (q *queueStore) DeletePendingPrompts(_ context.Context, conversationID string) ([]store.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var deleted []store.Message
	var kept []store.Message
	for _, m := range q.messages {
		if m.ConversationID == conversationID && m.QueueStatus == "pending" {
			deleted = append(deleted, m)
			continue
		}
		kept = append(kept, m)
	}
	q.messages = kept
	return deleted, nil
}

func (q *queueStore) DeletePendingPrompt(_ context.Context, messageID string) (store.Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, m := range q.messages {
		if m.ID != messageID {
			continue
		}
		if m.QueueStatus != "pending" {
			return store.Message{}, store.ErrNotFound
		}
		q.messages = append(q.messages[:i], q.messages[i+1:]...)
		return m, nil
	}
	return store.Message{}, store.ErrNotFound
}

func (q *queueStore) ResetProcessingToPending(_ context.Context) (recovered, abandoned int, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.messages {
		if q.messages[i].QueueStatus == "processing" {
			q.messages[i].QueueStatus = "pending"
			recovered++
		}
	}
	return recovered, abandoned, nil
}

func (q *queueStore) ListConversationsWithPending(_ context.Context) ([]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seen := map[string]struct{}{}
	var ids []string
	for _, m := range q.messages {
		if m.QueueStatus != "" {
			if _, ok := seen[m.ConversationID]; ok {
				continue
			}
			seen[m.ConversationID] = struct{}{}
			ids = append(ids, m.ConversationID)
		}
	}
	return ids, nil
}

// snapshot returns a copy of the current message list under lock so test
// assertions can inspect state without racing the worker.
func (q *queueStore) snapshot() []store.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]store.Message, len(q.messages))
	copy(out, q.messages)
	return out
}

// --- the rest of the Store interface — unused by the dispatcher, so
// they panic loudly if a future change starts depending on them.

func (q *queueStore) Init() error     { panic("unused") }
func (q *queueStore) Backend() string { return "test" }
func (q *queueStore) OptimizeDatabase(context.Context) (store.DBOptimizeResult, error) {
	panic("unused")
}
func (q *queueStore) DatabaseSize(context.Context) (int64, error) { panic("unused") }

func (q *queueStore) CreateUser(context.Context, store.User) error        { panic("unused") }
func (q *queueStore) GetUser(context.Context, string) (store.User, error) { panic("unused") }
func (q *queueStore) GetUserByUsername(context.Context, string) (store.User, error) {
	panic("unused")
}
func (q *queueStore) GetUserByEmail(context.Context, string) (store.User, error) {
	panic("unused")
}
func (q *queueStore) GetOwner(context.Context, store.User) (store.User, error) { panic("unused") }
func (q *queueStore) ListUsers(context.Context) ([]store.User, error)          { panic("unused") }
func (q *queueStore) UpdateUser(context.Context, store.User) error             { panic("unused") }
func (q *queueStore) SetUserPassword(context.Context, string, string) error    { panic("unused") }
func (q *queueStore) SetUserAdmin(context.Context, string, bool) error         { panic("unused") }
func (q *queueStore) SetUserDisabled(context.Context, string, bool) error      { panic("unused") }
func (q *queueStore) SetUserWorkDir(context.Context, string, string) error     { panic("unused") }
func (q *queueStore) GetUserProviderBindings(context.Context, string) (map[string]string, error) {
	panic("unused")
}
func (q *queueStore) GetUserProviderAccounts(context.Context, string) (map[string][]string, error) {
	panic("unused")
}
func (q *queueStore) SetUserProviderBinding(context.Context, string, string, string) error {
	panic("unused")
}
func (q *queueStore) SetUserProviderAccounts(context.Context, string, string, []string, string) error {
	panic("unused")
}
func (q *queueStore) DeleteUserProviderBinding(context.Context, string, string) error {
	panic("unused")
}
func (q *queueStore) SetUserEmail(context.Context, string, string) error        { panic("unused") }
func (q *queueStore) SetUserEnv(context.Context, string, string) error          { panic("unused") }
func (q *queueStore) SetUserDefaultModel(context.Context, string, string) error { panic("unused") }
func (q *queueStore) SetUserSandboxMode(context.Context, string, string) error  { panic("unused") }
func (q *queueStore) SetUserBarkURL(context.Context, string, string) error      { panic("unused") }
func (q *queueStore) SetUserPushDeerKey(context.Context, string, string) error  { panic("unused") }
func (q *queueStore) SetUserNotificationChannel(context.Context, string, string) error {
	panic("unused")
}
func (q *queueStore) DeleteUser(context.Context, string) error { panic("unused") }
func (q *queueStore) ReorderAgents(context.Context, string, []string) error {
	panic("unused")
}
func (q *queueStore) ArchiveUser(context.Context, string) error   { panic("unused") }
func (q *queueStore) UnarchiveUser(context.Context, string) error { panic("unused") }
func (q *queueStore) ListArchivedAgents(context.Context, string) ([]store.User, error) {
	panic("unused")
}

func (q *queueStore) CreateSession(context.Context, store.Session) error { panic("unused") }
func (q *queueStore) GetSession(context.Context, string) (store.Session, error) {
	panic("unused")
}
func (q *queueStore) DeleteSession(context.Context, string) error { panic("unused") }
func (q *queueStore) DeleteExpiredSessions(context.Context) error { panic("unused") }

func (q *queueStore) CreateConversation(context.Context, string, string, string, string, string, string) error {
	panic("unused")
}
func (q *queueStore) CreateConversationRecord(context.Context, store.NewConversation) error {
	panic("unused")
}
func (q *queueStore) UpdateConversationModel(context.Context, string, string, string) error {
	panic("unused")
}
func (q *queueStore) UpdateConversationAccount(context.Context, string, string) error {
	panic("unused")
}
func (q *queueStore) GetConversation(context.Context, string) (store.Conversation, error) {
	panic("unused")
}
func (q *queueStore) UpdateConversationTitle(context.Context, string, string) error { panic("unused") }
func (q *queueStore) UpdateConversationWorkDir(context.Context, string, string) error {
	panic("unused")
}
func (q *queueStore) UpdateConversationNotifications(context.Context, string, bool) error {
	panic("unused")
}
func (q *queueStore) UpdateConversationPinned(context.Context, string, bool) error {
	panic("unused")
}
func (q *queueStore) ReorderPinnedConversations(context.Context, string, []string) error {
	panic("unused")
}
func (q *queueStore) UpdateConversationContextUsage(context.Context, string, string) error {
	panic("unused")
}
func (q *queueStore) ResetConversationSession(context.Context, string) (string, error) {
	panic("unused")
}
func (q *queueStore) SetSessionID(context.Context, string, string) error {
	panic("unused")
}
func (q *queueStore) ListConversations(context.Context, string) ([]store.Conversation, error) {
	panic("unused")
}
func (q *queueStore) ListConversationsPage(context.Context, string, store.ConversationQuery) (store.ConversationPage, error) {
	panic("unused")
}
func (q *queueStore) DeleteConversation(context.Context, string) error       { panic("unused") }
func (q *queueStore) HasModelReply(context.Context, string) (bool, error)    { panic("unused") }
func (q *queueStore) CountUserMessages(context.Context, string) (int, error) { panic("unused") }

func (q *queueStore) SaveMessage(_ context.Context, message store.Message) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, message)
	return nil
}
func (q *queueStore) SaveImportedMessage(context.Context, store.Message, time.Time) (bool, error) {
	panic("unused")
}
func (q *queueStore) LatestMessageTime(context.Context, string) (time.Time, bool, error) {
	panic("unused")
}
func (q *queueStore) ListMessages(context.Context, string, int, int) ([]store.Message, error) {
	panic("unused")
}
func (q *queueStore) ListMessagesAfter(context.Context, string, string, int) ([]store.Message, error) {
	panic("unused")
}
func (q *queueStore) ListMessagesBefore(context.Context, string, string, int) ([]store.Message, error) {
	panic("unused")
}
func (q *queueStore) ClearMessages(context.Context, string) error { panic("unused") }
func (q *queueStore) GetSessionID(context.Context, string) (string, error) {
	panic("unused")
}
func (q *queueStore) AddTokenUsage(context.Context, store.TokenUsageDelta) error {
	return nil
}
func (q *queueStore) AggregateTokenUsage(context.Context, store.TokenUsageQuery) ([]store.TokenUsageRecord, error) {
	return nil, nil
}
func (q *queueStore) ListTokenUsageModels(context.Context) ([]string, error) {
	return nil, nil
}
func (q *queueStore) GetAppSetting(context.Context, string) (string, error) { return "", nil }
func (q *queueStore) SetAppSetting(context.Context, string, string) error   { return nil }
func (q *queueStore) GetBotThread(context.Context, string, string, string) (store.BotThread, error) {
	return store.BotThread{}, store.ErrNotFound
}
func (q *queueStore) UpsertBotThread(context.Context, store.BotThread) error { return nil }
func (q *queueStore) GetThreadCase(context.Context, string, string) (store.ThreadCase, error) {
	return store.ThreadCase{}, nil
}
func (q *queueStore) SaveThreadCase(context.Context, string, string, string, string) (store.ThreadCase, error) {
	return store.ThreadCase{}, nil
}
func (q *queueStore) ListThreadCaseHistory(context.Context, string, string, int) ([]store.ThreadCase, error) {
	return nil, nil
}
func (q *queueStore) LatestAssistantMessage(context.Context, string) (store.Message, error) {
	return store.Message{}, store.ErrNotFound
}
func (q *queueStore) Close() error { return nil }

// TestDispatcher_ProcessesPromptsFIFO is the happy path: enqueue three
// prompts on the same conversation, watch the processor see them in
// send order, watch every row end with queue_status cleared.
func TestDispatcher_ProcessesPromptsFIFO(t *testing.T) {
	q := newQueueStore()
	var processed []string
	var mu sync.Mutex
	done := make(chan struct{})
	var processedCount atomic.Int32

	processor := func(_ context.Context, msg store.Message) {
		mu.Lock()
		processed = append(processed, msg.Content)
		mu.Unlock()
		if processedCount.Add(1) == 3 {
			close(done)
		}
	}
	d := service.NewDispatcher(q, processor)

	for _, p := range []string{"first", "second", "third"} {
		if _, err := d.Enqueue(context.Background(), "conv-1", p); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher never finished processing")
	}

	mu.Lock()
	got := append([]string(nil), processed...)
	mu.Unlock()
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FIFO broken at %d: got %q want %q", i, got[i], want[i])
		}
	}

	for _, m := range q.snapshot() {
		if m.QueueStatus != "" {
			t.Errorf("expected queue_status cleared after processing, got %q on %q",
				m.QueueStatus, m.Content)
		}
	}

	d.Stop()
}

// TestDispatcher_RecoversProcessingOnStart verifies the crash-recovery
// path: rows left at 'processing' from a previous run are demoted to
// 'pending' and resumed on Start. Without this, a crash mid-stream
// would orphan the row and the user would never see their prompt
// finish.
func TestDispatcher_RecoversProcessingOnStart(t *testing.T) {
	q := newQueueStore()
	// Simulate a row that was actively being processed when the
	// previous instance crashed.
	q.messages = []store.Message{
		{ID: "m-stuck", ConversationID: "conv-r", Role: "user", Content: "leftover", QueueStatus: "processing"},
	}

	processed := make(chan store.Message, 1)
	processor := func(_ context.Context, m store.Message) {
		processed <- m
	}
	d := service.NewDispatcher(q, processor)

	resetRows, _, resumed, err := d.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if resetRows != 1 {
		t.Errorf("expected 1 row reset, got %d", resetRows)
	}
	if resumed != 1 {
		t.Errorf("expected 1 conversation resumed, got %d", resumed)
	}

	select {
	case m := <-processed:
		if m.Content != "leftover" {
			t.Errorf("recovered prompt content mismatch: %q", m.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered prompt never reached the processor")
	}

	d.Stop()
}

// TestDispatcher_CancelPendingReturnsRows ensures the cancel path
// returns deleted prompts in send order so the WS handler can ship the
// content back to the client editor. Mirrors production's processor
// contract: the processor claims the row (transitions it to
// 'processing') before doing anything else, so cancel-pending only
// affects siblings still in the queue.
func TestDispatcher_CancelPendingReturnsRows(t *testing.T) {
	q := newQueueStore()
	// Block the worker so we can stack pending rows behind a slow
	// in-flight one.
	startProcess := make(chan struct{})
	releaseProcess := make(chan struct{})
	processor := func(_ context.Context, m store.Message) {
		// Production processor claims after acquiring a pool slot; the
		// mock claims immediately so cancel-pending sees this row as
		// 'processing' and leaves it alone.
		if _, err := q.ClaimPendingPromptByID(context.Background(), m.ID); err != nil {
			t.Errorf("test processor claim: %v", err)
		}
		startProcess <- struct{}{}
		<-releaseProcess
	}
	d := service.NewDispatcher(q, processor)

	if _, err := d.Enqueue(context.Background(), "conv-c", "first"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-startProcess // first prompt has been claimed and is in the processor

	if _, err := d.Enqueue(context.Background(), "conv-c", "second"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := d.Enqueue(context.Background(), "conv-c", "third"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deleted, err := d.CancelPending(context.Background(), "conv-c")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deleted prompts, got %d", len(deleted))
	}
	if deleted[0].Content != "second" || deleted[1].Content != "third" {
		t.Errorf("FIFO broken in cancel return: %+v", deleted)
	}

	close(releaseProcess)
	d.Stop()
}

// TestDispatcher_StopExitsCleanly is a smoke test for the shutdown
// path: Stop should return promptly even with an idle worker hanging
// on the wait-for-work select.
func TestDispatcher_StopExitsCleanly(t *testing.T) {
	q := newQueueStore()
	d := service.NewDispatcher(q, func(context.Context, store.Message) {})
	// Enqueue → process → idle wait.
	if _, err := d.Enqueue(context.Background(), "conv-x", "p"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Give the worker a beat to drain and enter the idle wait.
	time.Sleep(50 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		d.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return promptly")
	}
}

// TestDispatcher_EnqueueErrorBubblesUp ensures a store-level failure
// propagates back to the caller (the WS handler uses this to surface
// "failed to enqueue prompt" to the client).
func TestDispatcher_EnqueueErrorBubblesUp(t *testing.T) {
	q := &erroringStore{}
	d := service.NewDispatcher(q, func(context.Context, store.Message) {})
	_, err := d.Enqueue(context.Background(), "conv", "x")
	if !errors.Is(err, errInjected) {
		t.Fatalf("expected errInjected, got %v", err)
	}
	d.Stop()
}

// TestDispatcher_HandsPendingRowToProcessor verifies the new peek-then-
// processor contract: the processor receives a row that is still
// 'pending' (NOT yet 'processing'), so the staging-area UI keeps
// rendering it until the processor itself claims the row after
// acquiring a pool slot. Prior to this change the dispatcher flipped
// the row to 'processing' and broadcast prompt_started up-front, which
// caused queued prompts on a 1-slot pool to leave the staging area
// before they were really running.
func TestDispatcher_HandsPendingRowToProcessor(t *testing.T) {
	q := newQueueStore()
	got := make(chan store.Message, 1)
	processor := func(_ context.Context, m store.Message) { got <- m }
	d := service.NewDispatcher(q, processor)

	if _, err := d.Enqueue(context.Background(), "conv-h", "alpha"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case m := <-got:
		if m.QueueStatus != "pending" {
			t.Errorf("expected processor to receive a still-pending row, got queue_status=%q", m.QueueStatus)
		}
		if m.Content != "alpha" {
			t.Errorf("content mismatch: %q", m.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processor never received the prompt")
	}

	d.Stop()
}

// TestDispatcher_SavePromptDoesNotKick ensures SavePrompt persists the
// row but leaves the worker idle until KickWorker is called explicitly.
// This is the contract the WS input handler relies on to broadcast its
// peer-tab user_message echo BEFORE the dispatcher's worker can win the
// race and broadcast prompt_started — see comments in
// terminal_ws.go:input handler.
func TestDispatcher_SavePromptDoesNotKick(t *testing.T) {
	q := newQueueStore()
	processed := make(chan struct{}, 1)
	processor := func(_ context.Context, _ store.Message) {
		processed <- struct{}{}
	}
	d := service.NewDispatcher(q, processor)

	saved, err := d.SavePrompt(context.Background(), "conv-save", "queued")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatalf("save returned empty id")
	}

	// Without KickWorker, the worker should never wake. Wait long enough
	// to be confident the goroutine isn't just slow.
	select {
	case <-processed:
		t.Fatal("processor ran without KickWorker — contract broken")
	case <-time.After(150 * time.Millisecond):
	}

	d.KickWorker("conv-save")

	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("processor never ran after KickWorker")
	}

	d.Stop()
}

func TestDispatcher_SavePromptWithMetadataPersistsPendingUserRow(t *testing.T) {
	q := newQueueStore()
	d := service.NewDispatcher(q, func(context.Context, store.Message) {})
	metadata := json.RawMessage(`{"attachments":[{"name":"shot.png"}]}`)

	saved, err := d.SavePromptWithMetadata(context.Background(), "conv-image", "inspect", metadata)
	if err != nil {
		t.Fatalf("save with metadata: %v", err)
	}
	if saved.ID == "" || saved.Role != "user" || saved.QueueStatus != "pending" {
		t.Fatalf("unexpected saved prompt: %+v", saved)
	}
	if string(saved.Metadata) != string(metadata) {
		t.Fatalf("metadata = %s, want %s", saved.Metadata, metadata)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.messages) != 1 || string(q.messages[0].Metadata) != string(metadata) {
		t.Fatalf("persisted messages = %+v", q.messages)
	}
}

// TestDispatcher_FIFOAcrossSiblings verifies per-conversation ordering
// is preserved even when the processor doesn't claim the row (the
// dispatcher's MarkPromptDone sweep clears 'pending' rows that the
// processor returned without claiming, so the worker advances).
func TestDispatcher_FIFOAcrossSiblings(t *testing.T) {
	q := newQueueStore()
	var got []string
	var mu sync.Mutex
	done := make(chan struct{})
	var seen atomic.Int32
	processor := func(_ context.Context, m store.Message) {
		mu.Lock()
		got = append(got, m.Content)
		mu.Unlock()
		if seen.Add(1) == 3 {
			close(done)
		}
	}
	d := service.NewDispatcher(q, processor)

	for _, p := range []string{"a", "b", "c"} {
		if _, err := d.Enqueue(context.Background(), "conv-fifo", p); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher never processed all three")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("FIFO broken at %d: got %q want %q", i, got[i], want[i])
		}
	}
	d.Stop()
}

// TestDispatcher_CancelOnePending drops a single queued prompt while
// leaving siblings intact. Powers the per-message recall affordance in
// the staging area; sibling prompts must keep running serially after.
func TestDispatcher_CancelOnePending(t *testing.T) {
	q := newQueueStore()
	// Use a slow claim so the second prompt is still 'pending' when we
	// recall it: the worker is parked inside the first claim.
	q.claimDelay = 200 * time.Millisecond
	processed := make(chan string, 4)
	processor := func(_ context.Context, m store.Message) { processed <- m.Content }
	d := service.NewDispatcher(q, processor)

	a, err := d.Enqueue(context.Background(), "conv-c", "first")
	if err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	b, err := d.Enqueue(context.Background(), "conv-c", "second")
	if err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	_ = a

	// Recall b before the worker can claim it. claimDelay parks the
	// worker on its FIRST claim attempt for 200ms — plenty of time.
	dropped, err := d.CancelOnePending(context.Background(), b.ID)
	if err != nil {
		t.Fatalf("cancel one: %v", err)
	}
	if dropped.ID != b.ID || dropped.Content != "second" {
		t.Errorf("returned wrong row: %+v", dropped)
	}

	select {
	case got := <-processed:
		if got != "first" {
			t.Errorf("expected only 'first' to run, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first prompt never processed")
	}
	// 'second' must NOT show up — the recall removed it.
	select {
	case extra := <-processed:
		t.Errorf("unexpected second processing: %q", extra)
	case <-time.After(200 * time.Millisecond):
	}

	d.Stop()
}

// TestDispatcher_CancelOnePendingFiresPreClaim verifies that CancelOne
// runs the registered pre-claim cancel for the targeted prompt id. This
// is what unparks a processor that's blocked on the per-account pool
// slot when the user recalls a queued prompt — without it, the dead
// pool ticket would linger and sibling waiters' position broadcasts
// would lie.
func TestDispatcher_CancelOnePendingFiresPreClaim(t *testing.T) {
	q := newQueueStore()
	d := service.NewDispatcher(q, func(context.Context, store.Message) {})

	msg, err := d.Enqueue(context.Background(), "conv-cancel", "queued")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.RegisterPreClaim(msg.ID, cancel)

	if _, err := d.CancelOnePending(context.Background(), msg.ID); err != nil {
		t.Fatalf("cancel one: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("pre-claim cancel was not fired by CancelOnePending")
	}

	// Re-firing should be safe (the registration is consumed on first
	// fire so the processor's defer Unregister is a no-op).
	d.UnregisterPreClaim(msg.ID)
	d.Stop()
}

// TestDispatcher_PreClaimUnregisterStopsCancel verifies that a
// processor that successfully claims its row (and therefore unregisters
// its pre-claim cancel) is no longer reachable via CancelOnePending's
// fire path. Otherwise a stale registration could fire after the run
// has begun, racing the in-flight claude job with a context cancel.
func TestDispatcher_PreClaimUnregisterStopsCancel(t *testing.T) {
	q := newQueueStore()
	d := service.NewDispatcher(q, func(context.Context, store.Message) {})

	msg, err := d.Enqueue(context.Background(), "conv-unregister", "running")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.RegisterPreClaim(msg.ID, cancel)
	d.UnregisterPreClaim(msg.ID)

	// The row is still 'pending' in the mock store (the test processor
	// is a no-op and the dispatcher's MarkPromptDone sweep races us);
	// we explicitly delete to simulate the cancel hitting after
	// unregister.
	if _, err := d.CancelOnePending(context.Background(), msg.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		// ErrNotFound is expected if MarkPromptDone got there first; any
		// other error is the test failure we care about.
		t.Fatalf("cancel one: %v", err)
	}

	select {
	case <-ctx.Done():
		t.Fatal("pre-claim cancel fired after Unregister — registration leaked")
	case <-time.After(100 * time.Millisecond):
	}

	cancel() // cleanup
	d.Stop()
}

var errInjected = errors.New("injected")

// accountPinStore records the account LockConversationAccount persists and
// serves a conversation whose current pin the test controls.
type accountPinStore struct {
	queueStore
	conv     store.Conversation
	pinnedTo string
	pinCalls int
}

func (s *accountPinStore) GetConversation(context.Context, string) (store.Conversation, error) {
	return s.conv, nil
}
func (s *accountPinStore) UpdateConversationAccount(_ context.Context, _, account string) error {
	s.pinCalls++
	s.pinnedTo = account
	return nil
}

// A first run with no pin persists the resolved account as the conversation's
// permanent binding.
func TestLockConversationAccount_PinsOnFirstRun(t *testing.T) {
	s := &accountPinStore{conv: store.Conversation{ID: "c1", AccountName: ""}}
	r := &service.PromptRunner{Store: s}
	r.LockConversationAccount(context.Background(), "c1", "account-a")
	if s.pinCalls != 1 || s.pinnedTo != "account-a" {
		t.Fatalf("expected pin to account-a once, got calls=%d pin=%q", s.pinCalls, s.pinnedTo)
	}
}

// An existing pin is never overwritten — the account a conversation first ran
// on stays fixed for its lifetime.
func TestLockConversationAccount_KeepsExistingPin(t *testing.T) {
	s := &accountPinStore{conv: store.Conversation{ID: "c1", AccountName: "account-a"}}
	r := &service.PromptRunner{Store: s}
	r.LockConversationAccount(context.Background(), "c1", "account-b")
	if s.pinCalls != 0 {
		t.Fatalf("expected no re-pin, got %d calls (pin=%q)", s.pinCalls, s.pinnedTo)
	}
}

type erroringStore struct{ queueStore }

func (e *erroringStore) EnqueuePrompt(context.Context, string, string) (store.Message, error) {
	return store.Message{}, errInjected
}
func (e *erroringStore) ResetProcessingToPending(context.Context) (int, int, error) { return 0, 0, nil }
func (e *erroringStore) ListConversationsWithPending(context.Context) ([]string, error) {
	return nil, nil
}
