package service

import (
	"context"
	"math"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// A claude-compatible session resumed for a second turn must bill only that
// turn. Claude Code reports modelUsage and total_cost_usd as session running
// totals on the resumed process too, so before the fix the second result was
// added whole and the first turn was billed twice.
//
// Output and cache-read figures are the ones measured on CLI 2.1.280 (a haiku
// session, then `--resume`): modelUsage.outputTokens 53 → 97 and
// cacheReadInputTokens 13689 → 38532, while the top-level usage reported the
// turn's own 45/13689 and 44/24843. Input and cache-write counts were not part
// of that measurement and are illustrative.
func TestPersistTokenUsage_CompatibleResumeBillsOnlyTheIncrease(t *testing.T) {
	const model = "compat-haiku"
	pricing.Set(config.CLITypeClaudeCompatible, pricing.Table{
		model: {Input: 1, CachedInput: 0.1, Output: 5, CacheCreation5m: 1.25, CacheCreation1h: 2},
	})
	t.Cleanup(pricing.ResetForTest)

	s := newMessagePersistTestStore(t)
	ctx := context.Background()
	userID, convID := "user-1", "conv-1"
	if err := s.CreateUser(ctx, store.User{ID: userID, Name: "u"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, convID, "", userID, t.TempDir(), config.CLITypeClaudeCompatible, model); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	p := &MessagePersister{Store: s}
	attr := UsageAttribution{ConversationID: convID, AgentID: userID, FallbackModel: model}

	results := []string{
		// Turn 1 (fresh process).
		`{"type":"result","subtype":"success","result":"one","num_turns":1,` +
			`"usage":{"input_tokens":10,"output_tokens":45,"cache_read_input_tokens":13689,"cache_creation_input_tokens":2000},` +
			`"modelUsage":{"` + model + `":{"inputTokens":10,"outputTokens":53,"cacheReadInputTokens":13689,"cacheCreationInputTokens":2000,"costUSD":0.0248}},` +
			`"total_cost_usd":0.0248}`,
		// Turn 2 (a new process resuming the session).
		`{"type":"result","subtype":"success","result":"two","num_turns":1,` +
			`"usage":{"input_tokens":12,"output_tokens":44,"cache_read_input_tokens":24843,"cache_creation_input_tokens":500},` +
			`"modelUsage":{"` + model + `":{"inputTokens":22,"outputTokens":97,"cacheReadInputTokens":38532,"cacheCreationInputTokens":2500,"costUSD":0.0277}},` +
			`"total_cost_usd":0.0277}`,
	}
	for _, line := range results {
		persisted := false
		for _, evt := range claudecli.NewStreamProcessorForRun(config.CLITypeClaudeCompatible, model).Process([]byte(line)) {
			if report, ok := agent.UsageOf(evt); ok {
				p.PersistTokenUsage(attr, report)
				persisted = true
			}
		}
		if !persisted {
			t.Fatalf("no usage event for %s", line)
		}
	}

	rows, err := s.AggregateTokenUsage(ctx, store.TokenUsageQuery{UserID: userID})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	got := rows[0]
	// Tokens: the session totals once — never 53+97 output or 10+22 input.
	if got.OutputTokens != 97 || got.InputTokens != 22 {
		t.Errorf("tokens = in %d / out %d, want the session's 22 / 97", got.InputTokens, got.OutputTokens)
	}
	// Cache counters come from each turn's own top-level usage.
	if got.CacheReadInputTokens != 13689+24843 || got.CacheCreationInputTokens != 2000+500 {
		t.Errorf("cache = read %d / write %d, want 38532 / 2500", got.CacheReadInputTokens, got.CacheCreationInputTokens)
	}
	// Cost: the session's cumulative local price, once. Per million tokens:
	// 22×1 + 38532×0.1 + 2500×1.25 + 97×5 = 7485.2 → $0.0074852.
	if math.Abs(got.CostUSD-0.0074852) > 1e-9 {
		t.Errorf("cost = %v, want 0.0074852", got.CostUSD)
	}
}
