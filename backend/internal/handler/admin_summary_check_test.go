package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postSummaryCheck(t *testing.T, backend *checkBackend, account, body string) (int, summaryCheckResponse) {
	t.Helper()
	r, h := accountCheckRouter(t, backend, accountAuthStatus{})
	r.POST("/api/admin/providers/:name/summary-check", h.SummaryCheck)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/admin/providers/"+account+"/summary-check", strings.NewReader(body)))
	var resp summaryCheckResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, resp
}

// The check must run the model typed in the field, not the saved one, under
// the account's own credentials.
func TestSummaryCheckTitlesWithTheRequestedModel(t *testing.T) {
	backend := &checkBackend{reply: "Reverse UTF-8 strings in Go"}
	code, resp := postSummaryCheck(t, backend, "main", `{"model":" model-typed "}`)
	if code != http.StatusOK || !resp.OK || resp.Title != "Reverse UTF-8 strings in Go" {
		t.Fatalf("status %d resp %#v", code, resp)
	}
	req := backend.lastReq
	if req.Model != "model-typed" || req.ConfigDir != "/creds/main" || req.AccountEnv["K"] != "V" {
		t.Errorf("request = %#v", req)
	}
}

func TestSummaryCheckSurfacesAnUnknownModel(t *testing.T) {
	backend := &checkBackend{err: errors.New("There's an issue with the selected model (claude-haiku-5)")}
	_, resp := postSummaryCheck(t, backend, "main", `{"model":"claude-haiku-5"}`)
	if resp.OK || !strings.Contains(resp.Error, "claude-haiku-5") {
		t.Fatalf("resp = %#v, want the model error surfaced", resp)
	}
}

func TestSummaryCheckRejectsBadInput(t *testing.T) {
	cases := []struct {
		name, account, body string
		want                int
	}{
		{"unknown account", "nope", `{"model":"m"}`, http.StatusNotFound},
		{"empty model", "main", `{"model":"  "}`, http.StatusBadRequest},
		{"bad json", "main", `{`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &checkBackend{reply: "x"}
			if code, _ := postSummaryCheck(t, backend, tc.account, tc.body); code != tc.want {
				t.Errorf("status = %d, want %d", code, tc.want)
			}
			if backend.calls != 0 {
				t.Errorf("backend ran for rejected input")
			}
		})
	}
}
