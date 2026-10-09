package store

import (
	"context"
	"testing"
)

func seedCronAgent(t *testing.T, s *SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "owner-1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, User{
		ID:      "agent-1",
		OwnerID: "owner-1",
		Name:    "Researcher",
		WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
}

func TestCronJobCRUD(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedCronAgent(t, s)
	ctx := context.Background()

	job := CronJob{
		ID:                   "cron-1",
		OwnerID:              "owner-1",
		AgentID:              "agent-1",
		Model:                "claude-sonnet",
		Expression:           "0 9 * * 1-5",
		Timezone:             "Asia/Shanghai",
		Description:          "Weekday build summary",
		Prompt:               "Prepare the daily report",
		Enabled:              true,
		NotificationsEnabled: true,
	}
	if err := s.CreateCronJob(ctx, job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	got, err := s.GetCronJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if got.AgentName != "Researcher" || got.Model != job.Model || got.Description != job.Description || got.Prompt != job.Prompt ||
		!got.Enabled || !got.NotificationsEnabled {
		t.Fatalf("unexpected job: %+v", got)
	}

	got.Expression = "30 8 * * *"
	got.Model = "claude-opus"
	got.Description = "Updated summary"
	got.Enabled = false
	got.NotificationsEnabled = false
	got.DisabledReason = "manual"
	if err := s.UpdateCronJob(ctx, got); err != nil {
		t.Fatalf("update job: %v", err)
	}
	listed, err := s.ListCronJobs(ctx, "owner-1")
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(listed) != 1 || listed[0].Model != "claude-opus" ||
		listed[0].Expression != "30 8 * * *" || listed[0].Description != "Updated summary" ||
		listed[0].Enabled || listed[0].NotificationsEnabled {
		t.Fatalf("unexpected listed jobs: %+v", listed)
	}

	if err := s.DeleteCronJob(ctx, job.ID, "owner-1"); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	if _, err := s.GetCronJob(ctx, job.ID); err == nil {
		t.Fatal("expected deleted job to be missing")
	}
}

func TestCronJobAgentLifecycleDisablesTask(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(context.Context, *SQLiteStore) error
	}{
		{
			name: "archive",
			mutate: func(ctx context.Context, s *SQLiteStore) error {
				return s.ArchiveUser(ctx, "agent-1")
			},
		},
		{
			name: "delete",
			mutate: func(ctx context.Context, s *SQLiteStore) error {
				return s.DeleteUser(ctx, "agent-1")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			seedCronAgent(t, s)
			ctx := context.Background()
			if err := s.CreateCronJob(ctx, CronJob{
				ID:         "cron-1",
				OwnerID:    "owner-1",
				AgentID:    "agent-1",
				Expression: "* * * * *",
				Timezone:   "UTC",
				Prompt:     "Run task",
				Enabled:    true,
			}); err != nil {
				t.Fatalf("create job: %v", err)
			}
			if err := tt.mutate(ctx, s); err != nil {
				t.Fatalf("%s agent: %v", tt.name, err)
			}
			got, err := s.GetCronJob(ctx, "cron-1")
			if err != nil {
				t.Fatalf("get job: %v", err)
			}
			if got.Enabled || got.DisabledReason != "agent_unavailable" {
				t.Fatalf("job not disabled after agent %s: %+v", tt.name, got)
			}
		})
	}
}
