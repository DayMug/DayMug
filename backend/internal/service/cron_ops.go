package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// maxCronPromptBytes caps the stored prompt. Every firing replays this text as
// a chat turn, so an unbounded value would be an unbounded prompt cost on a
// schedule nobody is watching.
const maxCronPromptBytes = 64 * 1024

// cronReloadTimeout bounds the scheduler re-drive that follows each mutation.
// The reload re-reads every enabled job, so it is capped independently of the
// request that triggered it rather than being allowed to hold the connection.
const cronReloadTimeout = 10 * time.Second

// CronOps owns the write use-cases behind /api/cron-jobs. Each one is a single
// use case rather than a bare write: the expression and timezone have to parse
// before anything is stored, the target Agent has to still belong to the
// caller, and the in-process scheduler has to be re-driven afterwards or the
// change would not take effect until the next restart.
//
// Bare struct with public fields and no constructor, assembled per call by the
// handler — same reason as the other *Ops types.
type CronOps struct {
	// Store resolves the target Agent for the ownership check.
	Store store.Store
	// Jobs is the scheduled-task store.
	Jobs store.CronStore
	// Bots validates a pinned delivery bot against the target Agent.
	Bots store.BotStore
	// Scheduler is re-driven after every mutation. Optional: a nil Scheduler
	// skips the reload, which is the pre-existing handler contract for test
	// wiring and for deployments that run no in-process scheduler.
	Scheduler CronReloader
}

// CronJobParams is the mutable field set of a scheduled task as supplied by a
// create or update call — the service-side mirror of the handler's JSON body.
type CronJobParams struct {
	AgentID     string
	Model       string
	Expression  string
	Timezone    string
	Description string
	Prompt      string
	Enabled     bool
	// NotificationsEnabled controls Bark/PushDeer delivery for each
	// conversation created by this task.
	NotificationsEnabled bool
	// DeliverToBot relays each run's answer into the Agent's bound IM thread
	// and silences that run's Bark/PushDeer push, since the thread reply is
	// then the delivery.
	DeliverToBot bool
	// BotID pins the delivery to one of the Agent's bots. Empty means "whichever
	// thread the Agent used most recently", which is what jobs created before
	// the picker existed still do.
	BotID string
}

// NormalizeCronTimezone defaults a blank timezone to UTC. Exported because the
// listing path renders next-run times with the same rule.
func NormalizeCronTimezone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "UTC"
	}
	return strings.TrimSpace(value)
}

// AttachNextCronRun stamps the job's computed next firing time. Disabled jobs
// and unparseable expressions are left with a nil NextRunAt — the field is
// advisory display data, so a failure here must not fail the surrounding call.
func AttachNextCronRun(job *store.CronJob) {
	if job == nil || !job.Enabled {
		return
	}
	if next, err := NextCronRun(job.Expression, job.Timezone, time.Now()); err == nil {
		job.NextRunAt = &next
	}
}

// validate checks p against both the schedule grammar and the caller's Agent
// list. The Agent check is deliberately re-run on every write rather than
// trusted from create time: an Agent can be archived, re-owned or deleted
// between the two, and a job pointing at one of those would fire into nothing.
func (o *CronOps) validate(ctx context.Context, ownerID string, p CronJobParams) error {
	agentID := strings.TrimSpace(p.AgentID)
	if agentID == "" {
		return BadRequest("agent_id is required")
	}
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return BadRequest("task content is required")
	}
	if len(prompt) > maxCronPromptBytes {
		return BadRequest("task content is too long")
	}
	if _, err := NextCronRun(
		strings.TrimSpace(p.Expression),
		NormalizeCronTimezone(p.Timezone),
		time.Now(),
	); err != nil {
		return BadRequest(err.Error())
	}
	agentUser, err := o.Store.GetUser(ctx, agentID)
	if err != nil ||
		agentUser.Username != "" ||
		agentUser.OwnerID != ownerID ||
		agentUser.Archived {
		// One message for "missing", "not an agent", "someone else's" and
		// "archived": distinguishing them would let a caller probe for the
		// existence of Agents they do not own.
		return BadRequest("agent is unavailable")
	}
	return o.validateDeliveryBot(ctx, agentID, p)
}

// validateDeliveryBot rejects a pinned bot that does not belong to the target
// Agent. Checked here rather than left to the delivery query, which would just
// find no thread and stay silent — a job that never delivers looks identical to
// one whose bot has simply never been messaged, and the operator gets no signal.
func (o *CronOps) validateDeliveryBot(ctx context.Context, agentID string, p CronJobParams) error {
	botID := strings.TrimSpace(p.BotID)
	if botID == "" {
		return nil
	}
	if !p.DeliverToBot {
		// Nothing to deliver through. Rejecting rather than silently clearing
		// keeps the stored row honest about what the caller asked for.
		return BadRequest("bot_id requires deliver_to_bot")
	}
	if o.Bots == nil {
		return Internal("bot store is unavailable", nil)
	}
	bot, err := o.Bots.GetBot(ctx, botID)
	if err != nil || bot.AgentID != agentID {
		// Same conflation as the Agent check above: "missing" and "someone
		// else's" answer identically so a caller cannot probe for bots.
		return BadRequest("bot is unavailable")
	}
	return nil
}

// reload re-drives the scheduler so the change takes effect immediately.
// Bounded separately from the caller's own deadline because it re-reads every
// enabled job, not just the one that changed.
func (o *CronOps) reload(ctx context.Context) error {
	if o.Scheduler == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cronReloadTimeout)
	defer cancel()
	if err := o.Scheduler.Reload(ctx); err != nil {
		return Internal("reload scheduled tasks: "+err.Error(), err)
	}
	return nil
}

// Create stores a new scheduled task for ownerID and returns it with its next
// firing time attached.
func (o *CronOps) Create(ctx context.Context, ownerID string, p CronJobParams) (store.CronJob, error) {
	if err := o.validate(ctx, ownerID, p); err != nil {
		return store.CronJob{}, err
	}
	// A task created in the off position records why, so the UI can tell an
	// operator's choice apart from the scheduler having disabled a failing job.
	reason := ""
	if !p.Enabled {
		reason = "manual"
	}
	job := store.CronJob{
		ID:                   uuid.New().String(),
		OwnerID:              ownerID,
		AgentID:              strings.TrimSpace(p.AgentID),
		Model:                strings.TrimSpace(p.Model),
		Expression:           strings.TrimSpace(p.Expression),
		Timezone:             NormalizeCronTimezone(p.Timezone),
		Description:          strings.TrimSpace(p.Description),
		Prompt:               strings.TrimSpace(p.Prompt),
		Enabled:              p.Enabled,
		NotificationsEnabled: p.NotificationsEnabled,
		DeliverToBot:         p.DeliverToBot,
		BotID:                strings.TrimSpace(p.BotID),
		DisabledReason:       reason,
	}
	if err := o.Jobs.CreateCronJob(ctx, job); err != nil {
		return store.CronJob{}, Internal(err.Error(), err)
	}
	if err := o.reload(ctx); err != nil {
		return store.CronJob{}, err
	}
	// Re-read: created_at/updated_at and the joined agent name are assigned by
	// the store and belong in the response.
	created, err := o.Jobs.GetCronJob(ctx, job.ID)
	if err != nil {
		return store.CronJob{}, Internal(err.Error(), err)
	}
	AttachNextCronRun(&created)
	return created, nil
}

// Update rewrites one of ownerID's tasks and returns it with its next firing
// time attached.
//
// The ownerID match is the ownership gate, and a task belonging to someone else
// reports "not found" rather than "forbidden" so foreign ids stay
// indistinguishable from absent ones.
func (o *CronOps) Update(ctx context.Context, ownerID, jobID string, p CronJobParams) (store.CronJob, error) {
	current, err := o.Jobs.GetCronJob(ctx, jobID)
	if err != nil || current.OwnerID != ownerID {
		return store.CronJob{}, NotFound("scheduled task not found")
	}
	if err := o.validate(ctx, ownerID, p); err != nil {
		return store.CronJob{}, err
	}

	agentID, model := strings.TrimSpace(p.AgentID), strings.TrimSpace(p.Model)
	// The recorded failure describes a fire under the previous state. Once the
	// operator turns the job back on or points it at another Agent/model, it is
	// no longer evidence about the next fire, and keeping it paints a healthy
	// job red until the next scheduled slot (days, for a weekday task). The
	// next fire records a fresh error if the problem persists.
	if (p.Enabled && !current.Enabled) || agentID != current.AgentID || model != current.Model {
		current.LastError = ""
	}
	current.AgentID = agentID
	current.Model = model
	current.Expression = strings.TrimSpace(p.Expression)
	current.Timezone = NormalizeCronTimezone(p.Timezone)
	current.Description = strings.TrimSpace(p.Description)
	current.Prompt = strings.TrimSpace(p.Prompt)
	current.Enabled = p.Enabled
	current.NotificationsEnabled = p.NotificationsEnabled
	current.DeliverToBot = p.DeliverToBot
	current.BotID = strings.TrimSpace(p.BotID)
	// Re-enabling clears the reason, so a job the scheduler auto-disabled stops
	// advertising a stale failure once the operator turns it back on.
	if p.Enabled {
		current.DisabledReason = ""
	} else {
		current.DisabledReason = "manual"
	}
	if err := o.Jobs.UpdateCronJob(ctx, current); err != nil {
		return store.CronJob{}, StoreError(err, "scheduled task not found")
	}
	if err := o.reload(ctx); err != nil {
		return store.CronJob{}, err
	}
	updated, err := o.Jobs.GetCronJob(ctx, current.ID)
	if err != nil {
		return store.CronJob{}, Internal(err.Error(), err)
	}
	AttachNextCronRun(&updated)
	return updated, nil
}

// Delete removes one of ownerID's tasks. Scoped by ownerID as well as by id,
// which is what stops a guessed id from reaching another user's schedule.
func (o *CronOps) Delete(ctx context.Context, ownerID, jobID string) error {
	if err := o.Jobs.DeleteCronJob(ctx, jobID, ownerID); err != nil {
		return StoreError(err, "scheduled task not found")
	}
	return o.reload(ctx)
}
