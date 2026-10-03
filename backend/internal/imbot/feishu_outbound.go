package imbot

// 飞书出站：回复卡片 / 文本 / 资源，Responder 装配，handoff 与产物上传。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// replyCard replies one interactive markdown card inside the message's 话题
// (reply_in_thread opens the thread when replying to a top-level message) and
// returns the created message id.
func (f *FeishuConnector) replyCard(ctx context.Context, replyToID, text string) (string, error) {
	content, err := feishuMarkdownCard(text)
	if err != nil {
		return "", err
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(replyToID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(content).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := f.rest.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", fmt.Errorf("feishu reply card: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu reply card: code %d: %s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("feishu reply card: missing message id")
	}
	return *resp.Data.MessageId, nil
}

// replyText is reserved for handoff messages. Unlike a markdown card, a text
// reply accepts Feishu's native <at user_id="…"> markup and returns the new
// message id that the target bot uses as its visible reply anchor.
func (f *FeishuConnector) replyText(ctx context.Context, replyToID, text string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(replyToID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("text").
			Content(string(content)).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := f.rest.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", fmt.Errorf("feishu reply handoff: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu reply handoff: code %d: %s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("feishu reply handoff: missing message id")
	}
	return *resp.Data.MessageId, nil
}

// feishuResponder adds a platform-specific handoff operation without growing
// the shared Responder interface used by every connector and test double.
type feishuResponder struct {
	*progressResponder
	connector *FeishuConnector
	msg       Message
	handoff   func(context.Context, string) (string, error)
}

var feishuOpenID = regexp.MustCompile(`^ou_[A-Za-z0-9]+$`)

// Complete notifies the trusted sender once the final card is ready, and turns
// the "@display name" the agent wrote for anyone in this 话题 into a real
// mention — see prefixMention for why the two must not stack. Feishu cards use
// a different at-tag from text messages, so this markup must stay in the card
// markdown path instead of going through replyText.
//
// Resolution happens only on the final card. Progress messages are edits, and
// Feishu does not notify anyone about a mention an edit introduced — which is
// the same reason completeAsNew posts the answer as a fresh message.
func (r *feishuResponder) Complete(ctx context.Context, text string) error {
	threadKey := mentionThreadKey(r.msg.ChannelID, r.msg.ThreadID)
	text = resolveMentions(text, r.connector.mentions.participants(threadKey), feishuCardMentionMarkup)
	text = prefixMention(text, feishuCardMentionMarkup(r.msg.SenderID))
	return r.completeAsNew(ctx, text)
}

// feishuCardMentionMarkup renders one directory hit as a card at-tag. An id
// that is not an open_id yields "" so resolveMentions leaves the name alone.
func feishuCardMentionMarkup(openID string) string {
	if !feishuOpenID.MatchString(openID) {
		return ""
	}
	return `<at id="` + openID + `"></at>`
}

func (r *feishuResponder) PostHandoff(ctx context.Context, text string) (string, error) {
	return r.handoff(ctx, text)
}

func (r *feishuResponder) PostAttachments(ctx context.Context, attachments []OutboundAttachment) error {
	return uploadEach(ctx, r.msg, attachments, r.connector.uploadInThread)
}

// feishuMaxUploadBytes is the ceiling im/v1/files accepts. The image branch
// below has its own, lower one (10 MiB for im/v1/images); an image past that
// is not rejected, it just travels as a file instead.
const feishuMaxUploadBytes int64 = 30 << 20

func (f *FeishuConnector) uploadInThread(ctx context.Context, msg Message, attachment OutboundAttachment) error {
	if attachment.Open == nil || attachment.Size <= 0 {
		return fmt.Errorf("feishu upload %q: invalid attachment", attachment.Name)
	}
	if attachment.Size > feishuMaxUploadBytes {
		return fmt.Errorf("feishu upload %q: %d bytes exceeds Feishu's %d byte per-file limit", attachment.Name, attachment.Size, feishuMaxUploadBytes)
	}
	reader, err := attachment.Open()
	if err != nil {
		return fmt.Errorf("feishu open upload %q: %w", attachment.Name, err)
	}
	defer func() { _ = reader.Close() }()
	if strings.HasPrefix(strings.ToLower(attachment.MIME), "image/") && attachment.Size <= 10<<20 {
		req := larkim.NewCreateImageReqBuilder().
			Body(larkim.NewCreateImageReqBodyBuilder().
				ImageType(larkim.CreateImageImageTypeMessage).
				Image(reader).
				Build()).
			Build()
		resp, err := f.rest.Im.Image.Create(ctx, req)
		if err != nil {
			return fmt.Errorf("feishu upload image %q: %w", attachment.Name, err)
		}
		if !resp.Success() || resp.Data == nil || resp.Data.ImageKey == nil {
			return fmt.Errorf("feishu upload image %q: code %d: %s", attachment.Name, resp.Code, resp.Msg)
		}
		return f.replyResource(ctx, msg.MessageID, "image", "image_key", *resp.Data.ImageKey)
	}
	req := larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType(feishuFileType(attachment)).
			FileName(attachment.Name).
			File(reader).
			Build()).
		Build()
	resp, err := f.rest.Im.File.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu upload file %q: %w", attachment.Name, err)
	}
	if !resp.Success() || resp.Data == nil || resp.Data.FileKey == nil {
		return fmt.Errorf("feishu upload file %q: code %d: %s", attachment.Name, resp.Code, resp.Msg)
	}
	return f.replyResource(ctx, msg.MessageID, "file", "file_key", *resp.Data.FileKey)
}

func feishuFileType(attachment OutboundAttachment) string {
	switch strings.ToLower(filepath.Ext(attachment.Name)) {
	case ".opus":
		return larkim.CreateFileFileTypeOpus
	case ".mp4":
		return larkim.CreateFileFileTypeMp4
	case ".pdf":
		return larkim.CreateFileFileTypePdf
	case ".doc", ".docx":
		return larkim.CreateFileFileTypeDoc
	case ".xls", ".xlsx":
		return larkim.CreateFileFileTypeXls
	case ".ppt", ".pptx":
		return larkim.CreateFileFileTypePpt
	default:
		return larkim.CreateFileFileTypeStream
	}
}

func (f *FeishuConnector) replyResource(ctx context.Context, replyToID, msgType, keyName, key string) error {
	content, err := json.Marshal(map[string]string{keyName: key})
	if err != nil {
		return err
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(replyToID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(string(content)).
			ReplyInThread(true).
			Build()).
		Build()
	resp, err := f.rest.Im.Message.Reply(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu reply %s: %w", msgType, err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reply %s: code %d: %s", msgType, resp.Code, resp.Msg)
	}
	return nil
}

func (f *FeishuConnector) Responder(msg Message) Responder {
	progress := &progressResponder{
		start: func(ctx context.Context, text string) (string, error) {
			return f.replyCard(ctx, msg.MessageID, text)
		},
		edit: func(ctx context.Context, messageID, text string) error {
			content, err := feishuMarkdownCard(text)
			if err != nil {
				return err
			}
			req := larkim.NewPatchMessageReqBuilder().
				MessageId(messageID).
				Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).
				Build()
			resp, err := f.rest.Im.Message.Patch(ctx, req)
			if err != nil {
				return fmt.Errorf("feishu update progress card: %w", err)
			}
			if !resp.Success() {
				return fmt.Errorf("feishu update progress card: code %d: %s", resp.Code, resp.Msg)
			}
			return nil
		},
		// Overflow chunks are markdown cards too, so every part of a long
		// answer renders the same way as the first.
		post: func(ctx context.Context, chunk string) error {
			_, err := f.replyCard(ctx, msg.MessageID, chunk)
			return err
		},
		remove: func(ctx context.Context, messageID string) error {
			req := larkim.NewDeleteMessageReqBuilder().MessageId(messageID).Build()
			resp, err := f.rest.Im.Message.Delete(ctx, req)
			if err != nil {
				return fmt.Errorf("feishu delete progress card: %w", err)
			}
			if !resp.Success() {
				return fmt.Errorf("feishu delete progress card: code %d: %s", resp.Code, resp.Msg)
			}
			return nil
		},
	}
	return &feishuResponder{
		progressResponder: progress,
		connector:         f,
		msg:               msg,
		handoff: func(ctx context.Context, text string) (string, error) {
			return f.replyText(ctx, msg.MessageID, text)
		},
	}
}

func feishuMarkdownCard(text string) (string, error) {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"body": map[string]any{"elements": []map[string]any{{
			"tag": "markdown", "content": text,
		}}},
	}
	raw, err := json.Marshal(card)
	return string(raw), err
}
