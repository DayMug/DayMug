package handler

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// UsageHandler exposes per-day token usage to the settings UI. There are
// two routes:
//
//   - GET /api/usage/me: the caller's own usage. Always scoped to the
//     authenticated user_id; the user_id query parameter is rejected so a
//     non-admin can't widen the scope to someone else.
//   - GET /api/admin/usage: the admin-only variant. Honours user_id and
//     model query parameters as filters. An empty user_id returns rows
//     for every user, which the admin UI uses to drive the per-user
//     breakdown chart.
//
// Loc is the operator-configured timezone used to interpret naked dates
// in start/end query parameters and to compute the default 30-day
// window. Defaults to UTC when nil — keeps tests and any caller that
// constructs the handler directly working without extra wiring.
type UsageHandler struct {
	Store      store.Store
	Loc        *time.Location
	Thresholds store.UsageThresholds
}

func NewUsageHandler(s store.Store) *UsageHandler {
	return &UsageHandler{
		Store: s, Loc: time.UTC,
		Thresholds: store.UsageThresholds{
			CacheReadRatio: 0.95, ModelRequests: 20, ToolCalls: 30,
			ContextWarningRatio: 0.8, ConversationCostUSD: 10,
		},
	}
}

// AdminInsights returns event-level usage observability. Unlike AdminAll it
// never infers tasks from token_usage.turns: instructions, provider requests,
// and tool calls are independent counters, and pre-event-ledger data is
// returned only as rows marked historical_estimate.
func (h *UsageHandler) AdminInsights(c *gin.Context) {
	h.insights(c, c.Query("user_id"), "")
}

// MineInsights exposes the same event ledger while forcing ownership to the
// signed-in human. Query-string user_id is deliberately ignored.
func (h *UsageHandler) MineInsights(c *gin.Context) {
	ownerID := middleware.CurrentUserID(c)
	if ownerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	h.insights(c, "", ownerID)
}

func (h *UsageHandler) insights(c *gin.Context, ownerID, scopeOwnerID string) {
	loc := h.location()
	start, end, err := parseUsageRange(c.Query("start"), c.Query("end"), loc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	page, err := positiveIntQuery(c.Query("page"), 1)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "page must be a positive integer"})
		return
	}
	pageSize, err := positiveIntQuery(c.Query("page_size"), 20)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "page_size must be a positive integer"})
		return
	}
	result, err := h.Store.QueryUsageInsights(c.Request.Context(), store.UsageInsightsQuery{
		Start: start, End: end, OwnerID: ownerID, ScopeOwnerID: scopeOwnerID, AgentID: c.Query("agent_id"),
		Provider: c.Query("provider"), Model: c.Query("model"),
		ConversationID: c.Query("conversation_id"), SourceType: c.Query("source_type"),
		GroupBy: c.Query("group_by"), RankBy: c.Query("rank_by"),
		SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order"),
		Page: page, PageSize: pageSize,
	}, h.Thresholds)
	if err != nil {
		respondInternalError(c, "UsageHandler.insights", err)
		return
	}
	result.Timezone = loc.String()
	c.JSON(http.StatusOK, result)
}

func positiveIntQuery(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, strconv.ErrSyntax
	}
	return v, nil
}

// usageResponse bundles the daily rows + the distinct model list so the
// admin UI's filter dropdown doesn't need a second round-trip on first
// load. The user-scoped variant returns only the models that user has
// actually used (any model that appears in the row set). Timezone is the
// IANA name (or "UTC") so the frontend's chart and date inputs label the
// day boundary correctly.
type usageResponse struct {
	Rows     []store.TokenUsageRecord `json:"rows"`
	Models   []string                 `json:"models"`
	Timezone string                   `json:"timezone"`
}

// Mine returns the authenticated caller's daily usage. Date range
// defaults to the last 30 days (in the configured timezone) when neither
// `start` nor `end` query params are supplied. Models seen in the result
// drive the model-filter dropdown for non-admins, so we always derive
// `models` from the row set rather than calling ListTokenUsageModels
// (which would leak names of models other users have used).
//
// token_usage rows are keyed by whichever User row ran the turn — that's
// usually one of the caller's agents, not the human caller themselves —
// so we expand the filter to "the caller + everyone whose owner is the
// caller" before querying. Without the expansion a human who only chats
// through agents sees an empty chart even when they have plenty of usage.
func (h *UsageHandler) Mine(c *gin.Context) {
	userID := middleware.CurrentUserID(c)
	if userID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "auth required"})
		return
	}
	loc := h.location()
	start, end, err := parseUsageRange(c.Query("start"), c.Query("end"), loc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ownerByUserID, err := h.buildOwnerMap(c.Request.Context())
	if err != nil {
		respondInternalError(c, "UsageHandler.Mine", err)
		return
	}
	q := store.TokenUsageQuery{
		UserIDs:  expandToOwnerSiblings(userID, ownerByUserID),
		Model:    c.Query("model"),
		StartUTC: start,
		EndUTC:   end,
	}
	rows, err := h.Store.AggregateTokenUsage(c.Request.Context(), q)
	if err != nil {
		respondInternalError(c, "UsageHandler.Mine", err)
		return
	}
	annotateOwners(rows, ownerByUserID)
	c.JSON(http.StatusOK, usageResponse{
		Rows:     rows,
		Models:   distinctModels(rows),
		Timezone: loc.String(),
	})
}

// AdminAll returns daily usage across users, optionally filtered by
// user_id and/or model. Admin-only — wired under /api/admin/* in
// routes.go so RequireAdmin gates access. The model dropdown is
// populated from ListTokenUsageModels (every model ever recorded) so
// admins can pick a model that hasn't been used in the current range.
//
// `user_id` is interpreted as a human-user filter: when supplied it
// expands to the human's id plus every agent that resolves to that
// human as owner. token_usage rows are keyed by the agent that ran the
// turn, so a literal id-equality filter would only ever return rows
// when the admin happened to chat directly without an agent. Each
// returned row also carries `owner_id` so the admin chart can stack-by
// human user without exposing agent uuids.
func (h *UsageHandler) AdminAll(c *gin.Context) {
	loc := h.location()
	start, end, err := parseUsageRange(c.Query("start"), c.Query("end"), loc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ownerByUserID, err := h.buildOwnerMap(c.Request.Context())
	if err != nil {
		respondInternalError(c, "UsageHandler.AdminAll", err)
		return
	}
	q := store.TokenUsageQuery{
		Model:    c.Query("model"),
		StartUTC: start,
		EndUTC:   end,
	}
	if filter := c.Query("user_id"); filter != "" {
		q.UserIDs = expandToOwnerSiblings(filter, ownerByUserID)
	}
	rows, err := h.Store.AggregateTokenUsage(c.Request.Context(), q)
	if err != nil {
		respondInternalError(c, "UsageHandler.AdminAll", err)
		return
	}
	annotateOwners(rows, ownerByUserID)
	models, err := h.Store.ListTokenUsageModels(c.Request.Context())
	if err != nil {
		respondInternalError(c, "UsageHandler.AdminAll", err)
		return
	}
	c.JSON(http.StatusOK, usageResponse{
		Rows:     rows,
		Models:   models,
		Timezone: loc.String(),
	})
}

// buildOwnerMap builds a userID → ownerID lookup for every active user
// row. Humans (rows with a non-empty username) own themselves; agents
// (empty username) resolve through their owner_id pointer — the same
// invariant Store.GetOwner relies on. Rows whose owner can't be resolved
// (orphan agents with an empty owner_id) are simply absent from the map;
// callers fall back to the agent id when a row is missed.
//
// Built once per request rather than per-row so the chart doesn't pay
// O(rows) GetOwner round-trips on a 30-day window.
func (h *UsageHandler) buildOwnerMap(ctx context.Context) (map[string]string, error) {
	users, err := h.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string, len(users))
	for _, u := range users {
		if o := u.Owner(); o != "" {
			owners[u.ID] = o
		}
	}
	return owners, nil
}

// expandToOwnerSiblings turns a single user-id filter into the set of
// user ids that belong to the same human owner. The supplied id can be
// either the human itself or one of their agents — both shapes resolve
// to the same expanded set. Includes the original id even when it has
// no owner mapping so an unknown id still returns rows that happen to
// match it directly (defensive — keeps the filter from silently
// widening to "everyone").
func expandToOwnerSiblings(userID string, ownerByUserID map[string]string) []string {
	owner, ok := ownerByUserID[userID]
	if !ok {
		return []string{userID}
	}
	out := []string{owner}
	if userID != owner {
		out = append(out, userID)
	}
	for id, oid := range ownerByUserID {
		if oid != owner {
			continue
		}
		if id == owner || id == userID {
			continue
		}
		out = append(out, id)
	}
	return out
}

// annotateOwners stamps each row's OwnerID using the supplied map.
// Rows without a known owner fall back to UserID so the UI never gets
// an empty group key.
func annotateOwners(rows []store.TokenUsageRecord, ownerByUserID map[string]string) {
	for i := range rows {
		if owner, ok := ownerByUserID[rows[i].UserID]; ok {
			rows[i].OwnerID = owner
		} else {
			rows[i].OwnerID = rows[i].UserID
		}
	}
}

// location returns h.Loc with a UTC fallback so callers don't need to
// nil-check on every request.
func (h *UsageHandler) location() *time.Location {
	if h.Loc != nil {
		return h.Loc
	}
	return time.UTC
}

// distinctModels collects the unique model names present in rows in the
// order of first appearance. Used by the per-user endpoint so a non-admin
// only sees the models attached to their own usage.
func distinctModels(rows []store.TokenUsageRecord) []string {
	seen := make(map[string]struct{}, len(rows))
	out := make([]string, 0)
	for _, r := range rows {
		if _, ok := seen[r.Model]; ok {
			continue
		}
		seen[r.Model] = struct{}{}
		out = append(out, r.Model)
	}
	return out
}

// parseUsageRange validates the start/end query parameters. Both must be
// YYYY-MM-DD if supplied; an empty value falls back to a 30-day window
// ending "today" in the supplied timezone. End is inclusive.
//
// The returned strings are still YYYY-MM-DD — they're matched against
// the `day` column of token_usage rows (also stored in the configured
// timezone), so no further conversion happens at the SQL layer.
func parseUsageRange(startQ, endQ string, loc *time.Location) (string, string, error) {
	if loc == nil {
		loc = time.UTC
	}
	const layout = "2006-01-02"
	var (
		start, end time.Time
		err        error
	)
	if endQ == "" {
		end = time.Now().In(loc)
	} else {
		end, err = time.ParseInLocation(layout, endQ, loc)
		if err != nil {
			return "", "", err
		}
	}
	if startQ == "" {
		start = end.AddDate(0, 0, -29)
	} else {
		start, err = time.ParseInLocation(layout, startQ, loc)
		if err != nil {
			return "", "", err
		}
	}
	if start.After(end) {
		start, end = end, start
	}
	return start.Format(layout), end.Format(layout), nil
}
