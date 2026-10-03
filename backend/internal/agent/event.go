// Package agent defines the LLM backend abstraction: a single Backend
// interface that every model adapter (claude CLI, codex CLI, future API
// adapters) implements, plus the neutral types those adapters speak in
// — RunRequest, StreamEvent, Capabilities, SessionState.
//
// Why this package exists: handlers no longer import a CLI-specific
// runner; they pick a Backend via the registry and pump events into the
// WebSocket. Adapter-specific concerns (argv shape, env vars, on-disk
// session files, stream-json envelope) live entirely under the
// adapter's own subpackage (agent/claudecli, agent/codexcli, ...).
package agent

// StreamEvent is one decoded frame from an adapter's streaming output.
// The set of Kind values is fixed in this package so every adapter
// emits the same alphabet and handlers stay backend-agnostic. Adapters
// that lack a given capability (e.g. codex has no rate-limit frame)
// simply never emit that Kind.
type StreamEvent struct {
	Kind    string // see Kind constants below
	Content string
	// TurnID identifies the user message whose provider turn produced this
	// event when the adapter exposes that correlation. It is intentionally
	// optional: older providers and non-terminal frames may not carry it.
	TurnID string
	// TurnIDs lists every user message this turn consumed, in consumption
	// order, when the adapter exposes it. A message steered into a turn the
	// model is still working on is folded into that turn rather than run as
	// its own, so the single terminal result names the original prompt in
	// TurnID and only mentions the folded message here. Always contains
	// TurnID when present; empty for adapters and provider versions that do
	// not report the list, which is why TurnID remains the fallback.
	TurnIDs []string
	// TurnOrigin identifies provider-initiated turns (for example,
	// "task-notification") so a background completion cannot be mistaken for
	// the terminal result of a user-prompted turn.
	TurnOrigin string
	// Subagent is true when the event originated from a sub-agent
	// invocation (the upstream stream-json line carried a non-empty
	// parent_tool_use_id). Consumers use this to keep sub-agent output
	// visually separate, skip persisting it onto the parent
	// conversation, and avoid letting a sub-agent's system_init /
	// context usage frames overwrite the parent's header state.
	Subagent bool
	// BlockIndex carries the upstream content_block index for delta /
	// thinking_delta events so the stream processor can detect
	// transitions between text content blocks and inject a paragraph
	// separator — without this, a turn that emits two text blocks
	// (e.g. prose then a list of "### N." sections) is concatenated
	// as "...prose### Heading" on the wire and markdown can no
	// longer recognise the heading mid-line.
	BlockIndex int
	// ModelCall marks a regular event (normally Claude's synthesized
	// context_usage at message_start) as the start of one real model request.
	// It is process-local metadata and is not serialized to clients.
	ModelCall bool
	// Usage is the typed payload of a KindUsage event; Content holds the same
	// report as JSON. Build both with NewUsageEvent and read with UsageOf.
	// Process-local like ModelCall: recordings keep Content, and agenttest
	// carries the one field the JSON cannot (UsageReport.Cumulative).
	Usage *UsageReport `json:"-"`
}

// StreamEvent Kind constants.
const (
	KindDelta          = "delta"            // partial text from assistant
	KindResult         = "result"           // final assistant text
	KindContextUsage   = "context_usage"    // {used, total} token counts
	KindSystemInit     = "system_init"      // {model, session_id, tools}
	KindToolUseStart   = "tool_use_start"   // {name, id}
	KindToolInputDelta = "tool_input_delta" // partial JSON for tool input
	KindToolResult     = "tool_result"      // tool execution result from "user" type message
	KindUsage          = "usage"            // detailed token usage + cost
	// KindModelCall marks the closest observable boundary when a provider has
	// begun one model response. It is internal orchestration data:
	// service guardrails consume it, but it is never rendered or persisted.
	KindModelCall     = "model_call"
	KindThinkingDelta = "thinking_delta" // extended thinking content
	// KindSessionInfo describes a provider session transition that is useful
	// while diagnosing continuity (fresh thread or successful resume). It is
	// intentionally ephemeral so healthy turns do not grow the transcript.
	KindSessionInfo = "session_info"
	// KindSessionWarning reports a recovered continuity failure. The turn may
	// continue on a fresh provider thread, but consumers must persist and show
	// the warning because earlier chat is no longer in the model context.
	KindSessionWarning = "session_warning"
	// KindUserQuestion carries a normalized UserQuestionRequest JSON payload.
	// It is a control event, not transcript content, and is never persisted.
	KindUserQuestion = "user_question"
	// KindUserQuestionResolved retracts a provider-side question that was
	// auto-resolved before the browser supplied an answer.
	KindUserQuestionResolved = "user_question_resolved"
	// KindRateLimit is emitted when stream-json carries a
	// rate_limit_event line. The payload is normalised to snake_case
	// JSON of the form {type, status, resets_at[, overage_status,
	// is_using_overage]}, where type is "five_hour" or "seven_day".
	// The CLI only emits these events at status transitions
	// (allowed → warning → blocked), so they are sparse — consumers
	// should treat the most recently seen frame per window as the
	// current state.
	KindRateLimit = "rate_limit"
	// KindBackgroundTasks carries the session's complete current set of
	// background tasks as {"tasks":[{task_id, task_type, description}]}.
	//
	// It is a LEVEL, not an edge: every frame replaces the consumer's set
	// wholesale, and an empty array means "no background work is running".
	// Pairing start/stop edges instead would let one missed frame wedge a
	// stale "still running" state forever. Nothing is emitted at process
	// start, so a consumer's set correctly begins empty — but that also
	// means the set is only meaningful for the lifetime of one provider
	// process and must be dropped when that process is replaced.
	KindBackgroundTasks = "background_tasks"
	// KindTaskNotification carries one settled background task's
	// user-facing summary as {task_id, status, summary}. Unlike
	// KindBackgroundTasks it is an edge, and it is a display cue rather
	// than transcript content — never persisted.
	KindTaskNotification = "task_notification"
	// KindLiveUsage is emitted when stream-json sends an Anthropic
	// message_start event whose message.usage is already populated.
	// It carries the input_tokens / cache_read / cache_creation totals
	// for the current turn but cannot determine the context-window
	// total on its own — that's filled in by StreamProcessor using
	// state from the most recent KindContextUsage (or a model-name
	// fallback). End consumers (handler/UI) only see the synthesized
	// KindContextUsage.
	KindLiveUsage = "live_usage"
	// KindError carries a runtime error message emitted by the CLI
	// mid-stream (codex's top-level "error" + "turn.failed" events;
	// the legacy envelope "error" message). The codex runner
	// intercepts these so the final wait error reflects the
	// model-side reason ("usage limit hit", "model not supported",
	// ...) instead of the generic "exit status 1: Reading prompt from
	// stdin..." that ended up in the chat UI when the real error was
	// buried in stdout JSON and stderr only held the startup banner.
	// Not forwarded to handlers — consumed in-runner.
	KindError = "error"
)

// UserQuestionRequest is the provider-neutral form rendered by the browser.
// RequestID is opaque and only valid for the in-flight turn that emitted it.
type UserQuestionRequest struct {
	RequestID        string         `json:"request_id"`
	Questions        []UserQuestion `json:"questions"`
	AutoResolutionMS *int64         `json:"auto_resolution_ms,omitempty"`
}

type UserQuestion struct {
	ID          string               `json:"id"`
	Header      string               `json:"header"`
	Question    string               `json:"question"`
	MultiSelect bool                 `json:"multi_select,omitempty"`
	AllowOther  bool                 `json:"allow_other,omitempty"`
	Secret      bool                 `json:"secret,omitempty"`
	Options     []UserQuestionOption `json:"options,omitempty"`
}

type UserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
