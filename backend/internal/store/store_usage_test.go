package store

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAddTokenUsage_SumsSameDay(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.AddTokenUsage(ctx, TokenUsageDelta{
			UserID:                   "u1",
			Model:                    "claude-opus-4-8",
			InputTokens:              100,
			OutputTokens:             50,
			CacheReadInputTokens:     10,
			CacheCreationInputTokens: 20,
			CostUSD:                  0.5,
		}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}

	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: "u1"})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (same day collapsed), got %d", len(rows))
	}
	got := rows[0]
	if got.InputTokens != 300 || got.OutputTokens != 150 ||
		got.CacheReadInputTokens != 30 || got.CacheCreationInputTokens != 60 ||
		got.Turns != 3 {
		t.Errorf("unexpected aggregate: %+v", got)
	}
	if got.CostUSD < 1.49 || got.CostUSD > 1.51 {
		t.Errorf("expected cost ~1.5, got %v", got.CostUSD)
	}
	if got.Day != time.Now().UTC().Format("2006-01-02") {
		t.Errorf("expected today's UTC date, got %s", got.Day)
	}
}

func TestAddTokenUsage_DifferentModelsAreSeparateRows(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.AddTokenUsage(ctx, TokenUsageDelta{UserID: "u1", Model: "claude-opus-4-8", InputTokens: 100}); err != nil {
		t.Fatalf("add opus: %v", err)
	}
	if err := s.AddTokenUsage(ctx, TokenUsageDelta{UserID: "u1", Model: "claude-haiku-4-5", InputTokens: 50}); err != nil {
		t.Fatalf("add haiku: %v", err)
	}
	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: "u1"})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows for 2 models, got %d", len(rows))
	}
}

func TestAddTokenUsage_EmptyModelDefaultsToUnknown(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.AddTokenUsage(ctx, TokenUsageDelta{UserID: "u1", InputTokens: 10}); err != nil {
		t.Fatalf("add: %v", err)
	}
	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: "u1"})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 || rows[0].Model != "unknown" {
		t.Errorf("expected unknown model bucket, got %+v", rows)
	}
}

func TestAddTokenUsage_RequiresUserID(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.AddTokenUsage(ctx, TokenUsageDelta{Model: "x", InputTokens: 1}); err == nil {
		t.Fatal("expected error for empty user_id, got nil")
	}
}

func TestAggregateTokenUsage_FiltersByModelAndDateRange(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	// Use AddTokenUsage to land today's rows, then directly insert older rows
	// since the day column is server-generated.
	if err := s.AddTokenUsage(ctx, TokenUsageDelta{UserID: "u1", Model: "M1", InputTokens: 1}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.AddTokenUsage(ctx, TokenUsageDelta{UserID: "u2", Model: "M2", InputTokens: 1}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO token_usage (user_id, model, day, input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens, cost_usd, turns)
VALUES ('u1', 'M1', '2020-01-01', 99, 0, 0, 0, 0, 1)`); err != nil {
		t.Fatalf("seed old row: %v", err)
	}

	today := time.Now().UTC().Format("2006-01-02")
	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{StartUTC: today, EndUTC: today})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("date filter: expected 2 today rows, got %d", len(rows))
	}

	rows, err = s.AggregateTokenUsage(ctx, TokenUsageQuery{Model: "M1"})
	if err != nil {
		t.Fatalf("aggregate by model: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("model filter: expected 2 rows for M1, got %d", len(rows))
	}
}

func TestListTokenUsageModels_DistinctSorted(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, m := range []string{"M2", "M1", "M2", "M3"} {
		if err := s.AddTokenUsage(ctx, TokenUsageDelta{UserID: "u1", Model: m}); err != nil {
			t.Fatalf("add %s: %v", m, err)
		}
	}
	models, err := s.ListTokenUsageModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	want := []string{"M1", "M2", "M3"}
	if len(models) != len(want) {
		t.Fatalf("expected %v, got %v", want, models)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("at %d: want %q, got %q", i, want[i], models[i])
		}
	}
}

func TestRecordClaudeTokenUsage_CumulativeGrowthAndDuplicateOnlyBillDelta(t *testing.T) {
	t.Parallel()
	s, conv := newClaudeUsageTestConversation(t, "cumulative")
	ctx := context.Background()
	first := ClaudeUsageEvent{
		ConversationID: conv.ID, SessionID: conv.SessionID, EventID: "turn-1",
		UserID: conv.UserID, FallbackModel: "claude-opus", Day: "2026-09-24",
		CacheReadInputTokens: 20, CacheCreationInputTokens: 10,
		PerModel: map[string]ClaudeModelUsage{
			"claude-opus": {InputTokens: 100, OutputTokens: 50, CostUSD: 1},
		},
	}
	second := first
	second.EventID = "turn-2"
	second.CacheReadInputTokens = 4
	second.CacheCreationInputTokens = 2
	second.PerModel = map[string]ClaudeModelUsage{
		"claude-opus": {InputTokens: 140, OutputTokens: 70, CostUSD: 1.4},
	}
	// The returned cost is what the call billed: the whole first snapshot,
	// then only the increase, then nothing for the repeated frame.
	for i, want := range []float64{1, 0.4, 0} {
		event := []ClaudeUsageEvent{first, second, second}[i]
		billed, err := s.RecordClaudeTokenUsage(ctx, event)
		if err != nil {
			t.Fatalf("record usage: %v", err)
		}
		if math.Abs(billed-want) > 1e-9 {
			t.Fatalf("event %d billed %v, want %v", i, billed, want)
		}
	}

	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: conv.UserID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	got := rows[0]
	if got.InputTokens != 140 || got.OutputTokens != 70 ||
		got.CacheReadInputTokens != 24 || got.CacheCreationInputTokens != 12 ||
		got.CostUSD != 1.4 || got.Turns != 2 {
		t.Fatalf("aggregate = %+v, want final cumulative tokens/cost and two unique cache deltas", got)
	}
}

func TestRecordClaudeTokenUsage_ConcurrentDuplicateIsIdempotent(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "usage-user-concurrent", Name: "concurrent"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "usage-conv-concurrent", "", "usage-user-concurrent", t.TempDir(), "claude", "claude-opus"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conv, err := s.GetConversation(ctx, "usage-conv-concurrent")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	event := ClaudeUsageEvent{
		ConversationID: conv.ID, SessionID: conv.SessionID, EventID: "same-turn",
		UserID: conv.UserID, FallbackModel: "claude-opus", Day: "2026-09-24",
		CacheReadInputTokens: 30,
		PerModel: map[string]ClaudeModelUsage{
			"claude-opus": {InputTokens: 90, OutputTokens: 40, CostUSD: 0.9},
		},
	}

	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RecordClaudeTokenUsage(context.Background(), event)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("record concurrent duplicate: %v", err)
		}
	}

	rows, err := s.AggregateTokenUsage(context.Background(), TokenUsageQuery{UserID: conv.UserID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 || rows[0].InputTokens != 90 || rows[0].CacheReadInputTokens != 30 || rows[0].Turns != 1 {
		t.Fatalf("aggregate = %+v, want one accepted event", rows)
	}
}

func TestRecordClaudeTokenUsage_DeltaLandsOnCompletionDay(t *testing.T) {
	t.Parallel()
	s, conv := newClaudeUsageTestConversation(t, "midnight")
	ctx := context.Background()
	first := ClaudeUsageEvent{
		ConversationID: conv.ID, SessionID: conv.SessionID, EventID: "before-midnight",
		UserID: conv.UserID, FallbackModel: "claude-opus", Day: "2026-09-24",
		PerModel: map[string]ClaudeModelUsage{
			"claude-opus": {InputTokens: 100, OutputTokens: 20, CostUSD: 1},
		},
	}
	second := first
	second.EventID = "after-midnight"
	second.Day = "2026-09-25"
	second.PerModel = map[string]ClaudeModelUsage{
		"claude-opus": {InputTokens: 125, OutputTokens: 30, CostUSD: 1.25},
	}
	for _, event := range []ClaudeUsageEvent{first, second} {
		if _, err := s.RecordClaudeTokenUsage(ctx, event); err != nil {
			t.Fatalf("record %s: %v", event.EventID, err)
		}
	}

	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: conv.UserID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two days", rows)
	}
	if rows[0].Day != "2026-09-24" || rows[0].InputTokens != 100 ||
		rows[1].Day != "2026-09-25" || rows[1].InputTokens != 25 {
		t.Fatalf("rows = %+v, delta did not land on completion day", rows)
	}
}

func TestRecordClaudeTokenUsage_ResetNewSessionAndMultipleModels(t *testing.T) {
	t.Parallel()
	s, conv := newClaudeUsageTestConversation(t, "cycles")
	ctx := context.Background()
	record := func(eventID, session string, models map[string]ClaudeModelUsage) {
		t.Helper()
		if _, err := s.RecordClaudeTokenUsage(ctx, ClaudeUsageEvent{
			ConversationID: conv.ID, SessionID: session, EventID: eventID,
			UserID: conv.UserID, FallbackModel: "claude-opus", Day: "2026-09-24",
			PerModel: models,
		}); err != nil {
			t.Fatalf("record %s: %v", eventID, err)
		}
	}
	record("turn-1", conv.SessionID, map[string]ClaudeModelUsage{
		"claude-opus":  {InputTokens: 100, OutputTokens: 40, CostUSD: 1},
		"claude-haiku": {InputTokens: 30, OutputTokens: 10, CostUSD: 0.2},
	})
	record("turn-2", conv.SessionID, map[string]ClaudeModelUsage{
		"claude-opus":  {InputTokens: 130, OutputTokens: 55, CostUSD: 1.3},
		"claude-haiku": {InputTokens: 45, OutputTokens: 14, CostUSD: 0.3},
	})
	// A lower counter starts a new baseline period within the same session.
	record("turn-3", conv.SessionID, map[string]ClaudeModelUsage{
		"claude-opus": {InputTokens: 5, OutputTokens: 2, CostUSD: 0.05},
	})
	// Compact/new session starts from zero even though the values happen to be
	// larger than the just-reset checkpoint.
	record("turn-4", "new-session", map[string]ClaudeModelUsage{
		"claude-opus":   {InputTokens: 7, OutputTokens: 3, CostUSD: 0.07},
		"claude-sonnet": {InputTokens: 20, OutputTokens: 8, CostUSD: 0.15},
	})

	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: conv.UserID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	byModel := make(map[string]TokenUsageRecord, len(rows))
	for _, row := range rows {
		byModel[row.Model] = row
	}
	if got := byModel["claude-opus"]; got.InputTokens != 142 || got.OutputTokens != 60 || math.Abs(got.CostUSD-1.42) > 1e-9 {
		t.Fatalf("opus aggregate = %+v", got)
	}
	if got := byModel["claude-haiku"]; got.InputTokens != 45 || got.OutputTokens != 14 || got.CostUSD != 0.3 {
		t.Fatalf("haiku aggregate = %+v", got)
	}
	if got := byModel["claude-sonnet"]; got.InputTokens != 20 || got.OutputTokens != 8 || got.CostUSD != 0.15 {
		t.Fatalf("sonnet aggregate = %+v", got)
	}
}

func TestRecordClaudeTokenUsage_PersistsCheckpointAcrossRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "usage.db")
	open := func() *SQLiteStore {
		t.Helper()
		s, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		if err := s.Init(); err != nil {
			_ = s.Close()
			t.Fatalf("init store: %v", err)
		}
		return s
	}

	s := open()
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "restart-user", Name: "Restart"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "restart-conv", "", "restart-user", t.TempDir(), "claude", "claude-opus"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conv, err := s.GetConversation(ctx, "restart-conv")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	first := ClaudeUsageEvent{
		ConversationID: conv.ID, SessionID: conv.SessionID, EventID: "turn-1",
		UserID: conv.UserID, FallbackModel: "claude-opus", Day: "2026-09-24",
		PerModel: map[string]ClaudeModelUsage{"claude-opus": {InputTokens: 100, OutputTokens: 20, CostUSD: 1}},
	}
	if _, err := s.RecordClaudeTokenUsage(ctx, first); err != nil {
		t.Fatalf("record before restart: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s = open()
	defer func() { _ = s.Close() }()
	second := first
	second.EventID = "turn-2"
	second.PerModel = map[string]ClaudeModelUsage{"claude-opus": {InputTokens: 125, OutputTokens: 30, CostUSD: 1.25}}
	if _, err := s.RecordClaudeTokenUsage(ctx, second); err != nil {
		t.Fatalf("record after restart: %v", err)
	}
	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: conv.UserID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 || rows[0].InputTokens != 125 || rows[0].OutputTokens != 30 || rows[0].CostUSD != 1.25 {
		t.Fatalf("aggregate = %+v, checkpoint did not survive restart", rows)
	}
}

func TestRecordClaudeTokenUsage_BootstrapsUpgradeFromAssistantMetadata(t *testing.T) {
	t.Parallel()
	s, conv := newClaudeUsageTestConversation(t, "upgrade")
	ctx := context.Background()
	metadata := json.RawMessage(`{"usage":{"total_cost_usd":1,"per_model":{"claude-opus":{"input_tokens":100,"output_tokens":20,"cost_usd":1}}}}`)
	if err := s.SaveMessage(ctx, Message{
		ID: "prior-result", ConversationID: conv.ID, Role: "assistant", Content: "prior", Metadata: metadata,
	}); err != nil {
		t.Fatalf("save prior metadata: %v", err)
	}
	if _, err := s.RecordClaudeTokenUsage(ctx, ClaudeUsageEvent{
		ConversationID: conv.ID, SessionID: conv.SessionID, EventID: "next-turn",
		UserID: conv.UserID, FallbackModel: "claude-opus", Day: "2026-09-24",
		PerModel: map[string]ClaudeModelUsage{
			"claude-opus": {InputTokens: 125, OutputTokens: 30, CostUSD: 1.25},
		},
	}); err != nil {
		t.Fatalf("record upgraded session: %v", err)
	}
	rows, err := s.AggregateTokenUsage(ctx, TokenUsageQuery{UserID: conv.UserID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 || rows[0].InputTokens != 25 || rows[0].OutputTokens != 10 || rows[0].CostUSD != 0.25 {
		t.Fatalf("aggregate = %+v, want only post-upgrade delta", rows)
	}
}

func newClaudeUsageTestConversation(t *testing.T, suffix string) (*SQLiteStore, Conversation) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	userID := "usage-user-" + suffix
	convID := "usage-conv-" + suffix
	if err := s.CreateUser(ctx, User{ID: userID, Name: suffix}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, convID, "", userID, t.TempDir(), "claude", "claude-opus"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	conv, err := s.GetConversation(ctx, convID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	return s, conv
}
