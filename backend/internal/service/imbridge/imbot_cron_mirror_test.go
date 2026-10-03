package imbridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

// cronDeliveryFake adds the optional cronBotDeliveryLookup surface on top of
// the shared fake. Kept local rather than pushed into storetest.Fake so the
// handler doubles that embed the fake keep satisfying `cronBotDeliveryLookup`
// only by explicit opt-in — the optionality is the point of the interface.
type cronDeliveryFake struct {
	*storetest.Fake
	targets map[string]store.BotThread
	// followUps stands in for the real query's "this is not the scheduled turn"
	// arm: message ids listed here resolve to nothing, the way a human's later
	// prompt in the same conversation does.
	followUps map[string]bool
	// pinnedBots mirrors cron_jobs.bot_id: the bot the job named, which the real
	// query resolves alongside the thread. Empty for a job that named none.
	pinnedBots map[string]string
}

func (f *cronDeliveryFake) CronBotDeliveryTarget(_ context.Context, conversationID, messageID string) (store.CronDelivery, error) {
	thread, ok := f.targets[conversationID]
	if ok && f.followUps[messageID] {
		return store.CronDelivery{}, store.ErrNotFound
	}
	if !ok {
		// What the real query returns for both "job did not opt in" and
		// "Agent has never talked to a bot": one join, one empty result.
		return store.CronDelivery{}, store.ErrNotFound
	}
	return store.CronDelivery{Thread: thread, BotID: f.pinnedBots[conversationID]}, nil
}

func newCronMirrorBridge(t *testing.T) (*IMBridge, *storetest.Fake, *cronDeliveryFake, *imbottest.ReplyRecorder) {
	t.Helper()
	bridge, ms := newInterruptTestBridge(t, 1, nil, nil)
	delivery := &cronDeliveryFake{
		Fake:       ms,
		targets:    map[string]store.BotThread{},
		followUps:  map[string]bool{},
		pinnedBots: map[string]string{},
	}
	bridge.Store = delivery
	replies := &imbottest.ReplyRecorder{}
	bridge.Responders = &imbottest.RecordingResponderResolver{Responder: replies}
	return bridge, ms, delivery, replies
}

func cronMirrorPrompt(conversationID, content string) store.Message {
	return store.Message{ConversationID: conversationID, Content: content}
}

// TestObservePromptMirrorsScheduledRunWithoutAck covers the fallback branch: a
// cron run deliberately owns no bot_threads row of its own, so the mirror has
// to reach the thread through the job's Agent instead. Two things are asserted
// together because each protects a separate mistake — the ⏰ prefix (without it
// an unattended job reads in the thread as a human question) and the absence of
// the ack placeholder (Complete creates its own message, so acking would put
// two messages per fire into someone's chat forever).
func TestObservePromptMirrorsScheduledRunWithoutAck(t *testing.T) {
	bridge, ms, delivery, replies := newCronMirrorBridge(t)
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())
	delivery.targets["conv-cron"] = store.BotThread{
		Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "thread1",
		AgentID: "agent1", ConversationID: "conv-im",
	}

	observation := bridge.ObservePrompt(context.Background(), cronMirrorPrompt("conv-cron", "生成日报"))
	if observation == nil {
		t.Fatal("scheduled run with deliver_to_bot produced no observation: its answer would never reach the thread")
	}
	defer observation.Close()
	observation.Started(context.Background())

	posted := replies.All()
	if len(posted) != 1 {
		t.Fatalf("thread received %d messages, want exactly the prompt mirror: %v", len(posted), posted)
	}
	if want := cronMirrorPromptPrefix + "生成日报"; posted[0] != want {
		t.Fatalf("mirrored prompt = %q, want %q", posted[0], want)
	}
	if strings.Contains(posted[0], AckText) {
		t.Fatalf("scheduled run posted the ack placeholder: %q", posted[0])
	}
}

// TestObservePromptSkipsScheduledRunWithoutDeliveryTarget keeps the fallback
// from becoming a catch-all. Every web prompt whose conversation has no thread
// binding reaches this branch, so anything short of a resolved, reachable
// thread must stay silent — otherwise ordinary cron runs (and plain web chats)
// start spraying into whatever thread the Agent last touched.
func TestObservePromptSkipsScheduledRunWithoutDeliveryTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *store.BotThread
	}{
		{name: "job did not opt in or Agent has no thread", target: nil},
		{name: "thread key names no bot", target: &store.BotThread{
			Platform: imbot.PlatformSlack, ChannelID: "C9", ThreadID: "thread9",
			AgentID: "agent1", ConversationID: "conv-im",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge, ms, delivery, replies := newCronMirrorBridge(t)
			addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())
			if tc.target != nil {
				delivery.targets["conv-cron"] = *tc.target
			}

			if observation := bridge.ObservePrompt(
				context.Background(), cronMirrorPrompt("conv-cron", "生成日报"),
			); observation != nil {
				observation.Close()
				t.Fatal("observation returned for a run with no reachable delivery thread")
			}
			if posted := replies.All(); len(posted) != 0 {
				t.Fatalf("thread received %v, want nothing", posted)
			}
		})
	}
}

// TestObservePromptSkipsHumanFollowUpInScheduledConversation covers the window
// after a run finishes: the job still points at that conversation, so opening it
// in the SPA and typing would relay a message nobody sent to the thread, labelled
// as a scheduled task that did not produce it.
func TestObservePromptSkipsHumanFollowUpInScheduledConversation(t *testing.T) {
	bridge, ms, delivery, replies := newCronMirrorBridge(t)
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())
	delivery.targets["conv-cron"] = store.BotThread{
		Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "thread1",
		AgentID: "agent1", ConversationID: "conv-im",
	}
	delivery.followUps["msg-followup"] = true

	observation := bridge.ObservePrompt(context.Background(), store.Message{
		ID: "msg-followup", ConversationID: "conv-cron", Content: "那下半年呢？",
	})
	if observation != nil {
		observation.Close()
		t.Fatal("human follow-up produced an observation: it would be relayed as a scheduled run")
	}
	if posted := replies.All(); len(posted) != 0 {
		t.Fatalf("thread received %v, want nothing", posted)
	}
}

// TestObservePromptForScheduledRunSkipsThreadLock guards the reason the
// scheduled branch returns before threadLock: the mutex serialises turns that
// share a conversation and session, and a cron run shares neither. Taking it
// would let an hour-long unattended job block every human message in that
// thread for the duration — a hang with no error and no log line.
func TestObservePromptForScheduledRunSkipsThreadLock(t *testing.T) {
	bridge, ms, delivery, _ := newCronMirrorBridge(t)
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())
	thread := store.BotThread{
		Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "thread1",
		AgentID: "agent1", ConversationID: "conv-im",
	}
	delivery.targets["conv-cron-a"] = thread
	delivery.targets["conv-cron-b"] = thread

	first := bridge.ObservePrompt(context.Background(), cronMirrorPrompt("conv-cron-a", "长任务"))
	if first == nil {
		t.Fatal("first scheduled run produced no observation")
	}
	t.Cleanup(first.Close)

	second := make(chan service.PromptObservation, 1)
	go func() {
		second <- bridge.ObservePrompt(context.Background(), cronMirrorPrompt("conv-cron-b", "另一个任务"))
	}()
	select {
	case observation := <-second:
		if observation == nil {
			t.Fatal("second scheduled run produced no observation")
		}
		observation.Close()
	case <-time.After(time.Second):
		t.Fatal("second run blocked on the first: the scheduled branch is holding the thread lock")
	}
}
