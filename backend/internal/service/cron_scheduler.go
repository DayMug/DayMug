package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/DayMug/DayMug/backend/internal/store"
)

const cronConversationTitleRunes = 42

var errCronRunOverlaps = errors.New("previous cron run is still active")

const (
	// A missed fire older than this is dropped rather than caught up, so a
	// long-stopped server does not replay stale scheduled prompts.
	cronCatchUpLookback = 72 * time.Hour
	cronCatchUpMaxSteps = 10000
)

// CronReloader is the small lifecycle surface used by the HTTP handler after a
// task is created, edited, enabled, disabled, or deleted.
type CronReloader interface {
	Reload(ctx context.Context) error
}

// CronScheduler turns enabled CronJob rows into ordinary Dispatcher prompts.
// It deliberately does not invoke an agent backend itself: Dispatcher and
// PromptRunner remain the single execution path for concurrency limits,
// provider/account resolution, environment layering, sandboxing, and chat
// message persistence.
type CronScheduler struct {
	// Runtime supplies the store, dispatcher, user hub, drainer and config.
	// Fires enqueue through its Dispatcher, i.e. the same Runtime.ProcessPrompt
	// path web chat uses, so provider bindings, account concurrency, sandboxing
	// and persistence are identical for scheduled and interactive runs.
	*Runtime
	Jobs store.CronStore

	mu       sync.Mutex
	cron     *cron.Cron
	caughtUp map[string]time.Time
	// dispatching guards runJob itself against re-entry for the same job.
	// It only covers "create the conversation and enqueue the prompt", which
	// is exactly the window in which the previous fire is not yet visible to
	// either of the in-flight signals lastRun consults below.
	dispatching map[string]struct{}
	// lastRun maps a job id to the conversation its most recent fire
	// dispatched. runJob returns long before the agent finishes, so this — not
	// a mutex held across runJob — is what answers "is the previous run of
	// this job still going?". Pruned in Reload against the enabled job set so
	// deleted or disabled jobs don't accumulate entries forever.
	lastRun map[string]string
}

func NewCronScheduler(rt *Runtime, jobs store.CronStore) *CronScheduler {
	return &CronScheduler{Runtime: rt, Jobs: jobs}
}

// NextCronRun validates a five-field POSIX cron expression and IANA timezone,
// returning the next wall-clock match as UTC.
func NextCronRun(expression, timezone string, after time.Time) (time.Time, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return time.Time{}, errors.New("cron expression is required")
	}
	schedule, err := cron.ParseStandard(expression)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone: %w", err)
	}
	return schedule.Next(after.In(location)).UTC(), nil
}

// lastMissedFire reports the most recent scheduled fire time inside
// (baseline, now], clamped to cronCatchUpLookback. Zero time when the
// schedule has not fired in that window.
func lastMissedFire(expression, timezone string, baseline, now time.Time) (time.Time, error) {
	schedule, err := cron.ParseStandard(strings.TrimSpace(expression))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	if timezone == "" {
		timezone = "UTC"
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone: %w", err)
	}
	if lower := now.Add(-cronCatchUpLookback); baseline.Before(lower) {
		baseline = lower
	}
	var missed time.Time
	next := baseline.In(location)
	for i := 0; i < cronCatchUpMaxSteps; i++ {
		next = schedule.Next(next)
		if next.IsZero() || next.After(now) {
			break
		}
		missed = next
	}
	if missed.IsZero() {
		return time.Time{}, nil
	}
	return missed.UTC(), nil
}

// Reload atomically replaces the in-memory cron registry from durable rows.
// It only schedules future fires because callers use it after task mutations;
// replaying with a newly edited expression would treat pre-edit slots as misses.
func (s *CronScheduler) Reload(ctx context.Context) error {
	return s.reload(ctx, false)
}

// ReloadAtStartup restores the registry and recovers the most recent fire lost
// while the process was stopped. Keep catch-up startup-only so an API edit does
// not reinterpret old history using the task's new expression.
func (s *CronScheduler) ReloadAtStartup(ctx context.Context) error {
	return s.reload(ctx, true)
}

// Running callbacks are short (they only create and enqueue a prompt), so
// waiting for the old registry to stop cannot wait on a long-lived Agent run.
func (s *CronScheduler) reload(ctx context.Context, recoverMissed bool) error {
	if s == nil || s.Jobs == nil {
		return nil
	}
	jobs, err := s.Jobs.ListEnabledCronJobs(ctx)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	type catchUp struct {
		jobID string
		fire  time.Time
	}
	var missedJobs []catchUp
	registered := make(map[string]struct{}, len(jobs))
	next := cron.New()
	for _, job := range jobs {
		if _, err := NextCronRun(job.Expression, job.Timezone, time.Now()); err != nil {
			log.Printf("crontab: disabling invalid job %s: %v", job.ID, err)
			// Not on ctx on purpose: this is a self-healing repair write, and
			// Reload also runs from an HTTP handler. If the caller's ctx dies
			// mid-loop the unschedulable row would stay enabled and be
			// rediscovered — and re-logged — on every subsequent reload.
			_ = s.Jobs.DisableCronJob(context.Background(), job.ID, "invalid_schedule")
			continue
		}
		jobID := job.ID
		spec := "CRON_TZ=" + job.Timezone + " " + job.Expression
		if _, err := next.AddFunc(spec, func() {
			s.runJob(jobID, time.Now().UTC())
		}); err != nil {
			log.Printf("crontab: register job %s: %v", job.ID, err)
			// Detached for the same reason as the branch above.
			_ = s.Jobs.DisableCronJob(context.Background(), job.ID, "invalid_schedule")
			continue
		}
		registered[jobID] = struct{}{}
		if recoverMissed {
			// The in-memory registry loses fires across restarts and drain
			// windows; recover the most recent one so a report scheduled for a
			// restart window still runs once.
			baseline := job.CreatedAt
			if job.LastRunAt != nil && job.LastRunAt.After(baseline) {
				baseline = *job.LastRunAt
			}
			if missed, err := lastMissedFire(job.Expression, job.Timezone, baseline, now); err == nil && !missed.IsZero() {
				missedJobs = append(missedJobs, catchUp{jobID: jobID, fire: missed})
			}
		}
	}

	s.mu.Lock()
	if s.cron != nil {
		stopped := s.cron.Stop()
		select {
		case <-stopped.Done():
		case <-ctx.Done():
			s.mu.Unlock()
			return ctx.Err()
		}
	}
	s.cron = next
	next.Start()
	if s.caughtUp == nil {
		s.caughtUp = make(map[string]time.Time)
	}
	// Drop overlap state for jobs that just left the registry (deleted,
	// disabled, or auto-disabled above). They cannot fire again, so keeping
	// their entry would only leak memory — and a job that comes back later
	// deserves a clean slate rather than a stale "still running" verdict.
	for id := range s.lastRun {
		if _, ok := registered[id]; !ok {
			delete(s.lastRun, id)
		}
	}
	var toRun []catchUp
	for _, m := range missedJobs {
		if prev, ok := s.caughtUp[m.jobID]; ok && !prev.Before(m.fire) {
			continue
		}
		s.caughtUp[m.jobID] = m.fire
		toRun = append(toRun, m)
	}
	s.mu.Unlock()
	for _, m := range toRun {
		log.Printf("crontab: catch-up run for job %s missed at %s", m.jobID, m.fire.Format(time.RFC3339))
		go s.runJob(m.jobID, m.fire)
	}
	return nil
}

func (s *CronScheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	current := s.cron
	s.cron = nil
	s.mu.Unlock()
	if current != nil {
		<-current.Stop().Done()
	}
}

func (s *CronScheduler) runJob(jobID string, firedAt time.Time) {
	if s == nil || s.Runtime == nil || s.Store == nil || s.Jobs == nil || s.Dispatcher == nil {
		return
	}
	if s.Drainer != nil && s.Drainer.IsDraining() {
		return
	}

	// Serialise the dispatch itself. Released by defer so a panic anywhere
	// below cannot wedge this job forever.
	endDispatch, free := s.beginDispatch(jobID)
	if !free {
		log.Printf("crontab: skipping job %s fired at %s — a previous fire is still being dispatched",
			jobID, firedAt.Format(time.RFC3339))
		return
	}
	defer endDispatch()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Skip rather than stack. Each fire used to mint its own conversation and
	// grab its own dispatcher worker plus account-pool ticket, so a task that
	// runs longer than its interval piled up indefinitely in front of every
	// human prompt.
	if prev, running := s.previousRunActive(ctx, jobID); running {
		log.Printf("crontab: skipping job %s fired at %s — previous run (conversation %s) has not finished",
			jobID, firedAt.Format(time.RFC3339), prev)
		s.recordRun(ctx, jobID, firedAt, prev, fmt.Errorf("%w: conversation %s", errCronRunOverlaps, prev))
		return
	}

	job, err := s.Jobs.GetCronJob(ctx, jobID)
	if err != nil || !job.Enabled {
		return
	}
	agentUser, err := s.Store.GetUser(ctx, job.AgentID)
	if err != nil ||
		agentUser.Username != "" ||
		agentUser.OwnerID != job.OwnerID ||
		agentUser.Archived {
		_ = s.Jobs.DisableCronJob(ctx, job.ID, "agent_unavailable")
		return
	}

	seed, err := s.conversationSeed(agentUser, job.Model)
	if err != nil {
		s.recordRun(ctx, job.ID, firedAt, "", err)
		return
	}

	conversationID := uuid.New().String()
	title := scheduledConversationTitle(job.Prompt)
	// Respect the task's push preference. A run relayed into the Agent's IM
	// thread is also muted because the thread reply is already the delivery and
	// a second buzz for the same sentence is noise. Expressing both cases as
	// conversation state keeps the reason visible in the UI bell.
	row := store.NewConversation{
		ID: conversationID, Title: title, UserID: agentUser.ID, WorkDir: agentUser.WorkDir,
		SourceType: "cron", CronJobID: job.ID,
		MuteNotifications: !job.NotificationsEnabled || job.DeliverToBot,
	}
	if err := CreateSeededConversation(ctx, s.Store, row, seed); err != nil {
		s.recordRun(ctx, job.ID, firedAt, "", fmt.Errorf("create conversation: %w", err))
		return
	}

	conversation, err := s.Store.GetConversation(ctx, conversationID)
	if err != nil {
		s.recordRun(ctx, job.ID, firedAt, conversationID, fmt.Errorf("load conversation: %w", err))
		return
	}
	s.broadcastConversationAdded(job.OwnerID, conversation)

	// Recorded before the enqueue, not after: last_conversation_id is how the
	// IM relay recognises an in-flight scheduled run, and the dispatcher can
	// hand the prompt to a worker before this goroutine gets to run again.
	s.recordRun(ctx, job.ID, firedAt, conversationID, nil)
	if _, err := s.Dispatcher.Enqueue(ctx, conversationID, job.Prompt); err != nil {
		s.recordRun(ctx, job.ID, firedAt, conversationID, fmt.Errorf("enqueue prompt: %w", err))
		return
	}
	s.recordDispatch(job.ID, conversationID)
}

// beginDispatch reserves the per-job dispatch slot, returning the release func
// and whether the slot was free. The returned func is a no-op when free is
// false, so callers can defer it unconditionally if they prefer.
func (s *CronScheduler) beginDispatch(jobID string) (release func(), free bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dispatching == nil {
		s.dispatching = make(map[string]struct{})
	}
	if _, busy := s.dispatching[jobID]; busy {
		return func() {}, false
	}
	s.dispatching[jobID] = struct{}{}
	return func() {
		s.mu.Lock()
		delete(s.dispatching, jobID)
		s.mu.Unlock()
	}, true
}

// recordDispatch remembers which conversation this fire handed to the
// dispatcher, so the next fire can tell whether it is still running.
func (s *CronScheduler) recordDispatch(jobID, conversationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRun == nil {
		s.lastRun = make(map[string]string)
	}
	s.lastRun[jobID] = conversationID
}

// previousRunActive reports the job's previous conversation and whether its
// prompt is still queued or running.
//
// Two signals, because neither alone is complete. The Drainer covers the live
// turn (both the queued-for-a-pool-slot and the actively-streaming phases) but
// has a gap between Enqueue and the runner registering the job. The store's
// pending/processing rows cover that gap, and also survive a restart that
// wiped the in-memory Drainer while the prompt row stayed queued.
//
// Deliberately fail-open: a store read error resolves to "not running" so one
// bad query cannot silently retire a scheduled job forever. Running a task
// twice is recoverable; never running it again is not.
func (s *CronScheduler) previousRunActive(ctx context.Context, jobID string) (string, bool) {
	s.mu.Lock()
	prev := s.lastRun[jobID]
	s.mu.Unlock()
	if prev == "" {
		return "", false
	}

	if s.Drainer != nil {
		for _, job := range s.Drainer.Jobs() {
			if job.ConversationID == prev {
				return prev, true
			}
		}
	}
	if s.Store != nil {
		if pending, err := s.Store.ListConversationsWithPending(ctx); err == nil {
			for _, id := range pending {
				if id == prev {
					return prev, true
				}
			}
		} else {
			log.Printf("crontab: overlap check for job %s could not read the prompt queue: %v (allowing the run)", jobID, err)
		}
	}

	// Finished: forget it so the map stays one entry per live job.
	s.mu.Lock()
	if s.lastRun[jobID] == prev {
		delete(s.lastRun, jobID)
	}
	s.mu.Unlock()
	return "", false
}

// conversationSeed resolves a fire's conversation through the same
// ResolveConversationSeed the web create endpoint uses, so a scheduled run
// starts on what the same agent would get from the browser (including the
// agent's think level when it falls back to the agent's default model). The
// task's own model is honoured when it still maps to a configured provider; a
// model that was since removed falls back to the agent's profile instead of
// failing every fire.
func (s *CronScheduler) conversationSeed(user store.User, configuredModel string) (ConversationSeed, error) {
	if s.Cfg == nil {
		return ConversationSeed{}, errors.New("server configuration is unavailable")
	}
	var req ConversationSeedRequest
	if configuredModel = strings.TrimSpace(configuredModel); configuredModel != "" {
		if provider := ProviderForModelForConfig(s.Cfg, configuredModel); provider != "" {
			req.Provider, req.Model = provider, configuredModel
		}
	}
	return ResolveConversationSeed(s.Cfg, user, req)
}

func (s *CronScheduler) recordRun(
	ctx context.Context,
	jobID string,
	firedAt time.Time,
	conversationID string,
	runErr error,
) {
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
		log.Printf("crontab: job %s: %v", jobID, runErr)
	}
	if err := s.Jobs.RecordCronJobRun(ctx, jobID, firedAt, conversationID, errText); err != nil {
		log.Printf("crontab: record run %s: %v", jobID, err)
	}
}

func (s *CronScheduler) broadcastConversationAdded(ownerID string, conversation store.Conversation) {
	if s.UserHub == nil || ownerID == "" {
		return
	}
	payload, err := json.Marshal(struct {
		Type         string             `json:"type"`
		Conversation store.Conversation `json:"conversation"`
	}{
		Type:         "conversation_added",
		Conversation: conversation,
	})
	if err == nil {
		s.UserHub.Broadcast(ownerID, payload)
	}
}

func scheduledConversationTitle(prompt string) string {
	normalized := strings.Join(strings.Fields(prompt), " ")
	if normalized == "" {
		return "Scheduled task"
	}
	if utf8.RuneCountInString(normalized) > cronConversationTitleRunes {
		runes := []rune(normalized)
		normalized = string(runes[:cronConversationTitleRunes]) + "…"
	}
	return "Scheduled · " + normalized
}
