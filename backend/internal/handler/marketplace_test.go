package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func setupMarketplaceRouter(t *testing.T, callerID string) (*gin.Engine, *store.SQLiteStore, *string) {
	t.Helper()
	s, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h := &MarketplaceHandler{Ops: &service.MarketplaceOps{Store: s}}
	currentCaller := callerID
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", currentCaller)
		c.Next()
	})
	r.GET("/api/marketplace/apps", h.List)
	r.POST("/api/marketplace/apps", h.Create)
	r.PUT("/api/marketplace/apps/:id", h.Update)
	r.DELETE("/api/marketplace/apps/:id", h.Delete)
	return r, s, &currentCaller
}

func TestMarketplaceHandlerAllowsCommunityCreateAndDelete(t *testing.T) {
	r, s, currentCaller := setupMarketplaceRouter(t, "alice")
	if err := s.CreateUser(context.Background(), store.User{ID: "alice", Name: "Alice Chen", Username: "alice"}); err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	w := doJSON(r, http.MethodPost, "/api/marketplace/apps", map[string]any{
		"name": "Notes", "description": "Shared notes", "url": "https://example.com/notes",
		"deploy_dir": "/srv/daymug/notes",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d body %s", w.Code, w.Body.String())
	}
	var created store.MarketplaceApp
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.CreatedBy != "alice" || created.CreatedByName != "Alice Chen" || created.DeployDir != "/srv/daymug/notes" {
		t.Fatalf("unexpected created app: %+v", created)
	}

	// A different signed-in user may edit and remove Alice's entry by design.
	*currentCaller = "bob"
	w = doJSON(r, http.MethodPut, "/api/marketplace/apps/"+created.ID, map[string]any{
		"name": "Notes v2", "description": "Shared notes, revised",
		"url": "https://example.com/notes-v2", "deploy_dir": "",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: status %d body %s", w.Code, w.Body.String())
	}
	var edited store.MarketplaceApp
	if err := json.Unmarshal(w.Body.Bytes(), &edited); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if edited.Name != "Notes v2" || edited.DeployDir != "" || edited.CreatedBy != "alice" {
		t.Fatalf("unexpected edited app: %+v", edited)
	}

	w = doJSON(r, http.MethodDelete, "/api/marketplace/apps/"+created.ID, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d body %s", w.Code, w.Body.String())
	}
	apps, err := s.ListMarketplaceApps(context.Background())
	if err != nil || len(apps) != 0 {
		t.Fatalf("apps after delete = %+v, err = %v", apps, err)
	}
}

func TestMarketplaceHandlerRejectsInvalidLink(t *testing.T) {
	r, _, _ := setupMarketplaceRouter(t, "alice")
	w := doJSON(r, http.MethodPost, "/api/marketplace/apps", map[string]any{
		"name": "Unsafe", "description": "Bad", "url": "file:///etc/passwd",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", w.Code, w.Body.String())
	}
}
