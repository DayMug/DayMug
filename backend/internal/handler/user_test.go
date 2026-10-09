package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupUserRouter(ms *storetest.Fake) *gin.Engine {
	r := gin.New()
	h := NewUserHandler(ms)
	r.GET("/api/users", h.List)
	r.POST("/api/users", h.Create)
	r.PUT("/api/users/:id", h.Update)
	r.POST("/api/users/:id/duplicate", h.Duplicate)
	r.DELETE("/api/users/:id", h.Delete)
	return r
}

// setupAuthedUserRouter is like setupUserRouter but also installs a fake
// auth middleware that pins the authenticated user id to authedUID. Used
// to exercise canAccessOwner — a human user can manage themselves and any
// agent (User row with no username), but not another human user.
func setupAuthedUserRouter(ms *storetest.Fake, authedUID string) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	h := NewUserHandler(ms)
	r.GET("/api/users", h.List)
	r.POST("/api/users", h.Create)
	r.PUT("/api/users/:id", h.Update)
	r.POST("/api/users/:id/duplicate", h.Duplicate)
	r.DELETE("/api/users/:id", h.Delete)
	return r
}

func TestUser_Create(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"name":"Alice","work_dir":"/home/alice","avatar":"A"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var user store.User
	if err := json.NewDecoder(rec.Body).Decode(&user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected name 'Alice', got '%s'", user.Name)
	}
	if user.WorkDir != "/home/alice" {
		t.Errorf("expected work_dir '/home/alice', got '%s'", user.WorkDir)
	}
	if user.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestUserCreatePersistsOptionalDefaultModel(t *testing.T) {
	ms := storetest.New()
	r := gin.New()
	h := NewUserHandler(ms)
	useAccountModels(t, map[string]service.AccountModels{
		"claude-main": {Models: []string{"claude-opus-4-8"}},
	})
	h.Cfg = &config.Config{Providers: []config.Provider{{
		Name: "claude-main", Type: config.CLITypeClaude,
	}}}
	r.POST("/api/users", h.Create)

	body := bytes.NewBufferString(`{"name":"Agent","work_dir":"/tmp/agent","default_model":" claude-opus-4-8 "}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.DefaultModel != "claude-opus-4-8" {
		t.Fatalf("default_model = %q, want claude-opus-4-8", got.DefaultModel)
	}
}

func TestUserCreateRejectsUnknownDefaultModel(t *testing.T) {
	ms := storetest.New()
	r := gin.New()
	h := NewUserHandler(ms)
	useAccountModels(t, map[string]service.AccountModels{
		"claude-main": {Models: []string{"claude-opus-4-8"}},
	})
	h.Cfg = &config.Config{Providers: []config.Provider{{
		Name: "claude-main", Type: config.CLITypeClaude,
	}}}
	r.POST("/api/users", h.Create)

	body := bytes.NewBufferString(`{"name":"Agent","work_dir":"/tmp/agent","default_model":"missing-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if len(ms.Users) != 0 {
		t.Fatalf("unknown model must not create a subject: %+v", ms.Users)
	}
}

func TestUserCreateIgnoresLegacyBotFields(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)
	body := bytes.NewBufferString(`{
		"kind":"bot","name":"Old Bot Persona","work_dir":"/tmp/bot",
		"bot_platform":"slack","bot_token":"legacy-secret"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"kind"`) || strings.Contains(rec.Body.String(), `"bot_token"`) {
		t.Fatalf("legacy bot fields leaked into agent response: %s", rec.Body.String())
	}
}

func TestUser_Create_MissingName(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"work_dir":"/tmp"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUser_Create_MissingWorkDir(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"name":"Bob"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUser_List(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "u1", Name: "Alice", WorkDir: "/home/alice"},
		{ID: "u2", Name: "Bob", WorkDir: "/home/bob"},
	}
	r := setupUserRouter(ms)

	req := httptest.NewRequest("GET", "/api/users", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var users []store.User
	if err := json.NewDecoder(rec.Body).Decode(&users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
}

func TestUser_List_FiltersOtherHumansAndUnownedAgents(t *testing.T) {
	// Visibility model: a human sees only the agents they own
	// (agent.owner_id == own.id). Their own human self-row is hidden (a login
	// account is not a chattable persona); other humans, agents owned by other
	// humans, and orphan agents (empty owner_id) must all be filtered out too.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/tmp/a"},
		{ID: "bob", Name: "Bob", Username: "bob", Email: "bob@x", WorkDir: "/tmp/b"},
		{ID: "alice-agent", Name: "Alice's Agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/tmp/aa"},
		{ID: "bob-agent", Name: "Bob's Agent", Username: "", Email: "bob@x", OwnerID: "bob", WorkDir: "/tmp/ba"},
		{ID: "orphan", Name: "Orphan", Username: "", Email: "", WorkDir: "/tmp/orphan"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	req := httptest.NewRequest("GET", "/api/users", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, u := range got {
		seen[u.ID] = true
	}
	if seen["alice"] {
		t.Error("caller's human self-row should be hidden from the picker")
	}
	if !seen["alice-agent"] {
		t.Errorf("alice's own agent missing: %+v", seen)
	}
	if seen["bob"] {
		t.Error("another human (bob) leaked into list")
	}
	if seen["bob-agent"] {
		t.Error("another human's agent (bob-agent) leaked into list")
	}
	if seen["orphan"] {
		t.Error("orphan agent (no owner email) leaked into non-admin list")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly alice-agent (1 row), got %d: %+v", len(got), got)
	}
}

func TestUser_List_AdminFollowsRegularRules(t *testing.T) {
	// Strict ownership: admin gets no shortcut on /api/users. The chat
	// page's agent picker must only surface admin's own agents — the same
	// shape every other human sees (self-row hidden). Cross-team agent
	// management is intentionally NOT done from this list (use direct DB
	// or, if/when added, /api/admin/* endpoints).
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "admin", Name: "Admin", Username: "admin", Email: "admin@x", IsAdmin: true},
		{ID: "admin-agent", Name: "Admin's Agent", Username: "", Email: "admin@x", OwnerID: "admin"},
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x"},
		{ID: "alice-agent", Name: "Alice's Agent", Username: "", Email: "alice@x", OwnerID: "alice"},
		{ID: "orphan", Name: "Orphan", Username: "", Email: ""},
	}
	r := setupAuthedUserRouter(ms, "admin")

	req := httptest.NewRequest("GET", "/api/users", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, u := range got {
		seen[u.ID] = true
	}
	if seen["admin"] {
		t.Error("admin's own human self-row should be hidden from the picker")
	}
	if !seen["admin-agent"] {
		t.Errorf("admin must see own agent, got %+v", seen)
	}
	if seen["alice"] || seen["alice-agent"] || seen["orphan"] {
		t.Errorf("admin must NOT see other humans / others' agents / orphans, got %+v", seen)
	}
	if len(got) != 1 {
		t.Errorf("admin should see exactly own-agent (1 row), got %d: %+v", len(got), got)
	}
}

func TestUser_Delete(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}
	r := setupUserRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/users/u1", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(ms.Users) != 0 {
		t.Error("expected user to be deleted")
	}
}

func TestUser_Update(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp", Avatar: "A"}}
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"name":"Alice Updated","work_dir":"/home/alice","avatar":"B"}`)
	req := httptest.NewRequest("PUT", "/api/users/u1", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var user store.User
	if err := json.NewDecoder(rec.Body).Decode(&user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user.Name != "Alice Updated" {
		t.Errorf("expected name 'Alice Updated', got '%s'", user.Name)
	}
	if user.Avatar != "B" {
		t.Errorf("expected avatar 'B', got '%s'", user.Avatar)
	}
}

func TestUser_Update_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"name":"Ghost","work_dir":"/tmp"}`)
	req := httptest.NewRequest("PUT", "/api/users/missing", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUser_Update_MissingName(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"work_dir":"/tmp"}`)
	req := httptest.NewRequest("PUT", "/api/users/u1", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUser_Create_WithRole(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"name":"Alice","work_dir":"/tmp","role_definition":"Backend dev"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var user store.User
	if err := json.NewDecoder(rec.Body).Decode(&user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user.RoleDefinition != "Backend dev" {
		t.Errorf("expected role_definition 'Backend dev', got '%s'", user.RoleDefinition)
	}
}

func TestUser_Update_WithRole(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Alice", WorkDir: "/tmp"}}
	r := setupUserRouter(ms)

	body := bytes.NewBufferString(`{"name":"Alice","work_dir":"/tmp","avatar":"A","role_definition":"SRE"}`)
	req := httptest.NewRequest("PUT", "/api/users/u1", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var user store.User
	if err := json.NewDecoder(rec.Body).Decode(&user); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if user.RoleDefinition != "SRE" {
		t.Errorf("expected role_definition 'SRE', got '%s'", user.RoleDefinition)
	}
}

func TestUser_Delete_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	req := httptest.NewRequest("DELETE", "/api/users/missing", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUser_Duplicate(t *testing.T) {
	ms := storetest.New()
	if err := ms.CreateUser(context.Background(), store.User{
		ID:              "src",
		Name:            "Backend Go",
		WorkDir:         "/home/alice/code/server",
		Avatar:          "BG",
		RoleDefinition:  "Senior backend engineer",
		McpConfig:       `{"mcpServers":{}}`,
		ClaudeMdContent: "# Project rules",
		ManageClaudeMd:  true,
		BarkURL:         "https://api.day.app/abc",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := setupUserRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/src/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var dup store.User
	if err := json.NewDecoder(rec.Body).Decode(&dup); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dup.ID == "" || dup.ID == "src" {
		t.Errorf("expected fresh id, got %q", dup.ID)
	}
	if dup.Name != "Backend Go (copy)" {
		t.Errorf("expected name 'Backend Go (copy)', got %q", dup.Name)
	}
	// Config fields are mirrored verbatim from the source agent.
	if dup.WorkDir != "/home/alice/code/server" {
		t.Errorf("work_dir not copied, got %q", dup.WorkDir)
	}
	if dup.RoleDefinition != "Senior backend engineer" {
		t.Errorf("role_definition not copied, got %q", dup.RoleDefinition)
	}
	// Notification config now lives on the human owner, not the agent —
	// duplicating an agent must not carry the source's stale BarkURL into
	// the new row, even when the source happens to have one set.
	if dup.BarkURL != "" {
		t.Errorf("bark_url should not be copied to a duplicated agent, got %q", dup.BarkURL)
	}
	if !dup.ManageClaudeMd {
		t.Errorf("manage_claude_md not copied")
	}
}

func TestUser_Update_AgentByOwner(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human", Name: "Human", Username: "human", Email: "human@x", WorkDir: "/tmp"},
		{ID: "agent", Name: "Agent", Username: "", Email: "human@x", OwnerID: "human", WorkDir: "/tmp/agent"},
	}
	r := setupAuthedUserRouter(ms, "human")

	body := bytes.NewBufferString(`{"name":"Agent Renamed","work_dir":"/tmp/agent"}`)
	req := httptest.NewRequest("PUT", "/api/users/agent", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Agent Renamed" {
		t.Errorf("expected updated name, got %q", got.Name)
	}
}

func TestUser_Update_OtherHumanForbidden(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", WorkDir: "/tmp/a"},
		{ID: "bob", Name: "Bob", Username: "bob", WorkDir: "/tmp/b"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	body := bytes.NewBufferString(`{"name":"Hacked","work_dir":"/tmp/b"}`)
	req := httptest.NewRequest("PUT", "/api/users/bob", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUser_Delete_AgentByOwner(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human", Name: "Human", Username: "human", Email: "human@x", WorkDir: "/tmp"},
		{ID: "agent", Name: "Agent", Username: "", Email: "human@x", OwnerID: "human", WorkDir: "/tmp/agent"},
	}
	r := setupAuthedUserRouter(ms, "human")

	req := httptest.NewRequest("DELETE", "/api/users/agent", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUser_Delete_SelfHumanRejected(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human", Name: "Human", Username: "human", Email: "human@x", WorkDir: "/tmp"},
	}
	r := setupAuthedUserRouter(ms, "human")

	req := httptest.NewRequest("DELETE", "/api/users/human", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(ms.Users) != 1 {
		t.Errorf("human user should remain in store, got %d users", len(ms.Users))
	}
}

func TestUser_Delete_OtherHumanForbidden(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", WorkDir: "/tmp/a"},
		{ID: "bob", Name: "Bob", Username: "bob", WorkDir: "/tmp/b"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	req := httptest.NewRequest("DELETE", "/api/users/bob", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(ms.Users) != 2 {
		t.Errorf("expected bob to remain in store, got %d users", len(ms.Users))
	}
}

func TestUser_Duplicate_AgentByOwner(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "human", Name: "Human", Username: "human", Email: "human@x", WorkDir: "/tmp"},
		{ID: "agent", Name: "Agent", Username: "", Email: "human@x", OwnerID: "human", WorkDir: "/tmp/agent"},
	}
	r := setupAuthedUserRouter(ms, "human")

	req := httptest.NewRequest("POST", "/api/users/agent/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var dup store.User
	if err := json.NewDecoder(rec.Body).Decode(&dup); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The duplicate must inherit the source's owner pointer — that's what
	// keeps it bound to the same human. Re-stamping with the duplicator's
	// id would silently transfer ownership when an admin clones an agent
	// on behalf of someone else.
	if dup.OwnerID != "human" {
		t.Errorf("duplicate must preserve source.owner_id=human, got %q", dup.OwnerID)
	}
}

func TestUser_Duplicate_OtherHumanForbidden(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", WorkDir: "/tmp/a"},
		{ID: "bob", Name: "Bob", Username: "bob", WorkDir: "/tmp/b"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	req := httptest.NewRequest("POST", "/api/users/bob/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUser_Duplicate_NotFound(t *testing.T) {
	ms := storetest.New()
	r := setupUserRouter(ms)

	req := httptest.NewRequest("POST", "/api/users/missing/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ---- Agent ownership: agent belongs to exactly one human ----------------
//
// The visibility model is: agents are personae owned by the human who
// created them, identified by a shared email. Other humans must not
// be able to enumerate, read, modify, duplicate, or delete agents they
// don't own. Conversations belonging to an agent inherit the agent's
// owner — exercised in conversation-handler tests below.

// twoOwnersFixture seeds the store with the canonical "two humans, one
// agent each" layout used by the cross-owner isolation tests. Returns a
// router authenticated as `authedUID` so the test can hit handlers as
// either alice or bob.
func twoOwnersFixture(authedUID string) (*storetest.Fake, *gin.Engine) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/tmp/a"},
		{ID: "bob", Name: "Bob", Username: "bob", Email: "bob@x", WorkDir: "/tmp/b"},
		{ID: "alice-agent", Name: "Alice's Agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/tmp/aa"},
		{ID: "bob-agent", Name: "Bob's Agent", Username: "", Email: "bob@x", OwnerID: "bob", WorkDir: "/tmp/ba"},
	}
	return ms, setupAuthedUserRouter(ms, authedUID)
}

func TestUser_Update_OtherUsersAgentForbidden(t *testing.T) {
	// Tightening canAccessOwner: alice must not be able to mutate an
	// agent owned by bob, even though it has Username == "" (the old
	// gate that admitted any agent).
	_, r := twoOwnersFixture("alice")

	body := bytes.NewBufferString(`{"name":"Hijacked","work_dir":"/tmp/ba"}`)
	req := httptest.NewRequest("PUT", "/api/users/bob-agent", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when alice updates bob's agent, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUser_Delete_OtherUsersAgentForbidden(t *testing.T) {
	ms, r := twoOwnersFixture("alice")

	req := httptest.NewRequest("DELETE", "/api/users/bob-agent", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(ms.Users) != 4 {
		t.Errorf("bob's agent should remain in store, got %d users", len(ms.Users))
	}
}

func TestUser_Duplicate_OtherUsersAgentForbidden(t *testing.T) {
	_, r := twoOwnersFixture("alice")

	req := httptest.NewRequest("POST", "/api/users/bob-agent/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestUser_OrphanAgentForbiddenForEveryone(t *testing.T) {
	// An agent with no owner email is an orphan from the pre-stamping
	// days. Under strict ownership it's unreachable through the API for
	// every role — including admin. Recovery must be done out-of-band
	// (direct DB UPDATE).
	cases := []struct {
		name       string
		authedUID  string
		seedUsers  []store.User
		wantStatus int
	}{
		{
			name:      "regular human blocked",
			authedUID: "alice",
			seedUsers: []store.User{
				{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x"},
				{ID: "orphan", Name: "Orphan", Username: "", Email: "", WorkDir: "/tmp/orphan"},
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:      "admin also blocked",
			authedUID: "admin",
			seedUsers: []store.User{
				{ID: "admin", Name: "Admin", Username: "admin", Email: "admin@x", IsAdmin: true},
				{ID: "orphan", Name: "Orphan", Username: "", Email: "", WorkDir: "/tmp/orphan"},
			},
			wantStatus: http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			ms.Users = tc.seedUsers
			r := setupAuthedUserRouter(ms, tc.authedUID)

			body := bytes.NewBufferString(`{"name":"x","work_dir":"/tmp/orphan"}`)
			req := httptest.NewRequest("PUT", "/api/users/orphan", body)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUser_AdminCannotUpdateOtherUsersAgent(t *testing.T) {
	// Strict ownership: even admins do NOT get a free pass on agents they
	// don't own. The /api/users/:id endpoint must 403 for admins reaching
	// across to another human's agent — admin elevation lives elsewhere.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "admin", Name: "Admin", Username: "admin", Email: "admin@x", IsAdmin: true},
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x"},
		{ID: "alice-agent", Name: "Alice's Agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/tmp/aa"},
	}
	r := setupAuthedUserRouter(ms, "admin")

	body := bytes.NewBufferString(`{"name":"Hijacked","work_dir":"/tmp/aa"}`)
	req := httptest.NewRequest("PUT", "/api/users/alice-agent", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin must NOT bypass agent ownership; expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---- Create/Duplicate: ownership pointer plumbed through correctly ------

func TestUser_Create_StampsCreatorOwnerID(t *testing.T) {
	// New agents must be stamped with their creator's id in owner_id so the
	// visibility model has a referential pointer to bind on. Without this,
	// every newly-created agent would be born as an orphan that nobody sees.
	// The caller also needs a work_dir for the home-jail; with one set,
	// "/tmp/n" is outside it but we don't care here — Create's home-jail
	// fires before the owner stamp, so we point the new agent inside.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/tmp"},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", "alice"); c.Next() })
	h := NewUserHandler(ms)
	r.POST("/api/users", h.Create)

	body := bytes.NewBufferString(`{"name":"NewAgent","work_dir":"/tmp/n"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OwnerID != "alice" {
		t.Errorf("expected agent.owner_id=alice (creator's id), got %q", got.OwnerID)
	}
	if got.Username != "" {
		t.Errorf("agent must have empty username, got %q", got.Username)
	}
}

func TestUser_Create_ReturnsInheritedProviderAccounts(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{
			ID:               "alice",
			Name:             "Alice",
			Username:         "alice",
			Email:            "alice@x",
			WorkDir:          "/tmp",
			ProviderBindings: map[string]string{"claude": "default"},
			ProviderAccounts: map[string][]string{"claude": {"default", "opus"}},
		},
	}
	r := setupAuthedUserRouter(ms, "alice")

	body := bytes.NewBufferString(`{"name":"NewAgent","work_dir":"/tmp/n"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProviderBindings["claude"] != "default" {
		t.Errorf("provider_bindings.claude = %q, want default", got.ProviderBindings["claude"])
	}
	if accounts := got.ProviderAccounts["claude"]; len(accounts) != 2 || accounts[0] != "default" || accounts[1] != "opus" {
		t.Errorf("provider_accounts.claude = %v, want [default opus]", accounts)
	}
}

func TestUser_Create_AllowsCreatorWithoutEmail(t *testing.T) {
	// With referential ownership the agent points at its creator via
	// owner_id, so email is no longer load-bearing for ownership: a human
	// with no email (e.g. a local-password account) can own agents. The
	// create succeeds and the agent is stamped with the creator's id.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "noemail", Name: "Quiet", Username: "quiet", Email: "", WorkDir: "/tmp"},
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("auth_user_id", "noemail"); c.Next() })
	h := NewUserHandler(ms)
	r.POST("/api/users", h.Create)

	body := bytes.NewBufferString(`{"name":"x","work_dir":"/tmp/x"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OwnerID != "noemail" {
		t.Errorf("expected agent.owner_id=noemail (creator's id), got %q", got.OwnerID)
	}
}

func TestUser_Duplicate_AdminBlockedFromOtherOwnersAgent(t *testing.T) {
	// Strict ownership extends to Duplicate: even admins can't clone an
	// agent they don't own — the request 403s before any copy happens.
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "admin", Name: "Admin", Username: "admin", Email: "admin@x", IsAdmin: true},
		{ID: "bob", Name: "Bob", Username: "bob", Email: "bob@x"},
		{ID: "bob-agent", Name: "Bob's Agent", Username: "", Email: "bob@x", OwnerID: "bob", WorkDir: "/tmp/ba"},
	}
	r := setupAuthedUserRouter(ms, "admin")

	req := httptest.NewRequest("POST", "/api/users/bob-agent/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(ms.Users) != 3 {
		t.Errorf("no duplicate should have been created, got %d users", len(ms.Users))
	}
}

// ---- work_dir home jail -------------------------------------------------
//
// Independent of the email-based ownership gate: even the owner can't
// place a new agent's work_dir outside their own home tree. Test seeds
// always pair the human + their agent with the same Email so the
// strict-ownership check passes and these tests actually exercise the
// jail (not the cross-owner gate).

// TestUser_Create_WithinHome — the agent's work_dir lives inside the
// caller's home, so the home-jail accepts it.
func TestUser_Create_WithinHome(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/home/alice"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	body := bytes.NewBufferString(`{"name":"Agent","work_dir":"/home/alice/projects/x"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestUser_Create_OutsideHomeForbidden — agent creation must reject any
// path outside the caller's own work_dir, even one that looks like a
// prefix sibling (/home/alice2 vs /home/alice).
func TestUser_Create_OutsideHomeForbidden(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/home/alice"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	cases := []string{
		"/tmp/elsewhere",
		"/home/alice2", // prefix sibling — must NOT be treated as inside /home/alice
		"/",
		"/home",
	}
	for _, wd := range cases {
		body := bytes.NewBufferString(`{"name":"Agent","work_dir":"` + wd + `"}`)
		req := httptest.NewRequest("POST", "/api/users", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("work_dir=%q: expected 403, got %d body=%s", wd, rec.Code, rec.Body.String())
		}
	}
}

// TestUser_Create_NoCallerHome — caller has no work_dir of their own, so
// they have nothing to base the home-jail on. Refuse rather than fall
// back to "anywhere".
func TestUser_Create_NoCallerHome(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: ""},
	}
	r := setupAuthedUserRouter(ms, "alice")

	body := bytes.NewBufferString(`{"name":"Agent","work_dir":"/tmp/anything"}`)
	req := httptest.NewRequest("POST", "/api/users", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestUser_Update_ChangeWorkDirOutsideHomeForbidden — editing an agent's
// work_dir to escape the caller's home is refused, even when the caller
// is allowed to edit the agent itself.
func TestUser_Update_ChangeWorkDirOutsideHomeForbidden(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/home/alice"},
		{ID: "agent", Name: "Agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/home/alice/proj"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	body := bytes.NewBufferString(`{"name":"Agent","work_dir":"/tmp/elsewhere"}`)
	req := httptest.NewRequest("PUT", "/api/users/agent", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The stored row must not have changed.
	if got := ms.Users[1].WorkDir; got != "/home/alice/proj" {
		t.Errorf("work_dir leaked through validation: %q", got)
	}
}

// TestUser_Update_KeepLegacyWorkDir — agents that pre-date the jail may
// already sit outside the caller's home. Editing _other_ fields without
// touching work_dir must still succeed; the jail only fires on changes.
func TestUser_Update_KeepLegacyWorkDir(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/home/alice"},
		{ID: "agent", Name: "Agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/legacy/path"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	body := bytes.NewBufferString(`{"name":"Renamed","work_dir":"/legacy/path"}`)
	req := httptest.NewRequest("PUT", "/api/users/agent", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestUser_Duplicate_OutsideHomeForbidden — duplicating an agent that
// pre-dates the jail and lives outside the caller's home is refused.
// Unlike Update, Duplicate mints a fresh row, so it must use the same
// gate as Create rather than grandfathering.
func TestUser_Duplicate_OutsideHomeForbidden(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "alice", Name: "Alice", Username: "alice", Email: "alice@x", WorkDir: "/home/alice"},
		{ID: "agent", Name: "Agent", Username: "", Email: "alice@x", OwnerID: "alice", WorkDir: "/legacy/path"},
	}
	r := setupAuthedUserRouter(ms, "alice")

	req := httptest.NewRequest("POST", "/api/users/agent/duplicate", http.NoBody)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIsPathWithin(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"/home/alice", "/home/alice", true},
		{"/home/alice", "/home/alice/x", true},
		{"/home/alice", "/home/alice/x/y/z", true},
		{"/home/alice/", "/home/alice/x", true},
		{"/home/alice", "/home/alice2", false},   // prefix sibling
		{"/home/alice", "/home/alice2/x", false}, // prefix sibling
		{"/home/alice", "/home", false},
		{"/home/alice", "/", false},
		{"", "/home/alice", false},
		{"/home/alice", "", false},
	}
	for _, c := range cases {
		if got := isPathWithin(c.parent, c.child); got != c.want {
			t.Errorf("isPathWithin(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestListAttachesBotPlatformsToAgents(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "owner", Username: "alice", Name: "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent", OwnerID: "owner", Name: "Web Agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, store.Bot{ID: "b1", AgentID: "agent", Name: "Ops", Platform: "slack",
		Enabled: true, BotToken: "xoxb", BotAppToken: "xapp", Channels: `[]`}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, store.Bot{ID: "b2", AgentID: "agent", Name: "Off", Platform: "feishu",
		Enabled: false, Channels: `[]`}); err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "owner")
		c.Next()
	})
	h := NewUserHandler(s)
	h.Bots = s
	r.GET("/api/users", h.List)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/users", http.NoBody))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var users []store.User
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ID != "agent" {
		t.Fatalf("users = %+v", users)
	}
	// Attachment is independent of runtime state, so disabled bots must still
	// make the owning agent visually distinct in the chat agent picker.
	if len(users[0].BotPlatforms) != 2 || users[0].BotPlatforms[0] != "slack" || users[0].BotPlatforms[1] != "feishu" {
		t.Fatalf("bot_platforms = %v, want [slack feishu]", users[0].BotPlatforms)
	}
}

// TestUpdateKeepsBotPlatformsOnTheResponse guards the agent list badge. The
// frontend replaces its whole cached agent row with this response, so an
// undecorated payload silently drops bot_platforms and the settings list stops
// showing which platforms an agent is wired to until a full page reload.
func TestUpdateKeepsBotPlatformsOnTheResponse(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "daymug.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	root := t.TempDir()
	if err := s.CreateUser(ctx, store.User{ID: "owner", Username: "alice", Name: "Alice", WorkDir: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, store.User{ID: "agent", OwnerID: "owner", Name: "Web Agent", WorkDir: root}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBot(ctx, store.Bot{ID: "b1", AgentID: "agent", Name: "Ops", Platform: "telegram",
		Enabled: true, BotToken: "tok", Channels: `[]`}); err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "owner")
		c.Next()
	})
	h := NewUserHandler(s)
	h.Bots = s
	r.PUT("/api/users/:id", h.Update)

	body := bytes.NewBufferString(`{"name":"Renamed Agent","work_dir":"` + root + `"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/users/agent", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var updated store.User
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Renamed Agent" {
		t.Fatalf("name = %q, want the renamed agent", updated.Name)
	}
	if len(updated.BotPlatforms) != 1 || updated.BotPlatforms[0] != "telegram" {
		t.Fatalf("bot_platforms = %v, want [telegram]", updated.BotPlatforms)
	}
}
