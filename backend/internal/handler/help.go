package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// helpDocKey is the app_settings key that backs the admin-configurable help
// popup. The key itself now lives with the use case in the service layer;
// this alias keeps the handler-side name for local readers.
const helpDocKey = service.HelpDocKey

// HelpHandler exposes the admin-configurable help document.
//
//   - GET  /api/help-doc        — any authenticated user. Returns the
//     stored markdown (empty when never set).
//   - PUT  /api/admin/help-doc  — admin only. Replaces the stored
//     markdown. Empty body clears it, which hides the sidebar "?"
//     button for every user.
type HelpHandler struct {
	Store store.Store
}

func NewHelpHandler(s store.Store) *HelpHandler {
	return &HelpHandler{Store: s}
}

// Get returns the currently configured help markdown. An empty string
// is a normal state (no doc configured yet) and the frontend uses it as
// the signal to hide the sidebar help button.
func (h *HelpHandler) Get(c *gin.Context) {
	v, err := service.HelpDoc(c.Request.Context(), h.Store)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"markdown": v})
}

type helpDocRequest struct {
	Markdown string `json:"markdown"`
}

// Put replaces the stored markdown. Body shape: {"markdown": "..."}.
// Storing an empty string is allowed and is how an admin "deletes" the
// help doc — the sidebar button hides itself when the value is empty.
func (h *HelpHandler) Put(c *gin.Context) {
	var req helpDocRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := service.SaveHelpDoc(c.Request.Context(), h.Store, req.Markdown); err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"markdown": req.Markdown})
}
