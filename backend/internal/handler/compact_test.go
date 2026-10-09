package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

type noCompactBackend struct{}

func (noCompactBackend) Name() string { return "no-compact" }
func (noCompactBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: false}
}
func (noCompactBackend) RunWithSession(context.Context, string, string, agent.RunRequest, chan<- agent.StreamEvent) error {
	return nil
}
func (noCompactBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (noCompactBackend) SessionExists(string, string, string) bool { return false }
func (noCompactBackend) SessionLogPath(string, string, string) string {
	return ""
}

type compactCaptureBackend struct {
	mu      sync.Mutex
	gotReqs []agent.RunRequest
	out     string
}

func (b *compactCaptureBackend) Name() string { return "compact-capture" }
func (b *compactCaptureBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: true}
}
func (b *compactCaptureBackend) RunWithSession(context.Context, string, string, agent.RunRequest, chan<- agent.StreamEvent) error {
	return nil
}
func (b *compactCaptureBackend) RunOneshot(_ context.Context, _, _ string, req agent.RunRequest) (string, error) {
	b.mu.Lock()
	b.gotReqs = append(b.gotReqs, req)
	b.mu.Unlock()
	return b.out, nil
}
func (b *compactCaptureBackend) SessionExists(string, string, string) bool { return true }
func (b *compactCaptureBackend) SessionLogPath(string, string, string) string {
	return ""
}

// TestCompact_RejectsBackendWithoutCapability verifies the capability
// gate fires for any backend that doesn't self-report SupportsCompaction.
// Per-conv provider is the source of truth, so a server with a compact-capable
// global default still rejects /compact for a non-capable conversation backend.
func TestCompact_RejectsBackendWithoutCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ms := storetest.New()
	const provider = "no-compact"
	_ = ms.CreateConversation(httptest.NewRequest(http.MethodGet, "/", http.NoBody).Context(), "c1", "", "u1", "", provider, "model")
	_ = ms.CreateUser(httptest.NewRequest(http.MethodGet, "/", http.NoBody).Context(), userOwner("u1"))
	h := NewTerminalHandler(&service.Runtime{
		Store: ms,
		Backends: service.NewBackendRegistry(map[string]agent.Backend{
			provider: noCompactBackend{},
		}, nil),
	})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/conversations/c1/compact", http.NoBody)
	c.Set("auth_user_id", "u1")
	c.Params = gin.Params{{Key: "id", Value: "c1"}}

	h.Compact(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not available") {
		t.Errorf("body should say compact is not available: %s", w.Body.String())
	}
}

func TestCompact_FallsBackToConversationModelWhenSummaryModelUnavailableForAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := httptest.NewRequest(http.MethodGet, "/", http.NoBody).Context()
	ms := storetest.New()
	const (
		provider          = config.CLITypeCodex
		account           = "codex-compatible"
		conversationModel = "openai/gpt-oss-120b"
		missingSummary    = "gpt-5.5"
	)
	_ = ms.CreateConversation(ctx, "c1", "", "u1", t.TempDir(), provider, conversationModel)
	_ = ms.CreateUser(ctx, store.User{ID: "u1", Username: "u1", Email: "u1@x", ProviderBindings: map[string]string{provider: account}})
	backend := &compactCaptureBackend{out: "summary"}
	h := newTestTerminalHandler(backend, ms)
	h.Backends = service.NewBackendRegistry(map[string]agent.Backend{provider: backend}, backend)
	useAccountModels(t, map[string]service.AccountModels{
		account: {Models: []string{conversationModel}, SummaryModel: missingSummary},
	})
	h.Cfg = &config.Config{Providers: []config.Provider{{
		Name: account,
		Type: provider,
	}}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/conversations/c1/compact", http.NoBody)
	c.Set("auth_user_id", "u1")
	c.Params = gin.Params{{Key: "id", Value: "c1"}}

	h.Compact(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.gotReqs) != 1 {
		t.Fatalf("RunOneshot calls = %d, want 1", len(backend.gotReqs))
	}
	if got := backend.gotReqs[0].Model; got != conversationModel {
		t.Fatalf("summary run model = %q, want conversation model %q", got, conversationModel)
	}
}

func userOwner(id string) store.User { return store.User{ID: id, Username: id, Email: id + "@x"} }
