package imbridge

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func rotationTestConfig(t *testing.T) *config.Config {
	t.Helper()
	useAccountModels(t, map[string][]string{
		"acc1": {agenttest.Model},
		"acc2": {agenttest.Model},
		"acc3": {agenttest.Model},
	})
	return &config.Config{Providers: []config.Provider{
		{Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1},
		{Name: "acc2", Type: config.CLITypeClaude, MaxConcurrent: 1},
		{Name: "acc3", Type: config.CLITypeClaude, MaxConcurrent: 1},
	}}
}

func rotationTestAgent(accounts ...string) store.User {
	return store.User{
		ID: "agent-1", Name: "agent-1", OwnerID: "owner1", WorkDir: "/tmp",
		ProviderBindings: map[string]string{config.CLITypeClaude: accounts[0]},
		ProviderAccounts: map[string][]string{config.CLITypeClaude: accounts},
	}
}

// A bot pins one model, so every thread it opens would otherwise resolve to the
// same default binding — the sibling accounts granted to the agent stay idle
// while that one queues and rate-limits.
func TestFreshThreadsRotateAcrossAccountsServingTheModel(t *testing.T) {
	bridge := &IMBridge{Runtime: &service.Runtime{Cfg: rotationTestConfig(t), Pool: service.NewPool(rotationTestConfig(t))}}
	agentUser := rotationTestAgent("acc1", "acc2", "acc3")

	var got []string
	for range 4 {
		got = append(got, bridge.resolveRunAccount(agentUser, "", config.CLITypeClaude, agenttest.Model))
	}
	want := []string{"acc1", "acc2", "acc3", "acc1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("accounts = %v, want %v", got, want)
		}
	}
}

// Resuming a CLI session only works on the account whose config dir holds it,
// so a thread that already has a conversation must never rotate.
func TestLiveThreadKeepsItsPinnedAccount(t *testing.T) {
	bridge := &IMBridge{Runtime: &service.Runtime{Cfg: rotationTestConfig(t), Pool: service.NewPool(rotationTestConfig(t))}}
	agentUser := rotationTestAgent("acc1", "acc2", "acc3")

	for range 3 {
		if got := bridge.resolveRunAccount(agentUser, "acc3", config.CLITypeClaude, agenttest.Model); got != "acc3" {
			t.Fatalf("account = %q, want acc3 — a live session was rotated away from its account", got)
		}
	}
}

// Revoking an account has to take effect even on a thread that already pinned
// it, otherwise the run resolves credentials the admin just took away.
func TestRevokedPinFallsBackToRotation(t *testing.T) {
	bridge := &IMBridge{Runtime: &service.Runtime{Cfg: rotationTestConfig(t), Pool: service.NewPool(rotationTestConfig(t))}}
	agentUser := rotationTestAgent("acc1", "acc2")

	if got := bridge.resolveRunAccount(agentUser, "acc3", config.CLITypeClaude, agenttest.Model); got == "acc3" {
		t.Fatal("account = acc3, want a granted account — a revoked pin is still being honoured")
	}
}

func TestResolveRunAccountKeepsBindingWhenRotationHasNothingToOffer(t *testing.T) {
	cfg := rotationTestConfig(t)
	bridge := &IMBridge{Runtime: &service.Runtime{Cfg: cfg, Pool: service.NewPool(cfg)}}

	tests := []struct {
		name  string
		user  store.User
		model string
		want  string
	}{
		{
			// Legacy rows: a binding with no granted set behind it.
			name: "no granted accounts",
			user: store.User{ID: "agent-1",
				ProviderBindings: map[string]string{config.CLITypeClaude: "acc2"}},
			model: agenttest.Model,
			want:  "acc2",
		},
		{
			name:  "no account serves the model",
			user:  rotationTestAgent("acc1", "acc2"),
			model: "some-unlisted-model",
			want:  "acc1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bridge.resolveRunAccount(tt.user, "", config.CLITypeClaude, tt.model); got != tt.want {
				t.Fatalf("account = %q, want %q", got, tt.want)
			}
		})
	}
}

// A rate-limited account is not a candidate: handing a turn to it only earns a
// CooldownError the thread reports as "try again later".
func TestRotationSkipsAccountsInCooldownForTheModel(t *testing.T) {
	cfg := rotationTestConfig(t)
	pool := service.NewPool(cfg)
	pool.Cooldown("acc2", agenttest.Model, time.Now().Add(time.Hour))
	bridge := &IMBridge{Runtime: &service.Runtime{Cfg: cfg, Pool: pool}}
	agentUser := rotationTestAgent("acc1", "acc2", "acc3")

	for range 4 {
		if got := bridge.resolveRunAccount(agentUser, "", config.CLITypeClaude, agenttest.Model); got == "acc2" {
			t.Fatal("rotation handed a turn to acc2, which is rate-limited for this model")
		}
	}
}

// End-to-end: two IM threads of the same bot must land on different accounts,
// and each conversation must record the one it used so its next turn resumes
// there instead of re-rolling the rotation.
func TestIMTurnsOnDifferentThreadsUseDifferentAccounts(t *testing.T) {
	workDir := t.TempDir()
	cfg := rotationTestConfig(t)
	ms := storetest.New()
	ms.Users = []store.User{
		{
			ID: "owner1", Username: "owner", Name: "Owner", WorkDir: workDir,
			ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
			ProviderAccounts: map[string][]string{config.CLITypeClaude: {"acc1", "acc2", "acc3"}},
		},
		{ID: "agent-1", Name: "agent-1", OwnerID: "owner1", WorkDir: workDir},
	}
	ms.Bots = []store.Bot{{
		ID: "bot-1", AgentID: "agent-1", Platform: "slack", Enabled: true,
		Model:    agenttest.Model,
		Channels: `[{"channel":"*","auto_reply":true}]`,
	}}
	bridge := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, artifactIMBackend{results: []string{"ok"}})}, Bots: ms,
	}

	send := func(threadID, messageID string) {
		msg := interruptTestMessage("slack", "agent-1", "bot-1", messageID)
		msg.ThreadID = threadID
		bridge.HandleMessage(context.Background(), msg, &imbottest.ReplyRecorder{})
	}
	send("thread1", "m1")
	send("thread2", "m2")
	send("thread1", "m3")

	accounts := map[string]string{}
	for _, thread := range []string{"thread1", "thread2"} {
		row, err := ms.GetBotThread(context.Background(), "slack@agent-1@bot-1", "C1", thread)
		if err != nil {
			t.Fatalf("get bot thread %s: %v", thread, err)
		}
		conv, err := ms.GetConversation(context.Background(), row.ConversationID)
		if err != nil {
			t.Fatalf("get conversation for %s: %v", thread, err)
		}
		if conv.AccountName == "" {
			t.Fatalf("%s conversation has no pinned account — its next turn would rotate away from its session", thread)
		}
		accounts[thread] = conv.AccountName
	}
	if accounts["thread1"] == accounts["thread2"] {
		t.Fatalf("both threads ran on %q — turns are not being spread across the agent's accounts", accounts["thread1"])
	}
	if accounts["thread1"] != "acc1" {
		t.Fatalf("thread1 account = %q, want acc1 (its second turn must stay on the first turn's account)", accounts["thread1"])
	}
}
