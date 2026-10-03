package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// setupAgentMgmtRouter installs the agent ordering + archive endpoints behind a
// fake auth middleware pinned to authedUID.
func setupAgentMgmtRouter(ms *storetest.Fake, authedUID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	h := NewUserHandler(ms)
	r.GET("/api/users", h.List)
	r.PUT("/api/user-order", h.Reorder)
	r.GET("/api/archived-agents", h.ListArchived)
	r.POST("/api/users/:id/archive", h.Archive)
	r.POST("/api/users/:id/unarchive", h.Unarchive)
	return r
}

func seedHumanWithAgents(t *testing.T, ms *storetest.Fake, humanID string, agentIDs ...string) {
	t.Helper()
	ctx := context.Background()
	if err := ms.CreateUser(ctx, store.User{ID: humanID, Name: "Human", Username: humanID, Email: humanID + "@x"}); err != nil {
		t.Fatalf("create human: %v", err)
	}
	for _, id := range agentIDs {
		if err := ms.CreateUser(ctx, store.User{ID: id, Name: id, OwnerID: humanID, WorkDir: "/home/" + humanID}); err != nil {
			t.Fatalf("create agent %s: %v", id, err)
		}
	}
}

func listIDs(t *testing.T, r *gin.Engine, path string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", path, http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", path, rec.Code, rec.Body.String())
	}
	var users []store.User
	if err := json.NewDecoder(rec.Body).Decode(&users); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	return ids
}

func TestUser_ArchiveHidesFromListAndRestores(t *testing.T) {
	ms := storetest.New()
	seedHumanWithAgents(t, ms, "h1", "a1", "a2")
	r := setupAgentMgmtRouter(ms, "h1")

	// Archive a1.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/api/users/a1/archive", http.NoBody))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("archive: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Sidebar list excludes the archived agent (human + a2 remain).
	got := listIDs(t, r, "/api/users")
	for _, id := range got {
		if id == "a1" {
			t.Fatalf("archived a1 still in /api/users: %v", got)
		}
	}

	// Archived list contains exactly a1.
	archived := listIDs(t, r, "/api/archived-agents")
	if len(archived) != 1 || archived[0] != "a1" {
		t.Fatalf("expected [a1] archived, got %v", archived)
	}

	// Restore a1.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/api/users/a1/unarchive", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("unarchive: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if archived = listIDs(t, r, "/api/archived-agents"); len(archived) != 0 {
		t.Fatalf("expected no archived agents after restore, got %v", archived)
	}
	got = listIDs(t, r, "/api/users")
	found := false
	for _, id := range got {
		if id == "a1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored a1 missing from /api/users: %v", got)
	}
}

func TestUser_ArchiveRejectsHuman(t *testing.T) {
	ms := storetest.New()
	seedHumanWithAgents(t, ms, "h1")
	r := setupAgentMgmtRouter(ms, "h1")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/api/users/h1/archive", http.NoBody))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("archiving a human should be 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUser_ReorderReturnsNewOrder(t *testing.T) {
	ms := storetest.New()
	seedHumanWithAgents(t, ms, "h1", "a1", "a2", "a3")
	r := setupAgentMgmtRouter(ms, "h1")

	body := bytes.NewBufferString(`{"ids":["a3","a1","a2"]}`)
	req := httptest.NewRequest("PUT", "/api/user-order", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var users []store.User
	if err := json.NewDecoder(rec.Body).Decode(&users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Pull out the agents in returned order and check SortOrder was applied.
	order := map[string]int{}
	for _, u := range users {
		if u.Username == "" {
			order[u.ID] = u.SortOrder
		}
	}
	if order["a3"] != 0 || order["a1"] != 1 || order["a2"] != 2 {
		t.Fatalf("unexpected sort orders: %v", order)
	}
}

func TestUser_ReorderPreservesBotPlatforms(t *testing.T) {
	ms := storetest.New()
	seedHumanWithAgents(t, ms, "h1", "a1", "a2")
	if err := ms.CreateBot(context.Background(), store.Bot{
		ID: "b1", AgentID: "a1", Name: "Ops", Platform: "slack",
	}); err != nil {
		t.Fatalf("create bot: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "h1")
		c.Next()
	})
	h := NewUserHandler(ms)
	h.Bots = ms
	r.PUT("/api/user-order", h.Reorder)

	body := bytes.NewBufferString(`{"ids":["a2","a1"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/user-order", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var users []store.User
	if err := json.NewDecoder(rec.Body).Decode(&users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, user := range users {
		if user.ID == "a1" {
			if len(user.BotPlatforms) != 1 || user.BotPlatforms[0] != "slack" {
				t.Fatalf("bot_platforms = %v, want [slack]", user.BotPlatforms)
			}
			return
		}
	}
	t.Fatal("agent a1 missing from reorder response")
}
