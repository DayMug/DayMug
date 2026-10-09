package store

import (
	"context"
	"time"
)

// Store is the full persistence surface the rest of the application depends
// on. SQLiteStore is the production implementation; storetest.Fake is the
// in-memory double used by handler and service tests.
//
// It is the union of the per-domain interfaces below. Code that only touches
// one domain can depend on that interface instead, which keeps its test
// doubles small. Every domain is required: a capability that production always
// has must not be probed with a type assertion, because a double that lacks it
// would silently drop the feature (routes never mounted) instead of failing to
// compile.
type Store interface {
	MaintenanceStore
	UserStore
	ProviderBindingStore
	SessionStore
	ConversationStore
	MessageStore
	MessageQueueStore
	AppSettingStore
	BotThreadStore
	UsageStore
	UsageInsightsStore
	BotStore
	CronStore
	MarketplaceStore
}

// MaintenanceStore is the database lifecycle: schema init, engine identity,
// housekeeping, and shutdown.
type MaintenanceStore interface {
	Init() error

	// Backend returns a stable identifier for the underlying database
	// engine (currently always "sqlite"). The admin UI uses this to gate
	// engine-specific operations such as VACUUM.
	Backend() string

	// OptimizeDatabase reaps stale rows and reclaims unused disk space.
	// Backends that cannot meaningfully implement this should still
	// satisfy the interface — they may return a zero result.
	OptimizeDatabase(ctx context.Context) (DBOptimizeResult, error)

	// DatabaseSize returns the on-disk size in bytes of the database.
	// In-memory backends and engines that don't expose a single file
	// (e.g. networked databases) may return 0 — callers display it
	// verbatim, so 0 simply renders as "unknown" in the UI.
	DatabaseSize(ctx context.Context) (int64, error)
	Close() error
}

// UserStore owns the users table: humans and the agents they own, their
// per-user settings, and the sidebar order / archive state of agents.
type UserStore interface {
	CreateUser(ctx context.Context, user User) error
	// CreateFirstAdmin atomically inserts the first-run admin and its default
	// Agent, failing with ErrSetupClosed once any login-capable user exists.
	CreateFirstAdmin(ctx context.Context, admin, agent User) error
	GetUser(ctx context.Context, id string) (User, error)
	GetUserByUsername(ctx context.Context, username string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)

	// GetOwner returns the human "owner" row for a given user. Humans are
	// their own owner; agents (rows with empty username) resolve through
	// their OwnerID pointer — never by email, which is an OIDC/login
	// identifier only. Returns ErrNotFound for orphan agents (empty or
	// dangling OwnerID), kept as an explicit error so callers don't
	// silently route writes into the wrong directory.
	GetOwner(ctx context.Context, user User) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateUser(ctx context.Context, user User) error
	SetUserPassword(ctx context.Context, userID, passwordHash string) error
	SetUserAdmin(ctx context.Context, userID string, isAdmin bool) error
	SetUserDisabled(ctx context.Context, userID string, disabled bool) error
	SetUserWorkDir(ctx context.Context, userID, workDir string) error
	SetUserEmail(ctx context.Context, userID, email string) error

	// SetUserEnv writes the user's newline-delimited environment configuration
	// injected into this user's agent child processes.
	SetUserEnv(ctx context.Context, userID, env string) error

	// SetUserDefaultModel writes the per-user default model id new
	// conversations inherit. Empty string clears the default. Validation
	// (model belongs to a configured provider) belongs to the caller — the
	// store accepts any string. Returns ErrNotFound when no row matches.
	SetUserDefaultModel(ctx context.Context, userID, model string) error

	// SetUserSandboxMode writes the per-user agent isolation mode
	// (SandboxModeJailed / SandboxModeUnrestricted). Returns ErrNotFound when
	// no row matches.
	SetUserSandboxMode(ctx context.Context, userID, mode string) error
	SetUserBarkURL(ctx context.Context, userID, barkURL string) error

	// SetUserPushDeerKey writes the PushDeer push key for a user. Notification
	// config is per-human-owner; the caller passes a human row's id.
	SetUserPushDeerKey(ctx context.Context, userID, pushDeerKey string) error

	// SetUserNotificationChannel picks which configured channel ("bark" or
	// "pushdeer") is used to deliver notifications when both are populated.
	// Empty string clears the explicit choice and lets the runtime auto-pick
	// whichever channel is configured.
	SetUserNotificationChannel(ctx context.Context, userID, channel string) error

	DeleteUser(ctx context.Context, id string) error

	// ReorderAgents sets the manual sidebar order of the rows owned by ownerID
	// — the caller's own human row plus the agents it owns: each id gets
	// sort_order = its index in the list, so the human owner can be interleaved
	// with its agents. Ids the caller doesn't own match no row and are skipped.
	// Runs in one transaction so a concurrent list never sees a half-applied
	// order.
	ReorderAgents(ctx context.Context, ownerID string, ids []string) error

	// ArchiveUser hides an agent from the sidebar by stamping archived_at.
	// Idempotent: returns ErrNotFound when the row is missing or already
	// archived.
	ArchiveUser(ctx context.Context, id string) error

	// UnarchiveUser clears archived_at, restoring the agent to the sidebar.
	// Returns ErrNotFound when the row is missing or not archived.
	UnarchiveUser(ctx context.Context, id string) error

	// ListArchivedAgents returns the archived agents owned by ownerID, ordered
	// like the sidebar (sort_order, then created_at).
	ListArchivedAgents(ctx context.Context, ownerID string) ([]User, error)
}

// ProviderBindingStore owns which provider accounts each user may run on.
type ProviderBindingStore interface {
	// GetUserProviderBindings returns the user's full {cliType → providerName}
	// binding map. Empty map (not nil) when the user has none. Callers
	// pass the resolved providerName to Pool.AccountForType for the
	// actual config lookup + type validation.
	GetUserProviderBindings(ctx context.Context, userID string) (map[string]string, error)

	// GetUserProviderAccounts returns the full {cliType → []accountName} set
	// the user may use, default account first. Empty map (not nil) when none.
	GetUserProviderAccounts(ctx context.Context, userID string) (map[string][]string, error)

	// SetUserProviderBinding upserts a single (userID, providerType) → name
	// row and marks it the default for that type. Empty providerName deletes
	// every account for that type so the runtime falls back to the implicit
	// default. Returns ErrNotFound when the referenced user does not exist;
	// downstream callers translate that into 404.
	SetUserProviderBinding(ctx context.Context, userID, providerType, providerName string) error

	// SetUserProviderAccounts replaces the user's allowed account set for one
	// CLI type. names is the full set; defaultName (must be in names, or empty
	// to auto-pick names[0]) becomes the type's default. Empty names clears the
	// type. Returns ErrNotFound when the user does not exist.
	SetUserProviderAccounts(ctx context.Context, userID, providerType string, names []string, defaultName string) error

	// DeleteUserProviderBinding removes the (userID, providerType) row.
	// No-op when no row matches (idempotent — admins clicking "clear" on
	// an already-empty slot don't get an error).
	DeleteUserProviderBinding(ctx context.Context, userID, providerType string) error
}

// SessionStore owns login sessions (the session cookie's backing rows).
type SessionStore interface {
	CreateSession(ctx context.Context, sess Session) error
	GetSession(ctx context.Context, token string) (Session, error)
	DeleteSession(ctx context.Context, token string) error
	DeleteExpiredSessions(ctx context.Context) error
}

// ConversationStore owns conversation rows: their metadata, per-conversation
// settings, and the agent session id each one resumes.
type ConversationStore interface {
	CreateConversation(ctx context.Context, id, title, userID, workDir, provider, model string) error
	// CreateConversationRecord creates a conversation with its full
	// creation-time state in one atomic write.
	CreateConversationRecord(ctx context.Context, c NewConversation) error
	GetConversation(ctx context.Context, id string) (Conversation, error)
	UpdateConversationTitle(ctx context.Context, id, title string) error
	UpdateConversationWorkDir(ctx context.Context, id, workDir string) error
	UpdateConversationNotifications(ctx context.Context, id string, enabled bool) error
	// SetConversationAttention records (or with "" clears) whether the
	// conversation needs its owner. ListConversationAttention returns every
	// flagged conversation in the live agents ownerID owns, newest first.
	SetConversationAttention(ctx context.Context, id, state string) error
	ListConversationAttention(ctx context.Context, ownerID string) ([]ConversationAttention, error)
	// SetConversationSuspendedQuestion stores (or with "" clears) the question
	// payload a turn was parked on when a drain ended it;
	// GetConversationSuspendedQuestion reads it back ("" when none).
	SetConversationSuspendedQuestion(ctx context.Context, id, payload string) error
	GetConversationSuspendedQuestion(ctx context.Context, id string) (string, error)
	// SetConversationRotatedFrom records the conversation id was rotated away
	// from; GetConversationRotatedFrom reads it back ("" for a conversation
	// that started its thread). Soft-deleted rows still answer, so one deleted
	// conversation does not cut the chain.
	SetConversationRotatedFrom(ctx context.Context, id, parentID string) error
	GetConversationRotatedFrom(ctx context.Context, id string) (string, error)
	UpdateConversationThinkLevel(ctx context.Context, id, level string) error

	// EnableConversationShare mints (or reuses) the conversation's public share
	// token; DisableConversationShare revokes it. GetSharedConversation resolves
	// a token, ErrNotFound when it is unknown or revoked.
	EnableConversationShare(ctx context.Context, id string) (Conversation, error)
	DisableConversationShare(ctx context.Context, id string) (Conversation, error)
	GetSharedConversation(ctx context.Context, token string) (Conversation, error)

	// DeleteConversationsUpdatedBefore removes a user's conversations idle
	// since before and returns their ids.
	DeleteConversationsUpdatedBefore(ctx context.Context, userID string, before time.Time) ([]string, error)

	// UpdateConversationPinned toggles the "pinned to top" flag. The sidebar
	// renders pinned rows above unpinned ones (within each group, ordered by
	// updated_at DESC — same ListConversations sort key). ErrNotFound when
	// the conversation row is missing.
	UpdateConversationPinned(ctx context.Context, id string, pinned bool) error

	// ReorderPinnedConversations makes the supplied id list the authoritative
	// pinned set for a user: every id becomes pinned with pin_order = its index,
	// and any of the user's previously-pinned conversations absent from the list
	// is unpinned. Atomic. Ids not owned by userID are ignored.
	ReorderPinnedConversations(ctx context.Context, userID string, ids []string) error

	// UpdateConversationModel sets the (provider, model) pair on a
	// conversation. The caller is responsible for validation: that the model
	// belongs to the provider, and that the provider is allowed to change
	// (the conversation handler refuses provider changes once a claude
	// session has been minted). ErrNotFound when no row matches.
	UpdateConversationModel(ctx context.Context, id, provider, model string) error

	// UpdateConversationAccount pins (or clears, when account == "") the
	// provider account a conversation runs against. The caller validates that
	// the account is in the user's allowed set for the conversation's type.
	// ErrNotFound when no row matches.
	UpdateConversationAccount(ctx context.Context, id, account string) error
	UpdateConversationContextUsage(ctx context.Context, id, payload string) error
	ResetConversationSession(ctx context.Context, id string) (string, error)

	// SetSessionID overwrites a conversation's session_id. Used by the
	// streaming loop when a backend that assigns its own session id
	// (codex) reports the chosen id via system_init so the next turn
	// can resume the right rollout. ErrNotFound when the conversation
	// row is missing.
	SetSessionID(ctx context.Context, id, newSessionID string) error
	GetSessionID(ctx context.Context, conversationID string) (string, error)
	ListConversations(ctx context.Context, userID string) ([]Conversation, error)

	// ListConversationsPage is the bounded read the chat shell actually uses.
	// ListConversations is the whole-list convenience wrapper on top of it and
	// still ships up to ConversationListHardCap rows, which is far more than a
	// sidebar ever paints — anything on the page-load path should page instead.
	ListConversationsPage(ctx context.Context, userID string, query ConversationQuery) (ConversationPage, error)
	DeleteConversation(ctx context.Context, id string) error
	// HasModelReply reports whether the model has really taken part in the
	// conversation: it has answered (an assistant row) or acted (a tool row),
	// or a turn is running right now. An assistant row whose usage reports
	// zero input and zero output tokens is the CLI relaying a failure (bad
	// credentials, an unsupported model) rather than a reply, and does not
	// count; neither do error rows or user prompts that never ran. Rows with
	// no usage at all (imported history, providers that don't report usage)
	// count, so an unknown turn errs on the side of "started".
	HasModelReply(ctx context.Context, conversationID string) (bool, error)
	CountUserMessages(ctx context.Context, conversationID string) (int, error)
}

// MessageStore owns a conversation's transcript: persisting rows and reading
// them back in pages.
type MessageStore interface {
	SaveMessage(ctx context.Context, msg Message) error

	// ExclusiveAttachmentPaths lists the attachment paths a conversation's
	// messages record that no other live conversation records. It still
	// reads a soft-deleted conversation, which is when it is meant to run.
	ExclusiveAttachmentPaths(ctx context.Context, conversationID string) ([]string, error)

	// SaveImportedMessage persists a message carrying a stable upstream
	// source id. It carries an explicit createdAt and uses INSERT OR IGNORE
	// keyed on the (conversation_id, source_id) unique index, so replaying
	// the same CLI transcript line or IM platform message is a no-op.
	// Returns whether a new row was actually inserted.
	SaveImportedMessage(ctx context.Context, msg Message, createdAt time.Time) (bool, error)

	// LatestMessageTime returns the created_at of the newest message in
	// the conversation. ok is false when the conversation has no messages
	// yet. The transcript importer uses it as a high-water mark — only
	// transcript lines newer than this are imported — so a conversation
	// that was used in chat mode before tty mode never re-imports its
	// existing turns, and repeated imports only append what's new.
	LatestMessageTime(ctx context.Context, conversationID string) (time.Time, bool, error)

	// LatestAssistantMessage returns the newest agent reply in a conversation,
	// or ErrNotFound. Case-mode rotation reads it out of the conversation it
	// is retiring — no other surface still holds that text.
	LatestAssistantMessage(ctx context.Context, conversationID string) (Message, error)
	ListMessages(ctx context.Context, conversationID string, limit, offset int) ([]Message, error)

	// ListMessagesAfter returns messages persisted strictly after the one
	// identified by lastMessageID, ordered by insertion order. Used to
	// backfill a reconnecting client with anything that landed while it
	// was offline. Empty or unknown lastMessageID returns every message
	// in the conversation — the caller's UI is expected to dedup against
	// any locally-loaded copies by id.
	ListMessagesAfter(ctx context.Context, conversationID, lastMessageID string, limit int) ([]Message, error)

	// ListMessagesBefore returns up to `limit` messages persisted strictly
	// before the one identified by beforeMessageID, ordered ascending
	// (oldest first). Empty or unknown beforeMessageID returns the most
	// recent `limit` messages. Used by the frontend's lazy-load flow:
	// the initial page-load fetches the latest page, then scroll-to-top
	// pages backwards using the oldest loaded id as the cursor.
	ListMessagesBefore(ctx context.Context, conversationID, beforeMessageID string, limit int) ([]Message, error)
	ClearMessages(ctx context.Context, conversationID string) error
}

// MessageQueueStore is the durable prompt queue the dispatcher drains: user
// prompts carry a queue_status until a worker has finished them.
type MessageQueueStore interface {
	// EnqueuePrompt persists a user prompt with queue_status='pending' and
	// returns the saved Message. Used by the dispatcher entry point — every
	// inbound "input" WS message goes through here so the prompt survives a
	// browser close and shows up in history immediately, regardless of
	// whether the worker has had a chance to start it yet.
	EnqueuePrompt(ctx context.Context, conversationID, content string) (Message, error)

	// PeekNextPendingPrompt returns the oldest pending prompt for the
	// conversation without changing its state. The dispatcher uses this
	// before acquiring a pool slot so a prompt only leaves the staging
	// area (via ClaimPendingPromptByID + prompt_started broadcast) once a
	// real account-pool slot has been granted. ErrNotFound when nothing
	// is pending.
	PeekNextPendingPrompt(ctx context.Context, conversationID string) (Message, error)

	// ClaimPendingPromptByID atomically transitions a specific 'pending'
	// row to 'processing'. Returns ErrNotFound if the row was deleted by a
	// concurrent user cancel (or never existed / was already claimed).
	// Used by the dispatcher right after a pool slot is acquired for the
	// prompt it peeked.
	ClaimPendingPromptByID(ctx context.Context, messageID string) (Message, error)

	// MarkPromptDone clears queue_status on a previously-claimed prompt so
	// it becomes a normal historical message. Called by the dispatcher
	// after the claude run finishes (success, error, or cancelled — partial
	// output is preserved either way).
	MarkPromptDone(ctx context.Context, messageID string) error

	// DeletePendingPrompts removes every 'pending' prompt for the
	// conversation and returns the deleted rows so the cancel path can ship
	// their content back to the client to repopulate the input editor. A
	// 'processing' prompt (currently running through claude) is left alone:
	// its partial output is already tied to it in history, so deleting the
	// row would orphan the assistant/tool messages that came from it.
	DeletePendingPrompts(ctx context.Context, conversationID string) ([]Message, error)

	// DeletePendingPrompt removes a single 'pending' prompt by id. Returns
	// the deleted row, or ErrNotFound if no row matches (already claimed,
	// already done, or never existed). 'processing' rows are protected the
	// same way DeletePendingPrompts protects them — the in-flight prompt
	// owns assistant/tool history that would orphan if its anchor row went
	// away. Used by the per-message recall affordance in the staging area.
	DeletePendingPrompt(ctx context.Context, messageID string) (Message, error)

	// ClearPoolQueuedPrompts drops the QueueStatusPoolQueued marker from every
	// row still carrying it. Called once at startup: an IM turn cannot outlive
	// the process, so a surviving marker would show a queue card for a turn
	// nobody is waiting on. Returns the number of rows cleared.
	ClearPoolQueuedPrompts(ctx context.Context) (int, error)

	// ResetProcessingToPending demotes leftover 'processing' rows back to
	// 'pending' so the dispatcher can re-run them on the next worker loop.
	// Called once at startup to recover from a crash that interrupted a
	// claude run mid-flight. Each demotion increments the row's
	// recovery_count; a prompt whose count would cross maxPromptRecoveries
	// is instead abandoned (queue_status cleared) rather than resumed, so a
	// prompt that itself triggers the shutdown — e.g. one that restarts the
	// service — can't wedge the server in an endless crash-recover-rerun
	// loop. Returns (recovered, abandoned) counts. Idempotent and safe to
	// call when no rows match.
	ResetProcessingToPending(ctx context.Context) (recovered, abandoned int, err error)

	// ListConversationsWithPending returns every conversation id that
	// currently has at least one prompt in the queue (pending or
	// processing). Used by the dispatcher at startup to spin up workers
	// for unfinished work.
	ListConversationsWithPending(ctx context.Context) ([]string, error)
}

// AppSettingStore is the key/value table behind admin-editable settings.
type AppSettingStore interface {
	// GetAppSetting returns the value stored under the given key. Returns
	// "" (no error) when the key has never been written — callers treat
	// "no value" and "empty value" as the same state.
	GetAppSetting(ctx context.Context, key string) (string, error)

	// SetAppSetting upserts a key/value pair. Empty value is a valid state
	// (means "cleared"); deletion of the row is not exposed because the
	// distinction adds no value to callers.
	SetAppSetting(ctx context.Context, key, value string) error
}

// BotThreadStore maps IM threads to the agent sessions serving them, and holds
// the durable case file of threads running in case-file mode.
type BotThreadStore interface {
	// GetBotThread returns the IM-thread → agent-session binding, or
	// ErrNotFound when the thread has never been served.
	GetBotThread(ctx context.Context, platform, channelID, threadID string) (BotThread, error)

	// UpsertBotThread creates or refreshes a thread binding after a run.
	UpsertBotThread(ctx context.Context, t BotThread) error

	// GetBotThreadByConversation returns the thread a conversation mirrors,
	// ErrNotFound for a web-only conversation.
	GetBotThreadByConversation(ctx context.Context, conversationID string) (BotThread, error)

	// IsBotConversation reports whether any IM thread is bound to the
	// conversation.
	IsBotConversation(ctx context.Context, conversationID string) (bool, error)

	// GetThreadCase returns the durable case for an IM thread in case-file
	// mode. A thread with no case yet returns a zero-version case, not an
	// error — the first turn of every thread hits that path.
	GetThreadCase(ctx context.Context, channelID, threadID string) (ThreadCase, error)

	// SaveThreadCase writes a new case revision and appends it to history.
	SaveThreadCase(ctx context.Context, channelID, threadID, doc, updatedBy string) (ThreadCase, error)

	// ListThreadCaseHistory returns case revisions newest-first.
	ListThreadCaseHistory(ctx context.Context, channelID, threadID string, limit int) ([]ThreadCase, error)
}

// UsageStore owns the daily token-usage aggregate.
type UsageStore interface {
	// AddTokenUsage adds a per-turn token usage delta to the
	// (user_id, model, day) aggregate. Day is the UTC date at insert time;
	// repeat calls on the same day collapse into a single row via ON CONFLICT.
	AddTokenUsage(ctx context.Context, delta TokenUsageDelta) error

	// AggregateTokenUsage returns daily-grouped usage matching the filter,
	// ordered ascending by (day, user_id, model). Used by both the per-user
	// settings panel (UserID set) and the admin panel (UserID empty).
	AggregateTokenUsage(ctx context.Context, q TokenUsageQuery) ([]TokenUsageRecord, error)

	// ListTokenUsageModels returns the distinct model names that have ever
	// recorded usage. Drives the model filter dropdown in the UI.
	ListTokenUsageModels(ctx context.Context) ([]string, error)

	// Billing and the attributed usage ledger. Part of the contract rather
	// than optional capabilities: a store that silently lacked them would
	// drop cost and usage records without any error.
	UsageEventRecorder
	ClaudeUsageRecorder
}

// BotStore owns the IM platform connections attached to agents.
type BotStore interface {
	CreateBot(ctx context.Context, bot Bot) error
	GetBot(ctx context.Context, id string) (Bot, error)
	ListBots(ctx context.Context, agentID string) ([]Bot, error)
	UpdateBot(ctx context.Context, bot Bot) error
	DeleteBot(ctx context.Context, id, agentID string) error
}

// UsageInsightsStore answers the event-level usage dashboard.
type UsageInsightsStore interface {
	QueryUsageInsights(context.Context, UsageInsightsQuery, UsageThresholds) (UsageInsightsResponse, error)
}

// CronStore owns recurring Agent prompts and their last-run bookkeeping.
type CronStore interface {
	CreateCronJob(ctx context.Context, job CronJob) error
	GetCronJob(ctx context.Context, id string) (CronJob, error)
	ListCronJobs(ctx context.Context, ownerID string) ([]CronJob, error)
	ListEnabledCronJobs(ctx context.Context) ([]CronJob, error)
	UpdateCronJob(ctx context.Context, job CronJob) error
	DeleteCronJob(ctx context.Context, id, ownerID string) error
	DisableCronJob(ctx context.Context, id, reason string) error
	RecordCronJobRun(ctx context.Context, id string, runAt time.Time, conversationID, runError string) error

	// CronBotDeliveryTarget resolves the IM thread a scheduled run's answer is
	// relayed into; ErrNotFound means the run stays web-only.
	CronBotDeliveryTarget(ctx context.Context, conversationID, messageID string) (CronDelivery, error)
}

// MarketplaceStore owns the community-managed application marketplace.
type MarketplaceStore interface {
	CreateMarketplaceApp(ctx context.Context, app MarketplaceApp) (MarketplaceApp, error)
	UpdateMarketplaceApp(ctx context.Context, app MarketplaceApp) (MarketplaceApp, error)
	ListMarketplaceApps(ctx context.Context) ([]MarketplaceApp, error)
	DeleteMarketplaceApp(ctx context.Context, id string) error
}

// SQLiteStore must implement every interface callers depend on — including the
// narrow recorders the stream persistence still feature-detects at runtime,
// where a missing method would silently stop recording usage rather than fail
// the build.
var (
	_ Store               = (*SQLiteStore)(nil)
	_ BotStore            = (*SQLiteStore)(nil)
	_ CronStore           = (*SQLiteStore)(nil)
	_ MarketplaceStore    = (*SQLiteStore)(nil)
	_ UsageInsightsStore  = (*SQLiteStore)(nil)
	_ UsageEventRecorder  = (*SQLiteStore)(nil)
	_ ClaudeUsageRecorder = (*SQLiteStore)(nil)
)
