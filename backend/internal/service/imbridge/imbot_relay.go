package imbridge

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/prompts"
)

// MentionResolver maps a bot id to its live platform mention markup
// (implemented by imbot.Manager). Nil on the bridge disables outbound
// handoffs.
type MentionResolver interface {
	MentionTag(botID string) string
}

type relayTarget struct {
	agentID string
	botID   string
	name    string
	tag     string
}

type handoffDirective struct {
	botName     string
	instruction string
}

// relayQuoteLimit keeps the quoted reply inside one IM chunk together with
// the mention prefix.
const relayQuoteLimit = 3000

// botRelayLimiter is the relay circuit breaker: a sliding window of
// bot-triggered run timestamps per thread. In-memory on purpose — after a
// restart the window simply starts fresh, and human messages never pass
// through it.
type botRelayLimiter struct {
	mu      sync.Mutex
	threads map[string]*relayThreadWindow
	calls   int
}

// relayThreadWindow is one thread's circuit-breaker state. The window length is
// remembered per key so the periodic sweep expires an entry against the channel
// rule that created it, not against whichever rule happens to call allow next.
type relayThreadWindow struct {
	hits     []time.Time
	notified time.Time
	window   time.Duration
}

// relaySweepInterval is how many allow calls pass between full sweeps. Without
// them a thread that relays once and then goes quiet would keep its entry for
// the lifetime of the process.
const relaySweepInterval = 128

// allow records one bot-triggered run attempt for key and reports whether it
// fits limit within the sliding window. notify is true at most once per
// window per key, so the "limit reached" notice does not itself spam the
// thread.
func (l *botRelayLimiter) allow(key string, limit int, window time.Duration, now time.Time) (allowed, notify bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.threads == nil {
		l.threads = map[string]*relayThreadWindow{}
	}
	l.calls++
	if l.calls%relaySweepInterval == 0 {
		l.sweep(now)
	}
	state := l.threads[key]
	if state == nil {
		state = &relayThreadWindow{}
		l.threads[key] = state
	}
	state.window = window
	state.hits = freshWithin(state.hits, window, now)
	if len(state.hits) < limit {
		state.hits = append(state.hits, now)
		return true, false
	}
	if state.notified.IsZero() || now.Sub(state.notified) >= window {
		state.notified = now
		return false, true
	}
	return false, false
}

// sweep drops every thread whose window has fully elapsed. Caller holds l.mu.
func (l *botRelayLimiter) sweep(now time.Time) {
	for key, state := range l.threads {
		state.hits = freshWithin(state.hits, state.window, now)
		if len(state.hits) > 0 {
			continue
		}
		if state.notified.IsZero() || now.Sub(state.notified) >= state.window {
			delete(l.threads, key)
		}
	}
}

// freshWithin filters in place: the kept prefix is always a prefix of the input
// in the same order, so reusing the backing array is safe.
func freshWithin(hits []time.Time, window time.Duration, now time.Time) []time.Time {
	fresh := hits[:0]
	for _, t := range hits {
		if now.Sub(t) < window {
			fresh = append(fresh, t)
		}
	}
	return fresh
}

// gateBotRelay decides whether a bot-authored message may trigger a run:
// the channel must opt in, the message must @-mention this bot (auto-reply
// never applies to bot chatter — two auto-reply bots would answer each other
// forever), DMs are excluded, and the per-thread sliding window caps the
// chain. Posts the circuit-breaker notice at most once per window.
func (b *IMBridge) gateBotRelay(msg imbot.Message, rule imbot.ChannelRule, responder imbot.Responder) bool {
	if msg.IsDM || !rule.AllowBotMentions || !msg.Mentioned {
		return false
	}
	limit := rule.RelayLimit()
	window := rule.RelayWindow()
	// The quota belongs to the platform thread, not to each receiving bot.
	// threadPlatform() qualifies the key with agent@bot — deliberately so for
	// threadKey()/threadLock(), where two bots in one thread must not block
	// each other — but reusing it here gave an A↔B ping-pong one full quota
	// per direction, i.e. limit × number of opted-in bots.
	key := msg.Platform + "|" + msg.ChannelID + "|" + msg.ThreadID
	allowed, notify := b.relay.allow(key, limit, window, time.Now())
	if allowed {
		return true
	}
	if notify {
		text := fmt.Sprintf(relayLimitReachedTextf, limit, int(window.Minutes()))
		b.respondBestEffort(msg, func(ctx context.Context) error { return responder.Start(ctx, text) })
	}
	return false
}

// parseHandoffDirective accepts only an explicit directive in the final
// non-empty line. Ordinary mentions in prose, quotes, or code never hand work
// to another bot.
func parseHandoffDirective(content string) (handoffDirective, bool) {
	line := strings.TrimSpace(content)
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[i+1:])
	}
	const prefix = prompts.HandoffMarkerPrefix
	if len(line) <= len(prefix) || line[:len(prefix)] != prefix {
		return handoffDirective{}, false
	}
	remainder := line[len(prefix):]
	closing := strings.IndexByte(remainder, ']')
	if closing < 0 {
		return handoffDirective{}, false
	}
	name := strings.TrimSpace(remainder[:closing])
	instruction := strings.TrimSpace(remainder[closing+1:])
	if name == "" || instruction == "" || strings.ContainsAny(name, "[]\r\n") {
		return handoffDirective{}, false
	}
	return handoffDirective{botName: name, instruction: instruction}, true
}

// handoffOwner resolves the human owner behind a bot's agent user, memoised
// per handoffTargets call. Empty means "no owner" — an unknown agent id, a
// lookup failure, or an orphan agent (store.User.Owner() returns "" for those)
// — and an empty owner never matches anything, mirroring
// handler.canAccessOwner. Without that rule every orphan agent on the instance
// would collapse into one shared tenant.
func (b *IMBridge) handoffOwner(ctx context.Context, cache map[string]string, agentID string) string {
	if agentID == "" || b.Store == nil {
		return ""
	}
	if owner, ok := cache[agentID]; ok {
		return owner
	}
	owner := ""
	if user, err := b.Store.GetUser(ctx, agentID); err != nil {
		log.Printf("imbot: relay handoff: owner of agent %q: %v", agentID, err)
	} else {
		owner = user.Owner()
	}
	cache[agentID] = owner
	return owner
}

// handoffTargets returns the connected same-platform bots that have opted into
// bot mentions for this channel *and* belong to the same human owner as the
// source bot. The same allow-list feeds both the agent prompt and the final
// server-side validation, so a bot can never hand work to another tenant's
// agent — nor deny an owner's legitimate handoff by squatting a bot name.
func (b *IMBridge) handoffTargets(ctx context.Context, msg imbot.Message) ([]relayTarget, string, error) {
	caps, ok := imbot.CapabilitiesFor(msg.Platform)
	if b.Mentions == nil || b.Bots == nil || b.Store == nil || msg.IsDM || !ok || !caps.SupportsBotRelay {
		return nil, "", nil
	}
	bots, err := b.Bots.ListBots(ctx, "")
	if err != nil {
		return nil, "", err
	}
	owners := map[string]string{}
	from := ""
	// The bot row is authoritative for the source agent; msg.AgentID is only
	// the fallback for a bot that vanished from the table mid-run.
	sourceAgentID := msg.AgentID
	for _, bot := range bots {
		if bot.ID == msg.BotID {
			from = bot.Name
			sourceAgentID = bot.AgentID
			break
		}
	}
	sourceOwner := b.handoffOwner(ctx, owners, sourceAgentID)
	if sourceOwner == "" {
		return nil, from, nil
	}
	var targets []relayTarget
	for _, bot := range bots {
		if bot.ID == msg.BotID {
			continue
		}
		name := strings.TrimSpace(bot.Name)
		if !bot.Enabled || bot.Platform != msg.Platform || name == "" || strings.ContainsAny(name, "[]\r\n") {
			continue
		}
		if b.handoffOwner(ctx, owners, bot.AgentID) != sourceOwner {
			continue
		}
		rules, err := imbot.ValidateChannels(bot.Channels)
		if err != nil {
			log.Printf("imbot: relay handoff: bot %q channels: %v", name, err)
			continue
		}
		rule, ok := imbot.RuleFor(rules, msg.Platform, msg.ChannelID, msg.IsDM)
		if !ok || !rule.AllowBotMentions {
			continue
		}
		tag := b.Mentions.MentionTag(bot.ID)
		if tag == "" {
			continue
		}
		targets = append(targets, relayTarget{agentID: bot.AgentID, botID: bot.ID, name: name, tag: tag})
	}
	sort.Slice(targets, func(i, j int) bool {
		return strings.ToLower(targets[i].name) < strings.ToLower(targets[j].name)
	})
	return targets, from, nil
}

func (b *IMBridge) handoffSystemPrompt(ctx context.Context, msg imbot.Message) string {
	targets, _, err := b.handoffTargets(ctx, msg)
	if err != nil {
		log.Printf("imbot: handoff prompt: list bots: %v", err)
		return ""
	}
	if len(targets) == 0 {
		return ""
	}
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, "@"+target.name)
	}
	return prompts.IMHandoff(strings.Join(names, ", "))
}

// relayHandoff sends one already-parsed and server-validated handoff. A fresh
// native mention is required because platforms do not reliably deliver mention
// events for edits to the completed progress message.
func (b *IMBridge) relayHandoff(msg imbot.Message, content string, directive handoffDirective, responder imbot.Responder) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	targets, from, err := b.handoffTargets(ctx, msg)
	if err != nil {
		log.Printf("imbot: relay handoff: list bots: %v", err)
		return false
	}
	var matched []relayTarget
	for _, target := range targets {
		if strings.EqualFold(target.name, directive.botName) {
			matched = append(matched, target)
		}
	}
	if len(matched) != 1 {
		log.Printf("imbot: relay handoff: target %q resolved to %d connected bots", directive.botName, len(matched))
		return false
	}
	target := matched[0]
	text := target.tag + " " + directive.instruction
	var messageID string
	var postErr error
	b.respondBestEffort(msg, func(ctx context.Context) error {
		if poster, ok := responder.(imbot.HandoffResponder); ok {
			messageID, postErr = poster.PostHandoff(ctx, text)
			return postErr
		}
		postErr = responder.Post(ctx, text)
		return postErr
	})
	if postErr != nil {
		return false
	}
	caps, _ := imbot.CapabilitiesFor(msg.Platform)
	if caps.DeliversBotMessagesToBots || messageID == "" {
		return true
	}
	// On platforms that do not expose bot-authored messages to other bots,
	// only the internal handoff carries the source reply as context. The
	// visible native-mention message stays compact.
	body := fmt.Sprintf(relayHandoffBodyTextf,
		firstNonEmptyString(from, unknownRelaySenderName),
		directive.instruction,
		relayQuote(content, relayQuoteLimit))
	b.dispatchRelayHandoff(msg, target, messageID, from, body)
	return true
}

// dispatchRelayHandoff compensates for receive-message contracts that exclude
// messages authored by bots. The source bot has already posted a visible
// native mention; this creates the equivalent normalized inbound message for
// the target Agent and lets its own connector author the reply.
func (b *IMBridge) dispatchRelayHandoff(source imbot.Message, target relayTarget, messageID, from, body string) {
	if b.Responders == nil {
		log.Printf("imbot: relay handoff: target bot %q has no responder resolver", target.botID)
		return
	}
	targetMsg := imbot.Message{
		Platform:    source.Platform,
		AgentID:     target.agentID,
		BotID:       target.botID,
		ChannelID:   source.ChannelID,
		ChannelName: source.ChannelName,
		ThreadID:    source.ThreadID,
		MessageID:   messageID,
		SenderID:    source.BotID,
		SenderName:  firstNonEmptyString(from, unknownRelaySenderName),
		Text:        body,
		Mentioned:   true,
		IsDM:        source.IsDM,
		FromBot:     true,
	}
	targetResponder, ok := b.Responders.ResponderFor(target.botID, targetMsg)
	if !ok {
		log.Printf("imbot: relay handoff: target bot %q is not connected", target.botID)
		return
	}
	// The handed-off turn is a new unit of work with its own lifetime, not a
	// continuation of the source turn: the relayHandoff ctx above only bounds
	// the bot lookup and is cancelled on return, while the target agent may run
	// for minutes.
	go b.HandleMessage(context.Background(), targetMsg, targetResponder)
}

// relayQuote truncates on a rune boundary so the handoff stays one chunk.
func relayQuote(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + relayQuoteTruncatedSuffix
}
