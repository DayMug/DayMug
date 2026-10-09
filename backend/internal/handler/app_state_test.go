package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupAppStateRouter(t *testing.T, h *AppStateHandler, callerID string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api")
	g.Use(adminAuthCtx(callerID, false))
	g.GET("/app-state", h.Get)
	return r
}

func TestAppState_ReturnsAuthUserAndVisibleUsers(t *testing.T) {
	ms := storetest.New()
	// Caller plus their owned agent, plus an unrelated human and an agent
	// owned by that other human — only the caller's row and their own
	// agent should come back.
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice", Email: "alice@example.com", WorkDir: "/home/alice"},
		{ID: "agent-a1", Name: "Researcher", Email: "alice@example.com", OwnerID: "human-a"},
		{ID: "agent-a2", Name: "Archived", Email: "alice@example.com", OwnerID: "human-a", Archived: true},
		{ID: "human-b", Name: "Bob", Username: "bob", Email: "bob@example.com"},
		{ID: "agent-b1", Name: "Builder", Email: "bob@example.com", OwnerID: "human-b"},
	}
	h := NewAppStateHandler(ms, nil, "1.2.3")
	r := setupAppStateRouter(t, h, "human-a")

	w := doJSON(r, "GET", "/api/app-state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		AuthUser struct {
			ID      string `json:"id"`
			WorkDir string `json:"work_dir"`
		} `json:"auth_user"`
		Users         []store.User         `json:"users"`
		ServerInfo    map[string]any       `json:"server_info"`
		Conversations []store.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AuthUser.ID != "human-a" {
		t.Errorf("expected auth_user.id=human-a, got %q", body.AuthUser.ID)
	}
	if body.AuthUser.WorkDir != "/home/alice" {
		t.Errorf("expected auth_user.work_dir to round-trip, got %q", body.AuthUser.WorkDir)
	}
	// The caller's own human self-row is no longer part of the picker — only
	// their (non-archived) agent survives.
	if len(body.Users) != 1 {
		t.Fatalf("expected exactly agent-a1 in users, got %d (%+v)", len(body.Users), body.Users)
	}
	have := map[string]bool{}
	for _, u := range body.Users {
		have[u.ID] = true
	}
	if !have["agent-a1"] {
		t.Errorf("missing owned agent in users, got %+v", body.Users)
	}
	if have["human-a"] {
		t.Errorf("caller's human self-row should be hidden from the picker: %+v", body.Users)
	}
	if have["human-b"] || have["agent-b1"] {
		t.Errorf("leaked another human's rows: %+v", body.Users)
	}
	if have["agent-a2"] {
		t.Errorf("leaked archived agent: %+v", body.Users)
	}
	if body.ServerInfo["version"] != "1.2.3" {
		t.Errorf("expected server_info.version=1.2.3, got %v", body.ServerInfo["version"])
	}
	// No ?user_id= → conversations omitted (null on the wire).
	if body.Conversations != nil {
		t.Errorf("expected conversations omitted without ?user_id, got %+v", body.Conversations)
	}
}

func TestAppState_AttachesBotPlatformsToColdStartUsers(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "human-a", Username: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent-a1", OwnerID: "human-a", Name: "Guard"}); err != nil {
		t.Fatal(err)
	}
	for _, bot := range []store.Bot{
		{ID: "slack-bot", AgentID: "agent-a1", Name: "Slack", Platform: "slack", Enabled: true},
		{ID: "feishu-bot", AgentID: "agent-a1", Name: "Feishu", Platform: "feishu", Enabled: false},
	} {
		if err := s.CreateBot(ctx, bot); err != nil {
			t.Fatal(err)
		}
	}

	h := NewAppStateHandler(s, nil, "v")
	r := setupAppStateRouter(t, h, "human-a")
	w := doJSON(r, "GET", "/api/app-state", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Users []store.User `json:"users"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Users) != 1 {
		t.Fatalf("users = %+v, want one agent", body.Users)
	}
	platforms := body.Users[0].BotPlatforms
	havePlatforms := make(map[string]bool, len(platforms))
	for _, platform := range platforms {
		havePlatforms[platform] = true
	}
	if len(platforms) != 2 || !havePlatforms["slack"] || !havePlatforms["feishu"] {
		t.Fatalf("bot_platforms = %v, want slack and feishu", platforms)
	}
}

func TestAppState_PreloadsConversationsForUserIDParam(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice", Email: "alice@example.com"},
		{ID: "agent-a1", Name: "Researcher", Email: "alice@example.com", OwnerID: "human-a"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "c1", UserID: "agent-a1", Title: "Task one"},
		{ID: "c2", UserID: "agent-a1", Title: "Task two"},
	}
	h := NewAppStateHandler(ms, nil, "v")
	r := setupAppStateRouter(t, h, "human-a")

	w := doJSON(r, "GET", "/api/app-state?user_id=agent-a1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		PreloadedUserID string               `json:"preloaded_user_id"`
		Conversations   []store.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.PreloadedUserID != "agent-a1" {
		t.Errorf("expected preloaded_user_id to echo, got %q", body.PreloadedUserID)
	}
	if len(body.Conversations) != 2 {
		t.Fatalf("expected 2 conversations preloaded, got %d", len(body.Conversations))
	}
}

func TestAppState_RefusesPreloadForUnauthorisedUser(t *testing.T) {
	// Caller asks to preload conversations for another human's agent —
	// the access check kicks the request to "omit conversations" rather
	// than 403, so the SPA still gets its identity payload and falls back
	// to the per-user fetch.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice", Email: "alice@example.com"},
		{ID: "human-b", Name: "Bob", Username: "bob", Email: "bob@example.com"},
		{ID: "agent-b1", Name: "Builder", Email: "bob@example.com", OwnerID: "human-b"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "x", UserID: "agent-b1", Title: "Other"},
	}
	h := NewAppStateHandler(ms, nil, "v")
	r := setupAppStateRouter(t, h, "human-a")

	w := doJSON(r, "GET", "/api/app-state?user_id=agent-b1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Conversations []store.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Conversations != nil {
		t.Errorf("expected no conversations for unauthorised preload, got %+v", body.Conversations)
	}
}

func TestAppState_RefusesPreloadForHumanUserID(t *testing.T) {
	// Stale URLs/localStorage can still carry a human login id from before the
	// agent-only picker. Do not preload those conversations: the chat surface
	// must fall back to a visible agent instead of rehydrating a hidden human
	// owner.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human-a", Name: "Alice", Username: "alice", Email: "alice@example.com"},
		{ID: "agent-a1", Name: "Researcher", Email: "alice@example.com", OwnerID: "human-a"},
	}
	ms.Conversations = []store.Conversation{
		{ID: "human-conv", UserID: "human-a", Title: "Hidden"},
	}
	h := NewAppStateHandler(ms, nil, "v")
	r := setupAppStateRouter(t, h, "human-a")

	w := doJSON(r, "GET", "/api/app-state?user_id=human-a", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Conversations []store.Conversation `json:"conversations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Conversations != nil {
		t.Errorf("expected no conversations for human preload, got %+v", body.Conversations)
	}
}
