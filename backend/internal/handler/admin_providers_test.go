package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestAdminProvidersPutPersistsAndHotSwaps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := storetest.New()
	cfg := &config.Config{}
	pool := service.NewPool(cfg)
	h := NewAdminProvidersHandler(cfg, settings, pool)
	r := gin.New()
	r.PUT("/api/admin/providers", h.Put)

	body := bytes.NewBufferString(`{"providers":[{"name":"codex-main","type":"codex","max_concurrent":2,"config_dir":"/tmp/codex","env":{"OPENAI_API_KEY":"secret"}}]}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/providers", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if settings.AppSettings[service.ProviderSettingsKey] == "" {
		t.Fatal("provider registry was not persisted")
	}
	account, err := pool.AccountForType("codex-main", config.CLITypeCodex)
	if err != nil || account.ConfigDir != "/tmp/codex" {
		t.Fatalf("hot account = %#v err=%v", account, err)
	}

	var response adminProvidersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Providers) != 1 || response.Providers[0].Env["OPENAI_API_KEY"] != "secret" {
		t.Fatalf("response = %#v", response)
	}
}

func TestAdminProvidersPutDropsLegacyRunnerMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := storetest.New()
	cfg := &config.Config{}
	pool := service.NewPool(cfg)
	h := NewAdminProvidersHandler(cfg, settings, pool)
	r := gin.New()
	r.PUT("/api/admin/providers", h.Put)

	body := bytes.NewBufferString(`{"providers":[{"name":"claude-main","type":"claude","max_concurrent":1,"runner_mode":"cli"}]}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/providers", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("runner_mode")) {
		t.Fatalf("legacy runner mode leaked into response: %s", rec.Body.String())
	}
	if strings.Contains(settings.AppSettings[service.ProviderSettingsKey], "runner_mode") {
		t.Fatalf("legacy runner mode was persisted: %s", settings.AppSettings[service.ProviderSettingsKey])
	}
}

func TestAdminProvidersPutAllowsDeletingEveryProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settings := storetest.New()
	cfg := &config.Config{Providers: []config.Provider{{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1}}}
	pool := service.NewPool(cfg)
	h := NewAdminProvidersHandler(cfg, settings, pool)
	r := gin.New()
	r.PUT("/api/admin/providers", h.Put)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/providers", bytes.NewBufferString(`{"providers":[]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := pool.AccountForType("default", config.CLITypeClaude); err == nil {
		t.Fatal("deleted provider remains available")
	}
}
