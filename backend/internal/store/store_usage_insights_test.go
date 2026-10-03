package store

import (
	"context"
	"testing"
	"time"
)

func seedUsageInsights(t *testing.T) (*SQLiteStore, context.Context) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "owner-1", Name: "Alice", Username: "alice", Email: "alice@example.com"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateUser(ctx, User{ID: "agent-1", OwnerID: "owner-1", Name: "Research agent"}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := s.CreateConversation(ctx, "conv-manual", "Manual analysis", "agent-1", t.TempDir(), "claude", "opus"); err != nil {
		t.Fatalf("create manual conversation: %v", err)
	}
	if err := s.CreateConversationRecord(ctx, NewConversation{
		ID: "conv-cron", Title: "Nightly report", UserID: "agent-1", WorkDir: t.TempDir(),
		Provider: "codex", Model: "gpt", SourceType: "cron", CronJobID: "cron-1",
	}); err != nil {
		t.Fatalf("create cron conversation: %v", err)
	}
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for _, event := range []UsageEvent{
		{ID: "e1", EventID: "turn-1", ConversationID: "conv-manual", AgentID: "agent-1", Provider: "claude", Model: "opus", SourceType: "manual", OccurredAt: now, Day: "2026-09-24", UserInstructionCount: 1, ModelRequestCount: 24, ToolCallCount: 3, InputTokens: 100, CacheReadInputTokens: 9_600, CacheCreationInputTokens: 100, OutputTokens: 200, CostUSD: 12, ContextUsedTokens: 850, ContextWindowTokens: 1000, RequestCountScope: "provider_reported"},
		{ID: "e2", EventID: "turn-2", ConversationID: "conv-cron", AgentID: "agent-1", Provider: "codex", Model: "gpt", SourceType: "cron", CronJobID: "cron-1", OccurredAt: now.Add(time.Hour), Day: "2026-09-24", UserInstructionCount: 1, ModelRequestCount: 1, ToolCallCount: 40, InputTokens: 500, CacheReadInputTokens: 100, OutputTokens: 80, ReasoningOutputTokens: 25, CostUSD: 2, RequestCountScope: "billing_event"},
	} {
		if err := s.RecordUsageEvent(ctx, event); err != nil {
			t.Fatalf("record %s: %v", event.ID, err)
		}
	}
	return s, ctx
}

func TestQueryUsageInsightsAggregatesOwnershipAndComponents(t *testing.T) {
	t.Parallel()
	s, ctx := seedUsageInsights(t)
	got, err := s.QueryUsageInsights(ctx, UsageInsightsQuery{
		Start: "2026-09-24", End: "2026-09-24", OwnerID: "owner-1",
		GroupBy: "day", RankBy: "conversation", Page: 1, PageSize: 20,
	}, UsageThresholds{CacheReadRatio: .95, ModelRequests: 20, ToolCalls: 30, ContextWarningRatio: .8, ConversationCostUSD: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.Summary.TotalTokens != 10_680 || got.Summary.UserInstructions != 2 || got.Summary.ActiveConversations != 2 {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if got.Summary.ModelRequests != 25 || got.Summary.ToolCalls != 43 {
		t.Fatalf("request/tool counts = %d/%d", got.Summary.ModelRequests, got.Summary.ToolCalls)
	}
	if len(got.Composition) != 1 || got.Composition[0].ReasoningOutputTokens != 25 {
		t.Fatalf("composition = %+v", got.Composition)
	}
	if got.TotalRows != 2 || len(got.Rankings) != 2 {
		t.Fatalf("ranking page = %d/%d", len(got.Rankings), got.TotalRows)
	}
	if got.Facets.Users[0].ID != "owner-1" || got.Facets.Agents[0].ID != "agent-1" {
		t.Fatalf("ownership facets = %+v / %+v", got.Facets.Users, got.Facets.Agents)
	}
}

func TestQueryUsageInsightsFiltersManualAndCron(t *testing.T) {
	t.Parallel()
	s, ctx := seedUsageInsights(t)
	thresholds := UsageThresholds{CacheReadRatio: .95, ModelRequests: 20, ToolCalls: 30, ContextWarningRatio: .8, ConversationCostUSD: 10}
	for _, tc := range []struct {
		source, conversation string
		tools                int64
	}{
		{"manual", "conv-manual", 3},
		{"cron", "conv-cron", 40},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got, err := s.QueryUsageInsights(ctx, UsageInsightsQuery{Start: "2026-09-24", End: "2026-09-24", SourceType: tc.source, RankBy: "conversation", GroupBy: "conversation"}, thresholds)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Rankings) != 1 || got.Rankings[0].ConversationID != tc.conversation || got.Summary.ToolCalls != tc.tools {
				t.Fatalf("filtered result = %+v", got)
			}
		})
	}
}

func TestQueryUsageInsightsExplainsAnomaliesAndPaginates(t *testing.T) {
	t.Parallel()
	s, ctx := seedUsageInsights(t)
	got, err := s.QueryUsageInsights(ctx, UsageInsightsQuery{Start: "2026-09-24", End: "2026-09-24", RankBy: "conversation", SortBy: "cost", Page: 1, PageSize: 1}, UsageThresholds{CacheReadRatio: .95, ModelRequests: 20, ToolCalls: 30, ContextWarningRatio: .8, ConversationCostUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalRows != 2 || len(got.Rankings) != 1 || got.Rankings[0].ConversationID != "conv-manual" {
		t.Fatalf("page = %+v", got)
	}
	codes := map[string]bool{}
	for _, anomaly := range got.Rankings[0].Anomalies {
		codes[anomaly.Code] = true
	}
	for _, code := range []string{"high_cache_read", "high_model_requests", "context_near_compact", "high_cost"} {
		if !codes[code] {
			t.Errorf("missing anomaly %q in %+v", code, got.Rankings[0].Anomalies)
		}
	}
	aggregated, err := s.QueryUsageInsights(ctx, UsageInsightsQuery{
		Start: "2026-09-24", End: "2026-09-24", RankBy: "user",
	}, UsageThresholds{CacheReadRatio: .1, ModelRequests: 1, ToolCalls: 1, ContextWarningRatio: .1, ConversationCostUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregated.Rankings) != 1 || len(aggregated.Rankings[0].Anomalies) != 0 {
		t.Fatalf("aggregate ranking must not apply per-task anomaly thresholds: %+v", aggregated.Rankings)
	}
}

func TestHistoricalUsageEventNeverInventsTaskCounts(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RecordUsageEvent(ctx, UsageEvent{ID: "legacy", EventID: "legacy", AgentID: "old-agent", Model: "opus", SourceType: "historical", Day: "2026-09-20", InputTokens: 1000, HistoricalEstimate: true, RequestCountScope: "historical_unavailable"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryUsageInsights(ctx, UsageInsightsQuery{Start: "2026-09-20", End: "2026-09-20", RankBy: "agent"}, UsageThresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.UserInstructions != 0 || got.Summary.ModelRequests != 0 || !got.Summary.HistoricalEstimate {
		t.Fatalf("historical summary = %+v", got.Summary)
	}
	if len(got.Rankings) != 1 || !got.Rankings[0].HistoricalEstimate || got.Rankings[0].RequestCountScope != "historical_unavailable" {
		t.Fatalf("historical ranking = %+v", got.Rankings)
	}
}

func TestQueryUsageInsightsEmptyKeepsFacetArrays(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	got, err := s.QueryUsageInsights(context.Background(), UsageInsightsQuery{
		Start: "2026-09-20", End: "2026-09-20",
	}, UsageThresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Composition == nil || got.Rankings == nil || got.Facets.Users == nil ||
		got.Facets.Agents == nil || got.Facets.Providers == nil || got.Facets.Models == nil ||
		got.Facets.Conversations == nil || got.Facets.CronJobs == nil {
		t.Fatalf("empty response must serialize collections as arrays: %+v", got)
	}
}

func TestUsageRequestAnomalyUsesSingleTaskMaximum(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, event := range []UsageEvent{
		{ID: "task-1", EventID: "task-1", ConversationID: "conv-1", AgentID: "agent-1", Day: "2026-09-24", ModelRequestCount: 12, ToolCallCount: 15},
		{ID: "task-2", EventID: "task-2", ConversationID: "conv-1", AgentID: "agent-1", Day: "2026-09-24", ModelRequestCount: 11, ToolCallCount: 16},
	} {
		if err := s.RecordUsageEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.QueryUsageInsights(ctx, UsageInsightsQuery{
		Start: "2026-09-24", End: "2026-09-24", RankBy: "conversation",
	}, UsageThresholds{ModelRequests: 20, ToolCalls: 30, CacheReadRatio: .95, ContextWarningRatio: .8, ConversationCostUSD: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rankings) != 1 || got.Rankings[0].ModelRequests != 23 || got.Rankings[0].ToolCalls != 31 {
		t.Fatalf("ranking totals = %+v", got.Rankings)
	}
	if len(got.Rankings[0].Anomalies) != 0 {
		t.Fatalf("separate tasks below thresholds must not alert on their period sum: %+v", got.Rankings[0].Anomalies)
	}
}
