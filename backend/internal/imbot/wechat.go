package imbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot/wechat"
)

const (
	// wechatPollBackoff paces retries of a failed getUpdates round trip.
	// Real reconnection belongs to the connector supervisor; this only stops
	// a hot loop in between.
	wechatPollBackoff = 3 * time.Second
	// wechatPollFailureBudget is how many consecutive recoverable failures are
	// absorbed before the connection is declared down.
	wechatPollFailureBudget = 5
)

// WeChatConnector drives one paired Weixin bot over the iLink Bot long poll.
//
// Pairing is out of band: a QR scan trades a phone confirmation for a bot
// token plus the account's own gateway, both stored on the bot config before
// this connector is ever constructed. Start therefore only resumes an existing
// pairing — it never blocks waiting for a human to scan, which would park the
// supervisor for minutes on every reconnect.
type WeChatConnector struct {
	baseConnector

	cfg    WeChatConfig
	client *wechat.Client
	typing *wechat.TypingTickets
	// threads assigns each peer a thread id and rotates it on /new. Weixin has
	// no threading primitive, so this is the only thing separating one topic
	// from the next.
	threads *wechat.Threads

	// contextTokens holds the most recent inbound context_token per peer.
	// Every outbound message must echo one or it is accepted and never
	// delivered, and Responder only receives the normalized Message — which
	// has no field for a platform-specific reply credential.
	tokensMu      sync.Mutex
	contextTokens map[string]string

	stopMu sync.Mutex
	// stopRun cancels the goroutine this attempt's Start opened. The
	// supervisor reuses one ctx across attempts, so without a per-attempt
	// cancel an abandoned poll would keep draining updates the live
	// generation needs.
	stopRun context.CancelFunc
	loops   sync.WaitGroup
}

func NewWeChatConnector(cfg WeChatConfig) *WeChatConnector {
	return &WeChatConnector{
		baseConnector: baseConnector{nameCache: map[string]string{}},
		cfg:           cfg,
		contextTokens: map[string]string{},
		threads:       wechat.NewThreads(wechat.ThreadsOptions{Logf: log.Printf}),
	}
}

func (w *WeChatConnector) Platform() string { return PlatformWeChat }

func (w *WeChatConnector) Capabilities() Capabilities { return platforms[PlatformWeChat].caps }

// MentionTag returns "": Weixin has no addressable bot handle, so a handoff
// mention would be plain text nobody resolves.
func (w *WeChatConnector) MentionTag() string { return "" }

// Start resumes a paired account and begins polling. It validates the
// credentials with a cheap authenticated call so a revoked pairing fails here
// — visibly, as a connection error — rather than silently never delivering.
func (w *WeChatConnector) Start(ctx context.Context) error {
	// Lazy so tests can point a pre-built client at an httptest server. The
	// supervisor builds a fresh connector per attempt, so this is nil on
	// every production Start.
	if w.client == nil {
		token := strings.TrimSpace(w.cfg.BotToken)
		if token == "" {
			return errors.New("wechat: this bot is not paired yet; scan the QR code first")
		}
		w.client = wechat.NewClient(wechat.ClientOptions{
			BaseURL:  w.cfg.BaseURL,
			Token:    token,
			BotAgent: wechatBotAgent,
		})
	}
	if w.typing == nil {
		w.typing = wechat.NewTypingTickets(w.client)
	}

	// notifyStart doubles as the credential check: it is authenticated, cheap,
	// and the gateway wants it anyway.
	if err := w.client.NotifyStart(ctx); err != nil {
		return fmt.Errorf("wechat notifyStart: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	w.stopMu.Lock()
	w.stopRun = cancel
	w.stopMu.Unlock()

	w.loops.Go(func() { w.pollLoop(runCtx) })
	return nil
}

// Stop ends this attempt's poll loop, waits for it, and detaches the inbound
// sink. Cancelling runCtx also aborts the in-flight long poll, so teardown
// does not wait out the remaining server-side window.
func (w *WeChatConnector) Stop() {
	w.stopMu.Lock()
	cancel := w.stopRun
	w.stopRun = nil
	w.stopMu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.loops.Wait()

	if w.client != nil {
		// Detached from the cancelled run context, which would otherwise abort
		// the notification before it leaves the process.
		shutdown, done := context.WithTimeout(context.WithoutCancel(context.Background()), wechatShutdownTimeout)
		if err := w.client.NotifyStop(shutdown); err != nil {
			log.Printf("imbot: wechat notifyStop: %v", err)
		}
		done()
	}
	w.SetOnMessage(nil)
}

const wechatShutdownTimeout = 5 * time.Second

// wechatBotAgent identifies DayMug in the gateway's logs, the protocol's
// equivalent of a User-Agent.
const wechatBotAgent = "DayMug"

// wechatNewThreadAck confirms that /new drew a conversation boundary. Weixin
// gives the user no way to see it otherwise: there is no thread UI, so a reset
// and a silently-continued conversation look identical from the phone.
const wechatNewThreadAck = "🆕 已开始新会话，之前的对话不再作为上下文。"

func (w *WeChatConnector) pollLoop(ctx context.Context) {
	poller := &wechat.Poller{
		Client: w.client,
		// The cursor lives for this connection only. Weixin replays from the
		// stored cursor, and a persisted one would make a restart answer
		// messages from before the process came up — Slack and 飞书 deliver
		// nothing from before their socket opened, and the bridge's semantics
		// depend on that being uniform.
		Cursor: &wechat.MemoryCursor{},
		Logf:   log.Printf,
		Handle: func(ctx context.Context, msg *wechat.Message) {
			w.handleMessage(ctx, msg)
		},
		RetryDelay:             wechatPollBackoff,
		BackoffDelay:           wechatPollBackoff,
		MaxConsecutiveFailures: wechatPollFailureBudget,
	}

	err := poller.Run(ctx)
	if ctx.Err() != nil {
		return
	}
	if err == nil {
		// Run only returns nil on cancellation, so reaching here without a
		// cancelled context means the loop gave up for a reason it already
		// logged. Treat it as down so the supervisor rebuilds.
		err = errors.New("wechat: poll loop ended unexpectedly")
	}
	log.Printf("imbot: wechat polling stopped: %v", err)
	w.reportDown(err)
}

// handleMessage normalizes one inbound protocol message and hands it to the
// bridge.
func (w *WeChatConnector) handleMessage(ctx context.Context, m *wechat.Message) {
	// The long-poll stream echoes this bot's own sends back.
	if !m.FromUser() {
		return
	}
	if !w.hasSink() {
		return
	}
	peer := m.PeerID()
	if peer == "" {
		return
	}

	// Record before routing: a reply needs this token even when the message
	// turns out to be a bare /new that produces no prompt.
	w.rememberContextToken(peer, m.ContextToken)

	routed := w.threads.Route(peer, m.PlainText())
	if routed.Reset {
		log.Printf("imbot: wechat started a new conversation for %s (thread %s)", peer, routed.ThreadID)
	}

	ack := ""
	if routed.Reset {
		// Weixin has no thread UI, so rotating the thread id is invisible: the
		// user cannot tell a fresh conversation from one that silently kept the
		// old context, and a bare /new used to produce no reply whatsoever.
		ack = wechatNewThreadAck
	}

	msg := Message{
		Platform:    PlatformWeChat,
		ChannelID:   peer,
		ThreadID:    routed.ThreadID,
		MessageID:   wechatMessageID(m, peer),
		SenderID:    m.FromUserID,
		Text:        routed.Prompt,
		Ack:         ack,
		Attachments: w.attachments(m),
		// Weixin has no bot identities in the message stream and no history
		// read, so FromBot is always false and LoadThreadMessages stays nil —
		// promising backfill would be a fabrication.
		IsDM: m.GroupID == "",
		// Every message in a private chat addresses the bot; group support is
		// unverified, so treating a group message as addressed would be a
		// guess. Channel rules still gate it.
		Mentioned: m.GroupID == "",
	}

	// An empty message still reaches the bridge when it carries an ack: a bare
	// /new has nothing to answer, but the user is owed the confirmation that
	// their reset landed. Dropping it here is what made /new silent.
	if ctx.Err() != nil || (msg.Ack == "" && strings.TrimSpace(msg.Text) == "" && len(msg.Attachments) == 0) {
		return
	}
	w.deliver(msg)
}

// attachments turns every media item into a lazily-downloaded attachment.
//
// The download closure keeps the bot token and the AES key inside the
// connector: the bridge, the database and the browser only ever see the
// decrypted bytes it writes.
func (w *WeChatConnector) attachments(m *wechat.Message) []Attachment {
	var out []Attachment
	for i := range m.ItemList {
		item := &m.ItemList[i]
		ref, name, ok := wechat.MediaOf(item)
		if !ok {
			continue
		}
		out = append(out, Attachment{
			ID:   fmt.Sprintf("%s#%d", m.ContextToken, i),
			Name: name,
			Download: func(ctx context.Context, dst io.Writer) error {
				return w.client.DownloadMedia(ctx, ref, dst, wechatMaxMediaBytes)
			},
		})
	}
	return out
}

// wechatMaxMediaBytes matches the bridge's own per-file inbound cap, so an
// attachment it would reject anyway is never pulled off the CDN.
const wechatMaxMediaBytes int64 = 25 << 20

// wechatMessageID builds the bridge's dedup key. The gateway's own message id
// is absent on some item shapes and only unique per conversation, so the peer
// is folded in and the send timestamp is the fallback discriminator.
func wechatMessageID(m *wechat.Message, peer string) string {
	switch {
	case m.MessageID != 0:
		return fmt.Sprintf("%s|%d", peer, m.MessageID)
	case m.Seq != 0:
		return fmt.Sprintf("%s|seq-%d", peer, m.Seq)
	default:
		return fmt.Sprintf("%s|t-%d", peer, m.CreateTimeMS)
	}
}

func (w *WeChatConnector) rememberContextToken(peer, token string) {
	if token == "" {
		return
	}
	w.tokensMu.Lock()
	w.contextTokens[peer] = token
	w.tokensMu.Unlock()
}

func (w *WeChatConnector) contextTokenFor(peer string) string {
	w.tokensMu.Lock()
	defer w.tokensMu.Unlock()
	return w.contextTokens[peer]
}
