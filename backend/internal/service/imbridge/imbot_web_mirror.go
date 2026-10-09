package imbridge

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// IMResponderResolver exposes only the healthy-connector operation the bridge
// needs. imbot.Manager implements it without exposing credentials.
type IMResponderResolver interface {
	ResponderFor(botID string, msg imbot.Message) (imbot.Responder, bool)
}

type webIMObservation struct {
	bridge    *IMBridge
	msg       imbot.Message
	responder imbot.Responder
	prompt    string
	// promptPrefix labels the mirrored prompt, and is what tells the thread
	// whether a human typed this on the web or the scheduler fired it.
	promptPrefix string
	// ack posts the "received, working on it" placeholder before the run. Off
	// for scheduled tasks: Complete creates its own message when none exists,
	// so skipping the ack costs nothing and keeps an unattended job from
	// putting two messages in someone's thread per fire.
	ack       bool
	started   bool
	closeOnce sync.Once
	unlock    func()
}

// ObservePrompt implements service.PromptObserver. It is the web path's entry
// into the IM thread lock, and the single lock order both surfaces obey is
// thread lock → Broadcaster job → account-pool slot. service.PromptRunner
// therefore calls ObservePrompt before Broadcaster.StartJob; taking those two
// in the opposite order is what used to make a web prompt and an IM turn fight
// over the same conversation.
func (b *IMBridge) ObservePrompt(ctx context.Context, prompt store.Message) service.PromptObservation {
	if b.Store == nil || b.Responders == nil || prompt.ConversationID == "" {
		return nil
	}
	scheduled := false
	chosenBotID := ""
	thread, err := b.Store.GetBotThreadByConversation(ctx, prompt.ConversationID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("imbot: lookup web conversation %s: %v", prompt.ConversationID, err)
			return nil
		}
		// No binding of its own is the normal shape for a scheduled run: the
		// scheduler mints a plain conversation precisely so the run stays a
		// readable web conversation. Ask whether that conversation belongs to a
		// job that opted into relaying its answer.
		var delivery store.CronDelivery
		delivery, scheduled = b.cronDeliveryThread(ctx, prompt.ConversationID, prompt.ID)
		if !scheduled {
			return nil
		}
		thread, chosenBotID = delivery.Thread, delivery.BotID
	}
	msg, ok := b.webMessageForThread(thread, chosenBotID)
	if !ok {
		return nil
	}
	responder, ok := b.Responders.ResponderFor(msg.BotID, msg)
	if !ok || responder == nil {
		return nil
	}
	observation := &webIMObservation{
		bridge: b, msg: msg, responder: responder, prompt: prompt.Content,
		promptPrefix: webMirrorPromptPrefix, ack: true, unlock: func() {},
	}
	if scheduled {
		// Deliberately unlocked. The thread mutex serialises turns that share a
		// conversation and session; a scheduled run has its own conversation and
		// touches no bot_threads row, so holding it would only let an hour-long
		// job freeze the human's chat in that thread. What the relay writes are
		// fresh messages, which need no ordering against an in-flight turn.
		observation.promptPrefix = cronMirrorPromptPrefix
		observation.ack = false
		return observation
	}
	// Key off the resolved message through threadKey, the same function the
	// inbound path uses, so both surfaces take the one mutex for the thread.
	lock := b.threadLock(threadKey(msg))
	lock.Lock()
	observation.unlock = lock.Unlock
	return observation
}

// cronDeliveryThread resolves the IM thread a scheduled run should relay into.
func (b *IMBridge) cronDeliveryThread(ctx context.Context, conversationID, messageID string) (store.CronDelivery, bool) {
	delivery, err := b.Store.CronBotDeliveryTarget(ctx, conversationID, messageID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("imbot: lookup scheduled-task delivery target for conversation %s: %v",
				conversationID, err)
		}
		return store.CronDelivery{}, false
	}
	return delivery, true
}

// webMessageForThread builds the outbound message for a thread from its
// "<platform>@<agentID>@<botID>" key. preferredBotID is the bot a scheduled job
// named; the delivery lookup only returns that bot's threads, so it agrees with
// the key and merely makes the choice explicit.
func (b *IMBridge) webMessageForThread(thread store.BotThread, preferredBotID string) (imbot.Message, bool) {
	platform, agentID, botID := parseThreadPlatform(thread.Platform)
	if preferredBotID != "" {
		botID = preferredBotID
	}
	if platform == "" || agentID == "" || botID == "" || thread.ChannelID == "" || thread.ThreadID == "" {
		return imbot.Message{}, false
	}
	return imbot.Message{
		Platform: platform, AgentID: agentID, BotID: botID,
		ChannelID: thread.ChannelID, ThreadID: thread.ThreadID,
		MessageID: firstNonEmptyString(thread.LastMessageID, thread.ThreadID),
	}, true
}

func parseThreadPlatform(value string) (platform, agentID, botID string) {
	parts := strings.SplitN(value, "@", 3)
	platform = parts[0]
	if len(parts) == 3 {
		agentID, botID = parts[1], parts[2]
	}
	return platform, agentID, botID
}

func (o *webIMObservation) Started(context.Context) {
	o.started = true
	o.bridge.respondBestEffort(o.msg, func(ctx context.Context) error {
		return o.responder.Post(ctx, o.promptPrefix+o.prompt)
	})
	if !o.ack {
		return
	}
	o.bridge.respondBestEffort(o.msg, func(ctx context.Context) error {
		return o.responder.Start(ctx, AckText)
	})
}

func (o *webIMObservation) Finish(_ context.Context, outcome service.PromptOutcome) {
	if !o.started {
		return
	}
	text := outcome.Content
	switch {
	case outcome.Cancelled:
		text = webMirrorCancelledText
	case outcome.Err != nil:
		text = errorNoticePrefix + outcome.Err.Error()
	case text == "":
		text = imbot.EmptyReplyText
	}
	o.bridge.respondBestEffort(o.msg, func(ctx context.Context) error {
		return o.responder.Complete(ctx, text)
	})
	if outcome.Err == nil && !outcome.Cancelled {
		o.bridge.publishArtifacts(o.msg, o.responder, service.ArtifactOutcome{
			Published: outcome.Artifacts,
			Rejected:  outcome.RejectedArtifacts,
		})
	}
}

func (o *webIMObservation) Guardrail(_ context.Context, snapshot service.GuardrailSnapshot) {
	text := guardrailPromptText(snapshot)
	o.bridge.respondBestEffort(o.msg, func(ctx context.Context) error {
		return imbot.Notify(ctx, o.responder, text)
	})
}

func (o *webIMObservation) Close() {
	o.closeOnce.Do(o.unlock)
}
