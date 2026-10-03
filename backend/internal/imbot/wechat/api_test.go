package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestSendTextBuildsBotMessage(t *testing.T) {
	var got SendMessageRequest
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+epSendMessage {
			t.Errorf("path = %q, want %q", r.URL.Path, "/"+epSendMessage)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	})

	clientID, err := client.SendText(context.Background(), "hello", SendTextOptions{
		To:           "u1@im.wechat",
		ContextToken: "ctx-abc",
		RunID:        "run-1",
	})
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if clientID == "" {
		t.Error("SendText returned an empty client id")
	}

	msg := got.Msg
	if msg == nil {
		t.Fatal("request carried no msg")
	}
	if msg.ToUserID != "u1@im.wechat" {
		t.Errorf("to_user_id = %q", msg.ToUserID)
	}
	if msg.ContextToken != "ctx-abc" {
		t.Errorf("context_token = %q, want %q", msg.ContextToken, "ctx-abc")
	}
	if msg.RunID != "run-1" {
		t.Errorf("run_id = %q, want %q", msg.RunID, "run-1")
	}
	if msg.MessageType != MessageTypeBot {
		t.Errorf("message_type = %d, want %d", msg.MessageType, MessageTypeBot)
	}
	// The protocol has no edit-in-place, so every outbound send is final.
	if msg.MessageState != StateFinish {
		t.Errorf("message_state = %d, want %d", msg.MessageState, StateFinish)
	}
	if msg.ClientID != clientID {
		t.Errorf("client_id = %q, want the returned id %q", msg.ClientID, clientID)
	}
	if len(msg.ItemList) != 1 || msg.ItemList[0].Type != ItemTypeText {
		t.Fatalf("item_list = %+v, want one text item", msg.ItemList)
	}
	if msg.ItemList[0].TextItem.Text != "hello" {
		t.Errorf("text = %q, want %q", msg.ItemList[0].TextItem.Text, "hello")
	}
}

// Sending without a context token is accepted by the gateway but never
// delivered, so it has to fail loudly on our side instead.
func TestSendTextRequiresContextToken(t *testing.T) {
	called := false
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"ret":0}`))
	})

	_, err := client.SendText(context.Background(), "hello", SendTextOptions{To: "u1@im.wechat"})
	if !errors.Is(err, ErrMissingContextToken) {
		t.Fatalf("err = %v, want ErrMissingContextToken", err)
	}
	if called {
		t.Error("request was sent despite the missing context token")
	}
}

func TestSendTextSurfacesGatewayRejection(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":10001,"errmsg":"blocked"}`))
	})

	_, err := client.SendText(context.Background(), "hi", SendTextOptions{To: "u1@im.wechat", ContextToken: "c"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiErr.Ret != 10001 || apiErr.Message != "blocked" {
		t.Errorf("APIError = %+v, want ret=10001 errmsg=blocked", apiErr)
	}
}

func TestTypingTicketsFetchesOnceThenReuses(t *testing.T) {
	configCalls, typingCalls := 0, 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + epGetConfig:
			configCalls++
			_, _ = w.Write([]byte(`{"ret":0,"typing_ticket":"tkt"}`))
		case "/" + epSendTyping:
			typingCalls++
			var req SendTypingRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode: %v", err)
			}
			if req.TypingTicket != "tkt" {
				t.Errorf("typing_ticket = %q, want %q", req.TypingTicket, "tkt")
			}
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	tickets := NewTypingTickets(client)
	for range 3 {
		if err := tickets.Set(context.Background(), "u1@im.wechat", "ctx", TypingActive); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if configCalls != 1 {
		t.Errorf("getConfig called %d times, want 1 (ticket should be cached)", configCalls)
	}
	if typingCalls != 3 {
		t.Errorf("sendTyping called %d times, want 3", typingCalls)
	}
}

// An expired ticket must be dropped, otherwise the indicator stays broken for
// the rest of the process lifetime.
func TestTypingTicketsRefetchAfterRejection(t *testing.T) {
	configCalls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + epGetConfig:
			configCalls++
			_, _ = w.Write([]byte(`{"ret":0,"typing_ticket":"tkt"}`))
		default:
			_, _ = w.Write([]byte(`{"ret":42,"errmsg":"stale ticket"}`))
		}
	})

	tickets := NewTypingTickets(client)
	if err := tickets.Set(context.Background(), "u1@im.wechat", "ctx", TypingActive); err == nil {
		t.Fatal("Set succeeded on a rejected ticket")
	}
	_ = tickets.Set(context.Background(), "u1@im.wechat", "ctx", TypingActive)
	if configCalls != 2 {
		t.Errorf("getConfig called %d times, want 2 (rejection should invalidate the cache)", configCalls)
	}
}

func TestMessagePlainText(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
		want string
	}{
		{"nil", nil, ""},
		{
			"text items joined",
			&Message{ItemList: []MessageItem{
				{Type: ItemTypeText, TextItem: &TextItem{Text: "one"}},
				{Type: ItemTypeText, TextItem: &TextItem{Text: "two"}},
			}},
			"one\ntwo",
		},
		{
			"voice contributes its transcription",
			&Message{ItemList: []MessageItem{
				{Type: ItemTypeVoice, VoiceItem: &VoiceItem{Text: "转写内容"}},
			}},
			"转写内容",
		},
		{
			"media-only message has no text",
			&Message{ItemList: []MessageItem{{Type: ItemTypeImage, ImageItem: &ImageItem{}}}},
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.msg.PlainText(); got != tc.want {
				t.Errorf("PlainText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessagePeerIDPrefersGroup(t *testing.T) {
	dm := &Message{FromUserID: "u1@im.wechat"}
	if got := dm.PeerID(); got != "u1@im.wechat" {
		t.Errorf("DM PeerID() = %q", got)
	}
	group := &Message{FromUserID: "u1@im.wechat", GroupID: "g1"}
	if got := group.PeerID(); got != "g1" {
		t.Errorf("group PeerID() = %q, want the group id", got)
	}
}
