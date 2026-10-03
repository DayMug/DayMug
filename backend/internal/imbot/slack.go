package imbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// slackFileShareSubtype marks a message whose payload carries uploaded files.
// It is the only subtype that represents new user content rather than a
// mutation of, or commentary on, an existing message.
const slackFileShareSubtype = "file_share"

// SlackConnector drives one Slack app over Socket Mode. The app needs the
// Events API subscriptions (message.channels / message.groups / message.im /
// app_mention) delivered over the socket, plus chat:write for replies.
type SlackConnector struct {
	baseConnector

	cfg       SlackConfig
	api       *slack.Client
	sm        *socketmode.Client
	botUserID string

	stopMu sync.Mutex
	// stopRun cancels the goroutines this connector's Start opened. It is a
	// child of the caller's ctx rather than the ctx itself because the
	// supervisor reuses one ctx for every attempt of the same bot: without a
	// per-attempt cancel, an abandoned attempt's event loop stays parked on
	// socketmode's Events channel (which the SDK never closes) for the life of
	// the bot.
	stopRun context.CancelFunc
	// loops tracks the goroutines Start opened so Stop can return only once
	// this attempt is genuinely quiet — the supervisor dials the replacement
	// immediately afterwards.
	loops sync.WaitGroup

	// botUsers records which resolved user ids belong to bot users. Slack gives
	// bots ordinary U… ids, so a mention of one is indistinguishable from a
	// mention of a person without asking users.info — and the mention directory
	// must not contain bots. Written by userName on a cache miss, so an id that
	// never resolved is absent and read as "assume bot".
	botMu    sync.Mutex
	botUsers map[string]bool
}

func NewSlackConnector(cfg SlackConfig) *SlackConnector {
	return &SlackConnector{
		baseConnector: baseConnector{nameCache: map[string]string{}},
		cfg:           cfg,
	}
}

func (s *SlackConnector) Platform() string { return PlatformSlack }

func (s *SlackConnector) Capabilities() Capabilities { return platforms[PlatformSlack].caps }

// MentionTag returns the mention markup wrapped in slackRawMarker. Outbound
// text is HTML escaped by SlackMrkdwn so that agent content can never inject
// live Slack markup; the marker is how the mention DayMug composes itself opts
// out of that escaping, and it survives only as the leading token of a message
// — which is where the handoff relay puts it.
func (s *SlackConnector) MentionTag() string {
	if s.botUserID == "" {
		return ""
	}
	return slackRawMarker + "<@" + s.botUserID + ">" + slackRawMarker
}

// Start validates the tokens with auth.test (also learning the bot's own user
// id for mention/self detection), then runs the Socket Mode event loop on
// background goroutines until Stop. socketmode reconnects on its own within one
// run; a permanent auth failure surfaces here, and a fatal socket failure is
// reported through SetOnDown.
func (s *SlackConnector) Start(ctx context.Context) error {
	// Lazy so a caller can supply a pre-built client (tests point it at an
	// httptest server). The supervisor builds a fresh connector per attempt, so
	// this is nil on every production Start.
	if s.api == nil {
		s.api = slack.New(s.cfg.BotToken, slack.OptionAppLevelToken(s.cfg.AppToken))
	}
	authTest, err := s.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("slack auth.test: %w", err)
	}
	if err := validateSlackBotIdentity(authTest); err != nil {
		return err
	}
	s.botUserID = authTest.UserID
	s.sm = socketmode.New(s.api)

	runCtx, cancel := context.WithCancel(ctx)
	s.stopMu.Lock()
	s.stopRun = cancel
	s.stopMu.Unlock()

	s.loops.Add(2)
	go func() {
		defer s.loops.Done()
		err := s.sm.RunContext(runCtx)
		if runCtx.Err() == nil {
			log.Printf("imbot: slack socket mode exited: %v", err)
			s.reportDown(err)
		}
	}()
	go func() {
		defer s.loops.Done()
		s.eventLoop(runCtx)
	}()
	return nil
}

// Stop ends this attempt's Socket Mode run, waits for its goroutines, and
// detaches the inbound sink. Cancelling runCtx is the only way out for the
// event loop: socketmode never closes its Events channel, so an abandoned
// attempt would otherwise sit on it for as long as the bot exists — one leaked
// goroutine (and one live socket) per reconnect.
func (s *SlackConnector) Stop() {
	s.stopMu.Lock()
	cancel := s.stopRun
	s.stopRun = nil
	s.stopMu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.loops.Wait()
	s.SetOnMessage(nil)
}

func validateSlackBotIdentity(auth *slack.AuthTestResponse) error {
	if auth == nil || strings.TrimSpace(auth.BotID) == "" {
		return errors.New("slack auth.test: configured bot_token is not a bot identity; refusing to send as a user")
	}
	return nil
}

func (s *SlackConnector) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-s.sm.Events:
			if !ok {
				return
			}
			if evt.Type != socketmode.EventTypeEventsAPI {
				continue
			}
			apiEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
			if !ok {
				continue
			}
			// Ack before processing: agent runs take minutes and an unacked
			// envelope gets redelivered after a few seconds.
			if evt.Request != nil {
				if err := s.sm.Ack(*evt.Request); err != nil {
					log.Printf("imbot: slack ack: %v", err)
				}
			}
			if apiEvent.Type != slackevents.CallbackEvent {
				continue
			}
			switch ev := apiEvent.InnerEvent.Data.(type) {
			case *slackevents.MessageEvent:
				s.handleMessage(ctx, ev)
			case *slackevents.AppMentionEvent:
				// A channel @-mention arrives as BOTH app_mention and (with
				// the message.* subscriptions) a message event. Normalizing
				// both under the same MessageID lets the manager's dedup
				// collapse them, while apps subscribed only to app_mention
				// still work.
				s.handleAppMention(ctx, ev)
			}
		}
	}
}

func (s *SlackConnector) handleMessage(ctx context.Context, ev *slackevents.MessageEvent) {
	// Slack delivers an uploaded image or file as a message carrying the
	// file_share subtype, so it has to pass; skip edits, deletions, joins and
	// every other subtype, plus our own echoes. Other bots' plain messages
	// pass through flagged FromBot — the bridge only acts on them in channels
	// that opted into bot relays. A file_share that also @-mentions the bot
	// arrives a second time as app_mention; Manager dedups on MessageID.
	if (ev.SubType != "" && ev.SubType != slackFileShareSubtype) || ev.User == s.botUserID {
		return
	}
	fromBot := ev.BotID != ""
	if ev.User == "" && !fromBot {
		return
	}
	mentionTag := "<@" + s.botUserID + ">"
	var files []slack.File
	var attachments []slack.Attachment
	if ev.Message != nil {
		files = ev.Message.Files
		// MessageEvent has no top-level attachments field; slack-go mirrors the
		// raw payload into Message, which is where a bot's alarm card lives.
		attachments = ev.Message.Attachments
	}
	text := slackMessageText(ev.Text, ev.Blocks, attachments)
	// Computed from the raw event text: inlineMentions rewrites the bot's own
	// tag away, so mention detection has to read the original.
	mentioned := strings.Contains(text, mentionTag)
	threadKey := mentionThreadKey(ev.Channel, firstNonEmpty(ev.ThreadTimeStamp, ev.TimeStamp))
	msg := Message{
		Platform:    PlatformSlack,
		ChannelID:   ev.Channel,
		ThreadID:    firstNonEmpty(ev.ThreadTimeStamp, ev.TimeStamp),
		MessageID:   ev.Channel + "|" + ev.TimeStamp,
		SenderID:    firstNonEmpty(ev.User, ev.BotID),
		SenderName:  s.userName(ctx, ev.User),
		Text:        s.inlineMentions(ctx, threadKey, text),
		Attachments: s.slackAttachments(files),
		Mentioned:   mentioned,
		IsDM:        ev.ChannelType == "im",
		FromBot:     fromBot,
	}
	s.rememberSender(threadKey, msg.SenderID, msg.SenderName, fromBot)
	msg.LoadThreadMessages = s.threadMessageLoader(ev.Channel, ev.ThreadTimeStamp, ev.TimeStamp, msg.IsDM)
	s.emit(ctx, msg)
}

func (s *SlackConnector) handleAppMention(ctx context.Context, ev *slackevents.AppMentionEvent) {
	if ev.User == s.botUserID || (ev.User == "" && ev.BotID == "") {
		return
	}
	threadKey := mentionThreadKey(ev.Channel, firstNonEmpty(ev.ThreadTimeStamp, ev.TimeStamp))
	msg := Message{
		Platform:    PlatformSlack,
		ChannelID:   ev.Channel,
		ThreadID:    firstNonEmpty(ev.ThreadTimeStamp, ev.TimeStamp),
		MessageID:   ev.Channel + "|" + ev.TimeStamp,
		SenderID:    firstNonEmpty(ev.User, ev.BotID),
		SenderName:  s.userName(ctx, ev.User),
		Text:        s.inlineMentions(ctx, threadKey, slackMessageText(ev.Text, ev.Blocks, ev.Attachments)),
		Attachments: s.slackAttachments(ev.Files),
		Mentioned:   true,
		FromBot:     ev.BotID != "",
	}
	s.rememberSender(threadKey, msg.SenderID, msg.SenderName, msg.FromBot)
	msg.LoadThreadMessages = s.threadMessageLoader(ev.Channel, ev.ThreadTimeStamp, ev.TimeStamp, false)
	s.emit(ctx, msg)
}

func (s *SlackConnector) threadMessageLoader(channelID, threadTS, beforeTS string, isDM bool) func(context.Context, string) ([]Message, error) {
	if threadTS == "" || s.api == nil {
		return nil
	}
	return func(ctx context.Context, afterMessageID string) ([]Message, error) {
		oldest := ""
		if strings.HasPrefix(afterMessageID, channelID+"|") {
			oldest = strings.TrimPrefix(afterMessageID, channelID+"|")
		}

		out := make([]Message, 0, 16)
		cursor := ""
		for {
			messages, _, nextCursor, err := s.api.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
				ChannelID: channelID,
				Timestamp: threadTS,
				Cursor:    cursor,
				Oldest:    oldest,
				Latest:    beforeTS,
				Limit:     100,
			})
			if err != nil {
				return nil, fmt.Errorf("slack conversations.replies: %w", err)
			}
			for _, history := range messages {
				// conversations.replies always puts the thread parent first,
				// even when oldest points at a later reply. The parent belongs
				// in the initial backfill, but importing it again on every
				// later mention duplicates both its text and attachments.
				repeatedRoot := oldest != "" && history.Timestamp == threadTS
				if history.Timestamp == "" || repeatedRoot || history.Timestamp == oldest || history.Timestamp == beforeTS || history.Hidden || history.User == s.botUserID {
					continue
				}
				senderID := firstNonEmpty(history.User, history.BotID)
				senderName := ""
				if history.User != "" {
					senderName = s.userName(ctx, history.User)
				} else if history.BotProfile != nil {
					senderName = history.BotProfile.Name
				}
				senderName = firstNonEmpty(senderName, history.Username, senderID)
				text := slackMessageText(history.Text, history.Blocks, history.Attachments)
				mentioned := strings.Contains(text, "<@"+s.botUserID+">")
				threadKey := mentionThreadKey(channelID, threadTS)
				msg := Message{
					Platform:    PlatformSlack,
					ChannelID:   channelID,
					ThreadID:    threadTS,
					MessageID:   channelID + "|" + history.Timestamp,
					SenderID:    senderID,
					SenderName:  senderName,
					Text:        s.inlineMentions(ctx, threadKey, text),
					Attachments: s.slackAttachments(history.Files),
					Mentioned:   mentioned,
					IsDM:        isDM,
					FromBot:     history.BotID != "",
				}
				// The person who opened the thread is usually the one an answer
				// needs to address, and they may never appear again in it.
				s.rememberSender(threadKey, senderID, senderName, msg.FromBot)
				if strings.TrimSpace(msg.Text) != "" || len(msg.Attachments) > 0 {
					out = append(out, msg)
				}
			}
			if nextCursor == "" {
				break
			}
			if nextCursor == cursor {
				return nil, errors.New("slack conversations.replies returned a repeated cursor")
			}
			cursor = nextCursor
		}
		return out, nil
	}
}

func (s *SlackConnector) emit(ctx context.Context, msg Message) {
	if ctx.Err() != nil || (strings.TrimSpace(msg.Text) == "" && len(msg.Attachments) == 0) {
		return
	}
	s.deliver(msg)
}

func (s *SlackConnector) slackAttachments(files []slack.File) []Attachment {
	out := make([]Attachment, 0, len(files))
	for _, file := range files {
		downloadURL := firstNonEmpty(file.URLPrivateDownload, file.URLPrivate)
		if downloadURL == "" {
			continue
		}
		name := firstNonEmpty(file.Name, file.Title, file.ID)
		attachment := Attachment{
			ID:   file.ID,
			Name: name,
			MIME: file.Mimetype,
			Size: int64(file.Size),
		}
		attachment.Download = func(ctx context.Context, dst io.Writer) error {
			return s.api.GetFileContext(ctx, downloadURL, dst)
		}
		out = append(out, attachment)
	}
	return out
}

// postInThread posts one message into the thread of the triggering message;
// replying to a top-level channel message opens a thread on it, per the
// always-reply-in-thread rule. Agent Markdown is converted to Slack mrkdwn so
// bold/links/headings render instead of showing raw asterisks. SlackMrkdwn
// escapes &, < and > in the agent's own text, so slack-go's escape flag stays
// false: escaping again would mangle the markup SlackMrkdwn just generated.
func (s *SlackConnector) postInThread(ctx context.Context, msg Message, text string) (string, error) {
	_, ts, err := s.api.PostMessageContext(ctx, msg.ChannelID,
		slack.MsgOptionText(SlackMrkdwn(text), false),
		slack.MsgOptionTS(msg.ThreadID),
	)
	if err != nil {
		return "", fmt.Errorf("slack post message: %w", err)
	}
	return ts, nil
}

type slackResponder struct {
	*progressResponder
	connector *SlackConnector
	msg       Message
}

// Complete notifies a trusted human sender once the final answer is ready, and
// turns the "@display name" the agent wrote for anyone in this thread into a
// real mention — see prefixMention for why the two must not stack. Bot senders
// are not notified implicitly: doing so would turn
// every ordinary bot reply into another relay, bypassing the explicit HANDOFF
// protocol. All markers are composed here from ids DayMug observed inbound;
// agent-authored mention *syntax* remains escaped by SlackMrkdwn.
//
// Resolution happens only on the final answer. Progress messages are edits, and
// Slack does not notify anyone about a mention an edit introduced — which is
// the same reason completeAsNew posts the answer as a fresh message.
func (r *slackResponder) Complete(ctx context.Context, text string) error {
	threadKey := mentionThreadKey(r.msg.ChannelID, r.msg.ThreadID)
	text = resolveMentions(text, r.connector.mentions.participants(threadKey), r.connector.slackMentionMarkup)
	if !r.msg.FromBot {
		text = prefixMention(text, r.connector.slackMentionMarkup(r.msg.SenderID))
	}
	return r.completeAsNew(ctx, text)
}

// PostHandoff replaces the progress message instead of appending a second
// visible reply. The native mention must be posted as a new Slack message so
// the target bot receives an event; deleting the progress message first keeps
// the handoff turn to one visible message.
func (r *slackResponder) PostHandoff(ctx context.Context, text string) (string, error) {
	if r.messageID != "" {
		if _, _, err := r.connector.api.DeleteMessageContext(ctx, r.msg.ChannelID, r.messageID); err != nil {
			return "", fmt.Errorf("slack delete progress for handoff: %w", err)
		}
		r.messageID = ""
	}
	return r.connector.postInThread(ctx, r.msg, text)
}

func (r *slackResponder) PostAttachments(ctx context.Context, attachments []OutboundAttachment) error {
	return uploadEach(ctx, r.msg, attachments, r.connector.uploadInThread)
}

// slackMaxUploadBytes is Slack's own per-file ceiling. Nothing on this side
// needs a tighter one: the external-upload flow streams the file straight off
// disk into the presigned URL, so a large artifact costs no more memory here
// than a small one.
const slackMaxUploadBytes int64 = 1 << 30

func (s *SlackConnector) uploadInThread(ctx context.Context, msg Message, attachment OutboundAttachment) error {
	if attachment.Open == nil || attachment.Size <= 0 {
		return fmt.Errorf("slack upload %q: invalid attachment", attachment.Name)
	}
	if attachment.Size > slackMaxUploadBytes {
		return fmt.Errorf("slack upload %q: %d bytes exceeds Slack's %d byte per-file limit", attachment.Name, attachment.Size, slackMaxUploadBytes)
	}
	uploadParams := slack.GetUploadURLExternalParameters{
		FileName: attachment.Name,
		FileSize: int(attachment.Size),
	}
	if strings.HasPrefix(attachment.MIME, "image/") {
		uploadParams.AltTxt = attachment.Name
	}
	upload, err := s.api.GetUploadURLExternalContext(ctx, uploadParams)
	if err != nil {
		return fmt.Errorf("slack get upload URL: %w", err)
	}
	reader, err := attachment.Open()
	if err != nil {
		return fmt.Errorf("slack open upload %q: %w", attachment.Name, err)
	}
	uploadErr := uploadSlackFile(ctx, upload.UploadURL, reader, attachment.Size)
	closeErr := reader.Close()
	if uploadErr != nil {
		return fmt.Errorf("slack upload %q: %w", attachment.Name, uploadErr)
	}
	if closeErr != nil {
		return fmt.Errorf("slack close upload %q: %w", attachment.Name, closeErr)
	}
	completed, err := s.api.CompleteUploadExternalContext(ctx, slack.CompleteUploadExternalParameters{
		Files:           []slack.FileSummary{{ID: upload.FileID, Title: attachment.Name}},
		Channel:         msg.ChannelID,
		ThreadTimestamp: msg.ThreadID,
		// Slack can accept a completed upload without creating a visible file
		// message. A comment makes the share materialize in the originating
		// thread instead of leaving the file silently hosted in the workspace.
		InitialComment: attachment.Name,
	})
	if err != nil {
		return fmt.Errorf("slack complete upload %q: %w", attachment.Name, err)
	}
	if completed == nil {
		return fmt.Errorf("slack complete upload %q: empty response", attachment.Name)
	}
	for _, file := range completed.Files {
		if file.ID == upload.FileID {
			return nil
		}
	}
	return fmt.Errorf("slack complete upload %q: response omitted file %s", attachment.Name, upload.FileID)
}

func uploadSlackFile(ctx context.Context, uploadURL string, reader io.Reader, size int64) error {
	// The caller owns reader and closes it after the upload. Wrapping it keeps
	// net/http from closing an underlying *os.File when the request completes.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, io.NopCloser(reader))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if detail := strings.TrimSpace(string(body)); detail != "" {
			return fmt.Errorf("unexpected status %s: %s", resp.Status, detail)
		}
		return fmt.Errorf("unexpected status %s", resp.Status)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (s *SlackConnector) Responder(msg Message) Responder {
	progress := &progressResponder{
		fitPreview: FitSlackProgress,
		start: func(ctx context.Context, text string) (string, error) {
			return s.postInThread(ctx, msg, text)
		},
		edit: func(ctx context.Context, ts, text string) error {
			_, _, _, err := s.api.UpdateMessageContext(ctx, msg.ChannelID, ts,
				slack.MsgOptionText(SlackMrkdwn(text), false))
			if err != nil {
				return fmt.Errorf("slack update progress: %w", err)
			}
			return nil
		},
		post: func(ctx context.Context, chunk string) error {
			_, err := s.postInThread(ctx, msg, chunk)
			return err
		},
		remove: func(ctx context.Context, ts string) error {
			if _, _, err := s.api.DeleteMessageContext(ctx, msg.ChannelID, ts); err != nil {
				return fmt.Errorf("slack delete progress: %w", err)
			}
			return nil
		},
		refreshActivity: func(ctx context.Context) {
			if err := s.api.SetAssistantThreadsStatusContext(ctx, slack.AssistantThreadsSetStatusParameters{
				ChannelID: msg.ChannelID,
				ThreadTS:  msg.ThreadID,
				Status:    TypingStatusText,
			}); err != nil {
				log.Printf("imbot: slack set typing status: %v", err)
			}
		},
		clearActivity: func(ctx context.Context) {
			if err := s.api.SetAssistantThreadsStatusContext(ctx, slack.AssistantThreadsSetStatusParameters{
				ChannelID: msg.ChannelID,
				ThreadTS:  msg.ThreadID,
				Status:    "",
			}); err != nil {
				log.Printf("imbot: slack clear typing status: %v", err)
			}
		},
	}
	return &slackResponder{progressResponder: progress, connector: s, msg: msg}
}

// userName resolves a user id to a human-readable name. Bot users resolve
// through the same users.info call; caching lives in baseConnector. A
// connector that has not started yet has no client to ask, so it resolves to
// empty instead of panicking.
func (s *SlackConnector) userName(ctx context.Context, userID string) string {
	return s.cachedName(userID, func() string {
		if s.api == nil {
			return ""
		}
		info, err := s.api.GetUserInfoContext(ctx, userID)
		if err != nil {
			return ""
		}
		s.botMu.Lock()
		if s.botUsers == nil {
			s.botUsers = map[string]bool{}
		}
		s.botUsers[userID] = info.IsBot
		s.botMu.Unlock()
		return firstNonEmpty(info.Profile.DisplayName, info.RealName, info.Name)
	})
}

// isHumanUser reports whether users.info has confirmed this id is a person. An
// id nobody ever resolved answers false: the mention directory fails closed
// rather than risk turning ordinary prose into a bot trigger.
func (s *SlackConnector) isHumanUser(userID string) bool {
	s.botMu.Lock()
	defer s.botMu.Unlock()
	isBot, resolved := s.botUsers[userID]
	return resolved && !isBot
}

// slackMentionPattern matches Slack's escaped user mention, both the bare
// `<@U0123>` form and the legacy labelled `<@U0123|alice>` one. Enterprise Grid
// ids start with W. Channel/broadcast escapes (`<!here>`, `<#C1|general>`) are
// deliberately left alone.
var slackMentionPattern = regexp.MustCompile(`<@([UW][A-Z0-9]+)(?:\|[^>]*)?>`)

// inlineMentions rewrites the user mentions Slack escapes in message text: the
// bot's own mention is dropped, everyone else's becomes `@name` so the agent
// can tell who the message addresses. An unresolvable name degrades to the
// bare user id — a raw `<@U0123>` in the prompt is meaningless to the agent,
// and deleting it outright would lose who was addressed. Mirrors the 飞书
// behaviour in inlineFeishuMentions.
//
// Everyone named this way joins threadKey's mention directory, so the agent can
// address them back by the same name it was shown — the reverse rewrite lives
// in slackResponder.Complete.
func (s *SlackConnector) inlineMentions(ctx context.Context, threadKey, text string) string {
	inlined := slackMentionPattern.ReplaceAllStringFunc(text, func(match string) string {
		userID := slackMentionPattern.FindStringSubmatch(match)[1]
		if userID == s.botUserID {
			return ""
		}
		name := s.userName(ctx, userID)
		if s.isHumanUser(userID) {
			s.mentions.remember(threadKey, name, userID)
		}
		return "@" + firstNonEmpty(name, userID)
	})
	return strings.TrimSpace(inlined)
}

// rememberSender adds a message's author to the thread's mention directory.
// Bot authors are excluded: a bot reachable by name from ordinary prose would
// bypass the deliberate [HANDOFF @Bot] protocol.
func (s *SlackConnector) rememberSender(threadKey, senderID, senderName string, fromBot bool) {
	if fromBot {
		return
	}
	s.mentions.remember(threadKey, senderName, senderID)
}

// slackMentionMarkup renders one directory hit as live Slack markup. The raw
// marker is what opts it out of SlackMrkdwn's escaping; an id that does not
// look like a user id yields "" so resolveMentions leaves the text alone.
func (s *SlackConnector) slackMentionMarkup(userID string) string {
	tag := "<@" + userID + ">"
	if !slackRawMention.MatchString(tag) {
		return ""
	}
	return slackRawMarker + tag + slackRawMarker
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
