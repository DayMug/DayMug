package service

import (
	"context"
	"slices"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// ConversationSeedRequest is what a caller pinned for a new conversation.
// Empty fields are filled from the agent's profile and the server defaults.
type ConversationSeedRequest struct {
	Provider string
	Model    string
	// Account optionally pins one of the agent's granted accounts for the
	// provider; the model default then comes from that account.
	Account string
	// ThinkLevel, when non-nil, is an explicit choice ("" = provider
	// default). nil lets the agent's profile decide.
	ThinkLevel *string
}

// ConversationSeed is the provider/model/think-level triple a new conversation
// starts on.
type ConversationSeed struct {
	Provider   string
	Model      string
	ThinkLevel string
}

// ResolveConversationSeed decides what a new conversation for agent runs on.
// It is the single definition shared by every path that creates one — the web
// create endpoint, cron fires, and the IM bridge — so a scheduled or IM-born
// conversation starts on exactly what the same request from the browser would.
//
// It performs no authorization: callers establish that the agent may be used
// before asking. It does validate an explicit account against the agent's own
// grants, because that is a property of the agent, not of the caller.
//
// Precedence: caller-pinned provider/model; otherwise the agent's admin-set
// default model (resolved to its provider, and carrying the agent's think
// level, which is part of that default-model profile); otherwise the server's
// default provider and its latest model. The pair is validated as a unit so a
// caller can't smuggle in (claude, gpt-5).
func ResolveConversationSeed(cfg *config.Config, agent store.User, req ConversationSeedRequest) (ConversationSeed, error) {
	provider, model := req.Provider, req.Model
	seededAgentDefaultModel := false
	if provider == "" && model == "" && agent.DefaultModel != "" {
		if pr := ProviderForModelForConfig(cfg, agent.DefaultModel); pr != "" {
			provider = pr
			model = agent.DefaultModel
			seededAgentDefaultModel = true
		}
	}
	thinkLevel := ""
	if req.ThinkLevel != nil {
		var err error
		if thinkLevel, err = NormalizeThinkLevel(*req.ThinkLevel); err != nil {
			return ConversationSeed{}, err
		}
	} else if seededAgentDefaultModel {
		// Copied onto the conversation at creation time so later Agent edits
		// cannot silently change an existing conversation's behavior.
		thinkLevel = agent.ThinkLevel
	}

	if provider == "" && cfg != nil {
		provider = cfg.DefaultProviderType()
	}
	if provider == "" {
		return ConversationSeed{}, BadRequest("no provider configured: add at least one entry to providers in config.yaml")
	}
	if !IsValidProviderForConfig(cfg, provider) {
		return ConversationSeed{}, BadRequest("unknown provider")
	}
	// Check authorization before live configuration so an unbound name
	// cannot be used to probe which provider accounts exist.
	if req.Account != "" && !slices.Contains(agent.ProviderAccounts[provider], req.Account) {
		return ConversationSeed{}, BadRequest("account not bound to this user for provider " + provider)
	}
	if req.Account != "" && (cfg == nil || cfg.FindAccountForType(req.Account, provider) == nil) {
		return ConversationSeed{}, BadRequest("provider account is not configured for provider " + provider)
	}
	if model == "" {
		// The pinned account's latest when one is given, else the type
		// default, so a codex-qwen pin doesn't inherit the stock codex default.
		model = LatestModelForAccount(cfg, provider, req.Account)
	}
	if model == "" {
		return ConversationSeed{}, BadRequest(NoModelsConfiguredError(provider))
	}
	if !IsValidModelForAccount(cfg, provider, req.Account, model) {
		return ConversationSeed{}, BadRequest("model does not belong to provider")
	}
	return ConversationSeed{Provider: provider, Model: model, ThinkLevel: thinkLevel}, nil
}

// CreateSeededConversation writes a new conversation born on seed. row carries
// the path's own creation-time columns (id, title, agent, work dir, and
// whatever else the path starts the conversation with: an account pin, an
// inherited session, usage attribution, muted notifications); the seed fills
// provider, model and think level. It is one INSERT, so no path can leave a
// conversation half-seeded, and every path goes through it so none can forget
// a column. The store error is returned unwrapped.
func CreateSeededConversation(ctx context.Context, s store.Store, row store.NewConversation, seed ConversationSeed) error {
	row.Provider, row.Model, row.ThinkLevel = seed.Provider, seed.Model, seed.ThinkLevel
	return s.CreateConversationRecord(ctx, row)
}
