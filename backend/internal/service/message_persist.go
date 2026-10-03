package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// MessagePersister owns the persistence/business side effects of a chat turn:
// saving assistant/thinking/tool rows, token-usage accounting, push
// notifications, and auto-titling. It is transport-agnostic — no WebSocket or
// HTTP types — so the terminal handler delegates here with explicit deps.
// Every field is optional (nil-safe) to match the historical handler wiring:
// tests construct only what they exercise.
type MessagePersister struct {
	Store       store.Store
	Broadcaster *Broadcaster
	// UserHub fans out user-scoped events (conversation list lifecycle
	// changes, auto-title) to every WebSocket the user has open. Optional;
	// nil disables cross-tab list sync.
	UserHub *UserHub
	// Pool resolves provider bindings for the auto-title generator so it
	// runs under the same credentials as the main turn. Optional in tests.
	Pool *Pool
	// BarkSender / PushDeerSender deliver completion and user-question push
	// notifications. Left nil in tests so they don't make outbound HTTP.
	BarkSender     BarkSender
	PushDeerSender PushDeerSender
	// TitleGen is the default title generator; TitleGens optionally
	// overrides it per provider. nil skips auto-titling.
	TitleGen  agent.TitleGenerator
	TitleGens map[string]agent.TitleGenerator
	// AutoTitleDelay staggers the async title generator behind the main turn
	// so the two don't fire their CLIs (and rewrite the account's OAuth token
	// file) at the same instant. Production wiring sets it to
	// AutoTitleStartDelay; zero (the test default) runs immediately.
	AutoTitleDelay time.Duration
	// TitleLimiter gates how many title-generator children run at once. nil
	// (the production and test default) uses the process-wide limiter, which
	// is what actually bounds fan-out — see defaultAutoTitleLimiter. Set it
	// per instance only in tests that need their own capacity/counters.
	TitleLimiter *AutoTitleLimiter
	// UsageLoc is the operator-configured timezone used to bucket
	// token_usage rows by day. nil falls back to UTC.
	UsageLoc *time.Location
	// Cfg is consulted for the default provider type when a legacy
	// conversation row carries an empty provider. Optional.
	Cfg *config.Config
}

// SaveMessageWithRetry persists a message, retrying briefly on SQLite BUSY
// contention. Used on the persistence-critical paths (assistant reply and its
// thinking buffer) so a transient WAL lock doesn't silently drop a reply the
// user just watched stream in.
func (p *MessagePersister) SaveMessageWithRetry(ctx context.Context, msg store.Message) error {
	const maxAttempts = 3
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = p.Store.SaveMessage(ctx, msg)
		if err == nil {
			return nil
		}
		if !isSQLiteBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(50*(attempt+1)) * time.Millisecond):
		}
	}
	return err
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}

// ExtractModelFromSystemInit pulls the "model" field out of a system_init
// event's JSON payload. Returns "" on any parse error so callers fall
// through to whatever fallback they already have. Only the parent agent's
// init carries the canonical model — sub-agent inits run a different
// model and the stream processor never feeds them to this handler.
func ExtractModelFromSystemInit(payload string) string {
	if payload == "" {
		return ""
	}
	var info struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(payload), &info); err != nil {
		return ""
	}
	return info.Model
}

// AssistantMetadata is the named form of the blob's inputs. It exists because
// the positional variants had already reached three arguments; Origin would
// have been the fourth, and by then nobody reading a call site can tell what
// the bare strings mean.
type AssistantMetadata struct {
	Usage     string
	Model     string
	Artifacts []Artifact
	// Origin records how the turn began when it was not the user asking —
	// today only "background_wakeup", a turn the provider started once
	// background work settled. Empty for an ordinary prompted reply.
	Origin string
}

func BuildAssistantMetadataFor(meta AssistantMetadata) json.RawMessage {
	usagePayload, model := meta.Usage, meta.Model
	hasUsage := usagePayload != "" && json.Valid([]byte(usagePayload))
	hasModel := model != ""
	webArtifacts := meta.Artifacts
	if !hasUsage && !hasModel && len(webArtifacts) == 0 && meta.Origin == "" {
		return nil
	}
	out := make(map[string]json.RawMessage, 4)
	if meta.Origin != "" {
		if ob, err := json.Marshal(meta.Origin); err == nil {
			out["origin"] = ob
		}
	}
	if hasUsage {
		out["usage"] = json.RawMessage(usagePayload)
	}
	if hasModel {
		// Encode the model string through json.Marshal so quoting /
		// escaping matches what the rest of the wire shape uses; the
		// allocation is trivial and we avoid hand-rolling a string
		// literal that breaks the moment a model id grows special chars.
		if mb, err := json.Marshal(model); err == nil {
			out["model"] = mb
		}
	}
	if len(webArtifacts) > 0 {
		if attachments, err := json.Marshal(webArtifacts); err == nil {
			out["attachments"] = attachments
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

// ExtractSessionIDFromSystemInit pulls the "session_id" field out of a
// system_init event payload. Used by the streaming loop to capture the
// session id chosen by a runner that assigns its own (codex), so the
// next turn can pass `--resume <id>`. Claude always echoes back the id
// we supplied via --session-id, so for that runner the value matches
// what we already have in the DB and the persistence call no-ops.
func ExtractSessionIDFromSystemInit(payload string) string {
	if payload == "" {
		return ""
	}
	var info struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(payload), &info); err != nil {
		return ""
	}
	return info.SessionID
}

type UsageAttribution struct {
	ConversationID      string
	AgentID             string
	FallbackModel       string
	EventID             string
	UserInstructions    int64
	ModelRequests       int64
	ToolCalls           int64
	ContextUsedTokens   int64
	ContextWindowTokens int64
}

// PersistTokenUsage writes a per-day token usage delta for every contributing
// model. A Cumulative report (Claude Code's per_model and cost are session
// running totals, for claude and claude-compatible alike) goes through the
// durable checkpoint path that bills only the increase; every other report is
// the spend it describes and is added as it arrives. The adapter declares
// which one it sent — the provider name is not consulted. Top-level cache
// counters remain attached to the primary model. Best-effort: a write failure
// is logged but never breaks the streaming flow.
//
// It returns the usage-event id (for the context-usage follow-up) and the
// report's own cost: what this report added to the bill. For a cumulative
// report that is the checkpoint's increase — the one figure a per-turn budget
// can add up — and when no checkpoint could be consulted it falls back to the
// reported running total, which overstates rather than hides spend.
func (p *MessagePersister) PersistTokenUsage(attr UsageAttribution, u agent.UsageReport) (string, float64) {
	if p.Store == nil || attr.ConversationID == "" || attr.AgentID == "" {
		return "", u.Cost()
	}

	loc := p.UsageLoc
	if loc == nil {
		loc = time.UTC
	}
	// Capture the bucket when the terminal usage frame is received. The write is
	// intentionally synchronous: one stream's result frames must advance their
	// cumulative baseline in wire order, or a later frame winning the goroutine
	// race would make the earlier one look like a counter reset.
	day := time.Now().In(loc).Format("2006-01-02")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conv, err := p.Store.GetConversation(ctx, attr.ConversationID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("[terminal] get conversation for token usage conv=%s: %v", attr.ConversationID, err)
			return "", u.Cost()
		}
		// Focused/ad-hoc callers may not persist a conversation row; production
		// turns always have one. The report's own semantics still apply.
		conv = store.Conversation{}
	}
	provider := conv.Provider
	if provider == "" && p.Cfg != nil {
		provider = p.Cfg.DefaultProviderType()
	}
	groupID := attr.EventID
	if groupID == "" && !u.Cumulative {
		groupID = uuid.NewString()
	}
	modelRequests := attr.ModelRequests
	requestScope := "billing_event"
	if u.NumTurns > 0 {
		modelRequests = u.NumTurns
		requestScope = "provider_reported"
	}
	if modelRequests <= 0 {
		modelRequests = 1
	}
	occurredAt := time.Now().UTC()
	sourceType := conv.SourceType
	if sourceType == "" {
		sourceType = "manual"
	}
	if u.Cumulative {
		perModel := make(map[string]store.ClaudeModelUsage, len(u.PerModel))
		for model, detail := range u.PerModel {
			perModel[model] = store.ClaudeModelUsage{
				InputTokens: detail.InputTokens, OutputTokens: detail.OutputTokens, CostUSD: detail.Cost(),
			}
		}
		billed, err := p.Store.RecordClaudeTokenUsage(ctx, store.ClaudeUsageEvent{
			ConversationID:           attr.ConversationID,
			SessionID:                conv.SessionID,
			EventID:                  attr.EventID,
			UserID:                   attr.AgentID,
			FallbackModel:            attr.FallbackModel,
			Day:                      day,
			Provider:                 provider,
			SourceType:               sourceType,
			CronJobID:                conv.CronJobID,
			OccurredAt:               occurredAt,
			UserInstructionCount:     attr.UserInstructions,
			ModelRequestCount:        modelRequests,
			ToolCallCount:            attr.ToolCalls,
			ContextUsedTokens:        attr.ContextUsedTokens,
			ContextWindowTokens:      attr.ContextWindowTokens,
			RequestCountScope:        requestScope,
			InputTokens:              u.InputTokens,
			OutputTokens:             u.OutputTokens,
			CacheReadInputTokens:     u.CacheReadInputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens,
			TotalCostUSD:             u.Cost(),
			PerModel:                 perModel,
		})
		if err != nil {
			log.Printf("[terminal] record claude token usage user=%s conv=%s: %v", attr.AgentID, attr.ConversationID, err)
			return attr.EventID, u.Cost()
		}
		return attr.EventID, billed
	}

	// A non-cumulative report is already the spend it describes.
	if len(u.PerModel) > 0 {
		models := make([]string, 0, len(u.PerModel))
		for modelName := range u.PerModel {
			models = append(models, modelName)
		}
		sort.Strings(models)
		counterModel := attr.FallbackModel
		if _, ok := u.PerModel[counterModel]; !ok && len(models) > 0 {
			counterModel = models[0]
		}
		for _, modelName := range models {
			detail := u.PerModel[modelName]
			delta := store.TokenUsageDelta{
				UserID: attr.AgentID,
				Model:  modelName,
				Day:    day,
			}
			delta.InputTokens = detail.InputTokens
			delta.OutputTokens = detail.OutputTokens
			delta.CostUSD = detail.Cost()
			delta.CacheReadInputTokens = detail.CacheReadInputTokens
			if modelName == attr.FallbackModel && delta.CacheReadInputTokens == 0 {
				delta.CacheReadInputTokens = u.CacheReadInputTokens
			}
			if modelName == attr.FallbackModel && delta.CacheCreationInputTokens == 0 {
				delta.CacheCreationInputTokens = u.CacheCreationInputTokens
			}
			if err := p.Store.AddTokenUsage(ctx, delta); err != nil {
				log.Printf("[terminal] add token usage user=%s model=%s: %v", attr.AgentID, modelName, err)
				continue
			}
			e := store.UsageEvent{
				ID: uuid.NewString(), EventID: groupID, ConversationID: attr.ConversationID,
				AgentID: attr.AgentID, Provider: provider, Model: modelName,
				SourceType: sourceType, CronJobID: conv.CronJobID, OccurredAt: occurredAt, Day: day,
				InputTokens: delta.InputTokens, CacheReadInputTokens: delta.CacheReadInputTokens,
				CacheCreationInputTokens: delta.CacheCreationInputTokens, OutputTokens: delta.OutputTokens,
				ReasoningOutputTokens: detail.ReasoningOutputTokens, CostUSD: delta.CostUSD,
				ContextUsedTokens: attr.ContextUsedTokens, ContextWindowTokens: attr.ContextWindowTokens,
				RequestCountScope: requestScope,
			}
			if modelName == counterModel {
				e.UserInstructionCount, e.ModelRequestCount, e.ToolCallCount = attr.UserInstructions, modelRequests, attr.ToolCalls
			}
			if err := p.Store.RecordUsageEvent(ctx, e); err != nil {
				log.Printf("[terminal] add attributed usage event conv=%s: %v", attr.ConversationID, err)
			}
		}
		return groupID, u.Cost()
	}

	delta := store.TokenUsageDelta{
		UserID:                   attr.AgentID,
		Model:                    attr.FallbackModel,
		Day:                      day,
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CostUSD:                  u.Cost(),
	}
	if err := p.Store.AddTokenUsage(ctx, delta); err != nil {
		log.Printf("[terminal] add token usage user=%s model=%s: %v", attr.AgentID, attr.FallbackModel, err)
		return "", u.Cost()
	}
	if err := p.Store.RecordUsageEvent(ctx, store.UsageEvent{
		ID: uuid.NewString(), EventID: groupID, ConversationID: attr.ConversationID,
		AgentID: attr.AgentID, Provider: provider, Model: attr.FallbackModel,
		SourceType: sourceType, CronJobID: conv.CronJobID, OccurredAt: occurredAt, Day: day,
		UserInstructionCount: attr.UserInstructions, ModelRequestCount: modelRequests, ToolCallCount: attr.ToolCalls,
		InputTokens: u.InputTokens, CacheReadInputTokens: u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens, OutputTokens: u.OutputTokens,
		ReasoningOutputTokens: u.ReasoningOutputTokens, CostUSD: u.Cost(),
		ContextUsedTokens: attr.ContextUsedTokens, ContextWindowTokens: attr.ContextWindowTokens,
		RequestCountScope: requestScope,
	}); err != nil {
		log.Printf("[terminal] add attributed usage event conv=%s: %v", attr.ConversationID, err)
	}
	return groupID, u.Cost()
}

// PersistContextUsage stores the latest context_usage payload on the
// conversation row so a future fresh client (server restart, new browser,
// localStorage cleared) can recover the token bar without waiting for the
// next claude reply. Async + best-effort: the in-memory broadcaster cache
// remains the authoritative live source; the DB only powers cold-start
// recovery, so a write failure is logged-and-forgotten rather than
// surfaced. ErrNotFound is silently ignored — the conversation row may
// not exist yet (e.g. ad-hoc test fixtures).
func (p *MessagePersister) PersistContextUsage(convID, payload string) {
	if p.Store == nil || convID == "" || payload == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Store.UpdateConversationContextUsage(ctx, convID, payload); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			log.Printf("[terminal] persist context_usage conv=%s: %v", convID, err)
		}
	}()
}

// PersistTool saves a single tool call to the store as a 'tool' role
// message and returns the assigned id (so the caller can ship it to the
// client via the matching tool_result event for id-based dedup). Empty
// id means the save failed — the caller should broadcast a
// `persist_failed` so the user knows their view is missing this row.
//
// After a successful persist, drops the broadcaster's replay buffer: the
// events that led up to this tool call are now in the DB and any
// reconnecting client will see them via REST + history_backfill, so
// leaving them in the replay buffer would render the same tool chip
// twice (once at its chronological position, once at the tail) and pin
// the live stream to a stale row a few entries above the visible end.
func (p *MessagePersister) PersistTool(convID, content string, durationMS *int64) string {
	if p.Store == nil || convID == "" || content == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg := store.Message{
		ID:             uuid.New().String(),
		ConversationID: convID,
		Role:           "tool",
		Content:        content,
	}
	if durationMS != nil {
		msg.Metadata, _ = json.Marshal(map[string]int64{"duration_ms": *durationMS})
	}
	if err := p.SaveMessageWithRetry(ctx, msg); err != nil {
		log.Printf("[terminal] save tool conv=%s: %v", convID, err)
		return ""
	}
	if p.Broadcaster != nil {
		p.Broadcaster.ResetReplay(convID)
	}
	return msg.ID
}

// PersistResultIDs is the return shape of PersistResult: the persisted
// thinking + assistant message ids (empty when the corresponding save
// was skipped or failed). The caller forwards them on the result event
// so the client can dedup by id rather than by string-prefix
// heuristics. AssistantOK signals whether the assistant save actually
// succeeded — when false we skip the side effects (bark, auto-title)
// since shipping a notification for content that isn't in the DB is
// the failure mode that lets users get a Bark with no matching chat
// row.
type PersistResultIDs struct {
	ThinkingID  string
	AssistantID string
	AssistantOK bool
}

// PersistResult saves thinking and assistant messages to the store. It
// returns the assigned ids and whether the assistant save succeeded so
// the caller can forward the ids on the WS result event (for client
// id-based dedup) and gate notifications/auto-title on actual
// persistence success.
//
// Uses a fresh background context (not the request ctx) because callers
// also run this on the cancel/abort path — when CancelJob fires or the
// service is shutting down, reqCtx is already cancelled and would block
// the SaveMessage that records the partial reply the user actually
// saw.
func (p *MessagePersister) PersistResult(
	convID string,
	thinkingBuf *strings.Builder,
	resultContent string,
	metadata json.RawMessage,
) PersistResultIDs {
	out := PersistResultIDs{}
	if p.Store == nil || convID == "" {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if thinkingBuf.Len() > 0 {
		thinkingMsg := store.Message{
			ID:             uuid.New().String(),
			ConversationID: convID,
			Role:           "thinking",
			Content:        thinkingBuf.String(),
		}
		if err := p.SaveMessageWithRetry(ctx, thinkingMsg); err != nil {
			log.Printf("[terminal] save thinking conv=%s: %v", convID, err)
		} else {
			out.ThinkingID = thinkingMsg.ID
		}
		thinkingBuf.Reset()
	}

	if resultContent != "" {
		assistantMsg := store.Message{
			ID:             uuid.New().String(),
			ConversationID: convID,
			Role:           "assistant",
			Content:        resultContent,
			Metadata:       metadata,
		}
		if err := p.SaveMessageWithRetry(ctx, assistantMsg); err != nil {
			log.Printf("[terminal] save assistant reply conv=%s len=%d: %v",
				convID, len(resultContent), err)
		} else {
			out.AssistantID = assistantMsg.ID
			out.AssistantOK = true
			// Auto-title gates on save success so we never title a
			// conversation off content that isn't actually in the DB.
			//
			// The push notification is NOT fired here: PersistResult runs
			// once per KindResult, and a single codex turn can emit several
			// (an agent_message per step in the new schema, or
			// agent_message + task_complete in the legacy one). Firing per
			// call would push one notification per chunk. The caller fires
			// MaybeNotify exactly once after the turn's stream drains —
			// see runClaudeRequest.
			//
			// Still fire-and-forget, but no longer unbounded: the generator
			// only forks a CLI if it can take an AutoTitleLimiter slot.
			go p.MaybeAutoTitle(convID)
		}
	}

	// Same reasoning as PersistTool: now that the assistant + thinking
	// chunks are in the DB, drop the in-flight replay buffer so a
	// reconnecting client won't re-render the same deltas on top of the
	// canonical REST history.
	if p.Broadcaster != nil {
		p.Broadcaster.ResetReplay(convID)
	}

	// SaveMessage bumps the conversation's `updated_at`, which is the
	// sort key for the sidebar. Fan a `conversation_updated` event out
	// on the user hub so peer tabs (and other-conversation tabs in the
	// same browser) reorder their session list to put this row back on
	// top — the active tab refreshes via REST on resultVersion, but
	// peer tabs would otherwise stay stale until the next manual reload.
	if out.AssistantOK || out.ThinkingID != "" {
		go p.BroadcastConvUpdated(convID)
	}
	return out
}

// BroadcastConvUpdated fans a `conversation_updated` event for the
// given conversation out on the owning user's hub. Best-effort — the
// nil-hub / nil-store / DB-error branches all silently no-op so a
// transient blip just leaves the peer sidebar to reconcile on its
// next REST refresh.
//
// Hub rooms are keyed by the *human's* id (see terminal_ws.go's
// hubUserID derivation); agent-owned conversations must therefore be
// resolved through Store.GetOwner before broadcasting, otherwise the
// event lands in an empty room and peer tabs stay stale until the next
// REST refresh.
func (p *MessagePersister) BroadcastConvUpdated(convID string) {
	if p.UserHub == nil || p.Store == nil || convID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conv, err := p.Store.GetConversation(ctx, convID)
	if err != nil || conv.UserID == "" {
		return
	}
	evt := struct {
		Type           string              `json:"type"`
		ConversationID string              `json:"conversation_id"`
		Conversation   *store.Conversation `json:"conversation"`
	}{
		Type:           "conversation_updated",
		ConversationID: convID,
		Conversation:   &conv,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return
	}
	p.UserHub.Broadcast(ResolveHubOwnerID(ctx, p.Store, conv.UserID), data)
}

// ResolveHubOwnerID maps a conversation's UserID to the hub room id that
// the owning human's WebSocket tabs are actually joined to. Returns the
// input unchanged for humans, the resolved owner id for agents, and falls
// back to the input on any store error so a nil store (tests) or DB blip
// behaves the same as the legacy direct-broadcast.
func ResolveHubOwnerID(ctx context.Context, s store.Store, userID string) string {
	if s == nil || userID == "" {
		return userID
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return userID
	}
	owner, err := s.GetOwner(ctx, user)
	if err != nil {
		return userID
	}
	return owner.ID
}
