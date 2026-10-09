package agent

import (
	"context"
	"errors"
	"time"
)

// ErrNoActiveTurn means an adapter had no in-flight turn that could accept
// steering. Callers may safely fall back to starting or queueing a new turn.
var ErrNoActiveTurn = errors.New("no active turn")

// ErrNoActiveQuestion means the referenced interactive question is no longer
// waiting for an answer (the turn completed, was cancelled, or already
// accepted a response).
var ErrNoActiveQuestion = errors.New("no active user question")

// Backend is the unified interface every LLM adapter implements. One
// conversation is bound to one Backend for its lifetime (DB column
// `provider`); the registry picks the right Backend per conversation.
//
// Adapters are stateless. All per-conversation state travels through
// RunRequest.SessionID (and SessionExists/SessionLogPath probes).
type Backend interface {
	// Name returns a short, stable identifier ("claude-cli",
	// "codex-cli"). Used for logging and for the registry round-trip.
	Name() string

	// Capabilities reports which optional features this adapter
	// supports. Handlers use this to gate UI affordances (e.g. /compact
	// is hidden for adapters that report SupportsCompaction=false) so
	// no caller has to hard-code "if provider == codex" again.
	Capabilities() Capabilities

	// RunWithSession spawns one turn of the model. The adapter streams
	// events through outputCh and closes the channel before returning.
	// A non-nil error means the run failed; whatever was emitted up to
	// that point still went through outputCh.
	RunWithSession(ctx context.Context, prompt, workDir string, req RunRequest, outputCh chan<- StreamEvent) error

	// RunOneshot collapses RunWithSession into a single string by
	// concatenating KindResult / KindDelta frames. Used for internal
	// side requests (title generation, /compact summary) that don't
	// stream to a user.
	RunOneshot(ctx context.Context, prompt, workDir string, req RunRequest) (string, error)

	// SessionExists reports whether the adapter has an on-disk
	// session log for (workDir, sessionID, configDir). When true, the
	// next RunWithSession must treat this as a resume — passing
	// --session-id again will fail with "Session ID is already in
	// use" for adapters that lock on the id.
	//
	// configDir must match what the next run will export
	// (CLAUDE_CONFIG_DIR / CODEX_HOME / ...) — otherwise the probe
	// looks at the wrong account's session tree.
	SessionExists(workDir, sessionID, configDir string) bool

	// SessionLogPath returns the deterministic on-disk path the adapter does
	// (or would) write its session log to. The transient retry loop snapshots
	// this file so it can erase a failed attempt before replaying the prompt.
	// Explicit cancellation keeps the log intact so the next message in the
	// same conversation inherits the stopped turn's context. Returns "" when
	// the adapter cannot predict the path (e.g. codex before its first line
	// lands).
	SessionLogPath(workDir, sessionID, configDir string) string
}

// TurnSteeringBackend is implemented by adapters that can append user input
// to an already-running turn. controlID is a caller-owned routing key (DayMug
// uses the conversation id); messageID is forwarded as the provider's client
// message id when supported so retries and diagnostics can correlate it.
type TurnSteeringBackend interface {
	SteerTurn(ctx context.Context, controlID, messageID, input string) error
}

// UserQuestionBackend is implemented by adapters whose live provider session
// can pause for structured user input and resume after DayMug returns it.
type UserQuestionBackend interface {
	AnswerUserQuestion(ctx context.Context, controlID, requestID string, answers map[string][]string) error
}

// Capabilities is the adapter-self-reported feature matrix. Booleans
// only — keep this comparable at a glance.
type Capabilities struct {
	// SupportsCompaction means the /compact one-shot summary flow works
	// against this adapter: run a summary prompt in the current backend
	// session, persist the summary as an assistant message, then rotate
	// the conversation's stored session id so the next turn starts fresh.
	SupportsCompaction bool

	// SupportsThinkingStream means KindThinkingDelta frames are
	// expected during a normal turn. The UI uses this to decide
	// whether to render a thinking pane.
	SupportsThinkingStream bool

	// SupportsRateLimitEvents means KindRateLimit frames carry usable
	// quota state. Only Anthropic's stream-json provides this today.
	SupportsRateLimitEvents bool

	// ReportsContextUsage means KindContextUsage frames carry a real
	// per-turn context occupancy against the model's real window, so the
	// UI may render a context bar from them.
	//
	// codexcli sets this false: its `token_count` envelope no longer
	// carries model_context_window (codex-cli moved it onto
	// `task_started`), so the adapter emits no context_usage at all and
	// the bar would only ever replay whatever stale row is already in
	// conversations.last_context_usage — in practice the pinned-at-100%
	// values written by an older estimating build.
	ReportsContextUsage bool

	// ReportsCostUSD means KindUsage frames carry a cost_usd field.
	// When false the adapter is expected to fill in cost from a local
	// price table; today no adapter promises this on its own — the
	// flag is kept here so a future API-backed adapter that does have
	// to compute cost externally has a clear extension point.
	ReportsCostUSD bool

	// AssignsSessionID means the backend picks the session id of a new
	// session itself and reports it in its system_init frame (codex threads),
	// so callers must not mint one: a new session starts with an empty
	// RunRequest.SessionID. When false the caller chooses the id (claude's
	// --session-id) and keeps it for the resume that follows.
	AssignsSessionID bool
}

// RunRequest is the neutral input contract for one turn. Adapters
// silently ignore fields they don't support (the Backend.Capabilities
// matrix says which ones).
// Residency is the contract for keeping a provider process alive between
// turns, split so that neither side has to know the other's business.
//
// The adapter knows whether a turn left background work running — it is the
// only thing reading the provider's stream. The caller knows whether the
// machine can afford another resident process, and where an unprompted turn
// would even go. Neither fact is derivable from the other, which is why this
// is a capability the caller grants rather than a decision the adapter makes.
type Residency struct {
	// Permit is consulted at the turn boundary, and only once the adapter
	// has evidence of live background work. ok=false means "close now",
	// which is not an error — it is the caller declining to spend ~1 GB.
	// release is called exactly once, when the process is finally gone.
	Permit func() (release func(), ok bool)
	// Parked is called after a turn successfully hands its process back to
	// the resident pool. Unlike Permit it fires for every reuse, allowing the
	// caller to mark this particular turn resident without acquiring another
	// process slot for the same process.
	Parked func()
	// Activity reports whether this resident conversation still has background
	// work (or an unprompted wakeup turn) in progress. It is independent from
	// the foreground turn boundary: a turn may be ready for more user input
	// while its background work continues. Adapters only call it on state
	// transitions and must balance every true with a later false.
	Activity func(active bool)
	// Wake asks for somewhere to put a turn nobody prompted: the provider
	// started it on its own because background work settled. It returns the
	// channel to stream into and a callback for that turn's terminal error.
	// A nil channel refuses the turn and retires the process. Wake must not
	// block — it is called from the goroutine draining the provider's stdout.
	Wake func() (chan<- StreamEvent, func(error))
	// Notice reports work that was stopped rather than kept, so the user
	// learns about it instead of watching a long-running job vanish. The
	// adapter cannot persist a chat row; the caller supplies that surface.
	Notice func(reason string)
}

type RunRequest struct {
	// ControlID lets an adapter expose controls for this particular in-flight
	// run without coupling the provider-neutral agent package to conversations.
	// Empty disables external turn control (oneshots and internal side runs).
	ControlID string
	// EnableUserQuestions allows the adapter to pause the live turn for a
	// structured browser response. It is deliberately opt-in because shared
	// stream consumers such as IM bridges have no matching response surface.
	EnableUserQuestions bool
	// Residency authorises the adapter to keep its provider process alive
	// past the end of this turn. Nil — the zero value, and what every side
	// run gets — means the adapter must tear down at the turn boundary
	// exactly as it always has. See the Residency doc for the split of
	// responsibilities.
	Residency *Residency

	// SessionID is the per-conversation session token. Empty starts
	// a fresh session (the adapter assigns one if it picks its own
	// ids). IsResume is set by callers that already saw a session
	// log on disk for (workDir, sessionID).
	SessionID string
	IsResume  bool

	// Model is the CLI model id (e.g. "claude-opus-4-8", "gpt-5.5").
	// Empty falls back to the CLI's own default.
	Model string
	// ContextWindow is the admin-declared window of Model in tokens, for
	// endpoints the CLI can't size on its own. Zero leaves it to the CLI.
	ContextWindow int
	// ThinkLevel controls how much reasoning the model should spend on a turn.
	// Empty leaves the setting out of the provider request so the CLI/model uses
	// its own default. The shared values are low, medium, high, and max; adapters
	// translate max to their strongest level.
	ThinkLevel string

	// SystemPrompt is appended to the adapter's own system message.
	// Adapters that have no "append" hook (codex) write it to the
	// conversation's AGENTS.md instead — that's an adapter concern.
	SystemPrompt string

	// ReadOnly asks the adapter to run with no write/exec/network side
	// effects — a "pure conversation" turn. Claude honours it via a
	// disallowed-tools list the caller passes; codex switches its sandbox
	// from the default full-access bypass to `--sandbox read-only`. Used by
	// the admin account check's probe turn. Adapters that can't enforce
	// it (e.g. codex on a resume, where --sandbox is rejected) fall back to
	// their normal flags and rely on an isolated cwd instead.
	ReadOnly bool

	// McpConfigPath is the temp file the claude CLI consumes via
	// --mcp-config. Codex reads MCP from $CODEX_HOME/config.toml so
	// this field is unused there.
	McpConfigPath string

	// ConfigDir maps to CLAUDE_CONFIG_DIR (Claude family) or CODEX_HOME
	// (codex) — i.e. the account-scoped on-disk credentials/config
	// directory. Empty inherits the process default (~/.claude or ~/.codex).
	ConfigDir string

	// AccountEnv layers extra env vars on top of the adapter's child
	// env (operator-supplied per-account secrets like
	// ANTHROPIC_API_KEY / OPENAI_API_KEY).
	AccountEnv map[string]string

	// Sandbox optionally wraps the spawned argv with an isolation
	// layer. nil means exec the binary directly.
	Sandbox Sandbox
	// ExtraBinds is forwarded to Sandbox.Wrap: per-call bind mounts for
	// directories the run needs from outside its JailRoot. Ignored by the
	// noop sandbox.
	ExtraBinds []BindMount
	// Unrestricted bypasses the isolating sandbox for this invocation — set
	// for users whose sandbox_mode is "unrestricted" (admins / trusted
	// operators). Ignored by the noop sandbox.
	Unrestricted bool
	// JailRoot is the only directory a jailed invocation may read/write
	// (normally the conversation work_dir). Forwarded to Sandbox.Wrap.
	JailRoot string
	// Spawner overrides how the child process is started. nil falls
	// back to PipeSpawner.
	Spawner ProcessSpawner

	// StallTimeout is the silence window between stdout bytes before
	// the stream watchdog checks whether the process tree is still
	// making progress. Zero lets the adapter choose its default.
	StallTimeout time.Duration
	// MaxSilentTimeout is an absolute cap on a single stdout-silent
	// period. Activity probes may extend StallTimeout repeatedly, but
	// never beyond this duration. Zero disables the absolute cap.
	MaxSilentTimeout time.Duration
}
