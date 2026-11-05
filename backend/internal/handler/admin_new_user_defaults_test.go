package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestAdminNewUserDefaults(t *testing.T) {
	ms := storetest.New()
	h := NewAdminHandler(ms, newTestConfig(t))
	r := setupAdminRouter(t, h, "admin", true)
	want := service.NewUserDefaults{ProviderType: "claude", ProviderName: "alt"}
	w := doJSON(r, http.MethodPut, "/api/admin/new-user-defaults", want)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w = doJSON(r, http.MethodGet, "/api/admin/new-user-defaults", nil)
	var got service.NewUserDefaults
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || got != want {
		t.Fatalf("get: %d %+v", w.Code, got)
	}
	w = doJSON(r, http.MethodPut, "/api/admin/new-user-defaults", service.NewUserDefaults{})
	if w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	w = doJSON(r, http.MethodPut, "/api/admin/new-user-defaults", map[string]string{"model": "unknown"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid defaults: %d %s", w.Code, w.Body)
	}
	w = doJSON(r, http.MethodPut, "/api/admin/new-user-defaults", "malformed")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed request: %d %s", w.Code, w.Body)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		nonAdmin := setupAdminRouter(t, h, "user", false)
		w = doJSON(nonAdmin, method, "/api/admin/new-user-defaults", service.NewUserDefaults{})
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s: %d", method, w.Code)
		}
	}
}
