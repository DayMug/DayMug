package imbridge

import (
	"context"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestObservedContextLoaderUsesCursorAndSnapshot(t *testing.T) {
	var cache observedThreadContextCache
	base := imbot.Message{
		Platform: imbot.PlatformTelegram, AgentID: "agent1", BotID: "bot1",
		ChannelID: "group1", ThreadID: "topic1", SenderName: "Alice",
	}
	for _, item := range []struct {
		id, text string
	}{
		{"m1", "root"},
		{"m2", "missed detail"},
	} {
		msg := base
		msg.MessageID = item.id
		msg.Text = item.text
		cache.observe(threadKey(msg), msg)
	}

	current := base
	current.MessageID = "m3"
	current.Text = "handoff"
	snapshot := cache.observe(threadKey(current), current)
	loader := observedContextLoader(snapshot)

	history, err := loader(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].MessageID != "m2" {
		t.Fatalf("history after cursor = %+v, want only m2", history)
	}

	later := base
	later.MessageID = "m4"
	later.Text = "arrived too late"
	cache.observe(threadKey(later), later)
	history, err = loader(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[1].MessageID != "m2" {
		t.Fatalf("snapshot changed after a later message: %+v", history)
	}
}

func TestInternalHandoffsImportUnmentionedContextForBothBots(t *testing.T) {
	for _, platform := range []string{imbot.PlatformFeishu, imbot.PlatformTelegram} {
		t.Run(platform, func(t *testing.T) {
			ms := storetest.New()
			cfg := agenttest.Config()
			ms.Users = []store.User{
				{ID: "owner1", Username: "owner", Name: "Owner",
					ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
				{ID: "agent1", Name: "Coder", OwnerID: "owner1", WorkDir: t.TempDir(),
					ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
				{ID: "agent2", Name: "Reviewer", OwnerID: "owner1", WorkDir: t.TempDir(),
					ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
			}
			const channels = `[{"channel":"*","allow_bot_mentions":true}]`
			ms.Bots = []store.Bot{
				{ID: "bot1", AgentID: "agent1", Name: "Coder", Platform: platform, Enabled: true, Channels: channels},
				{ID: "bot2", AgentID: "agent2", Name: "Reviewer", Platform: platform, Enabled: true, Channels: channels},
			}

			calls := &agenttest.CallRecord{}
			targetReplies := &handoffReplyRecorder{
				ReplyRecorder: &imbottest.ReplyRecorder{},
				messageID:     "handoff-b-to-a",
			}
			resolver := &imbottest.RecordingResponderResolver{Responder: targetReplies}
			bridge := &IMBridge{
				Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{
					Results: []string{
						"审核完成。\n[HANDOFF @Coder] 请结合补充要求收尾",
						"最终完成",
					},
					Rec: calls,
				})}, Bots: ms,

				Mentions:   stubMentions{"bot1": "@Coder", "bot2": "@Reviewer"},
				Responders: resolver,
			}

			for _, bot := range []struct {
				agentID, botID string
			}{
				{"agent1", "bot1"},
				{"agent2", "bot2"},
			} {
				for _, item := range []struct {
					id, text string
				}{
					{"root", "原始任务"},
					{"comment", "补充要求：必须覆盖边界条件"},
				} {
					bridge.HandleMessage(context.Background(), imbot.Message{
						Platform: platform, AgentID: bot.agentID, BotID: bot.botID,
						ChannelID: "team", ThreadID: "topic", MessageID: item.id,
						SenderID: "human1", SenderName: "Alice", Text: item.text,
					}, &imbottest.ReplyRecorder{})
				}
			}

			sourceReplies := &handoffReplyRecorder{
				ReplyRecorder: &imbottest.ReplyRecorder{},
				messageID:     "handoff-a-to-b",
			}
			bridge.relayHandoff(imbot.Message{
				Platform: platform, AgentID: "agent1", BotID: "bot1",
				ChannelID: "team", ThreadID: "topic", MessageID: "root",
			}, "A 已完成实现。", handoffDirective{
				botName: "Reviewer", instruction: "请审核实现",
			}, sourceReplies)

			imbottest.WaitForReplyContaining(t, targetReplies.ReplyRecorder, "最终完成")
			if calls.N != 2 {
				t.Fatalf("backend calls = %d, want B then A", calls.N)
			}
			for i, prompt := range calls.Prompts {
				if !strings.Contains(prompt, "[Alice]: 补充要求：必须覆盖边界条件") {
					t.Fatalf("handoff prompt %d missed unmentioned context:\n%s", i+1, prompt)
				}
			}
		})
	}
}
