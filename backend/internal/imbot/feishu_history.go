package imbot

// 飞书历史回填：按 chat + 话题 拉取一条消息之前的上下文，并归一化成 Message。

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/DayMug/DayMug/backend/internal/prompts"
)

// Bounds for one backfill. 飞书 has no per-thread listing endpoint, so the
// loader walks the *whole chat* and filters by thread client side — a busy
// channel would otherwise cost one serial API call per 50 channel messages
// with no ceiling at all.
//
//   - feishuHistoryPageSize is the API maximum for im/v1/messages.
//   - feishuHistoryMaxPages caps serial round trips. 20 calls at 飞书's
//     per-app rate limit stay in the low single-digit seconds, which is the
//     most latency we can add before the agent has even started working.
//   - feishuHistoryMaxItems caps how many raw chat messages we hold and
//     normalize. It is deliberately expressed independently of the page cap:
//     the API is not contractually bound to honour page_size, so a single
//     oversized page must not blow the budget. 1000 scanned chat messages is
//     already far more than a thread can contribute — a thread that itself had
//     ~1000 replies would exceed what the prompt (and the 10x3800-rune
//     SplitReply ceiling on the way back) can carry, so a lower ceiling would
//     only cost context without saving anything.
//   - feishuHistoryLookback bounds the listing when the trigger carries no
//     root_id (topic groups do this), where there is no root timestamp to
//     anchor start_time on. A thread silent for over a week is effectively a
//     new conversation; DayMug's own mirrored conversation carries continuity
//     past that point.
//   - feishuHistoryTimeout keeps the backfill off the caller's unbounded ctx.
//     It must cover maxPages listings plus the root fetch and sender-name
//     lookups; 30s leaves headroom over the ~10s worst case while capping how
//     long an IM user stares at a silent thread.
const (
	feishuHistoryPageSize = 50
	feishuHistoryMaxPages = 20
	feishuHistoryMaxItems = 1000
	feishuHistoryLookback = 7 * 24 * time.Hour
	feishuHistoryTimeout  = 30 * time.Second
)

// threadMessageLoader mirrors Slack's lazy history loader: the bridge invokes
// it only after a message passes channel/mention gates. The root is fetched
// explicitly because app/webhook messages are intentionally ignored as live
// triggers, yet still provide essential context for a human reply.
func (f *FeishuConnector) threadMessageLoader(channelID, threadID, rootID, beforeMessageID, beforeCreateTime string, isDM bool) func(context.Context, string) ([]Message, error) {
	if f.rest == nil || channelID == "" || (threadID == beforeMessageID && rootID == "") {
		return nil
	}
	return func(ctx context.Context, afterMessageID string) ([]Message, error) {
		ctx, cancel := context.WithTimeout(ctx, feishuHistoryTimeout)
		defer cancel()

		anchorID := rootID
		var root *larkim.Message
		if anchorID != "" {
			var err error
			root, err = f.getMessage(ctx, anchorID)
			if err != nil {
				return nil, err
			}
		}

		builder := larkim.NewListMessageReqBuilder().
			ContainerIdType("chat").
			ContainerId(channelID).
			// Newest first. When the caps below force a truncation this keeps
			// the messages closest to the trigger — the ones a short follow-up
			// ("check it") actually refers to — instead of stranding the agent
			// with month-old chatter from the top of the window.
			SortType(larkim.ReadHistoryMessageV1SortTypeByCreateTimeDesc).
			PageSize(feishuHistoryPageSize).
			CardMsgContentType("raw_card_content")
		if start := feishuHistoryStart(root, beforeCreateTime); start != "" {
			builder.StartTime(start)
		}
		if end := feishuSeconds(beforeCreateTime); end != "" {
			builder.EndTime(end)
		}

		newestFirst := make([]*larkim.Message, 0, feishuHistoryPageSize)
		truncated := false
		pageToken := ""
		for page := 0; ; page++ {
			if page >= feishuHistoryMaxPages {
				truncated = true
				break
			}
			reqBuilder := *builder
			if pageToken != "" {
				reqBuilder.PageToken(pageToken)
			}
			resp, err := f.rest.Im.Message.List(ctx, reqBuilder.Build())
			if err != nil {
				return nil, fmt.Errorf("feishu list thread messages: %w", err)
			}
			if !resp.Success() {
				return nil, fmt.Errorf("feishu list thread messages: code %d: %s", resp.Code, resp.Msg)
			}
			if resp.Data == nil {
				break
			}
			newestFirst = append(newestFirst, resp.Data.Items...)
			next := strDeref(resp.Data.PageToken)
			// Checked before the item cap so a window that ends exactly on the
			// cap is not reported as truncated.
			if resp.Data.HasMore == nil || !*resp.Data.HasMore || next == "" {
				break
			}
			if len(newestFirst) >= feishuHistoryMaxItems {
				newestFirst = newestFirst[:feishuHistoryMaxItems]
				truncated = true
				break
			}
			if next == pageToken {
				return nil, fmt.Errorf("feishu list thread messages returned a repeated page token")
			}
			pageToken = next
		}

		// normalizeThreadMessages and the bridge's cursor both assume
		// oldest-first, so undo the descending paging order.
		slices.Reverse(newestFirst)
		raw := make([]*larkim.Message, 0, len(newestFirst)+1)
		if root != nil {
			raw = append(raw, root)
		}
		raw = append(raw, newestFirst...)

		history := f.normalizeThreadMessages(ctx, raw, channelID, threadID, anchorID, beforeMessageID, beforeCreateTime, afterMessageID, isDM)
		if truncated {
			history = append([]Message{{
				Platform:  PlatformFeishu,
				ChannelID: channelID,
				ThreadID:  threadID,
				// No MessageID: this entry has no platform counterpart, and an
				// empty id keeps the bridge from minting an im: source id for
				// it (which would dedup the warning away on a later backfill).
				Text: prompts.FeishuHistoryTruncated,
				IsDM: isDM,
			}}, history...)
		}
		return history, nil
	}
}

// feishuHistoryStart bounds how far back the chat listing may reach. The thread
// root's timestamp is the natural floor, but topic-group replies can arrive
// with an empty root_id and a root fetch can fail to yield a usable create
// time — without a floor the listing would page back to the channel's very
// first message, so fall back to a fixed window before the trigger.
func feishuHistoryStart(root *larkim.Message, beforeCreateTime string) string {
	if root != nil {
		if start := feishuSeconds(strDeref(root.CreateTime)); start != "" {
			return start
		}
	}
	before, err := strconv.ParseInt(beforeCreateTime, 10, 64)
	if err != nil || before <= 0 {
		before = time.Now().UnixMilli()
	}
	start := before/1000 - int64(feishuHistoryLookback/time.Second)
	if start < 1 {
		start = 1
	}
	return strconv.FormatInt(start, 10)
}

func (f *FeishuConnector) getMessage(ctx context.Context, messageID string) (*larkim.Message, error) {
	req := larkim.NewGetMessageReqBuilder().
		MessageId(messageID).
		UserIdType("open_id").
		CardMsgContentType("raw_card_content").
		Build()
	resp, err := f.rest.Im.Message.Get(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("feishu get root message: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf("feishu get root message: code %d: %s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 || resp.Data.Items[0] == nil {
		return nil, fmt.Errorf("feishu get root message: message %s not found", messageID)
	}
	return resp.Data.Items[0], nil
}

func (f *FeishuConnector) normalizeThreadMessages(ctx context.Context, raw []*larkim.Message, channelID, threadID, rootID, beforeMessageID, beforeCreateTime, afterMessageID string, isDM bool) []Message {
	seen := make(map[string]bool, len(raw))
	ordered := make([]*larkim.Message, 0, len(raw))
	for _, item := range raw {
		if item == nil {
			continue
		}
		messageID := strDeref(item.MessageId)
		if messageID == "" || seen[messageID] || messageID == beforeMessageID || boolDeref(item.Deleted) {
			continue
		}
		if !feishuSameThread(item, threadID, rootID) || !feishuBefore(item.CreateTime, beforeCreateTime) {
			continue
		}
		seen[messageID] = true
		ordered = append(ordered, item)
	}

	start := 0
	if afterMessageID != "" {
		for i, item := range ordered {
			if strDeref(item.MessageId) == afterMessageID {
				start = i + 1
				break
			}
		}
	}
	out := make([]Message, 0, len(ordered)-start)
	for _, item := range ordered[start:] {
		if msg, ok := f.historyMessage(ctx, item, channelID, threadID, isDM); ok {
			out = append(out, msg)
		}
	}
	return out
}

func (f *FeishuConnector) historyMessage(ctx context.Context, item *larkim.Message, channelID, threadID string, isDM bool) (Message, bool) {
	senderID, senderType, senderName := "", "", ""
	if item.Sender != nil {
		senderID = strDeref(item.Sender.Id)
		senderType = strDeref(item.Sender.SenderType)
		senderName = strDeref(item.Sender.SenderName)
	}
	// Our own edited progress cards and completed replies are already persisted
	// by the bridge. Other apps (for example alert webhooks) remain context.
	if senderType == "app" && senderID == f.cfg.AppID {
		return Message{}, false
	}
	if senderName == "" && senderType == "user" {
		senderName = f.userName(ctx, senderID)
	}

	messageID := strDeref(item.MessageId)
	content := ""
	if item.Body != nil {
		content = strDeref(item.Body.Content)
	}
	text := ""
	var attachments []Attachment
	mentioned := false
	switch strDeref(item.MsgType) {
	case "text":
		text = parseFeishuText(content)
	case "image":
		if imageKey := parseFeishuImageKey(content); imageKey != "" {
			attachments = []Attachment{f.feishuImageAttachment(messageID, imageKey)}
		}
	case "file":
		if fileKey, fileName := parseFeishuFile(content); fileKey != "" {
			attachments = []Attachment{f.feishuFileAttachment(messageID, fileKey, fileName)}
		}
	case "post":
		var imageKeys []string
		text, imageKeys, mentioned = parseFeishuPost(content, f.botOpenID)
		for _, imageKey := range imageKeys {
			attachments = append(attachments, f.feishuImageAttachment(messageID, imageKey))
		}
	case "interactive":
		text = parseFeishuInteractive(content)
	default:
		return Message{}, false
	}
	text, botMentioned := f.inlineFeishuMentions(text, feishuHistoryMentions(item.Mentions))
	mentioned = mentioned || botMentioned
	msg := Message{
		Platform:    PlatformFeishu,
		ChannelID:   channelID,
		ThreadID:    threadID,
		MessageID:   messageID,
		SenderID:    senderID,
		SenderName:  senderName,
		Text:        strings.TrimSpace(text),
		Attachments: attachments,
		Mentioned:   mentioned,
		IsDM:        isDM,
		FromBot:     senderType != "" && senderType != "user",
	}
	// The person who opened the 话题 is usually the one an answer needs to
	// address, and they may never appear again in it.
	if !msg.FromBot {
		f.mentions.remember(mentionThreadKey(channelID, threadID), senderName, senderID)
	}
	return msg, msg.Text != "" || len(msg.Attachments) > 0
}

func feishuSameThread(item *larkim.Message, threadID, rootID string) bool {
	messageID := strDeref(item.MessageId)
	if rootID != "" && (messageID == rootID || strDeref(item.RootId) == rootID) {
		return true
	}
	return threadID != "" && strDeref(item.ThreadId) == threadID
}

func feishuBefore(createTime *string, beforeCreateTime string) bool {
	before, err := strconv.ParseInt(beforeCreateTime, 10, 64)
	if err != nil || before <= 0 {
		return true
	}
	created, err := strconv.ParseInt(strDeref(createTime), 10, 64)
	return err != nil || created < before
}

func feishuSeconds(milliseconds string) string {
	value, err := strconv.ParseInt(milliseconds, 10, 64)
	if err != nil || value <= 0 {
		return ""
	}
	return strconv.FormatInt(value/1000, 10)
}

func boolDeref(value *bool) bool {
	return value != nil && *value
}
