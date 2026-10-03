package imbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/prompts"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge/casefile"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// imQueueTimeout bounds how long a message may wait for an account slot
// before giving up with a "try again later" reply.
const imQueueTimeout = 30 * time.Minute

// imQueueWait is imQueueTimeout everywhere except tests, which shrink it so
// the give-up path can be exercised in milliseconds.
var imQueueWait = imQueueTimeout

// imAutoCompactLookupTimeout bounds the single thread-row read the post-turn
// auto-compact check makes. Runs after every IM turn, so it must never hang one.
const imAutoCompactLookupTimeout = 5 * time.Second

var errIMTurnSuperseded = errors.New("IM turn superseded by a newer message")

type imThreadTurn struct {
	cancel context.CancelCauseFunc
}

// IMBridge gates inbound messages on the owning Agent+Bot rules, mirrors each
// platform thread into a DayMug conversation, runs the shared backend/pool,
// and updates both the IM progress message and the web conversation stream.
type IMBridge struct {
	// Runtime is the store, config, pool, drainer, pause gate, sandbox,
	// broadcaster, user hub and backend registry shared with the chat and API
	// surfaces, so an IM turn obeys the same account limits and is jailed
	// exactly like a browser turn by the same owner.
	*service.Runtime
	Bots store.BotStore
	// Mentions resolves sibling bots' live mention markup for bot-to-bot
	// handoffs (implemented by imbot.Manager). Nil disables outbound relays.
	Mentions MentionResolver
	// Responders reuses the currently healthy connector when a web-chat turn
	// belongs to an IM-originated conversation.
	Responders IMResponderResolver

	// relay is the per-thread sliding-window circuit breaker for
	// bot-triggered runs.
	relay botRelayLimiter

	// observedContext retains allowed group chatter that did not start a turn.
	// Platform history remains authoritative when available; this bounded
	// fallback makes internally dispatched handoffs (and platforms without a
	// history API) see the same intervening messages.
	observedContext observedThreadContextCache

	// threadLocks serialises runs per thread so two rapid-fire messages in
	// one 话题 don't race on the same CLI session, and so a preempted turn's
	// bot_threads cursor write cannot interleave with its successor's (see
	// imRun.persistTurnBindings — those writes run on a context detached from
	// the cancelled turn, and this mutex is the only thing ordering them).
	// Grows with distinct threads; entries are tiny and thread counts are
	// human-scale.
	threadMu    sync.Mutex
	threadLocks map[string]*sync.Mutex

	// imTurns makes inbound IM turns latest-message-wins without changing the
	// web dispatcher's FIFO behaviour. The key includes agent+bot identity, so
	// sibling bots sharing one platform thread never cancel each other.
	imTurnMu sync.Mutex
	imTurns  map[string]*imThreadTurn

	// accounts round-robins fresh threads over the agent's granted accounts
	// for the resolved model. Process-local by design: it only has to keep
	// consecutive threads off the same account, not survive a restart.
	accounts service.AccountRotator
}

// HandleMessage implements imbot.Handler. Runs on its own goroutine per
// message (the manager dispatches that way), so blocking here is fine.
func (b *IMBridge) HandleMessage(ctx context.Context, msg imbot.Message, responder imbot.Responder) {
	// The turn owns the responder from here on, so this is the one place that
	// sees every exit path — including the denials below and a failed run,
	// neither of which reaches Complete. A platform-native activity indicator
	// left lit by one of those paths never goes out on its own.
	defer b.respondBestEffort(msg, func(closeCtx context.Context) error {
		imbot.CloseResponder(closeCtx, responder)
		return nil
	})

	resolved, err := b.ruleFor(ctx, msg)
	if err != nil {
		log.Printf("imbot: load message config: %v", err)
		return
	}
	if resolved.outcome != ruleActive {
		// A DM reaching here means the bot has no `dm` rule. That used to be
		// the default-open case, so an operator upgrading from an older build
		// sees working DMs stop reaching the Agent; keep a server-side reason
		// alongside the configurable user-facing notice.
		if msg.IsDM && !msg.FromBot {
			log.Printf("imbot: %s dm from %s ignored: bot %s has no enabled \"dm\" channel rule (DMs are opt-in; add one and scope it with allowed_user_ids)",
				msg.Platform, msg.SenderID, msg.BotID)
		}
		if resolved.outcome == ruleUnconfigured {
			b.replyToDeniedContact(msg, resolved.unconfiguredReply, responder)
		}
		return
	}
	rule := resolved.rule
	// Bot-authored messages go through the relay gate (opt-in + mention +
	// sliding-window circuit breaker). Allowed human group chatter is observed
	// before the mention gate: it stays silent now, but an internal handoff can
	// still import it as thread context later.
	if msg.FromBot {
		if !b.gateBotRelay(msg, rule, responder) {
			return
		}
		b.observeThreadContext(&msg)
	} else {
		if !rule.AllowsSender(msg.SenderID) {
			b.replyToDeniedContact(msg, resolved.unauthorizedReply, responder)
			return
		}
		b.observeThreadContext(&msg)
		// Mention-required group channels still run only on an explicit
		// mention, including follow-ups in an already-linked thread.
		if !msg.IsDM && rule.MentionRequired() && !msg.Mentioned {
			return
		}
	}

	// Past the gate, so a confirmation for a connector-side control instruction
	// can go out. It is a Notice rather than an Update: nothing supersedes it,
	// and on a platform with no edit primitive Update writes nowhere.
	if msg.Ack != "" {
		b.respondBestEffort(msg, func(ctx context.Context) error {
			return imbot.Notify(ctx, responder, msg.Ack)
		})
	}
	// A control instruction with nothing attached — a bare /new — is answered by
	// the confirmation alone. Running the agent on an empty prompt would burn an
	// account slot to reply to nothing.
	if strings.TrimSpace(msg.Text) == "" && len(msg.Attachments) == 0 {
		return
	}

	key := threadKey(msg)
	turnCtx, finishTurn := b.beginIMTurn(ctx, key)
	defer finishTurn()
	b.cancelBoundThreadJob(ctx, msg)

	// Held for the whole run, not just the lookups: run()'s end-of-turn
	// bot_threads writes are what this serialises (see persistTurnBindings).
	lock := b.threadLock(key)
	lock.Lock()
	defer lock.Unlock()
	if errors.Is(context.Cause(turnCtx), errIMTurnSuperseded) {
		return
	}

	_, err = b.run(turnCtx, msg, rule, responder)
	if err != nil {
		if errors.Is(context.Cause(turnCtx), errIMTurnSuperseded) {
			log.Printf("imbot: %s %s/%s: run superseded by a newer message", msg.Platform, msg.ChannelID, msg.ThreadID)
			b.respondBestEffort(msg, func(ctx context.Context) error {
				return responder.Post(ctx, supersededText)
			})
			return
		}
		log.Printf("imbot: %s %s/%s: run failed: %v", msg.Platform, msg.ChannelID, msg.ThreadID, err)
		b.respondBestEffort(msg, func(ctx context.Context) error {
			return imbot.Notify(ctx, responder, errorNoticePrefix+err.Error())
		})
		return
	}
	// After run() unwound its release stack (and with it EndJob), so the compact
	// finds a free room. IM threads are where sessions grow longest — they never
	// get the "user opens a new chat" reset a browser tab does — so skipping
	// this path would leave auto-compaction wired to the surface that needs it
	// least.
	b.maybeAutoCompact(msg)
}

// maybeAutoCompact hands the just-finished thread to the shared auto-compact
// check. Resolves the conversation through the thread row rather than plumbing
// an id back out of run(): syncThreadCursor has already pointed that row at the
// conversation this turn used, and reading it keeps run()'s signature intact.
func (b *IMBridge) maybeAutoCompact(msg imbot.Message) {
	if b.Cfg == nil || b.Cfg.AutoCompactRatio <= 0 || b.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), imAutoCompactLookupTimeout)
	defer cancel()
	thread, err := b.Store.GetBotThread(ctx, threadPlatform(msg), msg.ChannelID, msg.ThreadID)
	if err != nil || thread.ConversationID == "" {
		return
	}
	b.compactRunner().MaybeAutoCompact(thread.ConversationID)
}

// compactRunner is the runtime's PromptRunner; Compact and MaybeAutoCompact
// never touch its dispatcher or prompt observer, so the full assembly is safe.
func (b *IMBridge) compactRunner() *service.PromptRunner {
	return b.PromptRunner()
}

func (b *IMBridge) beginIMTurn(parent context.Context, threadKey string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	turn := &imThreadTurn{cancel: cancel}

	b.imTurnMu.Lock()
	if b.imTurns == nil {
		b.imTurns = make(map[string]*imThreadTurn)
	}
	previous := b.imTurns[threadKey]
	b.imTurns[threadKey] = turn
	b.imTurnMu.Unlock()

	if previous != nil {
		previous.cancel(errIMTurnSuperseded)
	}
	return ctx, func() {
		b.imTurnMu.Lock()
		if b.imTurns[threadKey] == turn {
			delete(b.imTurns, threadKey)
		}
		b.imTurnMu.Unlock()
		cancel(nil)
	}
}

// cancelBoundThreadJob also covers a run started from the web UI. IM-to-IM
// races are handled by beginIMTurn even before the conversation binding and
// Broadcaster job have been created.
func (b *IMBridge) cancelBoundThreadJob(ctx context.Context, msg imbot.Message) {
	if b.Broadcaster == nil {
		return
	}
	thread, err := b.Store.GetBotThread(ctx, threadPlatform(msg), msg.ChannelID, msg.ThreadID)
	if err != nil || thread.ConversationID == "" {
		return
	}
	b.Broadcaster.CancelJob(thread.ConversationID)
}

type ruleOutcome int

const (
	ruleInactive ruleOutcome = iota
	ruleUnconfigured
	ruleDisabled
	ruleActive
)

type resolvedBotRule struct {
	rule              imbot.ChannelRule
	outcome           ruleOutcome
	unconfiguredReply string
	unauthorizedReply string
}

func (b *IMBridge) ruleFor(ctx context.Context, msg imbot.Message) (resolvedBotRule, error) {
	if msg.AgentID == "" || msg.BotID == "" {
		// Both platforms are accepted only from a Manager-owned connector that
		// injected its configured agent and bot ids.
		return resolvedBotRule{outcome: ruleInactive}, nil
	}
	u, err := b.Store.GetUser(ctx, msg.AgentID)
	if err != nil {
		return resolvedBotRule{outcome: ruleInactive}, fmt.Errorf("IM agent %q not found: %w", msg.AgentID, err)
	}
	if u.Username != "" || u.Archived || b.Bots == nil {
		return resolvedBotRule{outcome: ruleInactive}, nil
	}
	bot, err := b.Bots.GetBot(ctx, msg.BotID)
	if err != nil {
		return resolvedBotRule{outcome: ruleInactive}, nil
	}
	if bot.AgentID != u.ID || !bot.Enabled || msg.Platform != bot.Platform {
		return resolvedBotRule{outcome: ruleInactive}, nil
	}
	resolved := resolvedBotRule{
		unconfiguredReply: bot.UnconfiguredReply,
		unauthorizedReply: bot.UnauthorizedReply,
	}
	rules, err := imbot.ValidateChannels(bot.Channels)
	if err != nil {
		resolved.outcome = ruleInactive
		return resolved, err
	}
	var matched bool
	resolved.rule, matched = imbot.MatchRule(rules, msg.Platform, msg.ChannelID, msg.IsDM)
	switch {
	case !matched:
		resolved.outcome = ruleUnconfigured
	case !resolved.rule.IsEnabled():
		resolved.outcome = ruleDisabled
	default:
		resolved.outcome = ruleActive
	}
	return resolved, nil
}

// replyToDeniedContact only answers a human who directly contacted this bot:
// an explicit mention in a group or any DM. Other channel chatter and
// bot-authored messages remain silent to avoid noise and reply loops.
func (b *IMBridge) replyToDeniedContact(msg imbot.Message, text string, responder imbot.Responder) {
	if msg.FromBot || (!msg.IsDM && !msg.Mentioned) || strings.TrimSpace(text) == "" {
		return
	}
	b.respondBestEffort(msg, func(ctx context.Context) error {
		return responder.Start(ctx, text)
	})
}

// replyBestEffort posts on a fresh context — the run context may already be
// cancelled/expired, and the failure notice must still reach the thread.
func (b *IMBridge) respondBestEffort(msg imbot.Message, action func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := action(ctx); err != nil {
		log.Printf("imbot: %s %s/%s: reply failed: %v", msg.Platform, msg.ChannelID, msg.ThreadID, err)
	}
}

// agentFor resolves the owning DayMug agent injected by Manager. There is no
// global fallback and channel rules cannot redirect to another agent.
func (b *IMBridge) agentFor(ctx context.Context, msg imbot.Message) (store.User, error) {
	if msg.AgentID != "" {
		u, err := b.Store.GetUser(ctx, msg.AgentID)
		if err != nil {
			return store.User{}, fmt.Errorf("IM agent %q 不存在", msg.AgentID)
		}
		if u.Username != "" {
			return store.User{}, fmt.Errorf("IM 配置必须绑定到 agent，不能绑定登录用户")
		}
		return u, nil
	}
	return store.User{}, errors.New("IM 消息缺少所属 agent")
}

func threadPlatform(msg imbot.Message) string {
	if msg.AgentID == "" || msg.BotID == "" {
		return msg.Platform
	}
	return msg.Platform + "@" + msg.AgentID + "@" + msg.BotID
}

// threadKey names the serialisation domain of one IM thread: the bot identity
// that owns it plus the platform thread coordinates. Both the inbound IM path
// and the web-mirror path MUST derive their lock key through this function,
// or the two surfaces take two different mutexes and a web turn and an IM turn
// drive the same thread concurrently — exactly what the lock exists to prevent.
func threadKey(msg imbot.Message) string {
	return threadPlatform(msg) + "|" + msg.ChannelID + "|" + msg.ThreadID
}

// ensureConversation returns the thread's live conversation, creating one when
// there is none. title names a created conversation; empty means the IM
// fallback title, which auto-title later replaces. rotatedFrom links a created
// conversation to the one a case-mode rotation retired for it.
func (b *IMBridge) ensureConversation(ctx context.Context, msg imbot.Message, agentUser store.User, thread store.BotThread, title, rotatedFrom string, seed service.ConversationSeed) (store.Conversation, bool, error) {
	if thread.ConversationID != "" {
		if conv, err := b.Store.GetConversation(ctx, thread.ConversationID); err == nil {
			return conv, false, nil
		}
	}
	convID := uuid.New().String()
	if title == "" {
		title = imConversationFallbackTitle(msg)
	}
	// A thread that outlived its conversation keeps its provider session, so
	// the replacement conversation continues it.
	row := store.NewConversation{
		ID: convID, Title: title, UserID: agentUser.ID, WorkDir: agentUser.WorkDir, SessionID: thread.SessionID,
	}
	if err := service.CreateSeededConversation(ctx, b.Store, row, seed); err != nil {
		return store.Conversation{}, false, fmt.Errorf("创建 IM conversation: %w", err)
	}
	if rotatedFrom != "" {
		// Not fatal: a missing link only costs later rotations their number.
		if err := b.Store.SetConversationRotatedFrom(ctx, convID, rotatedFrom); err != nil {
			log.Printf("[case] link conv=%s to rotated %s: %v", convID, rotatedFrom, err)
		}
	}
	conv, err := b.Store.GetConversation(ctx, convID)
	if err != nil {
		return store.Conversation{}, false, err
	}
	return conv, true, nil
}

// persistInbound writes the triggering IM message into the mirrored
// conversation and returns its row id, which the run needs to clear the
// pool-queued marker once a slot is granted.
func (b *IMBridge) persistInbound(ctx context.Context, msg imbot.Message, conv store.Conversation, created bool, attachments []IMImageMetadata) (string, error) {
	metadata, err := json.Marshal(IMMessageMetadata{
		Attachments: attachments,
		Sender: &IMSenderMetadata{
			Platform: msg.Platform,
			ID:       msg.SenderID,
			Name:     msg.SenderName,
		},
	})
	if err != nil {
		return "", fmt.Errorf("编码 IM 消息元数据: %w", err)
	}
	row := store.Message{
		ID:             uuid.New().String(),
		ConversationID: conv.ID,
		Role:           "user",
		Content:        b.buildPrompt(msg),
		Metadata:       metadata,
		SourceID:       imMessageSourceID(msg.Platform, msg.MessageID),
		// Born queued: the account slot has not been asked for yet, let alone
		// granted. imRun.leaveQueue clears this the moment the turn starts.
		QueueStatus: store.QueueStatusPoolQueued,
	}
	if err := b.Store.SaveMessage(ctx, row); err != nil {
		return "", fmt.Errorf("保存 IM 消息: %w", err)
	}
	// No prompt_started here. It means "this prompt is now running" on the web
	// path (prompt_runner emits it after the slot is granted), and firing it at
	// persist time made every IM turn look started while it was still queued —
	// the chat UI promoted the row out of its staging area, so the queue card
	// and its position never appeared. leaveQueue owns it now.
	b.broadcastIM(conv.ID, service.ServerMessage{
		Type: "user_message", Content: row.Content, MessageID: row.ID,
		Metadata: row.Metadata, QueueStatus: row.QueueStatus,
	})
	if refreshed, err := b.Store.GetConversation(ctx, conv.ID); err == nil {
		eventType := "conversation_updated"
		if created {
			eventType = "conversation_added"
		}
		b.broadcastConversation(ctx, eventType, &refreshed)
	}
	return row.ID, nil
}

func (b *IMBridge) persistThreadContext(ctx context.Context, msg imbot.Message, conv store.Conversation, agentUser store.User, afterMessageID string, budget *inboundAttachmentBudget) ([]imbot.Message, error) {
	if msg.LoadThreadMessages == nil {
		return nil, nil
	}
	history, err := msg.LoadThreadMessages(ctx, afterMessageID)
	if err != nil {
		return nil, fmt.Errorf("读取 IM 话题上下文: %w", err)
	}
	persisted := make([]imbot.Message, 0, len(history))
	for _, historical := range history {
		attachments, notice, err := b.materializeInboundImages(ctx, historical, agentUser, budget)
		if err != nil {
			return nil, err
		}
		// Rewrite before the row is built so the notice lands in both the
		// persisted transcript and the prompt this history feeds.
		historical.Text = appendInboundNotice(historical.Text, notice)
		metadata, err := json.Marshal(IMMessageMetadata{
			Attachments: attachments,
			Sender: &IMSenderMetadata{
				Platform: historical.Platform,
				ID:       historical.SenderID,
				Name:     historical.SenderName,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("编码 IM 话题消息元数据: %w", err)
		}
		role := "user"
		eventType := "user_message"
		if historical.FromBot {
			role = "assistant"
			eventType = "result"
		}
		row := store.Message{
			ID:             uuid.New().String(),
			ConversationID: conv.ID,
			Role:           role,
			Content:        b.buildPrompt(historical),
			Metadata:       metadata,
			SourceID:       imMessageSourceID(historical.Platform, historical.MessageID),
		}
		inserted := true
		if row.SourceID == "" {
			if err := b.Store.SaveMessage(ctx, row); err != nil {
				return nil, fmt.Errorf("保存 IM 话题上下文: %w", err)
			}
		} else {
			inserted, err = b.Store.SaveImportedMessage(ctx, row, time.Now())
			if err != nil {
				return nil, fmt.Errorf("保存 IM 话题上下文: %w", err)
			}
		}
		if !inserted {
			continue
		}
		persisted = append(persisted, historical)
		b.broadcastIM(conv.ID, service.ServerMessage{Type: eventType, Content: row.Content, MessageID: row.ID, Metadata: row.Metadata})
	}
	return persisted, nil
}

func imMessageSourceID(platform, messageID string) string {
	if platform == "" || messageID == "" {
		return ""
	}
	return "im:" + platform + ":" + messageID
}

// broadcastIM is the IM half of PromptRunner.broadcast, and has to intercept
// context_usage for the same two reasons that one does.
//
// The cached copy powers a tab that joins between turns; the persisted copy
// powers a cold start after a restart. Case-file mode then made the persisted
// copy load-bearing for something the web path never needed it for:
// casefile.ContextOverThreshold reads conversations.last_context_usage to
// decide whether to retire the thread's session. Without this interception that column stays
// empty for every IM-driven conversation, the check reads "" and returns false,
// and case-mode rotation silently never fires — which is exactly what happened
// in production, invisibly, because the live token bar is fed by the Broadcast
// call below and looked correct the whole time.
func (b *IMBridge) broadcastIM(conversationID string, msg service.ServerMessage) {
	if b.Broadcaster == nil || conversationID == "" {
		return
	}
	msg.ConversationID = conversationID
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	b.Broadcaster.Broadcast(conversationID, data)
	if msg.Type != "context_usage" {
		return
	}
	b.Broadcaster.SetLastContextUsage(conversationID, data)
	if content, ok := msg.Content.(string); ok {
		b.MessagePersister().PersistContextUsage(conversationID, content)
	}
}

// caseRotateRatio is the deployment's case-mode rotation threshold, falling
// back to the built-in when no config is wired (tests, and any embedder that
// constructs an IMBridge directly).
func (b *IMBridge) caseRotateRatio() float64 {
	if b.Cfg == nil {
		return casefile.RotateContextRatio
	}
	return b.Cfg.CaseRotateContextRatio
}

func (b *IMBridge) broadcastConversation(ctx context.Context, eventType string, conv *store.Conversation) {
	if b.UserHub == nil || conv == nil {
		return
	}
	data, err := json.Marshal(service.ServerMessage{Type: eventType, ConversationID: conv.ID, Conversation: conv})
	if err != nil {
		return
	}
	b.UserHub.Broadcast(service.ResolveHubOwnerID(ctx, b.Store, conv.UserID), data)
}

func imConversationFallbackTitle(msg imbot.Message) string {
	channel := firstNonEmptyString(msg.ChannelName, msg.ChannelID, "DM")
	return imConversationTitlePrefix(msg) + channel
}

func imConversationTitlePrefix(msg imbot.Message) string {
	platform := "Slack"
	if caps, ok := imbot.CapabilitiesFor(msg.Platform); ok {
		platform = caps.DisplayName
	}
	return platform + "·"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// resolveConversationSeed picks what a run executes on. The sticky thread
// binding comes first (a thread never migrates models mid-conversation), then
// the Bot's configured model; past those two IM-only layers the choice is
// service.ResolveConversationSeed — the Agent's default model (with its think
// level) and then the server default — exactly as a web-created conversation
// for the same Agent would get.
func (b *IMBridge) resolveConversationSeed(
	agentUser store.User,
	thread store.BotThread,
	configuredModel string,
) (service.ConversationSeed, error) {
	if thread.Provider != "" && thread.Model != "" &&
		service.IsValidModelForConfig(b.Cfg, thread.Provider, thread.Model) {
		return service.ConversationSeed{Provider: thread.Provider, Model: thread.Model}, nil
	}
	var req service.ConversationSeedRequest
	if configuredModel = strings.TrimSpace(configuredModel); configuredModel != "" {
		if p := service.ProviderForModelForConfig(b.Cfg, configuredModel); p != "" {
			req.Provider, req.Model = p, configuredModel
		}
	}
	seed, err := service.ResolveConversationSeed(b.Cfg, agentUser, req)
	if err != nil {
		return service.ConversationSeed{}, fmt.Errorf("无法为该话题选择模型：%w", err)
	}
	return seed, nil
}

// resolveRunAccount picks which of the agent's provider accounts this turn
// runs on.
//
// A thread that already has a live conversation keeps its account: the CLI
// session being resumed lives in that account's config dir, so re-homing it
// mid-thread loses the session. sticky carries that pin (empty for a fresh
// thread, or one whose conversation window just expired).
//
// Everything else rotates round-robin across every granted account that serves
// the resolved model. A bot pins one default model, so without rotation every
// thread lands on the single default binding and queues there while the agent's
// other accounts — same model, separate quota — stay idle.
//
// The binding remains the answer whenever rotation has nothing better to say:
// no granted accounts serve the model, or the binding itself isn't one of them
// (a deliberately odd config we don't second-guess).
func (b *IMBridge) resolveRunAccount(agentUser store.User, sticky, provider, model string) string {
	binding := agentUser.ProviderBindings[provider]
	granted := agentUser.ProviderAccounts[provider]
	if sticky != "" && (sticky == binding || slices.Contains(granted, sticky)) {
		return sticky
	}
	candidates := service.EligibleAccountsForModel(b.Cfg, agentUser, provider, model)
	if len(candidates) == 0 || (binding != "" && !slices.Contains(candidates, binding)) {
		return binding
	}
	key := agentUser.ID + "|" + provider + "|" + model
	next := b.accounts.Next(key, candidates, func(account string) bool {
		if b.Pool == nil {
			return true
		}
		_, cooling := b.Pool.CooldownUntil(account, model)
		return !cooling
	})
	if next == "" {
		return binding
	}
	return next
}

// buildPrompt frames the raw IM text for the agent. Group messages carry the
// sender's name so the agent can tell voices apart in a shared thread.
func (b *IMBridge) buildPrompt(msg imbot.Message) string {
	text := strings.TrimSpace(msg.Text)
	if msg.IsDM {
		return text
	}
	name := msg.SenderName
	if name == "" {
		name = msg.SenderID
	}
	if name == "" {
		return text
	}
	if text == "" {
		return "[" + name + "]"
	}
	return "[" + name + "]: " + text
}

// Thread-context budget. The backfill itself is deliberately unbounded — every
// message the thread produced still lands in the mirrored conversation, which is
// what DayMug's web history is for. What is bounded is how much of it is pasted
// in front of the model, because that blob is re-read on every turn of the
// resulting session.
//
// Two shapes make an unbounded window expensive. A thread that ran unattended
// for hours backfills the whole gap in one shot; and two bots handing work back
// and forth mirror each other's full progress narration, so each handoff
// re-injects everything the other one said. On 2026-07-31 one bug thread put
// roughly 36 KB of the co-bot's own prose through this path.
//
// Newest messages are kept: they are the ones a short follow-up like "check it"
// refers to. Older ones are elided with a pointer to the full record.
const (
	imThreadContextMessageLimit    = 20
	imThreadContextByteLimit       = 24000
	imThreadContextPerMessageBytes = 4000
)

// imThreadContextElidedTextf heads the window when older messages were dropped;
// formatted with the number omitted.
const imThreadContextElidedTextf = "(%d older message(s) omitted; the full thread is in the DayMug conversation)\n"

// imThreadContextRelayedTextf heads the window on a handoff turn, where the
// other agent's own messages are represented by the handoff instruction instead
// of being pasted in; formatted with the number left out.
const imThreadContextRelayedTextf = "(%d message(s) from other agents are not repeated here — the instruction below is their request, " +
	"and the full thread is in the DayMug conversation)\n"

// buildPromptWithThreadContext makes platform messages imported into the mirrored
// conversation visible to the CLI session as well. Persisting them alone only
// updates DayMug's web history; a newly created backend session otherwise sees
// the triggering mention and cannot resolve short follow-ups such as "check it".
func (b *IMBridge) buildPromptWithThreadContext(msg imbot.Message, history []imbot.Message) string {
	history, relayed := excludeRelayedAgentMessages(msg, history)
	if len(history) == 0 && relayed == 0 {
		return b.buildPrompt(msg)
	}
	rendered := make([]string, len(history))
	for i, historical := range history {
		rendered[i] = relayQuote(b.buildPrompt(historical), imThreadContextPerMessageBytes)
	}
	kept, dropped := boundThreadContext(rendered)
	platform := "Slack"
	if caps, ok := imbot.CapabilitiesFor(msg.Platform); ok {
		platform = caps.PromptName
	}
	var prompt strings.Builder
	prompt.WriteString("Earlier messages in this " + platform + " thread, oldest first:\n")
	if relayed > 0 {
		fmt.Fprintf(&prompt, imThreadContextRelayedTextf, relayed)
	}
	if dropped > 0 {
		fmt.Fprintf(&prompt, imThreadContextElidedTextf, dropped)
	}
	for _, line := range kept {
		prompt.WriteString(line)
		prompt.WriteByte('\n')
	}
	prompt.WriteString("\nCurrent message:\n")
	prompt.WriteString(b.buildPrompt(msg))
	return prompt.String()
}

// excludeRelayedAgentMessages drops the other agent's own messages from a turn
// another agent handed over, and reports how many were left out.
//
// The [HANDOFF] line is the interface between two agents: it already states the
// request, and on the platforms that hide bot messages from bots the relayed
// text carries a bounded quote of the source reply as well. Pasting the source
// agent's full progress narration on top of that delivers the same content a
// second time — and it is the expensive half, because it stays in the receiving
// CLI session and is re-read on every later turn. Measured on 2026-07-31: two
// agents relaying one bug fix mirrored ~36 KB of each other's prose this way.
//
// Human messages from the same gap are always kept: the handoff cannot restate
// what a person said while the other agent was working, and answering those is
// the reason unmentioned chatter is imported at all. Nothing is dropped from the
// database — the DayMug conversation still shows every message.
func excludeRelayedAgentMessages(msg imbot.Message, history []imbot.Message) ([]imbot.Message, int) {
	if !msg.FromBot {
		return history, 0
	}
	kept := make([]imbot.Message, 0, len(history))
	relayed := 0
	for _, historical := range history {
		// Connectors never flag this bot's own echo as FromBot, so this is
		// exactly "authored by another agent".
		if historical.FromBot {
			relayed++
			continue
		}
		kept = append(kept, historical)
	}
	return kept, relayed
}

// boundThreadContext keeps the newest rendered messages that fit the budget and
// reports how many older ones were dropped. The newest message is always kept
// even if it alone exceeds the byte budget: dropping the message immediately
// before the current one would break exactly the follow-ups this context exists
// to resolve.
func boundThreadContext(rendered []string) (kept []string, dropped int) {
	budget := imThreadContextByteLimit
	first := len(rendered)
	for i := len(rendered) - 1; i >= 0; i-- {
		if len(rendered)-i > imThreadContextMessageLimit {
			break
		}
		cost := len(rendered[i]) + 1
		if cost > budget && i != len(rendered)-1 {
			break
		}
		budget -= cost
		first = i
	}
	return rendered[first:], first
}

func imSystemPrompt(msg imbot.Message) string {
	platform := "Slack"
	resolvesMentions := false
	if caps, ok := imbot.CapabilitiesFor(msg.Platform); ok {
		platform = caps.SystemPromptName
		resolvesMentions = caps.ResolvesNameMentions
	}
	prompt := prompts.IMSession(platform)
	if resolvesMentions {
		prompt += "\n\n" + prompts.IMMention
	}
	return prompt
}

func (b *IMBridge) threadLock(key string) *sync.Mutex {
	b.threadMu.Lock()
	defer b.threadMu.Unlock()
	if b.threadLocks == nil {
		b.threadLocks = map[string]*sync.Mutex{}
	}
	if _, ok := b.threadLocks[key]; !ok {
		b.threadLocks[key] = &sync.Mutex{}
	}
	return b.threadLocks[key]
}
