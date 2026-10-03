package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupUsageRouter(t *testing.T, ms *storetest.Fake, callerID string, isAdmin bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", callerID)
		c.Set("auth_is_admin", isAdmin)
		c.Next()
	})
	h := NewUsageHandler(ms)
	r.GET("/api/usage/me", h.Mine)
	r.GET("/api/admin/usage", h.AdminAll)
	return r
}

func TestUsage_Mine_FiltersToCaller(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice", Model: "claude-opus-4-8", InputTokens: 100, OutputTokens: 50})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "bob", Model: "claude-opus-4-8", InputTokens: 999, OutputTokens: 999})

	r := setupUsageRouter(t, ms, "alice", false)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/me", http.NoBody)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("expected 1 row scoped to alice, got %d (%v)", len(resp.Rows), resp.Rows)
	}
	if resp.Rows[0].UserID != "alice" {
		t.Errorf("expected user_id=alice, got %s", resp.Rows[0].UserID)
	}
	if len(resp.Models) != 1 || resp.Models[0] != "claude-opus-4-8" {
		t.Errorf("expected models=[claude-opus-4-8], got %v", resp.Models)
	}
}

func TestUsage_Mine_AppliesModelFilter(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice", Model: "claude-opus-4-8", InputTokens: 100})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice", Model: "claude-haiku-4-5", InputTokens: 50})

	r := setupUsageRouter(t, ms, "alice", false)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/me?model=claude-haiku-4-5", http.NoBody)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rows) != 1 || resp.Rows[0].Model != "claude-haiku-4-5" {
		t.Errorf("expected only haiku row, got %v", resp.Rows)
	}
}

func TestUsage_AdminAll_ReturnsEveryUser(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice", Model: "claude-opus-4-8", InputTokens: 100})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "bob", Model: "claude-haiku-4-5", InputTokens: 50})

	r := setupUsageRouter(t, ms, "admin", true)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage", http.NoBody)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rows) != 2 {
		t.Errorf("expected 2 rows for both users, got %d", len(resp.Rows))
	}
	if len(resp.Models) != 2 {
		t.Errorf("expected 2 distinct models, got %v", resp.Models)
	}
}

func TestUsage_AdminAll_FilterByUser(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice", Model: "claude-opus-4-8", InputTokens: 100})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "bob", Model: "claude-haiku-4-5", InputTokens: 50})

	r := setupUsageRouter(t, ms, "admin", true)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage?user_id=bob", http.NoBody)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rows) != 1 || resp.Rows[0].UserID != "bob" {
		t.Errorf("expected only bob's row, got %v", resp.Rows)
	}
}

func TestUsage_BadDateRange(t *testing.T) {
	ms := storetest.New()
	r := setupUsageRouter(t, ms, "alice", false)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/me?start=not-a-date", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad date, got %d", w.Code)
	}
}

// When an admin filters by a human user_id, the handler should expand
// the filter to that human's id plus every agent that owns the human as
// owner (i.e. shares the same email). Each row also gets owner_id
// stamped to the human's id so the chart can stack-by human user.
func TestUsage_AdminAll_ExpandsHumanFilterToAgents(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, store.User{ID: "alice", Username: "alice", Email: "alice@example.com"})
	_ = ms.CreateUser(ctx, store.User{ID: "alice-agent-1", Username: "", Email: "alice@example.com", OwnerID: "alice"})
	_ = ms.CreateUser(ctx, store.User{ID: "alice-agent-2", Username: "", Email: "alice@example.com", OwnerID: "alice"})
	_ = ms.CreateUser(ctx, store.User{ID: "bob", Username: "bob", Email: "bob@example.com"})
	_ = ms.CreateUser(ctx, store.User{ID: "bob-agent", Username: "", Email: "bob@example.com", OwnerID: "bob"})

	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice-agent-1", Model: "claude-opus-4-8", InputTokens: 100})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice-agent-2", Model: "claude-opus-4-8", InputTokens: 200})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "bob-agent", Model: "claude-opus-4-8", InputTokens: 999})

	r := setupUsageRouter(t, ms, "admin", true)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/usage?user_id=alice", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rows) != 2 {
		t.Fatalf("expected 2 rows for alice's two agents, got %d (%v)", len(resp.Rows), resp.Rows)
	}
	for _, row := range resp.Rows {
		if row.OwnerID != "alice" {
			t.Errorf("row %s owner_id = %q; want alice", row.UserID, row.OwnerID)
		}
	}
}

// Mine should return the caller's own rows plus every row from agents
// owned by the caller — without the expansion a human who only chats
// through agents would see an empty chart.
func TestUsage_Mine_ExpandsToOwnedAgents(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, store.User{ID: "alice", Username: "alice", Email: "alice@example.com"})
	_ = ms.CreateUser(ctx, store.User{ID: "alice-agent", Username: "", Email: "alice@example.com", OwnerID: "alice"})
	_ = ms.CreateUser(ctx, store.User{ID: "bob", Username: "bob", Email: "bob@example.com"})

	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "alice-agent", Model: "claude-opus-4-8", InputTokens: 100})
	_ = ms.AddTokenUsage(ctx, store.TokenUsageDelta{UserID: "bob", Model: "claude-opus-4-8", InputTokens: 999})

	r := setupUsageRouter(t, ms, "alice", false)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/me", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Rows) != 1 {
		t.Fatalf("expected 1 row scoped to alice's agent, got %d (%v)", len(resp.Rows), resp.Rows)
	}
	if resp.Rows[0].UserID != "alice-agent" || resp.Rows[0].OwnerID != "alice" {
		t.Errorf("expected alice-agent / owner alice, got %+v", resp.Rows[0])
	}
}

func TestUsage_TimezoneInResponse(t *testing.T) {
	ms := storetest.New()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Asia/Shanghai: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "alice")
		c.Set("auth_is_admin", false)
		c.Next()
	})
	h := NewUsageHandler(ms)
	h.Loc = loc
	r.GET("/api/usage/me", h.Mine)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/usage/me", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var resp usageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Timezone != "Asia/Shanghai" {
		t.Errorf("expected timezone=Asia/Shanghai in response, got %q", resp.Timezone)
	}
}

type usageInsightsCapture struct {
	*storetest.Fake
	query store.UsageInsightsQuery
}

func (s *usageInsightsCapture) QueryUsageInsights(_ context.Context, query store.UsageInsightsQuery, _ store.UsageThresholds) (store.UsageInsightsResponse, error) {
	s.query = query
	return store.UsageInsightsResponse{Page: query.Page, PageSize: query.PageSize}, nil
}

func TestUsageInsightsParsesFiltersAndScopesMineToCaller(t *testing.T) {
	ms := &usageInsightsCapture{Fake: storetest.New()}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "alice")
		c.Next()
	})
	h := NewUsageHandler(ms)
	r.GET("/mine", h.MineInsights)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mine?start=2026-09-01&end=2026-09-24&user_id=bob&agent_id=agent-1&provider=claude&model=opus&conversation_id=conv-1&source_type=cron&group_by=agent&rank_by=cron&sort_by=tool_calls&sort_order=asc&page=2&page_size=10", http.NoBody)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	if ms.query.ScopeOwnerID != "alice" || ms.query.OwnerID != "" || ms.query.AgentID != "agent-1" || ms.query.Provider != "claude" ||
		ms.query.Model != "opus" || ms.query.ConversationID != "conv-1" || ms.query.SourceType != "cron" ||
		ms.query.GroupBy != "agent" || ms.query.RankBy != "cron" || ms.query.SortBy != "tool_calls" ||
		ms.query.SortOrder != "asc" || ms.query.Page != 2 || ms.query.PageSize != 10 {
		t.Fatalf("query = %+v", ms.query)
	}
}
