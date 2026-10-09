package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
)

type fakeCronReloader struct {
	calls int
}

func (f *fakeCronReloader) Reload(context.Context) error {
	f.calls++
	return nil
}

func setupCronRouter(t *testing.T, ownerID string) (*gin.Engine, *store.SQLiteStore, *fakeCronReloader) {
	t.Helper()
	s, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateUser(context.Background(), store.User{
		ID:       ownerID,
		Name:     "Owner",
		Username: "owner",
	}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(context.Background(), store.User{
		ID:      "agent-1",
		OwnerID: ownerID,
		Name:    "Researcher",
		WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	reloader := &fakeCronReloader{}
	h := NewCronHandler(s, s, s, reloader)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", ownerID)
		c.Next()
	})
	r.GET("/api/cron-jobs", h.List)
	r.POST("/api/cron-jobs", h.Create)
	r.PUT("/api/cron-jobs/:id", h.Update)
	r.DELETE("/api/cron-jobs/:id", h.Delete)
	return r, s, reloader
}

func TestCronHandlerCreate(t *testing.T) {
	r, s, reloader := setupCronRouter(t, "owner-1")
	body := bytes.NewBufferString(`{
		"agent_id":"agent-1",
		"model":"claude-sonnet",
		"expression":"0 9 * * 1-5",
		"timezone":"Asia/Shanghai",
		"description":"Weekday build summary",
		"prompt":"Prepare the daily report",
		"enabled":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/cron-jobs", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	jobs, err := s.ListCronJobs(context.Background(), "owner-1")
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].AgentID != "agent-1" ||
		jobs[0].Model != "claude-sonnet" || jobs[0].Description != "Weekday build summary" || !jobs[0].Enabled {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	if reloader.calls != 1 {
		t.Fatalf("reload calls = %d, want 1", reloader.calls)
	}
}

func TestCronHandlerRejectsInvalidSchedule(t *testing.T) {
	r, _, reloader := setupCronRouter(t, "owner-1")
	body := bytes.NewBufferString(`{
		"agent_id":"agent-1",
		"expression":"every morning",
		"timezone":"UTC",
		"prompt":"Prepare the daily report",
		"enabled":true
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/cron-jobs", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid schedule status = %d: %s", rec.Code, rec.Body.String())
	}
	if reloader.calls != 0 {
		t.Fatalf("reload calls = %d, want 0", reloader.calls)
	}
}
