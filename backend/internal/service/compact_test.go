package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// compactBackend is a one-shot-only stand-in that records the context it was
// handed so a test can assert on the run's cancellation lifetime.
type compactBackend struct {
	summary     string
	runErr      error
	compaction  bool
	gotCtxErr   error
	gotDeadline bool
	calls       int
}

func (compactBackend) Name() string { return "compact-fake" }
func (b *compactBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: b.compaction}
}
func (*compactBackend) RunWithSession(context.Context, string, string, agent.RunRequest, chan<- agent.StreamEvent) error {
	return nil
}
func (b *compactBackend) RunOneshot(ctx context.Context, _, _ string, _ agent.RunRequest) (string, error) {
	b.calls++
	b.gotCtxErr = ctx.Err()
	_, b.gotDeadline = ctx.Deadline()
	return b.summary, b.runErr
}
func (*compactBackend) SessionExists(string, string, string) bool    { return true }
func (*compactBackend) SessionLogPath(string, string, string) string { return "" }

// newCompactRunner wires the minimum a Compact call needs: a store holding one
// conversation with a live session id plus its owning user, and a backend
// resolver that always returns backend.
func newCompactRunner(convID string, backend agent.Backend) (*PromptRunner, *storetest.Fake) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID:        convID,
		UserID:    "u1",
		Provider:  config.CLITypeClaude,
		SessionID: "sess-1",
		WorkDir:   "/tmp",
		Model:     "claude-sonnet-5",
	}}
	ms.Users = []store.User{{ID: "u1", Username: "", OwnerID: "human-1", WorkDir: "/tmp"}}
	runner := &PromptRunner{
		Store:    ms,
		Persist:  &MessagePersister{Store: ms},
		Backends: NewBackendRegistry(nil, backend),
	}
	return runner, ms
}

// TestCompactRunOutlivesCancelledRequestContext pins the invariant the handler
// relied on before the use case moved down here: the summary run gets its own
// background-rooted context, so a browser tab that goes away mid-compact does
// NOT abort the CLI. Threading the request ctx into RunOneshot would degrade
// silently into "user switches page → summary killed → session never rotated".
func TestCompactRunOutlivesCancelledRequestContext(t *testing.T) {
	const convID = "conv-ctx"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, ms := newCompactRunner(convID, backend)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel() // the client disconnected before the run started

	res, err := runner.Compact(reqCtx, convID, "u1")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if backend.calls != 1 {
		t.Fatalf("RunOneshot calls = %d, want 1", backend.calls)
	}
	if backend.gotCtxErr != nil {
		t.Fatalf("the summary run inherited the cancelled request ctx (err=%v); it must run on its own background ctx", backend.gotCtxErr)
	}
	if !backend.gotDeadline {
		t.Fatal("the summary run ctx carries no deadline; compactRunTimeout must still cap a stuck CLI")
	}
	if res.Summary != "digest" {
		t.Fatalf("summary = %q, want %q", res.Summary, "digest")
	}
	if res.NewClaudeSession != "sess-1-rotated" {
		t.Fatalf("new session = %q, want the rotated id", res.NewClaudeSession)
	}
	if got := ms.SnapshotMessages(convID); len(got) != 1 || !strings.HasPrefix(got[0].Content, CompactSummaryHeader) {
		t.Fatalf("summary was not persisted with its header: %+v", got)
	}
}

// ctxSensitiveCompactStore mimics database/sql, which refuses any statement
// carrying an already-cancelled context. storetest.Fake ignores ctx entirely,
// so without this wrapper no test can tell the request ctx from a
// background-rooted one on the write path.
type ctxSensitiveCompactStore struct {
	*storetest.Fake
}

func (s *ctxSensitiveCompactStore) SaveMessage(ctx context.Context, msg store.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Fake.SaveMessage(ctx, msg)
}

func (s *ctxSensitiveCompactStore) ResetConversationSession(ctx context.Context, id string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.Fake.ResetConversationSession(ctx, id)
}

// TestCompactPersistsSummaryAfterTheClientDisconnects is the other half of
// TestCompactRunOutlivesCancelledRequestContext. Detaching only RunOneshot is
// pointless if the writes that follow it ride the dead request ctx: the CLI
// burns five minutes on a summary, the insert fails with context.Canceled, the
// session is never rotated, and the 500 goes to a tab that has already gone
// away. The fake store here refuses cancelled contexts the way database/sql
// does, so the tail writes must be on their own background-rooted ctx to pass.
func TestCompactPersistsSummaryAfterTheClientDisconnects(t *testing.T) {
	const convID = "conv-tail-ctx"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, fake := newCompactRunner(convID, backend)
	sensitive := &ctxSensitiveCompactStore{Fake: fake}
	runner.Store = sensitive
	runner.Persist = &MessagePersister{Store: sensitive}

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel() // the client disconnected while the summary was being generated

	res, err := runner.Compact(reqCtx, convID, "u1")
	if err != nil {
		t.Fatalf("Compact failed on a cancelled request ctx: %v", err)
	}
	if got := fake.SnapshotMessages(convID); len(got) != 1 {
		t.Fatalf("summary messages = %d, want the digest persisted despite the disconnect", len(got))
	}
	if res.NewClaudeSession != "sess-1-rotated" {
		t.Fatalf("new session = %q, want the rotation to have happened", res.NewClaudeSession)
	}
	if fake.Conversations[0].SessionID == "sess-1" {
		t.Fatal("the session id was never rotated; the next turn would resume the compacted session")
	}
}

// TestCompactRejectsBackendWithoutCapability keeps the capability gate at the
// service boundary: adapters that can't do the "run one-shot, stash summary,
// rotate session" dance must refuse rather than corrupt the session id.
func TestCompactRejectsBackendWithoutCapability(t *testing.T) {
	const convID = "conv-nocap"
	backend := &compactBackend{summary: "digest", compaction: false}
	runner, ms := newCompactRunner(convID, backend)

	_, err := runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("err = %v, want *ServiceError", err)
	}
	if svcErr.Status != 400 {
		t.Fatalf("status = %d, want 400", svcErr.Status)
	}
	if backend.calls != 0 {
		t.Fatal("an incapable backend was still asked to summarise")
	}
	if ms.Conversations[0].SessionID != "sess-1" {
		t.Fatal("the session id rotated despite the refusal")
	}
}

// TestCompactRefusesCrossOwnerCaller covers the ownership check now that the
// caller id arrives as a plain string instead of being read off the gin ctx.
func TestCompactRefusesCrossOwnerCaller(t *testing.T) {
	const convID = "conv-owner"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, _ := newCompactRunner(convID, backend)

	_, err := runner.Compact(context.Background(), convID, "someone-else")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != 403 {
		t.Fatalf("err = %v, want 403 ServiceError", err)
	}
	if backend.calls != 0 {
		t.Fatal("a foreign caller still triggered a summary run")
	}
}

// TestCompactSurfacesEmptySummaryAsUpstreamFailure guards the "don't rotate on
// nothing" rule: an empty summary must not persist a bare header message nor
// burn the session id.
func TestCompactSurfacesEmptySummaryAsUpstreamFailure(t *testing.T) {
	const convID = "conv-empty"
	backend := &compactBackend{summary: "   \n", compaction: true}
	runner, ms := newCompactRunner(convID, backend)

	_, err := runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != 502 {
		t.Fatalf("err = %v, want 502 ServiceError", err)
	}
	if got := ms.SnapshotMessages(convID); len(got) != 0 {
		t.Fatalf("an empty summary was persisted anyway: %+v", got)
	}
	if ms.Conversations[0].SessionID != "sess-1" {
		t.Fatal("the session id rotated despite the empty summary")
	}
}

// TestCompactRefusesWhileJobInFlight mirrors /clear-context: rotating the
// session under a running job orphans the CLI process from the next resume.
func TestCompactRefusesWhileJobInFlight(t *testing.T) {
	const convID = "conv-busy"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, _ := newCompactRunner(convID, backend)
	runner.Broadcaster = NewBroadcaster()
	if !runner.Broadcaster.StartJob(convID, func() {}) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer runner.Broadcaster.EndJob(convID)

	_, err := runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != 409 {
		t.Fatalf("err = %v, want 409 ServiceError", err)
	}
	if backend.calls != 0 {
		t.Fatal("compact ran a summary while a job was in flight")
	}
}

// newPooledCompactRunner is newCompactRunner plus the account Pool production
// always has: the owning user is bound to the single "acc1" provider, so
// Compact resolves a real account and has to reserve a slot before spawning.
// maxConcurrent sizes that account's execution semaphore.
func newPooledCompactRunner(convID string, backend agent.Backend, maxConcurrent int) (*PromptRunner, *Pool) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID:        convID,
		UserID:    "u1",
		Provider:  config.CLITypeClaude,
		SessionID: "sess-1",
		WorkDir:   "/tmp",
		Model:     "claude-sonnet-5",
	}}
	ms.Users = []store.User{{
		ID: "u1", OwnerID: "human-1", WorkDir: "/tmp",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}}
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: maxConcurrent},
	}}
	pool := NewPool(cfg)
	runner := &PromptRunner{
		Store:    ms,
		Persist:  &MessagePersister{Store: ms},
		Pool:     pool,
		Cfg:      cfg,
		Backends: NewBackendRegistry(nil, backend),
	}
	return runner, pool
}

// TestCompactGivesUpWhenNoAccountSlot pins the gate: /compact runs a real CLI
// child, so a saturated account must make it refuse rather than spawn outside
// the per-account limit. The wait is bounded because Pool.Enter's queue has no
// length limit and Ticket.Wait no deadline — parking here forever would leave
// the conversation claimed and the user staring at a spinner.
func TestCompactGivesUpWhenNoAccountSlot(t *testing.T) {
	const convID = "conv-pool-full"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, pool := newPooledCompactRunner(convID, backend, 1)

	// Occupy the account's only slot, the way an in-flight chat turn would.
	held, err := pool.EnterForUser("", "acc1", "")
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if err := held.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	defer held.Release()

	prev := compactTicketWait
	compactTicketWait = 50 * time.Millisecond
	defer func() { compactTicketWait = prev }()

	_, err = runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want 503 ServiceError", err)
	}
	if backend.calls != 0 {
		t.Fatalf("compact spawned a CLI with no pool slot (RunOneshot calls = %d)", backend.calls)
	}
	// The abandoned ticket must leave the queue, otherwise every timed-out
	// compact would permanently inflate the account's reported backlog.
	if _, queued, _ := pool.Stats("acc1"); queued != 0 {
		t.Fatalf("queued = %d after giving up, want 0", queued)
	}
}

// TestCompactReleasesAccountSlot covers the other half of the gate: whatever
// the run does, the slot goes back. A leak here is worse than no gate at all —
// the account would silently lose capacity with every compact.
func TestCompactReleasesAccountSlot(t *testing.T) {
	tests := []struct {
		name   string
		runErr error
	}{
		{name: "successful summary"},
		{name: "failed run", runErr: errors.New("cli exploded")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			convID := "conv-release-" + tt.name
			backend := &compactBackend{summary: "digest", compaction: true, runErr: tt.runErr}
			runner, pool := newPooledCompactRunner(convID, backend, 1)

			_, err := runner.Compact(context.Background(), convID, "u1")
			if tt.runErr == nil && err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if tt.runErr != nil && err == nil {
				t.Fatal("Compact swallowed the run failure")
			}
			if backend.calls != 1 {
				t.Fatalf("RunOneshot calls = %d, want 1", backend.calls)
			}
			inUse, queued, ok := pool.Stats("acc1")
			if !ok {
				t.Fatal("account acc1 missing from the pool")
			}
			if inUse != 0 || queued != 0 {
				t.Fatalf("pool after compact: inUse=%d queued=%d, want 0/0 — the slot leaked", inUse, queued)
			}
		})
	}
}

// A throttled account refuses the compact with 429 and the cooldown's own
// wording, and the refusal hands the conversation back.
func TestCompactRefusesCoolingAccount(t *testing.T) {
	const convID = "conv-cooling"
	backend := &compactBackend{summary: "digest", compaction: true}
	runner, pool := newPooledCompactRunner(convID, backend, 1)
	runner.Broadcaster = NewBroadcaster()
	pool.Cooldown("acc1", "claude-sonnet-5", time.Now().Add(time.Hour))

	_, err := runner.Compact(context.Background(), convID, "u1")

	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want 429 ServiceError", err)
	}
	if !strings.Contains(svcErr.Message(), `account "acc1" model "claude-sonnet-5" is rate-limited`) {
		t.Errorf("message = %q, want the cooldown notice", svcErr.Message())
	}
	if backend.calls != 0 {
		t.Fatal("compact ran on a cooling account")
	}
	if runner.Broadcaster.IsBusy(convID) {
		t.Error("refused compact left the conversation busy")
	}
}
