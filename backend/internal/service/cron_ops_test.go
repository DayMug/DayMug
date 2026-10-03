package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// countingReloader records that the scheduler was re-driven. Whether the reload
// happened at all is the point of these assertions: a write that skips it leaves
// the in-process schedule disagreeing with the database until restart.
type countingReloader struct {
	calls int
	err   error
}

func (r *countingReloader) Reload(context.Context) error {
	r.calls++
	return r.err
}

func newCronTestOps(t *testing.T) (*CronOps, *countingReloader) {
	t.Helper()
	s := newCronTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "owner-1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent-1", Name: "Bot", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// A second household, to prove the ownership scoping is real.
	if err := s.CreateUser(ctx, store.User{ID: "owner-2", Name: "Bob", Username: "bob"}); err != nil {
		t.Fatalf("create other owner: %v", err)
	}
	r := &countingReloader{}
	return &CronOps{Store: s, Jobs: s, Bots: s, Scheduler: r}, r
}

// seedCronDeliveryBots gives the fixture one bot the owner's Agent may deliver
// through and one that belongs to a different Agent, so "rejected" can be shown
// to depend on ownership rather than on the bot merely existing.
func seedCronDeliveryBots(t *testing.T, o *CronOps) {
	t.Helper()
	ctx := context.Background()
	if err := o.Bots.CreateBot(ctx, store.Bot{
		ID: "bot-1", AgentID: "agent-1", Name: "Slack", Platform: "slack", Enabled: true,
	}); err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if err := o.Bots.CreateBot(ctx, store.Bot{
		ID: "bot-foreign", AgentID: "agent-2", Name: "Foreign", Platform: "slack", Enabled: true,
	}); err != nil {
		t.Fatalf("create foreign bot: %v", err)
	}
}

// TestCronOpsValidatesPinnedDeliveryBot — pinning is checked at write time
// because the delivery query cannot complain: a job pointing at a bot that is
// gone, or never belonged to this Agent, simply resolves no thread and stays
// silent, which is indistinguishable from a bot nobody has messaged yet.
func TestCronOpsValidatesPinnedDeliveryBot(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*CronJobParams)
		wantMsg string
	}{
		{
			name:    "bot pinned without delivery enabled",
			mutate:  func(p *CronJobParams) { p.BotID = "bot-1"; p.DeliverToBot = false },
			wantMsg: "bot_id requires deliver_to_bot",
		},
		{
			name:    "bot does not exist",
			mutate:  func(p *CronJobParams) { p.BotID = "bot-gone"; p.DeliverToBot = true },
			wantMsg: "bot is unavailable",
		},
		{
			name:    "bot belongs to another agent",
			mutate:  func(p *CronJobParams) { p.BotID = "bot-foreign"; p.DeliverToBot = true },
			wantMsg: "bot is unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, reloader := newCronTestOps(t)
			seedCronDeliveryBots(t, o)
			p := cronParams()
			tc.mutate(&p)
			_, err := o.Create(context.Background(), "owner-1", p)
			status, msg := svcStatusOf(t, err)
			if status != 400 || msg != tc.wantMsg {
				t.Fatalf("got %d %q, want 400 %q", status, msg, tc.wantMsg)
			}
			if reloader.calls != 0 {
				t.Errorf("a rejected write must not re-drive the scheduler, got %d calls", reloader.calls)
			}
		})
	}

	t.Run("own bot with delivery enabled", func(t *testing.T) {
		o, _ := newCronTestOps(t)
		seedCronDeliveryBots(t, o)
		p := cronParams()
		p.BotID = "bot-1"
		p.DeliverToBot = true
		created, err := o.Create(context.Background(), "owner-1", p)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		// Read back rather than trusting the returned struct: the scheduler reads
		// the row, so a bot_id that never reached the column would deliver to
		// whatever thread was most recent instead of the one the owner chose.
		stored, err := o.Jobs.GetCronJob(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("GetCronJob: %v", err)
		}
		if stored.BotID != "bot-1" || !stored.DeliverToBot {
			t.Fatalf("stored job = %+v, want bot_id bot-1 with delivery enabled", stored)
		}
	})
}

func cronParams() CronJobParams {
	return CronJobParams{
		AgentID:              "agent-1",
		Expression:           "0 9 * * 1-5",
		Timezone:             "Asia/Shanghai",
		Description:          "  Weekday build summary  ",
		Prompt:               "Prepare the daily report",
		Enabled:              true,
		NotificationsEnabled: true,
	}
}

func TestCronOpsCreate(t *testing.T) {
	o, reloader := newCronTestOps(t)
	job, err := o.Create(context.Background(), "owner-1", cronParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.OwnerID != "owner-1" || job.AgentID != "agent-1" ||
		job.Description != "Weekday build summary" || !job.Enabled {
		t.Errorf("unexpected job: %+v", job)
	}
	if job.NextRunAt == nil {
		t.Error("an enabled job should come back with its next firing time attached")
	}
	if reloader.calls != 1 {
		t.Errorf("reload calls = %d, want 1", reloader.calls)
	}
}

// TestCronOpsCreateDisabledRecordsManualReason distinguishes "the operator
// switched this off" from "the scheduler disabled a failing job", which is what
// the UI renders differently.
func TestCronOpsCreateDisabledRecordsManualReason(t *testing.T) {
	o, _ := newCronTestOps(t)
	p := cronParams()
	p.Enabled = false
	job, err := o.Create(context.Background(), "owner-1", p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.DisabledReason != "manual" {
		t.Errorf("disabled_reason = %q, want manual", job.DisabledReason)
	}
	if job.NextRunAt != nil {
		t.Error("a disabled job has no next run")
	}
}

func TestCronOpsRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CronJobParams)
	}{
		{name: "missing agent", mutate: func(p *CronJobParams) { p.AgentID = " " }},
		{name: "blank prompt", mutate: func(p *CronJobParams) { p.Prompt = "  " }},
		{name: "oversized prompt", mutate: func(p *CronJobParams) { p.Prompt = strings.Repeat("x", maxCronPromptBytes+1) }},
		{name: "unparseable expression", mutate: func(p *CronJobParams) { p.Expression = "every morning" }},
		{name: "unknown timezone", mutate: func(p *CronJobParams) { p.Timezone = "Mars/Olympus" }},
		{name: "agent owned by someone else", mutate: func(p *CronJobParams) { p.AgentID = "owner-2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, reloader := newCronTestOps(t)
			p := cronParams()
			tc.mutate(&p)
			_, err := o.Create(context.Background(), "owner-1", p)
			if svcStatus(err) != 400 {
				t.Fatalf("status = %d, want 400 (err=%v)", svcStatus(err), err)
			}
			if reloader.calls != 0 {
				t.Errorf("a rejected write must not re-drive the scheduler, got %d calls", reloader.calls)
			}
		})
	}
}

func TestCronOpsUpdate(t *testing.T) {
	o, reloader := newCronTestOps(t)
	created, err := o.Create(context.Background(), "owner-1", cronParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	edit := cronParams()
	edit.Prompt = "Prepare the weekly report"
	edit.Expression = "0 8 * * 1"
	updated, err := o.Update(context.Background(), "owner-1", created.ID, edit)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Prompt != "Prepare the weekly report" || updated.Expression != "0 8 * * 1" {
		t.Errorf("unexpected job: %+v", updated)
	}
	if reloader.calls != 2 {
		t.Errorf("reload calls = %d, want 2 (create + update)", reloader.calls)
	}
}

// TestCronOpsUpdateReEnableClearsReason — turning a job back on must drop the
// stale explanation, otherwise a running job keeps advertising why it was off.
func TestCronOpsUpdateReEnableClearsReason(t *testing.T) {
	o, _ := newCronTestOps(t)
	p := cronParams()
	p.Enabled = false
	created, err := o.Create(context.Background(), "owner-1", p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := o.Update(context.Background(), "owner-1", created.ID, cronParams())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.DisabledReason != "" {
		t.Errorf("disabled_reason = %q, want empty after re-enabling", updated.DisabledReason)
	}
}

// TestCronOpsUpdateClearsStaleRunError — a failure recorded before the job was
// turned off (or re-pointed) must not stay on the card after the operator fixes
// and re-enables it; a weekday task would otherwise show it until the next slot.
// A cosmetic edit of a still-enabled job keeps the error, since it says nothing
// about whether the next fire will succeed.
func TestCronOpsUpdateClearsStaleRunError(t *testing.T) {
	const runErr = "no valid model configured for provider claude"
	cases := []struct {
		name        string
		startOff    bool
		edit        func(*CronJobParams)
		wantCleared bool
	}{
		{name: "re-enable", startOff: true, edit: func(*CronJobParams) {}, wantCleared: true},
		{name: "change model", edit: func(p *CronJobParams) { p.Model = "claude-opus-5-5" }, wantCleared: true},
		{name: "edit prompt only", edit: func(p *CronJobParams) { p.Prompt = "Other report" }, wantCleared: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := newCronTestOps(t)
			ctx := context.Background()
			p := cronParams()
			p.Enabled = !tc.startOff
			created, err := o.Create(ctx, "owner-1", p)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := o.Jobs.RecordCronJobRun(ctx, created.ID, time.Now(), "", runErr); err != nil {
				t.Fatalf("RecordCronJobRun: %v", err)
			}
			edit := cronParams()
			tc.edit(&edit)
			if _, err := o.Update(ctx, "owner-1", created.ID, edit); err != nil {
				t.Fatalf("Update: %v", err)
			}
			stored, err := o.Jobs.GetCronJob(ctx, created.ID)
			if err != nil {
				t.Fatalf("GetCronJob: %v", err)
			}
			want := runErr
			if tc.wantCleared {
				want = ""
			}
			if stored.LastError != want {
				t.Errorf("last_error = %q, want %q", stored.LastError, want)
			}
		})
	}
}

// TestCronOpsScopesToOwner pins that another user's task is reported absent
// rather than forbidden, and is left untouched.
func TestCronOpsScopesToOwner(t *testing.T) {
	o, _ := newCronTestOps(t)
	created, err := o.Create(context.Background(), "owner-1", cronParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := o.Update(context.Background(), "owner-2", created.ID, cronParams()); svcStatus(err) != 404 {
		t.Errorf("Update as a stranger: status = %d, want 404", svcStatus(err))
	}
	if err := o.Delete(context.Background(), "owner-2", created.ID); svcStatus(err) != 404 {
		t.Errorf("Delete as a stranger: status = %d, want 404", svcStatus(err))
	}
	jobs, err := o.Jobs.ListCronJobs(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Errorf("the owner's task should survive a stranger's delete, got %d rows", len(jobs))
	}
}

func TestCronOpsDelete(t *testing.T) {
	o, reloader := newCronTestOps(t)
	created, err := o.Create(context.Background(), "owner-1", cronParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := o.Delete(context.Background(), "owner-1", created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	jobs, err := o.Jobs.ListCronJobs(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected no rows, got %d", len(jobs))
	}
	if reloader.calls != 2 {
		t.Errorf("reload calls = %d, want 2 (create + delete)", reloader.calls)
	}
}

// TestCronOpsNilSchedulerIsTolerated — deployments without an in-process
// scheduler (and most unit wiring) leave Scheduler nil, which must not turn
// every write into a 500.
func TestCronOpsNilSchedulerIsTolerated(t *testing.T) {
	o, _ := newCronTestOps(t)
	o.Scheduler = nil
	if _, err := o.Create(context.Background(), "owner-1", cronParams()); err != nil {
		t.Fatalf("Create with nil scheduler: %v", err)
	}
}

func TestNormalizeCronTimezone(t *testing.T) {
	cases := map[string]string{"": "UTC", "   ": "UTC", " Asia/Shanghai ": "Asia/Shanghai"}
	for in, want := range cases {
		if got := NormalizeCronTimezone(in); got != want {
			t.Errorf("NormalizeCronTimezone(%q) = %q, want %q", in, got, want)
		}
	}
}
