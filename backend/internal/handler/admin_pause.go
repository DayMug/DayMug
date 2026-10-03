package handler

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// AdminPauseHandler serves the global admin page's "pause new tasks" switch:
// the operator's way to hold traffic still while staging a release, without
// committing to the one-way drain that /upgrade/* starts.
//
// It owns both halves of the toggle because they are one operation: flipping
// the flag back on is only half a resume — the workers that retired during the
// pause have to be re-created from the still-'pending' rows, which is what
// Dispatcher.ResumeAll does.
type AdminPauseHandler struct {
	Pause      *service.PauseGate
	Dispatcher *service.Dispatcher
}

func NewAdminPauseHandler(p *service.PauseGate, d *service.Dispatcher) *AdminPauseHandler {
	return &AdminPauseHandler{Pause: p, Dispatcher: d}
}

// Get serves GET /api/admin/pause.
func (h *AdminPauseHandler) Get(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"paused": h.Pause.Paused()})
}

type adminPauseRequest struct {
	// Pointer so an absent field is a 400 rather than a silent "resume":
	// unpausing by accident is the expensive direction during an upgrade.
	Paused *bool `json:"paused"`
}

// Put serves PUT /api/admin/pause. Responds with the state that is now in
// effect plus, on a resume, how many conversations were handed back to the
// dispatcher — the operator's confirmation that queued work actually restarted.
func (h *AdminPauseHandler) Put(c *gin.Context) {
	var req adminPauseRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Paused == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paused (boolean) is required"})
		return
	}

	changed := h.Pause.SetPaused(*req.Paused)

	resumed := 0
	if changed && !*req.Paused && h.Dispatcher != nil {
		n, err := h.Dispatcher.ResumeAll(c.Request.Context())
		if err != nil {
			// The flag is already off, so work will resume on its own as soon
			// as anything kicks a worker (a new prompt, a cron fire, the next
			// restart). Reporting a 500 here would tell the operator the pause
			// is still on, which is the more misleading failure.
			log.Printf("[admin] resume dispatcher after unpause: %v", err)
		}
		resumed = n
	}
	if changed {
		log.Printf("[admin] new-task pause set to %v by user %s", *req.Paused, middleware.CurrentUserID(c))
	}

	c.JSON(http.StatusOK, gin.H{"paused": *req.Paused, "resumed_conversations": resumed})
}
