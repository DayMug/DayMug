package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupRouter(ms *storetest.Fake) *gin.Engine {
	r := gin.New()
	h := NewConversationHandler(ms)
	h.Cfg = &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "ollama", Type: config.CLITypeClaude},
		{Name: "alt", Type: config.CLITypeClaude},
		{Name: "codex", Type: config.CLITypeCodex},
	}}
	r.GET("/api/conversations", h.List)
	r.POST("/api/conversations", h.Create)
	r.GET("/api/conversations/:id", h.Get)
	r.DELETE("/api/conversations/:id", h.Delete)
	r.DELETE("/api/stale-conversations", h.DeleteStale)
	r.GET("/api/conversations/:id/messages", h.GetMessages)
	r.DELETE("/api/conversations/:id/messages", h.ClearMessages)
	r.POST("/api/conversations/:id/share", h.Share)
	r.DELETE("/api/conversations/:id/share", h.Unshare)
	r.GET("/api/shared/conversations/:token", h.GetShared)
	r.PUT("/api/conversations/:id/title", h.UpdateTitle)
	r.PUT("/api/conversations/:id/notifications", h.UpdateNotifications)
	r.PUT("/api/conversations/:id/pinned", h.UpdatePinned)
	r.PUT("/api/conversation-pin-order", h.ReorderPinned)
	r.PUT("/api/conversations/:id/model", h.UpdateModel)
	r.POST("/api/conversations/:id/clear-context", h.ClearContext)
	return r
}

// setupAuthedRouter is like setupRouter but also installs a fake auth
// middleware that pins the authenticated user id to authedUID. Use it to
// exercise the ownership checks in conversation handlers.
func setupAuthedRouter(ms *storetest.Fake, authedUID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	h := NewConversationHandler(ms)
	h.Cfg = &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
		{Name: "ollama", Type: config.CLITypeClaude},
		{Name: "alt", Type: config.CLITypeClaude},
		{Name: "codex", Type: config.CLITypeCodex},
	}}
	r.GET("/api/conversations", h.List)
	r.POST("/api/conversations", h.Create)
	r.GET("/api/conversations/:id", h.Get)
	r.DELETE("/api/conversations/:id", h.Delete)
	r.DELETE("/api/stale-conversations", h.DeleteStale)
	r.GET("/api/conversations/:id/messages", h.GetMessages)
	r.DELETE("/api/conversations/:id/messages", h.ClearMessages)
	r.PUT("/api/conversations/:id/title", h.UpdateTitle)
	r.PUT("/api/conversations/:id/notifications", h.UpdateNotifications)
	r.PUT("/api/conversations/:id/pinned", h.UpdatePinned)
	r.PUT("/api/conversations/:id/model", h.UpdateModel)
	r.POST("/api/conversations/:id/clear-context", h.ClearContext)
	return r
}

func TestConversation_List(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", Title: "First"},
		{ID: "c2", Title: "Second"},
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/conversations?user_id=u1", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var convs []store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&convs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(convs))
	}
}

func TestConversation_Create(t *testing.T) {
	ms := storetest.New()
	// The owner row must exist: Create now refuses rather than seeding an
	// empty work_dir from a read it couldn't make.
	ms.Users = []store.User{{ID: "u1", Username: "u1"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"My Chat","user_id":"u1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.Title != "My Chat" {
		t.Errorf("expected title 'My Chat', got '%s'", conv.Title)
	}
	if conv.ID == "" {
		t.Error("expected non-empty ID")
	}
}

// A create that pins an account the user is allowed to use stores it; one that
// pins an account outside the user's allowed set is rejected with 400.
func TestConversation_Create_PinsAllowedAccount(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{
		ID:               "u1",
		Username:         "u1",
		ProviderBindings: map[string]string{"claude": "default"},
		ProviderAccounts: map[string][]string{"claude": {"default", "ollama"}},
	}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Pinned","user_id":"u1","provider":"claude","model":"claude-opus-5[1m]","account":"ollama"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.AccountName != "ollama" {
		t.Errorf("conv.AccountName = %q, want ollama", conv.AccountName)
	}
}

func TestConversation_Create_RejectsUnboundAccount(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{
		ID:               "u1",
		Username:         "u1",
		ProviderBindings: map[string]string{"claude": "default"},
		ProviderAccounts: map[string][]string{"claude": {"default"}},
	}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Bad","user_id":"u1","provider":"claude","model":"claude-opus-5[1m]","account":"ollama"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

// TestConversation_Create_DefaultsLatestModel pins the auto-pick behaviour:
// a create that omits provider+model lands on (claude, claude-opus-5[1m])
// when the handler has no Cfg, matching the latest entry of
// service.ProviderModels.
func TestConversation_Create_DefaultsLatestModel(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "u1"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Auto","user_id":"u1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.Provider != "claude" || conv.Model != "claude-opus-5-5[1m]" {
		t.Errorf("provider/model = %q/%q, want claude/claude-opus-5-5[1m]", conv.Provider, conv.Model)
	}
}

func TestConversation_Create_SeedsUserDefaultModel(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "u1", DefaultModel: "claude-haiku-4-5"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Auto","user_id":"u1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	_ = json.NewDecoder(rec.Body).Decode(&conv)
	if conv.Provider != "claude" || conv.Model != "claude-haiku-4-5" {
		t.Errorf("provider/model = %q/%q, want claude/claude-haiku-4-5", conv.Provider, conv.Model)
	}
}

func TestConversation_Create_ExplicitModelOverridesUserDefault(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "u1", DefaultModel: "claude-haiku-4-5"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"X","user_id":"u1","provider":"codex","model":"gpt-6-astra"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	_ = json.NewDecoder(rec.Body).Decode(&conv)
	if conv.Provider != "codex" || conv.Model != "gpt-6-astra" {
		t.Errorf("provider/model = %q/%q, want codex/gpt-6-astra (explicit pair must win)", conv.Provider, conv.Model)
	}
}

func TestConversation_Create_AcceptsCodexPair(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "u1"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"X","user_id":"u1","provider":"codex","model":"gpt-5.6-luna"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var conv store.Conversation
	_ = json.NewDecoder(rec.Body).Decode(&conv)
	if conv.Provider != "codex" || conv.Model != "gpt-5.6-luna" {
		t.Errorf("provider/model = %q/%q, want codex/gpt-5.6-luna", conv.Provider, conv.Model)
	}
}

func TestConversation_Create_RejectsCrossProviderModel(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "u1"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"X","user_id":"u1","provider":"claude","model":"gpt-5.5"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestConversation_UpdateModel_AllowsSwitchWithinProvider(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: "claude", Model: "claude-opus-4-8",
	}}
	r := setupAuthedRouter(ms, "u1")

	body := bytes.NewBufferString(`{"provider":"claude","model":"claude-sonnet-5"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/model", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ms.Conversations[0].Model != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", ms.Conversations[0].Model)
	}
}

func TestConversation_UpdateModel_AllowsProviderSwitchBeforeFirstMessage(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: "claude", Model: "claude-opus-4-8",
	}}
	// The model has not replied — HasModelReply returns false, so the
	// provider swap is allowed.
	r := setupAuthedRouter(ms, "u1")

	body := bytes.NewBufferString(`{"provider":"codex","model":"gpt-5.6-terra"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/model", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ms.Conversations[0].Provider != "codex" {
		t.Errorf("provider = %q, want codex", ms.Conversations[0].Provider)
	}
}

func TestConversation_UpdateModel_RejectsProviderSwitchAfterFirstMessage(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: "claude", Model: "claude-opus-4-8",
	}}
	// Seed a model reply so HasModelReply returns true — the gate must then
	// refuse the provider change.
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hello"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hi"},
	}
	r := setupAuthedRouter(ms, "u1")

	body := bytes.NewBufferString(`{"provider":"codex","model":"gpt-5.6-terra"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/model", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ms.Conversations[0].Provider != "claude" {
		t.Errorf("provider should not have changed; got %q", ms.Conversations[0].Provider)
	}
}

// Once a conversation has run a turn it is permanently bound to the account it
// ran on: re-pinning it to a sibling account of the same provider type is a 409.
func TestConversation_UpdateModel_RejectsAccountSwitchAfterFirstMessage(t *testing.T) {
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{
		ID: "u1", Username: "alice",
		ProviderBindings: map[string]string{"claude": "default"},
		ProviderAccounts: map[string][]string{"claude": {"default", "alt"}},
	})
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: "claude", Model: "claude-opus-4-8", AccountName: "default",
	}}
	// Seed a model reply so the conversation counts as started.
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hello"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hi"},
	}
	r := setupAuthedRouter(ms, "u1")

	body := bytes.NewBufferString(`{"provider":"claude","model":"claude-sonnet-5","account":"alt"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/model", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ms.Conversations[0].AccountName != "default" {
		t.Errorf("account pin should be unchanged, got %q", ms.Conversations[0].AccountName)
	}
}

// Before the first turn the pin is still mutable — a user can freely re-select
// the account while configuring a fresh conversation.
func TestConversation_UpdateModel_AllowsAccountSwitchBeforeFirstMessage(t *testing.T) {
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{
		ID: "u1", Username: "alice",
		ProviderBindings: map[string]string{"claude": "default"},
		ProviderAccounts: map[string][]string{"claude": {"default", "alt"}},
	})
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: "claude", Model: "claude-opus-4-8", AccountName: "default",
	}}
	r := setupAuthedRouter(ms, "u1")

	body := bytes.NewBufferString(`{"provider":"claude","model":"claude-sonnet-5","account":"alt"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/model", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ms.Conversations[0].AccountName != "alt" {
		t.Errorf("account pin = %q, want alt", ms.Conversations[0].AccountName)
	}
}

func TestConversation_UpdateModel_RejectsCrossProvider(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: "claude", Model: "claude-opus-4-8",
	}}
	r := setupAuthedRouter(ms, "u1")

	body := bytes.NewBufferString(`{"provider":"codex","model":"claude-opus-4-8"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/model", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestConversation_Delete(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", Title: "X"}}
	r := setupRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/conversations/c1", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(ms.Conversations) != 0 {
		t.Error("expected conversation to be deleted")
	}
}

func TestConversation_Delete_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/conversations/missing", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConversation_DeleteStale(t *testing.T) {
	now := time.Now()
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "old", UserID: "agent-1", UpdatedAt: now.Add(-15 * 24 * time.Hour)},
		{ID: "recent", UserID: "agent-1", UpdatedAt: now.Add(-13 * 24 * time.Hour)},
		{ID: "other", UserID: "agent-2", UpdatedAt: now.Add(-15 * 24 * time.Hour)},
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/stale-conversations?user_id=agent-1", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var response struct {
		DeletedIDs   []string `json:"deleted_ids"`
		DeletedCount int      `json:"deleted_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.DeletedCount != 1 || len(response.DeletedIDs) != 1 || response.DeletedIDs[0] != "old" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestConversation_GetMessages(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hello"},
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/conversations/c1/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var msgs []store.Message
	if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func TestConversation_GetMessagesRepairsLegacyArtifactPath(t *testing.T) {
	fileRoot := t.TempDir()
	workDir := filepath.Join(fileRoot, "pdf-export")
	if err := os.MkdirAll(filepath.Join(workDir, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "work", "preview.png"), []byte("\x89PNG\r\n\x1a\nimage"), 0o600); err != nil {
		t.Fatal(err)
	}
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent-1", WorkDir: fileRoot}}
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "agent-1", WorkDir: workDir}}
	ms.Messages["c1"] = []store.Message{{
		ID:             "m1",
		ConversationID: "c1",
		Role:           "assistant",
		Content:        "done",
		Metadata:       json.RawMessage(`{"attachments":[{"name":"preview.png","mime":"image/png","path":"./work/preview.png","url":"/api/users/agent-1/files/read?path=.%2Fwork%2Fpreview.png"}]}`),
	}}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/conversations/c1/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var msgs []store.Message
	if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !strings.Contains(string(msgs[0].Metadata), "path=.%2Fpdf-export%2Fwork%2Fpreview.png") {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestConversation_ShareReturnsPublicURL(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Title: "Shared chat"}}
	r := setupRouter(ms)

	req := httptest.NewRequest("POST", "/api/conversations/c1/share", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversation store.Conversation `json:"conversation"`
		Token        string             `json:"token"`
		URL          string             `json:"url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Token != "share-c1" || body.URL != "/share/conversation/share-c1" {
		t.Fatalf("unexpected share payload: %+v", body)
	}
	if !body.Conversation.Shared {
		t.Fatal("expected shared conversation payload")
	}
}

func TestConversation_UnshareClearsPublicAccess(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Title: "Shared chat", ShareToken: "tok123", Shared: true,
	}}
	r := setupRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/conversations/c1/share", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Shared {
		t.Fatal("expected conversation to be unshared")
	}

	req = httptest.NewRequest("GET", "/api/shared/conversations/tok123", http.NoBody)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected old token to return 404, got %d", rec.Code)
	}
}

func TestConversation_GetSharedReturnsTranscriptWithoutAuth(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Title: "Shared chat", ShareToken: "tok123",
	}}
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hello"},
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/shared/conversations/tok123", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversation store.Conversation `json:"conversation"`
		Messages     []store.Message    `json:"messages"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Conversation.ID != "c1" || body.Conversation.Title != "Shared chat" {
		t.Fatalf("unexpected conversation: %+v", body.Conversation)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(body.Messages))
	}
}

func TestConversation_GetSharedReturnsAllTranscriptMessages(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Title: "Long shared chat", ShareToken: "tok123",
	}}
	for i := 0; i < 605; i++ {
		ms.Messages["c1"] = append(ms.Messages["c1"], store.Message{
			ID:             fmt.Sprintf("m%03d", i),
			ConversationID: "c1",
			Role:           "assistant",
			Content:        fmt.Sprintf("message %03d", i),
		})
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/shared/conversations/tok123", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Messages []store.Message `json:"messages"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Messages) != 605 {
		t.Fatalf("expected all 605 messages, got %d", len(body.Messages))
	}
	if body.Messages[0].ID != "m000" || body.Messages[604].ID != "m604" {
		t.Fatalf("messages out of order or truncated: first=%q last=%q", body.Messages[0].ID, body.Messages[604].ID)
	}
}

func TestConversation_GetSharedMissingTokenReturnsNotFound(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/shared/conversations/missing", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConversation_ClearMessages(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/conversations/c1/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(ms.Messages["c1"]) != 0 {
		t.Errorf("expected messages to be cleared, got %d", len(ms.Messages["c1"]))
	}
}

// The route has to carry the back-filled Broadcaster through to the service
// layer, otherwise the owner's other tabs keep rendering messages that were
// already deleted until someone reloads the page.
func TestConversation_ClearMessages_PushesMessagesClearedToRoom(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
	}
	bc := service.NewBroadcaster()
	peer := make(chan []byte, 4)
	bc.Join("c1", "peer-tab", peer)

	r := gin.New()
	h := NewConversationHandler(ms)
	h.Broadcaster = bc
	r.DELETE("/api/conversations/:id/messages", h.ClearMessages)

	req := httptest.NewRequest("DELETE", "/api/conversations/c1/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	var frame struct {
		Type           string `json:"type"`
		ConversationID string `json:"conversation_id"`
	}
	select {
	case data := <-peer:
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatalf("decode: %v", err)
		}
	default:
		t.Fatal("peer tab received no frame")
	}
	if frame.Type != "messages_cleared" || frame.ConversationID != "c1" {
		t.Fatalf("unexpected frame: %+v", frame)
	}
}

func TestConversation_Create_InvalidJSON(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{invalid`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// API contract: an owner row the store can't produce now fails the create
// (404) instead of returning 201 with a silently empty work_dir.
func TestConversation_Create_UnknownOwnerIsNotFound(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Orphan","user_id":"ghost"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(ms.Conversations) != 0 {
		t.Fatalf("no conversation may be persisted: %+v", ms.Conversations)
	}
}

func TestConversation_Create_MissingUserID(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"No User"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "user_id is required" {
		t.Errorf("expected 'user_id is required' error, got '%s'", resp["error"])
	}
}

func TestConversation_GetMessages_BeforeID(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "1"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "2"},
		{ID: "m3", ConversationID: "c1", Role: "user", Content: "3"},
		{ID: "m4", ConversationID: "c1", Role: "assistant", Content: "4"},
	}
	r := setupRouter(ms)

	t.Run("empty before_id returns latest page", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/conversations/c1/messages?before_id=&limit=2", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var msgs []store.Message
		if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 2 || msgs[0].ID != "m3" || msgs[1].ID != "m4" {
			t.Fatalf("expected [m3,m4], got %+v", msgs)
		}
	})

	t.Run("populated before_id returns the prior page", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/conversations/c1/messages?before_id=m3&limit=2", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var msgs []store.Message
		if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 2 || msgs[0].ID != "m1" || msgs[1].ID != "m2" {
			t.Fatalf("expected [m1,m2], got %+v", msgs)
		}
	})

	t.Run("omitting the cursors returns the whole conversation ascending", func(t *testing.T) {
		// Reverse pagination is opt-in. The retired offset parameter is
		// ignored rather than skipping rows.
		req := httptest.NewRequest("GET", "/api/conversations/c1/messages?offset=2", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var msgs []store.Message
		if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 4 || msgs[0].ID != "m1" || msgs[3].ID != "m4" {
			t.Fatalf("expected [m1..m4], got %+v", msgs)
		}
	})
}

func TestConversation_GetMessages_AfterID(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	ms.Messages["c1"] = []store.Message{
		{ID: "m1", ConversationID: "c1", Role: "user", Content: "1"},
		{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "2"},
		{ID: "m3", ConversationID: "c1", Role: "user", Content: "3"},
		{ID: "m4", ConversationID: "c1", Role: "assistant", Content: "4"},
	}
	r := setupRouter(ms)

	t.Run("populated after_id returns rows strictly newer than the cursor", func(t *testing.T) {
		// The reconnect / focus-resume sync path: client passes the id of
		// the last message it has and expects every persisted message
		// after that, in ascending insertion order.
		req := httptest.NewRequest("GET", "/api/conversations/c1/messages?after_id=m2", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
		}
		var msgs []store.Message
		if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 2 || msgs[0].ID != "m3" || msgs[1].ID != "m4" {
			t.Fatalf("expected [m3,m4], got %+v", msgs)
		}
	})

	t.Run("empty after_id returns the entire conversation (recovery path)", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/conversations/c1/messages?after_id=", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var msgs []store.Message
		if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 4 {
			t.Fatalf("expected 4 messages on empty cursor, got %d", len(msgs))
		}
	})

	t.Run("after_id at the tail returns no rows", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/conversations/c1/messages?after_id=m4", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var msgs []store.Message
		if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(msgs) != 0 {
			t.Fatalf("expected 0 rows past tail cursor, got %d", len(msgs))
		}
	})
}

// TestConversation_GetMessages_NoStoreCacheHeader locks in the
// Cache-Control header so the iOS Safari "stale snapshot after refresh"
// regression can't sneak back in. Without no-store, browsers can serve
// a cached message list whose tail is missing the assistant reply that
// just persisted, even though the row is in the DB — that's the
// failure mode where users see a Bark notification but no chat row.
func TestConversation_GetMessages_NoStoreCacheHeader(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/conversations/c1/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("expected Cache-Control to contain 'no-store', got %q", cc)
	}
}

func TestConversation_GetMessages_Empty(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/conversations/c1/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var msgs []store.Message
	if err := json.NewDecoder(rec.Body).Decode(&msgs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}

func TestConversation_Get(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Name: "Alice", WorkDir: "/home/alice"})
	ms.Conversations = []store.Conversation{
		{ID: "c1", Title: "Test", UserID: "u1", WorkDir: "/home/alice/project"},
	}
	r := setupRouter(ms)

	req := httptest.NewRequest("GET", "/api/conversations/c1", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.WorkDir != "/home/alice/project" {
		t.Errorf("expected work_dir '/home/alice/project', got '%s'", conv.WorkDir)
	}
}

func TestConversation_UpdateTitle(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", Title: "Old Title", UserID: "u1"},
	}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"New Title"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/title", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.Title != "New Title" {
		t.Errorf("expected title 'New Title', got '%s'", conv.Title)
	}
}

func TestConversation_UpdateTitle_InvalidJSON(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{invalid`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/title", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestConversation_Create_DefaultsNotificationsOn(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Name: "Alice", WorkDir: "/home/alice"})
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Default","user_id":"u1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !conv.NotificationsEnabled {
		t.Errorf("expected new conversation to default to notifications_enabled=true")
	}
}

// An Agent's working directory is fixed: a work_dir in the create body is not
// part of the contract and must not re-root the conversation.
func TestConversation_Create_IgnoresClientWorkDir(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Name: "Alice", WorkDir: "/home/alice"})
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"Inherit","user_id":"u1","work_dir":"/home/alice/project"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.WorkDir != "/home/alice" {
		t.Errorf("expected the owner's work_dir, got '%s'", conv.WorkDir)
	}
}

func TestConversation_UpdateNotifications(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", Title: "Test", UserID: "u1", NotificationsEnabled: false},
	}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"enabled":true}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/notifications", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !conv.NotificationsEnabled {
		t.Errorf("expected notifications_enabled=true, got false")
	}
	if !ms.Conversations[0].NotificationsEnabled {
		t.Errorf("expected store updated to true")
	}
}

func TestConversation_UpdateNotifications_Disable(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", Title: "Test", UserID: "u1", NotificationsEnabled: true},
	}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"enabled":false}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/notifications", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ms.Conversations[0].NotificationsEnabled {
		t.Errorf("expected notifications_enabled=false")
	}
}

func TestConversation_UpdateNotifications_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"enabled":true}`)
	req := httptest.NewRequest("PUT", "/api/conversations/missing/notifications", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConversation_UpdateNotifications_InvalidJSON(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1"}}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{invalid`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/notifications", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestConversation_UpdatePinned(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Pinned: false},
	}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"pinned":true}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/pinned", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !conv.Pinned {
		t.Errorf("expected pinned=true in response")
	}
	if !ms.Conversations[0].Pinned {
		t.Errorf("expected store updated to pinned=true")
	}
}

func TestConversation_UpdatePinned_Unpin(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Pinned: true},
	}
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"pinned":false}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/pinned", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ms.Conversations[0].Pinned {
		t.Errorf("expected pinned=false after unpin")
	}
}

func TestConversation_UpdatePinned_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"pinned":true}`)
	req := httptest.NewRequest("PUT", "/api/conversations/missing/pinned", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConversation_ReorderPinned(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Pinned: true, PinOrder: 0},
		{ID: "c2", UserID: "u1", Pinned: true, PinOrder: 1},
		{ID: "c3", UserID: "u1", Pinned: false},
	}
	r := setupRouter(ms)

	// New order: c2 first, c1 second; c1's prior pin survives, c3 stays unpinned.
	body := bytes.NewBufferString(`{"user_id":"u1","ids":["c2","c1"]}`)
	req := httptest.NewRequest("PUT", "/api/conversation-pin-order", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	byID := map[string]store.Conversation{}
	for _, c := range ms.Conversations {
		byID[c.ID] = c
	}
	if !byID["c2"].Pinned || byID["c2"].PinOrder != 0 {
		t.Errorf("c2 should be pinned at order 0, got pinned=%v order=%d", byID["c2"].Pinned, byID["c2"].PinOrder)
	}
	if !byID["c1"].Pinned || byID["c1"].PinOrder != 1 {
		t.Errorf("c1 should be pinned at order 1, got pinned=%v order=%d", byID["c1"].Pinned, byID["c1"].PinOrder)
	}
	if byID["c3"].Pinned {
		t.Errorf("c3 should remain unpinned")
	}
}

func TestConversation_ReorderPinned_DropsOmittedPins(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "u1", Pinned: true, PinOrder: 0},
		{ID: "c2", UserID: "u1", Pinned: true, PinOrder: 1},
	}
	r := setupRouter(ms)

	// Only c2 stays pinned; c1 (omitted) must be unpinned.
	body := bytes.NewBufferString(`{"user_id":"u1","ids":["c2"]}`)
	req := httptest.NewRequest("PUT", "/api/conversation-pin-order", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	for _, c := range ms.Conversations {
		if c.ID == "c1" && c.Pinned {
			t.Errorf("c1 should be unpinned after being dropped from the order")
		}
		if c.ID == "c2" && !c.Pinned {
			t.Errorf("c2 should remain pinned")
		}
	}
}

func TestConversation_ClearContext_RotatesSessionAndClearsUsage(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID:               "c1",
		UserID:           "u1",
		SessionID:        "old-session",
		LastContextUsage: `{"used":42,"total":200000}`,
	}}
	r := setupRouter(ms)

	req := httptest.NewRequest("POST", "/api/conversations/c1/clear-context", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	var got store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SessionID == "old-session" {
		t.Errorf("expected session_id to be rotated, still %q", got.SessionID)
	}
	if got.LastContextUsage != "" {
		t.Errorf("expected last_context_usage cleared, got %q", got.LastContextUsage)
	}

	// Stored row should match the response.
	stored, _ := ms.GetConversation(context.Background(), "c1")
	if stored.SessionID != got.SessionID {
		t.Errorf("response/store drift: %q vs %q", got.SessionID, stored.SessionID)
	}
}

func TestConversation_ClearContext_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupRouter(ms)

	req := httptest.NewRequest("POST", "/api/conversations/missing/clear-context", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestConversation_Create_CopiesUserWorkDir(t *testing.T) {
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1", Name: "Alice", WorkDir: "/home/alice"})
	r := setupRouter(ms)

	body := bytes.NewBufferString(`{"title":"New","user_id":"u1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	var conv store.Conversation
	if err := json.NewDecoder(rec.Body).Decode(&conv); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if conv.WorkDir != "/home/alice" {
		t.Errorf("expected work_dir '/home/alice', got '%s'", conv.WorkDir)
	}
}

// Authorization checks: with auth context, accessing another login-able human's
// conversation must be blocked. Agent-owned conversations remain reachable.
func TestConversation_Get_BlocksOtherHumansConv(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice"},
		{ID: "bob", Username: "bob"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "bobs-conv", UserID: "bob"},
	}
	r := setupAuthedRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/conversations/bobs-conv", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestConversation_Delete_BlocksOtherHumansConv(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice"},
		{ID: "bob", Username: "bob"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "bobs-conv", UserID: "bob"},
	}
	r := setupAuthedRouter(ms, "alice")

	req := httptest.NewRequest("DELETE", "/api/conversations/bobs-conv", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if len(ms.Conversations) != 1 {
		t.Errorf("conversation must not be deleted on forbidden access")
	}
}

func TestConversation_Get_AllowsOwnAgentConv(t *testing.T) {
	// Conversations owned by an agent stay reachable for the agent's
	// owner — the human creator, identified by a shared email.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice", Email: "alice@x"},
		{ID: "agent", Username: "", Email: "alice@x", OwnerID: "alice"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "agent-conv", UserID: "agent"},
	}
	r := setupAuthedRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/conversations/agent-conv", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- Conversation -> agent -> owner: cross-owner isolation --------------
//
// A conversation is owned by an agent, an agent is owned by exactly one
// human (via shared email). bob must therefore be unable to read, list,
// modify, or delete a conversation that lives under alice's agent — even
// though the agent itself has no `username`, which used to be the only
// gate.

// twoOwnersConvFixture seeds the canonical "alice has an agent + a
// conversation under that agent; bob is a separate human" layout.
func twoOwnersConvFixture(authedUID string) (*storetest.Fake, *gin.Engine) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice", Email: "alice@x", WorkDir: "/tmp/a"},
		{ID: "bob", Username: "bob", Email: "bob@x", WorkDir: "/tmp/b"},
		{ID: "alice-agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/tmp/aa"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "alice-agent-conv", UserID: "alice-agent", WorkDir: "/tmp/aa"},
	}
	ms.Messages = map[string][]store.Message{
		"alice-agent-conv": {{ID: "m1", ConversationID: "alice-agent-conv", Role: "user", Content: "hi"}},
	}
	return ms, setupAuthedRouter(ms, authedUID)
}

func TestConversation_Get_BlocksOtherOwnersAgentConv(t *testing.T) {
	_, r := twoOwnersConvFixture("bob")

	req := httptest.NewRequest("GET", "/api/conversations/alice-agent-conv", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (bob -> alice's agent's conv), got %d", rec.Code)
	}
}

func TestConversation_GetMessages_BlocksOtherOwnersAgentConv(t *testing.T) {
	// Even reading the message log must be gated — otherwise bob could
	// scrape what alice's agent said in past sessions.
	_, r := twoOwnersConvFixture("bob")

	req := httptest.NewRequest("GET", "/api/conversations/alice-agent-conv/messages", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestConversation_Delete_BlocksOtherOwnersAgentConv(t *testing.T) {
	ms, r := twoOwnersConvFixture("bob")

	req := httptest.NewRequest("DELETE", "/api/conversations/alice-agent-conv", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if len(ms.Conversations) != 1 {
		t.Errorf("alice's agent conversation must not be deleted by bob")
	}
}

func TestConversation_List_BlocksFilterByOtherOwnersAgent(t *testing.T) {
	// bob asking for "give me conversations under alice-agent" must be
	// rejected at the user_id gate — the agent isn't his.
	_, r := twoOwnersConvFixture("bob")

	req := httptest.NewRequest("GET", "/api/conversations?user_id=alice-agent", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestConversation_Create_BlocksUnderOtherOwnersAgent(t *testing.T) {
	// Even creating a fresh conversation under another human's agent
	// must be blocked — that's how a hostile user could plant prompts
	// against someone else's filesystem.
	_, r := twoOwnersConvFixture("bob")

	body := bytes.NewBufferString(`{"title":"sneaky","user_id":"alice-agent"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestConversation_AdminBlockedFromOtherOwnersAgentConv(t *testing.T) {
	// Strict ownership: admin gets no shortcut here either. A
	// conversation belongs to an agent which belongs to one human; admin
	// reaching across is 403, same as any other unrelated user.
	ms, _ := twoOwnersConvFixture("admin")
	ms.Users = append(ms.Users, store.User{ID: "admin", Username: "admin", Email: "admin@x", IsAdmin: true})
	r := setupAuthedRouter(ms, "admin")

	req := httptest.NewRequest("GET", "/api/conversations/alice-agent-conv", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (admin must follow ownership rules), got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestConversation_List_BlocksOtherHumansUserID(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice"},
		{ID: "bob", Username: "bob"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "bobs-conv", UserID: "bob"},
	}
	r := setupAuthedRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/conversations?user_id=bob", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestConversation_Create_BlocksOtherHumansUserID(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice"},
		{ID: "bob", Username: "bob"},
	}
	r := setupAuthedRouter(ms, "alice")

	body := bytes.NewBufferString(`{"title":"x","user_id":"bob"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestConversation_List_BlocksOwnHumanUserID(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice"},
		{ID: "alice-agent", OwnerID: "alice"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "human-conv", UserID: "alice"},
	}
	r := setupAuthedRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/conversations?user_id=alice", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
}

func TestConversation_Create_BlocksOwnHumanUserID(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Username: "alice"},
		{ID: "alice-agent", OwnerID: "alice"},
	}
	r := setupAuthedRouter(ms, "alice")

	body := bytes.NewBufferString(`{"title":"human-owned","user_id":"alice"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body.String())
	}
	if len(ms.Conversations) != 0 {
		t.Fatalf("human-owned conversation should not be created: %+v", ms.Conversations)
	}
}

// setupRouterWithHub wires a ConversationHandler whose UserHub fan-out
// is also exercised. Used by the lifecycle-broadcast tests so we can
// observe what other tabs of the same user would receive in real time.
func setupRouterWithHub(ms *storetest.Fake, hub *service.UserHub) *gin.Engine {
	r := gin.New()
	h := NewConversationHandler(ms)
	h.Cfg = &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude},
	}}
	h.UserHub = hub
	r.GET("/api/conversations", h.List)
	r.POST("/api/conversations", h.Create)
	r.GET("/api/conversations/:id", h.Get)
	r.DELETE("/api/conversations/:id", h.Delete)
	r.PUT("/api/conversations/:id/title", h.UpdateTitle)
	r.PUT("/api/conversations/:id/notifications", h.UpdateNotifications)
	r.PUT("/api/conversations/:id/pinned", h.UpdatePinned)
	return r
}

// drainHubChannel reads everything currently buffered in ch into a slice
// without blocking. Test helper for assertions on user-hub broadcasts.
func drainHubChannel(ch chan []byte) [][]byte {
	out := [][]byte{}
	for {
		select {
		case data := <-ch:
			out = append(out, data)
		default:
			return out
		}
	}
}

func TestConversation_Create_BroadcastsConversationAddedToUserHub(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Username: "alice", WorkDir: "/home/alice"}}
	hub := service.NewUserHub()
	peer := make(chan []byte, 4)
	hub.Join("u1", "peer-tab", peer)
	r := setupRouterWithHub(ms, hub)

	body := bytes.NewBufferString(`{"title":"new","user_id":"u1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	events := drainHubChannel(peer)
	if len(events) != 1 {
		t.Fatalf("expected 1 user-hub event, got %d", len(events))
	}
	var evt struct {
		Type           string              `json:"type"`
		ConversationID string              `json:"conversation_id"`
		Conversation   *store.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(events[0], &evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Type != "conversation_added" {
		t.Errorf("expected type=conversation_added, got %q", evt.Type)
	}
	if evt.Conversation == nil || evt.Conversation.UserID != "u1" {
		t.Errorf("expected payload to carry the new conversation, got %+v", evt.Conversation)
	}
}

func TestConversation_Delete_BroadcastsConversationRemovedToUserHub(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c-doomed", UserID: "u1"}}
	hub := service.NewUserHub()
	peer := make(chan []byte, 4)
	hub.Join("u1", "peer-tab", peer)
	r := setupRouterWithHub(ms, hub)

	req := httptest.NewRequest("DELETE", "/api/conversations/c-doomed", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	events := drainHubChannel(peer)
	if len(events) != 1 {
		t.Fatalf("expected 1 user-hub event, got %d", len(events))
	}
	var evt struct {
		Type           string `json:"type"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(events[0], &evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Type != "conversation_removed" || evt.ConversationID != "c-doomed" {
		t.Errorf("unexpected event payload: %+v", evt)
	}
}

func TestConversation_UpdateTitle_BroadcastsConversationUpdatedToUserHub(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Title: "old"}}
	hub := service.NewUserHub()
	peer := make(chan []byte, 4)
	hub.Join("u1", "peer-tab", peer)
	other := make(chan []byte, 4)
	hub.Join("u-other", "stranger", other)
	r := setupRouterWithHub(ms, hub)

	body := bytes.NewBufferString(`{"title":"renamed"}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/title", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	events := drainHubChannel(peer)
	if len(events) != 1 {
		t.Fatalf("expected 1 user-hub event for owner, got %d", len(events))
	}
	if got := drainHubChannel(other); len(got) != 0 {
		t.Errorf("foreign user must not receive update, got %v", got)
	}
	var evt struct {
		Type         string              `json:"type"`
		Conversation *store.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(events[0], &evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Type != "conversation_updated" {
		t.Errorf("expected type=conversation_updated, got %q", evt.Type)
	}
	if evt.Conversation == nil || evt.Conversation.Title != "renamed" {
		t.Errorf("expected updated title in payload, got %+v", evt.Conversation)
	}
}

// Agent-owned conversations broadcast to the human owner's hub room, not
// the agent's, because WebSocket clients subscribe under the human's id
// (see terminal_ws.go's hubUserID derivation). Without this resolution
// the create/update/delete fan-outs would land in an empty room and peer
// tabs would only see the change on the next REST refresh.
func TestConversation_Create_BroadcastsToHumanOwner_WhenConvBelongsToAgent(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-alice", Username: "alice", Email: "alice@example.com", WorkDir: "/home/alice"},
		// Agent: empty Username, Email mirrors the human owner — the same
		// shape canAccessOwner / GetOwner expect in production.
		{ID: "agent-1", Email: "alice@example.com", WorkDir: "/home/alice/projects/proj"},
	}
	hub := service.NewUserHub()
	humanTab := make(chan []byte, 4)
	agentRoom := make(chan []byte, 4)
	hub.Join("human-alice", "alice-tab", humanTab)
	// Subscribe a listener to the agent id too so we can prove nothing
	// lands there — the legacy direct-broadcast would have hit this.
	hub.Join("agent-1", "ghost", agentRoom)
	r := setupRouterWithHub(ms, hub)

	body := bytes.NewBufferString(`{"title":"new","user_id":"agent-1"}`)
	req := httptest.NewRequest("POST", "/api/conversations", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	humanEvents := drainHubChannel(humanTab)
	if len(humanEvents) != 1 {
		t.Fatalf("expected 1 event in the human owner's room, got %d", len(humanEvents))
	}
	if got := drainHubChannel(agentRoom); len(got) != 0 {
		t.Errorf("agent-keyed room must stay empty (events should resolve to the human), got %d", len(got))
	}
	var evt struct {
		Type         string              `json:"type"`
		Conversation *store.Conversation `json:"conversation"`
	}
	if err := json.Unmarshal(humanEvents[0], &evt); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if evt.Type != "conversation_added" {
		t.Errorf("expected conversation_added, got %q", evt.Type)
	}
	if evt.Conversation == nil || evt.Conversation.UserID != "agent-1" {
		t.Errorf("payload should still carry the agent's user id, got %+v", evt.Conversation)
	}
}

func TestConversation_UpdateNotifications_BroadcastsToUserHub(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", NotificationsEnabled: true}}
	hub := service.NewUserHub()
	peer := make(chan []byte, 4)
	hub.Join("u1", "peer-tab", peer)
	r := setupRouterWithHub(ms, hub)

	body := bytes.NewBufferString(`{"enabled":false}`)
	req := httptest.NewRequest("PUT", "/api/conversations/c1/notifications", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	events := drainHubChannel(peer)
	if len(events) != 1 {
		t.Fatalf("expected 1 user-hub event, got %d", len(events))
	}
}
