package service

import (
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func TestResolveConversationSeed(t *testing.T) {
	cfg := twoProviderCfg()
	claudeModel := LatestModelForAccount(cfg, config.CLITypeClaude, "")
	codexModel := LatestModelForAccount(cfg, config.CLITypeCodex, "")
	agentWithDefault := store.User{DefaultModel: codexModel, ThinkLevel: "high"}
	low := "low"

	tests := []struct {
		name      string
		agent     store.User
		req       ConversationSeedRequest
		want      ConversationSeed
		wantError string
	}{
		{
			name:  "agent default model seeds provider and think level",
			agent: agentWithDefault,
			want:  ConversationSeed{Provider: config.CLITypeCodex, Model: codexModel, ThinkLevel: "high"},
		},
		{
			name:  "explicit think level beats the agent's",
			agent: agentWithDefault,
			req:   ConversationSeedRequest{ThinkLevel: &low},
			want:  ConversationSeed{Provider: config.CLITypeCodex, Model: codexModel, ThinkLevel: "low"},
		},
		{
			name:  "a pinned provider suppresses the agent default entirely",
			agent: agentWithDefault,
			req:   ConversationSeedRequest{Provider: config.CLITypeClaude},
			want:  ConversationSeed{Provider: config.CLITypeClaude, Model: claudeModel},
		},
		{
			name: "no agent default falls back to the server default provider",
			want: ConversationSeed{Provider: cfg.DefaultProviderType(), Model: LatestModelForAccount(cfg, cfg.DefaultProviderType(), "")},
		},
		{
			name:      "a model from another provider is refused",
			req:       ConversationSeedRequest{Provider: config.CLITypeClaude, Model: codexModel},
			wantError: "model does not belong to provider",
		},
		{
			name:      "an account the agent was not granted is refused",
			req:       ConversationSeedRequest{Provider: config.CLITypeClaude, Account: "someone-elses"},
			wantError: "account not bound",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveConversationSeed(cfg, tc.agent, tc.req)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantError)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}

	if _, err := ResolveConversationSeed(nil, store.User{}, ConversationSeedRequest{}); err == nil {
		t.Fatal("no configuration means no provider; the seed must be refused")
	}
}
