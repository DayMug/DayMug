package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func adminModelsRouter(t *testing.T) (*gin.Engine, *storetest.Fake) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ms := storetest.New()
	h := NewAdminModelsHandler(&config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "codex-qwen", Type: config.CLITypeCodex},
	}}, ms)
	r := gin.New()
	r.GET("/api/admin/models", h.Get)
	r.PUT("/api/admin/models", h.Put)
	return r, ms
}

func getAdminModels(t *testing.T, r *gin.Engine) adminModelsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/models", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d: %s", rec.Code, rec.Body.String())
	}
	var resp adminModelsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestAdminModelsGetListsEveryAccountWithItsInheritedList(t *testing.T) {
	useAccountModels(t, nil)
	r, _ := adminModelsRouter(t)

	resp := getAdminModels(t, r)
	if len(resp.Accounts) != 2 {
		t.Fatalf("accounts = %#v, want one row per configured account", resp.Accounts)
	}
	qwen := resp.Accounts[1]
	if qwen.Account != "codex-qwen" || qwen.Provider != config.CLITypeCodex {
		t.Fatalf("second row = %#v", qwen)
	}
	if qwen.Overridden {
		t.Error("an account with no stored row must report overridden=false")
	}
	if !slices.Equal(qwen.Models, service.ProviderModels[config.CLITypeCodex]) {
		t.Errorf("models = %#v, want the (test fixture) codex baseline", qwen.Models)
	}
}

func TestAdminModelsGetEncodesEmptyModelListAsArray(t *testing.T) {
	useAccountModels(t, nil)
	h := NewAdminModelsHandler(&config.Config{Providers: []config.Provider{
		{Name: "compatible", Type: config.CLITypeOpenAICompatible},
	}}, storetest.New())
	r := gin.New()
	r.GET("/api/admin/models", h.Get)

	resp := getAdminModels(t, r)
	if len(resp.Accounts) != 1 {
		t.Fatalf("accounts = %#v, want one row", resp.Accounts)
	}
	if resp.Accounts[0].Models == nil {
		t.Fatal("models decoded as nil, meaning the API returned null instead of []")
	}
}

func TestAdminModelsPutPersistsAndTakesEffectImmediately(t *testing.T) {
	useAccountModels(t, nil)
	r, ms := adminModelsRouter(t)

	body := bytes.NewBufferString(`{"accounts":{"codex-qwen":{"models":["Qwen/Qwen3"],"summary_model":"Qwen/Qwen3"}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/models", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}

	if ms.AppSettings[service.ModelOverridesKey] == "" {
		t.Fatal("registry was not persisted to app_settings")
	}
	// No restart: the resolver the picker and the runner share must already
	// see the new list.
	got := service.ModelsForAccount(nil, config.CLITypeCodex, "codex-qwen")
	if !slices.Equal(got, []string{"Qwen/Qwen3"}) {
		t.Fatalf("ModelsForAccount = %#v, want the just-saved list", got)
	}

	resp := getAdminModels(t, r)
	if !resp.Accounts[1].Overridden {
		t.Error("the saved account must report overridden=true")
	}
	if resp.Accounts[0].Overridden {
		t.Error("an untouched sibling must stay on inheritance")
	}
}

func TestAdminModelsPutRejectsAnAccountThatIsNotConfigured(t *testing.T) {
	useAccountModels(t, nil)
	r, ms := adminModelsRouter(t)

	body := bytes.NewBufferString(`{"accounts":{"typo-acct":{"models":["m"]}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/models", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// A rejected save must not have written anything — a dead row keyed by a
	// typo would never apply to any account and would be invisible in the UI.
	if _, ok := ms.AppSettings[service.ModelOverridesKey]; ok {
		t.Fatal("rejected save must not touch app_settings")
	}
}

// With no built-in list to fall back on, the summary model is never guessed:
// an account that serves models must name the one titles and /compact use.
func TestAdminModelsPutRequiresASummaryModel(t *testing.T) {
	useAccountModels(t, nil)
	r, ms := adminModelsRouter(t)

	body := bytes.NewBufferString(`{"accounts":{"codex-qwen":{"models":["Qwen/Qwen3"],"summary_model":" "}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/models", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if _, ok := ms.AppSettings[service.ModelOverridesKey]; ok {
		t.Error("a rejected registry must not be persisted")
	}
}

func TestAdminModelsPutStoresModelSpecs(t *testing.T) {
	useAccountModels(t, nil)
	r, _ := adminModelsRouter(t)

	body := bytes.NewBufferString(`{"accounts":{"codex-qwen":{"models":["Qwen/Qwen3"],"summary_model":"Qwen/Qwen3","specs":{"Qwen/Qwen3":{"context_window":131072,"no_image_input":true}}}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/models", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", rec.Code, rec.Body.String())
	}
	want := service.ModelSpec{ContextWindow: 131072, NoImageInput: true}
	if got := service.ModelSpecFor("codex-qwen", "Qwen/Qwen3"); got != want {
		t.Fatalf("ModelSpecFor = %#v, want %#v", got, want)
	}
	if got := getAdminModels(t, r).Accounts[1].Specs["Qwen/Qwen3"]; got != want {
		t.Fatalf("GET specs = %#v, want %#v", got, want)
	}
}

func TestAdminModelsPutRejectsANegativeContextWindow(t *testing.T) {
	useAccountModels(t, nil)
	r, ms := adminModelsRouter(t)

	body := bytes.NewBufferString(`{"accounts":{"codex-qwen":{"models":["Qwen/Qwen3"],"summary_model":"Qwen/Qwen3","specs":{"Qwen/Qwen3":{"context_window":-1}}}}}`)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/models", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if _, ok := ms.AppSettings[service.ModelOverridesKey]; ok {
		t.Error("a rejected registry must not be persisted")
	}
}
