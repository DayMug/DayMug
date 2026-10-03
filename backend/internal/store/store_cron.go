package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CronJob is one recurring Agent prompt. Every firing creates a regular
// conversation and user message; the scheduler then hands it to Dispatcher so
// execution follows the same pool, environment, sandbox, and persistence path
// as an interactive chat turn.
type CronJob struct {
	ID                   string     `json:"id"`
	OwnerID              string     `json:"owner_id"`
	AgentID              string     `json:"agent_id"`
	AgentName            string     `json:"agent_name"`
	Model                string     `json:"model"`
	Expression           string     `json:"expression"`
	Timezone             string     `json:"timezone"`
	Description          string     `json:"description"`
	Prompt               string     `json:"prompt"`
	Enabled              bool       `json:"enabled"`
	NotificationsEnabled bool       `json:"notifications_enabled"`
	DeliverToBot         bool       `json:"deliver_to_bot"`
	BotID                string     `json:"bot_id"`
	DisabledReason       string     `json:"disabled_reason,omitempty"`
	LastRunAt            *time.Time `json:"last_run_at,omitempty"`
	NextRunAt            *time.Time `json:"next_run_at,omitempty"`
	LastConversationID   string     `json:"last_conversation_id,omitempty"`
	LastError            string     `json:"last_error,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

const cronJobSelect = `
	SELECT cj.id, cj.owner_id, cj.agent_id,
	       COALESCE(a.name, u.name, ''),
	       cj.model, cj.expression, cj.timezone, cj.description, cj.prompt, cj.enabled,
	       cj.notifications_enabled,
	       cj.deliver_to_bot,
	       cj.bot_id,
	       cj.disabled_reason, cj.last_run_at, cj.last_conversation_id,
	       cj.last_error, cj.created_at, cj.updated_at
	  FROM cron_jobs cj
	  LEFT JOIN agents a ON a.id = cj.agent_id
	  LEFT JOIN users u ON u.id = cj.agent_id AND u.username = ''`

func scanCronJob(scanner interface{ Scan(...any) error }) (CronJob, error) {
	var (
		job     CronJob
		lastRun sql.NullTime
	)
	err := scanner.Scan(
		&job.ID,
		&job.OwnerID,
		&job.AgentID,
		&job.AgentName,
		&job.Model,
		&job.Expression,
		&job.Timezone,
		&job.Description,
		&job.Prompt,
		&job.Enabled,
		&job.NotificationsEnabled,
		&job.DeliverToBot,
		&job.BotID,
		&job.DisabledReason,
		&lastRun,
		&job.LastConversationID,
		&job.LastError,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CronJob{}, ErrNotFound
	}
	if err != nil {
		return CronJob{}, err
	}
	if lastRun.Valid {
		t := lastRun.Time
		job.LastRunAt = &t
	}
	return job, nil
}

func (s *SQLiteStore) CreateCronJob(ctx context.Context, job CronJob) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cron_jobs
		    (id, owner_id, agent_id, model, expression, timezone, description, prompt, enabled,
		     notifications_enabled, deliver_to_bot, bot_id, disabled_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		job.OwnerID,
		job.AgentID,
		job.Model,
		job.Expression,
		job.Timezone,
		job.Description,
		job.Prompt,
		job.Enabled,
		job.NotificationsEnabled,
		job.DeliverToBot,
		job.BotID,
		job.DisabledReason,
	)
	return err
}

func (s *SQLiteStore) GetCronJob(ctx context.Context, id string) (CronJob, error) {
	return scanCronJob(s.db.QueryRowContext(ctx, cronJobSelect+" WHERE cj.id = ?", id))
}

func (s *SQLiteStore) ListCronJobs(ctx context.Context, ownerID string) ([]CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		cronJobSelect+" WHERE cj.owner_id = ? ORDER BY cj.created_at DESC", ownerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	jobs := make([]CronJob, 0)
	for rows.Next() {
		job, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLiteStore) ListEnabledCronJobs(ctx context.Context) ([]CronJob, error) {
	rows, err := s.db.QueryContext(ctx,
		cronJobSelect+" WHERE cj.enabled = 1 ORDER BY cj.created_at")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	jobs := make([]CronJob, 0)
	for rows.Next() {
		job, err := scanCronJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *SQLiteStore) UpdateCronJob(ctx context.Context, job CronJob) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE cron_jobs
		   SET agent_id = ?,
		       model = ?,
		       expression = ?,
		       timezone = ?,
		       description = ?,
		       prompt = ?,
		       enabled = ?,
		       notifications_enabled = ?,
		       deliver_to_bot = ?,
		       bot_id = ?,
		       disabled_reason = ?,
		       last_error = ?,
		       updated_at = datetime('now')
		 WHERE id = ? AND owner_id = ?`,
		job.AgentID,
		job.Model,
		job.Expression,
		job.Timezone,
		job.Description,
		job.Prompt,
		job.Enabled,
		job.NotificationsEnabled,
		job.DeliverToBot,
		job.BotID,
		job.DisabledReason,
		job.LastError,
		job.ID,
		job.OwnerID,
	)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *SQLiteStore) DeleteCronJob(ctx context.Context, id, ownerID string) error {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM cron_jobs WHERE id = ? AND owner_id = ?", id, ownerID)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *SQLiteStore) DisableCronJob(ctx context.Context, id, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE cron_jobs
		   SET enabled = 0, disabled_reason = ?, updated_at = datetime('now')
		 WHERE id = ?`,
		reason, id)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func (s *SQLiteStore) RecordCronJobRun(
	ctx context.Context,
	id string,
	runAt time.Time,
	conversationID, runError string,
) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE cron_jobs
		   SET last_run_at = ?,
		       last_conversation_id = ?,
		       last_error = ?,
		       updated_at = datetime('now')
		 WHERE id = ?`,
		runAt.UTC(),
		conversationID,
		runError,
		id,
	)
	if err != nil {
		return err
	}
	return requireAffected(res)
}

func requireAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
