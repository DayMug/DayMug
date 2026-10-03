package service

import (
	"context"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func codexCfgWithoutAstra(t *testing.T) *config.Config {
	t.Helper()
	withAccountModels(t, map[string]AccountModels{
		"codex": {Models: []string{"gpt-5.6-sol", "gpt-5.6-terra"}},
	})
	return &config.Config{Providers: []config.Provider{
		{Name: "codex", Type: config.CLITypeCodex},
	}}
}

// A prompt sent to a conversation whose model an admin unchecked must stop at
// the gate with a visible error instead of reaching the CLI: silently running
// it was the bug, and silently substituting another model hides that the
// conversation is no longer on the model its history was produced with.
func TestProcessPromptRefusesWithdrawnModel(t *testing.T) {
	cfg := codexCfgWithoutAstra(t)
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: config.CLITypeCodex, Model: "gpt-6-astra",
	}}
	ms.Users = []store.User{{ID: "u1", Username: "claire", ProviderBindings: map[string]string{config.CLITypeCodex: "codex"}}}
	runner := &PromptRunner{Store: ms, Broadcaster: NewBroadcaster(), Cfg: cfg}

	runner.ProcessPrompt(context.Background(), store.Message{
		ID: "m1", ConversationID: "c1", Role: "user", Content: "hi",
	})

	msgs, err := ms.ListMessages(context.Background(), "c1", 10, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Role != "error" {
		t.Fatalf("messages = %+v, want a single error row", msgs)
	}
	if !strings.Contains(msgs[0].Content, "gpt-6-astra") {
		t.Errorf("error %q must name the unavailable model", msgs[0].Content)
	}

	// The room has to be released, or the conversation stays wedged as busy
	// and every later prompt bounces with "another run is in progress".
	if runner.Broadcaster.IsBusy("c1") {
		t.Error("conversation left marked busy after the refusal")
	}

	// And nothing may be written back: the stored model is what the picker
	// shows the user, so it has to keep naming the model they must move off.
	conv, err := ms.GetConversation(context.Background(), "c1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Model != "gpt-6-astra" {
		t.Errorf("stored model = %q, want it left alone", conv.Model)
	}
}

func TestCompactRefusesWithdrawnModel(t *testing.T) {
	cfg := codexCfgWithoutAstra(t)
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{
		ID: "c1", UserID: "u1", Provider: config.CLITypeCodex, Model: "gpt-6-astra",
		SessionID: "s1", WorkDir: t.TempDir(),
	}}
	ms.Users = []store.User{{ID: "u1", Username: "claire", ProviderBindings: map[string]string{config.CLITypeCodex: "codex"}}}
	backend := &compactBackend{summary: "digest", compaction: true}
	runner := &PromptRunner{
		Store:       ms,
		Broadcaster: NewBroadcaster(),
		Cfg:         cfg,
		Persist:     &MessagePersister{Store: ms},
		Backends:    NewBackendRegistry(nil, backend),
	}

	_, err := runner.Compact(context.Background(), "c1", "u1")
	if backend.calls != 0 {
		t.Errorf("RunOneshot calls = %d, want the summary run never attempted", backend.calls)
	}
	if err == nil {
		t.Fatal("Compact accepted a conversation pinned to a withdrawn model")
	}
	if !strings.Contains(err.Error(), "gpt-6-astra") {
		t.Errorf("error = %q, must name the unavailable model", err)
	}
}
