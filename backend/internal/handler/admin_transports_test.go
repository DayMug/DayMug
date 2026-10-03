package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func adminTransportsRouter(t *testing.T, probe func(context.Context, string, string) error) (*gin.Engine, *storetest.Fake) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	service.SetTransports(nil)
	t.Cleanup(func() { service.SetTransports(nil) })
	ms := storetest.New()
	h := &AdminTransportsHandler{Store: ms, Probe: probe}
	r := gin.New()
	r.GET("/api/admin/transports", h.Get)
	r.PUT("/api/admin/transports", h.Put)
	return r, ms
}

func putTransports(r *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/admin/transports", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAdminTransportsGetListsEveryTypeWithItsOptions(t *testing.T) {
	r, _ := adminTransportsRouter(t, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/transports", http.NoBody))
	var resp adminTransportsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]adminTransport{}
	for _, row := range resp.Transports {
		got[row.Provider] = row
	}
	if len(got) != len(config.SupportedCLITypes) {
		t.Fatalf("rows = %#v, want one per supported type", resp.Transports)
	}
	if row := got[config.CLITypeClaude]; row.Transport != service.TransportAgentSDK || len(row.Options) != 2 {
		t.Errorf("claude row = %#v, want agent-sdk default with a cli option", row)
	}
	if row := got[config.CLITypeClaudeCompatible]; len(row.Options) != 1 {
		t.Errorf("claude-compatible row = %#v, want a single fixed transport", row)
	}
}

func TestAdminTransportsPutSwitchesAndPersists(t *testing.T) {
	var probed []string
	r, ms := adminTransportsRouter(t, func(_ context.Context, provider, transport string) error {
		probed = append(probed, provider+"="+transport)
		return nil
	})
	rec := putTransports(r, `{"transports":{"codex":"cli","claude":"agent-sdk"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	if got := service.TransportFor(config.CLITypeCodex); got != service.TransportCLI {
		t.Errorf("codex transport = %q, want cli", got)
	}
	// Claude was already on agent-sdk, so there was nothing to probe.
	if len(probed) != 1 || probed[0] != "codex=cli" {
		t.Errorf("probed = %v, want only the changed type", probed)
	}
	if got := ms.AppSettings[service.TransportsKey]; got != `{"codex":"cli"}` {
		t.Errorf("stored = %q, want only the non-default selection", got)
	}
}

func TestAdminTransportsPutRefusesAnUnrunnableTransport(t *testing.T) {
	r, ms := adminTransportsRouter(t, func(context.Context, string, string) error {
		return errors.New("claude not found")
	})
	rec := putTransports(r, `{"transports":{"claude":"cli"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status %d, want 400", rec.Code)
	}
	if got := service.TransportFor(config.CLITypeClaude); got != service.TransportAgentSDK {
		t.Errorf("claude transport = %q, want unchanged agent-sdk", got)
	}
	if _, ok := ms.AppSettings[service.TransportsKey]; ok {
		t.Error("a refused switch must not be persisted")
	}
}

func TestAdminTransportsPutRejectsUnsupportedTransport(t *testing.T) {
	r, _ := adminTransportsRouter(t, nil)
	rec := putTransports(r, `{"transports":{"claude":"app-server"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
