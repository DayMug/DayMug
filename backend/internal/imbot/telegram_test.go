package imbot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// telegramStub is a fake Bot API endpoint. Handlers are registered per method
// name; anything unregistered answers with an empty successful envelope so a
// test only has to describe the calls it cares about.
type telegramStub struct {
	*httptest.Server

	mu       sync.Mutex
	handlers map[string]telegramStubHandler
	calls    map[string][]map[string]any
}

// telegramStubHandler receives the already-decoded request payload, because the
// stub consumes the body to record the call.
type telegramStubHandler func(w http.ResponseWriter, r *http.Request, payload map[string]any)

func newTelegramStub(t *testing.T) *telegramStub {
	t.Helper()
	stub := &telegramStub{
		handlers: map[string]telegramStubHandler{},
		calls:    map[string][]map[string]any{},
	}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)

		payload := map[string]any{}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &payload)
		}
		stub.mu.Lock()
		stub.calls[method] = append(stub.calls[method], payload)
		handler := stub.handlers[method]
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if handler != nil {
			handler(w, r, payload)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(stub.Close)
	return stub
}

func (s *telegramStub) on(method string, handler telegramStubHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

// reply registers a static JSON envelope for one method.
func (s *telegramStub) reply(method, envelope string) {
	s.on(method, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_, _ = io.WriteString(w, envelope)
	})
}

func (s *telegramStub) callsTo(method string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.calls[method]...)
}

// newTestTelegramConnector wires a connector to the stub with the bot identity
// already resolved, so inbound tests can call handleMessage directly.
func newTestTelegramConnector(t *testing.T, stub *telegramStub) (*TelegramConnector, *[]Message) {
	t.Helper()
	connector := NewTelegramConnector(TelegramConfig{Enabled: true, BotToken: "test-token"})
	connector.api = &telegramClient{token: "test-token", baseURL: stub.URL, http: stub.Client()}
	connector.botUserID = 42
	connector.botUsername = "daymugbot"

	var mu sync.Mutex
	received := &[]Message{}
	connector.SetOnMessage(func(msg Message) {
		mu.Lock()
		defer mu.Unlock()
		*received = append(*received, msg)
	})
	return connector, received
}

func telegramTextMessage(chatID int64, chatType, text string) *telegramMessage {
	return &telegramMessage{
		MessageID: 7,
		From:      &telegramUser{ID: 1001, FirstName: "Ada", LastName: "Lovelace", Username: "ada"},
		Chat:      &telegramChat{ID: chatID, Type: chatType, Title: "eng"},
		Text:      text,
	}
}

func TestTelegramHandleMessageNormalizesDirectChat(t *testing.T) {
	stub := newTelegramStub(t)
	connector, received := newTestTelegramConnector(t, stub)

	connector.handleMessage(context.Background(), telegramTextMessage(-100, "private", "ship it"))

	if len(*received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(*received))
	}
	msg := (*received)[0]
	if msg.Platform != PlatformTelegram {
		t.Errorf("platform = %q, want %q", msg.Platform, PlatformTelegram)
	}
	if msg.ChannelID != "-100" {
		t.Errorf("channel = %q, want %q", msg.ChannelID, "-100")
	}
	// A non-forum chat has no threading primitive, so the chat is the session.
	if msg.ThreadID != "-100" {
		t.Errorf("thread = %q, want the chat id", msg.ThreadID)
	}
	if msg.MessageID != "-100|7" {
		t.Errorf("message id = %q, want %q", msg.MessageID, "-100|7")
	}
	if !msg.IsDM {
		t.Error("private chat should be flagged as a DM")
	}
	if msg.SenderName != "Ada Lovelace" {
		t.Errorf("sender name = %q, want %q", msg.SenderName, "Ada Lovelace")
	}
	if msg.Text != "ship it" {
		t.Errorf("text = %q", msg.Text)
	}
	// The Bot API exposes no history read, so promising backfill would be a lie.
	if msg.LoadThreadMessages != nil {
		t.Error("Telegram cannot backfill thread history; LoadThreadMessages must stay nil")
	}
}

func TestTelegramForumTopicGetsItsOwnThread(t *testing.T) {
	stub := newTelegramStub(t)
	connector, received := newTestTelegramConnector(t, stub)

	m := telegramTextMessage(-100, "supergroup", "in topic")
	m.IsTopicMessage = true
	m.MessageThreadID = 55
	connector.handleMessage(context.Background(), m)

	if len(*received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(*received))
	}
	if got := (*received)[0].ThreadID; got != "55" {
		t.Errorf("thread = %q, want the forum topic id %q", got, "55")
	}
}

func TestTelegramMentionDetection(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*telegramMessage)
		wantHit bool
	}{
		{
			name:    "plain text is not a mention",
			mutate:  func(*telegramMessage) {},
			wantHit: false,
		},
		{
			name:    "username mention",
			mutate:  func(m *telegramMessage) { m.Text = "@daymugbot status?" },
			wantHit: true,
		},
		{
			name: "text_mention entity naming the bot",
			mutate: func(m *telegramMessage) {
				m.Entities = []telegramEntity{{Type: "text_mention", User: &telegramUser{ID: 42}}}
			},
			wantHit: true,
		},
		{
			name: "text_mention entity naming somebody else",
			mutate: func(m *telegramMessage) {
				m.Entities = []telegramEntity{{Type: "text_mention", User: &telegramUser{ID: 9}}}
			},
			wantHit: false,
		},
		{
			name: "replying to the bot addresses it",
			mutate: func(m *telegramMessage) {
				m.ReplyToMessage = &telegramMessage{From: &telegramUser{ID: 42, IsBot: true}}
			},
			wantHit: true,
		},
		{
			name: "replying to somebody else does not",
			mutate: func(m *telegramMessage) {
				m.ReplyToMessage = &telegramMessage{From: &telegramUser{ID: 9}}
			},
			wantHit: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newTelegramStub(t)
			connector, received := newTestTelegramConnector(t, stub)

			m := telegramTextMessage(-100, "supergroup", "status?")
			tt.mutate(m)
			connector.handleMessage(context.Background(), m)

			if len(*received) != 1 {
				t.Fatalf("expected 1 message, got %d", len(*received))
			}
			if got := (*received)[0].Mentioned; got != tt.wantHit {
				t.Errorf("Mentioned = %v, want %v", got, tt.wantHit)
			}
		})
	}
}

// The bot's own handle addresses DayMug rather than saying anything; leaving it
// in the prompt makes the agent think a third party was named.
func TestTelegramStripsItsOwnMentionFromThePrompt(t *testing.T) {
	stub := newTelegramStub(t)
	connector, received := newTestTelegramConnector(t, stub)

	connector.handleMessage(context.Background(), telegramTextMessage(-100, "supergroup", "@daymugbot deploy @ada"))

	if len(*received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(*received))
	}
	if got := (*received)[0].Text; got != "deploy @ada" {
		t.Errorf("text = %q, want %q", got, "deploy @ada")
	}
}

func TestTelegramDropsUninterestingMessages(t *testing.T) {
	tests := []struct {
		name string
		m    *telegramMessage
	}{
		{
			name: "its own echo",
			m: &telegramMessage{
				MessageID: 7,
				From:      &telegramUser{ID: 42, IsBot: true},
				Chat:      &telegramChat{ID: -100, Type: "supergroup"},
				Text:      "earlier reply",
			},
		},
		{
			name: "no text and no attachment",
			m:    telegramTextMessage(-100, "supergroup", "   "),
		},
		{
			name: "no sender",
			m: &telegramMessage{
				MessageID: 7,
				Chat:      &telegramChat{ID: -100, Type: "supergroup"},
				Text:      "orphan",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newTelegramStub(t)
			connector, received := newTestTelegramConnector(t, stub)

			connector.handleMessage(context.Background(), tt.m)

			if len(*received) != 0 {
				t.Fatalf("expected the message to be dropped, got %d", len(*received))
			}
		})
	}
}

// Another bot's message must still reach the bridge, flagged, because channels
// can opt into bot-to-bot relays.
func TestTelegramFlagsOtherBots(t *testing.T) {
	stub := newTelegramStub(t)
	connector, received := newTestTelegramConnector(t, stub)

	m := telegramTextMessage(-100, "supergroup", "handing off")
	m.From = &telegramUser{ID: 77, IsBot: true, FirstName: "Other"}
	connector.handleMessage(context.Background(), m)

	if len(*received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(*received))
	}
	if !(*received)[0].FromBot {
		t.Error("a message from another bot must be flagged FromBot")
	}
}

func TestTelegramAttachmentsPickTheLargestPhoto(t *testing.T) {
	stub := newTelegramStub(t)
	connector, received := newTestTelegramConnector(t, stub)

	m := telegramTextMessage(-100, "private", "look")
	m.Photo = []telegramPhotoSize{
		{FileID: "small", Width: 90, FileSize: 100},
		{FileID: "large", Width: 1280, FileSize: 9000},
		{FileID: "medium", Width: 320, FileSize: 900},
	}
	connector.handleMessage(context.Background(), m)

	if len(*received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(*received))
	}
	attachments := (*received)[0].Attachments
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].ID != "large" {
		t.Errorf("attachment id = %q, want the largest pre-scaled size", attachments[0].ID)
	}
}

// File paths expire after about an hour, so getFile must run at download time
// rather than while the message is queued behind a long agent run.
func TestTelegramAttachmentResolvesFilePathLazily(t *testing.T) {
	stub := newTelegramStub(t)
	connector, received := newTestTelegramConnector(t, stub)
	stub.reply("getFile", `{"ok":true,"result":{"file_path":"documents/plan.txt"}}`)
	stub.on("plan.txt", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_, _ = io.WriteString(w, "file body")
	})

	m := telegramTextMessage(-100, "private", "here")
	m.Document = &telegramFile{FileID: "doc-1", FileName: "plan.txt", MimeType: "text/plain", FileSize: 9}
	connector.handleMessage(context.Background(), m)

	if len(*received) != 1 {
		t.Fatalf("expected 1 message, got %d", len(*received))
	}
	if calls := stub.callsTo("getFile"); len(calls) != 0 {
		t.Fatalf("getFile must not run during normalization, got %d calls", len(calls))
	}

	var body strings.Builder
	if err := (*received)[0].Attachments[0].Download(context.Background(), &body); err != nil {
		t.Fatalf("download: %v", err)
	}
	if body.String() != "file body" {
		t.Errorf("downloaded %q", body.String())
	}
	if calls := stub.callsTo("getFile"); len(calls) != 1 {
		t.Fatalf("expected 1 getFile call at download time, got %d", len(calls))
	}
}

func TestTelegramStartRejectsAUserToken(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("getMe", `{"ok":true,"result":{"id":5,"is_bot":false,"username":"human"}}`)

	err := connector.Start(context.Background())
	if err == nil {
		t.Fatal("expected Start to refuse a non-bot identity")
	}
	if !strings.Contains(err.Error(), "not a bot identity") {
		t.Errorf("error = %v", err)
	}
}

// Telegram holds undelivered updates for 24h. Without discarding them a
// restart would replay — and answer — day-old messages, which neither Slack
// nor 飞书 ever do.
func TestTelegramStartDiscardsBacklog(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("getMe", `{"ok":true,"result":{"id":42,"is_bot":true,"username":"daymugbot"}}`)
	stub.on("getUpdates", func(w http.ResponseWriter, r *http.Request, payload map[string]any) {
		if offset, ok := payload["offset"].(float64); ok && offset == -1 {
			_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":900}]}`)
			return
		}
		<-r.Context().Done()
	})

	if err := connector.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer connector.Stop()

	if connector.offset != 901 {
		t.Errorf("offset = %d, want 901 so the backlog is acknowledged", connector.offset)
	}
	if connector.MentionTag() != "@daymugbot" {
		t.Errorf("MentionTag = %q", connector.MentionTag())
	}
}

// A revoked token or a still-registered webhook stays broken until a human
// acts, so the poll loop must surface it instead of retrying silently.
func TestTelegramPollLoopReportsPermanentFailureImmediately(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("getUpdates", `{"ok":false,"error_code":409,"description":"Conflict: can't use getUpdates method while webhook is active"}`)

	down := make(chan error, 1)
	connector.SetOnDown(func(err error) { down <- err })

	go connector.pollLoop(context.Background())

	select {
	case err := <-down:
		if !strings.Contains(err.Error(), "409") {
			t.Errorf("reported %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a permanent failure was not reported")
	}
	if calls := stub.callsTo("getUpdates"); len(calls) != 1 {
		t.Errorf("expected exactly 1 attempt, got %d", len(calls))
	}
}

func TestTelegramResponderPostsHTML(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("sendMessage", `{"ok":true,"result":{"message_id":314}}`)

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "-100"})
	if err := responder.Start(context.Background(), "**working** on it"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	calls := stub.callsTo("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("expected 1 sendMessage, got %d", len(calls))
	}
	if got := calls[0]["parse_mode"]; got != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", got)
	}
	if got := calls[0]["text"]; got != "<b>working</b> on it" {
		t.Errorf("text = %v", got)
	}
	// A non-forum chat must not carry a topic id.
	if _, ok := calls[0]["message_thread_id"]; ok {
		t.Error("message_thread_id must be omitted outside forum topics")
	}
}

func TestTelegramResponderTargetsTheForumTopic(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("sendMessage", `{"ok":true,"result":{"message_id":1}}`)

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "55"})
	if err := responder.Start(context.Background(), "hi"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	calls := stub.callsTo("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("expected 1 sendMessage, got %d", len(calls))
	}
	if got := calls[0]["message_thread_id"]; got != float64(55) {
		t.Errorf("message_thread_id = %v, want 55", got)
	}
}

// The bridge throttles progress edits but does not compare bodies, so a stable
// banner legitimately re-sends identical text. Treating Telegram's refusal as
// an error would abort a healthy turn.
func TestTelegramEditSwallowsNotModified(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("sendMessage", `{"ok":true,"result":{"message_id":314}}`)
	stub.reply("editMessageText", `{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`)

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "-100"})
	if err := responder.Start(context.Background(), "thinking"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := responder.Update(context.Background(), "thinking"); err != nil {
		t.Errorf("an unchanged edit must not fail the turn: %v", err)
	}
}

func TestTelegramEditSurfacesRealFailures(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("sendMessage", `{"ok":true,"result":{"message_id":314}}`)
	stub.reply("editMessageText", `{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`)

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "-100"})
	if err := responder.Start(context.Background(), "thinking"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := responder.Update(context.Background(), "still thinking"); err == nil {
		t.Error("a genuine edit failure must propagate")
	}
}

// A reply past the chunk budget rewrites the progress message with the first
// chunk and posts the rest, so nothing is silently dropped.
func TestTelegramCompleteChunksLongReplies(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("sendMessage", `{"ok":true,"result":{"message_id":314}}`)
	stub.reply("editMessageText", `{"ok":true,"result":{}}`)

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "-100"})
	if err := responder.Start(context.Background(), "working"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	long := strings.Repeat("x", replyChunkLimit) + "\n" + strings.Repeat("y", 100)
	if err := responder.Complete(context.Background(), long); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if edits := stub.callsTo("editMessageText"); len(edits) != 1 {
		t.Errorf("expected the progress message to be rewritten once, got %d edits", len(edits))
	}
	// One sendMessage opened the progress message, one carried the overflow.
	if posts := stub.callsTo("sendMessage"); len(posts) != 2 {
		t.Errorf("expected 2 sendMessage calls, got %d", len(posts))
	}
}

func TestTelegramRetriesOnceAfterRateLimit(t *testing.T) {
	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)

	var attempts int
	stub.on("sendMessage", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		attempts++
		if attempts == 1 {
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":9}}`)
	})

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "-100"})
	if err := responder.Start(context.Background(), "hi"); err != nil {
		t.Fatalf("a throttled send must be retried, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestTelegramTestConnectionReportsWebhookConflict(t *testing.T) {
	stub := newTelegramStub(t)
	stub.reply("getMe", `{"ok":true,"result":{"id":42,"is_bot":true,"username":"daymugbot","can_join_groups":true,"can_read_all_group_messages":false}}`)
	stub.reply("getWebhookInfo", `{"ok":true,"result":{"url":"https://example.test/hook"}}`)

	result := testTelegramConnection(context.Background(), BotConfig{Platform: PlatformTelegram, BotToken: "t"}, stub.URL)

	if result.Connected {
		t.Error("a registered webhook makes long polling impossible; the test must fail")
	}
	if !strings.Contains(result.Error, "webhook is registered") {
		t.Errorf("error = %q", result.Error)
	}
	granted := map[string]bool{}
	for _, permission := range result.Permissions {
		granted[permission.Key] = permission.Granted
	}
	if !granted["bot_token"] {
		t.Error("the token itself validated and should be reported as granted")
	}
	if granted["polling_available"] {
		t.Error("polling must not be reported as available")
	}
	if !granted["can_join_groups"] {
		t.Error("can_join_groups should mirror getMe")
	}
	if granted["can_read_all_group_messages"] {
		t.Error("privacy mode is on; can_read_all_group_messages must be false")
	}
}

func TestTelegramTestConnectionSucceeds(t *testing.T) {
	stub := newTelegramStub(t)
	stub.reply("getMe", `{"ok":true,"result":{"id":42,"is_bot":true,"username":"daymugbot","can_join_groups":true,"can_read_all_group_messages":true}}`)
	stub.reply("getWebhookInfo", `{"ok":true,"result":{"url":""}}`)

	result := testTelegramConnection(context.Background(), BotConfig{Platform: PlatformTelegram, BotToken: "t"}, stub.URL)

	if !result.Connected {
		t.Fatalf("expected a healthy connection, got %q", result.Error)
	}
	if !result.AllRequiredPermissionsGranted {
		t.Error("every required permission was satisfied")
	}
	if result.Identity != "@daymugbot" {
		t.Errorf("identity = %q", result.Identity)
	}
}

func TestTelegramPermanentErrorClassification(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{http.StatusUnauthorized, true},
		{http.StatusNotFound, true},
		{http.StatusConflict, true},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, false},
	}
	for _, tt := range tests {
		err := &telegramError{Code: tt.code}
		if got := err.Permanent(); got != tt.want {
			t.Errorf("Permanent(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestTelegramValidateBotConfig(t *testing.T) {
	t.Run("requires a bot token when enabled", func(t *testing.T) {
		_, err := ValidateBotConfig(BotConfig{ID: "b1", Platform: PlatformTelegram, Enabled: true})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("clears credentials belonging to other platforms", func(t *testing.T) {
		got, err := ValidateBotConfig(BotConfig{
			ID: "b1", Platform: PlatformTelegram, Enabled: true,
			BotToken: "123:abc", AppToken: "xapp-1", AppID: "cli_x", AppSecret: "s",
		})
		if err != nil {
			t.Fatalf("ValidateBotConfig: %v", err)
		}
		if got.BotToken != "123:abc" {
			t.Errorf("bot token = %q", got.BotToken)
		}
		if got.AppToken != "" || got.AppID != "" || got.AppSecret != "" {
			t.Errorf("credentials from other platforms must be cleared, got %+v", got)
		}
	})
}

// The registry is what the manager and the bridge branch on; a platform missing
// from it cannot be configured at all.
func TestTelegramIsRegistered(t *testing.T) {
	caps, ok := CapabilitiesFor(PlatformTelegram)
	if !ok {
		t.Fatal("telegram must be present in the platform registry")
	}
	if caps.DisplayName != "Telegram" {
		t.Errorf("display name = %q", caps.DisplayName)
	}
	// Telegram never delivers another bot's messages, so the bridge has to hand
	// a handoff to the target connector itself.
	if caps.DeliversBotMessagesToBots {
		t.Error("Telegram bots cannot observe other bots' messages")
	}
	if !slices.Contains(SupportedPlatforms(), PlatformTelegram) {
		t.Error("SupportedPlatforms must list telegram")
	}
}

func TestTelegramDownloadRejectsEmptyPath(t *testing.T) {
	stub := newTelegramStub(t)
	client := &telegramClient{token: "t", baseURL: stub.URL, http: stub.Client()}
	err := client.download(context.Background(), "  ", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "empty file path") {
		t.Errorf("err = %v", err)
	}
}

// Telegram expires the typing indicator after ~5s and offers no way to refresh
// it other than re-sending. Riding the agent stream is not enough: one long
// tool call emits nothing for a minute and the indicator goes dark exactly when
// the reader most needs it.
func TestTelegramResponderKeepsTypingAliveBetweenAgentEvents(t *testing.T) {
	if telegramTypingHeartbeat >= 5*time.Second {
		t.Fatalf("heartbeat %s does not beat Telegram's ~5s expiry", telegramTypingHeartbeat)
	}

	stub := newTelegramStub(t)
	connector, _ := newTestTelegramConnector(t, stub)
	stub.reply("sendMessage", `{"ok":true,"result":{"message_id":314}}`)

	responder := connector.Responder(Message{ChannelID: "-100", ThreadID: "-100"})
	progress, ok := responder.(*telegramResponder)
	if !ok {
		t.Fatalf("responder type = %T", responder)
	}
	// Drive the heartbeat far faster than the shipped interval so the test does
	// not wait on wall-clock seconds; the shipped value is asserted above.
	progress.activityInterval = time.Millisecond
	// Count beats where the count is ordered against the heartbeat goroutine's
	// lifetime. The stub records a call server-side when the request arrives, so
	// a beat that Close cancels mid-flight can land in stub.calls *after* Close
	// has returned — counting there would make "stopped" a race. Every increment
	// here happens on the goroutine endActivity joins, so once Close returns the
	// count is frozen by construction.
	var beats atomic.Int64
	refresh := progress.refreshActivity
	progress.refreshActivity = func(ctx context.Context) {
		beats.Add(1)
		refresh(ctx)
	}

	ctx := context.Background()
	if err := responder.Start(ctx, "working"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// One sendChatAction comes from Start itself; anything beyond that can only
	// come from the heartbeat, since no further progress write happens here.
	deadline := time.Now().Add(2 * time.Second)
	for len(stub.callsTo("sendChatAction")) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("typing lapsed: only %d sendChatAction calls", len(stub.callsTo("sendChatAction")))
		}
		time.Sleep(2 * time.Millisecond)
	}

	CloseResponder(ctx, responder)
	settled := beats.Load()
	time.Sleep(20 * time.Millisecond)
	if got := beats.Load(); got != settled {
		t.Errorf("heartbeat still running after the turn ended: %d -> %d", settled, got)
	}
}
