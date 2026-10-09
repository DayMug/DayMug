package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// ConversationHandler is the HTTP adapter for the conversation surface. The
// write path (create, the field updaters, session rotation, share toggles,
// delete, clear) lives in service.ConversationOps; what remains here is
// unwrapping route params / JSON bodies and rendering the result. The read
// path (list, get, messages) is still store-direct.
type ConversationHandler struct {
	Store store.Store
	// Broadcaster is optional. When set, ClearContext also wipes the
	// in-memory context_usage cache and the active replay buffer so peer
	// tabs already connected to the same conversation pick up the reset
	// at their next Join. Tests that don't exercise WS state can leave
	// it nil.
	Broadcaster *service.Broadcaster
	// UserHub is optional. When set, conversation lifecycle changes
	// (create / delete / rename / notification toggle / work_dir
	// update) fan out to every WebSocket the owning user has open so
	// peer tabs reflect the new state in real time. Tests that don't
	// exercise WS state can leave it nil.
	UserHub *service.UserHub
	// Drainer is optional; it names the caller's running conversations in
	// the attention feed.
	Drainer *service.Drainer
	// Cfg supplies the server's default provider (CLIType) for new
	// conversations whose caller didn't specify one. Optional in tests;
	// when nil the create handler falls back to the claude provider so
	// the in-memory test fixtures still validate.
	Cfg *config.Config
}

// ops builds the service-layer use-case struct for this request. Assembled on
// every call rather than cached at construction time on purpose: routes.go
// back-fills Broadcaster and UserHub onto the handler *after*
// NewConversationHandler runs (they only exist once the terminal handler is
// built), so a snapshot taken in the constructor would pin nil forever.
func (h *ConversationHandler) ops() *service.ConversationOps {
	return &service.ConversationOps{
		Store:       h.Store,
		Cfg:         h.Cfg,
		Broadcaster: h.Broadcaster,
		UserHub:     h.UserHub,
	}
}

func NewConversationHandler(s store.Store) *ConversationHandler {
	return &ConversationHandler{Store: s}
}

// requireChatOwner gates the collection-level endpoints, which name their
// owner in the query string / body instead of inheriting it from a loaded
// row. The rule itself lives in service.ConversationOps.RequireChatOwner.
func (h *ConversationHandler) requireChatOwner(c *gin.Context, userID string) bool {
	if err := h.ops().RequireChatOwner(c.Request.Context(), middleware.CurrentUserID(c), userID); err != nil {
		respondServiceError(c, err)
		return false
	}
	return true
}

func (h *ConversationHandler) List(c *gin.Context) {
	userID := c.Query("user_id")
	if !h.requireChatOwner(c, userID) {
		return
	}

	// The response stays a bare array on every path so the existing clients
	// keep working. Callers that page pass limit/offset and learn there is
	// another page from a full-length result, which is why the window is
	// clamped here rather than silently widened.
	query := store.ConversationQuery{
		Limit:  atoiOrZero(c.Query("limit")),
		Offset: atoiOrZero(c.Query("offset")),
	}
	page, err := h.Store.ListConversationsPage(c.Request.Context(), userID, query)
	if err != nil {
		respondInternalError(c, "ConversationHandler.List", err)
		return
	}
	convs := page.Conversations
	if convs == nil {
		convs = []store.Conversation{}
	}
	c.JSON(http.StatusOK, convs)
}

// atoiOrZero maps absent and unparseable alike to 0, which every consumer
// treats as "unset" and clamps to its own default.
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// createConversationRequest stays in the transport layer: the json tags and
// binding directives describe the wire contract, and the service takes a
// tag-free service.CreateConversationParams instead so it never has to know
// about HTTP.
type createConversationRequest struct {
	Title    string `json:"title"`
	UserID   string `json:"user_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// ThinkLevel omitted inherits the Agent's default-model profile. Empty is an
	// explicit request to use the provider default.
	ThinkLevel *string `json:"think_level"`
	// Account pins the conversation to a specific provider account within its
	// CLI type. Empty = the user's default account for that type. Must be one
	// of the user's allowed accounts for the resolved provider, else 400.
	Account string `json:"account"`
}

func (h *ConversationHandler) Create(c *gin.Context) {
	var req createConversationRequest
	if !bindJSON(c, &req) {
		return
	}
	conv, err := h.ops().Create(c.Request.Context(), middleware.CurrentUserID(c), service.CreateConversationParams{
		Title:      req.Title,
		UserID:     req.UserID,
		Provider:   req.Provider,
		Model:      req.Model,
		ThinkLevel: req.ThinkLevel,
		Account:    req.Account,
	})
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, conv)
}

func (h *ConversationHandler) Get(c *gin.Context) {
	conv, ok := h.requireConv(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, conv)
}

func (h *ConversationHandler) UpdateTitle(c *gin.Context) {
	var req struct {
		Title string `json:"title"`
	}
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().UpdateTitle(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c), req.Title)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// UpdatePinned flips the pinned-to-top flag on a conversation.
func (h *ConversationHandler) UpdatePinned(c *gin.Context) {
	var req struct {
		Pinned bool `json:"pinned"`
	}
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().UpdatePinned(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c), req.Pinned)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *ConversationHandler) Share(c *gin.Context) {
	shared, err := h.ops().Share(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"conversation": shared,
		"token":        shared.ShareToken,
		"url":          "/share/conversation/" + shared.ShareToken,
	})
}

func (h *ConversationHandler) Unshare(c *gin.Context) {
	updated, err := h.ops().Unshare(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// ReorderPinned sets the manual order of a user's pinned conversations from a
// drag-to-reorder gesture. The body carries the owner's user_id and the full
// ordered list of pinned conversation ids.
func (h *ConversationHandler) ReorderPinned(c *gin.Context) {
	var req struct {
		UserID string   `json:"user_id"`
		IDs    []string `json:"ids"`
	}
	if !bindJSON(c, &req) {
		return
	}
	convs, err := h.ops().ReorderPinned(c.Request.Context(), middleware.CurrentUserID(c), req.UserID, req.IDs)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, convs)
}

func (h *ConversationHandler) UpdateNotifications(c *gin.Context) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().UpdateNotifications(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c), req.Enabled)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// attentionResponse pairs the persisted "needs you" rows with the titles of
// the caller's conversations that are running right now. The activity
// snapshot is broadcast to everyone and deliberately carries no titles, so
// the task feed names live work from here instead.
type attentionResponse struct {
	Items  []store.ConversationAttention `json:"items"`
	Titles map[string]string             `json:"titles"`
}

// ListAttention returns the caller's conversations that finished, failed or
// stopped on a question and have not been opened since.
func (h *ConversationHandler) ListAttention(c *gin.Context) {
	ctx := c.Request.Context()
	uid := middleware.CurrentUserID(c)
	rows, err := h.Store.ListConversationAttention(ctx, uid)
	if err != nil {
		respondInternalError(c, "ConversationHandler.ListAttention", err)
		return
	}
	titles := map[string]string{}
	jobs := append(h.Drainer.Jobs(), h.Drainer.ResidentActivities()...)
	for _, job := range jobs {
		if job.ConversationID == "" {
			continue
		}
		if _, seen := titles[job.ConversationID]; seen {
			continue
		}
		conv, err := h.Store.GetConversation(ctx, job.ConversationID)
		if err != nil {
			continue
		}
		if agent, err := h.Store.GetUser(ctx, conv.UserID); err == nil && agent.OwnerID == uid {
			titles[conv.ID] = conv.Title
		}
	}
	c.JSON(http.StatusOK, attentionResponse{Items: rows, Titles: titles})
}

// MarkRead clears the conversation's attention flag. See
// service.ConversationOps.MarkRead.
func (h *ConversationHandler) MarkRead(c *gin.Context) {
	if err := h.ops().MarkRead(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c)); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// UpdateModel changes the provider+model pair on a conversation. See
// service.ConversationOps.UpdateModel for the mutability rules.
func (h *ConversationHandler) UpdateModel(c *gin.Context) {
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		// Account, when present, re-pins (or clears, when "") the account.
		// Omitted (nil) leaves the pin untouched — unless the provider type
		// changes, which always clears the now-stale pin.
		Account *string `json:"account"`
		// ThinkLevel follows the same optional-update semantics: omitted keeps
		// the current value, empty clears the override.
		ThinkLevel *string `json:"think_level"`
	}
	if !bindJSON(c, &req) {
		return
	}
	updated, err := h.ops().UpdateModel(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c), req.Provider, req.Model, req.Account, req.ThinkLevel)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

// ClearContext rotates the conversation's session_id and wipes any cached
// token usage. See service.ConversationOps.ResetSession.
func (h *ConversationHandler) ClearContext(c *gin.Context) {
	updated, err := h.ops().ResetSession(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (h *ConversationHandler) Delete(c *gin.Context) {
	if err := h.ops().Delete(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c)); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *ConversationHandler) DeleteStale(c *gin.Context) {
	ids, err := h.ops().DeleteStale(c.Request.Context(), c.Query("user_id"), middleware.CurrentUserID(c), 14*24*time.Hour)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted_ids": ids, "deleted_count": len(ids)})
}

func (h *ConversationHandler) ClearMessages(c *gin.Context) {
	if err := h.ops().ClearMessages(c.Request.Context(), c.Param("id"), middleware.CurrentUserID(c)); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *ConversationHandler) GetMessages(c *gin.Context) {
	conv, ok := h.requireConv(c)
	if !ok {
		return
	}

	// Disable browser caching of message lists. Without this, iOS Safari
	// (and Chromium under heuristic freshness) can serve a stale snapshot
	// after a refresh, hiding messages that were just persisted — exactly
	// the failure mode where users see a Bark notification arrive but
	// the corresponding assistant reply never appears in the chat
	// surface. The endpoint is cheap to recompute, so the cost of always
	// revalidating is negligible compared to the cost of losing data.
	c.Header("Cache-Control", "no-store, must-revalidate")
	c.Header("Pragma", "no-cache")

	limit, _ := strconv.Atoi(c.Query("limit"))
	writeMessages := func(msgs []store.Message) {
		if msgs == nil {
			msgs = []store.Message{}
		}
		c.JSON(http.StatusOK, rebaseMessageArtifacts(c.Request.Context(), h.Store, conv, msgs))
	}

	// `before_id` opts in to the reverse-pagination path that the chat UI
	// uses for lazy-loading: an empty value (or one whose row no longer
	// exists) returns the latest `limit` messages; a populated value
	// returns the page strictly older than that cursor. We detect opt-in
	// via GetQuery so the empty-cursor case (initial page load) is
	// distinguishable from "param omitted entirely", which returns the
	// conversation from the start.
	if beforeID, useReverse := c.GetQuery("before_id"); useReverse {
		msgs, err := h.Store.ListMessagesBefore(c.Request.Context(), conv.ID, beforeID, limit)
		if err != nil {
			respondInternalError(c, "ConversationHandler.GetMessages", err)
			return
		}
		writeMessages(msgs)
		return
	}

	// `after_id` is the symmetric forward-incremental path used by the
	// reconnect / focus-resume flow on the client. Asks for every
	// message strictly newer than the supplied cursor so the client
	// can do a confident catch-up sync without rebuilding its full
	// view. Empty cursor returns every message — same recovery
	// semantics as the WS history_backfill path.
	if afterID, useForward := c.GetQuery("after_id"); useForward {
		msgs, err := h.Store.ListMessagesAfter(c.Request.Context(), conv.ID, afterID, limit)
		if err != nil {
			respondInternalError(c, "ConversationHandler.GetMessages", err)
			return
		}
		writeMessages(msgs)
		return
	}

	msgs, err := h.Store.ListMessages(c.Request.Context(), conv.ID, limit, 0)
	if err != nil {
		respondInternalError(c, "ConversationHandler.GetMessages", err)
		return
	}
	writeMessages(msgs)
}

func rebaseMessageArtifacts(ctx context.Context, s store.Store, conv store.Conversation, msgs []store.Message) []store.Message {
	if s == nil || conv.WorkDir == "" || len(msgs) == 0 {
		return msgs
	}
	owner, err := s.GetUser(ctx, conv.UserID)
	if err != nil || owner.WorkDir == "" || owner.WorkDir == conv.WorkDir {
		return msgs
	}
	for i := range msgs {
		msgs[i].Metadata = service.RebaseArtifactMetadata(msgs[i].Metadata, conv.WorkDir, owner.WorkDir, conv.UserID)
	}
	return msgs
}

func (h *ConversationHandler) GetShared(c *gin.Context) {
	conv, msgs, err := h.ops().GetShared(c.Request.Context(), c.Param("token"))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"conversation": conv,
		"messages":     msgs,
	})
}

// requireConv reads the :id path param, loads the conversation, and verifies
// the authenticated caller owns it. On any failure it writes the appropriate
// JSON response and returns ok=false so the caller can return immediately.
// Only the read endpoints use it now — the write path does the same check
// inside service.ConversationOps.
func (h *ConversationHandler) requireConv(c *gin.Context) (store.Conversation, bool) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing conversation id"})
		return store.Conversation{}, false
	}
	return loadOwnedConversation(c, h.Store, id)
}
