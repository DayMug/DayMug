package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

const testNonce = "nonce-0123456789abcdef0123456789abcdef"

func deviceFixture(t *testing.T) (*storetest.Fake, *DeviceHub) {
	t.Helper()
	s := storetest.New()
	ctx := context.Background()
	for _, u := range []store.User{
		{ID: "h1", Name: "Alice", Username: "alice"},
		{ID: "a1", Name: "Agent", OwnerID: "h1"},
		{ID: "h2", Name: "Bob", Username: "bob"},
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	if err := s.CreateConversation(ctx, "c1", "", "a1", "", "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	return s, NewDeviceHub(s)
}

// fakeClient answers every call with reply(method, params) on its own
// goroutine, the way the client's socket reader would.
type fakeClient struct {
	mu      sync.Mutex
	frames  []DeviceFrame
	session *DeviceSession
	reply   func(DeviceFrame) DeviceFrame
}

func (f *fakeClient) send(frame DeviceFrame) error {
	f.mu.Lock()
	f.frames = append(f.frames, frame)
	s := f.session
	f.mu.Unlock()
	if frame.Type == "call" && f.reply != nil {
		go s.Deliver(f.reply(frame))
	}
	return nil
}

func connect(h *DeviceHub, d store.Device, hello DeviceHello, reply func(DeviceFrame) DeviceFrame) *fakeClient {
	f := &fakeClient{reply: reply}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.session = h.Register(d, hello, f.send)
	return f
}

func TestDevicePairThenClaim(t *testing.T) {
	_, h := deviceFixture(t)
	ctx := context.Background()
	if _, _, ok, _ := h.Claim(ctx, testNonce); ok {
		t.Fatal("claim succeeded with nothing pending")
	}
	if err := h.BeginPair(ctx, "h1", "short", "laptop", "macos"); err == nil {
		t.Fatal("short nonce accepted")
	}
	if err := h.BeginPair(ctx, "h1", testNonce, "laptop", "macos"); err != nil {
		t.Fatalf("pair: %v", err)
	}
	d, token, ok, err := h.Claim(ctx, testNonce)
	if err != nil || !ok || d.UserID != "h1" || d.Name != "laptop" {
		t.Fatalf("claim = %+v %v %v", d, ok, err)
	}
	if got, err := h.Authenticate(ctx, token); err != nil || got.ID != d.ID {
		t.Fatalf("authenticate = %+v, %v", got, err)
	}
	if _, _, ok, _ := h.Claim(ctx, testNonce); ok {
		t.Fatal("a pairing was claimed twice")
	}
	if _, err := h.Authenticate(ctx, "dmd_wrong"); err == nil {
		t.Fatal("unknown token authenticated")
	}
}

func TestDevicePairPushesCredentialsToConnectedClient(t *testing.T) {
	s, h := deviceFixture(t)
	ctx := context.Background()
	d, _ := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", TokenHash: "x"})
	client := connect(h, d, DeviceHello{Nonce: testNonce, OS: "linux"}, nil)

	if _, _, ok := h.CurrentDevice("h1", testNonce); !ok {
		t.Fatal("current device not found for its owner")
	}
	if _, _, ok := h.CurrentDevice("h2", testNonce); ok {
		t.Fatal("current device reported to another user")
	}
	// Bob logs in inside the same client: the connected client is rebound.
	if err := h.BeginPair(ctx, "h2", testNonce, "", ""); err != nil {
		t.Fatalf("pair: %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.frames) != 1 || client.frames[0].Type != "rebind" || client.frames[0].Token == "" {
		t.Fatalf("frames = %+v", client.frames)
	}
	got, err := h.Authenticate(ctx, client.frames[0].Token)
	if err != nil || got.UserID != "h2" || got.OS != "linux" {
		t.Fatalf("rebound device = %+v, %v", got, err)
	}
}

func TestDeviceCallRoundTripAndCancel(t *testing.T) {
	s, h := deviceFixture(t)
	ctx := context.Background()
	d, _ := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", TokenHash: "x"})
	connect(h, d, DeviceHello{}, func(f DeviceFrame) DeviceFrame {
		if f.Method == "fail" {
			return DeviceFrame{Type: "result", ID: f.ID, Error: "boom"}
		}
		return DeviceFrame{Type: "result", ID: f.ID, Result: f.Params}
	})
	raw, err := h.Call(ctx, "d1", "echo", map[string]string{"a": "b"})
	if err != nil || string(raw) != `{"a":"b"}` {
		t.Fatalf("echo = %s, %v", raw, err)
	}
	if _, err := h.Call(ctx, "d1", "fail", nil); err == nil || err.Error() != "boom" {
		t.Fatalf("fail err = %v", err)
	}

	// A call whose caller gives up sends a cancel to the client.
	silent := connect(h, d, DeviceHello{}, nil)
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := h.Call(cctx, "d1", "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow err = %v", err)
	}
	silent.mu.Lock()
	defer silent.mu.Unlock()
	if n := len(silent.frames); n != 2 || silent.frames[1].Type != "cancel" || silent.frames[1].ID != silent.frames[0].ID {
		t.Fatalf("frames = %+v", silent.frames)
	}
}

func TestDeviceCloseFailsPendingCalls(t *testing.T) {
	s, h := deviceFixture(t)
	ctx := context.Background()
	d, _ := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", TokenHash: "x"})
	client := connect(h, d, DeviceHello{}, nil)
	errCh := make(chan error, 1)
	go func() {
		_, err := h.Call(ctx, "d1", "slow", nil)
		errCh <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.mu.Lock()
		n := len(client.frames)
		client.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	client.session.Close()
	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("err = %v", err)
	}
	if h.Online("d1") {
		t.Fatal("closed session still online")
	}
}

func TestDeviceMCPToken(t *testing.T) {
	_, h := deviceFixture(t)
	tok := h.MCPToken("c1")
	if conv, ok := h.VerifyMCPToken(tok); !ok || conv != "c1" {
		t.Fatalf("verify = %q %v", conv, ok)
	}
	for _, bad := range []string{"", "c1", "c1.deadbeef", "c2" + tok[2:], NewDeviceHub(nil).MCPToken("c1")} {
		if _, ok := h.VerifyMCPToken(bad); ok {
			t.Errorf("token %q accepted", bad)
		}
	}
}

func TestApplyDeviceRoute(t *testing.T) {
	s, h := deviceFixture(t)
	ctx := context.Background()

	var plain agent.RunRequest
	if h.ApplyDeviceRoute(ctx, s, &plain, "c1", "http://127.0.0.1:8080") || plain.ToolRoute != nil || plain.SystemPrompt != "" {
		t.Fatalf("unbound conversation was rewired: %+v", plain)
	}

	// A device of another user is never reachable, even with a binding row.
	other, _ := s.CreateDevice(ctx, store.Device{ID: "d2", UserID: "h2", TokenHash: "y"})
	if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: other.ID, WorkDir: "/w"}); err != nil {
		t.Fatal(err)
	}
	if h.ApplyDeviceRoute(ctx, s, &plain, "c1", "http://127.0.0.1:8080") {
		t.Fatal("foreign device routed")
	}

	d, _ := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", OS: "macos", TokenHash: "x"})
	if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: d.ID, WorkDir: "/Users/alice/proj"}); err != nil {
		t.Fatal(err)
	}
	connect(h, d, DeviceHello{OS: "macos", Hostname: "mbp", Tools: []DeviceTool{{Name: "Bash"}, {Name: "Read"}, {Name: "Custom"}}}, nil)
	var opts agent.RunRequest
	if !h.ApplyDeviceRoute(ctx, s, &opts, "c1", "http://127.0.0.1:8080/") {
		t.Fatal("bound conversation not routed")
	}
	route := opts.ToolRoute
	server, _ := route.MCPServers[DeviceMCPServerName].(map[string]any)
	headers, _ := server["headers"].(map[string]string)
	if server["url"] != "http://127.0.0.1:8080/api/devices/mcp" || headers["Authorization"] != "Bearer "+h.MCPToken("c1") {
		t.Fatalf("mcp server = %+v", server)
	}
	if want := map[string]string{"Bash": "mcp__daymug_local__Bash", "Read": "mcp__daymug_local__Read"}; len(route.Aliases) != 2 || route.Aliases["Bash"] != want["Bash"] || route.Aliases["Read"] != want["Read"] {
		t.Fatalf("aliases = %v", route.Aliases)
	}
	for _, tool := range []string{"Bash", "Read", "Write", "Edit", "Glob", "Grep", "NotebookEdit"} {
		found := false
		for _, d := range route.Disallowed {
			found = found || d == tool
		}
		if !found {
			t.Errorf("%s not disallowed", tool)
		}
	}
	if !strings.Contains(opts.SystemPrompt, "/Users/alice/proj") || !strings.Contains(opts.SystemPrompt, "mbp") {
		t.Fatalf("system prompt = %q", opts.SystemPrompt)
	}
	payload, _ := json.Marshal(route.MCPServers)
	if !strings.Contains(string(payload), `"type":"http"`) {
		t.Fatalf("mcp payload = %s", payload)
	}
}

func TestDeviceMCPBaseURL(t *testing.T) {
	if DeviceMCPBaseURL(nil) != "" {
		t.Fatal("nil config produced a URL")
	}
}
