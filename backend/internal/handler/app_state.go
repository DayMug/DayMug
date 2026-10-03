package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AppStateHandler bundles the four small read-only payloads the SPA fires on
// every cold load (`/auth/me`, `/users`, `/server-info`, and the active
// agent's `/conversations`) into a single response so the chat surface
// reaches first paint in one round-trip instead of four serial ones.
//
// Notification + work-dir fields stay on the existing /auth/me handler so
// the notifications-settings page (which hydrates after the bootstrap is
// already cached on the client) doesn't have to round-trip through a heavier
// endpoint. /server-info and /help-doc keep their dedicated endpoints too,
// for the same reason — surfaces that aren't part of the cold-start path
// shouldn't be coupled to this aggregate.
type AppStateHandler struct {
	Store         store.Store
	Cfg           *config.Config
	ServerVersion string
}

// NewAppStateHandler binds the aggregate handler to its dependencies. cfg may
// be nil (matches ServerInfoHandler's contract — nil reads as "no sandbox,
// no manifest knobs").
func NewAppStateHandler(s store.Store, cfg *config.Config, version string) *AppStateHandler {
	return &AppStateHandler{Store: s, Cfg: cfg, ServerVersion: version}
}

// Get serves GET /api/app-state. With ?user_id=X the response includes that
// agent's conversation list so the chat shell can paint without a second
// round-trip; without the param `conversations` is null and the client falls
// back to its existing per-user fetch. Authorisation reuses each underlying
// handler's check (Me requires a valid session; conversations re-run the
// owner-access predicate).
func (h *AppStateHandler) Get(c *gin.Context) {
	uid := middleware.CurrentUserID(c)

	// 1. Identity. Hard-fail if the session lookup blows up — the SPA can't
	//    render anything useful without an auth user.
	me, err := h.Store.GetUser(c.Request.Context(), uid)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return
	}

	// 2. Visible users. Literally the same code path as /api/users — the
	//    ownership rule lives once, in service.UserOps.VisibleUsers, so the
	//    two responses cannot drift apart.
	users, err := (&service.UserOps{Store: h.Store, Cfg: h.Cfg}).VisibleUsers(c.Request.Context(), uid)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	decorateBotPlatforms(c.Request.Context(), h.Store, users)

	// 3. Optional conversation preload. The SPA passes the user_id it's
	//    about to focus on (URL param or saved-user). Empty/unauthorised
	//    user_id returns nil so the frontend falls back to the existing
	//    per-user fetch — no need to error, this is a best-effort speedup.
	//    Only the first page ships: this handler runs on every page load, and
	//    the sidebar paints a screenful regardless of how many rows exist. The
	//    client pages the rest in through GET /api/conversations on demand.
	var conversations []store.Conversation
	conversationsHasMore := false
	preloadUserID := c.Query("user_id")
	if preloadUserID != "" && service.CanAccessChatOwner(c.Request.Context(), h.Store, uid, preloadUserID) {
		page, cErr := h.Store.ListConversationsPage(c.Request.Context(), preloadUserID,
			store.ConversationQuery{Limit: store.ConversationPageDefault})
		if cErr == nil {
			conversations = page.Conversations
			if conversations == nil {
				conversations = []store.Conversation{}
			}
			conversationsHasMore = page.HasMore
		}
	}

	// 4. Server info — same shape as /api/server-info. Inlined so the SPA
	//    can prime its slash-command cache without an extra request.
	sandboxEnabled := false
	sandboxType := ""
	if h.Cfg != nil {
		sandboxEnabled = h.Cfg.Sandbox.Enabled
		sandboxType = h.Cfg.Sandbox.Type
	}

	resp := gin.H{
		"auth_user": gin.H{
			"id":                   me.ID,
			"username":             me.Username,
			"name":                 me.Name,
			"is_admin":             me.IsAdmin,
			"bark_url":             me.BarkURL,
			"pushdeer_key":         me.PushDeerKey,
			"notification_channel": me.NotificationChannel,
			"work_dir":             me.WorkDir,
		},
		"users": users,
		"server_info": gin.H{
			"version":         h.ServerVersion,
			"sandbox_enabled": sandboxEnabled,
			"sandbox_type":    sandboxType,
			"manifest_url":    service.DefaultManifestURL,
		},
		"preloaded_user_id":      preloadUserID,
		"conversations":          conversations,
		"conversations_has_more": conversationsHasMore,
	}
	c.JSON(http.StatusOK, resp)
}
