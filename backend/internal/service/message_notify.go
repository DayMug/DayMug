package service

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// MaybeNotify dispatches the final assistant result to the push channel chosen
// by the conversation's human owner.
func (p *MessagePersister) MaybeNotify(convID, resultContent string) {
	p.maybeNotify(convID, resultContent, "Task completed.")
}

// MaybeNotifyUserQuestion alerts the owner as soon as an agent pauses for
// input. The provider payload is normalized before it reaches this layer, so
// every backend produces the same concise notification body.
func (p *MessagePersister) MaybeNotifyUserQuestion(convID, payload string) {
	p.maybeNotify(convID, userQuestionNotificationBody(payload), "Your reply is needed.")
}

// maybeNotify contains the shared Bark/PushDeer routing for completion and
// user-question notifications. Notification config lives on the human owner —
// the conversation's user can be an agent, so we resolve via Store.GetOwner
// before reading channel config. It uses context.Background() so a client
// disconnect doesn't drop a notification mid-flight. No-op when:
//   - no senders are wired (tests),
//   - the store is missing or the conversation can't be looked up,
//   - the conversation mirrors a bot/IM thread (the IM reply is its delivery),
//   - the conversation has notifications_enabled = false,
//   - the owner has no channels configured at all,
//   - the explicitly-chosen channel is configured but the matching sender
//     isn't wired (preserves the "user's explicit choice wins" invariant
//     without silently falling back to the other channel).
func (p *MessagePersister) maybeNotify(convID, content, fallbackBody string) {
	if p.Store == nil || convID == "" {
		return
	}
	if p.BarkSender == nil && p.PushDeerSender == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conv, err := p.Store.GetConversation(ctx, convID)
	if err != nil || !conv.NotificationsEnabled {
		return
	}
	// Keep bot-originated conversations out of Bark/PushDeer. Their final
	// answer is already delivered to Slack/Feishu, so an additional completion
	// push is duplicate noise.
	isBotConversation, err := p.Store.IsBotConversation(ctx, convID)
	if err != nil {
		log.Printf("check bot conversation %s: %v", convID, err)
		return
	}
	if isBotConversation {
		return
	}

	user, err := p.Store.GetUser(ctx, conv.UserID)
	if err != nil {
		return
	}
	owner, err := p.Store.GetOwner(ctx, user)
	if err != nil {
		return
	}

	channel, ok := resolveNotificationChannel(owner.BarkURL, owner.PushDeerKey, owner.NotificationChannel)
	if !ok {
		return
	}

	title := conv.Title
	if title == "" {
		title = "DayMug"
	}
	// Notification content may contain markdown; send only the readable text so
	// bodies don't show raw "**", "#", "`" markup.
	body := stripMarkdown(content)
	const maxBody = 400
	if len([]rune(body)) > maxBody {
		runes := []rune(body)
		body = string(runes[:maxBody]) + "…"
	}
	if body == "" {
		body = fallbackBody
	}

	// Deep link back into this conversation so tapping the push opens it in the
	// browser. Empty when no public URL is configured — senders treat that as
	// "no tap target".
	clickURL := conversationURL(p.Cfg, conv.UserID, convID)

	switch channel {
	case "bark":
		if p.BarkSender == nil {
			return
		}
		if err := p.BarkSender.Send(ctx, owner.BarkURL, title, body, clickURL); err != nil {
			log.Printf("bark notify %s: %v", convID, err)
		}
	case "pushdeer":
		if p.PushDeerSender == nil {
			return
		}
		if err := p.PushDeerSender.Send(ctx, owner.PushDeerKey, title, body); err != nil {
			log.Printf("pushdeer notify %s: %v", convID, err)
		}
	}
}

func userQuestionNotificationBody(payload string) string {
	var request agent.UserQuestionRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return ""
	}

	questions := make([]string, 0, len(request.Questions))
	for _, question := range request.Questions {
		if text := strings.TrimSpace(question.Question); text != "" {
			questions = append(questions, text)
		}
	}
	if len(questions) == 0 {
		return ""
	}
	return "Your reply is needed:\n" + strings.Join(questions, "\n")
}

// conversationURL builds the absolute deep link that opens a conversation in
// the browser, matching the SPA route `/chat/:userId/:conversationId`. Returns
// "" when no public base URL is configured (or required pieces are missing), in
// which case notifications are sent without a tap target. userID is the
// conversation's owning user — the same value the frontend puts in the route.
func conversationURL(cfg *config.Config, userID, convID string) string {
	if cfg == nil || userID == "" || convID == "" {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Server.PublicURL), "/")
	if base == "" {
		return ""
	}
	return base + "/chat/" + url.PathEscape(userID) + "/" + url.PathEscape(convID)
}

// resolveNotificationChannel picks the channel that should deliver a push
// based on the user's configured credentials and explicit preference. Rules:
//   - both bark + pushdeer configured: honor the user's explicit choice;
//     an empty/unknown preference defaults to "bark".
//   - exactly one channel configured: use it regardless of preference, so
//     toggling the preference while only one channel is set doesn't silently
//     disable notifications.
//   - neither configured: returns ok=false so the caller skips notifying.
func resolveNotificationChannel(barkURL, pushDeerKey, preference string) (string, bool) {
	hasBark := strings.TrimSpace(barkURL) != ""
	hasPushDeer := strings.TrimSpace(pushDeerKey) != ""
	switch {
	case hasBark && hasPushDeer:
		if preference == "pushdeer" {
			return "pushdeer", true
		}
		return "bark", true
	case hasBark:
		return "bark", true
	case hasPushDeer:
		return "pushdeer", true
	default:
		return "", false
	}
}
