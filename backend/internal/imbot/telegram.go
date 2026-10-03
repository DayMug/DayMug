package imbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// telegramPollBackoff paces retries of a failed getUpdates round trip.
	// Recoverable failures here are network blips; the connector supervisor
	// owns real reconnection, so this only avoids a hot loop in between.
	telegramPollBackoff = 3 * time.Second
	// telegramPollFailureBudget is how many consecutive recoverable getUpdates
	// failures are absorbed before the connection is declared down. Handing it
	// back to the supervisor rebuilds the client and applies the real backoff.
	telegramPollFailureBudget = 5
)

// TelegramConnector drives one Telegram bot over Bot API long polling
// (getUpdates), which needs no public endpoint — the same property that makes
// Slack Socket Mode and 飞书长连接 deployable behind NAT.
//
// The bot must have group privacy mode disabled (via @BotFather) to see
// un-addressed group messages; with it left on, Telegram only delivers
// commands, @mentions of the bot, and replies to the bot's own messages —
// which still covers every message DayMug would act on under the default
// require_mention rule.
type TelegramConnector struct {
	baseConnector

	cfg         TelegramConfig
	api         *telegramClient
	botUserID   int64
	botUsername string

	// offset is the getUpdates acknowledgement cursor: passing it confirms
	// every lower update id, so Telegram stops redelivering them.
	offset int64

	stopMu sync.Mutex
	// stopRun cancels the goroutine this attempt's Start opened. It is a child
	// of the caller's ctx because the supervisor reuses one ctx across attempts
	// — without a per-attempt cancel an abandoned poll would keep consuming
	// updates that the live generation needs.
	stopRun context.CancelFunc
	loops   sync.WaitGroup
}

func NewTelegramConnector(cfg TelegramConfig) *TelegramConnector {
	return &TelegramConnector{
		baseConnector: baseConnector{nameCache: map[string]string{}},
		cfg:           cfg,
	}
}

func (t *TelegramConnector) Platform() string { return PlatformTelegram }

func (t *TelegramConnector) Capabilities() Capabilities { return platforms[PlatformTelegram].caps }

// MentionTag returns the bot's @username. Unlike Slack's escaped `<@U…>`, this
// is ordinary text that Telegram resolves at render time, so it needs no
// escape opt-out — TelegramHTML leaves it untouched.
func (t *TelegramConnector) MentionTag() string {
	if t.botUsername == "" {
		return ""
	}
	return "@" + t.botUsername
}

// Start validates the token with getMe (also learning the bot's own id and
// username for self/mention detection), discards whatever backlog Telegram is
// holding, then polls for updates on a background goroutine until Stop.
func (t *TelegramConnector) Start(ctx context.Context) error {
	// Lazy so tests can point a pre-built client at an httptest server. The
	// supervisor builds a fresh connector per attempt, so this is nil on every
	// production Start.
	if t.api == nil {
		if strings.TrimSpace(t.cfg.BotToken) == "" {
			return errors.New("telegram: bot_token is required")
		}
		t.api = newTelegramClient(t.cfg.BotToken)
	}

	authCtx, cancelAuth := context.WithTimeout(ctx, telegramCallTimeout)
	defer cancelAuth()
	var me telegramUser
	if err := t.api.call(authCtx, "getMe", map[string]any{}, &me); err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	if !me.IsBot || me.ID == 0 {
		return errors.New("telegram getMe: configured bot_token is not a bot identity; refusing to send as a user")
	}
	t.botUserID = me.ID
	t.botUsername = me.Username

	if err := t.discardBacklog(ctx); err != nil {
		return fmt.Errorf("telegram getUpdates: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	t.stopMu.Lock()
	t.stopRun = cancel
	t.stopMu.Unlock()

	t.loops.Add(1)
	go func() {
		defer t.loops.Done()
		t.pollLoop(runCtx)
	}()
	return nil
}

// Stop ends this attempt's poll loop, waits for it, and detaches the inbound
// sink. Cancelling runCtx also aborts the in-flight long poll, so teardown
// does not wait out the remaining server-side window.
func (t *TelegramConnector) Stop() {
	t.stopMu.Lock()
	cancel := t.stopRun
	t.stopRun = nil
	t.stopMu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.loops.Wait()
	t.SetOnMessage(nil)
}

// discardBacklog advances the cursor past everything queued before this
// connection. Telegram retains undelivered updates for 24h and would otherwise
// replay them on every (re)connect — answering a day-old message after a
// restart, repeatedly. Slack and 飞书 deliver nothing from before the socket
// opened, and matching that is what keeps the bridge's semantics uniform.
func (t *TelegramConnector) discardBacklog(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, telegramCallTimeout)
	defer cancel()
	var updates []telegramUpdate
	// offset -1 asks for the most recent pending update only; acknowledging it
	// confirms the whole queue behind it in one round trip.
	err := t.api.call(callCtx, "getUpdates", map[string]any{"offset": -1, "limit": 1, "timeout": 0}, &updates)
	if err != nil {
		return err
	}
	t.advanceOffset(updates)
	return nil
}

func (t *TelegramConnector) advanceOffset(updates []telegramUpdate) {
	for _, update := range updates {
		if update.UpdateID >= t.offset {
			t.offset = update.UpdateID + 1
		}
	}
}

func (t *TelegramConnector) pollLoop(ctx context.Context) {
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := t.fetchUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var apiErr *telegramError
			if errors.As(err, &apiErr) && apiErr.Permanent() {
				log.Printf("imbot: telegram polling stopped: %v", err)
				t.reportDown(err)
				return
			}
			failures++
			if failures >= telegramPollFailureBudget {
				log.Printf("imbot: telegram polling gave up after %d failures: %v", failures, err)
				t.reportDown(err)
				return
			}
			log.Printf("imbot: telegram getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(telegramPollBackoff):
			}
			continue
		}
		failures = 0
		for _, update := range updates {
			if update.Message != nil {
				t.handleMessage(ctx, update.Message)
			}
		}
	}
}

func (t *TelegramConnector) fetchUpdates(ctx context.Context) ([]telegramUpdate, error) {
	// The deadline covers the server-side window plus transport slack; without
	// it a half-open connection would park the loop forever.
	callCtx, cancel := context.WithTimeout(ctx, telegramPollTimeout+telegramCallTimeout)
	defer cancel()

	payload := map[string]any{
		"timeout":         int(telegramPollTimeout / time.Second),
		"limit":           telegramPollLimit,
		"allowed_updates": []string{"message"},
	}
	if t.offset > 0 {
		payload["offset"] = t.offset
	}
	var updates []telegramUpdate
	if err := t.api.call(callCtx, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	t.advanceOffset(updates)
	return updates, nil
}

func (t *TelegramConnector) handleMessage(ctx context.Context, m *telegramMessage) {
	if m.Chat == nil || m.From == nil || m.From.ID == t.botUserID {
		return
	}
	if !t.hasSink() {
		return
	}
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	isDM := m.Chat.Type == "private"

	msg := Message{
		Platform:    PlatformTelegram,
		ChannelID:   chatID,
		ChannelName: firstNonEmpty(m.Chat.Title, m.Chat.Username),
		ThreadID:    t.threadID(m, chatID),
		MessageID:   chatID + "|" + strconv.FormatInt(m.MessageID, 10),
		SenderID:    strconv.FormatInt(m.From.ID, 10),
		SenderName:  m.From.displayName(),
		Text:        t.stripOwnMention(m.body()),
		Attachments: t.attachments(m),
		Mentioned:   t.mentioned(m),
		IsDM:        isDM,
		FromBot:     m.From.IsBot,
	}
	// LoadThreadMessages stays nil: the Bot API exposes no history read, so a
	// backfill would be a fabrication. Threads therefore start from the
	// triggering message, and the bridge's stored transcript carries the rest.
	if ctx.Err() != nil || (strings.TrimSpace(msg.Text) == "" && len(msg.Attachments) == 0) {
		return
	}
	t.deliver(msg)
}

// threadID maps Telegram's chat shapes onto the bridge's one-thread-one-session
// model. A forum topic is a real thread and gets its own session; every other
// chat (DM, plain group, channel comment thread) has no threading primitive, so
// the chat itself is the session — which is also how people read a Telegram
// conversation. Reply chains deliberately do not fork a session: only the
// immediate parent is knowable from a payload, so following them would produce
// a different session depending on which message a user happened to swipe.
func (t *TelegramConnector) threadID(m *telegramMessage, chatID string) string {
	if m.IsTopicMessage && m.MessageThreadID != 0 {
		return strconv.FormatInt(m.MessageThreadID, 10)
	}
	return chatID
}

// mentioned reports whether the message addresses this bot: an @username
// mention, a text_mention entity carrying the bot's id (how Telegram encodes a
// mention of an account with no public username), or a reply to one of the
// bot's own messages — which is the platform's idiomatic way to continue a
// conversation and reads as addressing it.
func (t *TelegramConnector) mentioned(m *telegramMessage) bool {
	if t.botUsername != "" && strings.Contains(m.body(), "@"+t.botUsername) {
		return true
	}
	for _, entity := range m.entities() {
		if entity.Type == "text_mention" && entity.User != nil && entity.User.ID == t.botUserID {
			return true
		}
	}
	return m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == t.botUserID
}

// stripOwnMention removes the bot's own @handle from the prompt: it addresses
// DayMug rather than saying anything, and leaving it in makes the agent think
// a third party was named. Other users' mentions survive verbatim — they
// already read as "@name", so unlike Slack's `<@U0123>` there is nothing to
// rewrite for the agent's benefit.
func (t *TelegramConnector) stripOwnMention(text string) string {
	if t.botUsername == "" {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(strings.ReplaceAll(text, "@"+t.botUsername, ""))
}

// attachments normalizes every file shape a Telegram message can carry. Photos
// arrive as a ladder of pre-scaled sizes; the largest is the closest thing to
// the original the Bot API will hand out.
func (t *TelegramConnector) attachments(m *telegramMessage) []Attachment {
	var out []Attachment
	add := func(file *telegramFile, fallbackName string) {
		if file == nil || file.FileID == "" {
			return
		}
		out = append(out, t.newAttachment(file.FileID, firstNonEmpty(file.FileName, fallbackName), file.MimeType, file.FileSize))
	}
	add(m.Document, "document")
	add(m.Video, "video.mp4")
	add(m.Audio, "audio")
	add(m.Voice, "voice.ogg")

	if largest := largestPhoto(m.Photo); largest != nil {
		out = append(out, t.newAttachment(largest.FileID, "photo.jpg", "image/jpeg", largest.FileSize))
	}
	return out
}

func largestPhoto(sizes []telegramPhotoSize) *telegramPhotoSize {
	var largest *telegramPhotoSize
	for i := range sizes {
		if sizes[i].FileID == "" {
			continue
		}
		if largest == nil || sizes[i].Width > largest.Width {
			largest = &sizes[i]
		}
	}
	return largest
}

// newAttachment defers the getFile lookup to download time. File paths expire
// after about an hour, so resolving one eagerly for a message that sits in the
// queue behind a long agent run would hand the bridge a dead URL.
func (t *TelegramConnector) newAttachment(fileID, name, mime string, size int64) Attachment {
	attachment := Attachment{ID: fileID, Name: name, MIME: mime, Size: size}
	attachment.Download = func(ctx context.Context, dst io.Writer) error {
		var resolved struct {
			FilePath string `json:"file_path"`
		}
		if err := t.api.call(ctx, "getFile", map[string]any{"file_id": fileID}, &resolved); err != nil {
			return fmt.Errorf("telegram getFile %q: %w", name, err)
		}
		return t.api.download(ctx, resolved.FilePath, dst)
	}
	return attachment
}
