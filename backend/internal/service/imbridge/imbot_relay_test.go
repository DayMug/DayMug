package imbridge

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

type stubMentions map[string]string

func (s stubMentions) MentionTag(botID string) string { return s[botID] }

type handoffReplyRecorder struct {
	*imbottest.ReplyRecorder
	messageID string
}

func (r *handoffReplyRecorder) PostHandoff(_ context.Context, text string) (string, error) {
	return r.messageID, r.Record(text)
}

func relayTestBridge(channels string) (*IMBridge, *storetest.Fake) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent1", OwnerID: "owner1"}}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true, Channels: channels}}
	return &IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms}, ms
}

func relayMsg(messageID string, mutate func(*imbot.Message)) imbot.Message {
	msg := imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1",
		ThreadID: "t1", MessageID: messageID, Text: "请审核", FromBot: true, Mentioned: true,
	}
	if mutate != nil {
		mutate(&msg)
	}
	return msg
}

func TestIMBridgeBotRelayGating(t *testing.T) {
	tests := []struct {
		name      string
		channels  string
		mutate    func(*imbot.Message)
		wantReply bool
	}{
		{
			name:     "channel without opt-in stays silent",
			channels: `[{"channel":"C1","auto_reply":true}]`,
		},
		{
			name:     "opted-in channel without mention stays silent even with auto_reply",
			channels: `[{"channel":"C1","auto_reply":true,"allow_bot_mentions":true}]`,
			mutate:   func(m *imbot.Message) { m.Mentioned = false },
		},
		{
			name:      "opted-in channel with mention reaches the run path",
			channels:  `[{"channel":"C1","allow_bot_mentions":true}]`,
			wantReply: true, // run fails on uninitialised pool → error reply
		},
		{
			name:     "bot DMs never relay",
			channels: `[{"channel":"dm","allow_bot_mentions":true}]`,
			mutate:   func(m *imbot.Message) { m.IsDM = true; m.ChannelID = "D1" },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := relayTestBridge(tt.channels)
			rec := &imbottest.ReplyRecorder{}
			b.HandleMessage(context.Background(), relayMsg("m1", tt.mutate), rec)
			if tt.wantReply && len(rec.All()) == 0 {
				t.Fatal("expected a reply, got none")
			}
			if !tt.wantReply && len(rec.All()) != 0 {
				t.Fatalf("expected silence, got %v", rec.All())
			}
		})
	}
}

func TestIMBridgeBotRelayRateLimit(t *testing.T) {
	b, _ := relayTestBridge(`[{"channel":"C1","allow_bot_mentions":true,"bot_mention_limit":1}]`)
	rec := &imbottest.ReplyRecorder{}

	b.HandleMessage(context.Background(), relayMsg("m1", nil), rec)
	if n := len(rec.All()); n == 0 {
		t.Fatal("first bot-triggered run should pass the limiter")
	}
	replies := len(rec.All())

	// Second trigger inside the window: blocked, one circuit-breaker notice.
	b.HandleMessage(context.Background(), relayMsg("m2", nil), rec)
	notices := rec.All()[replies:]
	if len(notices) != 1 || !strings.Contains(notices[0], "接力已达上限") {
		t.Fatalf("expected one limit notice, got %v", notices)
	}

	// Third trigger: still blocked, and the notice is not repeated.
	before := len(rec.All())
	b.HandleMessage(context.Background(), relayMsg("m3", nil), rec)
	if len(rec.All()) != before {
		t.Fatalf("limit notice repeated: %v", rec.All()[before:])
	}

	// A human message in the same thread is never rate-limited.
	human := relayMsg("m4", func(m *imbot.Message) { m.FromBot = false })
	b.HandleMessage(context.Background(), human, rec)
	if len(rec.All()) == before {
		t.Fatal("human message must bypass the relay limiter")
	}
}

// relayTwoBotBridge wires two bots owned by the same human into one platform
// thread — the shape of an A↔B relay ping-pong.
func relayTwoBotBridge(channels string) *IMBridge {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent1", OwnerID: "owner1"}, {ID: "agent2", OwnerID: "owner1"}}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true, Channels: channels},
		{ID: "bot2", AgentID: "agent2", Platform: "slack", Enabled: true, Channels: channels},
	}
	return &IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms}
}

func relayBotMsg(botID, agentID, channelID, threadID, messageID string) imbot.Message {
	return imbot.Message{
		Platform: "slack", AgentID: agentID, BotID: botID, ChannelID: channelID,
		ThreadID: threadID, MessageID: messageID, Text: "请审核", FromBot: true, Mentioned: true,
	}
}

// deliverRelay runs one bot-authored message and returns only the replies it
// produced. A message that clears the limiter still replies — the run fails on
// the uninitialised pool — so the notice text is what distinguishes the two.
func deliverRelay(b *IMBridge, rec *imbottest.ReplyRecorder, msg imbot.Message) []string {
	before := len(rec.All())
	b.HandleMessage(context.Background(), msg, rec)
	return rec.All()[before:]
}

func relayBlocked(replies []string) bool {
	for _, r := range replies {
		if strings.Contains(r, "接力已达上限") {
			return true
		}
	}
	return false
}

// The circuit breaker is documented as a per-thread quota. Keying it by the
// receiving bot handed every direction of an A↔B relay its own budget.
func TestIMBridgeBotRelayQuotaIsSharedAcrossBotsInThread(t *testing.T) {
	b := relayTwoBotBridge(`[{"channel":"C1","allow_bot_mentions":true,"bot_mention_limit":1}]`)
	rec := &imbottest.ReplyRecorder{}

	first := deliverRelay(b, rec, relayBotMsg("bot1", "agent1", "C1", "t1", "m1"))
	if len(first) == 0 || relayBlocked(first) {
		t.Fatalf("first relay should consume the quota and run, got %v", first)
	}

	// Same thread, different bot: the quota is already spent.
	second := deliverRelay(b, rec, relayBotMsg("bot2", "agent2", "C1", "t1", "m2"))
	if !relayBlocked(second) {
		t.Fatalf("a second bot in the same thread must share the quota, got %v", second)
	}

	// The notice is per thread per window, not per bot: neither bot repeats it.
	for _, msg := range []imbot.Message{
		relayBotMsg("bot1", "agent1", "C1", "t1", "m3"),
		relayBotMsg("bot2", "agent2", "C1", "t1", "m4"),
	} {
		if replies := deliverRelay(b, rec, msg); len(replies) != 0 {
			t.Fatalf("blocked relay from %s must stay silent, got %v", msg.BotID, replies)
		}
	}
}

// Guard against over-correcting the key into something channel- or
// platform-wide: distinct threads keep distinct budgets.
func TestIMBridgeBotRelayQuotaIsPerThread(t *testing.T) {
	b := relayTwoBotBridge(`[{"channel":"*","allow_bot_mentions":true,"bot_mention_limit":1}]`)
	rec := &imbottest.ReplyRecorder{}

	if replies := deliverRelay(b, rec, relayBotMsg("bot1", "agent1", "C1", "t1", "m1")); relayBlocked(replies) {
		t.Fatalf("first relay must pass, got %v", replies)
	}
	for _, tt := range []struct {
		name                string
		channelID, threadID string
	}{
		{"other thread in the same channel", "C1", "t2"},
		{"other channel", "C2", "t1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := relayBotMsg("bot2", "agent2", tt.channelID, tt.threadID, "m-"+tt.channelID+tt.threadID)
			replies := deliverRelay(b, rec, msg)
			if len(replies) == 0 || relayBlocked(replies) {
				t.Fatalf("quota must be independent per thread, got %v", replies)
			}
		})
	}
}

func TestBotRelayLimiterSweepsExpiredThreads(t *testing.T) {
	var l botRelayLimiter
	t0 := time.Unix(1_700_000_000, 0)
	window := 10 * time.Minute

	for i := 0; i < 200; i++ {
		l.allow("thread-"+strconv.Itoa(i), 1, window, t0)
	}
	if len(l.threads) != 200 {
		t.Fatalf("expected 200 tracked threads, got %d", len(l.threads))
	}

	// Enough calls to cross a sweep boundary after every earlier window expired.
	for i := 0; i < 2*relaySweepInterval; i++ {
		l.allow("survivor", 1_000, window, t0.Add(11*time.Minute))
	}
	if len(l.threads) != 1 {
		t.Fatalf("expired threads must be swept, still tracking %d", len(l.threads))
	}
	if _, ok := l.threads["survivor"]; !ok {
		t.Fatal("the active thread must survive the sweep")
	}
}

func TestBotRelayLimiterSlidingWindow(t *testing.T) {
	var l botRelayLimiter
	t0 := time.Unix(1_700_000_000, 0)
	window := 10 * time.Minute

	if ok, _ := l.allow("k", 2, window, t0); !ok {
		t.Fatal("first hit should pass")
	}
	if ok, _ := l.allow("k", 2, window, t0.Add(1*time.Minute)); !ok {
		t.Fatal("second hit should pass")
	}
	allowed, notify := l.allow("k", 2, window, t0.Add(2*time.Minute))
	if allowed || !notify {
		t.Fatalf("third hit should be blocked with notice, got allowed=%v notify=%v", allowed, notify)
	}
	if allowed, notify := l.allow("k", 2, window, t0.Add(3*time.Minute)); allowed || notify {
		t.Fatal("repeat blocks inside the window must not re-notify")
	}
	if ok, _ := l.allow("k", 2, window, t0.Add(11*time.Minute)); !ok {
		t.Fatal("expired hits must free the window")
	}
	if ok, _ := l.allow("other", 2, window, t0.Add(2*time.Minute)); !ok {
		t.Fatal("keys must be independent")
	}
	if ok, _ := l.allow("zero", 0, window, t0); ok {
		t.Fatal("limit 0 must block every relay")
	}
}

func TestIMBridgeRelayHandoff(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Coder", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}}, agentUser,
		{ID: "agent2", Name: "Reviewer", OwnerID: "owner1", WorkDir: t.TempDir()},
		{ID: "agent3", Name: "Offline", OwnerID: "owner1", WorkDir: t.TempDir()}}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Name: "Coder", Platform: "slack", Enabled: true, Channels: `[{"channel":"*","auto_reply":true}]`},
		{ID: "bot2", AgentID: "agent2", Name: "Reviewer", Platform: "slack", Enabled: true,
			Channels: `[{"channel":"*","allow_bot_mentions":true}]`},
		{ID: "bot3", AgentID: "agent3", Name: "Offline", Platform: "slack", Enabled: true,
			Channels: `[{"channel":"*","allow_bot_mentions":true}]`},
	}
	calls := &agenttest.CallRecord{}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "代码写完了，正文提到 @Offline 不会触发。\n\n[HANDOFF @Reviewer] 请审核边界条件", Rec: calls})}, Bots: ms,

		Mentions: stubMentions{"bot2": "<@UREV>"}, // bot3 has no live connection
	}
	rec := &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1",
		ThreadID: "171.1", MessageID: "C1|171.1", Text: "写一个函数", Mentioned: true,
	}, rec)

	replies := rec.All()
	if len(replies) == 0 {
		t.Fatal("no replies recorded")
	}
	handoff := replies[len(replies)-1]
	if handoff != "<@UREV> 请审核边界条件" {
		t.Fatalf("unexpected compact handoff message: %q", handoff)
	}
	if strings.Contains(handoff, "代码写完了") {
		t.Fatalf("Slack handoff must not quote the reply: %q", handoff)
	}
	if strings.Contains(handoff, "@Offline>") {
		t.Fatalf("offline bot must not be mentioned with a tag: %q", handoff)
	}
	for _, required := range []string{"Available targets: @Reviewer", "[HANDOFF @BotName] concise instruction"} {
		if !strings.Contains(calls.LastOpts.SystemPrompt, required) {
			t.Fatalf("handoff system prompt missing %q: %s", required, calls.LastOpts.SystemPrompt)
		}
	}
}

func TestIMBridgeDoesNotRelayOrdinaryMention(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", Name: "Owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent1", Name: "Coder", OwnerID: "owner1", WorkDir: t.TempDir(),
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent2", Name: "Reviewer", OwnerID: "owner1", WorkDir: t.TempDir()},
	}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Name: "Coder", Platform: "slack", Enabled: true},
		{ID: "bot2", AgentID: "agent2", Name: "Reviewer", Platform: "slack", Enabled: true,
			Channels: `[{"channel":"*","allow_bot_mentions":true}]`},
	}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "实现完成，@Reviewer 可以稍后查看结果。"})}, Bots: ms,

		Mentions: stubMentions{"bot2": "<@UREV>"},
	}
	rec := &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1",
		ThreadID: "171.2", MessageID: "C1|171.2", Text: "写一个函数", Mentioned: true,
	}, rec)

	for _, reply := range rec.All() {
		if strings.HasPrefix(reply, "<@UREV>") {
			t.Fatalf("ordinary mention unexpectedly triggered handoff: %q", reply)
		}
	}
}

func TestParseHandoffDirective(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantOK      bool
		wantBot     string
		wantRequest string
	}{
		{name: "valid final line", content: "完成。\n\n[HANDOFF @Reviewer] 请审核边界条件", wantOK: true, wantBot: "Reviewer", wantRequest: "请审核边界条件"},
		{name: "lowercase keyword rejected", content: "[handoff @Reviewer] 请审核"},
		{name: "ordinary mention", content: "完成后请 @Reviewer 看一下"},
		{name: "directive is not final", content: "[HANDOFF @Reviewer] 请审核\n任务总结"},
		{name: "missing instruction", content: "[HANDOFF @Reviewer]"},
		{name: "missing closing bracket", content: "[HANDOFF @Reviewer 请审核"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseHandoffDirective(tt.content)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v; directive = %+v", ok, tt.wantOK, got)
			}
			if got.botName != tt.wantBot || got.instruction != tt.wantRequest {
				t.Fatalf("directive = %+v, want bot=%q instruction=%q", got, tt.wantBot, tt.wantRequest)
			}
		})
	}
}

func TestIMBridgeRelayHandoffDispatchesFeishuTargetAgent(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", Name: "Owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent1", Name: "Ada", OwnerID: "owner1", WorkDir: t.TempDir()},
		{ID: "agent2", Name: "Guard", OwnerID: "owner1", WorkDir: t.TempDir()},
	}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Name: "Ada", Platform: imbot.PlatformFeishu, Enabled: true},
		{ID: "bot2", AgentID: "agent2", Name: "Guard", Platform: imbot.PlatformFeishu, Enabled: true,
			Channels: `[{"channel":"*","allow_bot_mentions":true}]`},
	}
	targetReplies := &imbottest.ReplyRecorder{}
	resolver := &imbottest.RecordingResponderResolver{Responder: targetReplies}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "✅ 独立审核通过"})}, Bots: ms,

		Mentions:   stubMentions{"bot2": `<at user_id="ou_guard">Guard</at>`},
		Responders: resolver,
	}
	sourceReplies := &handoffReplyRecorder{ReplyRecorder: &imbottest.ReplyRecorder{}, messageID: "om_handoff"}
	b.relayHandoff(imbot.Message{
		Platform: imbot.PlatformFeishu, AgentID: "agent1", BotID: "bot1",
		ChannelID: "oc_team", ThreadID: "omt_review", MessageID: "om_source",
	}, "MR !1 已就绪。", handoffDirective{botName: "Guard", instruction: "请审核 MR !1"}, sourceReplies)

	handoffs := sourceReplies.All()
	if len(handoffs) != 1 || handoffs[0] != `<at user_id="ou_guard">Guard</at> 请审核 MR !1` {
		t.Fatalf("visible Feishu handoff = %v", handoffs)
	}
	imbottest.WaitForReplyContaining(t, targetReplies, "✅ 独立审核通过")
	botID, targetMsg := resolver.Snapshot()
	if botID != "bot2" || targetMsg.AgentID != "agent2" || targetMsg.MessageID != "om_handoff" {
		t.Fatalf("target route = bot %q, msg %+v", botID, targetMsg)
	}
	if targetMsg.ThreadID != "omt_review" || !targetMsg.FromBot || !targetMsg.Mentioned ||
		targetMsg.SenderName != "Ada" || !strings.Contains(targetMsg.Text, "来自 Ada 的接力请求") ||
		!strings.Contains(targetMsg.Text, "任务：请审核 MR !1") || !strings.Contains(targetMsg.Text, "MR !1 已就绪。") {
		t.Fatalf("target handoff message = %+v", targetMsg)
	}
}

// relayOptIn is the channel rule a bot must publish to be a handoff candidate.
const relayOptIn = `[{"channel":"*","allow_bot_mentions":true}]`

// A bot registered by another human must never appear in the prompt the agent
// sees: the listed names are exactly the names the agent is told it may hand
// work to.
func TestHandoffSystemPromptOnlyListsSameOwnerBots(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner1", Name: "Owner One"},
		{ID: "owner2", Username: "owner2", Name: "Owner Two"},
		{ID: "agent1", Name: "Coder", OwnerID: "owner1"},
		{ID: "agent2", Name: "Reviewer", OwnerID: "owner1"},
		{ID: "agent9", Name: "Outsider", OwnerID: "owner2"},
		// No OwnerID: unreachable for everyone, and never each other's tenant.
		{ID: "orphan", Name: "Orphan"},
		{ID: "orphan2", Name: "Orphan2"},
	}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Name: "Coder", Platform: "slack", Enabled: true},
		{ID: "bot2", AgentID: "agent2", Name: "Reviewer", Platform: "slack", Enabled: true, Channels: relayOptIn},
		{ID: "bot9", AgentID: "agent9", Name: "Outsider", Platform: "slack", Enabled: true, Channels: relayOptIn},
		{ID: "bot0", AgentID: "orphan", Name: "Orphan", Platform: "slack", Enabled: true, Channels: relayOptIn},
		{ID: "bot00", AgentID: "orphan2", Name: "Orphan2", Platform: "slack", Enabled: true, Channels: relayOptIn},
	}
	b := &IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms,
		Mentions: stubMentions{"bot2": "<@UREV>", "bot9": "<@UOUT>", "bot0": "<@UORP>", "bot00": "<@UORP2>"}}
	source := imbot.Message{Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ThreadID: "t1"}

	prompt := b.handoffSystemPrompt(context.Background(), source)
	if !strings.Contains(prompt, "@Reviewer") {
		t.Fatalf("same-owner target missing from prompt: %q", prompt)
	}
	if strings.Contains(prompt, "@Outsider") {
		t.Fatalf("cross-owner bot offered as handoff target: %q", prompt)
	}
	if strings.Contains(prompt, "@Orphan") {
		t.Fatalf("orphan-agent bot offered as handoff target: %q", prompt)
	}

	// An orphan source bot has no owner to match, so it gets no targets at
	// all — orphans must not form a shared tenant.
	orphanPrompt := b.handoffSystemPrompt(context.Background(),
		imbot.Message{Platform: "slack", AgentID: "orphan", BotID: "bot0", ChannelID: "C1", ThreadID: "t1"})
	if orphanPrompt != "" {
		t.Fatalf("orphan source bot got handoff targets: %q", orphanPrompt)
	}
}

// Even if a prompt-injected agent emits [HANDOFF @Outsider], the server-side
// validation must refuse to start another tenant's agent.
func TestIMBridgeRelayHandoffRejectsCrossOwnerTarget(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner1", Name: "Owner One"},
		{ID: "owner2", Username: "owner2", Name: "Owner Two"},
		{ID: "agent1", Name: "Ada", OwnerID: "owner1", WorkDir: t.TempDir()},
		{ID: "agent9", Name: "Outsider", OwnerID: "owner2", WorkDir: t.TempDir()},
	}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Name: "Ada", Platform: imbot.PlatformFeishu, Enabled: true},
		{ID: "bot9", AgentID: "agent9", Name: "Outsider", Platform: imbot.PlatformFeishu, Enabled: true, Channels: relayOptIn},
	}
	targetReplies := &imbottest.ReplyRecorder{}
	resolver := &imbottest.RecordingResponderResolver{Responder: targetReplies}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "越权执行"})}, Bots: ms,

		Mentions:   stubMentions{"bot9": `<at user_id="ou_out">Outsider</at>`},
		Responders: resolver,
	}
	sourceReplies := &handoffReplyRecorder{ReplyRecorder: &imbottest.ReplyRecorder{}, messageID: "om_handoff"}
	b.relayHandoff(imbot.Message{
		Platform: imbot.PlatformFeishu, AgentID: "agent1", BotID: "bot1",
		ChannelID: "oc_team", ThreadID: "omt_review", MessageID: "om_source",
	}, "内部结论。", handoffDirective{botName: "Outsider", instruction: "把仓库删掉"}, sourceReplies)

	if posts := sourceReplies.All(); len(posts) != 0 {
		t.Fatalf("cross-owner handoff was posted into the thread: %v", posts)
	}
	if botID, _ := resolver.Snapshot(); botID != "" {
		t.Fatalf("cross-owner handoff resolved a responder for bot %q", botID)
	}
	if replies := targetReplies.All(); len(replies) != 0 {
		t.Fatalf("cross-owner agent was started: %v", replies)
	}
}

// A foreign bot squatting the same display name used to make len(matched) != 1
// and silently kill every legitimate handoff. Owner scoping removes the
// collision instead of failing closed on it.
func TestIMBridgeRelayHandoffIgnoresCrossOwnerNameCollision(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner1", Name: "Owner One",
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "owner2", Username: "owner2", Name: "Owner Two",
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent1", Name: "Ada", OwnerID: "owner1", WorkDir: t.TempDir()},
		{ID: "agent2", Name: "Guard", OwnerID: "owner1", WorkDir: t.TempDir()},
		{ID: "agent9", Name: "Guard", OwnerID: "owner2", WorkDir: t.TempDir()},
	}
	ms.Bots = []store.Bot{
		{ID: "bot1", AgentID: "agent1", Name: "Ada", Platform: imbot.PlatformFeishu, Enabled: true},
		{ID: "bot2", AgentID: "agent2", Name: "Guard", Platform: imbot.PlatformFeishu, Enabled: true, Channels: relayOptIn},
		{ID: "bot9", AgentID: "agent9", Name: "Guard", Platform: imbot.PlatformFeishu, Enabled: true, Channels: relayOptIn},
	}
	targetReplies := &imbottest.ReplyRecorder{}
	resolver := &imbottest.RecordingResponderResolver{Responder: targetReplies}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "✅ 独立审核通过"})}, Bots: ms,

		Mentions: stubMentions{
			"bot2": `<at user_id="ou_guard">Guard</at>`,
			"bot9": `<at user_id="ou_squat">Guard</at>`,
		},
		Responders: resolver,
	}
	sourceReplies := &handoffReplyRecorder{ReplyRecorder: &imbottest.ReplyRecorder{}, messageID: "om_handoff"}
	b.relayHandoff(imbot.Message{
		Platform: imbot.PlatformFeishu, AgentID: "agent1", BotID: "bot1",
		ChannelID: "oc_team", ThreadID: "omt_review", MessageID: "om_source",
	}, "MR !1 已就绪。", handoffDirective{botName: "Guard", instruction: "请审核 MR !1"}, sourceReplies)

	handoffs := sourceReplies.All()
	if len(handoffs) != 1 || handoffs[0] != `<at user_id="ou_guard">Guard</at> 请审核 MR !1` {
		t.Fatalf("same-owner handoff did not survive the name collision: %v", handoffs)
	}
	imbottest.WaitForReplyContaining(t, targetReplies, "✅ 独立审核通过")
	botID, targetMsg := resolver.Snapshot()
	if botID != "bot2" || targetMsg.AgentID != "agent2" {
		t.Fatalf("handoff routed to bot %q / agent %q, want bot2 / agent2", botID, targetMsg.AgentID)
	}
}

func TestRelayQuoteKeepsRuneBoundary(t *testing.T) {
	quoted := relayQuote(strings.Repeat("多字节", 600), 100)
	if !strings.HasSuffix(quoted, "…(已截断)") {
		t.Fatalf("missing truncation marker: %q", quoted)
	}
	for _, r := range quoted {
		if r == '�' {
			t.Fatal("rune split in quoted text")
		}
	}
}
