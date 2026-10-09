package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

const (
	botTestToken    = "xoxb-stored-bot-token"
	botTestAppToken = "xapp-stored-app-token"
)

func newBotTestOps() (*BotOps, *storetest.Fake) {
	ms := storetest.New()
	return &BotOps{Bots: ms}, ms
}

func slackParams() BotParams {
	return BotParams{
		Name:        "Slack",
		Platform:    "slack",
		Enabled:     true,
		BotToken:    botTestToken,
		BotAppToken: botTestAppToken,
		Channels:    `[{"channel":"*"}]`,
	}
}

func TestBotOpsCreate(t *testing.T) {
	o, _ := newBotTestOps()
	bot, err := o.Create(context.Background(), "agent-1", slackParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bot.AgentID != "agent-1" || bot.Name != "Slack" {
		t.Errorf("unexpected bot: %+v", bot)
	}
	if bot.ID == "" {
		t.Error("created bot should carry a generated id")
	}
	if bot.UnconfiguredReply != store.DefaultBotUnconfiguredReply ||
		bot.UnauthorizedReply != store.DefaultBotUnauthorizedReply {
		t.Errorf("default reply configuration = %+v", bot)
	}
	if bot.MaxConversationDuration != store.DefaultBotMaxConversationDuration {
		t.Errorf("default max conversation duration = %q, want %q", bot.MaxConversationDuration, store.DefaultBotMaxConversationDuration)
	}
}

func TestBotOpsCreateStoresCustomReplies(t *testing.T) {
	o, _ := newBotTestOps()
	p := slackParams()
	p.MaxConversationDuration = "180m"
	p.UnconfiguredReply = "custom unconfigured"
	p.UnauthorizedReply = "custom unauthorized"
	bot, err := o.Create(context.Background(), "agent-1", p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bot.UnconfiguredReply != p.UnconfiguredReply || bot.UnauthorizedReply != p.UnauthorizedReply {
		t.Fatalf("reply configuration = %+v", bot)
	}
	if bot.MaxConversationDuration != "180m" {
		t.Fatalf("max conversation duration = %q, want trimmed input", bot.MaxConversationDuration)
	}
}

// TestBotOpsCreateRejectsInvalidInput is table-driven because the rejection
// reasons come from three different validators and all must surface as 400
// rather than as a 500 from the store.
func TestBotOpsCreateRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*BotParams)
	}{
		{name: "blank name", mutate: func(p *BotParams) { p.Name = "  " }},
		{name: "unknown platform", mutate: func(p *BotParams) { p.Platform = "myspace" }},
		{name: "malformed channel rules", mutate: func(p *BotParams) { p.Channels = "{not json" }},
		{name: "enabled without credentials", mutate: func(p *BotParams) { p.BotToken, p.BotAppToken = "", "" }},
		{name: "malformed max conversation duration", mutate: func(p *BotParams) { p.MaxConversationDuration = "three hours" }},
		{name: "negative max conversation duration", mutate: func(p *BotParams) { p.MaxConversationDuration = "-1s" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, ms := newBotTestOps()
			p := slackParams()
			tc.mutate(&p)
			_, err := o.Create(context.Background(), "agent-1", p)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if got := svcStatus(err); got != 400 {
				t.Errorf("status = %d, want 400", got)
			}
			if len(ms.Bots) != 0 {
				t.Error("nothing may be persisted when validation fails")
			}
		})
	}
}

func TestBotOpsCreateAllowsUnlimitedConversation(t *testing.T) {
	o, _ := newBotTestOps()
	p := slackParams()
	p.MaxConversationDuration = "0"
	bot, err := o.Create(context.Background(), "agent-1", p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bot.MaxConversationDuration != "0" {
		t.Fatalf("max conversation duration = %q, want explicit unlimited value", bot.MaxConversationDuration)
	}
}

// TestBotOpsUpdateKeepsStoredCredentialsWhenBlank is the credential-merge
// decision that made Update worth sinking into a service op: the UI cannot read
// a secret back, so it resubmits blanks, and treating those as "clear it" would
// break a working connector on an unrelated edit.
func TestBotOpsUpdateKeepsStoredCredentialsWhenBlank(t *testing.T) {
	o, ms := newBotTestOps()
	created, err := o.Create(context.Background(), "agent-1", slackParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	edit := slackParams()
	edit.Name = "Renamed"
	edit.BotToken, edit.BotAppToken = "", ""
	updated, err := o.Update(context.Background(), "agent-1", created.ID, edit)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", updated.Name)
	}
	if updated.BotToken != botTestToken || updated.BotAppToken != botTestAppToken {
		t.Errorf("blank credentials wiped the stored secrets: %+v", updated)
	}
	if len(ms.Bots) != 1 {
		t.Errorf("expected the row to be rewritten in place, got %d rows", len(ms.Bots))
	}
}

func TestBotOpsUpdateReplacesSuppliedCredentials(t *testing.T) {
	o, _ := newBotTestOps()
	created, err := o.Create(context.Background(), "agent-1", slackParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	edit := slackParams()
	edit.BotToken = "xoxb-rotated"
	updated, err := o.Update(context.Background(), "agent-1", created.ID, edit)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.BotToken != "xoxb-rotated" {
		t.Errorf("token = %q, want the rotated value", updated.BotToken)
	}
}

// TestBotOpsRejectsForeignAgent pins that a bot belonging to another Agent is
// reported as absent, not as forbidden — the caller has only been proven to own
// their own agent id, so anything else must stay indistinguishable from a bad
// id.
func TestBotOpsRejectsForeignAgent(t *testing.T) {
	o, ms := newBotTestOps()
	created, err := o.Create(context.Background(), "agent-1", slackParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := o.Update(context.Background(), "agent-2", created.ID, slackParams()); svcStatus(err) != 404 {
		t.Errorf("Update across agents: status = %d, want 404", svcStatus(err))
	}
	if _, err := o.LoadOwnedBot(context.Background(), "agent-2", created.ID); svcStatus(err) != 404 {
		t.Errorf("LoadOwnedBot across agents: status = %d, want 404", svcStatus(err))
	}
	if err := o.Delete(context.Background(), "agent-2", created.ID); svcStatus(err) != 404 {
		t.Errorf("Delete across agents: status = %d, want 404", svcStatus(err))
	}
	if len(ms.Bots) != 1 {
		t.Error("the foreign delete must not have removed the row")
	}
}

func TestBotOpsDelete(t *testing.T) {
	o, ms := newBotTestOps()
	created, err := o.Create(context.Background(), "agent-1", slackParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := o.Delete(context.Background(), "agent-1", created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(ms.Bots) != 0 {
		t.Errorf("expected the row to be gone, got %d", len(ms.Bots))
	}
}

func TestMergeStoredCredentials(t *testing.T) {
	stored := store.Bot{
		BotToken: "tok", BotAppToken: "app", BotAppID: "id", BotAppSecret: "secret",
	}
	// Whitespace counts as blank: the UI submits an untouched input as "".
	got := MergeStoredCredentials(BotParams{BotToken: "  ", BotAppID: "new-id"}, stored)
	if got.BotToken != "tok" || got.BotAppToken != "app" || got.BotAppSecret != "secret" {
		t.Errorf("blank fields were not back-filled: %+v", got)
	}
	if got.BotAppID != "new-id" {
		t.Errorf("supplied field was overwritten: %q", got.BotAppID)
	}
}

// svcStatus extracts the HTTP status a service failure carries, or 0 when the
// error is nil or not a *ServiceError. Keeps the assertions above about the
// contract the handler renders rather than about message text.
func svcStatus(err error) int {
	var svcErr *ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Status
	}
	return 0
}
