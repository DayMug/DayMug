package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// The summary model is free text, and nothing else on the models page ever
// runs it: a typo (or a model the account can't serve) only surfaces as every
// conversation silently staying untitled, with the reason buried in the server
// log. This check runs one real title through the exact generator auto-title
// uses, on the model typed in the field — saved or not — so the admin sees
// the failure while still looking at the input.
const (
	summaryCheckTimeout = time.Minute
	// Substantive enough that a working model titles it instead of abstaining.
	summaryCheckPrompt = "Help me write a Go function that reverses a UTF-8 string and add unit tests for it."
)

type summaryCheckRequest struct {
	Model string `json:"model"`
}

type summaryCheckResponse struct {
	Account   string `json:"account"`
	Model     string `json:"model"`
	OK        bool   `json:"ok"`
	Title     string `json:"title,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SummaryCheck serves POST /api/admin/providers/:name/summary-check.
func (h *AdminAccountCheckHandler) SummaryCheck(c *gin.Context) {
	name := c.Param("name")
	var acc *config.Provider
	if h.Cfg != nil {
		acc = h.Cfg.FindAccount(name)
	}
	if acc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown account " + name})
		return
	}
	var body summaryCheckRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	model := service.CanonicalModel(strings.TrimSpace(body.Model))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}
	// Shares the account-check guard: both spawn a CLI under the same
	// credentials, and double-clicking either shouldn't fan out processes.
	if !h.begin(acc.Name) {
		c.JSON(http.StatusConflict, gin.H{"error": "a check for account " + acc.Name + " is already running"})
		return
	}
	defer h.end(acc.Name)

	resp := summaryCheckResponse{Account: acc.Name, Model: model}
	registered, _ := h.Backends.Lookup(acc.Type)
	if agent.Resolve(registered) == nil {
		resp.Error = "no backend registered for provider type " + acc.Type
		c.JSON(http.StatusOK, resp)
		return
	}
	if h.Pool != nil {
		release, err := h.Pool.EnterLive(acc.Name)
		if err != nil {
			resp.Error = err.Error()
			c.JSON(http.StatusOK, resp)
			return
		}
		defer release()
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), summaryCheckTimeout)
	defer cancel()
	start := time.Now()
	title, err := titleGeneratorFor(registered, model).GenerateTitle(ctx,
		[]string{summaryCheckPrompt},
		agent.TitleAccount{ConfigDir: acc.ConfigDir, Env: acc.Env})
	resp.LatencyMS = time.Since(start).Milliseconds()
	switch {
	case err != nil:
		resp.Error = err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			resp.Error = "no reply within " + summaryCheckTimeout.String() + ": " + resp.Error
		}
	case title == "":
		resp.Error = "the model returned no title"
	default:
		resp.OK = true
		resp.Title = title
	}
	c.JSON(http.StatusOK, resp)
}
