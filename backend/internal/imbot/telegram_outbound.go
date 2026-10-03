package imbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// telegramNotModified is the rejection Telegram returns when an edit would not
// change the message. The bridge throttles progress edits but does not compare
// bodies, so a stable banner (a long tool call, say) legitimately re-sends the
// same text — treating that as a failure would abort a healthy turn.
const telegramNotModified = "message is not modified"

// telegramTypingHeartbeat re-asserts the typing indicator while a turn runs.
// Telegram expires it after ~5 seconds, and the agent-driven refreshes cannot
// be relied on to beat that: progress edits are throttled to 3s and only happen
// when the stream produces something, so one long tool call — the very stretch
// where the reader most needs to know the bot is alive — goes silent and the
// indicator lapses. Under the platform's own expiry rather than equal to it, so
// a slow round-trip does not leave a gap.
//
// Slack deliberately has no counterpart: SetAssistantThreadsStatus is a
// persistent status with an explicit clear, so re-sending it would only burn
// rate limit. The heartbeat exists for indicators that expire on their own.
const telegramTypingHeartbeat = 4 * time.Second

// telegramResponder adds file upload and handoff posting to the shared
// progress-message skeleton.
type telegramResponder struct {
	*progressResponder
	connector *TelegramConnector
	msg       Message
}

func (r *telegramResponder) PostAttachments(ctx context.Context, attachments []OutboundAttachment) error {
	return uploadEach(ctx, r.msg, attachments, r.connector.uploadInThread)
}

// PostHandoff posts a fresh message carrying a native mention and returns its
// platform id, so the bridge can relay the target bot's reply into the same
// thread.
func (r *telegramResponder) PostHandoff(ctx context.Context, text string) (string, error) {
	return r.connector.postInThread(ctx, r.msg, text)
}

func (t *TelegramConnector) Responder(msg Message) Responder {
	progress := &progressResponder{
		start: func(ctx context.Context, text string) (string, error) {
			return t.postInThread(ctx, msg, text)
		},
		edit: func(ctx context.Context, messageID, text string) error {
			return t.editInThread(ctx, msg, messageID, text)
		},
		post: func(ctx context.Context, chunk string) error {
			_, err := t.postInThread(ctx, msg, chunk)
			return err
		},
		refreshActivity: func(ctx context.Context) {
			// Telegram's typing indicator expires on its own after ~5s and has
			// no clear operation, so clearActivity stays unset.
			payload := map[string]any{"chat_id": msg.ChannelID, "action": "typing"}
			t.applyThread(payload, msg)
			if err := t.api.call(ctx, "sendChatAction", payload, nil); err != nil {
				log.Printf("imbot: telegram send typing status: %v", err)
			}
		},
		activityInterval: telegramTypingHeartbeat,
	}
	return &telegramResponder{progressResponder: progress, connector: t, msg: msg}
}

// applyThread targets a forum topic when the triggering message came from one.
// threadID falls back to the chat id for every non-forum chat, so an equal
// value means "no topic" rather than topic zero.
func (t *TelegramConnector) applyThread(payload map[string]any, msg Message) {
	if msg.ThreadID == "" || msg.ThreadID == msg.ChannelID {
		return
	}
	topicID, err := strconv.ParseInt(msg.ThreadID, 10, 64)
	if err != nil {
		return
	}
	payload["message_thread_id"] = topicID
}

// postInThread posts one message into the originating chat (or forum topic).
// Link previews are disabled because an agent reply routinely cites URLs it is
// merely referring to, and an unfurled card per link buries the answer.
func (t *TelegramConnector) postInThread(ctx context.Context, msg Message, text string) (string, error) {
	payload := map[string]any{
		"chat_id":              msg.ChannelID,
		"text":                 TelegramHTML(text),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	t.applyThread(payload, msg)

	var sent telegramMessage
	if err := t.api.call(ctx, "sendMessage", payload, &sent); err != nil {
		return "", fmt.Errorf("telegram send message: %w", err)
	}
	return strconv.FormatInt(sent.MessageID, 10), nil
}

func (t *TelegramConnector) editInThread(ctx context.Context, msg Message, messageID, text string) error {
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram update progress: bad message id %q: %w", messageID, err)
	}
	payload := map[string]any{
		"chat_id":              msg.ChannelID,
		"message_id":           id,
		"text":                 TelegramHTML(text),
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if err := t.api.call(ctx, "editMessageText", payload, nil); err != nil {
		var apiErr *telegramError
		if errors.As(err, &apiErr) && strings.Contains(apiErr.Description, telegramNotModified) {
			return nil
		}
		return fmt.Errorf("telegram update progress: %w", err)
	}
	return nil
}

// uploadInThread sends one agent artifact as a document. Everything goes through
// sendDocument rather than sendPhoto: Telegram re-encodes and downscales photos,
// which would silently corrupt a screenshot or chart the agent produced as
// evidence.
// telegramMaxUploadBytes is the Bot API's sendDocument ceiling. It is also the
// one cap here with a local cost behind it: telegramClient.upload builds the
// whole multipart body in memory, so this bounds a single turn's allocation as
// well as the platform's answer.
const telegramMaxUploadBytes int64 = 50 << 20

func (t *TelegramConnector) uploadInThread(ctx context.Context, msg Message, attachment OutboundAttachment) error {
	if attachment.Open == nil || attachment.Size <= 0 {
		return fmt.Errorf("telegram upload %q: invalid attachment", attachment.Name)
	}
	if attachment.Size > telegramMaxUploadBytes {
		return fmt.Errorf("telegram upload %q: %d bytes exceeds Telegram's %d byte per-file limit", attachment.Name, attachment.Size, telegramMaxUploadBytes)
	}
	reader, err := attachment.Open()
	if err != nil {
		return fmt.Errorf("telegram open upload %q: %w", attachment.Name, err)
	}
	defer func() { _ = reader.Close() }()

	fields := map[string]string{"chat_id": msg.ChannelID}
	payload := map[string]any{}
	t.applyThread(payload, msg)
	if topicID, ok := payload["message_thread_id"].(int64); ok {
		fields["message_thread_id"] = strconv.FormatInt(topicID, 10)
	}
	if err := t.api.upload(ctx, "sendDocument", fields, "document", attachment.Name, reader, nil); err != nil {
		return fmt.Errorf("telegram upload %q: %w", attachment.Name, err)
	}
	return nil
}
