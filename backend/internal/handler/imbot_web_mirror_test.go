package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"

	"github.com/DayMug/DayMug/backend/internal/service/imbridge"
)

func TestWebPromptMirrorsToOriginatingIMThread(t *testing.T) {
	runner := &testRunner{events: []agent.StreamEvent{
		{Kind: agent.KindDelta, Content: "synced"},
		{Kind: agent.KindResult, Content: "synced answer"},
	}}
	h, ms := terminalTestHandler(runner, "conv-im-web")
	ms.BotThreads = map[string]store.BotThread{
		"slack@agent1@bot1|C1|171.1": {
			Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "171.1",
			AgentID: "agent1", ConversationID: "conv-im-web", LastMessageID: "C1|171.9",
		},
	}
	replies := &imbottest.ReplyRecorder{}
	resolver := &imbottest.RecordingResponderResolver{Responder: replies}
	h.PromptObserver = &imbridge.IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms, Responders: resolver}

	srv := terminalTestServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	readMsg(t, ctx, conn) // ready
	sendMsg(t, ctx, conn, clientMessage{Type: "init", ConversationID: "conv-im-web"})
	readMsg(t, ctx, conn) // init_ok
	sendMsg(t, ctx, conn, clientMessage{Type: "input", Content: "run from web"})
	for {
		msg := readMsg(t, ctx, conn)
		if msg.Type == "status" && msg.Status == "ready" {
			break
		}
	}

	imbottest.WaitForReplyContaining(t, replies, "synced answer")
	got := replies.All()
	if len(got) != 3 {
		t.Fatalf("mirrored replies = %v, want web prompt + progress + answer", got)
	}
	if !strings.Contains(got[0], "DayMug Web") || !strings.Contains(got[0], "run from web") {
		t.Fatalf("web prompt mirror = %q", got[0])
	}
	if got[1] != imbridge.AckText || got[2] != "synced answer" {
		t.Fatalf("progress/final mirrors = %v", got[1:])
	}
	botID, outbound := resolver.Snapshot()
	if botID != "bot1" || outbound.Platform != imbot.PlatformSlack || outbound.ChannelID != "C1" || outbound.ThreadID != "171.1" || outbound.MessageID != "C1|171.9" {
		t.Fatalf("outbound route = bot %q, msg %+v", botID, outbound)
	}
}

func TestOrdinaryWebConversationDoesNotMirrorToIM(t *testing.T) {
	resolver := &imbottest.RecordingResponderResolver{Responder: &imbottest.ReplyRecorder{}}
	bridge := &imbridge.IMBridge{Runtime: &service.Runtime{Store: storetest.New()}, Responders: resolver}
	if observation := bridge.ObservePrompt(context.Background(), store.Message{ConversationID: "web-only", Content: "hello"}); observation != nil {
		t.Fatal("ordinary web conversation unexpectedly created an IM observation")
	}
	botID, _ := resolver.Snapshot()
	if botID != "" {
		t.Fatalf("resolver called for ordinary web conversation: %q", botID)
	}
}
