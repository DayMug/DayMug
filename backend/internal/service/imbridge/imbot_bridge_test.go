package imbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/prompts"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge/casefile"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

func TestIMBridgeGating(t *testing.T) {
	boolFalse := false
	tests := []struct {
		name              string
		rules             []imbot.ChannelRule
		msg               imbot.Message
		unconfiguredReply string
		unauthorizedReply string
		wantReply         bool
		wantText          string
	}{
		{
			name:              "mention in unconfigured group channel gets configured reply",
			rules:             nil,
			msg:               imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi", Mentioned: true},
			unconfiguredReply: "not enabled here",
			wantReply:         true,
			wantText:          "not enabled here",
		},
		{
			name:              "unmentioned message in unconfigured group stays silent",
			rules:             nil,
			msg:               imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi"},
			unconfiguredReply: "not enabled here",
			wantReply:         false,
		},
		{
			name:      "mention required and absent is silent",
			rules:     []imbot.ChannelRule{{Channel: "C1"}},
			msg:       imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi"},
			wantReply: false,
		},
		{
			name:      "mention required and present reaches the run path",
			rules:     []imbot.ChannelRule{{Channel: "C1"}},
			msg:       imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi", Mentioned: true},
			wantReply: true, // run fails on uninitialised pool → error reply in thread
		},
		{
			name:      "allowed user mention reaches the run path",
			rules:     []imbot.ChannelRule{{Channel: "C1", AllowedUserIDs: []string{"ou_allowed"}}},
			msg:       imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", SenderID: "ou_allowed", Text: "hi", Mentioned: true},
			wantReply: true,
		},
		{
			name:              "unlisted user mention gets configured reply",
			rules:             []imbot.ChannelRule{{Channel: "C1", AllowedUserIDs: []string{"ou_allowed"}}},
			msg:               imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", SenderID: "ou_other", Text: "hi", Mentioned: true},
			unauthorizedReply: "not allowed",
			wantReply:         true,
			wantText:          "not allowed",
		},
		{
			name:              "unlisted unmentioned user stays silent in auto-reply channel",
			rules:             []imbot.ChannelRule{{Channel: "C1", AutoReply: true, AllowedUserIDs: []string{"ou_allowed"}}},
			msg:               imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", SenderID: "ou_other", Text: "hi"},
			unauthorizedReply: "not allowed",
			wantReply:         false,
		},
		{
			name:      "auto-reply channel responds without mention",
			rules:     []imbot.ChannelRule{{Channel: "C1", AutoReply: true}},
			msg:       imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi"},
			wantReply: true,
		},
		{
			name:              "explicitly disabled channel is silent even with mention",
			rules:             []imbot.ChannelRule{{Channel: "C1", Enabled: &boolFalse}},
			msg:               imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi", Mentioned: true},
			unconfiguredReply: "not enabled here",
			wantReply:         false,
		},
		{
			name:      "DM without a dm rule is silent",
			rules:     nil,
			msg:       imbot.Message{Platform: "feishu", ChannelID: "oc_dm", ThreadID: "t", MessageID: "m", Text: "hi", IsDM: true},
			wantReply: false,
		},
		{
			name:              "DM without a dm rule gets configured reply",
			rules:             nil,
			msg:               imbot.Message{Platform: "feishu", ChannelID: "oc_dm", ThreadID: "t", MessageID: "m", Text: "hi", IsDM: true},
			unconfiguredReply: "not enabled here",
			wantReply:         true,
			wantText:          "not enabled here",
		},
		{
			name:      "DM answers once a dm rule enables it",
			rules:     []imbot.ChannelRule{{Channel: "dm"}},
			msg:       imbot.Message{Platform: "feishu", ChannelID: "oc_dm", ThreadID: "t", MessageID: "m", Text: "hi", IsDM: true},
			wantReply: true,
		},
		{
			name:              "dm rule still honours allowed_user_ids with configured reply",
			rules:             []imbot.ChannelRule{{Channel: "dm", AllowedUserIDs: []string{"ou_owner"}}},
			msg:               imbot.Message{Platform: "feishu", ChannelID: "oc_dm", ThreadID: "t", MessageID: "m", Text: "hi", IsDM: true, SenderID: "ou_stranger"},
			unauthorizedReply: "not allowed",
			wantReply:         true,
			wantText:          "not allowed",
		},
		{
			name:              "bot-authored unconfigured mention stays silent",
			rules:             nil,
			msg:               imbot.Message{Platform: "feishu", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi", Mentioned: true, FromBot: true},
			unconfiguredReply: "not enabled here",
			wantReply:         false,
		},
		{
			name:      "legacy system Slack rules are ignored",
			rules:     []imbot.ChannelRule{{Platform: "slack", Channel: "C1", AutoReply: true}},
			msg:       imbot.Message{Platform: "slack", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi"},
			wantReply: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := storetest.New()
			if tt.name != "legacy system Slack rules are ignored" {
				tt.msg.AgentID, tt.msg.BotID = "agent1", "bot1"
				raw, err := json.Marshal(tt.rules)
				if err != nil {
					t.Fatal(err)
				}
				ms.Users = []store.User{{ID: "agent1", OwnerID: "owner1"}}
				ms.Bots = []store.Bot{{
					ID: "bot1", AgentID: "agent1", Platform: tt.msg.Platform, Enabled: true, Channels: string(raw),
					UnconfiguredReply: tt.unconfiguredReply, UnauthorizedReply: tt.unauthorizedReply,
				}}
			}
			b := &IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms} // no Pool/Cfg: reaching run() yields an error reply
			rec := &imbottest.ReplyRecorder{}
			b.HandleMessage(context.Background(), tt.msg, rec)
			replies := rec.All()
			if tt.wantReply && len(replies) == 0 {
				t.Fatal("expected a reply, got none")
			}
			if !tt.wantReply && len(replies) != 0 {
				t.Fatalf("expected silence, got %v", replies)
			}
			if tt.wantText != "" && (len(replies) != 1 || replies[0] != tt.wantText) {
				t.Fatalf("reply = %v, want %q", replies, tt.wantText)
			}
		})
	}
}

func TestIMBridgeBotSlackGating(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent1", Name: "Ops", OwnerID: "owner1"}}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Name: "Personal Slack", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"C1","auto_reply":true}]`}}
	b := &IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms}
	rec := &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ThreadID: "t", MessageID: "m", Text: "hi",
	}, rec)
	if len(rec.All()) == 0 {
		t.Fatal("agent-owned Slack rule should reach the run path")
	}

	rec = &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C2", ThreadID: "t", MessageID: "m2", Text: "hi",
	}, rec)
	if len(rec.All()) != 0 {
		t.Fatalf("unconfigured Slack channel should stay silent: %v", rec.All())
	}
}

func TestIMBridgeExistingThreadStillRequiresMention(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent1", OwnerID: "owner1"}}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"C1","require_mention":true}]`}}
	ms.BotThreads = map[string]store.BotThread{
		"slack@agent1@bot1|C1|thread1": {Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "thread1"},
	}
	rec := &imbottest.ReplyRecorder{}
	(&IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms}).HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ThreadID: "thread1",
		MessageID: "m2", Text: "follow up without mention",
	}, rec)
	if len(rec.All()) != 0 {
		t.Fatalf("existing thread follow-up without mention should stay silent: %v", rec.All())
	}
}

func TestIMBridgeMirrorsThreadIntoConversation(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}}, agentUser}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true, Channels: `[{"channel":"*","auto_reply":true}]`}}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "deployed"})}, Bots: ms,
	}
	rec := &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ChannelName: "ops",
		ThreadID: "171.1", MessageID: "C1|171.1", SenderID: "U_ALICE", SenderName: "Alice", Text: "deploy", Mentioned: true,
	}, rec)
	if len(ms.Conversations) != 1 || ms.Conversations[0].UserID != "agent1" {
		t.Fatalf("conversation not created for agent: %+v", ms.Conversations)
	}
	if ms.Conversations[0].Title != "Slack·ops" {
		t.Fatalf("initial conversation title = %q, want platform/channel fallback", ms.Conversations[0].Title)
	}
	convID := ms.Conversations[0].ID
	messages := ms.Messages[convID]
	if len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != "[Alice]: deploy" || messages[1].Role != "assistant" || messages[1].Content != "deployed" {
		t.Fatalf("mirrored messages = %+v", messages)
	}
	var metadata IMMessageMetadata
	if err := json.Unmarshal(messages[0].Metadata, &metadata); err != nil {
		t.Fatalf("decode inbound metadata: %v", err)
	}
	if metadata.Sender == nil || metadata.Sender.Platform != "slack" || metadata.Sender.ID != "U_ALICE" || metadata.Sender.Name != "Alice" {
		t.Fatalf("inbound sender metadata = %+v, want Slack Alice", metadata.Sender)
	}
	thread := ms.BotThreads["slack@agent1@bot1|C1|171.1"]
	if thread.ConversationID != convID {
		t.Fatalf("thread conversation id = %q, want %q", thread.ConversationID, convID)
	}
	replies := rec.All()
	if len(replies) < 2 || replies[len(replies)-1] != "deployed" {
		t.Fatalf("progress/final replies = %v", replies)
	}
}

func TestIMBridgeStartsNewConversationAfterBotWindowExpires(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}}, agentUser}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		MaxConversationDuration: "3h",
		Channels:                `[{"channel":"*","auto_reply":true}]`,
	}}
	const (
		oldConversationID = "old-conversation"
		oldSessionID      = "old-cli-session"
	)
	ms.Conversations = []store.Conversation{{
		ID: oldConversationID, UserID: agentUser.ID, WorkDir: agentUser.WorkDir,
		SessionID: oldSessionID, Provider: config.CLITypeClaude, Model: agenttest.Model,
		CreatedAt: time.Now().Add(-4 * time.Hour),
	}}
	threadKey := "slack@agent1@bot1|C1|171.1"
	ms.BotThreads = make(map[string]store.BotThread)
	ms.BotThreads[threadKey] = store.BotThread{
		Platform: "slack@agent1@bot1", ChannelID: "C1", ThreadID: "171.1",
		AgentID: agentUser.ID, ConversationID: oldConversationID, SessionID: oldSessionID,
		Provider: config.CLITypeClaude, Model: agenttest.Model, LastMessageID: "C1|171.0",
	}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "fresh answer"})}, Bots: ms,
	}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ChannelName: "ops",
		ThreadID: "171.1", MessageID: "C1|171.2", SenderName: "Alice", Text: "new task",
	}, &imbottest.ReplyRecorder{})

	if len(ms.Conversations) != 2 {
		t.Fatalf("conversations = %+v, want old plus one fresh conversation", ms.Conversations)
	}
	fresh := ms.Conversations[1]
	if fresh.ID == oldConversationID || fresh.SessionID == oldSessionID {
		t.Fatalf("fresh conversation reused old identity: %+v", fresh)
	}
	thread := ms.BotThreads[threadKey]
	if thread.ConversationID != fresh.ID || thread.SessionID != fresh.SessionID {
		t.Fatalf("thread binding = %+v, want fresh conversation/session", thread)
	}
	if got := ms.Messages[fresh.ID]; len(got) != 2 || got[0].Content != "[Alice]: new task" {
		t.Fatalf("fresh conversation messages = %+v", got)
	}
	if got := ms.Messages[oldConversationID]; len(got) != 0 {
		t.Fatalf("expired conversation received new messages: %+v", got)
	}
}

type failOnceSessionBackend struct {
	sessionID string
	opts      []agent.RunRequest
}

func (b *failOnceSessionBackend) Name() string { return "fail-once-session" }

// Capabilities reports what the codex provider this stands in for does: it
// picks its own session ids, so the first run carries none.
func (b *failOnceSessionBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{AssignsSessionID: true}
}
func (b *failOnceSessionBackend) RunWithSession(_ context.Context, _, _ string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.opts = append(b.opts, opts)
	if len(b.opts) == 1 {
		ch <- agent.StreamEvent{Kind: agent.KindSystemInit, Content: `{"session_id":"` + b.sessionID + `"}`}
		close(ch)
		return errors.New("signal: terminated")
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "continued"}
	close(ch)
	return nil
}
func (*failOnceSessionBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (b *failOnceSessionBackend) SessionExists(_, sessionID, _ string) bool {
	return sessionID == b.sessionID
}
func (*failOnceSessionBackend) SessionLogPath(string, string, string) string { return "" }

type capturingIMBackend struct {
	opts agent.RunRequest
}

func (*capturingIMBackend) Name() string { return "capturing-im" }
func (*capturingIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}
func (b *capturingIMBackend) RunWithSession(_ context.Context, _, _ string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.opts = opts
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "done"}
	close(ch)
	return nil
}
func (*capturingIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*capturingIMBackend) SessionExists(string, string, string) bool { return false }
func (*capturingIMBackend) SessionLogPath(string, string, string) string {
	return ""
}

// useAccountModels registers the models each account serves for one test.
// Model lists used to ride on config.Provider; they now come from the
// process-global, database-backed registry, so each test installs its own and
// the previous one is restored afterwards to keep cases independent.
func useAccountModels(t *testing.T, models map[string][]string) {
	t.Helper()
	prev := service.ModelOverrides()
	next := make(map[string]service.AccountModels, len(models))
	for account, list := range models {
		next[account] = service.AccountModels{Models: list}
	}
	service.SetModelOverrides(next)
	t.Cleanup(func() { service.SetModelOverrides(prev) })
}

func TestIMBridgeInheritsOwnerEnvironment(t *testing.T) {
	const model = "gpt-test"
	useAccountModels(t, map[string][]string{"codex1": {model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "codex1", Type: config.CLITypeCodex, MaxConcurrent: 1,
	}}}
	ms := storetest.New()
	ms.Users = []store.User{
		{
			ID: "owner1", Username: "owner", Name: "Owner", Email: "owner@example.com",
			Env:              "GLAB_CONFIG_DIR=/home/owner/glab",
			ProviderBindings: map[string]string{config.CLITypeCodex: "codex1"},
		},
		{
			ID: "agent1", Name: "Ops", OwnerID: "owner1", Email: "owner@example.com",
			WorkDir: t.TempDir(), DefaultModel: model,
			ProviderBindings: map[string]string{config.CLITypeCodex: "codex1"},
		},
	}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"*","require_mention":true}]`,
	}}
	backend := &capturingIMBackend{}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(map[string]agent.Backend{config.CLITypeCodex: backend}, nil)}, Bots: ms,
	}

	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ThreadID: "171.1",
		MessageID: "C1|171.1", SenderName: "Alice", Text: "check env", Mentioned: true,
	}, &imbottest.ReplyRecorder{})

	if got := backend.opts.AccountEnv["GLAB_CONFIG_DIR"]; got != "/home/owner/glab" {
		t.Fatalf("GLAB_CONFIG_DIR = %q, want owner environment", got)
	}
}

func TestIMBridgeResumesCapturedSessionAfterFailedRun(t *testing.T) {
	const (
		model     = "gpt-test"
		sessionID = "codex-session-1"
	)
	useAccountModels(t, map[string][]string{"codex1": {model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "codex1", Type: config.CLITypeCodex, MaxConcurrent: 2,
	}}}
	ms := storetest.New()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", Name: "Owner", ProviderBindings: map[string]string{config.CLITypeCodex: "codex1"}},
		{
			ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
			DefaultModel: model, ProviderBindings: map[string]string{config.CLITypeCodex: "codex1"},
		},
	}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"*","require_mention":true}]`,
	}}
	backend := &failOnceSessionBackend{sessionID: sessionID}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(map[string]agent.Backend{config.CLITypeCodex: backend}, nil)}, Bots: ms,
	}
	msg := imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ThreadID: "171.1",
		MessageID: "C1|171.1", SenderName: "Alice", Text: "start the task", Mentioned: true,
	}
	b.HandleMessage(context.Background(), msg, &imbottest.ReplyRecorder{})

	if len(ms.Conversations) != 1 || ms.Conversations[0].SessionID != sessionID {
		t.Fatalf("conversation session after failed run = %+v, want %q", ms.Conversations, sessionID)
	}
	threadKey := "slack@agent1@bot1|C1|171.1"
	if got := ms.BotThreads[threadKey].SessionID; got != sessionID {
		t.Fatalf("thread session after failed run = %q, want %q", got, sessionID)
	}

	msg.MessageID = "C1|171.2"
	msg.Text = "continue"
	replies := &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), msg, replies)

	if len(backend.opts) != 2 {
		t.Fatalf("backend calls = %d, want 2", len(backend.opts))
	}
	if got := backend.opts[1]; got.SessionID != sessionID || !got.IsResume {
		t.Fatalf("follow-up opts = %+v, want resumed session %q", got, sessionID)
	}
	if got := replies.All(); len(got) == 0 || got[len(got)-1] != "continued" {
		t.Fatalf("follow-up replies = %v, want continued result", got)
	}
}

func TestIMBridgeBackfillsUnmentionedSlackMessagesOnNextMention(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}, agentUser}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"*","require_mention":true}]`,
	}}
	rec := &agenttest.CallRecord{}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "new answer", Rec: rec})}, Bots: ms,
	}
	loadCalls := 0
	msg := imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1",
		ThreadID: "171.1", MessageID: "C1|171.3", SenderName: "Carol", Text: "current question", Mentioned: true,
		LoadThreadMessages: func(_ context.Context, afterMessageID string) ([]imbot.Message, error) {
			loadCalls++
			switch afterMessageID {
			case "":
				return []imbot.Message{
					{Platform: "slack", MessageID: "C1|171.1", SenderName: "Alice", Text: "root question"},
					{Platform: "slack", MessageID: "C1|171.2", SenderName: "Helper", Text: "earlier answer", FromBot: true},
				}, nil
			case "C1|171.3":
				return []imbot.Message{
					// Slack includes the parent at the front of every
					// conversations.replies response. The bridge-level
					// source id guard must still make a repeated platform
					// message harmless if a connector returns it again.
					{Platform: "slack", MessageID: "C1|171.1", SenderName: "Alice", Text: "root question"},
					{Platform: "slack", MessageID: "C1|171.4", SenderName: "Dave", Text: "missed detail"},
				}, nil
			default:
				t.Fatalf("unexpected history cursor %q", afterMessageID)
				return nil, nil
			}
		},
	}
	b.HandleMessage(context.Background(), msg, &imbottest.ReplyRecorder{})

	if loadCalls != 1 {
		t.Fatalf("history loader calls = %d, want 1", loadCalls)
	}
	convID := ms.Conversations[0].ID
	messages := ms.Messages[convID]
	if len(messages) != 4 {
		t.Fatalf("mirrored messages = %+v, want history + trigger + answer", messages)
	}
	want := []struct{ role, content string }{
		{"user", "[Alice]: root question"},
		{"assistant", "[Helper]: earlier answer"},
		{"user", "[Carol]: current question"},
		{"assistant", "new answer"},
	}
	for i, expected := range want {
		if messages[i].Role != expected.role || messages[i].Content != expected.content {
			t.Fatalf("message[%d] = %+v, want role=%q content=%q", i, messages[i], expected.role, expected.content)
		}
	}

	msg.MessageID = "C1|171.4"
	msg.SenderName = "Dave"
	msg.Text = "missed detail"
	msg.Mentioned = false
	silent := &imbottest.ReplyRecorder{}
	b.HandleMessage(context.Background(), msg, silent)
	if loadCalls != 1 {
		t.Fatalf("unmentioned message loaded history; calls = %d", loadCalls)
	}
	if len(silent.All()) != 0 {
		t.Fatalf("unmentioned message received a reply: %v", silent.All())
	}

	msg.MessageID = "C1|171.5"
	msg.SenderName = "Carol"
	msg.Text = "answer with that detail"
	msg.Mentioned = true
	b.HandleMessage(context.Background(), msg, &imbottest.ReplyRecorder{})
	if loadCalls != 2 {
		t.Fatalf("next mention did not backfill history; calls = %d", loadCalls)
	}
	messages = ms.Messages[convID]
	want = append(want,
		struct{ role, content string }{"user", "[Dave]: missed detail"},
		struct{ role, content string }{"user", "[Carol]: answer with that detail"},
		struct{ role, content string }{"assistant", "new answer"},
	)
	if len(messages) != len(want) {
		t.Fatalf("mirrored messages = %+v, want %d complete messages", messages, len(want))
	}
	for i, expected := range want {
		if messages[i].Role != expected.role || messages[i].Content != expected.content {
			t.Fatalf("message[%d] = %+v, want role=%q content=%q", i, messages[i], expected.role, expected.content)
		}
	}
	thread := ms.BotThreads["slack@agent1@bot1|C1|171.1"]
	if thread.LastMessageID != "C1|171.5" {
		t.Fatalf("thread cursor = %q, want latest mentioned message", thread.LastMessageID)
	}
	if len(rec.Prompts) != 2 {
		t.Fatalf("agent prompts = %v, want one per mention", rec.Prompts)
	}
	firstPrompt := "Earlier messages in this Slack thread, oldest first:\n" +
		"[Alice]: root question\n" +
		"[Helper]: earlier answer\n\n" +
		"Current message:\n[Carol]: current question"
	if rec.Prompts[0] != firstPrompt {
		t.Fatalf("first agent prompt = %q, want thread context %q", rec.Prompts[0], firstPrompt)
	}
	secondPrompt := "Earlier messages in this Slack thread, oldest first:\n" +
		"[Dave]: missed detail\n\n" +
		"Current message:\n[Carol]: answer with that detail"
	if rec.Prompts[1] != secondPrompt {
		t.Fatalf("second agent prompt = %q, want missed thread context %q", rec.Prompts[1], secondPrompt)
	}
}

func TestPersistThreadContextDeduplicatesPlatformMessageIDs(t *testing.T) {
	tests := []struct {
		platform  string
		messageID string
	}{
		{platform: imbot.PlatformSlack, messageID: "C1|171.100"},
		{platform: imbot.PlatformFeishu, messageID: "om_100"},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			ms := storetest.New()
			bridge := &IMBridge{Runtime: &service.Runtime{Store: ms}}
			conv := store.Conversation{ID: "conv", WorkDir: t.TempDir()}
			agentUser := store.User{ID: "agent", WorkDir: conv.WorkDir}
			inbound := imbot.Message{
				Platform: tt.platform,
				LoadThreadMessages: func(context.Context, string) ([]imbot.Message, error) {
					return []imbot.Message{{
						Platform:   tt.platform,
						MessageID:  tt.messageID,
						SenderName: "Alice",
						Text:       "same platform message",
					}}, nil
				},
			}

			first, err := bridge.persistThreadContext(context.Background(), inbound, conv, agentUser, "", newInboundAttachmentBudget())
			if err != nil {
				t.Fatalf("first backfill: %v", err)
			}
			second, err := bridge.persistThreadContext(context.Background(), inbound, conv, agentUser, "", newInboundAttachmentBudget())
			if err != nil {
				t.Fatalf("repeated backfill: %v", err)
			}
			if len(first) != 1 || len(second) != 0 {
				t.Fatalf("backfill lengths = %d, %d; want 1, 0", len(first), len(second))
			}
			if got := len(ms.Messages[conv.ID]); got != 1 {
				t.Fatalf("persisted messages = %d, want 1", got)
			}
		})
	}
}

func TestIMBridgeNamesConversationFromMessages(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}, agentUser}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"*","auto_reply":true}]`,
	}}
	pool := service.NewPool(cfg)
	titler := &agenttest.FakeTitler{Out: "Deploy service"}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: pool, Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "deployed"}), TitleGen: titler}, Bots: ms,
	}

	b.HandleMessage(context.Background(), imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ChannelName: "ops",
		ThreadID: "171.1", MessageID: "C1|171.1", SenderName: "Alice", Text: "deploy", Mentioned: true,
	}, &imbottest.ReplyRecorder{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ms.ConversationTitle(0) == "Slack·Deploy service" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := ms.ConversationTitle(0); got != "Slack·Deploy service" {
		t.Fatalf("conversation title = %q, want platform-prefixed generated title", got)
	}
	if titler.CallCount() != 1 {
		t.Fatalf("title generator calls = %d, want 1", titler.CallCount())
	}
	if got := titler.GotInput[0]; len(got) != 1 || got[0] != "[Alice]: deploy" {
		t.Fatalf("title input = %v, want persisted user message", got)
	}
}

type blockingIMBackend struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (blockingIMBackend) Name() string { return "blocking-im" }
func (blockingIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}
func (b blockingIMBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.started <- struct{}{}
	select {
	case <-b.release:
		ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "done"}
		close(ch)
		return nil
	case <-ctx.Done():
		close(ch)
		return ctx.Err()
	}
}
func (blockingIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "done", nil
}
func (blockingIMBackend) SessionExists(string, string, string) bool    { return false }
func (blockingIMBackend) SessionLogPath(string, string, string) string { return "" }

type noisyCancelIMBackend struct {
	started chan<- struct{}
}

func (noisyCancelIMBackend) Name() string { return "noisy-cancel-im" }
func (noisyCancelIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}
func (b noisyCancelIMBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.started <- struct{}{}
	<-ctx.Done()
	close(ch)
	return errors.New("signal: terminated: backend command failed")
}
func (noisyCancelIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (noisyCancelIMBackend) SessionExists(string, string, string) bool    { return false }
func (noisyCancelIMBackend) SessionLogPath(string, string, string) string { return "" }

type thinkingIMBackend struct{}

func (thinkingIMBackend) Name() string { return "thinking-im" }
func (thinkingIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinkingStream: true}
}
func (thinkingIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "先检查部署状态"}
	ch <- agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "，再核对发布记录"}
	ch <- agent.StreamEvent{Kind: agent.KindToolUseStart, Content: "Read"}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "部署完成"}
	close(ch)
	return nil
}
func (thinkingIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "部署完成", nil
}
func (thinkingIMBackend) SessionExists(string, string, string) bool    { return false }
func (thinkingIMBackend) SessionLogPath(string, string, string) string { return "" }

type streamingReplyIMBackend struct{}

func (streamingReplyIMBackend) Name() string { return "streaming-reply-im" }
func (streamingReplyIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}
func (streamingReplyIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindDelta, Content: "合并结果已无 Git 冲突"}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "合并结果已无 Git 冲突"}
	close(ch)
	return nil
}
func (streamingReplyIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "合并结果已无 Git 冲突", nil
}
func (streamingReplyIMBackend) SessionExists(string, string, string) bool    { return false }
func (streamingReplyIMBackend) SessionLogPath(string, string, string) string { return "" }

type replyThenToolIMBackend struct{}

func (replyThenToolIMBackend) Name() string { return "reply-then-tool-im" }
func (replyThenToolIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinkingStream: true}
}
func (replyThenToolIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "先检查部署状态"}
	ch <- agent.StreamEvent{Kind: agent.KindDelta, Content: "生产流水线已成功，正在核验线上资源。"}
	ch <- agent.StreamEvent{Kind: agent.KindToolUseStart, Content: "Bash"}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "生产流水线已成功，正在核验线上资源。"}
	close(ch)
	return nil
}
func (replyThenToolIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "生产流水线已成功，正在核验线上资源。", nil
}
func (replyThenToolIMBackend) SessionExists(string, string, string) bool    { return false }
func (replyThenToolIMBackend) SessionLogPath(string, string, string) string { return "" }

type checkpointThinkingIMBackend struct {
	release <-chan struct{}
}

func (checkpointThinkingIMBackend) Name() string { return "checkpoint-thinking-im" }
func (checkpointThinkingIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinkingStream: true}
}
func (b checkpointThinkingIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	ch <- agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "完整的前段思考"}
	ch <- agent.StreamEvent{Kind: agent.KindToolResult, Content: `{"name":"Read","id":"tool-1","input":{"file":"deploy.log"}}`}
	ch <- agent.StreamEvent{Kind: agent.KindContextUsage, Content: `{"used":1}`}
	<-b.release
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "部署完成"}
	close(ch)
	return nil
}
func (checkpointThinkingIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "部署完成", nil
}
func (checkpointThinkingIMBackend) SessionExists(string, string, string) bool    { return false }
func (checkpointThinkingIMBackend) SessionLogPath(string, string, string) string { return "" }

func waitForIMBroadcast(t *testing.T, ch <-chan []byte, eventType string) service.ServerMessage {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case data := <-ch:
			var msg service.ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("decode broadcast: %v", err)
			}
			if msg.Type == eventType {
				return msg
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s broadcast", eventType)
		}
	}
}

func TestIMBridgeStreamsThinkingContentToProgressMessage(t *testing.T) {
	for _, platform := range []string{"slack", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			ms := storetest.New()
			recorder := &imbottest.ReplyRecorder{}
			bridge := &IMBridge{Runtime: &service.Runtime{Store: ms}}

			result, _, _, err := bridge.runAndStream(
				context.Background(), thinkingIMBackend{}, "prompt", t.TempDir(), "agent", "account", agent.RunRequest{},
				"conversation", imbot.Message{Platform: platform, ChannelID: "channel", ThreadID: "thread"}, recorder,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result != "部署完成" {
				t.Fatalf("result = %q, want final answer", result)
			}
			imbottest.WaitForReplyContaining(t, recorder, "💭 正在思考…\n\n先检查部署状态")
			imbottest.WaitForReplyContaining(t, recorder, "🛠️ 正在调用工具…\n\n先检查部署状态，再核对发布记录")
		})
	}
}

func TestIMBridgeStreamsReplyContentToProgressMessage(t *testing.T) {
	for _, platform := range []string{"slack", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			recorder := &imbottest.ReplyRecorder{}
			bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New()}}

			result, _, _, err := bridge.runAndStream(
				context.Background(), streamingReplyIMBackend{}, "prompt", t.TempDir(), "agent", "account", agent.RunRequest{},
				"conversation", imbot.Message{Platform: platform, ChannelID: "channel", ThreadID: "thread"}, recorder,
			)
			if err != nil {
				t.Fatal(err)
			}
			if result != "合并结果已无 Git 冲突" {
				t.Fatalf("result = %q, want final answer", result)
			}
			imbottest.WaitForReplyContaining(t, recorder, "🤖 正在生成回复…\n\n合并结果已无 Git 冲突")
		})
	}
}

func TestIMBridgeKeepsReplyPreviewDuringLaterPhases(t *testing.T) {
	for _, platform := range []string{"slack", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			recorder := &imbottest.ReplyRecorder{}
			bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New()}}

			if _, _, _, err := bridge.runAndStream(
				context.Background(), replyThenToolIMBackend{}, "prompt", t.TempDir(), "agent", "account", agent.RunRequest{},
				"conversation", imbot.Message{Platform: platform, ChannelID: "channel", ThreadID: "thread"}, recorder,
			); err != nil {
				t.Fatal(err)
			}

			texts := recorder.All()
			replyStarted := false
			for _, text := range texts {
				if strings.Contains(text, "生产流水线已成功") {
					replyStarted = true
					continue
				}
				if replyStarted {
					t.Fatalf("reply preview disappeared from a later progress update: %q; all updates: %v", text, texts)
				}
			}
			if !replyStarted {
				t.Fatalf("reply preview was never shown; updates: %v", texts)
			}
			imbottest.WaitForReplyContaining(t, recorder, "🛠️ 正在调用工具…\n\n【推理】\n先检查部署状态\n\n【回复】\n生产流水线已成功，正在核验线上资源。")
			imbottest.WaitForReplyContaining(t, recorder, "🤖 正在整理回复…\n\n【推理】\n先检查部署状态\n\n【回复】\n生产流水线已成功，正在核验线上资源。")
		})
	}
}

func TestIMBridgeDoesNotExposeCanceledBackendError(t *testing.T) {
	const conversationID = "conversation"
	ms := storetest.New()
	broadcaster := service.NewBroadcaster()
	events := make(chan []byte, 16)
	broadcaster.Join(conversationID, "observer", events)
	bridge := &IMBridge{Runtime: &service.Runtime{Store: ms, Broadcaster: broadcaster}}
	started := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	workDir := t.TempDir()

	go func() {
		_, _, _, err := bridge.runAndStream(
			ctx, noisyCancelIMBackend{started: started}, "prompt", workDir, "agent", "account", agent.RunRequest{},
			conversationID, imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"}, &imbottest.ReplyRecorder{},
		)
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "signal: terminated") {
		t.Fatalf("run error = %v, want backend cancellation error returned to the caller", err)
	}
	for _, msg := range ms.SnapshotMessages(conversationID) {
		if msg.Role == "error" {
			t.Fatalf("canceled backend error was persisted: %+v", msg)
		}
	}
	for len(events) > 0 {
		var msg service.ServerMessage
		if err := json.Unmarshal(<-events, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "error" {
			t.Fatalf("canceled backend error was broadcast: %+v", msg)
		}
	}
}

func TestIMBridgeLateJoinReplaysFullThinkingAfterToolCheckpoint(t *testing.T) {
	for _, platform := range []string{"slack", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			const conversationID = "conversation"
			ms := storetest.New()
			broadcaster := service.NewBroadcaster()
			observer := make(chan []byte, 16)
			broadcaster.Join(conversationID, "observer", observer)
			bridge := &IMBridge{Runtime: &service.Runtime{Store: ms, Broadcaster: broadcaster}}
			release := make(chan struct{})
			done := make(chan error, 1)
			workDir := t.TempDir()
			go func() {
				_, _, _, err := bridge.runAndStream(
					context.Background(), checkpointThinkingIMBackend{release: release}, "prompt", workDir, "agent", "account", agent.RunRequest{},
					conversationID, imbot.Message{Platform: platform, ChannelID: "channel", ThreadID: "thread"}, &imbottest.ReplyRecorder{},
				)
				done <- err
			}()

			waitForIMBroadcast(t, observer, "context_usage")
			lateJoiner := make(chan []byte, 16)
			broadcaster.Join(conversationID, "late", lateJoiner)

			var thinking service.ServerMessage
			replayed := len(lateJoiner)
			for i := 0; i < replayed; i++ {
				var msg service.ServerMessage
				if err := json.Unmarshal(<-lateJoiner, &msg); err != nil {
					t.Fatal(err)
				}
				if msg.Type == "thinking_delta" {
					thinking = msg
				}
			}
			if thinking.Content != "完整的前段思考" || !thinking.Replay {
				t.Fatalf("late-join thinking = %#v, want complete replay snapshot", thinking)
			}

			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIMBridgeResultCarriesPersistedThinkingID(t *testing.T) {
	const conversationID = "conversation"
	ms := storetest.New()
	broadcaster := service.NewBroadcaster()
	events := make(chan []byte, 16)
	broadcaster.Join(conversationID, "observer", events)
	bridge := &IMBridge{Runtime: &service.Runtime{Store: ms, Broadcaster: broadcaster}}

	if _, _, _, err := bridge.runAndStream(
		context.Background(), thinkingIMBackend{}, "prompt", t.TempDir(), "agent", "account", agent.RunRequest{},
		conversationID, imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"}, &imbottest.ReplyRecorder{},
	); err != nil {
		t.Fatal(err)
	}
	result := waitForIMBroadcast(t, events, "result")
	if result.ThinkingMessageID == "" {
		t.Fatal("result omitted persisted thinking message id")
	}

	ms.QueueMu.Lock()
	defer ms.QueueMu.Unlock()
	for _, msg := range ms.Messages[conversationID] {
		if msg.Role == "thinking" {
			if result.ThinkingMessageID != msg.ID {
				t.Fatalf("result thinking id = %q, persisted id = %q", result.ThinkingMessageID, msg.ID)
			}
			return
		}
	}
	t.Fatal("thinking message was not persisted")
}

func TestIMThinkingProgressTextKeepsLatestContentWithinPlatformLimit(t *testing.T) {
	content := strings.Repeat("前", imProgressPreviewLimit) + "最新思考"
	got := imProgressText(thinkingStatusText, imProgressBody(content, ""))
	if !strings.HasPrefix(got, "💭 正在思考…\n\n…") {
		t.Fatalf("truncated progress lacks heading/ellipsis: %q", got[:64])
	}
	if !strings.HasSuffix(got, "最新思考") {
		t.Fatal("truncated progress did not retain the latest thinking content")
	}
}

func TestIMReplyProgressTextKeepsLatestUnicodeContentWithinPlatformLimit(t *testing.T) {
	content := strings.Repeat("前", imReplyPreviewLimit) + "最终中文回复"
	got := imProgressText(replyStatusText, imProgressBody("", content))
	if !strings.HasPrefix(got, "🤖 正在生成回复…\n\n…") {
		t.Fatalf("truncated progress lacks heading/ellipsis: %q", got[:64])
	}
	if !strings.HasSuffix(got, "最终中文回复") {
		t.Fatal("truncated progress did not retain the latest reply content")
	}
}

func TestIMProgressBodyStacksReasoningAboveReply(t *testing.T) {
	got := imProgressBody("接着核验线上资源", "生产流水线已成功。")
	want := "【推理】\n接着核验线上资源\n\n【回复】\n生产流水线已成功。"
	if got != want {
		t.Fatalf("progress body = %q, want reasoning above reply", got)
	}
}

// Both sections share one rune budget so the message stays inside a single
// platform update; the reply takes its share first and the reasoning window is
// whatever is left.
func TestIMProgressBodyKeepsBothSectionsWithinPlatformLimit(t *testing.T) {
	got := imProgressBody(strings.Repeat("思", 9000), strings.Repeat("答", 9000))
	if n := len([]rune(got)); n > imProgressPreviewLimit+len([]rune(thinkingSectionLabel+replySectionLabel))+8 {
		t.Fatalf("progress body = %d runes, want within the shared preview budget", n)
	}
	if !strings.Contains(got, thinkingSectionLabel) || !strings.Contains(got, replySectionLabel) {
		t.Fatalf("progress body dropped a section under the budget: %q", got[:64])
	}
}

// A turn that resumes thinking after it has already said something must not
// swap the reply out of the progress message: the two streams alternating in
// place is what the reader perceives as the bot rewriting itself.
func TestIMProgressKeepsReplyPreviewWhenThinkingResumes(t *testing.T) {
	recorder := &imbottest.ReplyRecorder{}
	progress := &imProgress{bridge: &IMBridge{Runtime: &service.Runtime{}}, msg: imbot.Message{Platform: "slack"}, responder: recorder}

	progress.observe(service.AgentStreamFrame{
		Event: agent.StreamEvent{Kind: agent.KindDelta, Content: "生产流水线已成功。"},
		Reply: "生产流水线已成功。",
	})
	// The throttle would otherwise swallow the thinking frame outright, and the
	// regression only shows up on an update that actually reaches the platform.
	progress.lastUpdate = time.Now().Add(-imThinkingUpdateInterval)
	progress.observe(service.AgentStreamFrame{
		Event:    agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "接着核验线上资源"},
		Reply:    "生产流水线已成功。",
		Thinking: "接着核验线上资源",
	})

	texts := recorder.All()
	if len(texts) != 2 {
		t.Fatalf("progress updates = %v, want one per observed frame", texts)
	}
	want := thinkingStatusText + "\n\n【推理】\n接着核验线上资源\n\n【回复】\n生产流水线已成功。"
	if got := texts[1]; got != want {
		t.Fatalf("thinking update = %q, want both streams under the thinking banner", got)
	}
}

// The streamer resets its thinking buffer whenever it persists a result, so a
// turn with several results must not blank the reasoning section in between.
func TestIMProgressKeepsReasoningAfterStreamerResetsThinkingBuffer(t *testing.T) {
	recorder := &imbottest.ReplyRecorder{}
	progress := &imProgress{bridge: &IMBridge{Runtime: &service.Runtime{}}, msg: imbot.Message{Platform: "slack"}, responder: recorder}

	progress.observe(service.AgentStreamFrame{
		Event:    agent.StreamEvent{Kind: agent.KindThinkingDelta, Content: "先检查部署状态"},
		Thinking: "先检查部署状态",
	})
	progress.observe(service.AgentStreamFrame{
		Event:  agent.StreamEvent{Kind: agent.KindToolUseStart, Content: "Bash"},
		Reply:  "部署已完成。",
		Result: "部署已完成。",
	})

	texts := recorder.All()
	if got := texts[len(texts)-1]; !strings.Contains(got, "先检查部署状态") {
		t.Fatalf("progress update = %q, want the reasoning retained across the buffer reset", got)
	}
}

func TestIMThinkingUpdateInterval(t *testing.T) {
	if imThinkingUpdateInterval != 3*time.Second {
		t.Fatalf("thinking update interval = %s, want 3s", imThinkingUpdateInterval)
	}
}

func TestIMBridgeStartsTitleExtractionBeforeAgentReply(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", Name: "Owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		agentUser,
	}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "feishu", Enabled: true,
		Channels: `[{"channel":"*","auto_reply":true}]`,
	}}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	titler := &agenttest.FakeTitler{Out: "Investigate alert"}
	pool := service.NewPool(cfg)
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: pool, Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, blockingIMBackend{started: started, release: release}), TitleGen: titler}, Bots: ms,
	}

	done := make(chan struct{})
	go func() {
		b.HandleMessage(context.Background(), imbot.Message{
			Platform: "feishu", AgentID: "agent1", BotID: "bot1", ChannelID: "C1", ChannelName: "alerts",
			ThreadID: "thread1", MessageID: "message1", SenderName: "Alice", Text: "investigate the alert", Mentioned: true,
		}, &imbottest.ReplyRecorder{})
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for titler.CallCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if titler.CallCount() != 1 {
		t.Fatalf("title generator calls before agent reply = %d, want 1", titler.CallCount())
	}
	for ms.ConversationTitle(0) != "飞书·Investigate alert" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ms.ConversationTitle(0); got != "飞书·Investigate alert" {
		t.Fatalf("conversation title before agent reply = %q, want early platform-prefixed title", got)
	}
	select {
	case <-done:
		t.Fatal("agent completed before the test released its reply")
	default:
	}

	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("IM run did not finish after releasing the agent")
	}
	if titler.CallCount() != 1 {
		t.Fatalf("title generator calls after agent reply = %d, want no duplicate extraction", titler.CallCount())
	}
}

func TestIMConversationTitlePrefix(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		want     string
	}{
		{name: "Slack", platform: imbot.PlatformSlack, want: "Slack·"},
		{name: "Feishu", platform: imbot.PlatformFeishu, want: "飞书·"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imConversationTitlePrefix(imbot.Message{Platform: tt.platform}); got != tt.want {
				t.Fatalf("imConversationTitlePrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIMBridgeReportsEveryQueuePositionDecreaseToAppAndConversation(t *testing.T) {
	ms := storetest.New()
	useAccountModels(t, map[string][]string{"acc1": {agenttest.Model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}, agentUser}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true, Channels: `[{"channel":"*","auto_reply":true}]`}}
	pool := service.NewPool(cfg)
	holder, err := pool.EnterForUser(agentUser.ID, "acc1", agenttest.Model)
	if err != nil || holder.Wait(context.Background()) != nil {
		t.Fatalf("acquire holder: %v", err)
	}
	ahead, err := pool.EnterForUser(agentUser.ID, "acc1", agenttest.Model)
	if err != nil {
		t.Fatal(err)
	}

	broadcaster := service.NewBroadcaster()
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: pool, Drainer: service.NewDrainer(), Broadcaster: broadcaster, UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "done"})}, Bots: ms,
	}
	recorder := &imbottest.ReplyRecorder{}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		b.HandleMessage(context.Background(), imbot.Message{
			Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1",
			ThreadID: "thread1", MessageID: "m1", Text: "run", Mentioned: true,
		}, recorder)
	}()

	imbottest.WaitForReplyContaining(t, recorder, "前面共有 2 个任务，其中 1 个正在执行")
	if len(ms.Conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(ms.Conversations))
	}
	convID := ms.Conversations[0].ID
	webEvents := make(chan []byte, 32)
	broadcaster.Join(convID, "web", webEvents)
	defer broadcaster.Leave(convID, "web")

	holder.Release()
	if err := ahead.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	imbottest.WaitForReplyContaining(t, recorder, "前面共有 1 个任务，其中 1 个正在执行")
	ahead.Release()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("IM task did not run after its queue slot was granted")
	}

	replies := recorder.All()
	wantQueueUpdates := []string{
		"⏳ 我当前在排队，前面共有 2 个任务，其中 1 个正在执行。",
		"⏳ 我当前在排队，前面共有 1 个任务，其中 1 个正在执行。",
	}
	for _, want := range wantQueueUpdates {
		count := 0
		for _, got := range replies {
			if got == want {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("queue update %q occurred %d times; replies=%v", want, count, replies)
		}
	}

	type queueSnapshot struct {
		position int
		ahead    int
		running  int
	}
	// The counts ride the wire as *int so a real zero survives omitempty; a
	// nil here would mean the frame omitted them, which queue_status never does.
	derefQueueCount := func(v *int) int {
		if v == nil {
			t.Fatal("queue_status frame omitted a queue count")
		}
		return *v
	}
	snapshots := make([]queueSnapshot, 0, 2)
	drain := true
	for drain {
		select {
		case data := <-webEvents:
			var event service.ServerMessage
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "queue_status" {
				snapshots = append(snapshots, queueSnapshot{
					position: event.QueuePosition,
					ahead:    derefQueueCount(event.QueueAhead),
					running:  derefQueueCount(event.QueueRunning),
				})
			}
		default:
			drain = false
		}
	}
	wantSnapshots := []queueSnapshot{{2, 2, 1}, {1, 1, 1}}
	if !reflect.DeepEqual(snapshots, wantSnapshots) {
		t.Fatalf("web queue snapshots = %v, want %v", snapshots, wantSnapshots)
	}
}

func TestIMBridgeAgentFor(t *testing.T) {
	t.Run("injected agent id resolves", func(t *testing.T) {
		ms := storetest.New()
		ms.Users = []store.User{{ID: "agent1", Name: "Ops", OwnerID: "admin1"}}
		b := &IMBridge{Runtime: &service.Runtime{Store: ms}}
		u, err := b.agentFor(context.Background(), imbot.Message{Platform: "slack", AgentID: "agent1"})
		if err != nil || u.ID != "agent1" {
			t.Fatalf("got %v, %v", u.ID, err)
		}
	})

	t.Run("missing injected agent errors", func(t *testing.T) {
		ms := storetest.New()
		b := &IMBridge{Runtime: &service.Runtime{Store: ms}}
		if _, err := b.agentFor(context.Background(), imbot.Message{AgentID: "ghost"}); err == nil {
			t.Fatal("expected error for missing agent")
		}
	})
	t.Run("missing agent id errors", func(t *testing.T) {
		ms := storetest.New()
		b := &IMBridge{Runtime: &service.Runtime{Store: ms}}
		if _, err := b.agentFor(context.Background(), imbot.Message{}); err == nil {
			t.Fatal("expected error when no agent id exists")
		}
	})
}

func TestIMBridgeResolveConversationSeed(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1},
	}}
	useAccountModels(t, map[string][]string{"default": {"claude-a", "claude-b"}})
	b := &IMBridge{Runtime: &service.Runtime{Cfg: cfg}}

	t.Run("sticky thread binding wins", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(store.User{}, store.BotThread{Provider: "claude", Model: "claude-b"}, "claude-a")
		if err != nil || seed.Provider != "claude" || seed.Model != "claude-b" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})

	t.Run("stale thread model falls through to defaults", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(store.User{}, store.BotThread{Provider: "claude", Model: "removed-model"}, "")
		if err != nil || seed.Provider != "claude" || seed.Model == "removed-model" || seed.Model == "" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})

	t.Run("bot configured model wins over agent default", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(
			store.User{DefaultModel: "claude-b"},
			store.BotThread{},
			"claude-a",
		)
		if err != nil || seed.Provider != "claude" || seed.Model != "claude-a" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})

	t.Run("agent default model implies provider", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(store.User{DefaultModel: "claude-a"}, store.BotThread{}, "")
		if err != nil || seed.Provider != "claude" || seed.Model != "claude-a" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})

	t.Run("server default provider fallback", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(store.User{}, store.BotThread{}, "")
		if err != nil || seed.Provider != "claude" || seed.Model == "" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})

	t.Run("no providers configured errors", func(t *testing.T) {
		empty := &IMBridge{Runtime: &service.Runtime{Cfg: &config.Config{}}}
		if _, err := empty.resolveConversationSeed(store.User{}, store.BotThread{}, ""); err == nil {
			t.Fatal("expected error with no providers")
		}
	})

	// The Agent's think level travels with its default model, as it does for
	// a conversation created in the browser; a model the Bot pins does not
	// carry it.
	t.Run("agent default model carries the agent think level", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(store.User{DefaultModel: "claude-a", ThinkLevel: "high"}, store.BotThread{}, "")
		if err != nil || seed.Model != "claude-a" || seed.ThinkLevel != "high" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})

	t.Run("bot configured model keeps the provider default think level", func(t *testing.T) {
		seed, err := b.resolveConversationSeed(store.User{DefaultModel: "claude-a", ThinkLevel: "high"}, store.BotThread{}, "claude-b")
		if err != nil || seed.Model != "claude-b" || seed.ThinkLevel != "" {
			t.Fatalf("got %+v, %v", seed, err)
		}
	})
}

func TestIMBridgeBuildPrompt(t *testing.T) {
	b := &IMBridge{Runtime: &service.Runtime{}}
	if got := b.buildPrompt(imbot.Message{IsDM: true, SenderName: "王", Text: "hi"}); got != "hi" {
		t.Fatalf("DM prompt should be bare text, got %q", got)
	}
	if got := b.buildPrompt(imbot.Message{SenderName: "王", Text: "hi"}); got != "[王]: hi" {
		t.Fatalf("group prompt should carry sender, got %q", got)
	}
	if got := b.buildPrompt(imbot.Message{SenderID: "U1", Text: "hi"}); got != "[U1]: hi" {
		t.Fatalf("sender id fallback, got %q", got)
	}
	if got := b.buildPrompt(imbot.Message{SenderName: "王"}); got != "[王]" {
		t.Fatalf("image-only group prompt should still identify sender, got %q", got)
	}
}

func TestIMBridgeMaterializeInboundImages(t *testing.T) {
	// The owner id carries a space on purpose: it reaches the read URL as a
	// path segment, so a missing escape would produce a link nothing resolves.
	homeRoot := t.TempDir()
	owner := store.User{ID: "owner 1", Username: "alice", Email: "alice@example.com", WorkDir: homeRoot}
	agentUser := store.User{ID: "agent 1", OwnerID: owner.ID, Email: owner.Email, WorkDir: filepath.Join(homeRoot, "project")}
	ms := storetest.New()
	ms.Users = []store.User{owner, agentUser}
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	msg := imbot.Message{Attachments: []imbot.Attachment{{
		ID:   "img-1",
		Name: "screen",
		Download: func(_ context.Context, dst io.Writer) error {
			_, err := dst.Write(png)
			return err
		},
	}}}

	got, _, err := (&IMBridge{Runtime: &service.Runtime{Store: ms}}).materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MIME != "image/png" || !strings.HasSuffix(got[0].Path, ".png") {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if !strings.Contains(got[0].URL, "/api/users/owner%201/files/read?path=") {
		t.Fatalf("unexpected protected URL %q", got[0].URL)
	}
	if saved, err := os.ReadFile(service.DaymugPath(homeRoot, got[0].Path)); err != nil || !bytes.Equal(saved, png) {
		t.Fatalf("saved image mismatch: err=%v", err)
	}
}

func TestIMBridgeMaterializesInboundOrdinaryFile(t *testing.T) {
	b, agentUser, homeRoot := attachmentBridge(t)
	pdf := []byte("%PDF-1.7\nreport")
	msg := imbot.Message{Attachments: []imbot.Attachment{{
		ID:   "file-1",
		Name: "brief.pdf",
		MIME: "application/pdf",
		Download: func(_ context.Context, dst io.Writer) error {
			_, err := dst.Write(pdf)
			return err
		},
	}}}

	got, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MIME != "application/pdf" || !strings.HasSuffix(got[0].Path, ".pdf") {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if saved, err := os.ReadFile(service.DaymugPath(homeRoot, got[0].Path)); err != nil || !bytes.Equal(saved, pdf) {
		t.Fatalf("saved file mismatch: err=%v", err)
	}
}

func TestSniffInboundAttachmentSVG(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "svg", content: `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><text>hello</text></svg>`},
		{name: "not svg", content: `<html><body>hello</body></html>`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "attachment.svg")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := sniffInboundAttachment(path, "image/svg+xml; charset=utf-8")
			if (err != nil) != tt.wantErr {
				t.Fatalf("sniffInboundAttachment() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != "image/svg+xml" {
				t.Fatalf("sniffInboundAttachment() = %q, want image/svg+xml", got)
			}
		})
	}
}

// An oversize attachment is skipped rather than failing the whole message: the
// rest of the message is still worth ingesting, and the agent learns about the
// gap from the returned notice.
func TestIMBridgeSkipsOversizeInboundImage(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	msg := imbot.Message{Attachments: []imbot.Attachment{
		{
			Name: "huge.png",
			Size: service.DefaultUploadMaxBytes + 1,
			Download: func(_ context.Context, _ io.Writer) error {
				t.Error("oversize image should be rejected before download")
				return nil
			},
		},
		{
			Name: "small.png",
			Download: func(_ context.Context, dst io.Writer) error {
				_, err := dst.Write(png)
				return err
			},
		},
	}}
	b, agentUser, _ := attachmentBridge(t)
	got, notice, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err != nil {
		t.Fatalf("oversize attachment must not fail the message: %v", err)
	}
	if len(got) != 1 || got[0].Name != "small.png" {
		t.Fatalf("expected only the small attachment, got %+v", got)
	}
	if !strings.Contains(notice, "1 个附件") {
		t.Fatalf("notice should name the skipped attachment count, got %q", notice)
	}
}

func TestThreadPlatformScopesSlackThreadsToAgent(t *testing.T) {
	msg := imbot.Message{Platform: "slack", AgentID: "agent-1", BotID: "bot-1"}
	if got := threadPlatform(msg); got != "slack@agent-1@bot-1" {
		t.Fatalf("got %q", got)
	}
	if got := threadPlatform(imbot.Message{Platform: "feishu"}); got != "feishu" {
		t.Fatalf("system integration key changed: %q", got)
	}
}

func TestIMSystemPromptRequiresBotIdentityDelivery(t *testing.T) {
	for _, platform := range []string{imbot.PlatformSlack, imbot.PlatformFeishu, imbot.PlatformWeChat} {
		prompt := imSystemPrompt(imbot.Message{Platform: platform})
		for _, required := range []string{
			"exclusively handled by DayMug's attached bot connector",
			"Never call any Slack, Feishu, Lark, Telegram, or WeChat/Weixin messaging tool",
			"Never use employee or user credentials",
			"configured bot identity",
		} {
			if !strings.Contains(prompt, required) {
				t.Fatalf("%s IM prompt missing %q: %s", platform, required, prompt)
			}
		}
	}
}

// The mention instruction is only true where the connector actually resolves
// names, so it must not reach an agent on a platform that would leave "@name"
// as plain text.
func TestIMSystemPromptTeachesMentionsOnlyWhereResolved(t *testing.T) {
	for platform, want := range map[string]bool{
		imbot.PlatformSlack:    true,
		imbot.PlatformFeishu:   true,
		imbot.PlatformTelegram: false,
		imbot.PlatformWeChat:   false,
	} {
		prompt := imSystemPrompt(imbot.Message{Platform: platform})
		if got := strings.Contains(prompt, prompts.IMMention); got != want {
			t.Fatalf("%s prompt teaches mentions = %v, want %v", platform, got, want)
		}
	}
}

// noEditResponder models a platform that cannot revise a sent message (Weixin):
// Update writes nowhere, so anything the reader must actually see has to arrive
// through Notice.
type noEditResponder struct {
	mu       sync.Mutex
	notices  []string
	updates  []string
	closed   int
	complete []string
}

func (r *noEditResponder) Start(context.Context, string) error { return nil }
func (r *noEditResponder) Update(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, text)
	return nil
}
func (r *noEditResponder) Complete(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.complete = append(r.complete, text)
	return nil
}
func (r *noEditResponder) Post(context.Context, string) error { return nil }
func (r *noEditResponder) Notice(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, text)
	return nil
}
func (r *noEditResponder) Close(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
}
func (r *noEditResponder) snapshot() (notices []string, closed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.notices...), r.closed
}

type failingIMBackend struct{}

func (failingIMBackend) Name() string                     { return "failing-im" }
func (failingIMBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (failingIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	close(ch)
	return errors.New("boom")
}
func (failingIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", errors.New("boom")
}
func (failingIMBackend) SessionExists(string, string, string) bool    { return false }
func (failingIMBackend) SessionLogPath(string, string, string) string { return "" }

// A failed run produces no reply to supersede the progress message, so on a
// platform where Update writes nowhere the notice is the reader's only signal
// that the turn is over — and the turn must still release the responder.
func TestIMBridgeDeliversFailureNoticeWithoutEditPrimitive(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
	}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: imbot.PlatformWeChat, Enabled: true,
		Channels: `[{"channel":"dm","auto_reply":true}]`}}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, failingIMBackend{})}, Bots: ms,
	}

	rec := &noEditResponder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: imbot.PlatformWeChat, AgentID: "agent1", BotID: "bot1", ChannelID: "u1@im.wechat",
		ThreadID: "t1", MessageID: "m1", SenderID: "u1@im.wechat", Text: "部署", IsDM: true,
	}, rec)

	notices, closed := rec.snapshot()
	if len(notices) == 0 || !strings.HasPrefix(notices[0], errorNoticePrefix) {
		t.Fatalf("notices = %v, want a failure notice the reader can see", notices)
	}
	if closed != 1 {
		t.Fatalf("responder closed %d times, want exactly 1 so the activity indicator goes out", closed)
	}
}

// userPromptRow returns the mirrored user prompt for a conversation, read
// through the store API so it is safe to inspect while the run goroutine is
// still writing.
func userPromptRow(t *testing.T, ms *storetest.Fake, convID string) store.Message {
	t.Helper()
	msgs, err := ms.ListMessages(context.Background(), convID, 100, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, m := range msgs {
		if m.Role == "user" {
			return m
		}
	}
	t.Fatalf("no user row in conversation %s", convID)
	return store.Message{}
}

// The web mirror used to announce prompt_started at persist time, so a turn
// stuck behind a full account pool looked like it was already running: the chat
// UI promoted the row out of its staging area and the queue card never showed.
func TestIMBridgeKeepsPromptQueuedUntilTheSlotIsGranted(t *testing.T) {
	ms := storetest.New()
	useAccountModels(t, map[string][]string{"acc1": {agenttest.Model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	agentUser := store.User{
		ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}, agentUser}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: imbot.PlatformWeChat, Enabled: true,
		Channels: `[{"channel":"dm","auto_reply":true}]`}}

	pool := service.NewPool(cfg)
	holder, err := pool.EnterForUser("", "acc1", agenttest.Model)
	if err != nil || holder.Wait(context.Background()) != nil {
		t.Fatalf("acquire holder: %v", err)
	}

	broadcaster := service.NewBroadcaster()
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: pool, Drainer: service.NewDrainer(), Broadcaster: broadcaster, UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "done"})}, Bots: ms,
	}
	recorder := &imbottest.ReplyRecorder{}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		b.HandleMessage(context.Background(), imbot.Message{
			Platform: imbot.PlatformWeChat, AgentID: "agent1", BotID: "bot1", ChannelID: "u1@im.wechat",
			ThreadID: "t1", MessageID: "m1", SenderID: "u1@im.wechat", Text: "部署", IsDM: true,
		}, recorder)
	}()

	imbottest.WaitForReplyContaining(t, recorder, "我当前在排队")
	if len(ms.Conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(ms.Conversations))
	}
	convID := ms.Conversations[0].ID
	if got := userPromptRow(t, ms, convID).QueueStatus; got != store.QueueStatusPoolQueued {
		t.Fatalf("queued prompt queue_status = %q, want %q", got, store.QueueStatusPoolQueued)
	}

	webEvents := make(chan []byte, 32)
	broadcaster.Join(convID, "web", webEvents)
	defer broadcaster.Leave(convID, "web")

	holder.Release()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("IM task did not run after its queue slot was granted")
	}

	if got := userPromptRow(t, ms, convID).QueueStatus; got != "" {
		t.Fatalf("started prompt queue_status = %q, want it cleared", got)
	}
	if !sawFrameType(t, webEvents, "prompt_started") {
		t.Fatal("no prompt_started reached the web once the slot was granted")
	}
}

// A turn that dies while queued still left the queue. Leaving the marker on
// would show a queue card that nothing ever clears.
func TestIMBridgeClearsQueuedMarkerWhenTheRunFails(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
	}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: imbot.PlatformWeChat, Enabled: true,
		Channels: `[{"channel":"dm","auto_reply":true}]`}}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, failingIMBackend{})}, Bots: ms,
	}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: imbot.PlatformWeChat, AgentID: "agent1", BotID: "bot1", ChannelID: "u1@im.wechat",
		ThreadID: "t1", MessageID: "m1", SenderID: "u1@im.wechat", Text: "部署", IsDM: true,
	}, &noEditResponder{})

	if len(ms.Conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(ms.Conversations))
	}
	if got := userPromptRow(t, ms, ms.Conversations[0].ID).QueueStatus; got != "" {
		t.Fatalf("failed prompt queue_status = %q, want it cleared", got)
	}
}

// An IM turn cannot outlive the process, so a marker that survives a restart
// belongs to nobody.
func TestClearStaleQueuedPromptsWipesLeftovers(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1"}}
	if err := ms.SaveMessage(context.Background(), store.Message{
		ID: "m1", ConversationID: "c1", Role: "user", Content: "hi",
		QueueStatus: store.QueueStatusPoolQueued,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	(&IMBridge{Runtime: &service.Runtime{Store: ms}}).ClearStaleQueuedPrompts(context.Background())
	if got := userPromptRow(t, ms, "c1").QueueStatus; got != "" {
		t.Fatalf("queue_status = %q, want it cleared at boot", got)
	}
}

func sawFrameType(t *testing.T, events <-chan []byte, want string) bool {
	t.Helper()
	for {
		select {
		case data := <-events:
			var event service.ServerMessage
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == want {
				return true
			}
		default:
			return false
		}
	}
}

// A bare /new has nothing to answer, but the user is owed proof that the reset
// landed — on Weixin there is no thread UI to show it any other way.
func TestIMBridgeAcksAControlInstructionWithoutRunningTheAgent(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	ms.Users = []store.User{
		{ID: "owner1", Username: "owner", ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
		{ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir(),
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"}},
	}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: imbot.PlatformWeChat, Enabled: true,
		Channels: `[{"channel":"dm","auto_reply":true}]`}}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "should not run"})}, Bots: ms,
	}

	rec := &noEditResponder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: imbot.PlatformWeChat, AgentID: "agent1", BotID: "bot1", ChannelID: "u1@im.wechat",
		ThreadID: "t1", MessageID: "m1", SenderID: "u1@im.wechat", Text: "", Ack: "🆕 已开始新会话", IsDM: true,
	}, rec)

	notices, _ := rec.snapshot()
	if len(notices) != 1 || notices[0] != "🆕 已开始新会话" {
		t.Fatalf("notices = %v, want just the confirmation", notices)
	}
	// No prompt means no conversation, no account slot, no agent run.
	if len(ms.Conversations) != 0 {
		t.Fatalf("a bare control instruction started %d conversation(s)", len(ms.Conversations))
	}
}

// The confirmation is a reply like any other, so it must not reach a sender the
// operator scoped out — it would tell them the bot is alive and listening.
func TestIMBridgeWithholdsTheAckFromAnUnauthorizedSender(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent1", Name: "Ops", OwnerID: "owner1"}}
	ms.Bots = []store.Bot{{ID: "bot1", AgentID: "agent1", Platform: imbot.PlatformWeChat, Enabled: true,
		Channels: `[{"channel":"dm","auto_reply":true,"allowed_user_ids":["someone-else"]}]`}}
	b := &IMBridge{Runtime: &service.Runtime{Store: ms}, Bots: ms}

	rec := &noEditResponder{}
	b.HandleMessage(context.Background(), imbot.Message{
		Platform: imbot.PlatformWeChat, AgentID: "agent1", BotID: "bot1", ChannelID: "u1@im.wechat",
		ThreadID: "t1", MessageID: "m1", SenderID: "u1@im.wechat", Ack: "🆕 已开始新会话", IsDM: true,
	}, rec)

	notices, _ := rec.snapshot()
	for _, text := range append(notices, rec.updates...) {
		if strings.Contains(text, "已开始新会话") {
			t.Fatalf("the confirmation reached a scoped-out sender: %v", text)
		}
	}
}

// contextUsageOf reads the fake store's persisted reading, retrying because
// PersistContextUsage writes on its own goroutine.
func contextUsageOf(t *testing.T, ms *storetest.Fake, convID string) string {
	t.Helper()
	for range 200 {
		conv, err := ms.GetConversation(context.Background(), convID)
		if err == nil && conv.LastContextUsage != "" {
			return conv.LastContextUsage
		}
		time.Sleep(5 * time.Millisecond)
	}
	return ""
}

// Case-mode rotation reads conversations.last_context_usage, so an IM turn that
// only pushed its context reading to the websocket left the column empty and
// the thread's session immortal. The live token bar looked correct throughout,
// which is why this went unnoticed in production.
func TestBroadcastIMPersistsTheContextReadingRotationDependsOn(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1"}}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Broadcaster: service.NewBroadcaster()},
	}

	payload := `{"used":200000,"total":258400}`
	b.broadcastIM("c1", service.ServerMessage{Type: "context_usage", Content: payload})

	got := contextUsageOf(t, ms, "c1")
	if got != payload {
		t.Fatalf("last_context_usage = %q, want %q", got, payload)
	}
	if !casefile.ContextOverThreshold(got, b.caseRotateRatio()) {
		t.Fatal("the persisted reading is past the rotation ratio but casefile.ContextOverThreshold disagrees")
	}
}

// Only the context reading is persisted. Every other frame kind flows through
// the same function, and writing them to the conversation row would corrupt the
// value rotation is read from.
func TestBroadcastIMLeavesOtherFramesOutOfTheConversationRow(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1"}}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Broadcaster: service.NewBroadcaster()},
	}

	b.broadcastIM("c1", service.ServerMessage{Type: "delta", Content: "hello"})
	b.broadcastIM("c1", service.ServerMessage{Type: "result", Content: "done"})

	time.Sleep(20 * time.Millisecond)
	conv, err := ms.GetConversation(context.Background(), "c1")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if conv.LastContextUsage != "" {
		t.Fatalf("last_context_usage = %q, want it untouched by non-usage frames", conv.LastContextUsage)
	}
}
