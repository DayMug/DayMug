package service

import (
	"context"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestBuildRunOptionsConversationThinkLevelOverridesAgentDefault(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "agent-1", Model: "model-1", ThinkLevel: "low",
	}}
	runner := &PromptRunner{Store: ms}
	opts := runner.BuildRunOptions(context.Background(), "c1", t.TempDir(), &store.User{
		ID: "agent-1", ThinkLevel: "high",
	}, nil)
	if opts.ThinkLevel != "low" {
		t.Fatalf("ThinkLevel = %q, want conversation value low", opts.ThinkLevel)
	}
}

func TestBuildRunOptionsEmptyConversationThinkLevelUsesProviderDefault(t *testing.T) {
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: "c1", UserID: "agent-1", Model: "model-1"}}
	runner := &PromptRunner{Store: ms}
	opts := runner.BuildRunOptions(context.Background(), "c1", t.TempDir(), &store.User{
		ID: "agent-1", ThinkLevel: "high",
	}, nil)
	if opts.ThinkLevel != "" {
		t.Fatalf("ThinkLevel = %q, want provider default", opts.ThinkLevel)
	}
}
