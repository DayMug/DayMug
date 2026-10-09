package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

type checkBackend struct {
	reply    string
	err      error
	calls    int
	lastReq  agent.RunRequest
	lastWD   string
	blocking chan struct{}
}

func (*checkBackend) Name() string                                 { return "check" }
func (*checkBackend) Capabilities() agent.Capabilities             { return agent.Capabilities{} }
func (*checkBackend) SessionExists(string, string, string) bool    { return false }
func (*checkBackend) SessionLogPath(string, string, string) string { return "" }
func (*checkBackend) RunWithSession(context.Context, string, string, agent.RunRequest, chan<- agent.StreamEvent) error {
	return errors.New("unused")
}
func (b *checkBackend) RunOneshot(_ context.Context, _, wd string, req agent.RunRequest) (string, error) {
	b.calls++
	b.lastReq = req
	b.lastWD = wd
	if b.blocking != nil {
		<-b.blocking
	}
	return b.reply, b.err
}

func accountCheckRouter(t *testing.T, backend *checkBackend, auth accountAuthStatus) (*gin.Engine, *AdminAccountCheckHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "main", Type: config.CLITypeClaude, ConfigDir: "/creds/main", Env: map[string]string{"K": "V"}},
	}}
	useAccountModels(t, map[string]service.AccountModels{
		"main": {Models: []string{"model-a", "model-b"}, SummaryModel: "model-b"},
	})
	h := NewAdminAccountCheckHandler(cfg, service.NewBackendRegistry(map[string]agent.Backend{config.CLITypeClaude: backend}, nil), nil)
	h.AuthStatus = func(context.Context, *config.Provider) accountAuthStatus { return auth }
	r := gin.New()
	r.POST("/api/admin/providers/:name/check", h.Check)
	return r, h
}

func postCheck(t *testing.T, r *gin.Engine, name string) (int, accountCheckResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/providers/"+name+"/check", http.NoBody))
	var resp accountCheckResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, resp
}

// The run stage has to exercise what chat would: the account's credentials,
// its default model, and no write access to anything.
func TestAccountCheckRunsOneReadOnlyTurnAsTheAccount(t *testing.T) {
	backend := &checkBackend{reply: " OK \n"}
	r, _ := accountCheckRouter(t, backend, accountAuthStatus{Checked: true, LoggedIn: true, Method: "claude.ai"})

	code, resp := postCheck(t, r, "main")
	if code != http.StatusOK || !resp.OK {
		t.Fatalf("status %d resp %#v", code, resp)
	}
	if resp.Run.Reply != "OK" || resp.Run.Model != "model-a" {
		t.Errorf("run = %#v, want reply OK on the default model", resp.Run)
	}
	req := backend.lastReq
	if !req.ReadOnly || req.ConfigDir != "/creds/main" || req.AccountEnv["K"] != "V" || req.Model != "model-a" || req.SessionID == "" {
		t.Errorf("request = %#v", req)
	}
	if backend.lastWD == "" {
		t.Error("the run must get a throwaway working directory")
	}
}

func TestAccountCheckSkipsTheRunWhenLoggedOut(t *testing.T) {
	backend := &checkBackend{reply: "OK"}
	r, _ := accountCheckRouter(t, backend, accountAuthStatus{Checked: true, LoggedIn: false})

	code, resp := postCheck(t, r, "main")
	if code != http.StatusOK || resp.OK || resp.Run.Attempted {
		t.Fatalf("status %d resp %#v, want a failed check with no run", code, resp)
	}
	if backend.calls != 0 {
		t.Errorf("backend ran %d times for a logged-out account", backend.calls)
	}
}

func TestAccountCheckReportsARunFailure(t *testing.T) {
	backend := &checkBackend{err: errors.New("401 invalid token")}
	r, _ := accountCheckRouter(t, backend, accountAuthStatus{})

	_, resp := postCheck(t, r, "main")
	if resp.OK || !resp.Run.Attempted || resp.Run.Error != "401 invalid token" {
		t.Fatalf("resp = %#v, want the run error surfaced", resp)
	}
}

func TestAccountCheckRejectsUnknownAccountsAndOverlaps(t *testing.T) {
	backend := &checkBackend{reply: "OK", blocking: make(chan struct{})}
	r, h := accountCheckRouter(t, backend, accountAuthStatus{})

	if code, _ := postCheck(t, r, "nope"); code != http.StatusNotFound {
		t.Errorf("unknown account status = %d, want 404", code)
	}
	if !h.begin("main") {
		t.Fatal("first begin must succeed")
	}
	if code, _ := postCheck(t, r, "main"); code != http.StatusConflict {
		t.Errorf("overlapping check status = %d, want 409", code)
	}
	h.end("main")
	close(backend.blocking)
}

func TestParseClaudeAuthStatus(t *testing.T) {
	got := parseClaudeAuthStatus(`{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max","email":"a@b.c"}`, nil)
	if !got.Checked || !got.LoggedIn || got.Method != "claude.ai" || got.Detail != "max · a@b.c" {
		t.Errorf("logged in = %#v", got)
	}
	got = parseClaudeAuthStatus(`{"loggedIn":false}`, &exec.ExitError{})
	if !got.Checked || got.LoggedIn || got.Error != "" {
		t.Errorf("logged out = %#v", got)
	}
	got = parseClaudeAuthStatus("", errors.New(`exec: "claude": executable file not found`))
	if got.LoggedIn || got.Error == "" {
		t.Errorf("missing binary = %#v, want an error", got)
	}
}

func TestParseCodexLoginStatus(t *testing.T) {
	got := parseCodexLoginStatus("Logged in using ChatGPT\n", nil)
	if !got.LoggedIn || got.Method != "ChatGPT" {
		t.Errorf("logged in = %#v", got)
	}
	got = parseCodexLoginStatus("Not logged in", &exec.ExitError{})
	if got.LoggedIn || got.Error != "" || got.Detail != "Not logged in" {
		t.Errorf("logged out = %#v", got)
	}
}
