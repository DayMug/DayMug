package imbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// FeishuConnector drives one 飞书 self-built app over the事件长连接 (WebSocket)
// mode, so no public callback URL is needed. Replies go through the REST
// reply-message API with reply_in_thread, which keeps every answer inside the
// originating 话题.
type FeishuConnector struct {
	baseConnector

	cfg       FeishuConfig
	rest      *lark.Client
	botOpenID string

	// newWS builds the 长连接 client. Tests substitute a fake; production leaves
	// it nil and gets a real larkws client.
	newWS func(*dispatcher.EventDispatcher) feishuWSClient

	wsMu sync.Mutex
	ws   feishuWSClient
}

// feishuWSClient is the subset of *larkws.Client the connector drives. Only
// Start and Close are needed, and naming them here keeps the SDK out of tests.
type feishuWSClient interface {
	Start(ctx context.Context) error
	Close()
}

func NewFeishuConnector(cfg FeishuConfig) *FeishuConnector {
	return &FeishuConnector{
		baseConnector: baseConnector{nameCache: map[string]string{}},
		cfg:           cfg,
	}
}

func (f *FeishuConnector) Platform() string { return PlatformFeishu }

func (f *FeishuConnector) Capabilities() Capabilities { return platforms[PlatformFeishu].caps }

// MentionTag returns the native text-message markup for mentioning this bot.
// Feishu does not deliver bot-authored messages to other bots, so the bridge
// also routes the handoff internally; the tag keeps that handoff visible to
// people in the originating topic as a real platform mention.
func (f *FeishuConnector) MentionTag() string {
	if f.botOpenID == "" {
		return ""
	}
	name := strings.TrimSpace(f.cfg.BotName)
	if name == "" {
		name = "Bot"
	}
	return `<at user_id="` + html.EscapeString(f.botOpenID) + `">` + html.EscapeString(name) + `</at>`
}

// Start resolves the bot's own open_id (for mention/self detection), then opens
// the 长连接 on a background goroutine. Reconnection is deliberately DayMug's
// job, not the SDK's: larkws with WithAutoReconnect(true) retries forever, never
// tells the caller the connection is dead (so the UI keeps claiming "running"),
// and its in-flight retry loop can re-open a socket after Close() — which is
// exactly how connections leaked. With auto-reconnect off, every WS-layer
// failure — a refused dial at Start, or a drop mid-life via OnDisconnected —
// reaches the supervisor's backoff instead.
func (f *FeishuConnector) Start(ctx context.Context) error {
	// Lazy so a caller can supply a pre-built client. The supervisor builds a
	// fresh connector per attempt, so this is nil on every production Start.
	if f.rest == nil {
		f.rest = newFeishuRESTClient(f.cfg.AppID, f.cfg.AppSecret)
	}
	openID, err := f.fetchBotOpenID(ctx)
	if err != nil {
		return fmt.Errorf("feishu bot info: %w", err)
	}
	// A code:0 response can still omit bot.open_id (bot capability not enabled
	// for the app). Refusing to start keeps mention matching honest: without an
	// identity the connector cannot tell a mention of itself from a mention of
	// anyone else, and require_mention channels would answer every message.
	// TestConnection already rejects this shape; Start must agree.
	if strings.TrimSpace(openID) == "" {
		return fmt.Errorf("feishu bot info: 应用未返回机器人 open_id，请确认已开启机器人能力")
	}
	f.botOpenID = openID

	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			f.handleReceive(ctx, event)
			return nil
		})
	newWS := f.newWS
	if newWS == nil {
		newWS = f.newLarkWSClient
	}
	wsClient := newWS(handler)

	f.wsMu.Lock()
	f.ws = wsClient
	f.wsMu.Unlock()

	go func() {
		// larkws.Client.Start never returns while the socket is alive: it parks
		// on `select {}` and ignores ctx entirely. So this goroutine is leaked by
		// design once a connection is up — Stop closes the socket, which is the
		// resource 飞书 actually rations (one app has a hard cap on concurrent
		// long connections). Only the failure paths make Start return.
		err := wsClient.Start(ctx)
		if ctx.Err() == nil {
			log.Printf("imbot: feishu long connection exited: %v", err)
			f.reportDown(err)
		}
	}()
	return nil
}

func (f *FeishuConnector) newLarkWSClient(handler *dispatcher.EventDispatcher) feishuWSClient {
	return larkws.NewClient(f.cfg.AppID, f.cfg.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithAutoReconnect(false),
		larkws.WithOnDisconnected(func() {
			f.reportDown(errFeishuConnectionClosed)
		}),
	)
}

// errFeishuConnectionClosed marks a 长连接 that dropped after it was
// established — the SDK's disconnect hook carries no cause of its own.
var errFeishuConnectionClosed = errors.New("飞书长连接已断开")

// Stop closes the 长连接 and detaches the inbound sink. Both halves matter:
// without Close the socket survives every config reload until 飞书's per-app
// connection cap is hit and the bot goes permanently silent, and without
// clearing the sink an abandoned connector keeps pushing messages into its dead
// generation, where they are dropped rather than answered.
func (f *FeishuConnector) Stop() {
	f.wsMu.Lock()
	ws := f.ws
	f.ws = nil
	f.wsMu.Unlock()
	if ws != nil {
		ws.Close()
	}
	f.SetOnMessage(nil)
}

func (f *FeishuConnector) handleReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) {
	if !f.hasSink() || event == nil || event.Event == nil || event.Event.Message == nil {
		return
	}
	m := event.Event.Message
	sender := event.Event.Sender

	senderID := ""
	if sender != nil && sender.SenderId != nil {
		senderID = strDeref(sender.SenderId.OpenId)
	}
	// Only human-sent messages; drop app/bot senders (including our own
	// replies echoed back into a group).
	if sender != nil && strDeref(sender.SenderType) != "user" {
		return
	}
	if f.botOpenID != "" && senderID == f.botOpenID {
		return
	}

	messageID := strDeref(m.MessageId)
	text := ""
	var attachments []Attachment
	mentioned := false
	switch strDeref(m.MessageType) {
	case "text":
		text = parseFeishuText(strDeref(m.Content))
	case "image":
		if imageKey := parseFeishuImageKey(strDeref(m.Content)); imageKey != "" {
			attachments = []Attachment{f.feishuImageAttachment(messageID, imageKey)}
		}
	case "file":
		if fileKey, fileName := parseFeishuFile(strDeref(m.Content)); fileKey != "" {
			attachments = []Attachment{f.feishuFileAttachment(messageID, fileKey, fileName)}
		}
	case "post":
		var imageKeys []string
		text, imageKeys, mentioned = parseFeishuPost(strDeref(m.Content), f.botOpenID)
		for _, imageKey := range imageKeys {
			attachments = append(attachments, f.feishuImageAttachment(messageID, imageKey))
		}
	default:
		return
	}
	text, botMentioned := f.inlineFeishuMentions(text, feishuEventMentions(m.Mentions))
	mentioned = mentioned || botMentioned

	chatType := strDeref(m.ChatType)
	msg := Message{
		Platform:  PlatformFeishu,
		ChannelID: strDeref(m.ChatId),
		// 话题 identity: topic-group messages carry thread_id; regular reply
		// chains carry root_id; a fresh top-level message roots its own thread.
		ThreadID:    firstNonEmpty(strDeref(m.ThreadId), strDeref(m.RootId), messageID),
		MessageID:   messageID,
		SenderID:    senderID,
		SenderName:  f.userName(ctx, senderID),
		Text:        strings.TrimSpace(text),
		Attachments: attachments,
		Mentioned:   mentioned,
		IsDM:        chatType == "p2p",
	}
	// handleReceive already rejected every non-user sender, so this can only
	// add humans — bots must stay unmentionable from ordinary prose.
	f.mentions.remember(mentionThreadKey(msg.ChannelID, msg.ThreadID), msg.SenderName, senderID)
	msg.LoadThreadMessages = f.threadMessageLoader(
		msg.ChannelID,
		msg.ThreadID,
		strDeref(m.RootId),
		messageID,
		strDeref(m.CreateTime),
		msg.IsDM,
	)
	if ctx.Err() != nil || (msg.Text == "" && len(msg.Attachments) == 0) || msg.ChannelID == "" {
		return
	}
	f.deliver(msg)
}

// feishuMention is the mention shape the inliner needs, normalized across the
// two payloads that carry it: the inbound event (Mentions[].Id.OpenId) and the
// history listing (Mentions[].Id).
type feishuMention struct {
	openID string
	key    string
	name   string
}

func feishuEventMentions(mentions []*larkim.MentionEvent) []feishuMention {
	out := make([]feishuMention, 0, len(mentions))
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		openID := ""
		if mention.Id != nil {
			openID = strDeref(mention.Id.OpenId)
		}
		out = append(out, feishuMention{openID: openID, key: strDeref(mention.Key), name: strDeref(mention.Name)})
	}
	return out
}

func feishuHistoryMentions(mentions []*larkim.Mention) []feishuMention {
	out := make([]feishuMention, 0, len(mentions))
	for _, mention := range mentions {
		if mention == nil {
			continue
		}
		out = append(out, feishuMention{openID: strDeref(mention.Id), key: strDeref(mention.Key), name: strDeref(mention.Name)})
	}
	return out
}

// inlineFeishuMentions rewrites the @-placeholder keys the text content carries
// (`@_user_1`): the bot's own is dropped, everyone else's is inlined as @name
// so the agent still sees who was addressed, falling back to the open_id when
// the payload carries no name. Reports whether the bot itself was mentioned.
// Mirrors the Slack behaviour in (*SlackConnector).inlineMentions.
func (f *FeishuConnector) inlineFeishuMentions(text string, mentions []feishuMention) (string, bool) {
	mentionedBot := false
	for _, mention := range mentions {
		// Fail closed on an unknown identity: claiming every mention would let
		// an @-anyone message satisfy require_mention. Start refuses to run
		// without an open_id, so in production this can only be a zero-value
		// connector in a test.
		isBot := f.botOpenID != "" && mention.openID == f.botOpenID
		if isBot {
			mentionedBot = true
		}
		if mention.key == "" {
			continue
		}
		replacement := ""
		if !isBot {
			replacement = "@" + firstNonEmpty(mention.name, mention.openID)
		}
		text = strings.ReplaceAll(text, mention.key, replacement)
	}
	return text, mentionedBot
}

func (f *FeishuConnector) feishuImageAttachment(messageID, imageKey string) Attachment {
	attachment := Attachment{ID: imageKey, Name: imageKey + ".png", MIME: "image/png"}
	attachment.Download = func(ctx context.Context, dst io.Writer) error {
		req := larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(imageKey).
			Type("image").
			Build()
		resp, err := f.rest.Im.MessageResource.Get(ctx, req)
		if err != nil {
			return fmt.Errorf("feishu image download: %w", err)
		}
		if resp.File == nil {
			return fmt.Errorf("feishu image download: code %d: %s", resp.Code, resp.Msg)
		}
		_, err = io.Copy(dst, resp.File)
		return err
	}
	return attachment
}

func (f *FeishuConnector) feishuFileAttachment(messageID, fileKey, fileName string) Attachment {
	attachment := Attachment{ID: fileKey, Name: firstNonEmpty(fileName, fileKey)}
	attachment.Download = func(ctx context.Context, dst io.Writer) error {
		req := larkim.NewGetMessageResourceReqBuilder().
			MessageId(messageID).
			FileKey(fileKey).
			Type("file").
			Build()
		resp, err := f.rest.Im.MessageResource.Get(ctx, req)
		if err != nil {
			return fmt.Errorf("feishu file download: %w", err)
		}
		if resp.File == nil {
			return fmt.Errorf("feishu file download: code %d: %s", resp.Code, resp.Msg)
		}
		_, err = io.Copy(dst, resp.File)
		return err
	}
	return attachment
}

// fetchBotOpenID reads the app's own bot identity — the v3 SDK has no typed
// wrapper for /bot/v3/info, so this goes through the raw client.
func (f *FeishuConnector) fetchBotOpenID(ctx context.Context) (string, error) {
	resp, err := f.rest.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return "", err
	}
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.Unmarshal(resp.RawBody, &body); err != nil {
		return "", err
	}
	if body.Code != 0 {
		return "", fmt.Errorf("code %d: %s", body.Code, body.Msg)
	}
	return body.Bot.OpenID, nil
}

// userName resolves an open_id to the user's display name via the contact
// API. The contact read scope is optional: without it the lookup fails once,
// the miss is cached by baseConnector, and the bridge falls back to showing
// the open_id. A connector that has not started yet has no client to ask, so
// it resolves to empty without poisoning the cache.
func (f *FeishuConnector) userName(ctx context.Context, openID string) string {
	if f.rest == nil {
		return ""
	}
	return f.cachedName(openID, func() string {
		req := larkcontact.NewGetUserReqBuilder().UserId(openID).UserIdType("open_id").Build()
		resp, err := f.rest.Contact.User.Get(ctx, req)
		if err != nil || !resp.Success() || resp.Data == nil || resp.Data.User == nil {
			return ""
		}
		return strings.TrimSpace(strDeref(resp.Data.User.Name))
	})
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
