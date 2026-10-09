package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
)

// The /compact orchestration that used to live in this file (capability gate,
// busy guard, run-options build, the one-shot summary run, summary
// persistence and session rotation) moved to service.PromptRunner.Compact
// (internal/service/compact.go). What is left here is the HTTP adapter:
// unwrap the route params and the auth context, delegate, render.

// Compact serves POST /api/conversations/:id/compact. It asks the
// conversation's backend for a summary of the conversation so far, persists it
// as a regular assistant message, then rotates the conversation's session id so
// the next message starts fresh. See service.PromptRunner.Compact for the
// contract — notably that the summary run is deliberately NOT bound to this
// request's context.
func (h *TerminalHandler) Compact(c *gin.Context) {
	res, err := h.PromptRunner().Compact(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}
