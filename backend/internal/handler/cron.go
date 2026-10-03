package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

type CronHandler struct {
	Store     store.Store
	Jobs      store.CronStore
	Bots      store.BotStore
	Scheduler service.CronReloader
}

type cronJobRequest struct {
	AgentID              string `json:"agent_id"`
	Model                string `json:"model"`
	Expression           string `json:"expression"`
	Timezone             string `json:"timezone"`
	Description          string `json:"description"`
	Prompt               string `json:"prompt"`
	Enabled              bool   `json:"enabled"`
	NotificationsEnabled *bool  `json:"notifications_enabled"`
	DeliverToBot         bool   `json:"deliver_to_bot"`
	BotID                string `json:"bot_id"`
}

// params projects the request body onto the service-side field set, leaving the
// json tags as the single description of the wire format.
func (r cronJobRequest) params() service.CronJobParams {
	notificationsEnabled := true
	if r.NotificationsEnabled != nil {
		notificationsEnabled = *r.NotificationsEnabled
	}
	return service.CronJobParams{
		AgentID:              r.AgentID,
		Model:                r.Model,
		Expression:           r.Expression,
		Timezone:             r.Timezone,
		Description:          r.Description,
		Prompt:               r.Prompt,
		Enabled:              r.Enabled,
		NotificationsEnabled: notificationsEnabled,
		DeliverToBot:         r.DeliverToBot,
		BotID:                r.BotID,
	}
}

func NewCronHandler(s store.Store, jobs store.CronStore, bots store.BotStore, scheduler service.CronReloader) *CronHandler {
	return &CronHandler{Store: s, Jobs: jobs, Bots: bots, Scheduler: scheduler}
}

// ops assembles a service.CronOps from the handler's current fields. Built
// lazily per call for the same reason as the other handlers' factories.
func (h *CronHandler) ops() *service.CronOps {
	return &service.CronOps{Store: h.Store, Jobs: h.Jobs, Bots: h.Bots, Scheduler: h.Scheduler}
}

// List is a thin read: the rows come back as-is, with the advisory next-run
// time computed per row for display.
func (h *CronHandler) List(c *gin.Context) {
	ownerID := middleware.CurrentUserID(c)
	jobs, err := h.Jobs.ListCronJobs(c.Request.Context(), ownerID)
	if err != nil {
		respondInternalError(c, "CronHandler.List", err)
		return
	}
	now := time.Now()
	for i := range jobs {
		if !jobs[i].Enabled {
			continue
		}
		next, err := service.NextCronRun(jobs[i].Expression, jobs[i].Timezone, now)
		if err != nil {
			continue
		}
		jobs[i].NextRunAt = &next
	}
	c.JSON(http.StatusOK, jobs)
}

func (h *CronHandler) Create(c *gin.Context) {
	var req cronJobRequest
	if !bindJSON(c, &req) {
		return
	}
	created, err := h.ops().Create(c.Request.Context(), middleware.CurrentUserID(c), req.params())
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (h *CronHandler) Update(c *gin.Context) {
	var req cronJobRequest
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().Update(c.Request.Context(), middleware.CurrentUserID(c), c.Param("id"), req.params())
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *CronHandler) Delete(c *gin.Context) {
	if err := h.ops().Delete(c.Request.Context(), middleware.CurrentUserID(c), c.Param("id")); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
