package handler

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestInputWithSendAtSchedulesInsteadOfQueueing(t *testing.T) {
	ms := storetest.New()
	_ = ms.CreateUser(context.Background(), store.User{ID: "u1"})
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1", Provider: "claude"}}
	h := newTestTerminalHandler(&testRunner{}, ms)
	state := &connState{conversationID: "c1", userID: "u1", subscriptionID: "sub-1"}
	var got []serverMessage
	write := func(m serverMessage) { got = append(got, m) }

	sendAt := time.Now().Add(time.Hour)
	h.handleInputMsg(context.Background(), clientMessage{Type: "input", Content: "later", SendAt: &sendAt, Steer: true},
		state, write, func(string, serverMessage) {})
	if len(got) != 1 || got[0].Type != "scheduled_ack" || len(got[0].ScheduledPrompts) != 1 {
		t.Fatalf("frames = %+v, want one scheduled_ack", got)
	}
	id := got[0].ScheduledPrompts[0].ID
	if msgs := ms.Messages["c1"]; len(msgs) != 0 {
		t.Fatalf("a scheduled send was queued right away: %+v", msgs)
	}

	// A tab joining later is told what is waiting.
	got = nil
	h.offerScheduledPrompts(context.Background(), state, write)
	if len(got) != 1 || got[0].Type != "scheduled_prompts" || got[0].SubscriptionID != "sub-1" ||
		len(got[0].ScheduledPrompts) != 1 || got[0].ScheduledPrompts[0].Content != "later" {
		t.Fatalf("init offer = %+v", got)
	}

	got = nil
	h.handleCancelScheduledMsg(context.Background(), clientMessage{Type: "cancel_scheduled", MessageID: id}, state, write)
	if len(got) != 1 || got[0].Type != "scheduled_prompt_cancelled" || len(got[0].ScheduledPrompts) != 1 ||
		got[0].ScheduledPrompts[0].Content != "later" {
		t.Fatalf("cancel frames = %+v", got)
	}

	// Nothing left: the second cancel recalls nothing and init offers nothing.
	got = nil
	h.handleCancelScheduledMsg(context.Background(), clientMessage{Type: "cancel_scheduled", MessageID: id}, state, write)
	h.offerScheduledPrompts(context.Background(), state, write)
	if len(got) != 1 || len(got[0].ScheduledPrompts) != 0 {
		t.Fatalf("after cancel = %+v", got)
	}
}

func TestInputWithPastSendAtIsRejected(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "u1"}}
	h := newTestTerminalHandler(&testRunner{}, ms)
	var got []serverMessage
	sendAt := time.Now().Add(-time.Minute)
	h.handleInputMsg(context.Background(), clientMessage{Type: "input", Content: "x", SendAt: &sendAt},
		&connState{conversationID: "c1"}, func(m serverMessage) { got = append(got, m) }, func(string, serverMessage) {})
	if len(got) != 1 || got[0].Type != "error" || got[0].Message != "invalid scheduled time" {
		t.Fatalf("frames = %+v", got)
	}
	if len(ms.ScheduledPrompts) != 0 || len(ms.Messages["c1"]) != 0 {
		t.Fatal("a rejected schedule left a row behind")
	}
}
