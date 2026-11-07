package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

type mcpFixture struct {
	engine *gin.Engine
	hub    *service.DeviceHub
	token  string
	calls  chan service.DeviceFrame
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := storetest.New()
	ctx := context.Background()
	for _, u := range []store.User{{ID: "h1", Username: "alice"}, {ID: "a1", OwnerID: "h1"}} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateConversation(ctx, "c1", "", "a1", "", "claude", ""); err != nil {
		t.Fatal(err)
	}
	d, _ := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", TokenHash: "x"})
	if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: "d1", WorkDir: "/work"}); err != nil {
		t.Fatal(err)
	}
	hub := service.NewDeviceHub(s)
	f := &mcpFixture{hub: hub, token: hub.MCPToken("c1"), calls: make(chan service.DeviceFrame, 4)}
	var session *service.DeviceSession
	session = hub.Register(d, service.DeviceHello{Tools: []service.DeviceTool{{Name: "Bash", InputSchema: json.RawMessage(`{"type":"object"}`)}}},
		func(frame service.DeviceFrame) error {
			if frame.Type == "call" {
				f.calls <- frame
				go session.Deliver(service.DeviceFrame{Type: "result", ID: frame.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"hi"}]}`)})
			}
			return nil
		})
	h := &DeviceHandler{Store: s, Hub: hub, Version: "test"}
	f.engine = gin.New()
	f.engine.Any("/api/devices/mcp", h.MCP)
	return f
}

func (f *mcpFixture) post(t *testing.T, token, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/devices/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	return rec
}

func TestDeviceMCPRejectsNonLocalAndBadTokens(t *testing.T) {
	f := newMCPFixture(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	cases := map[string]func(*http.Request){
		"remote":    func(r *http.Request) { r.RemoteAddr = "203.0.113.5:1234" },
		"forwarded": func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.5") },
	}
	for name, mutate := range cases {
		if rec := f.post(t, f.token, body, mutate); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := f.post(t, "c1.bad", body, nil); rec.Code != http.StatusNotFound {
		t.Errorf("bad token: status %d", rec.Code)
	}
	if rec := f.post(t, f.token, body, nil); rec.Code != http.StatusOK {
		t.Errorf("valid: status %d", rec.Code)
	}
}

func TestDeviceMCPProtocol(t *testing.T) {
	f := newMCPFixture(t)

	rec := f.post(t, f.token, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, nil)
	if !strings.Contains(rec.Body.String(), `"protocolVersion":"2025-03-26"`) || !strings.Contains(rec.Body.String(), `"tools":{}`) {
		t.Fatalf("initialize = %s", rec.Body)
	}
	if rec := f.post(t, f.token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d", rec.Code)
	}
	rec = f.post(t, f.token, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if !strings.Contains(rec.Body.String(), `"name":"Bash"`) {
		t.Fatalf("tools/list = %s", rec.Body)
	}

	rec = f.post(t, f.token, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"Bash","arguments":{"command":"ls"}}}`, nil)
	if !strings.Contains(rec.Body.String(), `"text":"hi"`) {
		t.Fatalf("tools/call = %s", rec.Body)
	}
	call := <-f.calls
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		CWD       string          `json:"cwd"`
	}
	if err := json.Unmarshal(call.Params, &params); err != nil || call.Method != "tool" || params.Name != "Bash" || params.CWD != "/work" || string(params.Arguments) != `{"command":"ls"}` {
		t.Fatalf("relayed call = %+v %s", call, call.Params)
	}

	rec = f.post(t, f.token, `{"jsonrpc":"2.0","id":4,"method":"bogus"}`, nil)
	if !strings.Contains(rec.Body.String(), `-32601`) {
		t.Fatalf("unknown method = %s", rec.Body)
	}
}

// Files from the user's computer must never render as an active document of
// the server's origin.
func TestDeviceFileContentType(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"a.png", "\x89PNG\r\n\x1a\n", "image/png"},
		{"a.html", "<script>alert(1)</script>", "text/plain; charset=utf-8"},
		{"a.svg", "<svg onload=alert(1)>", "text/plain; charset=utf-8"},
		{"main.go", "package main\n", "text/plain; charset=utf-8"},
		{"doc.pdf", "%PDF-1.7", "application/pdf"},
		{"blob.bin", "\x00\x01\x02", "application/octet-stream"},
	} {
		if got := deviceFileContentType(tc.name, []byte(tc.data)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Activity lists only the device's own conversations that have a turn running,
// and needs the device token.
func TestDeviceActivity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := storetest.New()
	ctx := context.Background()
	for _, u := range []store.User{{ID: "h1", Username: "alice"}, {ID: "a1", OwnerID: "h1"}} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"busy", "idle", "elsewhere"} {
		if err := s.CreateConversation(ctx, id, "title "+id, "a1", "", "claude", ""); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", TokenHash: service.HashDeviceToken("dmd_tok1")})
	_, _ = s.CreateDevice(ctx, store.Device{ID: "d2", UserID: "h1", TokenHash: service.HashDeviceToken("dmd_tok2")})
	for conv, dev := range map[string]string{"busy": "d1", "idle": "d1", "elsewhere": "d2"} {
		if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: conv, DeviceID: dev, WorkDir: "/w"}); err != nil {
			t.Fatal(err)
		}
	}
	h := &DeviceHandler{Store: s, Hub: service.NewDeviceHub(s), Busy: func(id string) bool { return id != "idle" }}
	engine := gin.New()
	engine.GET("/api/devices/activity", h.Activity)

	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/activity", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	if w := get(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", w.Code)
	}
	w := get("dmd_tok1")
	var body struct {
		Running []deviceActivityConversation `json:"running"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("activity: %d %s", w.Code, w.Body)
	}
	if len(body.Running) != 1 || body.Running[0].ID != "busy" || body.Running[0].Title != "title busy" {
		t.Fatalf("running = %+v", body.Running)
	}
}

func TestConversationDeviceLockedAfterReply(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := storetest.New()
	ctx := context.Background()
	for _, u := range []store.User{{ID: "h1", Username: "alice"}, {ID: "a1", OwnerID: "h1"}} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateConversation(ctx, "c1", "", "a1", "", "claude", ""); err != nil {
		t.Fatal(err)
	}
	_, _ = s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", TokenHash: "x"})
	bind := func() {
		if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: "d1", WorkDir: "/w"}); err != nil {
			t.Fatal(err)
		}
	}
	h := &DeviceHandler{Store: s, Hub: service.NewDeviceHub(s)}
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("auth_user_id", "h1") })
	engine.PUT("/api/conversations/:id/device", h.SetConversationDevice)
	engine.DELETE("/api/conversations/:id/device", h.ClearConversationDevice)
	do := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/conversations/c1/device", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	// Bound but not yet answered: the folder can still be dropped.
	bind()
	if w := do(http.MethodDelete, ""); w.Code != http.StatusNoContent {
		t.Fatalf("unbind before reply: %d %s", w.Code, w.Body)
	}

	bind()
	if err := s.SaveMessage(ctx, store.Message{ID: "m1", ConversationID: "c1", Role: "assistant", Content: "done"}); err != nil {
		t.Fatal(err)
	}
	if w := do(http.MethodDelete, ""); w.Code != http.StatusConflict {
		t.Fatalf("unbind after reply: %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, `{"device_id":"d1","work_dir":"/other"}`); w.Code != http.StatusConflict {
		t.Fatalf("rebind after reply: %d %s", w.Code, w.Body)
	}
	if b, err := s.GetConversationDevice(ctx, "c1"); err != nil || b.WorkDir != "/w" {
		t.Fatalf("binding changed: %+v %v", b, err)
	}
}
