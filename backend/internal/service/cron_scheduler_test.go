package service

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func newCronTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "cron.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNextCronRunUsesRequestedTimezone(t *testing.T) {
	after := time.Date(2026, time.July, 24, 0, 30, 0, 0, time.UTC)
	next, err := NextCronRun("0 9 * * *", "Asia/Shanghai", after)
	if err != nil {
		t.Fatalf("next run: %v", err)
	}
	want := time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestLastMissedFireFindsSkippedWeekdayRun(t *testing.T) {
	// A weekday 07:00 Asia/Singapore job created Thursday midday whose
	// Friday fire (2026-07-23 23:00 UTC) fell into a restart window.
	baseline := time.Date(2026, time.July, 23, 4, 50, 0, 0, time.UTC)
	now := time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)
	missed, err := lastMissedFire("0 7 * * 1-5", "Asia/Singapore", baseline, now)
	if err != nil {
		t.Fatalf("last missed fire: %v", err)
	}
	want := time.Date(2026, time.July, 23, 23, 0, 0, 0, time.UTC)
	if !missed.Equal(want) {
		t.Fatalf("missed = %s, want %s", missed, want)
	}

	// No fire between baseline and now: Saturday-only window on a weekday job.
	missed, err = lastMissedFire("0 7 * * 1-5", "Asia/Singapore", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("last missed fire: %v", err)
	}
	if !missed.IsZero() {
		t.Fatalf("missed = %s, want zero", missed)
	}
}

func newMissedCronFixture(t *testing.T) (*CronScheduler, <-chan store.Message, context.Context) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cron.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{
		ID:       "owner-1",
		Name:     "Alice",
		Username: "alice",
	}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{
		ID:      "agent-1",
		OwnerID: "owner-1",
		Name:    "Researcher",
		WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// A daily job whose slot passed an hour ago: the fire was missed while
	// the scheduler was "down", and the next live fire is ~23h away, so the
	// running registry cannot interfere with the assertions below.
	fireAt := time.Now().UTC().Add(-time.Hour)
	if err := s.CreateCronJob(ctx, store.CronJob{
		ID:         "cron-1",
		OwnerID:    "owner-1",
		AgentID:    "agent-1",
		Expression: fmt.Sprintf("%d %d * * *", fireAt.Minute(), fireAt.Hour()),
		Timezone:   "UTC",
		Prompt:     "Catch up on the missed report",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create cron job: %v", err)
	}
	// Backdate creation so the missed slot postdates the job's existence,
	// then record a last run before the missed slot.
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.ExecContext(ctx,
		`UPDATE cron_jobs SET created_at = datetime('now','-3 hours') WHERE id = 'cron-1'`); err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}
	if err := s.RecordCronJobRun(ctx, "cron-1", time.Now().UTC().Add(-2*time.Hour), "", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}

	processed := make(chan store.Message, 4)
	dispatcher := NewDispatcher(s, func(_ context.Context, prompt store.Message) {
		processed <- prompt
	})
	if _, _, _, err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	cfg := &config.Config{Providers: []config.Provider{{
		Name:          "default",
		Type:          config.CLITypeClaude,
		MaxConcurrent: 1,
	}}}
	scheduler := NewCronScheduler(&Runtime{Store: s, Dispatcher: dispatcher, UserHub: NewUserHub(), Drainer: NewDrainer(), Cfg: cfg}, s)
	t.Cleanup(scheduler.Stop)
	return scheduler, processed, ctx
}

func TestReloadDoesNotCatchUpMissedRun(t *testing.T) {
	scheduler, processed, ctx := newMissedCronFixture(t)
	if err := scheduler.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}

	select {
	case prompt := <-processed:
		t.Fatalf("ordinary reload caught up a past fire: %+v", prompt)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestReloadAtStartupCatchesUpMissedRun(t *testing.T) {
	scheduler, processed, ctx := newMissedCronFixture(t)
	if err := scheduler.ReloadAtStartup(ctx); err != nil {
		t.Fatalf("startup reload: %v", err)
	}

	select {
	case prompt := <-processed:
		if prompt.Content != "Catch up on the missed report" {
			t.Fatalf("prompt content = %q", prompt.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missed run was not caught up at startup")
	}

	// A second startup reload must not double-fire the same missed slot.
	if err := scheduler.ReloadAtStartup(ctx); err != nil {
		t.Fatalf("second startup reload: %v", err)
	}
	select {
	case prompt := <-processed:
		t.Fatalf("catch-up ran twice for the same missed fire: %+v", prompt)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestCronSchedulerCreatesVisibleChatPrompt(t *testing.T) {
	s := newCronTestStore(t)
	ctx := context.Background()
	workDir := t.TempDir()
	if err := s.CreateUser(ctx, store.User{
		ID:       "owner-1",
		Name:     "Alice",
		Username: "alice",
	}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{
		ID:      "agent-1",
		OwnerID: "owner-1",
		Name:    "Researcher",
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateCronJob(ctx, store.CronJob{
		ID:         "cron-1",
		OwnerID:    "owner-1",
		AgentID:    "agent-1",
		Expression: "0 9 * * *",
		Timezone:   "UTC",
		Prompt:     "Summarize yesterday's changes",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create cron job: %v", err)
	}

	processed := make(chan store.Message, 1)
	dispatcher := NewDispatcher(s, func(_ context.Context, prompt store.Message) {
		processed <- prompt
	})
	if _, _, _, err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	cfg := &config.Config{Providers: []config.Provider{{
		Name:          "default",
		Type:          config.CLITypeClaude,
		MaxConcurrent: 1,
	}}}
	scheduler := NewCronScheduler(&Runtime{Store: s, Dispatcher: dispatcher, UserHub: NewUserHub(), Drainer: NewDrainer(), Cfg: cfg}, s)
	firedAt := time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC)
	scheduler.runJob("cron-1", firedAt)

	select {
	case prompt := <-processed:
		if prompt.Content != "Summarize yesterday's changes" {
			t.Fatalf("prompt content = %q", prompt.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled prompt was not dispatched")
	}

	conversations, err := s.ListConversations(ctx, "agent-1")
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversations = %d, want 1", len(conversations))
	}
	attributed, err := s.GetConversation(ctx, conversations[0].ID)
	if err != nil {
		t.Fatalf("get attributed conversation: %v", err)
	}
	if attributed.SourceType != "cron" || attributed.CronJobID != "cron-1" {
		t.Fatalf("scheduled conversation attribution = %q/%q", attributed.SourceType, attributed.CronJobID)
	}
	messages, err := s.ListMessages(ctx, conversations[0].ID, 20, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" ||
		messages[0].Content != "Summarize yesterday's changes" {
		t.Fatalf("scheduled task not visible in chat history: %+v", messages)
	}
}

func TestCronSchedulerConversationModelPrecedence(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "default", Type: config.CLITypeClaude,
	}}}
	withAccountModels(t, map[string]AccountModels{
		"default": {Models: []string{"claude-configured", "claude-agent"}},
	})
	scheduler := NewCronScheduler(&Runtime{Cfg: cfg}, nil)
	user := store.User{DefaultModel: "claude-agent"}

	seed, err := scheduler.conversationSeed(user, "claude-configured")
	if err != nil || seed.Provider != "claude" || seed.Model != "claude-configured" {
		t.Fatalf("configured task model = %s/%s, %v", seed.Provider, seed.Model, err)
	}

	seed, err = scheduler.conversationSeed(user, "")
	if err != nil || seed.Provider != "claude" || seed.Model != "claude-agent" {
		t.Fatalf("inherited Agent model = %s/%s, %v", seed.Provider, seed.Model, err)
	}

	// A task model that no longer maps to any provider falls back to the
	// Agent's profile rather than failing every fire.
	seed, err = scheduler.conversationSeed(user, "removed-model")
	if err != nil || seed.Model != "claude-agent" {
		t.Fatalf("stale task model = %s/%s, %v", seed.Provider, seed.Model, err)
	}
}

// TestCronSchedulerSkipsOverlappingFire covers the starvation bug's root
// cause: every fire used to mint its own conversation and claim its own
// dispatcher worker plus account-pool ticket, so a task that runs longer than
// its interval stacked up indefinitely in front of every human prompt.
func TestCronSchedulerSkipsOverlappingFire(t *testing.T) {
	s := newCronTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "owner-1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent-1", OwnerID: "owner-1", Name: "Researcher", WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateCronJob(ctx, store.CronJob{
		ID:         "cron-1",
		OwnerID:    "owner-1",
		AgentID:    "agent-1",
		Expression: "*/1 * * * *",
		Timezone:   "UTC",
		Prompt:     "Long running report",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("create cron job: %v", err)
	}

	// The callback stands in for a turn that outlives its own interval: it
	// parks until the test lets go, leaving the prompt row queued.
	unblock := make(chan struct{})
	started := make(chan struct{}, 4)
	dispatcher := NewDispatcher(s, func(_ context.Context, _ store.Message) {
		started <- struct{}{}
		<-unblock
	})
	if _, _, _, err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(unblock)
		}
	})

	cfg := &config.Config{Providers: []config.Provider{{
		Name:          "default",
		Type:          config.CLITypeClaude,
		MaxConcurrent: 1,
	}}}
	scheduler := NewCronScheduler(&Runtime{Store: s, Dispatcher: dispatcher, UserHub: NewUserHub(), Drainer: NewDrainer(), Cfg: cfg}, s)

	base := time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC)
	scheduler.runJob("cron-1", base)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first fire was never dispatched")
	}

	// Second fire while the first is still parked: must be skipped outright.
	scheduler.runJob("cron-1", base.Add(time.Minute))
	conversations, err := s.ListConversations(ctx, "agent-1")
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("overlapping fire created %d conversations, want 1", len(conversations))
	}
	job, err := s.GetCronJob(ctx, "cron-1")
	if err != nil {
		t.Fatalf("get cron job after overlap: %v", err)
	}
	if job.LastConversationID != conversations[0].ID {
		t.Errorf("last conversation after overlap = %q, want %q", job.LastConversationID, conversations[0].ID)
	}
	if !strings.Contains(job.LastError, errCronRunOverlaps.Error()) {
		t.Errorf("last error after overlap = %q, want overlap diagnostic", job.LastError)
	}

	// Once the first run drains, the next fire proceeds normally — the guard
	// must not retire the job permanently.
	close(unblock)
	closed = true
	waitForEmptyPromptQueue(t, s)
	scheduler.runJob("cron-1", base.Add(2*time.Minute))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fire after the previous run finished was skipped too")
	}
	conversations, err = s.ListConversations(ctx, "agent-1")
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 2 {
		t.Fatalf("conversations after the queue drained = %d, want 2", len(conversations))
	}
}

func waitForEmptyPromptQueue(t *testing.T, s *store.SQLiteStore) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := s.ListConversationsWithPending(context.Background())
		if err != nil {
			t.Fatalf("list pending: %v", err)
		}
		if len(pending) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prompt queue never drained")
}

// TestCronSchedulerTreatsDrainerJobAsStillRunning covers the second in-flight
// signal: once the runner has registered the turn, the prompt row may already
// be claimed, so only the Drainer still knows the run is alive.
func TestCronSchedulerTreatsDrainerJobAsStillRunning(t *testing.T) {
	drainer := NewDrainer()
	scheduler := NewCronScheduler(&Runtime{Drainer: drainer}, nil)
	scheduler.recordDispatch("cron-1", "conv-1")

	done := drainer.JobStartWithInfo(DrainerJob{ConversationID: "conv-1"})
	prev, running := scheduler.previousRunActive(context.Background(), "cron-1")
	if !running || prev != "conv-1" {
		t.Fatalf("previousRunActive = (%q, %v), want (\"conv-1\", true)", prev, running)
	}

	done()
	if _, running := scheduler.previousRunActive(context.Background(), "cron-1"); running {
		t.Fatal("finished run still reported as in flight")
	}
	// The bookkeeping entry is dropped once the run is done, so the map holds
	// at most one entry per live job.
	scheduler.mu.Lock()
	_, leaked := scheduler.lastRun["cron-1"]
	scheduler.mu.Unlock()
	if leaked {
		t.Fatal("lastRun entry leaked after the run finished")
	}
}

// newCronDeliveryFixture seeds an owner, one Agent, and a single job whose only
// interesting axes are the two delivery switches.
func newCronDeliveryFixture(t *testing.T, s *store.SQLiteStore, notificationsEnabled, deliverToBot bool) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "owner-1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{
		ID: "agent-1", OwnerID: "owner-1", Name: "Researcher", WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateCronJob(ctx, store.CronJob{
		ID:                   "cron-1",
		OwnerID:              "owner-1",
		AgentID:              "agent-1",
		Expression:           "0 9 * * *",
		Timezone:             "UTC",
		Prompt:               "Summarize yesterday's changes",
		Enabled:              true,
		NotificationsEnabled: notificationsEnabled,
		DeliverToBot:         deliverToBot,
	}); err != nil {
		t.Fatalf("create cron job: %v", err)
	}
}

func cronTestConfig() *config.Config {
	return &config.Config{Providers: []config.Provider{{
		Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
}

// TestCronSchedulerMutesNotificationsForBotDelivery guards the "one buzz per
// answer" rule. A run that relays into an IM thread must not also fire
// Bark/PushDeer, and the mute is expressed as conversation state rather than a
// branch inside MaybeNotify — so if the flag ever stops reaching
// UpdateConversationNotifications, the silencing quietly disappears with no
// other symptom than a duplicate push on the user's phone. The opted-out row
// is the other half: muting must not leak onto ordinary scheduled runs, whose
// push notification is their only delivery.
func TestCronSchedulerMutesNotificationsForBotDelivery(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		notificationsEnabled bool
		deliverToBot         bool
		wantNotificationsOff bool
	}{
		{name: "relayed to IM thread", notificationsEnabled: true, deliverToBot: true, wantNotificationsOff: true},
		{name: "push enabled", notificationsEnabled: true, deliverToBot: false, wantNotificationsOff: false},
		{name: "push disabled", notificationsEnabled: false, deliverToBot: false, wantNotificationsOff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCronTestStore(t)
			ctx := context.Background()
			newCronDeliveryFixture(t, s, tc.notificationsEnabled, tc.deliverToBot)

			processed := make(chan store.Message, 1)
			dispatcher := NewDispatcher(s, func(_ context.Context, prompt store.Message) {
				processed <- prompt
			})
			if _, _, _, err := dispatcher.Start(ctx); err != nil {
				t.Fatalf("start dispatcher: %v", err)
			}
			t.Cleanup(dispatcher.Stop)

			scheduler := NewCronScheduler(&Runtime{Store: s, Dispatcher: dispatcher, UserHub: NewUserHub(), Drainer: NewDrainer(), Cfg: cronTestConfig()}, s)
			scheduler.runJob("cron-1", time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC))

			select {
			case <-processed:
			case <-time.After(2 * time.Second):
				t.Fatal("scheduled prompt was not dispatched")
			}

			conversations, err := s.ListConversations(ctx, "agent-1")
			if err != nil {
				t.Fatalf("list conversations: %v", err)
			}
			if len(conversations) != 1 {
				t.Fatalf("conversations = %d, want 1", len(conversations))
			}
			if got := !conversations[0].NotificationsEnabled; got != tc.wantNotificationsOff {
				t.Fatalf("notifications muted = %v, want %v", got, tc.wantNotificationsOff)
			}
		})
	}
}

// enqueueProbeStore snapshots a cron job's recorded conversation at the instant
// Dispatcher.Enqueue persists the prompt — the last moment before a worker can
// pick it up. Wrapping the store is the only hook that sits *inside* Enqueue;
// reading the job after runJob returns would pass even with the write ordered
// after the enqueue, which is precisely the regression this guards.
type enqueueProbeStore struct {
	*store.SQLiteStore
	jobID string

	mu             sync.Mutex
	seenLastConvID string
	enqueuedConvID string
	probeErr       error
}

func (p *enqueueProbeStore) EnqueuePrompt(ctx context.Context, conversationID, content string) (store.Message, error) {
	job, err := p.GetCronJob(ctx, p.jobID)
	p.mu.Lock()
	p.enqueuedConvID = conversationID
	p.probeErr = err
	p.seenLastConvID = job.LastConversationID
	p.mu.Unlock()
	return p.SQLiteStore.EnqueuePrompt(ctx, conversationID, content)
}

// TestCronSchedulerRecordsLastConversationBeforeEnqueue pins the ordering the
// IM relay depends on: CronBotDeliveryTarget matches on
// cron_jobs.last_conversation_id, and the dispatcher can hand the prompt to a
// worker before runJob's goroutine is scheduled again. With recordRun after
// Enqueue the relay loses that race and the answer silently stays web-only —
// intermittently, which is why the assertion has to happen inside Enqueue.
func TestCronSchedulerRecordsLastConversationBeforeEnqueue(t *testing.T) {
	s := newCronTestStore(t)
	ctx := context.Background()
	newCronDeliveryFixture(t, s, true, true)

	probe := &enqueueProbeStore{SQLiteStore: s, jobID: "cron-1"}
	processed := make(chan store.Message, 1)
	dispatcher := NewDispatcher(probe, func(_ context.Context, prompt store.Message) {
		processed <- prompt
	})
	if _, _, _, err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	scheduler := NewCronScheduler(&Runtime{Store: s, Dispatcher: dispatcher, UserHub: NewUserHub(), Drainer: NewDrainer(), Cfg: cronTestConfig()}, s)
	scheduler.runJob("cron-1", time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC))

	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled prompt was not dispatched")
	}

	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.probeErr != nil {
		t.Fatalf("probe read cron job: %v", probe.probeErr)
	}
	if probe.enqueuedConvID == "" {
		t.Fatal("Enqueue was never called")
	}
	if probe.seenLastConvID != probe.enqueuedConvID {
		t.Fatalf("last_conversation_id at enqueue time = %q, want %q — the IM relay cannot resolve the run's delivery thread",
			probe.seenLastConvID, probe.enqueuedConvID)
	}
}

// A scheduled conversation seeds its model exactly like one created from the
// web: when it falls back to the Agent's default model it also takes the
// Agent's think level (the effort is part of that default-model profile), and
// when the task pins its own model it keeps the provider default.
func TestCronSchedulerSeedsThinkLevelLikeWebCreate(t *testing.T) {
	withAccountModels(t, map[string]AccountModels{
		"default": {Models: []string{"claude-configured", "claude-agent"}},
	})
	for _, tc := range []struct {
		name, jobModel, wantModel, wantThink string
	}{
		{name: "agent default model", jobModel: "", wantModel: "claude-agent", wantThink: "high"},
		{name: "task pins its model", jobModel: "claude-configured", wantModel: "claude-configured", wantThink: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCronTestStore(t)
			ctx := context.Background()
			newCronDeliveryFixture(t, s, true, false)
			agentUser, err := s.GetUser(ctx, "agent-1")
			if err != nil {
				t.Fatalf("get agent: %v", err)
			}
			agentUser.DefaultModel = "claude-agent"
			agentUser.ThinkLevel = "high"
			if err := s.UpdateUser(ctx, agentUser); err != nil {
				t.Fatalf("update agent: %v", err)
			}
			if tc.jobModel != "" {
				job, err := s.GetCronJob(ctx, "cron-1")
				if err != nil {
					t.Fatalf("get job: %v", err)
				}
				job.Model = tc.jobModel
				if err := s.UpdateCronJob(ctx, job); err != nil {
					t.Fatalf("update job: %v", err)
				}
			}

			processed := make(chan store.Message, 1)
			dispatcher := NewDispatcher(s, func(_ context.Context, prompt store.Message) {
				processed <- prompt
			})
			if _, _, _, err := dispatcher.Start(ctx); err != nil {
				t.Fatalf("start dispatcher: %v", err)
			}
			t.Cleanup(dispatcher.Stop)
			scheduler := NewCronScheduler(&Runtime{Store: s, Dispatcher: dispatcher, UserHub: NewUserHub(), Drainer: NewDrainer(), Cfg: cronTestConfig()}, s)
			scheduler.runJob("cron-1", time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC))
			select {
			case <-processed:
			case <-time.After(2 * time.Second):
				t.Fatal("scheduled prompt was not dispatched")
			}

			conversations, err := s.ListConversations(ctx, "agent-1")
			if err != nil || len(conversations) != 1 {
				t.Fatalf("conversations = %+v, %v; want one", conversations, err)
			}
			conv, err := s.GetConversation(ctx, conversations[0].ID)
			if err != nil {
				t.Fatalf("get conversation: %v", err)
			}
			if conv.Model != tc.wantModel || conv.ThinkLevel != tc.wantThink {
				t.Fatalf("conversation model/think = %q/%q, want %q/%q", conv.Model, conv.ThinkLevel, tc.wantModel, tc.wantThink)
			}
		})
	}
}
