package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// ConversationOps owns the conversation write path: create, the field
// updaters (work_dir / title / pinned / notifications / model), session
// rotation, share toggles, deletion and message clearing — plus the
// lifecycle fan-out to the owning user's tabs that every one of those has to
// perform after the store write lands.
//
// Deliberately a bare struct with public fields and no constructor, matching
// PromptRunner: the transport layer assembles one per call from whatever it
// currently holds. That matters because routes.go back-fills Broadcaster and
// UserHub onto the handler *after* the handler is constructed; a snapshot
// taken at construction time would freeze the nil values in place.
//
// Broadcaster, UserHub and Cfg are all optional. nil Broadcaster means "no WS
// state to reset" (the busy guard then never fires), nil UserHub means "no
// peer tabs to notify", nil Cfg means "no providers configured" — which the
// create path treats as a hard error rather than guessing a backend.
type ConversationOps struct {
	Store       store.Store
	Cfg         *config.Config
	Broadcaster *Broadcaster
	UserHub     *UserHub
}

func (o *ConversationOps) updateThinkLevel(ctx context.Context, id, level string) error {
	if err := o.Store.UpdateConversationThinkLevel(ctx, id, level); err != nil {
		return StoreError(err, "conversation not found")
	}
	return nil
}

// CreateConversationParams is the create use case's input. It mirrors the
// HTTP body one-for-one but carries no json tags or binding directives: those
// stay on the handler's request struct so the wire contract keeps living in
// the transport layer and this package stays free of gin.
type CreateConversationParams struct {
	Title    string
	UserID   string
	WorkDir  string
	Provider string
	Model    string
	// ThinkLevel nil inherits the Agent's configured default when that Agent has
	// a default model. A non-nil empty string explicitly selects the provider
	// default; other values are normalized by NormalizeThinkLevel.
	ThinkLevel *string
	// Account pins the conversation to a specific provider account within its
	// CLI type. Empty = the user's default account for that type. Must be one
	// of the user's allowed accounts for the resolved provider, else 400.
	Account string
}

// broadcastConv sends a conversation-lifecycle event to the owning
// user's hub. Centralised so the JSON shape stays consistent across
// the create / update / delete paths and so the nil-hub branch lives
// in one place. eventType is one of "conversation_added",
// "conversation_updated", "conversation_removed". For removed events
// only the id is needed; for the others the full row keeps peer tabs
// from having to round-trip REST.
//
// userID is the conversation's owner — which for agent-owned conversations
// is the agent's user id, not the human's. WebSocket clients join the hub
// under the *human's* id (see terminal_ws.go's hubUserID derivation), so
// broadcasting straight to an agent's id reaches nobody. We resolve via
// Store.GetOwner — humans resolve to themselves, agents resolve through
// their owner_id to the owning human. If the owner can't be resolved (orphan
// agent), we fall back to broadcasting under the original userID so the
// behaviour at least matches the legacy path.
func (o *ConversationOps) broadcastConv(ctx context.Context, eventType, userID string, conv *store.Conversation, convID string) {
	if o.UserHub == nil || userID == "" {
		return
	}
	msg := struct {
		Type           string              `json:"type"`
		ConversationID string              `json:"conversation_id,omitempty"`
		Conversation   *store.Conversation `json:"conversation,omitempty"`
	}{
		Type:           eventType,
		ConversationID: convID,
		Conversation:   conv,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	o.UserHub.Broadcast(ResolveHubOwnerID(ctx, o.Store, userID), data)
}

// RequireChatOwner enforces the "conversations belong to agents, and the
// caller must be able to speak for that agent" rule on the collection-level
// endpoints, which name their owner in the payload instead of inheriting it
// from a loaded row.
//
// Exported because the read-side List endpoint still gates on it from the
// transport layer; the write paths in this file call it internally.
//
// authedUID == "" is the unit-test bypass convention CanAccessOwner uses: no
// auth context means the agent-only check is skipped as well, otherwise every
// fixture would have to model an agent user.
func (o *ConversationOps) RequireChatOwner(ctx context.Context, authedUID, userID string) error {
	if userID == "" {
		return BadRequest("user_id is required")
	}
	if !CanAccessOwner(ctx, o.Store, authedUID, userID) {
		return Forbidden("forbidden")
	}
	if authedUID == "" || o.Store == nil {
		return nil
	}
	owner, err := o.Store.GetUser(ctx, userID)
	if err != nil {
		return StoreError(err, "user not found")
	}
	if owner.Username != "" || owner.OwnerID == "" {
		return BadRequest("conversations can only belong to agents")
	}
	return nil
}

// requireConv loads a conversation by id and verifies the caller owns it.
// The service-side twin of the transport helper: same three failure modes in
// the same order, so the response bodies are unchanged by the move.
func (o *ConversationOps) requireConv(ctx context.Context, convID, authedUID string) (store.Conversation, error) {
	if convID == "" {
		return store.Conversation{}, BadRequest("missing conversation id")
	}
	conv, err := o.Store.GetConversation(ctx, convID)
	if err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}
	if !CanAccessOwner(ctx, o.Store, authedUID, conv.UserID) {
		return store.Conversation{}, Forbidden("forbidden")
	}
	return conv, nil
}

// reload re-reads a row after a write so the response reflects store-side
// state (session_id, pin_order, share_token, …) rather than a locally patched
// copy, then fans the fresh row out to the owner's other tabs. Broadcast
// deliberately happens after the re-read: a peer tab that repopulated from a
// pre-write snapshot would show stale state until its next manual refresh.
func (o *ConversationOps) reload(ctx context.Context, convID string) (store.Conversation, error) {
	updated, err := o.Store.GetConversation(ctx, convID)
	if err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}
	o.broadcastConv(ctx, "conversation_updated", updated.UserID, &updated, updated.ID)
	return updated, nil
}

// Create makes a conversation for an agent owner, resolving the
// provider/model/account triple before the row is written so a conversation
// never exists pointing at a backend that can't serve it.
func (o *ConversationOps) Create(ctx context.Context, authedUID string, p CreateConversationParams) (store.Conversation, error) {
	if p.UserID == "" {
		return store.Conversation{}, BadRequest("user_id is required")
	}
	if err := o.RequireChatOwner(ctx, authedUID, p.UserID); err != nil {
		return store.Conversation{}, err
	}

	// Caller-supplied values win; otherwise inherit from the user's profile
	// (work_dir + the admin-set default model). One read serves the seeding
	// here and the account check further down.
	//
	// A read failure is fatal. It used to be swallowed here while the account
	// check below returned 500 on the very same error, and the tolerant half
	// was the harmful one: it persisted a conversation with an empty work_dir,
	// which silently re-roots the agent off the owner's home and is
	// indistinguishable from a deliberately blank one afterwards. Refusing the
	// create keeps the caller able to retry.
	workDir := p.WorkDir
	var owner store.User
	// Read only when a decision actually depends on the row: a request that
	// pins work_dir, the provider/model pair and no account needs nothing from
	// it, and failing such a request would be a new failure mode for no gain.
	if workDir == "" || (p.Provider == "" && p.Model == "") || p.Account != "" {
		var err error
		if owner, err = o.Store.GetUser(ctx, p.UserID); err != nil {
			return store.Conversation{}, StoreError(err, "user not found")
		}
	}
	if workDir == "" {
		workDir = owner.WorkDir
	}
	seed, err := ResolveConversationSeed(o.Cfg, owner, ConversationSeedRequest{
		Provider:   p.Provider,
		Model:      p.Model,
		Account:    p.Account,
		ThinkLevel: p.ThinkLevel,
	})
	if err != nil {
		return store.Conversation{}, err
	}

	id := uuid.New().String()
	row := store.NewConversation{ID: id, Title: p.Title, UserID: p.UserID, WorkDir: workDir, AccountName: p.Account}
	if err := CreateSeededConversation(ctx, o.Store, row, seed); err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}

	// Re-read so the response reflects store-side defaults (e.g.
	// notifications_enabled, session_id) instead of a hand-built
	// struct that drifts from the persisted row.
	conv, err := o.Store.GetConversation(ctx, id)
	if err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}
	o.broadcastConv(ctx, "conversation_added", conv.UserID, &conv, conv.ID)
	return conv, nil
}

// UpdateWorkDir repoints a conversation at a different working directory,
// which is only legal before the first user turn — afterwards the agent's
// session state is tied to the old tree.
func (o *ConversationOps) UpdateWorkDir(ctx context.Context, convID, authedUID, workDir string) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}

	// Sandbox: a conversation's work_dir must always live under the owning
	// user's (or agent's) home dir. Otherwise a non-admin could escape their
	// jail by POSTing an arbitrary absolute path here. This applies to admins
	// too — admins move their own home via /api/admin/users.
	owner, err := o.Store.GetUser(ctx, conv.UserID)
	if err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}
	if !workDirInsideHome(workDir, owner.WorkDir) {
		return store.Conversation{}, Forbidden("work_dir must be within the user's home")
	}

	hasMessages, err := o.Store.HasUserMessages(ctx, conv.ID)
	if err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}
	if hasMessages {
		return store.Conversation{}, Conflict("working directory is locked")
	}
	if err := o.Store.UpdateConversationWorkDir(ctx, conv.ID, workDir); err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}

	return o.reload(ctx, conv.ID)
}

// workDirInsideHome reports whether candidate is the owner's home dir itself
// or sits below it, with every symlink on both sides dereferenced first.
// Relative or empty paths are rejected outright — the API contract is an
// absolute path, and resolving a relative one against the server's cwd would
// silently move the jail.
//
// Containment goes through PathWithin, the same resolver the agent spawn path
// and the file handlers use, so a link planted inside the home dir cannot
// point the conversation at a tree outside it. Fails closed: an unresolvable
// path (dangling link, symlink loop, denied parent) counts as outside.
func workDirInsideHome(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	if !filepath.IsAbs(candidate) || !filepath.IsAbs(root) {
		return false
	}
	within, err := PathWithin(root, candidate)
	return err == nil && within
}

// UpdateTitle renames a conversation.
func (o *ConversationOps) UpdateTitle(ctx context.Context, convID, authedUID, title string) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := o.Store.UpdateConversationTitle(ctx, conv.ID, title); err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}
	return o.reload(ctx, conv.ID)
}

// UpdatePinned flips the pinned-to-top flag on a conversation. The
// sidebar groups pinned rows above unpinned ones (within each group,
// sorted by updated_at DESC). Persistent across reloads and broadcast
// to peer tabs so every open session sees the new order.
func (o *ConversationOps) UpdatePinned(ctx context.Context, convID, authedUID string, pinned bool) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := o.Store.UpdateConversationPinned(ctx, conv.ID, pinned); err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}
	return o.reload(ctx, conv.ID)
}

// UpdateNotifications toggles push notifications for a conversation.
func (o *ConversationOps) UpdateNotifications(ctx context.Context, convID, authedUID string, enabled bool) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}
	if err := o.Store.UpdateConversationNotifications(ctx, conv.ID, enabled); err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}
	return o.reload(ctx, conv.ID)
}

// MarkRead clears the conversation's attention flag because authedUID is
// looking at it, and tells that user's other tabs to drop their badge too.
func (o *ConversationOps) MarkRead(ctx context.Context, convID, authedUID string) error {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return err
	}
	if err := o.Store.SetConversationAttention(ctx, conv.ID, ""); err != nil {
		return StoreError(err, "conversation not found")
	}
	if o.UserHub != nil {
		if data, err := json.Marshal(ServerMessage{Type: "attention_changed", ConversationID: conv.ID}); err == nil {
			o.UserHub.Broadcast(authedUID, data)
		}
	}
	return nil
}

// Share mints (or re-returns) the public share token for a conversation.
func (o *ConversationOps) Share(ctx context.Context, convID, authedUID string) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}
	shared, err := o.Store.EnableConversationShare(ctx, conv.ID)
	if err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}
	o.broadcastConv(ctx, "conversation_updated", shared.UserID, &shared, shared.ID)
	return shared, nil
}

// Unshare revokes a conversation's public share token.
func (o *ConversationOps) Unshare(ctx context.Context, convID, authedUID string) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}
	updated, err := o.Store.DisableConversationShare(ctx, conv.ID)
	if err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}
	o.broadcastConv(ctx, "conversation_updated", updated.UserID, &updated, updated.ID)
	return updated, nil
}

// GetShared resolves a public share token to the conversation and its full
// message history. Unauthenticated by design — possession of the token is the
// credential — so there is no owner check here.
//
// The share token is stripped from the returned row: the reader already has
// it, and echoing it back would leak it into any downstream cache or copy of
// the public payload.
func (o *ConversationOps) GetShared(ctx context.Context, token string) (store.Conversation, []store.Message, error) {
	conv, err := o.Store.GetSharedConversation(ctx, token)
	if err != nil {
		return store.Conversation{}, nil, StoreError(err, "conversation not found")
	}
	msgs, err := o.listAllMessages(ctx, conv.ID)
	if err != nil {
		return store.Conversation{}, nil, Internal(err.Error(), err)
	}
	if msgs == nil {
		msgs = []store.Message{}
	}
	conv.ShareToken = ""
	return conv, msgs, nil
}

func (o *ConversationOps) listAllMessages(ctx context.Context, conversationID string) ([]store.Message, error) {
	const pageSize = 500
	var out []store.Message
	for offset := 0; ; offset += pageSize {
		msgs, err := o.Store.ListMessages(ctx, conversationID, pageSize, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
		if len(msgs) < pageSize {
			return out, nil
		}
	}
}

// ReorderPinned sets the manual order of a user's pinned conversations from a
// drag-to-reorder gesture. ids is the full ordered list of pinned conversation
// ids and the store treats it as authoritative (ids absent from the list get
// unpinned). Returns — and broadcasts — the refreshed list so every open
// session re-sorts.
func (o *ConversationOps) ReorderPinned(ctx context.Context, authedUID, userID string, ids []string) ([]store.Conversation, error) {
	if err := o.RequireChatOwner(ctx, authedUID, userID); err != nil {
		return nil, err
	}

	if err := o.Store.ReorderPinnedConversations(ctx, userID, ids); err != nil {
		return nil, Internal(err.Error(), err)
	}

	convs, err := o.Store.ListConversations(ctx, userID)
	if err != nil {
		return nil, Internal(err.Error(), err)
	}
	if convs == nil {
		convs = []store.Conversation{}
	}
	// Reuse the per-conversation broadcast so peer tabs re-sort. Each updated
	// row carries its new pinned/pin_order; the client merges by id.
	for i := range convs {
		o.broadcastConv(ctx, "conversation_updated", convs[i].UserID, &convs[i], convs[i].ID)
	}
	return convs, nil
}

// UpdateModel changes the provider+model pair on a conversation. The
// constraint matches the UI: the provider field is mutable only while the
// conversation has no active claude session (i.e. no message has been sent
// yet) — once the first turn establishes a session, switching providers
// would orphan the rollout on the previous CLI's disk layout. The model
// itself is always mutable within the locked provider so users can move
// between sonnet/opus mid-conversation.
//
// account, when non-nil, re-pins (or clears, when "") the account. nil leaves
// the pin untouched — unless the provider type changes, which always clears
// the now-stale pin.
//
// TODO(tx): UpdateConversationModel and UpdateConversationAccount are two
// separate writes; a crash between them can leave the row with only part of
// the requested account/think-level update. Atomicity needs a store-level
// transaction boundary, which is out of scope here.
func (o *ConversationOps) UpdateModel(ctx context.Context, convID, authedUID, provider, model string, account, thinkLevel *string) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}

	if provider == "" || model == "" {
		return store.Conversation{}, BadRequest("provider and model are required")
	}
	// Provider before account: the binding set is keyed by provider, so a
	// bogus provider name has no bindings under it and the account check would
	// report "account not bound to this user for provider xxx" for what is
	// really an unknown provider — naming the wrong field as the problem.
	if !IsValidProviderForConfig(o.Cfg, provider) {
		return store.Conversation{}, BadRequest("unknown provider")
	}
	if account != nil && *account != "" {
		owner, err := o.Store.GetUser(ctx, conv.UserID)
		if err != nil {
			return store.Conversation{}, Internal(err.Error(), err)
		}
		if !slices.Contains(owner.ProviderAccounts[provider], *account) {
			return store.Conversation{}, BadRequest("account not bound to this user for provider " + provider)
		}
	}

	// Account/provider changes are permanently locked after the first turn.
	// Apply that gate before live configuration validation: a removed account
	// must not turn an immutable started conversation into a misleading 400.
	changingProvider := provider != conv.Provider
	changingAccount := account != nil && *account != conv.AccountName
	if changingProvider || changingAccount {
		hasMessages, err := o.Store.HasUserMessages(ctx, conv.ID)
		if err != nil {
			return store.Conversation{}, Internal(err.Error(), err)
		}
		if hasMessages {
			if changingProvider {
				return store.Conversation{}, Conflict("cannot change provider after the conversation has started")
			}
			return store.Conversation{}, Conflict("cannot change account after the conversation has started")
		}
	}

	// Validate the model against the account it will actually run under: the
	// explicitly supplied account when present, otherwise the conversation's
	// existing pin. A provider switch without an explicit account clears the
	// old provider's pin below, so it validates against the new type default.
	effectiveAccount := conv.AccountName
	if account != nil {
		effectiveAccount = *account
	} else if changingProvider {
		effectiveAccount = ""
	}
	if effectiveAccount != "" && (o.Cfg == nil || o.Cfg.FindAccountForType(effectiveAccount, provider) == nil) {
		return store.Conversation{}, BadRequest("provider account is not configured for provider " + provider)
	}
	if !IsValidModelForAccount(o.Cfg, provider, effectiveAccount, model) {
		if len(ModelsForAccount(o.Cfg, provider, effectiveAccount)) == 0 {
			return store.Conversation{}, BadRequest(NoModelsConfiguredError(provider))
		}
		return store.Conversation{}, BadRequest("model does not belong to provider")
	}
	var normalizedThinkLevel string
	if thinkLevel != nil {
		normalizedThinkLevel, err = NormalizeThinkLevel(*thinkLevel)
		if err != nil {
			return store.Conversation{}, err
		}
	}

	if err := o.Store.UpdateConversationModel(ctx, conv.ID, provider, model); err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}

	// Account pin: an explicit value re-pins or clears it; otherwise a provider
	// type change invalidates the old pin (it belonged to the previous type's
	// account namespace) so we reset it to the default.
	if account != nil {
		if err := o.Store.UpdateConversationAccount(ctx, conv.ID, *account); err != nil {
			return store.Conversation{}, StoreError(err, "conversation not found")
		}
	} else if provider != conv.Provider && conv.AccountName != "" {
		if err := o.Store.UpdateConversationAccount(ctx, conv.ID, ""); err != nil {
			return store.Conversation{}, StoreError(err, "conversation not found")
		}
	}
	if thinkLevel != nil {
		if err := o.updateThinkLevel(ctx, conv.ID, normalizedThinkLevel); err != nil {
			return store.Conversation{}, err
		}
	}

	return o.reload(ctx, conv.ID)
}

// ResetSession rotates the conversation's session_id and wipes any
// cached token usage so the next user message starts a fresh claude CLI
// session with no prior context. The conversation row, its message
// history (DB), and the UI thread are intentionally left intact — this
// is *not* "new session" (which forks a brand new conversation); it's a
// "reset memory but keep the chat visible" toggle. Refused while a job
// is in flight on the same conversation, since rotating the session ID
// mid-run would orphan the running claude process from --resume.
//
// This is the canonical home for the rotate-and-reset sequence.
// PromptRunner.Compact deliberately does NOT route through here — see the
// note at its ResetConversationSession call.
//
// Deliberately the only write path here that sends no `conversation_updated`,
// and it should stay that way. That event exists to refresh sidebar rows: the
// client's handler splices the row into its session list and reads nothing
// else from it. This call changes only session_id and last_context_usage —
// neither of which the client renders from a conversation row — so the frame
// would carry a visually identical row and cost a round of re-renders for
// nothing. Every field the sidebar does show (title, pin, notifications,
// provider/model, share, work_dir) is untouched here.
//
// The reset that peer tabs *do* care about is the token bar, and
// ClearContextUsage below is what covers it: dropping the cached frame (the DB
// column is wiped by the same rotation) stops the stale bar from being
// replayed to any tab that joins or reconnects afterwards. A tab that is
// already open keeps showing its last bar until the next turn's context_usage
// frame overwrites it — accepted, because pushing an authoritative "bar is
// empty" frame needs a client handler that does not exist yet (`context_usage`
// with empty content is ignored on arrival, by design).
func (o *ConversationOps) ResetSession(ctx context.Context, convID, authedUID string) (store.Conversation, error) {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return store.Conversation{}, err
	}

	if o.Broadcaster != nil && o.Broadcaster.IsBusy(conv.ID) {
		return store.Conversation{}, Conflict("cannot clear context while a request is in flight")
	}

	if _, err := o.Store.ResetConversationSession(ctx, conv.ID); err != nil {
		return store.Conversation{}, StoreError(err, "conversation not found")
	}

	// Ordering matters: the in-memory wipe must follow the row rotation,
	// otherwise a peer tab can repopulate the bar from the pre-rotation row.
	if o.Broadcaster != nil {
		o.Broadcaster.ClearContextUsage(conv.ID)
	}

	updated, err := o.Store.GetConversation(ctx, conv.ID)
	if err != nil {
		return store.Conversation{}, Internal(err.Error(), err)
	}
	return updated, nil
}

// Delete removes a conversation, deletes the attachment files only it
// referenced, and tells the owner's other tabs to drop it from their sidebar.
func (o *ConversationOps) Delete(ctx context.Context, convID, authedUID string) error {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return err
	}
	if err := o.Store.DeleteConversation(ctx, conv.ID); err != nil {
		return StoreError(err, "conversation not found")
	}
	o.broadcastConv(ctx, "conversation_removed", conv.UserID, nil, conv.ID)
	removeUploadsInBackground(ctx, o.Store, []store.ConversationRef{{ID: conv.ID, UserID: conv.UserID}})
	return nil
}

// DeleteStale removes conversations for one agent that have not changed in
// staleFor, fans each removal out to the owner's open tabs, and deletes the
// attachment files only those conversations referenced.
func (o *ConversationOps) DeleteStale(ctx context.Context, userID, authedUID string, staleFor time.Duration) ([]string, error) {
	if err := o.RequireChatOwner(ctx, authedUID, userID); err != nil {
		return nil, err
	}
	ids, err := o.Store.DeleteConversationsUpdatedBefore(ctx, userID, time.Now().Add(-staleFor))
	if err != nil {
		return nil, StoreError(err, "failed to clean conversations")
	}
	refs := make([]store.ConversationRef, 0, len(ids))
	for _, id := range ids {
		o.broadcastConv(ctx, "conversation_removed", userID, nil, id)
		refs = append(refs, store.ConversationRef{ID: id, UserID: userID})
	}
	removeUploadsInBackground(ctx, o.Store, refs)
	return ids, nil
}

// ClearMessages empties a conversation's message history while keeping the
// row itself.
//
// No `conversation_updated` on the user hub: the sidebar row genuinely does
// not change, and the frontend's handler for that event only splices the row
// into its session list — it never touches the message list. What does go
// stale is the chat body of the owner's other tabs, which keep rendering
// messages that no longer exist until a manual refresh, so the reset is
// pushed into the conversation room instead as `messages_cleared`.
//
// BroadcastExcept with an empty exceptClientID is the established "reach every
// client, stay out of the replay buffer" idiom (see the auto-title push in
// message_persist.go): client ids are UUIDs so nobody is excluded, and a
// reconnecting tab reloads history from the store — where the rows are already
// gone — so buffering the frame would only risk replaying a stale wipe over
// messages sent after the clear.
func (o *ConversationOps) ClearMessages(ctx context.Context, convID, authedUID string) error {
	conv, err := o.requireConv(ctx, convID, authedUID)
	if err != nil {
		return err
	}
	if err := o.Store.ClearMessages(ctx, conv.ID); err != nil {
		return Internal(err.Error(), err)
	}
	o.broadcastMessagesCleared(conv.ID)
	return nil
}

// broadcastMessagesCleared tells every tab subscribed to the conversation to
// drop its rendered message list. Nil Broadcaster (the documented degraded
// wiring) means there is no room to push into.
func (o *ConversationOps) broadcastMessagesCleared(convID string) {
	if o.Broadcaster == nil {
		return
	}
	data, err := json.Marshal(ServerMessage{
		Type:           "messages_cleared",
		ConversationID: convID,
	})
	if err != nil {
		return
	}
	o.Broadcaster.BroadcastExcept(convID, "", data)
}
