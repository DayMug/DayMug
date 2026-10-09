// Package imbot connects DayMug to IM platforms (Slack, 飞书/Lark) over
// outbound long-lived connections — Slack Socket Mode and 飞书长连接 — so no
// public webhook endpoint is required. Connectors normalize platform events
// into Message values; a Handler (the bridge in the handler package) decides
// whether and how to answer, and replies are always posted back into the
// originating thread / 话题.
package imbot

import (
	"context"
	"errors"
	"io"
	"log"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	PlatformSlack    = "slack"
	PlatformFeishu   = "feishu"
	PlatformTelegram = "telegram"
	PlatformWeChat   = "wechat"
)

// Message is one normalized inbound IM message.
type Message struct {
	Platform string
	// AgentID and BotID are set by Manager for agent-owned connections. They are not
	// derived from untrusted platform payloads.
	AgentID     string
	BotID       string
	ChannelID   string
	ChannelName string
	// ThreadID identifies the thread the message belongs to: Slack thread_ts
	// (falling back to the message ts, which becomes the root of the thread
	// our reply opens), 飞书 thread/root id (falling back to the message id).
	ThreadID string
	// MessageID is the platform id of this concrete message — the dedup key
	// and, on 飞书, the anchor the reply API threads under.
	MessageID  string
	SenderID   string
	SenderName string
	Text       string
	// Attachments contains platform-hosted files that still need to be
	// materialized into the serving agent's work directory. Download keeps
	// platform credentials inside the connector instead of leaking temporary
	// private URLs or tokens into the bridge, database, or browser.
	Attachments []Attachment
	// Mentioned is true when the bot was explicitly @-mentioned.
	Mentioned bool
	// IsDM is true for direct/private chats with the bot.
	IsDM bool
	// FromBot is true when the sender is another bot (never this bot's own
	// echo — connectors drop those before emitting). The bridge only acts on
	// such messages in channels that opted into bot-to-bot relays.
	FromBot bool
	// Ack is a confirmation the bridge posts before deciding whether to run the
	// agent. Connectors set it for a control instruction they already carried
	// out themselves — Weixin's /new, which rotates the thread inside the
	// connector — so the user learns the instruction landed.
	//
	// It travels on the message rather than being posted by the connector so
	// the confirmation passes the same channel-rule gating as any other reply.
	// Sending it straight from the connector would tell a sender the operator
	// scoped out that the bot is alive and listening to them.
	//
	// A message whose Text and Attachments are both empty is acknowledged and
	// nothing more: a bare /new has no question in it to answer.
	Ack string
	// LoadThreadMessages lazily reads platform-thread messages after the given
	// concrete platform message id and before this message. An empty id loads
	// all available preceding context. Connectors set it only when their
	// platform supports thread-history backfill.
	LoadThreadMessages func(ctx context.Context, afterMessageID string) ([]Message, error)
}

// Attachment is one normalized inbound IM file. Download writes the source
// bytes to dst using the connector's authenticated API client. The bridge
// supplies a size-limited writer and persists the result under the agent's
// .daymug/uploads directory before invoking the CLI.
type Attachment struct {
	ID       string
	Name     string
	MIME     string
	Size     int64
	Download func(ctx context.Context, dst io.Writer) error
}

// OutboundAttachment is one validated agent-generated artifact. Open returns
// a fresh reader for each platform upload attempt; the connector remains the
// only component that can use Slack/Feishu credentials.
type OutboundAttachment struct {
	Name string
	MIME string
	Size int64
	Open func() (io.ReadCloser, error)
}

// Responder owns the single progress message shown inside an IM thread. Start
// creates it, Update edits it in place as the agent streams, and Complete
// replaces it with the final response (posting overflow chunks when needed).
// Post appends one standalone message to the thread without touching the
// progress message — used for bot-to-bot handoff mentions, which must be
// fresh messages because platforms do not reliably re-deliver edits.
type Responder interface {
	Start(ctx context.Context, text string) error
	Update(ctx context.Context, text string) error
	Complete(ctx context.Context, text string) error
	Post(ctx context.Context, text string) error
}

// ArtifactResponder is an optional connector capability. Keeping it separate
// from Responder avoids breaking existing transports and test doubles that
// only implement text progress messages.
type ArtifactResponder interface {
	PostAttachments(ctx context.Context, attachments []OutboundAttachment) error
}

// uploadEach is the PostAttachments loop every connector shares: one upload
// per attachment, in order, stopping at the first failure. Callers that must
// not lose the files behind a failed one post them one at a time.
func uploadEach(ctx context.Context, msg Message, attachments []OutboundAttachment, upload func(context.Context, Message, OutboundAttachment) error) error {
	for _, attachment := range attachments {
		if err := upload(ctx, msg, attachment); err != nil {
			return err
		}
	}
	return nil
}

// HandoffResponder is an optional connector capability: a specialized message
// type for native mentions that returns the new platform message id. Kept
// separate from Responder so existing transports and test doubles that only
// implement text progress messages keep working.
type HandoffResponder interface {
	PostHandoff(ctx context.Context, text string) (string, error)
}

// NoticeResponder is an optional connector capability: deliver text that must
// reach the reader even on a platform where Update is a no-op. Optional rather
// than part of Responder so the test doubles that only implement the four core
// methods keep compiling; Notify falls back to Update for them, which is what
// they modelled before this existed.
type NoticeResponder interface {
	Notice(ctx context.Context, text string) error
}

// Notify posts a standalone notice — a failure, a cancellation, a queue
// position — through the responder's best available primitive.
func Notify(ctx context.Context, responder Responder, text string) error {
	if notifier, ok := responder.(NoticeResponder); ok {
		return notifier.Notice(ctx, text)
	}
	return responder.Update(ctx, text)
}

// TurnCloser is an optional connector capability: release whatever the
// responder holds for the duration of one turn. A Responder that starts a
// background activity heartbeat needs an end-of-turn signal that fires even
// when the run failed before Complete, which is the path that used to leave a
// Weixin thread showing "typing" indefinitely.
type TurnCloser interface {
	Close(ctx context.Context)
}

// CloseResponder ends the turn for responders that hold per-turn resources,
// and does nothing for the ones that do not.
func CloseResponder(ctx context.Context, responder Responder) {
	if closer, ok := responder.(TurnCloser); ok {
		closer.Close(ctx)
	}
}

// Handler consumes one deduplicated inbound message. It owns the full
// decision: channel-rule gating, running the agent, and posting replies (or
// staying silent) through reply. Called on its own goroutine per message.
type Handler interface {
	HandleMessage(ctx context.Context, msg Message, responder Responder)
}

// Capabilities describes what one IM platform can do, so callers never
// branch on a platform id. Mirrors agent.Backend.Capabilities().
type Capabilities struct {
	// DisplayName names the platform in user-facing strings such as the
	// conversation title prefix.
	DisplayName string
	// PromptName names the platform inside English prompt scaffolding.
	PromptName string
	// SystemPromptName names the platform in the system prompt, where every
	// alias the agent might encounter is worth spelling out.
	SystemPromptName string
	// SupportsBotRelay reports whether bot-to-bot handoff works here.
	SupportsBotRelay bool
	// ResolvesNameMentions reports whether a reply's "@display name" is turned
	// into a native mention of that thread participant. Where it is false the
	// agent must not be told to write one — it would only ever be plain text.
	ResolvesNameMentions bool
	// DeliversBotMessagesToBots reports whether the platform's inbound event
	// stream includes messages authored by other bots. When false the bridge
	// must deliver a validated handoff to the target connector itself.
	DeliversBotMessagesToBots bool
}

// Connector is one platform connection. Start opens the long-lived connection
// and returns once it is established (or fails); delivery then happens on
// internal goroutines until Stop is called. Inbound messages are pushed through
// the sink set via SetOnMessage by the Manager before Start.
type Connector interface {
	Platform() string
	Capabilities() Capabilities
	Start(ctx context.Context) error
	// Stop releases everything Start opened and detaches the inbound sink.
	// Cancelling the ctx passed to Start is not enough: neither platform SDK
	// honours it, and the supervisor reuses one ctx across attempts anyway.
	// Must be safe to call after a failed Start and more than once.
	Stop()
	Responder(msg Message) Responder
	SetOnMessage(fn func(Message))
	// SetOnDown registers the callback invoked (at most once per Start) when
	// an established connection dies while its context is still live, so the
	// supervisor can mark the bot unhealthy and reconnect.
	SetOnDown(fn func(err error))
	// MentionTag returns the platform markup that @-mentions this bot in an
	// outbound message (Slack "<@U…>"), or "" when unknown or unsupported.
	MentionTag() string
}

// platformDescriptor is everything the rest of DayMug needs to know about one
// IM platform. Each field replaces what used to be a per-platform switch
// somewhere else, so a new platform is described here once instead of being
// threaded through validation, the admin API and the connection test.
// TestEveryRegisteredPlatformIsFullyDescribed fails on an entry that leaves one
// out.
type platformDescriptor struct {
	caps    Capabilities
	factory func(BotConfig) Connector
	// credentials is which BotConfig credential slots the platform uses.
	credentials credentialSpec
	// testConnection probes the platform with a bot's credentials for the
	// admin UI's connection check.
	testConnection func(context.Context, BotConfig) ConnectionTestResult
}

// platforms is the single source of truth for what DayMug supports. Adding a
// platform means one entry here, its permission requirements, and a Connector
// implementation — the Manager, the bridge, bot validation and the admin API
// stay platform-agnostic.
var platforms = map[string]platformDescriptor{
	PlatformSlack: {
		caps: Capabilities{
			DisplayName: "Slack", PromptName: "Slack", SystemPromptName: "Slack",
			SupportsBotRelay: true, DeliversBotMessagesToBots: true,
			ResolvesNameMentions: true,
		},
		factory: func(bot BotConfig) Connector {
			return NewSlackConnector(SlackConfig{Enabled: true, BotToken: bot.BotToken, AppToken: bot.AppToken})
		},
		credentials: credentialSpec{
			fields:  credentialBotToken | credentialAppToken,
			missing: "enabled Slack bots require bot_token and app_token",
		},
		testConnection: func(ctx context.Context, bot BotConfig) ConnectionTestResult {
			return testSlackConnection(ctx, bot, nil)
		},
	},
	PlatformFeishu: {
		caps: Capabilities{
			DisplayName: "飞书", PromptName: "Feishu", SystemPromptName: "飞书 (Feishu/Lark)",
			SupportsBotRelay: true, DeliversBotMessagesToBots: false,
			ResolvesNameMentions: true,
		},
		factory: func(bot BotConfig) Connector {
			return NewFeishuConnector(FeishuConfig{
				Enabled: true, BotName: bot.Name, AppID: bot.AppID, AppSecret: bot.AppSecret,
			})
		},
		credentials: credentialSpec{
			fields:  credentialAppID | credentialAppSecret,
			missing: "enabled Feishu bots require app_id and app_secret",
		},
		testConnection: func(ctx context.Context, bot BotConfig) ConnectionTestResult {
			return testFeishuConnection(ctx, bot, nil)
		},
	},
	PlatformTelegram: {
		caps: Capabilities{
			DisplayName: "Telegram", PromptName: "Telegram", SystemPromptName: "Telegram",
			// A Telegram bot never receives another bot's messages, so a
			// handoff mention posted into the chat would reach nobody; the
			// bridge delivers it to the target connector itself instead.
			SupportsBotRelay: true, DeliversBotMessagesToBots: false,
		},
		factory: func(bot BotConfig) Connector {
			return NewTelegramConnector(TelegramConfig{Enabled: true, BotToken: bot.BotToken})
		},
		credentials: credentialSpec{
			fields:  credentialBotToken,
			missing: "enabled Telegram bots require bot_token",
		},
		testConnection: func(ctx context.Context, bot BotConfig) ConnectionTestResult {
			return testTelegramConnection(ctx, bot, "")
		},
	},
	PlatformWeChat: {
		caps: Capabilities{
			DisplayName: "微信", PromptName: "WeChat", SystemPromptName: "微信 (WeChat/Weixin)",
			// The iLink stream carries no bot identities at all, so a relay
			// target could neither be mentioned nor observed here.
			SupportsBotRelay: false, DeliversBotMessagesToBots: false,
		},
		factory: func(bot BotConfig) Connector {
			// AppID carries the paired gateway rather than an app identifier:
			// the QR handshake returns a per-account base URL, and reusing the
			// spare credential slot keeps a new platform off the bots schema.
			return NewWeChatConnector(WeChatConfig{Enabled: true, BotToken: bot.BotToken, BaseURL: bot.AppID})
		},
		credentials: credentialSpec{
			fields: credentialBotToken | credentialAppID,
			// Both halves come from the QR pairing, never from a human typing
			// them, so the message points at pairing rather than at a form
			// field.
			missing: "enabled WeChat bots must be paired first: scan the QR code to obtain a bot token and gateway",
		},
		testConnection: func(ctx context.Context, bot BotConfig) ConnectionTestResult {
			return testWeChatConnection(ctx, bot, "")
		},
	},
}

// credentialField names one of BotConfig's four credential slots.
type credentialField uint8

const (
	credentialBotToken credentialField = 1 << iota
	credentialAppToken
	credentialAppID
	credentialAppSecret
)

// credentialSpec is the set of credential slots a platform uses. An enabled
// bot must fill all of them; the rest are cleared on save so a platform switch
// cannot leave another platform's secret behind.
type credentialSpec struct {
	fields credentialField
	// missing is the refusal shown when an enabled bot lacks one of them.
	missing string
}

type credentialSlot struct {
	field credentialField
	value *string
}

// credentialSlots pairs each credential field with where it lives on bot.
func (bot *BotConfig) credentialSlots() []credentialSlot {
	return []credentialSlot{
		{credentialBotToken, &bot.BotToken},
		{credentialAppToken, &bot.AppToken},
		{credentialAppID, &bot.AppID},
		{credentialAppSecret, &bot.AppSecret},
	}
}

func (c credentialSpec) complete(bot BotConfig) bool {
	for _, slot := range bot.credentialSlots() {
		if c.fields&slot.field != 0 && strings.TrimSpace(*slot.value) == "" {
			return false
		}
	}
	return true
}

func (c credentialSpec) strip(bot BotConfig) BotConfig {
	for _, slot := range bot.credentialSlots() {
		if c.fields&slot.field == 0 {
			*slot.value = ""
		}
	}
	return bot
}

// CredentialsComplete reports whether bot carries every credential its
// platform needs to connect, i.e. whether it could be enabled as-is. Always
// false for a platform DayMug does not implement.
func CredentialsComplete(platform string, bot BotConfig) bool {
	d, ok := platforms[platform]
	return ok && d.credentials.complete(bot)
}

// CapabilitiesFor returns the descriptor for a platform id. ok is false for
// platforms DayMug does not implement.
func CapabilitiesFor(platform string) (Capabilities, bool) {
	d, ok := platforms[platform]
	return d.caps, ok
}

// SupportedPlatforms lists the implemented platform ids in a stable order, so
// callers can build a validation message from the registry instead of a
// literal that silently goes stale the next time a platform is added.
func SupportedPlatforms() []string {
	out := make([]string, 0, len(platforms))
	for platform := range platforms {
		out = append(out, platform)
	}
	sort.Strings(out)
	return out
}

const (
	defaultRetryBaseDelay = 5 * time.Second
	defaultRetryMaxDelay  = 5 * time.Minute
	// defaultStableAfter is how long a connection must survive before the
	// backoff counts it as a real success and resets.
	//
	// A successful Start proves very little: it only validates REST credentials
	// (Slack auth.test with the bot token, 飞书 /bot/v3/info), while the long
	// connection needs a *different* credential and scope. Revoke only Slack's
	// app-level token and Start keeps returning nil while the socket dies within
	// milliseconds — resetting on Start turned that into ~720 reconnects and
	// ~720 auth.test calls per hour, forever, with the UI flashing red/green.
	// One minute is far longer than any handshake or auth rejection takes
	// (sub-second) and far shorter than a healthy connection's lifetime, so it
	// separates "connected" from "flapping" without slowing genuine recovery.
	defaultStableAfter = time.Minute
	// generationHandoffTimeout bounds how long a replacement supervisor waits
	// for the generation it supersedes to release its connection. Teardown is
	// local (cancel + socket Close), so exceeding this means the old connector
	// is wedged — proceeding is then better than leaving the bot offline.
	generationHandoffTimeout = 10 * time.Second
)

// Manager owns the set of running connectors, deduplicates deliveries (both
// platforms are at-least-once, and Slack additionally sends app_mention +
// message for the same event), and fans messages out to the Handler. Each bot
// runs under its own supervisor goroutine that reconnects with backoff, so
// one bad credential or a platform outage never takes down the other bots.
type Manager struct {
	Handler Handler

	// RetryBaseDelay and RetryMaxDelay bound the reconnect backoff for a bot
	// whose connection failed to start or died. Zero values use the defaults
	// (5s doubling up to 5m); tests shrink them.
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	// StableAfter is how long a connection must stay up before the backoff is
	// reset. Zero uses defaultStableAfter; tests shrink it.
	StableAfter time.Duration

	mu      sync.Mutex
	root    context.Context
	cancel  context.CancelFunc
	managed map[string]*managedBot
	seen    *dedup
}

type BotStatus struct {
	AgentID  string `json:"agent_id"`
	BotID    string `json:"bot_id"`
	Platform string `json:"platform"`
	Running  bool   `json:"running"`
	Error    string `json:"error,omitempty"`
}

// connectorKey is the part of a bot's config a live connection depends on.
// Channel rules are deliberately absent: the bridge re-reads them from the
// store per message, so rule edits must not bounce a healthy connection.
type connectorKey struct {
	agentID   string
	botID     string
	platform  string
	botToken  string
	appToken  string
	appID     string
	appSecret string
}

// managedBot is one supervised bot. Identity of the pointer doubles as the
// supervisor generation: a newer Apply that replaces the bot installs a new
// value, and the old supervisor's state writes are dropped.
type managedBot struct {
	key     connectorKey
	cancel  context.CancelFunc
	status  BotStatus
	mention string // live connection's mention markup, for outbound handoffs
	// connector is retained only while this generation is healthy so other
	// DayMug surfaces can reuse the configured Bot identity for thread replies.
	connector Connector
	// done is closed when this generation's supervisor has returned and its
	// connector is released. prev is the predecessor's done, which the
	// supervisor waits on before dialling — two live sockets for one bot mean
	// the platform delivers every message twice.
	done chan struct{}
	prev <-chan struct{}
}

func NewManager(h Handler) *Manager {
	return &Manager{Handler: h, managed: map[string]*managedBot{}, seen: newDedup(2048)}
}

// Apply reconciles the running connectors with cfg. Bots whose
// connection-relevant config is unchanged keep their live connection; new or
// changed bots are (re)started and removed bots stopped. Connections are
// established asynchronously with backoff retries, so Apply returns
// immediately — the UI polls Statuses for the outcome. Safe to call
// repeatedly (admin saves trigger a reload).
func (m *Manager) Apply(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	desired := map[string]connectorKey{}
	for _, agentBot := range cfg.Bots {
		bot := agentBot.Bot
		if agentBot.AgentID == "" || bot.ID == "" || !bot.Enabled {
			continue
		}
		if _, ok := platforms[bot.Platform]; !ok {
			continue
		}
		desired[bot.ID] = connectorKey{
			agentID: agentBot.AgentID, botID: bot.ID, platform: bot.Platform,
			botToken: bot.BotToken, appToken: bot.AppToken,
			appID: bot.AppID, appSecret: bot.AppSecret,
		}
	}

	// retiring maps a bot id to the generation being torn down here, so its
	// replacement can wait for the old connection to actually be released.
	retiring := map[string]<-chan struct{}{}
	for botID, mb := range m.managed {
		if key, ok := desired[botID]; ok && key == mb.key {
			continue
		}
		mb.cancel()
		retiring[botID] = mb.done
		delete(m.managed, botID)
	}
	if len(desired) > 0 && m.root == nil {
		m.root, m.cancel = context.WithCancel(context.Background())
	}
	for botID, key := range desired {
		if _, ok := m.managed[botID]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(m.root)
		mb := &managedBot{key: key, cancel: cancel,
			status: BotStatus{AgentID: key.agentID, BotID: key.botID, Platform: key.platform},
			done:   make(chan struct{}), prev: retiring[botID]}
		m.managed[botID] = mb
		go m.supervise(ctx, mb)
	}
}

// permanentConnectorError is implemented by platform errors that know the same
// request cannot succeed until an operator changes credentials or permissions.
type permanentConnectorError interface {
	Permanent() bool
}

// isPermanentConnectorError also recognizes permission failures from SDKs that
// expose only text errors. The platform clients do not share an error type, but
// these stable API markers let the common supervisor avoid reconnecting with
// credentials the provider has already rejected.
func isPermanentConnectorError(err error) bool {
	if err == nil {
		return false
	}
	var permanent permanentConnectorError
	if errors.As(err, &permanent) && permanent.Permanent() {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"http 401", "http 403", "unauthorized", "forbidden",
		"permission denied", "permission_missing", "missing_scope",
		"not_allowed_token_type", "invalid_auth", "not_authed",
		"account_inactive", "token_revoked", "invalid token",
		"invalid access token", "invalid app", "appsecret", "app secret",
		"authentication failed", "authorization failed", "access denied",
		"insufficient permission", "no permission", "not a bot identity",
		"机器人能力", "权限", "未授权",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// supervise owns one bot's connection lifecycle: build the connector, start
// it, and rebuild with exponential backoff after a transient failed start or a
// dead connection. Permission and credential failures stop this generation
// until the bot configuration is changed.
func (m *Manager) supervise(ctx context.Context, mb *managedBot) {
	defer close(mb.done)
	if !awaitPreviousGeneration(ctx, mb.prev) {
		return
	}

	factory := platforms[mb.key.platform].factory
	botID := mb.key.botID
	delay := m.retryBaseDelay()
	for {
		c := factory(BotConfig{
			ID: botID, Platform: mb.key.platform, Enabled: true,
			BotToken: mb.key.botToken, AppToken: mb.key.appToken,
			AppID: mb.key.appID, AppSecret: mb.key.appSecret,
		})
		stable, err := m.runConnection(ctx, mb, c)
		if ctx.Err() != nil {
			return
		}
		// Only a connection that held resets the backoff. Start succeeding says
		// nothing about the long connection — see defaultStableAfter.
		if stable {
			delay = m.retryBaseDelay()
		}
		errText := "connection lost"
		if err != nil {
			errText = err.Error()
		}
		m.setBotState(mb, nil, "", false, errText)
		if isPermanentConnectorError(err) {
			log.Printf("imbot: %s connector stopped (bot %s): %s — credentials or permissions must be fixed before retrying", mb.key.platform, botID, errText)
			return
		}
		wait := jittered(delay)
		log.Printf("imbot: %s connector down (bot %s): %s — reconnecting in %s", mb.key.platform, botID, errText, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		delay *= 2
		if maxDelay := m.retryMaxDelay(); delay > maxDelay {
			delay = maxDelay
		}
	}
}

// runConnection drives one connection attempt to its end, always releasing the
// connector before returning. stable reports whether the connection stayed up
// long enough to count as a working configuration.
func (m *Manager) runConnection(ctx context.Context, mb *managedBot, c Connector) (bool, error) {
	defer c.Stop()

	agentID, botID := mb.key.agentID, mb.key.botID
	c.SetOnMessage(func(msg Message) {
		msg.AgentID = agentID
		msg.BotID = botID
		m.dispatch(ctx, c, msg)
	})
	down := make(chan error, 1)
	c.SetOnDown(func(err error) {
		select {
		case down <- err:
		default:
		}
	})

	if err := c.Start(ctx); err != nil {
		return false, err
	}
	m.setBotState(mb, c, c.MentionTag(), true, "")
	log.Printf("imbot: %s connector started (bot %s)", mb.key.platform, botID)

	connectedAt := time.Now()
	select {
	case <-ctx.Done():
		return true, nil
	case err := <-down:
		return time.Since(connectedAt) >= m.stableAfter(), err
	}
}

// jittered spreads reconnects that would otherwise fire in lockstep: one Apply
// starts every bot at the same instant, so without jitter a platform outage has
// them all retrying on the same tick — and a shared rate limiter then fails
// them all together, in step, forever. ±20% is enough to decorrelate them while
// keeping the documented "5s to 5min" envelope recognisable.
func jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := int64(d) / 5
	if spread <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*spread+1)-spread)
}

// awaitPreviousGeneration blocks until the superseded supervisor for this bot
// has released its connection, and reports whether it is still worth
// connecting. Waiting here rather than inside Apply keeps admin saves
// non-blocking.
func awaitPreviousGeneration(ctx context.Context, prev <-chan struct{}) bool {
	if prev == nil {
		return true
	}
	timer := time.NewTimer(generationHandoffTimeout)
	defer timer.Stop()
	select {
	case <-prev:
		return true
	case <-timer.C:
		log.Printf("imbot: previous connector generation did not release in %s — connecting anyway", generationHandoffTimeout)
		return true
	case <-ctx.Done():
		return false
	}
}

// setBotState publishes a bot's connection state — unless the bot was removed
// or replaced by a newer Apply since this supervisor started.
func (m *Manager) setBotState(mb *managedBot, connector Connector, mention string, running bool, errText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.managed[mb.key.botID] != mb {
		return
	}
	mb.connector = connector
	mb.mention = mention
	mb.status.Running = running
	mb.status.Error = errText
}

// ResponderFor returns a responder backed by the bot's current healthy
// connector. It never rebuilds a connector or reads credentials; callers use
// the exact Bot identity already supervised by the Manager.
func (m *Manager) ResponderFor(botID string, msg Message) (Responder, bool) {
	m.mu.Lock()
	mb, ok := m.managed[botID]
	if !ok || !mb.status.Running || mb.connector == nil {
		m.mu.Unlock()
		return nil, false
	}
	connector := mb.connector
	m.mu.Unlock()
	return connector.Responder(msg), true
}

// MentionTag returns the live mention markup for a connected bot, or "" when
// the bot is unknown or its connection is down.
func (m *Manager) MentionTag(botID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mb, ok := m.managed[botID]; ok {
		return mb.mention
	}
	return ""
}

func (m *Manager) retryBaseDelay() time.Duration {
	if m.RetryBaseDelay > 0 {
		return m.RetryBaseDelay
	}
	return defaultRetryBaseDelay
}

func (m *Manager) retryMaxDelay() time.Duration {
	if m.RetryMaxDelay > 0 {
		return m.RetryMaxDelay
	}
	return defaultRetryMaxDelay
}

func (m *Manager) stableAfter() time.Duration {
	if m.StableAfter > 0 {
		return m.StableAfter
	}
	return defaultStableAfter
}

// Stop tears down every running connector.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
		m.root = nil
	}
	m.managed = map[string]*managedBot{}
}

// Running reports the platforms with a live connector, for the admin UI.
func (m *Manager) Running() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, mb := range m.managed {
		if mb.status.Running {
			out = append(out, mb.key.platform)
		}
	}
	sort.Strings(out)
	return out
}

func (m *Manager) Statuses(agentID string) []BotStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []BotStatus{}
	for _, mb := range m.managed {
		if agentID == "" || mb.status.AgentID == agentID {
			out = append(out, mb.status)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BotID < out[j].BotID })
	return out
}

func (m *Manager) dispatch(ctx context.Context, c Connector, msg Message) {
	// A cancelled ctx means this connector generation was replaced or removed.
	// Handing the message on anyway burns the dedup slot and then dies deep in
	// the bridge with "context canceled", so the user simply never gets an
	// answer; dropping it here at least leaves the platform free to redeliver.
	if m.Handler == nil || msg.MessageID == "" || ctx.Err() != nil {
		return
	}
	if !m.seen.add(msg.Platform + "|" + msg.AgentID + "|" + msg.BotID + "|" + msg.MessageID) {
		return
	}
	go m.Handler.HandleMessage(ctx, msg, c.Responder(msg))
}

// dedup is a fixed-capacity set with FIFO eviction — enough to absorb
// redelivery bursts without growing unboundedly over a long-lived process.
type dedup struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	cap   int
}

func newDedup(capacity int) *dedup {
	return &dedup{seen: make(map[string]struct{}, capacity), cap: capacity}
}

// add returns false when the key was already present.
func (d *dedup) add(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > d.cap {
		delete(d.seen, d.order[0])
		d.order = d.order[1:]
	}
	return true
}
