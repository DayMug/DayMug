package storetest

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// A test double that persists more than production does is worse than no double
// at all: it lets wrong code pass. These tests run one set of assertions against
// both implementations so a divergence fails on the Fake rather than surviving
// until someone hits it in production.
//
// Each domain interface of store.Store gets at least one core read and one
// core write here. MaintenanceStore is the exception: its results (engine name,
// file size, reclaimed bytes) are engine-specific by contract.
type conformanceStore = store.Store

type implCase struct {
	name  string
	build func(t *testing.T) conformanceStore
}

func conformanceImpls() []implCase {
	return []implCase{
		{name: "fake", build: func(*testing.T) conformanceStore { return New() }},
		{name: "sqlite", build: func(t *testing.T) conformanceStore {
			t.Helper()
			s, err := store.NewSQLiteStore(":memory:")
			if err != nil {
				t.Fatalf("new sqlite store: %v", err)
			}
			if err := s.Init(); err != nil {
				t.Fatalf("init: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		}},
	}
}

// UpdateUser writes the self-service editable fields and nothing else. work_dir
// (admin-only, SetUserWorkDir), env (SetUserEnv) and bark_url (SetUserBarkURL)
// all have their own owners; a caller that leaves them zero must not blank them.
func TestUpdateUserColumnSetMatchesSQLite(t *testing.T) {
	const (
		seededWorkDir  = "/home/alice"
		seededEnv      = "GH_TOKEN=abc"
		seededBark     = "https://bark.example/key"
		seededPassword = "hash"
	)
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			if err := s.CreateUser(ctx, store.User{
				ID: "u1", Name: "Alice", Username: "alice", PasswordHash: seededPassword,
				WorkDir: seededWorkDir, Avatar: "A", Env: seededEnv,
				BarkURL: seededBark,
			}); err != nil {
				t.Fatalf("create: %v", err)
			}

			// Every field the update path does not own is set to a value the
			// store must ignore, so a stray column in the UPDATE list shows up
			// as a changed value rather than as silence.
			if err := s.UpdateUser(ctx, store.User{
				ID: "u1", Name: "Alice II", Avatar: "B",
				RoleDefinition: "Backend", McpConfig: `{"a":1}`,
				ClaudeMdContent: "# hi", ManageClaudeMd: true, DefaultModel: "some-model",
				WorkDir: "/moved", Env: "GH_TOKEN=zzz", BarkURL: "https://evil.example",
			}); err != nil {
				t.Fatalf("update: %v", err)
			}

			got, err := s.GetUser(ctx, "u1")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			for _, f := range []struct{ field, got, want string }{
				{"name", got.Name, "Alice II"},
				{"avatar", got.Avatar, "B"},
				{"role_definition", got.RoleDefinition, ""},
				{"mcp_config", got.McpConfig, ""},
				{"claude_md_content", got.ClaudeMdContent, ""},
				{"default_model", got.DefaultModel, "some-model"},
				{"work_dir", got.WorkDir, seededWorkDir},
				{"env", got.Env, seededEnv},
				{"bark_url", got.BarkURL, seededBark},
			} {
				if f.got != f.want {
					t.Errorf("%s = %q, want %q", f.field, f.got, f.want)
				}
			}
			if got.ManageClaudeMd {
				t.Error("manage_claude_md = true, want false for a human")
			}
		})
	}
}

// UpdateBot's statement omits created_at, so the stored timestamp survives a
// caller that passes the zero value.
func TestUpdateBotPreservesCreatedAt(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			if err := s.CreateBot(ctx, store.Bot{
				ID: "b1", AgentID: "a1", Name: "Scout bot", Platform: "slack",
				CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			}); err != nil {
				t.Fatalf("create bot: %v", err)
			}
			created, err := s.GetBot(ctx, "b1")
			if err != nil {
				t.Fatalf("get bot: %v", err)
			}
			if created.CreatedAt.IsZero() {
				t.Fatal("created_at is zero after create; the assertion below would be vacuous")
			}

			if err := s.UpdateBot(ctx, store.Bot{
				ID: "b1", AgentID: "a1", Name: "Renamed", Platform: "slack", Enabled: true,
			}); err != nil {
				t.Fatalf("update bot: %v", err)
			}

			got, err := s.GetBot(ctx, "b1")
			if err != nil {
				t.Fatalf("get bot after update: %v", err)
			}
			if got.Name != "Renamed" || !got.Enabled {
				t.Errorf("bot = %+v, want name Renamed and enabled", got)
			}
			if !got.CreatedAt.Equal(created.CreatedAt) {
				t.Errorf("created_at = %v, want %v", got.CreatedAt, created.CreatedAt)
			}
		})
	}
}

func createHumanAndAgent(t *testing.T, s conformanceStore) (human, agent store.User) {
	t.Helper()
	ctx := context.Background()
	human = store.User{ID: "h1", Name: "Alice", Username: "alice"}
	agent = store.User{ID: "a1", Name: "Researcher", OwnerID: "h1"}
	for _, u := range []store.User{human, agent} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
	}
	return human, agent
}

func TestUserArchiveRoundTrip(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)

			if err := s.ArchiveUser(ctx, "a1"); err != nil {
				t.Fatalf("archive: %v", err)
			}
			if err := s.ArchiveUser(ctx, "a1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("archive twice = %v, want ErrNotFound", err)
			}
			archived, err := s.ListArchivedAgents(ctx, "h1")
			if err != nil || len(archived) != 1 || archived[0].ID != "a1" || !archived[0].Archived {
				t.Fatalf("archived = %+v, %v; want [a1]", archived, err)
			}
			if err := s.UnarchiveUser(ctx, "a1"); err != nil {
				t.Fatalf("unarchive: %v", err)
			}
			if archived, _ := s.ListArchivedAgents(ctx, "h1"); len(archived) != 0 {
				t.Errorf("archived after unarchive = %+v, want none", archived)
			}
			if _, err := s.GetUserByUsername(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("unknown username = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestProviderAccountsListDefaultFirst(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)

			if err := s.SetUserProviderAccounts(ctx, "h1", "claude", []string{"acc-a", "acc-b", "acc-c"}, "acc-b"); err != nil {
				t.Fatalf("set accounts: %v", err)
			}
			accounts, err := s.GetUserProviderAccounts(ctx, "h1")
			if err != nil {
				t.Fatalf("get accounts: %v", err)
			}
			if want := []string{"acc-b", "acc-a", "acc-c"}; !reflect.DeepEqual(accounts["claude"], want) {
				t.Errorf("accounts = %v, want %v", accounts["claude"], want)
			}
			bindings, err := s.GetUserProviderBindings(ctx, "h1")
			if err != nil || bindings["claude"] != "acc-b" {
				t.Errorf("bindings = %v, %v; want claude=acc-b", bindings, err)
			}
			if err := s.SetUserProviderBinding(ctx, "ghost", "claude", "acc-a"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("bind unknown user = %v, want ErrNotFound", err)
			}
			if err := s.DeleteUserProviderBinding(ctx, "h1", "claude"); err != nil {
				t.Fatalf("delete binding: %v", err)
			}
			if bindings, _ := s.GetUserProviderBindings(ctx, "h1"); len(bindings) != 0 {
				t.Errorf("bindings after delete = %v, want empty", bindings)
			}
		})
	}
}

func TestSessionExpiryReadsAsNotFound(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			now := time.Now()
			for _, sess := range []store.Session{
				{Token: "live", UserID: "h1", ExpiresAt: now.Add(time.Hour)},
				{Token: "stale", UserID: "h1", ExpiresAt: now.Add(-time.Hour)},
			} {
				if err := s.CreateSession(ctx, sess); err != nil {
					t.Fatalf("create session: %v", err)
				}
			}
			if got, err := s.GetSession(ctx, "live"); err != nil || got.UserID != "h1" {
				t.Errorf("live session = %+v, %v", got, err)
			}
			if _, err := s.GetSession(ctx, "stale"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("expired session = %v, want ErrNotFound", err)
			}
			if err := s.DeleteSession(ctx, "live"); err != nil {
				t.Fatalf("delete session: %v", err)
			}
			if _, err := s.GetSession(ctx, "live"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("deleted session = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestConversationCreateReadDelete(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			if err := s.CreateConversation(ctx, "c1", "Plan", "a1", "/work", "claude", "opus"); err != nil {
				t.Fatalf("create: %v", err)
			}
			got, err := s.GetConversation(ctx, "c1")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Title != "Plan" || got.UserID != "a1" || got.WorkDir != "/work" || got.Provider != "claude" ||
				got.Model != "opus" || !got.NotificationsEnabled || got.Pinned || got.SessionID == "" {
				t.Errorf("conversation = %+v", got)
			}
			if err := s.UpdateConversationPinned(ctx, "c1", true); err != nil {
				t.Fatalf("pin: %v", err)
			}
			if got, _ := s.GetConversation(ctx, "c1"); !got.Pinned {
				t.Error("pinned = false after pin")
			}
			if err := s.UpdateConversationPinned(ctx, "missing", true); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("pin missing = %v, want ErrNotFound", err)
			}
			if err := s.DeleteConversation(ctx, "c1"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := s.GetConversation(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("deleted conversation = %v, want ErrNotFound", err)
			}
			if err := s.DeleteConversation(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("delete twice = %v, want ErrNotFound", err)
			}
		})
	}
}

func messageIDs(msgs []store.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func TestMessageCursorPagingAndImportIdempotence(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			if err := s.CreateConversation(ctx, "c1", "", "a1", "", "claude", ""); err != nil {
				t.Fatalf("create conversation: %v", err)
			}
			for _, m := range []store.Message{
				{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi"},
				{ID: "m2", ConversationID: "c1", Role: "assistant", Content: "hello"},
				{ID: "m3", ConversationID: "c1", Role: "user", Content: "bye"},
			} {
				if err := s.SaveMessage(ctx, m); err != nil {
					t.Fatalf("save %s: %v", m.ID, err)
				}
			}
			after, err := s.ListMessagesAfter(ctx, "c1", "m1", 10)
			if err != nil || !reflect.DeepEqual(messageIDs(after), []string{"m2", "m3"}) {
				t.Errorf("after m1 = %v, %v; want [m2 m3]", messageIDs(after), err)
			}
			before, err := s.ListMessagesBefore(ctx, "c1", "m3", 10)
			if err != nil || !reflect.DeepEqual(messageIDs(before), []string{"m1", "m2"}) {
				t.Errorf("before m3 = %v, %v; want [m1 m2]", messageIDs(before), err)
			}
			if latest, err := s.LatestAssistantMessage(ctx, "c1"); err != nil || latest.ID != "m2" {
				t.Errorf("latest assistant = %+v, %v; want m2", latest, err)
			}

			imported := store.Message{ConversationID: "c1", Role: "user", Content: "from IM", SourceID: "im-42"}
			at := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
			if inserted, err := s.SaveImportedMessage(ctx, imported, at); err != nil || !inserted {
				t.Errorf("first import = %v, %v; want inserted", inserted, err)
			}
			if inserted, err := s.SaveImportedMessage(ctx, imported, at); err != nil || inserted {
				t.Errorf("replayed import = %v, %v; want ignored", inserted, err)
			}
		})
	}
}

func TestPromptQueueClaimCancelAndDrain(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			if err := s.CreateConversation(ctx, "c1", "", "a1", "", "claude", ""); err != nil {
				t.Fatalf("create conversation: %v", err)
			}
			first, err := s.EnqueuePrompt(ctx, "c1", "one")
			if err != nil || first.QueueStatus != "pending" || first.Role != "user" {
				t.Fatalf("enqueue = %+v, %v", first, err)
			}
			second, err := s.EnqueuePrompt(ctx, "c1", "two")
			if err != nil {
				t.Fatalf("enqueue second: %v", err)
			}
			if peeked, err := s.PeekNextPendingPrompt(ctx, "c1"); err != nil || peeked.ID != first.ID {
				t.Fatalf("peek = %+v, %v; want first", peeked, err)
			}
			claimed, err := s.ClaimPendingPromptByID(ctx, first.ID)
			if err != nil || claimed.QueueStatus != "processing" {
				t.Fatalf("claim = %+v, %v; want processing", claimed, err)
			}
			if peeked, err := s.PeekNextPendingPrompt(ctx, "c1"); err != nil || peeked.ID != second.ID {
				t.Errorf("peek after claim = %+v, %v; want second", peeked, err)
			}
			if convs, err := s.ListConversationsWithPending(ctx); err != nil || !reflect.DeepEqual(convs, []string{"c1"}) {
				t.Errorf("with pending = %v, %v; want [c1]", convs, err)
			}
			// Cancel removes only what is still pending; the running prompt owns
			// history and survives.
			deleted, err := s.DeletePendingPrompts(ctx, "c1")
			if err != nil || !reflect.DeepEqual(messageIDs(deleted), []string{second.ID}) {
				t.Errorf("cancel = %v, %v; want [second]", messageIDs(deleted), err)
			}
			if _, err := s.ClaimPendingPromptByID(ctx, second.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("claim cancelled = %v, want ErrNotFound", err)
			}
			if err := s.MarkPromptDone(ctx, first.ID); err != nil {
				t.Fatalf("mark done: %v", err)
			}
			if convs, err := s.ListConversationsWithPending(ctx); err != nil || len(convs) != 0 {
				t.Errorf("with pending after drain = %v, %v; want none", convs, err)
			}
		})
	}
}

func TestAppSettingUnsetReadsEmpty(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			if v, err := s.GetAppSetting(ctx, "help_doc"); err != nil || v != "" {
				t.Errorf("unset = %q, %v; want empty", v, err)
			}
			for _, want := range []string{"v1", "v2"} {
				if err := s.SetAppSetting(ctx, "help_doc", want); err != nil {
					t.Fatalf("set: %v", err)
				}
				if v, err := s.GetAppSetting(ctx, "help_doc"); err != nil || v != want {
					t.Errorf("get = %q, %v; want %q", v, err, want)
				}
			}
		})
	}
}

func TestBotThreadUpsertAndCaseHistory(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			if _, err := s.GetBotThread(ctx, "slack", "C1", "T1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("unknown thread = %v, want ErrNotFound", err)
			}
			thread := store.BotThread{Platform: "slack", ChannelID: "C1", ThreadID: "T1", AgentID: "a1", ConversationID: "c1", SessionID: "s1"}
			if err := s.UpsertBotThread(ctx, thread); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			thread.SessionID = "s2"
			if err := s.UpsertBotThread(ctx, thread); err != nil {
				t.Fatalf("re-upsert: %v", err)
			}
			if got, err := s.GetBotThread(ctx, "slack", "C1", "T1"); err != nil || got.SessionID != "s2" || got.ConversationID != "c1" {
				t.Errorf("thread = %+v, %v; want session s2", got, err)
			}

			if c, err := s.GetThreadCase(ctx, "C1", "T1"); err != nil || c.Version != 0 {
				t.Errorf("fresh case = %+v, %v; want version 0", c, err)
			}
			for _, doc := range []string{"draft", "final"} {
				if _, err := s.SaveThreadCase(ctx, "C1", "T1", doc, "a1"); err != nil {
					t.Fatalf("save case: %v", err)
				}
			}
			history, err := s.ListThreadCaseHistory(ctx, "C1", "T1", 10)
			if err != nil || len(history) != 2 || history[0].Version != 2 || history[0].Doc != "final" || history[1].Version != 1 {
				t.Errorf("history = %+v, %v; want [v2 final, v1]", history, err)
			}
		})
	}
}

func TestTokenUsageCollapsesPerUserModelDay(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			for _, d := range []store.TokenUsageDelta{
				{UserID: "a1", Model: "opus", Day: "2026-09-01", InputTokens: 10, OutputTokens: 1, CostUSD: 0.5},
				{UserID: "a1", Model: "opus", Day: "2026-09-01", InputTokens: 20, OutputTokens: 2, CostUSD: 0.25},
				{UserID: "a1", Model: "haiku", Day: "2026-09-02", InputTokens: 5},
				{UserID: "b1", Model: "gpt", Day: "2026-09-01", InputTokens: 7},
			} {
				if err := s.AddTokenUsage(ctx, d); err != nil {
					t.Fatalf("add usage: %v", err)
				}
			}
			if err := s.AddTokenUsage(ctx, store.TokenUsageDelta{Model: "opus"}); err == nil {
				t.Error("usage without a user was accepted")
			}
			rows, err := s.AggregateTokenUsage(ctx, store.TokenUsageQuery{UserID: "a1", StartUTC: "2026-09-01", EndUTC: "2026-09-01"})
			if err != nil || len(rows) != 1 {
				t.Fatalf("aggregate = %+v, %v; want one row", rows, err)
			}
			if r := rows[0]; r.Model != "opus" || r.InputTokens != 30 || r.OutputTokens != 3 || r.CostUSD != 0.75 || r.Turns != 2 {
				t.Errorf("row = %+v", r)
			}
			if models, err := s.ListTokenUsageModels(ctx); err != nil || !reflect.DeepEqual(models, []string{"gpt", "haiku", "opus"}) {
				t.Errorf("models = %v, %v; want sorted distinct", models, err)
			}
		})
	}
}

func TestUsageInsightsAttributeToOwnerAndPage(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			for _, c := range []struct{ id, title string }{{"c1", "Cheap"}, {"c2", "Pricey"}} {
				if err := s.CreateConversation(ctx, c.id, c.title, "a1", "", "claude", "opus"); err != nil {
					t.Fatalf("create conversation: %v", err)
				}
			}
			at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
			for _, e := range []store.UsageEvent{
				{ID: "u1", EventID: "e1", ConversationID: "c1", AgentID: "a1", Provider: "claude", Model: "opus", OccurredAt: at,
					UserInstructionCount: 1, ModelRequestCount: 2, ToolCallCount: 3,
					InputTokens: 100, CacheReadInputTokens: 50, OutputTokens: 50, CostUSD: 1},
				{ID: "u2", EventID: "e2", ConversationID: "c2", AgentID: "a1", Provider: "claude", Model: "opus", OccurredAt: at.Add(time.Hour),
					UserInstructionCount: 1, InputTokens: 300, CostUSD: 3},
				// Previous period: feeds the comparison, not the totals.
				{ID: "u3", EventID: "e3", ConversationID: "c1", AgentID: "a1", Provider: "claude", Model: "opus", OccurredAt: at.AddDate(0, 0, -30),
					InputTokens: 100, CostUSD: 2},
			} {
				if err := s.RecordUsageEvent(ctx, e); err != nil {
					t.Fatalf("record: %v", err)
				}
			}
			if err := s.RecordUsageEvent(ctx, store.UsageEvent{ID: "u1", EventID: "e1", AgentID: "a1", CostUSD: 99, OccurredAt: at}); err != nil {
				t.Fatalf("replayed record: %v", err)
			}

			if got, err := s.ConversationCostUSD(ctx, "c1"); err != nil || got != 3 {
				t.Errorf("conversation cost c1 = %v, %v; want 3 across every period", got, err)
			}
			if got, err := s.ConversationCostUSD(ctx, "missing"); err != nil || got != 0 {
				t.Errorf("conversation cost of unknown conversation = %v, %v; want 0", got, err)
			}

			q := store.UsageInsightsQuery{Start: "2026-09-01", End: "2026-09-30", ScopeOwnerID: "h1", SortBy: "cost"}
			got, err := s.QueryUsageInsights(ctx, q, store.UsageThresholds{})
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			sum := got.Summary
			if sum.TotalTokens != 500 || sum.CostUSD != 4 || sum.UserInstructions != 2 || sum.ActiveConversations != 2 ||
				sum.ModelRequests != 2 || sum.ToolCalls != 3 || sum.CacheReadRatio != 0.1 {
				t.Errorf("summary = %+v", sum)
			}
			if sum.Comparison.CostUSD == nil || *sum.Comparison.CostUSD != 100 {
				t.Errorf("cost comparison = %v, want +100%%", sum.Comparison.CostUSD)
			}
			if got.TotalRows != 2 || len(got.Rankings) != 2 || got.Rankings[0].Key != "c2" || got.Rankings[1].Key != "c1" {
				t.Fatalf("rankings = %+v (total %d); want c2 then c1", got.Rankings, got.TotalRows)
			}
			if r := got.Rankings[1]; r.ConversationTitle != "Cheap" || r.OwnerID != "h1" || r.OwnerName != "Alice" ||
				r.AgentName != "Researcher" || r.MaxTaskToolCalls != 3 {
				t.Errorf("ranking row = %+v", r)
			}
			if got.GroupBy != "day" || got.RankBy != "conversation" || len(got.Composition) != 1 || got.Composition[0].Key != "2026-09-10" {
				t.Errorf("composition = %+v (group %q rank %q)", got.Composition, got.GroupBy, got.RankBy)
			}
			if len(got.Facets.Conversations) != 2 || len(got.Facets.Agents) != 1 || got.Facets.Agents[0].Label != "Researcher" {
				t.Errorf("facets = %+v", got.Facets)
			}

			q.Page, q.PageSize = 2, 1
			if paged, err := s.QueryUsageInsights(ctx, q, store.UsageThresholds{}); err != nil || len(paged.Rankings) != 1 || paged.Rankings[0].Key != "c1" {
				t.Errorf("page 2 = %+v, %v; want [c1]", paged.Rankings, err)
			}
			q.ScopeOwnerID, q.Page, q.PageSize = "someone-else", 0, 0
			if other, err := s.QueryUsageInsights(ctx, q, store.UsageThresholds{}); err != nil || other.Summary.TotalTokens != 0 || len(other.Rankings) != 0 {
				t.Errorf("foreign scope = %+v, %v; want empty", other, err)
			}
		})
	}
}

func TestCronJobLifecycle(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			job := store.CronJob{
				ID: "j1", OwnerID: "h1", AgentID: "a1", Model: "opus", Expression: "0 9 * * *", Timezone: "UTC",
				Description: "daily", Prompt: "report", Enabled: true, NotificationsEnabled: true,
				LastError: "ignored on insert",
			}
			if err := s.CreateCronJob(ctx, job); err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := s.CreateCronJob(ctx, store.CronJob{ID: "j2", OwnerID: "b1", AgentID: "x", Expression: "* * * * *", Prompt: "p"}); err != nil {
				t.Fatalf("create other: %v", err)
			}
			got, err := s.GetCronJob(ctx, "j1")
			if err != nil || got.AgentName != "Researcher" || got.CreatedAt.IsZero() || got.LastError != "" || got.LastRunAt != nil {
				t.Fatalf("get = %+v, %v", got, err)
			}
			if jobs, err := s.ListCronJobs(ctx, "h1"); err != nil || len(jobs) != 1 || jobs[0].ID != "j1" {
				t.Errorf("list h1 = %+v, %v; want [j1]", jobs, err)
			}
			if jobs, err := s.ListEnabledCronJobs(ctx); err != nil || len(jobs) != 1 || jobs[0].ID != "j1" {
				t.Errorf("enabled = %+v, %v; want [j1]", jobs, err)
			}

			foreign := got
			foreign.OwnerID = "b1"
			if err := s.UpdateCronJob(ctx, foreign); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("foreign update = %v, want ErrNotFound", err)
			}
			got.Prompt = "weekly report"
			if err := s.UpdateCronJob(ctx, got); err != nil {
				t.Fatalf("update: %v", err)
			}
			if err := s.DisableCronJob(ctx, "j1", "invalid_schedule"); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if jobs, _ := s.ListEnabledCronJobs(ctx); len(jobs) != 0 {
				t.Errorf("enabled after disable = %+v", jobs)
			}
			runAt := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
			if err := s.RecordCronJobRun(ctx, "j1", runAt, "conv-9", "boom"); err != nil {
				t.Fatalf("record run: %v", err)
			}
			got, err = s.GetCronJob(ctx, "j1")
			if err != nil || got.Prompt != "weekly report" || got.Enabled || got.DisabledReason != "invalid_schedule" ||
				got.LastRunAt == nil || !got.LastRunAt.Equal(runAt) || got.LastConversationID != "conv-9" || got.LastError != "boom" {
				t.Errorf("after run = %+v, %v", got, err)
			}
			if err := s.RecordCronJobRun(ctx, "missing", runAt, "", ""); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("record missing = %v, want ErrNotFound", err)
			}
			if err := s.DeleteCronJob(ctx, "j1", "b1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("foreign delete = %v, want ErrNotFound", err)
			}
			if err := s.DeleteCronJob(ctx, "j1", "h1"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := s.GetCronJob(ctx, "j1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("deleted job = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestMarketplaceAppKeepsPublisher(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			app, err := s.CreateMarketplaceApp(ctx, store.MarketplaceApp{Name: "Board", URL: "https://board.example", CreatedBy: "h1"})
			if err != nil || app.ID == "" || app.CreatedByName != "Alice" || app.CreatedAt.IsZero() {
				t.Fatalf("create = %+v, %v", app, err)
			}
			if ghost, err := s.CreateMarketplaceApp(ctx, store.MarketplaceApp{Name: "Old", URL: "https://old.example", CreatedBy: "ghost"}); err != nil || ghost.CreatedByName != "ghost" {
				t.Errorf("unknown publisher = %+v, %v; want name falls back to id", ghost, err)
			}
			updated, err := s.UpdateMarketplaceApp(ctx, store.MarketplaceApp{ID: app.ID, Name: "Board v2", URL: app.URL, CreatedBy: "a1"})
			if err != nil || updated.Name != "Board v2" || updated.CreatedBy != "h1" {
				t.Errorf("update = %+v, %v; want renamed, publisher kept", updated, err)
			}
			if _, err := s.UpdateMarketplaceApp(ctx, store.MarketplaceApp{ID: "missing", Name: "x"}); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("update missing = %v, want ErrNotFound", err)
			}
			if apps, err := s.ListMarketplaceApps(ctx); err != nil || len(apps) != 2 {
				t.Errorf("list = %+v, %v; want 2", apps, err)
			}
			if err := s.DeleteMarketplaceApp(ctx, app.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if err := s.DeleteMarketplaceApp(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("delete twice = %v, want ErrNotFound", err)
			}
		})
	}
}

// The web-mirror bridge decides from these two reads whether a conversation's
// output also goes to an IM thread, so the Fake must answer them the way the
// SQLite joins do.
func TestBotConversationAndCronDeliveryLookup(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			for _, id := range []string{"chat-c", "cron-c"} {
				if err := s.CreateConversation(ctx, id, "", "a1", "", "claude", ""); err != nil {
					t.Fatalf("create conversation %s: %v", id, err)
				}
			}
			if err := s.CreateBot(ctx, store.Bot{ID: "b1", AgentID: "a1", Name: "Ops", Platform: "slack"}); err != nil {
				t.Fatalf("create bot: %v", err)
			}
			thread := store.BotThread{Platform: "slack@a1@b1", ChannelID: "C1", ThreadID: "T1", AgentID: "a1", ConversationID: "chat-c"}
			if err := s.UpsertBotThread(ctx, thread); err != nil {
				t.Fatalf("upsert thread: %v", err)
			}

			if ok, err := s.IsBotConversation(ctx, "chat-c"); err != nil || !ok {
				t.Errorf("IsBotConversation(chat-c) = %v, %v; want true", ok, err)
			}
			if ok, err := s.IsBotConversation(ctx, "cron-c"); err != nil || ok {
				t.Errorf("IsBotConversation(cron-c) = %v, %v; want false", ok, err)
			}

			if _, err := s.CronBotDeliveryTarget(ctx, "cron-c", "p1"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("before any job run = %v, want ErrNotFound", err)
			}
			if err := s.CreateCronJob(ctx, store.CronJob{
				ID: "j1", OwnerID: "h1", AgentID: "a1", Expression: "0 9 * * *", Prompt: "report",
				Enabled: true, DeliverToBot: true, BotID: "b1",
			}); err != nil {
				t.Fatalf("create job: %v", err)
			}
			if err := s.RecordCronJobRun(ctx, "j1", time.Now().UTC(), "cron-c", ""); err != nil {
				t.Fatalf("record run: %v", err)
			}
			if err := s.SaveMessage(ctx, store.Message{ID: "p1", ConversationID: "cron-c", Role: "user", Content: "report"}); err != nil {
				t.Fatalf("save prompt: %v", err)
			}
			got, err := s.CronBotDeliveryTarget(ctx, "cron-c", "p1")
			if err != nil || got.BotID != "b1" || got.Thread.ThreadID != "T1" || got.Thread.ConversationID != "chat-c" {
				t.Errorf("delivery = %+v, %v; want thread T1 via b1", got, err)
			}

			// A human follow-up turns the run into an ordinary chat: no relay.
			if err := s.SaveMessage(ctx, store.Message{ID: "p2", ConversationID: "cron-c", Role: "user", Content: "more"}); err != nil {
				t.Fatalf("save follow-up: %v", err)
			}
			if _, err := s.CronBotDeliveryTarget(ctx, "cron-c", "p2"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("after follow-up = %v, want ErrNotFound", err)
			}
		})
	}
}

// A question a restart left unanswered is stored, read back verbatim and
// cleared with "" — the round trip the restart recovery path relies on.
func TestConversationSuspendedQuestionConformance(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			if err := s.CreateConversation(ctx, "c1", "t", "agent", "/tmp", "claude", ""); err != nil {
				t.Fatalf("create: %v", err)
			}
			if got, err := s.GetConversationSuspendedQuestion(ctx, "c1"); err != nil || got != "" {
				t.Fatalf("fresh = %q, %v; want empty", got, err)
			}
			const payload = `{"request_id":"r1"}`
			if err := s.SetConversationSuspendedQuestion(ctx, "c1", payload); err != nil {
				t.Fatalf("set: %v", err)
			}
			if got, err := s.GetConversationSuspendedQuestion(ctx, "c1"); err != nil || got != payload {
				t.Fatalf("stored = %q, %v; want %q", got, err, payload)
			}
			if err := s.SetConversationSuspendedQuestion(ctx, "c1", ""); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if got, err := s.GetConversationSuspendedQuestion(ctx, "c1"); err != nil || got != "" {
				t.Fatalf("cleared = %q, %v; want empty", got, err)
			}
		})
	}
}

// A rotated conversation's link to the one it replaced round-trips; a
// conversation that opened its thread has none.
func TestConversationRotatedFromConformance(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			for _, id := range []string{"c1", "c2"} {
				if err := s.CreateConversation(ctx, id, "t", "agent", "/tmp", "claude", ""); err != nil {
					t.Fatalf("create %s: %v", id, err)
				}
			}
			if got, err := s.GetConversationRotatedFrom(ctx, "c1"); err != nil || got != "" {
				t.Fatalf("fresh = %q, %v; want empty", got, err)
			}
			if err := s.SetConversationRotatedFrom(ctx, "c2", "c1"); err != nil {
				t.Fatalf("set: %v", err)
			}
			if got, err := s.GetConversationRotatedFrom(ctx, "c2"); err != nil || got != "c1" {
				t.Fatalf("stored = %q, %v; want c1", got, err)
			}
		})
	}
}

// ExclusiveAttachmentPaths is read right after a soft delete, so both stores
// must still see the deleted conversation's messages while ignoring the
// references of other deleted conversations.
func TestExclusiveAttachmentPaths(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			for _, id := range []string{"target", "live", "gone"} {
				if err := s.CreateConversation(ctx, id, "", "a1", "", "claude", ""); err != nil {
					t.Fatalf("create %s: %v", id, err)
				}
			}
			for _, m := range []store.Message{
				{ID: "t1", ConversationID: "target", Metadata: []byte(`{"attachments":[{"path":"up/own.png"},{"path":"up/shared.png"}]}`)},
				{ID: "t2", ConversationID: "target", Metadata: []byte(`{"attachments":[{"path":"up/own.png"},{"path":"up/orphan.png"}]}`)},
				{ID: "l1", ConversationID: "live", Metadata: []byte(`{"attachments":[{"path":"up/shared.png"}]}`)},
				{ID: "g1", ConversationID: "gone", Metadata: []byte(`{"attachments":[{"path":"up/orphan.png"}]}`)},
			} {
				m.Role = "user"
				if err := s.SaveMessage(ctx, m); err != nil {
					t.Fatalf("save %s: %v", m.ID, err)
				}
			}
			for _, id := range []string{"gone", "target"} {
				if err := s.DeleteConversation(ctx, id); err != nil {
					t.Fatalf("delete %s: %v", id, err)
				}
			}

			got, err := s.ExclusiveAttachmentPaths(ctx, "target")
			if err != nil {
				t.Fatalf("exclusive paths: %v", err)
			}
			sort.Strings(got)
			if want := []string{"up/orphan.png", "up/own.png"}; !reflect.DeepEqual(got, want) {
				t.Errorf("paths = %v, want %v", got, want)
			}
		})
	}
}

// CreateFirstAdmin writes the admin and its Agent together while no human
// exists, ignores agent-only rows, and refuses once a human is on file.
func TestCreateFirstAdminGate(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			if err := s.CreateUser(ctx, store.User{ID: "stray", Name: "Stray", OwnerID: "nobody", WorkDir: "/x"}); err != nil {
				t.Fatalf("seed agent: %v", err)
			}
			admin := store.User{ID: "u1", Name: "Alice", Username: "alice", Email: "alice@example.com", IsAdmin: true, WorkDir: "/home/alice"}
			agent := store.User{ID: "a1", Name: "alice", OwnerID: "u1", WorkDir: "/home/alice"}
			if err := s.CreateFirstAdmin(ctx, admin, agent); err != nil {
				t.Fatalf("first admin: %v", err)
			}
			got, err := s.GetUser(ctx, "u1")
			if err != nil || !got.IsAdmin || got.Username != "alice" {
				t.Fatalf("admin row = %+v, err %v", got, err)
			}
			if a, err := s.GetUser(ctx, "a1"); err != nil || a.OwnerID != "u1" {
				t.Fatalf("agent row = %+v, err %v", a, err)
			}

			err = s.CreateFirstAdmin(ctx,
				store.User{ID: "u2", Name: "Bob", Username: "bob", Email: "bob@example.com", IsAdmin: true, WorkDir: "/home/bob"},
				store.User{ID: "a2", Name: "bob", OwnerID: "u2", WorkDir: "/home/bob"})
			if !errors.Is(err, store.ErrSetupClosed) {
				t.Fatalf("second admin err = %v, want ErrSetupClosed", err)
			}
			if _, err := s.GetUser(ctx, "u2"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("refused admin was persisted (err=%v)", err)
			}
			if _, err := s.GetUser(ctx, "a2"); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("refused agent was persisted (err=%v)", err)
			}
		})
	}
}
