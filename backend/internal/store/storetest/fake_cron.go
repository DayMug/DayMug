package storetest

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// fakeNow mirrors SQLite's datetime('now') column defaults: UTC, whole seconds.
func fakeNow() time.Time { return time.Now().UTC().Truncate(time.Second) }

// cronAgentName resolves the display name the SQLite read joins in from the
// agent row. Only agent rows (empty Username) qualify, as in the LEFT JOIN.
func (m *Fake) cronAgentName(agentID string) string {
	for _, u := range m.Users {
		if u.ID == agentID && u.Username == "" {
			return u.Name
		}
	}
	return ""
}

func (m *Fake) cronJobView(job store.CronJob) store.CronJob {
	job.AgentName = m.cronAgentName(job.AgentID)
	if job.LastRunAt != nil {
		t := *job.LastRunAt
		job.LastRunAt = &t
	}
	// NextRunAt is computed by the scheduler, never persisted.
	job.NextRunAt = nil
	return job
}

func (m *Fake) CreateCronJob(_ context.Context, job store.CronJob) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, existing := range m.CronJobs {
		if existing.ID == job.ID {
			return fmt.Errorf("UNIQUE constraint failed: cron_jobs.id")
		}
	}
	// The insert writes only the configuration columns; run bookkeeping and
	// timestamps start from the table defaults.
	now := fakeNow()
	job.AgentName = ""
	job.LastRunAt, job.NextRunAt = nil, nil
	job.LastConversationID, job.LastError = "", ""
	job.CreatedAt, job.UpdatedAt = now, now
	m.CronJobs = append(m.CronJobs, job)
	return nil
}

func (m *Fake) GetCronJob(_ context.Context, id string) (store.CronJob, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, job := range m.CronJobs {
		if job.ID == id {
			return m.cronJobView(job), nil
		}
	}
	return store.CronJob{}, store.ErrNotFound
}

func (m *Fake) ListCronJobs(_ context.Context, ownerID string) ([]store.CronJob, error) {
	return m.listCronJobs(func(job store.CronJob) bool { return job.OwnerID == ownerID }, true), nil
}

func (m *Fake) ListEnabledCronJobs(_ context.Context) ([]store.CronJob, error) {
	return m.listCronJobs(func(job store.CronJob) bool { return job.Enabled }, false), nil
}

func (m *Fake) listCronJobs(keep func(store.CronJob) bool, newestFirst bool) []store.CronJob {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	jobs := make([]store.CronJob, 0)
	for _, job := range m.CronJobs {
		if keep(job) {
			jobs = append(jobs, m.cronJobView(job))
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		if newestFirst {
			return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs
}

// UpdateCronJob is owner-scoped like the SQL WHERE clause: a foreign id reads
// as ErrNotFound. Owner, creation time and run bookkeeping other than
// last_error are not writable through it.
func (m *Fake) UpdateCronJob(_ context.Context, job store.CronJob) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i := range m.CronJobs {
		cur := &m.CronJobs[i]
		if cur.ID != job.ID || cur.OwnerID != job.OwnerID {
			continue
		}
		cur.AgentID = job.AgentID
		cur.Model = job.Model
		cur.Expression = job.Expression
		cur.Timezone = job.Timezone
		cur.Description = job.Description
		cur.Prompt = job.Prompt
		cur.Enabled = job.Enabled
		cur.NotificationsEnabled = job.NotificationsEnabled
		cur.DeliverToBot = job.DeliverToBot
		cur.BotID = job.BotID
		cur.DisabledReason = job.DisabledReason
		cur.LastError = job.LastError
		cur.UpdatedAt = fakeNow()
		return nil
	}
	return store.ErrNotFound
}

func (m *Fake) DeleteCronJob(_ context.Context, id, ownerID string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, job := range m.CronJobs {
		if job.ID == id && job.OwnerID == ownerID {
			m.CronJobs = append(m.CronJobs[:i], m.CronJobs[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) DisableCronJob(_ context.Context, id, reason string) error {
	return m.updateCronJob(id, func(job *store.CronJob) {
		job.Enabled = false
		job.DisabledReason = reason
	})
}

func (m *Fake) RecordCronJobRun(_ context.Context, id string, runAt time.Time, conversationID, runError string) error {
	return m.updateCronJob(id, func(job *store.CronJob) {
		t := runAt.UTC()
		job.LastRunAt = &t
		job.LastConversationID = conversationID
		job.LastError = runError
	})
}

func (m *Fake) updateCronJob(id string, apply func(*store.CronJob)) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i := range m.CronJobs {
		if m.CronJobs[i].ID == id {
			apply(&m.CronJobs[i])
			m.CronJobs[i].UpdatedAt = fakeNow()
			return nil
		}
	}
	return store.ErrNotFound
}

// CronBotDeliveryTarget mirrors the SQLite join: the job that created the
// conversation must opt in, the conversation must hold no user message other
// than the run's own prompt, and the thread is the job Agent's most recently
// active one on the job's bot (any bot when the job names none).
func (m *Fake) CronBotDeliveryTarget(_ context.Context, conversationID, messageID string) (store.CronDelivery, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, msg := range m.Messages[conversationID] {
		if msg.Role == "user" && msg.ID != messageID {
			return store.CronDelivery{}, store.ErrNotFound
		}
	}
	var best store.CronDelivery
	found := false
	for _, job := range m.CronJobs {
		if job.LastConversationID != conversationID || !job.DeliverToBot {
			continue
		}
		var bot store.Bot
		botFound := false
		for _, b := range m.Bots {
			if b.ID == job.BotID && b.AgentID == job.AgentID {
				bot, botFound = b, true
				break
			}
		}
		for _, thread := range m.BotThreads {
			if thread.AgentID != job.AgentID {
				continue
			}
			matches := job.BotID == "" ||
				(botFound && thread.Platform == bot.Platform+"@"+bot.AgentID+"@"+bot.ID)
			if !matches {
				continue
			}
			if !found || thread.UpdatedAt.After(best.Thread.UpdatedAt) {
				best = store.CronDelivery{Thread: thread}
				if botFound {
					best.BotID = bot.ID
				}
				found = true
			}
		}
	}
	if !found {
		return store.CronDelivery{}, store.ErrNotFound
	}
	return best, nil
}
