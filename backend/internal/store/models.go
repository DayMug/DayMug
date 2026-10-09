package store

import (
	"encoding/json"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

// ErrSetupClosed is returned by CreateFirstAdmin when a login-capable user
// already exists, i.e. the install is past first run.
var ErrSetupClosed = errors.New("first-run setup is closed: a user already exists")

// User sandbox modes — see User.SandboxMode. Stored verbatim in the
// users.sandbox_mode column; any unrecognised value (including "") is treated
// as jailed by SandboxUnrestricted so the fail-safe is "more isolation".
const (
	SandboxModeJailed       = "jailed"
	SandboxModeUnrestricted = "unrestricted"
)

// SandboxUnrestricted reports whether the given sandbox_mode value grants the
// agent the server user's full filesystem reach. Only the exact
// SandboxModeUnrestricted value qualifies; everything else (including "" from
// pre-migration rows) is jailed.
func SandboxUnrestricted(mode string) bool {
	return mode == SandboxModeUnrestricted
}

type User struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Email        string `json:"email"`
	IsAdmin      bool   `json:"is_admin"`
	Disabled     bool   `json:"disabled"`
	// OwnerID is the referential owner pointer for agent rows (Username == "").
	// It holds the id of the human (login) row that owns the agent — a
	// TEXT-by-convention FK to users.id, matching the unenforced style of
	// conversations.user_id (SQLite ALTER can't add enforced FKs). Human rows
	// own themselves and carry the empty default. This replaces the legacy
	// email-match ownership invariant: email is now consulted only for
	// OIDC/login. Resolve via User.Owner() / Store.GetOwner, never by email.
	OwnerID string `json:"owner_id"`
	// ProviderBindings is the full per-CLI-type binding map for this user.
	// Keys are CLI type strings ("claude", "codex"); values are provider
	// names referencing entries in the database-backed provider registry. Populated by store
	// reads alongside Provider. Empty/nil when the user has no
	// bindings (resolves to the implicit "default" account at runtime).
	ProviderBindings map[string]string `json:"provider_bindings,omitempty"`
	// ProviderAccounts is the full SET of accounts this user may use per CLI
	// type — keys are CLI type strings, values are account-name lists with the
	// default account first. ProviderBindings[type] always equals
	// ProviderAccounts[type][0]. A conversation may pin any name from this set;
	// the dispatcher re-validates against it so a revoked account stops working
	// even on conversations that already pinned it. Empty/nil when unbound.
	ProviderAccounts map[string][]string `json:"provider_accounts,omitempty"`
	// DefaultModel is the optional model id new conversations inherit when
	// the create caller pins neither provider nor model. Empty means "no
	// per-user default" — the create path falls back to the server's default
	// provider + that provider's latest model. The id implies its provider
	// (a model belongs to exactly one provider in ProviderModels), so the
	// create path resolves both from this single field. Set in bulk from the
	// admin user panel for humans, or the Agent / Bot settings form for subjects.
	DefaultModel string `json:"default_model"`
	// ThinkLevel is the optional per-agent reasoning effort. Empty leaves the
	// provider default unchanged.
	ThinkLevel string `json:"think_level"`
	// CaseMode switches every IM bot under this Agent from one ever-growing
	// session per thread to the case-file model: the durable state of the work
	// lives in a bounded document, and the CLI session behind it is rotated
	// once its context crosses a threshold. Agent-only; humans always read 0.
	CaseMode bool `json:"case_mode"`
	// SandboxMode selects the filesystem isolation applied to this user's
	// agent child processes when the bwrap sandbox is enabled server-side.
	// SandboxModeUnrestricted runs the agent with the server user's full
	// filesystem reach (admins / trusted operators); SandboxModeJailed (the
	// default) confines reads+writes to the user's work_dir. Empty is treated
	// as jailed — fail-safe so a column added on upgrade never silently grants
	// full-host access. Has no effect while config sandbox.enabled is false.
	SandboxMode    string `json:"sandbox_mode"`
	WorkDir        string `json:"work_dir"`
	Avatar         string `json:"avatar"`
	RoleDefinition string `json:"role_definition"`
	// Env is the user's newline-delimited VAR=VAL configuration layered onto
	// every agent child process. Agents inherit their owner's row at runtime.
	Env             string `json:"env"`
	McpConfig       string `json:"mcp_config"`
	ClaudeMdContent string `json:"claude_md_content"`
	ManageClaudeMd  bool   `json:"manage_claude_md"`
	BarkURL         string `json:"bark_url"`
	// PushDeerKey is the per-human-owner PushDeer push key. Notification
	// config lives on the human owner (not the agent) so agent conversations
	// resolve to their owner before reading this — see maybeNotify in the
	// terminal handler.
	PushDeerKey string `json:"pushdeer_key"`
	// NotificationChannel selects which configured channel is used to send
	// notifications. Only meaningful when both BarkURL and PushDeerKey are
	// set; otherwise the runtime falls back to whichever one is configured.
	// Allowed values: "", "bark", "pushdeer".
	NotificationChannel string `json:"notification_channel"`
	// SortOrder is the manual position of an agent within its owner's sidebar
	// list, set by drag-to-reorder. 0 for humans and never-reordered agents;
	// ListUsers orders by it ascending, then created_at as a stable tie-break.
	SortOrder int `json:"sort_order"`
	// Archived is true when the agent has been hidden from the sidebar via the
	// right-click "Archive" action. Archived agents stay on disk and can be
	// restored from the owner's settings page. Derived from the archived_at
	// column (non-NULL == archived).
	Archived bool `json:"archived"`

	// BotPlatforms lists the platforms of the agent's attached transport
	// connections. Response decoration assembled by the user handler — never
	// persisted on the users or agents tables.
	BotPlatforms []string `json:"bot_platforms,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// Bot is one IM platform connection attached to an Agent. Persona, prompt,
// tools, work directory, provider bindings, and conversations remain owned by
// AgentID; a Bot carries transport settings plus an optional model override
// for new threads.
// The four credential fields are write-only: `json:"-"` keeps a Slack
// `xoxb-`/`xapp-` token or a 飞书 app secret from ever reaching a client, even
// if some future handler marshals a Bot directly. Whoever holds them owns the
// workspace bot outright — far beyond what a DayMug session grants — so the
// API only ever reports whether each one is set (see handler.botResponse).
type Bot struct {
	ID       string `json:"id"`
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Enabled  bool   `json:"enabled"`
	Model    string `json:"model"`
	// MaxConversationDuration is a Go duration such as "3h". Empty uses the
	// 12-hour product default; 0 keeps one platform thread bound indefinitely.
	MaxConversationDuration string    `json:"max_conversation_duration"`
	BotToken                string    `json:"-"`
	BotAppToken             string    `json:"-"`
	BotAppID                string    `json:"-"`
	BotAppSecret            string    `json:"-"`
	Channels                string    `json:"channels"`
	UnconfiguredReply       string    `json:"unconfigured_reply"`
	UnauthorizedReply       string    `json:"unauthorized_reply"`
	CreatedAt               time.Time `json:"created_at"`
}

// Owner returns the id of the human who owns this row: the user's own id when
// it is a human (non-empty Username), or its OwnerID when it is an agent. An
// orphan agent (empty OwnerID) returns "" — callers treat that as "no owner",
// mirroring the ErrNotFound that GetOwner returns for the same shape. This is
// the single source of truth for ownership; do not reconstruct it from email.
func (u User) Owner() string {
	if u.Username != "" {
		return u.ID
	}
	return u.OwnerID
}

type Session struct {
	Token     string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// NewConversation is every column a conversation is born with. Creation paths
// fill it in full and hand it to CreateConversationRecord instead of creating
// a bare row and patching it with follow-up UPDATEs, which a crash or error in
// between would leave half-seeded.
type NewConversation struct {
	ID       string
	Title    string
	UserID   string
	WorkDir  string
	Provider string
	Model    string
	// ThinkLevel, AccountName: "" leaves the provider default / no pin.
	ThinkLevel  string
	AccountName string
	// SessionID continues an existing provider session; "" mints a fresh id.
	SessionID string
	// MuteNotifications starts the conversation with push notifications off.
	MuteNotifications bool
	// SourceType attributes usage ("" = manual); CronJobID names the job for
	// scheduled runs.
	SourceType string
	CronJobID  string
}

type Conversation struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Title  string `json:"title"`
	// Provider names the underlying agent CLI this conversation talks to
	// ("claude" or "codex"). Locked once the first message has been sent
	// (session_id != "") — see the conversation update handler. New
	// conversations default to the server's global CLIType.
	Provider string `json:"provider"`
	// Model is the CLI model id passed via --model on every run. Empty is
	// treated as "use the CLI's own default"; the create path populates it
	// with service.LatestModel(provider) so the UI always has a value to
	// render and the runner always has a concrete id to ship.
	Model string `json:"model"`
	// ThinkLevel is the reasoning effort selected for this conversation. Empty
	// means the provider request omits an effort override and uses the CLI/model
	// default. Unlike the account pin, it remains mutable after the first turn.
	ThinkLevel string `json:"think_level,omitempty"`
	// AccountName pins this conversation to a specific provider account
	// within its CLI type. Empty means "use the user's default account for
	// this type" — which is how every pre-multi-account row behaves, so the
	// dispatcher stays backward-compatible. When non-empty the dispatcher
	// re-validates the name against the user's currently-allowed account set
	// before use, so revoking a binding takes effect even on conversations
	// that already pinned it.
	AccountName          string `json:"account_name"`
	WorkDir              string `json:"work_dir"`
	SessionID            string `json:"session_id"`
	NotificationsEnabled bool   `json:"notifications_enabled"`
	// Pinned conversations float to the top of the sidebar (still sorted
	// by updated_at DESC within the pinned/unpinned partitions). Per-row
	// boolean so the UI toggle is a plain PUT instead of a separate
	// "favourites" table.
	Pinned bool `json:"pinned"`
	// PinOrder is the manual sort position within the pinned block (ascending;
	// smaller = higher). Only meaningful while Pinned is true — the sidebar
	// sorts `pinned DESC, pin_order ASC, updated_at DESC`. Set by drag-to-reorder
	// (ReorderPinnedConversations) and by the pin toggle (new pins go to the top).
	PinOrder int `json:"pin_order"`
	// ShareToken is non-empty once the owner has explicitly shared the
	// conversation. Public read-only links resolve by this token rather than
	// the conversation id.
	ShareToken string `json:"-"`
	// Shared is derived from ShareToken and exposed so clients can mark rows
	// that already have a public read-only link without seeing the token.
	Shared bool `json:"shared"`
	// LastContextUsage stores the most recent context_usage payload (raw
	// JSON: {used,total,input_tokens,cache_read,cache_creation}) so a fresh
	// browser, a server restart, or a tab whose localStorage was cleared can
	// still display the token bar without waiting for the next claude reply.
	// Empty when no usage event has ever been seen for this conversation.
	LastContextUsage string `json:"last_context_usage"`
	// SourceType distinguishes an interactive/manual conversation from one
	// created by the cron scheduler. CronJobID identifies the schedule that
	// produced it. Both are durable so usage events remain attributable even
	// after the scheduler has moved on to a later run.
	SourceType string    `json:"source_type,omitempty"`
	CronJobID  string    `json:"cron_job_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Conversation attention states. The empty string means nothing is waiting
// for the user.
const (
	AttentionDone    = "done"
	AttentionError   = "error"
	AttentionWaiting = "waiting"
)

// ConversationAttention is one conversation that wants its owner back: the
// latest turn finished or failed unseen, or it is parked on a question.
type ConversationAttention struct {
	ConversationID string    `json:"conversation_id"`
	AgentID        string    `json:"agent_id"`
	Title          string    `json:"title"`
	State          string    `json:"state"`
	At             time.Time `json:"at"`
}

// QueueStatusPoolQueued marks an IM-originated prompt that is waiting for an
// account slot. It is deliberately NOT "pending": every dispatcher query
// filters on 'pending'/'processing' exactly, so an IM prompt carrying this
// value is invisible to the web prompt queue — PeekNextPendingPrompt will not
// claim and re-run it, DeletePendingPrompt(s) will not delete the row out from
// under the IM thread, and ResetProcessingToPending will not resurrect it.
// What it does buy is a durable "still queued" fact the chat UI can read back
// over REST, which live-only queue_status frames cannot give a tab that opens
// after the message arrived.
const QueueStatusPoolQueued = "pool_queued"

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	CreatedAt      time.Time `json:"created_at"`
	// QueueStatus is non-empty for user prompts that haven't yet finished
	// the dispatcher pipeline. Values: "pending" (queued, no worker has
	// picked it up), "processing" (a worker is actively running this
	// prompt through claude), QueueStatusPoolQueued (an IM turn waiting for
	// an account slot). Empty for completed history messages.
	// Surfaced to the frontend so the chat UI can subtly mark un-processed
	// turns; the dispatcher uses it as the source of truth for FIFO claim
	// + cancel-clears-queue behaviour.
	QueueStatus string `json:"queue_status,omitempty"`
	// Metadata is per-turn JSON. Assistant rows carry usage/model details;
	// user rows may carry uploaded-image metadata, and tool rows may carry
	// duration_ms so the chat UI can restore completed timers from history.
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// SourceID is the stable id of the upstream record a row came from.
	// The tty importer uses the JSONL line uuid plus a block suffix; IM
	// messages use their platform message id. Empty for ordinary web chat
	// rows. It powers the (conversation_id, source_id) unique index so
	// replaying an upstream record is idempotent. Write-only: the read/list
	// helpers don't SELECT it back, so it never reaches the API.
	SourceID string `json:"-"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	content := any(m.Content)
	if m.Role == "tool" {
		content = ToolContentForWire(m.Content)
	}
	type messageJSON struct {
		ID             string          `json:"id"`
		ConversationID string          `json:"conversation_id"`
		Role           string          `json:"role"`
		Content        any             `json:"content"`
		CreatedAt      time.Time       `json:"created_at"`
		QueueStatus    string          `json:"queue_status,omitempty"`
		Metadata       json.RawMessage `json:"metadata,omitempty"`
	}
	return json.Marshal(messageJSON{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		Role:           m.Role,
		Content:        content,
		CreatedAt:      m.CreatedAt,
		QueueStatus:    m.QueueStatus,
		Metadata:       m.Metadata,
	})
}

// DBOptimizeResult reports the on-disk size of the database file before and
// after a maintenance pass, plus how many expired login sessions were
// reaped during the pass. Sizes are zero for in-memory backends.
type DBOptimizeResult struct {
	BeforeBytes            int64 `json:"before_bytes"`
	AfterBytes             int64 `json:"after_bytes"`
	ExpiredSessionsDeleted int64 `json:"expired_sessions_deleted"`
	PurgedConversations    int64 `json:"purged_conversations"`
	PurgedUsers            int64 `json:"purged_users"`
	PurgedAgents           int64 `json:"purged_agents"`
}
