package service

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func newMessagePersistTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	// A file, not ":memory:": every pooled connection to ":memory:" opens its
	// own empty database, so a write that lands on a second connection sees
	// no tables at all.
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestBuildAssistantMetadataFor(t *testing.T) {
	tests := []struct {
		name    string
		usage   string
		model   string
		want    string
		isEmpty bool
	}{
		{
			name:  "usage+model",
			usage: `{"input_tokens":10,"output_tokens":20}`,
			model: "claude-opus-4-8",
			want:  `{"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":20}}`,
		},
		{
			name:  "model only",
			model: "claude-haiku-4-5",
			want:  `{"model":"claude-haiku-4-5"}`,
		},
		{
			name:    "neither",
			isEmpty: true,
		},
		{
			name:  "invalid usage JSON falls back to model-only",
			usage: "not-json",
			model: "claude-opus-4-8",
			want:  `{"model":"claude-opus-4-8"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAssistantMetadataFor(AssistantMetadata{Usage: tt.usage, Model: tt.model})
			if tt.isEmpty {
				if got != nil {
					t.Fatalf("expected nil, got %s", string(got))
				}
				return
			}
			if string(got) != tt.want {
				t.Errorf("got %s, want %s", string(got), tt.want)
			}
		})
	}
}

// The report's declared semantics, not the conversation's provider, decide
// whether usage is differenced against a checkpoint or added as it arrives.
// Each case persists the same report twice: a cumulative report bills once,
// a per-turn report bills twice.
func TestPersistTokenUsage_ReportSemanticsDecideBilling(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		cumulative bool
		wantInput  int64
		wantCost   float64
		wantTurns  int64
	}{
		{name: "claude cumulative", provider: config.CLITypeClaude, cumulative: true, wantInput: 10, wantCost: 0.1, wantTurns: 1},
		{name: "claude-compatible cumulative", provider: config.CLITypeClaudeCompatible, cumulative: true, wantInput: 10, wantCost: 0.1, wantTurns: 1},
		{name: "codex per-turn", provider: config.CLITypeCodex, cumulative: false, wantInput: 20, wantCost: 0.2, wantTurns: 2},
		{name: "provider is not consulted", provider: config.CLITypeClaude, cumulative: false, wantInput: 20, wantCost: 0.2, wantTurns: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newMessagePersistTestStore(t)
			ctx := context.Background()
			userID, convID := "user-1", "conv-1"
			if err := s.CreateUser(ctx, store.User{ID: userID, Name: tt.provider}); err != nil {
				t.Fatalf("create user: %v", err)
			}
			if err := s.CreateConversation(ctx, convID, "", userID, t.TempDir(), tt.provider, "model-1"); err != nil {
				t.Fatalf("create conversation: %v", err)
			}

			p := &MessagePersister{Store: s}
			report := agent.UsageReport{
				InputTokens: 10, OutputTokens: 4, CacheReadInputTokens: 3, TotalCostUSD: agent.USD(0.1),
				PerModel:   map[string]agent.ModelUsage{"model-1": {InputTokens: 10, OutputTokens: 4, CostUSD: agent.USD(0.1)}},
				Cumulative: tt.cumulative,
			}
			attr := UsageAttribution{ConversationID: convID, AgentID: userID, FallbackModel: "model-1", EventID: "turn-1"}
			p.PersistTokenUsage(attr, report)
			p.PersistTokenUsage(attr, report)

			rows, err := s.AggregateTokenUsage(ctx, store.TokenUsageQuery{UserID: userID})
			if err != nil {
				t.Fatalf("aggregate: %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows = %+v, want one", rows)
			}
			got := rows[0]
			if got.InputTokens != tt.wantInput || got.CostUSD != tt.wantCost || got.Turns != tt.wantTurns {
				t.Fatalf("aggregate = %+v, want input=%d cost=%v turns=%d", got, tt.wantInput, tt.wantCost, tt.wantTurns)
			}
		})
	}
}

// The returned cost is what a per-turn budget may add up: a resumed Claude
// session's report carries its whole history's running cost, so only the
// increase over the checkpoint counts; a per-turn report counts as reported.
func TestPersistTokenUsage_ReturnsWhatTheReportBilled(t *testing.T) {
	s := newMessagePersistTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{ID: "user-1", Name: "u"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "conv-1", "", "user-1", t.TempDir(), config.CLITypeClaude, "model-1"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	p := &MessagePersister{Store: s}
	cumulative := func(turn string, cost float64) (UsageAttribution, agent.UsageReport) {
		return UsageAttribution{ConversationID: "conv-1", AgentID: "user-1", FallbackModel: "model-1", EventID: turn},
			agent.UsageReport{
				TotalCostUSD: agent.USD(cost), Cumulative: true,
				PerModel: map[string]agent.ModelUsage{"model-1": {InputTokens: int64(cost * 1000), OutputTokens: 1, CostUSD: agent.USD(cost)}},
			}
	}
	for _, step := range []struct {
		turn       string
		cumulative float64
		want       float64
	}{
		{"turn-1", 0.0248, 0.0248},
		{"turn-2", 0.0277, 0.0029},
		{"turn-2", 0.0277, 0}, // the same frame again bills nothing
	} {
		attr, report := cumulative(step.turn, step.cumulative)
		if _, got := p.PersistTokenUsage(attr, report); math.Abs(got-step.want) > 1e-9 {
			t.Fatalf("%s @ %v billed %v, want %v", step.turn, step.cumulative, got, step.want)
		}
	}

	perTurn := agent.UsageReport{InputTokens: 5, OutputTokens: 1, TotalCostUSD: agent.USD(0.3)}
	attr := UsageAttribution{ConversationID: "conv-1", AgentID: "user-1", FallbackModel: "model-1"}
	if _, got := p.PersistTokenUsage(attr, perTurn); got != 0.3 {
		t.Fatalf("per-turn report billed %v, want its own 0.3", got)
	}
}
