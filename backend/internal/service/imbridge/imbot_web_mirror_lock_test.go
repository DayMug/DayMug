package imbridge

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

// TestWebMirrorSharesThreadLockWithIMTurn pins the two-primitive split: the
// inbound IM path keys its lock off the live message and the web mirror off the
// stored row, so unless both go through threadKey the two surfaces take
// different mutexes and drive the same IM thread at the same time.
func TestWebMirrorSharesThreadLockWithIMTurn(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	bridge, ms := newInterruptTestBridge(t, 1, started, release)
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())
	bridge.Responders = &imbottest.RecordingResponderResolver{Responder: &imbottest.ReplyRecorder{}}

	ms.Conversations = append(ms.Conversations, store.Conversation{ID: "conv-im", UserID: "agent1", Provider: "claude"})
	ms.BotThreads = map[string]store.BotThread{
		"slack@agent1@bot1|C1|thread1": {
			Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "thread1",
			AgentID: "agent1", ConversationID: "conv-im",
		},
	}

	imDone := make(chan struct{})
	go func() {
		defer close(imDone)
		bridge.HandleMessage(
			context.Background(),
			interruptTestMessage(imbot.PlatformSlack, "agent1", "bot1", "im-first"),
			&imbottest.ReplyRecorder{},
		)
	}()
	waitForIMRunStart(t, started)

	observed := make(chan service.PromptObservation, 1)
	go func() {
		observed <- bridge.ObservePrompt(
			context.Background(),
			store.Message{ConversationID: "conv-im", Content: "from web"},
		)
	}()

	select {
	case obs := <-observed:
		if obs != nil {
			obs.Close()
		}
		close(release)
		<-imDone
		t.Fatal("web mirror took a different lock than the running IM turn: the two surfaces would run the same thread concurrently")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	waitForIMHandle(t, imDone)

	select {
	case obs := <-observed:
		if obs == nil {
			t.Fatal("web mirror produced no observation for the IM-bound conversation")
		}
		obs.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("web mirror never acquired the thread lock after the IM turn finished")
	}
}
