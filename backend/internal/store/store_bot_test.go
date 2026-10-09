package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBotThreadMissingIsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_, err := s.GetBotThread(context.Background(), "slack", "C1", "t1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestIsBotConversation(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	isBot, err := s.IsBotConversation(ctx, "conv1")
	if err != nil {
		t.Fatalf("check unmapped conversation: %v", err)
	}
	if isBot {
		t.Fatal("unmapped conversation reported as bot conversation")
	}

	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "slack", ChannelID: "C1", ThreadID: "t1",
		AgentID: "agent1", ConversationID: "conv1",
	}); err != nil {
		t.Fatalf("insert bot thread: %v", err)
	}
	isBot, err = s.IsBotConversation(ctx, "conv1")
	if err != nil {
		t.Fatalf("check mapped conversation: %v", err)
	}
	if !isBot {
		t.Fatal("mapped conversation was not reported as bot conversation")
	}
}

func TestBotThreadUpsertAndGet(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	first := BotThread{
		Platform: "feishu", ChannelID: "oc_1", ThreadID: "om_root",
		AgentID: "agent1", ConversationID: "conv1", SessionID: "sess1", Provider: "claude", Model: "claude-a", LastMessageID: "om_1",
	}
	if err := s.UpsertBotThread(ctx, first); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.GetBotThread(ctx, "feishu", "oc_1", "om_root")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentID != "agent1" || got.ConversationID != "conv1" || got.SessionID != "sess1" || got.Provider != "claude" || got.Model != "claude-a" || got.LastMessageID != "om_1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	byConversation, err := s.GetBotThreadByConversation(ctx, "conv1")
	if err != nil {
		t.Fatalf("reverse lookup: %v", err)
	}
	if byConversation.Platform != first.Platform || byConversation.ChannelID != first.ChannelID || byConversation.ThreadID != first.ThreadID {
		t.Fatalf("reverse lookup mismatch: %+v", byConversation)
	}

	// Same thread identity updates in place (e.g. codex assigns a new
	// session id every turn).
	first.SessionID = "sess2"
	first.Model = "claude-b"
	first.LastMessageID = "om_2"
	if err := s.UpsertBotThread(ctx, first); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err = s.GetBotThread(ctx, "feishu", "oc_1", "om_root")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.SessionID != "sess2" || got.Model != "claude-b" || got.LastMessageID != "om_2" {
		t.Fatalf("update not applied: %+v", got)
	}

	// A different thread in the same channel is a distinct row.
	if _, err := s.GetBotThread(ctx, "feishu", "oc_1", "om_other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("distinct thread should be not-found, got %v", err)
	}
}

func TestBotThreadByConversationMissingIsNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_, err := s.GetBotThreadByConversation(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestCronBotDeliveryTarget(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedCronAgent(t, s)
	ctx := context.Background()

	if err := s.CreateCronJob(ctx, CronJob{
		ID: "cron-1", OwnerID: "owner-1", AgentID: "agent-1",
		Expression: "* * * * *", Timezone: "UTC", Prompt: "Run task",
		Enabled: true, DeliverToBot: true,
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "feishu", ChannelID: "oc_1", ThreadID: "om_old",
		AgentID: "agent-1", ConversationID: "conv-old",
	}); err != nil {
		t.Fatalf("insert old thread: %v", err)
	}

	// The run's own conversation has no bot_threads row — that is the whole
	// point of the join going through the Agent — so before the scheduler
	// records the run there is nothing to resolve.
	if _, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unrecorded run should be not-found, got %v", err)
	}

	if err := s.RecordCronJobRun(ctx, "cron-1", time.Now(), "conv-run", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}
	got, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled")
	if err != nil {
		t.Fatalf("resolve delivery target: %v", err)
	}
	if got.Thread.ThreadID != "om_old" || got.Thread.Platform != "feishu" || got.Thread.ChannelID != "oc_1" {
		t.Fatalf("unexpected delivery target: %+v", got)
	}

	// Two threads for one Agent resolve to the one used most recently, which is
	// the closest stand-in for "the bot this Agent is talking to right now".
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "feishu", ChannelID: "oc_2", ThreadID: "om_new",
		AgentID: "agent-1", ConversationID: "conv-new",
	}); err != nil {
		t.Fatalf("insert new thread: %v", err)
	}
	// Both upserts land in the same clock second, so order the rows explicitly
	// rather than letting the assertion depend on datetime('now') resolution.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE bot_threads SET updated_at = datetime('now', '-1 hour') WHERE thread_id = 'om_old'`); err != nil {
		t.Fatalf("age old thread: %v", err)
	}
	got, err = s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled")
	if err != nil {
		t.Fatalf("resolve delivery target after second thread: %v", err)
	}
	if got.Thread.ThreadID != "om_new" {
		t.Fatalf("delivery target = %q, want the most recently used thread", got.Thread.ThreadID)
	}
}

// seedCronDeliveryBots gives agent-1 two Slack bots, the shape every pinned-bot
// case needs: with only one bot the query cannot tell "picked the named bot"
// apart from "picked the only bot there was".
func seedCronDeliveryBots(t *testing.T, s *SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{"bot-a", "bot-b"} {
		if err := s.CreateBot(ctx, Bot{
			ID: id, AgentID: "agent-1", Name: id, Platform: "slack", Enabled: true,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
}

// TestCronBotDeliveryTargetHonoursPinnedBot is the core of the feature: once a
// job names a bot, chatter in another bot's thread must not steal the delivery.
// Ordering the rival thread first is the whole assertion — the unpinned query
// sorts by updated_at, so a filter that silently fails to apply still returns a
// thread and would look correct without it.
func TestCronBotDeliveryTargetHonoursPinnedBot(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedCronAgent(t, s)
	seedCronDeliveryBots(t, s)
	ctx := context.Background()

	if err := s.CreateCronJob(ctx, CronJob{
		ID: "cron-1", OwnerID: "owner-1", AgentID: "agent-1",
		Expression: "* * * * *", Timezone: "UTC", Prompt: "Run task",
		Enabled: true, DeliverToBot: true, BotID: "bot-a",
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "slack@agent-1@bot-a", ChannelID: "C_a", ThreadID: "thread-a",
		AgentID: "agent-1", ConversationID: "conv-a",
	}); err != nil {
		t.Fatalf("insert pinned bot thread: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "slack@agent-1@bot-b", ChannelID: "C_b", ThreadID: "thread-b",
		AgentID: "agent-1", ConversationID: "conv-b",
	}); err != nil {
		t.Fatalf("insert rival bot thread: %v", err)
	}
	// Both upserts land in the same clock second, so age the pinned bot's thread
	// explicitly to make it the *loser* of the recency ordering.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE bot_threads SET updated_at = datetime('now', '-1 hour') WHERE thread_id = 'thread-a'`); err != nil {
		t.Fatalf("age pinned thread: %v", err)
	}
	if err := s.RecordCronJobRun(ctx, "cron-1", time.Now(), "conv-run", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}

	got, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled")
	if err != nil {
		t.Fatalf("resolve delivery target: %v", err)
	}
	if got.Thread.ThreadID != "thread-a" {
		t.Fatalf("delivery thread = %q, want the pinned bot's thread even though it is older", got.Thread.ThreadID)
	}
	if got.BotID != "bot-a" {
		t.Fatalf("delivery bot = %q, want bot-a: the relay needs the id to pick a responder", got.BotID)
	}
}

// TestCronBotDeliveryTargetSkipsUnavailablePinnedBot — a job whose bot has been
// deleted, or was moved to another Agent, must deliver nowhere rather than fall
// back to "any thread this Agent touched". Falling back would send an Agent's
// scheduled output into a chat its owner never pointed the job at.
func TestCronBotDeliveryTargetSkipsUnavailablePinnedBot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		botID string
	}{
		{name: "bot deleted", botID: "bot-gone"},
		{name: "bot belongs to another agent", botID: "bot-foreign"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedCronAgent(t, s)
			seedCronDeliveryBots(t, s)
			ctx := context.Background()

			if err := s.CreateUser(ctx, User{ID: "agent-2", OwnerID: "owner-1", Name: "Other"}); err != nil {
				t.Fatalf("create other agent: %v", err)
			}
			if err := s.CreateBot(ctx, Bot{
				ID: "bot-foreign", AgentID: "agent-2", Name: "Foreign", Platform: "slack", Enabled: true,
			}); err != nil {
				t.Fatalf("create foreign bot: %v", err)
			}
			if err := s.CreateCronJob(ctx, CronJob{
				ID: "cron-1", OwnerID: "owner-1", AgentID: "agent-1",
				Expression: "* * * * *", Timezone: "UTC", Prompt: "Run task",
				Enabled: true, DeliverToBot: true, BotID: tc.botID,
			}); err != nil {
				t.Fatalf("create job: %v", err)
			}
			// A thread that the unpinned query would happily hand back, so the
			// not-found below can only come from the bot filter.
			if err := s.UpsertBotThread(ctx, BotThread{
				Platform: "slack@agent-1@bot-a", ChannelID: "C_a", ThreadID: "thread-a",
				AgentID: "agent-1", ConversationID: "conv-a",
			}); err != nil {
				t.Fatalf("insert thread: %v", err)
			}
			if err := s.RecordCronJobRun(ctx, "cron-1", time.Now(), "conv-run", ""); err != nil {
				t.Fatalf("record run: %v", err)
			}

			if _, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("want ErrNotFound for an unavailable pinned bot, got %v", err)
			}
		})
	}
}

// TestCronBotDeliveryTargetWithoutPinnedBotKeepsMostRecentThread pins the
// v1.2.58 behaviour that every pre-existing job relies on: an empty bot_id must
// keep meaning "wherever this Agent last talked", across bots, and must report
// no pinned bot so the relay takes the bot from the thread's key.
func TestCronBotDeliveryTargetWithoutPinnedBotKeepsMostRecentThread(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedCronAgent(t, s)
	seedCronDeliveryBots(t, s)
	ctx := context.Background()

	if err := s.CreateCronJob(ctx, CronJob{
		ID: "cron-1", OwnerID: "owner-1", AgentID: "agent-1",
		Expression: "* * * * *", Timezone: "UTC", Prompt: "Run task",
		Enabled: true, DeliverToBot: true,
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "slack@agent-1@bot-a", ChannelID: "C_a", ThreadID: "thread-a",
		AgentID: "agent-1", ConversationID: "conv-a",
	}); err != nil {
		t.Fatalf("insert first thread: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "slack@agent-1@bot-b", ChannelID: "C_b", ThreadID: "thread-b",
		AgentID: "agent-1", ConversationID: "conv-b",
	}); err != nil {
		t.Fatalf("insert second thread: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE bot_threads SET updated_at = datetime('now', '-1 hour') WHERE thread_id = 'thread-a'`); err != nil {
		t.Fatalf("age first thread: %v", err)
	}
	if err := s.RecordCronJobRun(ctx, "cron-1", time.Now(), "conv-run", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}

	got, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled")
	if err != nil {
		t.Fatalf("resolve delivery target: %v", err)
	}
	if got.Thread.ThreadID != "thread-b" {
		t.Fatalf("delivery thread = %q, want the most recently used one", got.Thread.ThreadID)
	}
	if got.BotID != "" {
		t.Fatalf("delivery bot = %q, want empty for a job that pinned none", got.BotID)
	}
}

func TestCronBotDeliveryTargetSkipsJobsThatDidNotOptIn(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedCronAgent(t, s)
	ctx := context.Background()

	if err := s.CreateCronJob(ctx, CronJob{
		ID: "cron-1", OwnerID: "owner-1", AgentID: "agent-1",
		Expression: "* * * * *", Timezone: "UTC", Prompt: "Run task", Enabled: true,
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "feishu", ChannelID: "oc_1", ThreadID: "om_1",
		AgentID: "agent-1", ConversationID: "conv-thread",
	}); err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	if err := s.RecordCronJobRun(ctx, "cron-1", time.Now(), "conv-run", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}

	if _, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("job without deliver_to_bot should be not-found, got %v", err)
	}
}

func TestCronBotDeliveryTargetOnlyRelaysTheScheduledTurn(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	seedCronAgent(t, s)
	ctx := context.Background()

	if err := s.CreateCronJob(ctx, CronJob{
		ID: "cron-1", OwnerID: "owner-1", AgentID: "agent-1",
		Expression: "* * * * *", Timezone: "UTC", Prompt: "Run task",
		Enabled: true, DeliverToBot: true,
	}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := s.UpsertBotThread(ctx, BotThread{
		Platform: "feishu", ChannelID: "oc_1", ThreadID: "om_1",
		AgentID: "agent-1", ConversationID: "conv-thread",
	}); err != nil {
		t.Fatalf("insert thread: %v", err)
	}
	if err := s.CreateConversation(ctx, "conv-run", "Scheduled", "agent-1", "", "claude", ""); err != nil {
		t.Fatalf("create run conversation: %v", err)
	}
	if err := s.RecordCronJobRun(ctx, "cron-1", time.Now(), "conv-run", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{
		ID: "msg-scheduled", ConversationID: "conv-run", Role: "user", Content: "Run task",
	}); err != nil {
		t.Fatalf("save scheduled prompt: %v", err)
	}

	// The scheduler's own turn relays even though it is already persisted —
	// the guard must exclude the asking message itself, not merely "no messages".
	if _, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-scheduled"); err != nil {
		t.Fatalf("scheduled turn should relay: %v", err)
	}

	// last_conversation_id keeps pointing here until the job fires again, so a
	// human follow-up in the SPA would otherwise be pushed into the thread
	// wearing the scheduled-task label.
	if err := s.SaveMessage(ctx, Message{
		ID: "msg-followup", ConversationID: "conv-run", Role: "user", Content: "And the second half?",
	}); err != nil {
		t.Fatalf("save follow-up: %v", err)
	}
	if _, err := s.CronBotDeliveryTarget(ctx, "conv-run", "msg-followup"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("human follow-up should not relay, got %v", err)
	}
}
