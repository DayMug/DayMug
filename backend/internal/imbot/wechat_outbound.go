package imbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot/wechat"
)

// wechatTypingHeartbeat is how often the typing indicator is re-asserted while
// a turn runs. How long the gateway keeps an indicator lit is not documented
// and has not been measured against the real gateway (see "尚未验证的事情" in
// docs/guides/wechat.md), so this matches the shortest window any
// mainstream platform uses — Telegram's 5s — rather than guessing longer. It is
// one light JSON POST with a cached ticket; over-refreshing costs far less than
// an indicator that quietly goes out mid-turn.
const wechatTypingHeartbeat = 5 * time.Second

// wechatResponder is the shared progress skeleton with no edit primitive.
//
// Weixin cannot revise a message it has already delivered, so the skeleton
// degrades to "stay quiet until the answer is ready": Start and Update only
// refresh the typing indicator, and Complete posts the reply — its first chunk
// through start, the rest through post, both of which send a fresh message
// here. That is why edit is deliberately absent below rather than stubbed with
// a function that silently does nothing: a nil primitive is what tells the
// skeleton to take the degraded path at all.
type wechatResponder struct {
	*progressResponder
	connector *WeChatConnector
	msg       Message
}

// PostAttachments uploads each artifact to the CDN and sends it as its own
// message. One item per message is what the gateway expects for media.
func (r *wechatResponder) PostAttachments(ctx context.Context, attachments []OutboundAttachment) error {
	return uploadEach(ctx, r.msg, attachments, r.connector.uploadInThread)
}

// uploadInThread encrypts one artifact onto the CDN and references it from a
// message item.
func (w *WeChatConnector) uploadInThread(ctx context.Context, msg Message, attachment OutboundAttachment) error {
	token := w.contextTokenFor(msg.ChannelID)
	if token == "" {
		return fmt.Errorf("wechat: no context token for %s: %w", msg.ChannelID, wechat.ErrMissingContextToken)
	}

	body, err := attachment.Open()
	if err != nil {
		return fmt.Errorf("wechat: open %q: %w", attachment.Name, err)
	}
	defer func() { _ = body.Close() }()

	// The whole payload has to be in memory anyway: the upload handshake
	// declares the plaintext MD5 and both sizes before the first byte is sent.
	// One byte past the cap tells "at the cap" from "over it".
	data, err := io.ReadAll(io.LimitReader(body, wechatMaxMediaBytes+1))
	if err != nil {
		return fmt.Errorf("wechat: read %q: %w", attachment.Name, err)
	}
	if int64(len(data)) > wechatMaxMediaBytes {
		return fmt.Errorf("wechat: %q exceeds the %d byte upload cap", attachment.Name, wechatMaxMediaBytes)
	}

	asImage := strings.HasPrefix(attachment.MIME, "image/")
	mediaType := wechat.UploadMediaFile
	if asImage {
		mediaType = wechat.UploadMediaImage
	}

	uploaded, err := w.client.UploadMedia(ctx, data, mediaType, msg.ChannelID)
	if err != nil {
		return fmt.Errorf("wechat: upload %q: %w", attachment.Name, err)
	}

	item := wechat.MessageItem{Type: wechat.ItemTypeFile, FileItem: &wechat.FileItem{
		Media:    uploaded.CDNMedia(),
		FileName: attachment.Name,
		Len:      strconv.Itoa(uploaded.PlainSize),
	}}
	if asImage {
		item = wechat.MessageItem{Type: wechat.ItemTypeImage, ImageItem: &wechat.ImageItem{
			Media: uploaded.CDNMedia(),
			// No item-level `aeskey`: the gateway sets that field on inbound
			// images, but Tencent's own plugin never sends it outbound. The
			// key travels in the media block for every outbound media type.
			//
			// Weixin sizes an inline image by its ciphertext length.
			MidSize: uploaded.CipherSize,
		}}
	}

	if _, err := w.client.SendItem(ctx, item, wechat.SendTextOptions{
		To: msg.ChannelID, ContextToken: token, RunID: msg.ThreadID,
	}); err != nil {
		return fmt.Errorf("wechat: send %q: %w", attachment.Name, err)
	}
	return nil
}

func (w *WeChatConnector) Responder(msg Message) Responder {
	progress := &progressResponder{
		start: func(ctx context.Context, text string) (string, error) {
			return w.send(ctx, msg, text)
		},
		post: func(ctx context.Context, chunk string) error {
			_, err := w.send(ctx, msg, chunk)
			return err
		},
		refreshActivity: func(ctx context.Context) {
			w.setTyping(ctx, msg, wechat.TypingActive)
		},
		clearActivity: func(ctx context.Context) {
			w.setTyping(ctx, msg, wechat.TypingCancel)
		},
		activityInterval: wechatTypingHeartbeat,
	}
	return &wechatResponder{progressResponder: progress, connector: w, msg: msg}
}

// send posts one message into the originating conversation.
func (w *WeChatConnector) send(ctx context.Context, msg Message, text string) (string, error) {
	token := w.contextTokenFor(msg.ChannelID)
	if token == "" {
		// Without it the gateway accepts the send and drops it on the floor,
		// so failing here is what turns an invisible loss into a logged error.
		return "", fmt.Errorf("wechat: no context token for %s: %w", msg.ChannelID, wechat.ErrMissingContextToken)
	}
	id, err := w.client.SendText(ctx, text, wechat.SendTextOptions{
		To:           msg.ChannelID,
		ContextToken: token,
		// Grouping every message of one turn under the thread id is what lets
		// the Weixin client fold them into a single progress block.
		RunID: msg.ThreadID,
	})
	if err != nil {
		return "", fmt.Errorf("wechat send message: %w", err)
	}
	return id, nil
}

// setTyping drives the platform-native indicator. It is the only progress
// signal available before the answer lands, but it is still advisory: a
// failure must never cost the reply, so it is logged and swallowed.
func (w *WeChatConnector) setTyping(ctx context.Context, msg Message, status int) {
	if w.typing == nil {
		return
	}
	token := w.contextTokenFor(msg.ChannelID)
	if err := w.typing.Set(ctx, msg.SenderID, token, status); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Printf("imbot: wechat send typing status: %v", err)
	}
}
