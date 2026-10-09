package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestServerInfo_ReturnsVersionAndSandboxFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Sandbox.Enabled = true
	cfg.Sandbox.Type = config.SandboxNoop
	h := NewServerInfoHandler(cfg, "1.2.3")

	r := gin.New()
	r.GET("/api/server-info", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/server-info", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["version"] != "1.2.3" {
		t.Errorf("version = %v, want 1.2.3", got["version"])
	}
	if got["sandbox_enabled"] != true {
		t.Errorf("sandbox_enabled = %v, want true", got["sandbox_enabled"])
	}
	if got["sandbox_type"] != config.SandboxNoop {
		t.Errorf("sandbox_type = %v, want %q", got["sandbox_type"], config.SandboxNoop)
	}
	if _, ok := got["manifest_url"].(string); !ok {
		t.Errorf("manifest_url missing or not a string: %v", got["manifest_url"])
	}
}

func TestServerInfo_NilConfigTreatedAsSandboxOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewServerInfoHandler(nil, "dev")

	r := gin.New()
	r.GET("/api/server-info", h.Get)

	req := httptest.NewRequest(http.MethodGet, "/api/server-info", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["sandbox_enabled"] != false {
		t.Errorf("sandbox_enabled = %v, want false on nil config", got["sandbox_enabled"])
	}
	if got["version"] != "dev" {
		t.Errorf("version = %v, want dev", got["version"])
	}
}
