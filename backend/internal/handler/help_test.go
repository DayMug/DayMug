package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupHelpRouter(t *testing.T, ms *storetest.Fake, callerID string, isAdmin bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(adminAuthCtx(callerID, isAdmin))
	h := NewHelpHandler(ms)
	r.GET("/api/help-doc", h.Get)
	// Admin route is mounted directly without RequireAdmin in this test
	// router — the routes.go integration covers the admin gating. Here
	// we only need the handler's own logic.
	r.PUT("/api/admin/help-doc", h.Put)
	return r
}

func TestHelpDocEmptyByDefault(t *testing.T) {
	ms := storetest.New()
	r := setupHelpRouter(t, ms, "u1", false)

	w := doJSON(r, "GET", "/api/help-doc", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["markdown"] != "" {
		t.Errorf("expected empty markdown by default, got %q", body["markdown"])
	}
}

func TestHelpDocRoundTrip(t *testing.T) {
	ms := storetest.New()
	r := setupHelpRouter(t, ms, "admin-1", true)

	md := "# Welcome\n\nThis is **help** copy."
	w := doJSON(r, "PUT", "/api/admin/help-doc", map[string]any{"markdown": md})
	if w.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", w.Code, w.Body.String())
	}

	stored, _ := ms.GetAppSetting(context.Background(), helpDocKey)
	if stored != md {
		t.Errorf("store value: want %q got %q", md, stored)
	}

	w = doJSON(r, "GET", "/api/help-doc", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status %d body %s", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["markdown"] != md {
		t.Errorf("get returned %q, want %q", body["markdown"], md)
	}
}

func TestHelpDocClearWithEmptyString(t *testing.T) {
	ms := storetest.New()
	_ = ms.SetAppSetting(context.Background(), helpDocKey, "old content")
	r := setupHelpRouter(t, ms, "admin-1", true)

	w := doJSON(r, "PUT", "/api/admin/help-doc", map[string]any{"markdown": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("put: status %d body %s", w.Code, w.Body.String())
	}
	stored, _ := ms.GetAppSetting(context.Background(), helpDocKey)
	if stored != "" {
		t.Errorf("expected cleared value, got %q", stored)
	}
}
