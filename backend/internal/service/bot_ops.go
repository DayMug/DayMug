package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// BotOps owns the write use-cases behind /api/users/:id/bots — the Slack/飞书
// connector rows hanging off one Agent. Like the other *Ops types it is a bare
// struct with public fields and no constructor, assembled per call by the
// handler so it never snapshots a field routes.go fills in later.
//
// What stays in the handler: proving the caller owns the Agent (an
// authorization boundary), and the detached imbot.Manager reload that follows
// every mutation. The reload is side-effect orchestration over a long-lived
// connection pool, not part of the bot-configuration domain.
type BotOps struct {
	// Bots is the connector-row store. Nil is a programming error, not a
	// runtime state: the handler refuses the request before reaching here.
	Bots store.BotStore
}

// BotParams is the mutable field set of a bot row as supplied by a create or
// update call — the service-side mirror of the handler's JSON request body, so
// the wire format's binding tags stay at the transport boundary.
//
// The four credential fields are blank-preserving on update: see
// MergeStoredCredentials for why an empty value means "keep what is stored".
type BotParams struct {
	Name                    string
	Platform                string
	Enabled                 bool
	Model                   string
	BotToken                string
	BotAppToken             string
	BotAppID                string
	BotAppSecret            string
	Channels                string
	MaxConversationDuration string
	UnconfiguredReply       string
	UnauthorizedReply       string
}

// MergeStoredCredentials back-fills every blank credential in p from existing.
//
// The UI can never read a stored secret back, so it submits blanks for
// credentials the operator did not retype. Treating those as "clear the field"
// would wipe a working bot's token the first time someone edited its channel
// rules. Merging before validation also keeps the "enabled bots need a token"
// check honest.
//
// Exported because the connection tester needs the same rule for a draft that
// names a saved bot, and two copies of it would drift.
func MergeStoredCredentials(p BotParams, existing store.Bot) BotParams {
	keep := func(incoming, stored string) string {
		if strings.TrimSpace(incoming) == "" {
			return stored
		}
		return incoming
	}
	p.BotToken = keep(p.BotToken, existing.BotToken)
	p.BotAppToken = keep(p.BotAppToken, existing.BotAppToken)
	p.BotAppID = keep(p.BotAppID, existing.BotAppID)
	p.BotAppSecret = keep(p.BotAppSecret, existing.BotAppSecret)
	return p
}

// normalizeBot validates p and renders it as the row to persist. Channel rules
// arrive as the raw JSON text the UI holds and leave as the canonical
// re-marshalled form, so what is stored is always what ValidateChannels
// accepted rather than the operator's formatting.
func normalizeBot(agentID, botID string, p BotParams) (store.Bot, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return store.Bot{}, BadRequest("name is required")
	}
	rules, err := imbot.ValidateChannels(p.Channels)
	if err != nil {
		return store.Bot{}, BadRequest(err.Error())
	}
	cfg, err := imbot.ValidateBotConfig(imbot.BotConfig{
		ID: botID, Name: name, Platform: p.Platform, Enabled: p.Enabled,
		BotToken: p.BotToken, AppToken: p.BotAppToken, AppID: p.BotAppID,
		AppSecret: p.BotAppSecret, Channels: rules,
	})
	if err != nil {
		return store.Bot{}, BadRequest(err.Error())
	}
	raw, err := json.MarshalIndent(cfg.Channels, "", "  ")
	if err != nil {
		return store.Bot{}, Internal(err.Error(), err)
	}
	// An empty rule set persists as "" rather than "[]" so the column reads as
	// "never configured" for the UI's placeholder logic.
	channels := ""
	if len(cfg.Channels) > 0 {
		channels = string(raw)
	}
	maxConversationDuration := strings.TrimSpace(p.MaxConversationDuration)
	if maxConversationDuration == "" {
		maxConversationDuration = store.DefaultBotMaxConversationDuration
	}
	duration, err := time.ParseDuration(maxConversationDuration)
	if err != nil || duration < 0 {
		return store.Bot{}, BadRequest("max_conversation_duration must be a non-negative duration such as 3h, 30m, or 0 for no limit")
	}
	unconfiguredReply := strings.TrimSpace(p.UnconfiguredReply)
	if unconfiguredReply == "" {
		unconfiguredReply = store.DefaultBotUnconfiguredReply
	}
	unauthorizedReply := strings.TrimSpace(p.UnauthorizedReply)
	if unauthorizedReply == "" {
		unauthorizedReply = store.DefaultBotUnauthorizedReply
	}
	return store.Bot{
		ID: botID, AgentID: agentID, Name: cfg.Name, Platform: cfg.Platform, Enabled: cfg.Enabled,
		Model: strings.TrimSpace(p.Model), MaxConversationDuration: maxConversationDuration,
		BotToken: cfg.BotToken, BotAppToken: cfg.AppToken, BotAppID: cfg.AppID,
		BotAppSecret: cfg.AppSecret, Channels: channels,
		UnconfiguredReply: unconfiguredReply, UnauthorizedReply: unauthorizedReply,
	}, nil
}

// Create adds a bot to agentID and returns the persisted row.
//
// The row is re-read rather than returned from the local struct because
// created_at is assigned by the store and the response contract includes it.
func (o *BotOps) Create(ctx context.Context, agentID string, p BotParams) (store.Bot, error) {
	bot, err := normalizeBot(agentID, uuid.New().String(), p)
	if err != nil {
		return store.Bot{}, err
	}
	if err := o.Bots.CreateBot(ctx, bot); err != nil {
		return store.Bot{}, Internal(err.Error(), err)
	}
	created, err := o.Bots.GetBot(ctx, bot.ID)
	if err != nil {
		return store.Bot{}, Internal(err.Error(), err)
	}
	return created, nil
}

// Update rewrites one of agentID's bots and returns the persisted row.
//
// The agentID match is the ownership gate, and it is why a bot belonging to
// someone else reports "not found" rather than "forbidden": the caller has
// already been proven to own agentID, so anything outside it must stay
// indistinguishable from a bad id.
func (o *BotOps) Update(ctx context.Context, agentID, botID string, p BotParams) (store.Bot, error) {
	existing, err := o.LoadOwnedBot(ctx, agentID, botID)
	if err != nil {
		return store.Bot{}, err
	}
	bot, err := normalizeBot(agentID, botID, MergeStoredCredentials(p, existing))
	if err != nil {
		return store.Bot{}, err
	}
	if err := o.Bots.UpdateBot(ctx, bot); err != nil {
		return store.Bot{}, StoreError(err, "bot not found")
	}
	updated, err := o.Bots.GetBot(ctx, botID)
	if err != nil {
		return store.Bot{}, Internal(err.Error(), err)
	}
	return updated, nil
}

// Delete removes one of agentID's bots. The delete is scoped by agentID as well
// as by id — that scoping, not a prior lookup, is what stops a guessed id from
// reaching another Agent's connector.
func (o *BotOps) Delete(ctx context.Context, agentID, botID string) error {
	if err := o.Bots.DeleteBot(ctx, botID, agentID); err != nil {
		return StoreError(err, "bot not found")
	}
	return nil
}

// LoadOwnedBot fetches a bot and asserts it belongs to agentID, collapsing a
// missing row and a foreign row into the same 404 so the two are not
// distinguishable from outside.
//
// Exported for the connection tester, which needs a saved bot's stored
// credentials without performing a write.
func (o *BotOps) LoadOwnedBot(ctx context.Context, agentID, botID string) (store.Bot, error) {
	existing, err := o.Bots.GetBot(ctx, botID)
	if err != nil || existing.AgentID != agentID {
		return store.Bot{}, NotFound("bot not found")
	}
	return existing, nil
}
