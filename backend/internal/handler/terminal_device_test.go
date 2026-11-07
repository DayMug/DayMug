package handler

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

const terminalDeviceNonce = "nonce-0123456789abcdef0123456789abcdef"

// bindTerminalTestDevice binds convID to a device whose client hosts the page
// carrying terminalDeviceNonce.
func bindTerminalTestDevice(t *testing.T, h *TerminalHandler, s store.Store, convID string) {
	t.Helper()
	ctx := context.Background()
	d, err := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "u1", TokenHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: convID, DeviceID: d.ID, WorkDir: "/w"}); err != nil {
		t.Fatal(err)
	}
	h.Devices.Register(d, service.DeviceHello{Nonce: terminalDeviceNonce}, func(service.DeviceFrame) error { return nil })
}

// A browser tab (no client nonce) watching a computer-bound conversation can
// read it but its prompts are refused before anything is queued.
func TestTerminalHandler_RefusesInputForDeviceConversationFromBrowser(t *testing.T) {
	h, ms := terminalTestHandler(&testRunner{}, "conv-dev")
	bindTerminalTestDevice(t, h, ms, "conv-dev")
	srv := terminalTestServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // status:ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-dev", UserID: "u1", SubscriptionID: "sub"})
	readMsg(t, ctx, conn) // init_ok
	readMsg(t, ctx, conn) // init_status

	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "from the browser", ConversationID: "conv-dev", SubscriptionID: "sub"})
	got := readMsg(t, ctx, conn)
	if got.Type != "error" || got.Message != service.ErrConversationOnDevice.Message() || got.ConversationID != "conv-dev" {
		t.Fatalf("response = %+v, want the device-conversation refusal", got)
	}
	if msgs := ms.Messages["conv-dev"]; len(msgs) != 0 {
		t.Fatalf("browser input was queued: %+v", msgs)
	}
}

func TestTerminalHandler_SenderMayDriveDeviceConversation(t *testing.T) {
	h, ms := terminalTestHandler(&testRunner{}, "conv-dev", "conv-server")
	bindTerminalTestDevice(t, h, ms, "conv-dev")
	ignore := func(serverMessage) {}
	for _, tc := range []struct {
		conv, nonce string
		want        bool
	}{
		{"conv-dev", terminalDeviceNonce, true},
		{"conv-dev", "", false},
		{"conv-server", "", true},
	} {
		state := connState{conversationID: tc.conv, deviceNonce: tc.nonce}
		if got := h.senderMayDrive(context.Background(), &state, ignore); got != tc.want {
			t.Errorf("senderMayDrive(%s, nonce=%q) = %v, want %v", tc.conv, tc.nonce, got, tc.want)
		}
	}
}
