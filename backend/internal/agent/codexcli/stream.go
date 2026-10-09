package codexcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcommon"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// --- JSON wire types for codex CLI's `exec --json` stream ---
//
// Older codex builds emitted newline-delimited JSON in the shape:
//
//	{"id": "<uuid>", "msg": {"type": "<event_type>", ...payload}}
//
// Current builds emit top-level events:
//
//	{"type":"thread.started","thread_id":"..."}
//	{"type":"item.completed","item":{"type":"agent_message","text":"..."}}
//	{"type":"turn.completed","usage":{...}}
//
// We only parse the fields we forward; unknown fields and unknown event
// types are silently dropped. This keeps the parser robust across codex
// CLI minor versions — new event types won't crash daymug, they'll just
// not surface in the UI until we map them.

type codexEnvelope struct {
	ID       string           `json:"id"`
	Msg      codexEventMsg    `json:"msg"`
	Type     string           `json:"type,omitempty"`
	ThreadID string           `json:"thread_id,omitempty"`
	Item     *codexStreamItem `json:"item,omitempty"`
	Usage    *codexTokenUsage `json:"usage,omitempty"`
	// Message is the human-readable payload of a top-level "error" event
	// (e.g. {"type":"error","message":"You've hit your usage limit..."}).
	Message string `json:"message,omitempty"`
	// Error is the nested payload of a "turn.failed" event
	// ({"type":"turn.failed","error":{"message":"..."}}). Both shapes
	// carry the same string for the cases codex emits today, but they're
	// kept distinct so a future codex build adding fields to one envelope
	// doesn't get parsed into the wrong slot.
	Error *codexTopLevelError `json:"error,omitempty"`
}

// codexTopLevelError mirrors the {"error": {"message": "..."}} payload
// codex packs into its "turn.failed" event. Pointer-typed on the
// envelope so the zero value distinguishes "no error field" from "error
// field with empty message".
type codexTopLevelError struct {
	Message string `json:"message,omitempty"`
}

type codexEventMsg struct {
	Type string `json:"type"`

	// session_configured
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`

	// agent_message / agent_message_delta
	Message string `json:"message,omitempty"`
	Delta   string `json:"delta,omitempty"`

	// agent_reasoning / agent_reasoning_delta
	Text string `json:"text,omitempty"`

	// exec_command_begin / exec_command_end
	CallID   string   `json:"call_id,omitempty"`
	Command  []string `json:"command,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
	ExitCode *int     `json:"exit_code,omitempty"`

	// exec_command_output_delta
	Stream string `json:"stream,omitempty"`
	Chunk  string `json:"chunk,omitempty"`

	// task_complete
	LastAgentMessage string `json:"last_agent_message,omitempty"`

	// token_count
	Info *codexTokenInfo `json:"info,omitempty"`

	// error
	Error string `json:"error,omitempty"`
}

// codexTokenInfo mirrors the subset of codex's token_count event we care
// about. The field names match codex-rs's serde-tagged output so the JSON
// passthrough into agent.KindUsage stays predictable for any future per-model
// accounting code that consumes it.
type codexTokenInfo struct {
	TotalTokenUsage    *codexTokenUsage `json:"total_token_usage,omitempty"`
	LastTokenUsage     *codexTokenUsage `json:"last_token_usage,omitempty"`
	Model              string           `json:"model,omitempty"`
	ModelContextWindow int              `json:"model_context_window,omitempty"`
}

type codexTokenUsage struct {
	InputTokens         int `json:"input_tokens"`
	CachedInputTokens   int `json:"cached_input_tokens,omitempty"`
	OutputTokens        int `json:"output_tokens"`
	ReasoningOutputToks int `json:"reasoning_output_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
}

// codexStreamItem is the union of the `codex exec --json` ThreadItem variants
// this adapter understands (codex-rs exec_events.rs, snake_case on the wire).
// Unknown variants such as todo_list decode into it harmlessly and map to no
// event.
type codexStreamItem struct {
	ID               string `json:"id,omitempty"`
	Type             string `json:"type,omitempty"`
	Text             string `json:"text,omitempty"`
	Message          string `json:"message,omitempty"`
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	Status           string `json:"status,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	// file_change
	Changes []codexFileChange `json:"changes,omitempty"`
	// mcp_tool_call
	Server    string          `json:"server,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
	// web_search
	Query string `json:"query,omitempty"`
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// ParseCodexLine decodes one envelope and maps it to the same agent.StreamEvent
// Kind constants the claude parser produces. The mapping is deliberately
// lossy where the two CLIs don't line up — better to fold a codex-only
// concept into the closest claude analogue than to introduce a new Kind
// the frontend doesn't understand.
//
// Mapping summary:
//
//	session_configured        → agent.KindSystemInit (carries session_id + model)
//	agent_message_delta       → agent.KindDelta
//	agent_message             → agent.KindResult           (codex emits one per turn)
//	agent_reasoning_delta     → agent.KindThinkingDelta
//	exec_command_begin        → agent.KindToolUseStart     (call_id, "Bash" surrogate)
//	exec_command_output_delta → agent.KindToolInputDelta   (live stdout/stderr chunks)
//	exec_command_end          → agent.KindToolResult       (exit code + tail of output)
//	task_complete             → agent.KindResult           (last_agent_message fallback)
//	token_count               → agent.KindUsage
//
// Top-level "error" and "turn.failed" events are surfaced as agent.KindError —
// the codex runner intercepts these so its final wait error carries the
// model-side reason (rate limit, model not supported, ...) instead of
// the generic "exit status 1" decorated with codex's startup banner that
// stderr leaked. Background_event and other informational types are
// dropped.
func ParseCodexLine(line []byte) []agent.StreamEvent {
	var env codexEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil
	}

	if env.Msg.Type == "" && env.Type != "" {
		return parseCodexTopLevel(env)
	}

	switch env.Msg.Type {
	case "session_configured":
		return parseCodexSessionConfigured(env.Msg)
	case "agent_message_delta":
		if env.Msg.Delta == "" {
			return nil
		}
		return []agent.StreamEvent{{Kind: agent.KindDelta, Content: env.Msg.Delta}}
	case "agent_message":
		// Codex emits agent_message at the end of every assistant turn.
		// We surface it as agent.KindResult so the existing UI persistence
		// path (which prefers agent.KindResult over delta concatenation)
		// captures the canonical final text.
		return []agent.StreamEvent{{Kind: agent.KindResult, Content: env.Msg.Message}}
	case "agent_reasoning_delta":
		if env.Msg.Delta == "" {
			return nil
		}
		return []agent.StreamEvent{{Kind: agent.KindThinkingDelta, Content: env.Msg.Delta}}
	case "agent_reasoning":
		// The matching "complete reasoning" event arrives after the deltas
		// have already streamed; suppressing it avoids doubling up the
		// thinking buffer in the UI.
		return nil
	case "exec_command_begin":
		return parseCodexExecBegin(env.Msg)
	case "exec_command_output_delta":
		return parseCodexExecOutputDelta(env.Msg)
	case "exec_command_end":
		return parseCodexExecEnd(env.Msg)
	case "task_complete":
		// Some codex flows omit a trailing agent_message (e.g. when the
		// model produced only tool calls). Emitting a final agent.KindResult
		// from task_complete.last_agent_message means the persistence
		// path still has something to save in those cases.
		if strings.TrimSpace(env.Msg.LastAgentMessage) == "" {
			return []agent.StreamEvent{{Kind: agent.KindResult}}
		}
		return []agent.StreamEvent{{Kind: agent.KindResult, Content: env.Msg.LastAgentMessage}}
	case "token_count":
		return parseCodexTokenCount(env.Msg)
	case "error":
		// Legacy envelope shape: {"id":..., "msg":{"type":"error","error":"<msg>"}}.
		// Modern builds emit the top-level error variant handled in
		// parseCodexTopLevel; keep this branch so older codex CLIs still
		// surface their runtime errors through the same agent.KindError seam.
		if env.Msg.Error == "" {
			return nil
		}
		return []agent.StreamEvent{{Kind: agent.KindError, Content: env.Msg.Error}}
	}
	return nil
}

func parseCodexTopLevel(env codexEnvelope) []agent.StreamEvent {
	switch env.Type {
	case "thread.started":
		return parseCodexThreadStarted(env)
	case "item.started":
		return parseCodexItemStarted(env.Item)
	case "item.completed":
		return parseCodexItemCompleted(env.Item)
	case "turn.completed":
		if env.Usage == nil {
			return nil
		}
		return codexcommon.UsageEvents(config.CLITypeCodex, "", env.Usage.common(), 0)
	case "error":
		// Top-level error event ({"type":"error","message":"..."}).
		// Emitted by current codex builds for cases like usage-limit
		// exhaustion or "model X not supported with this account".
		if env.Message == "" {
			return nil
		}
		return []agent.StreamEvent{{Kind: agent.KindError, Content: env.Message}}
	case "turn.failed":
		// Same string as the preceding "error" event in the wild, but
		// emitted separately so callers can distinguish "fatal for this
		// turn" from "informational mid-stream". We surface both so the
		// runner picks up whichever arrives last; rebroadcasting the
		// same text twice is fine — the consumer in the runner stashes
		// the most-recent message and discards the previous one.
		if env.Error == nil || env.Error.Message == "" {
			return nil
		}
		return []agent.StreamEvent{{Kind: agent.KindError, Content: env.Error.Message}}
	}
	return nil
}

func parseCodexThreadStarted(env codexEnvelope) []agent.StreamEvent {
	if env.ThreadID == "" {
		return nil
	}
	info := map[string]any{
		"session_id": env.ThreadID,
	}
	return streamcommon.Event(agent.KindSystemInit, info)
}

func parseCodexItemStarted(item *codexStreamItem) []agent.StreamEvent {
	if item == nil {
		return nil
	}
	return codexcommon.ItemEvents(item.common(), true)
}

func parseCodexItemCompleted(item *codexStreamItem) []agent.StreamEvent {
	if item == nil {
		return nil
	}
	return codexcommon.ItemEvents(item.common(), false)
}

// execItemKinds is the exec transport's snake_case spelling of each variant.
// agent_reasoning is an older build's name for reasoning.
var execItemKinds = map[string]codexcommon.ItemKind{
	"agent_message":     codexcommon.ItemAgentMessage,
	"reasoning":         codexcommon.ItemReasoning,
	"agent_reasoning":   codexcommon.ItemReasoning,
	"command_execution": codexcommon.ItemCommand,
	"file_change":       codexcommon.ItemFileChange,
	"mcp_tool_call":     codexcommon.ItemMcpToolCall,
	"web_search":        codexcommon.ItemWebSearch,
}

func (item *codexStreamItem) common() codexcommon.Item {
	out := codexcommon.Item{
		Kind: execItemKinds[item.Type], ID: item.ID, Status: item.Status, Text: item.Text,
		Command: item.Command, AggregatedOutput: item.AggregatedOutput, ExitCode: item.ExitCode,
		Server: item.Server, Tool: item.Tool, Arguments: item.Arguments, Result: item.Result,
		Query: item.Query,
	}
	if item.Error != nil {
		out.ErrorMessage = item.Error.Message
	}
	for _, change := range item.Changes {
		out.Changes = append(out.Changes, codexcommon.FileChange{Path: change.Path, Kind: change.Kind})
	}
	return out
}

func parseCodexSessionConfigured(msg codexEventMsg) []agent.StreamEvent {
	info := map[string]any{}
	if msg.Model != "" {
		info["model"] = msg.Model
	}
	if msg.SessionID != "" {
		info["session_id"] = msg.SessionID
	}
	return streamcommon.Event(agent.KindSystemInit, info)
}

func parseCodexExecBegin(msg codexEventMsg) []agent.StreamEvent {
	// Frontend's tool-use card expects a JSON content payload with at
	// least {"name", "id"}. We label every codex command "Bash" so the
	// same UI affordances (collapsing, copy) work without a codex-specific
	// rendering path. The full argv is shoved into "command" for any
	// future view that wants to display it; today it's informational.
	payload := map[string]any{
		"id":   msg.CallID,
		"name": "Bash",
	}
	if len(msg.Command) > 0 {
		payload["command"] = msg.Command
	}
	if msg.Cwd != "" {
		payload["cwd"] = msg.Cwd
	}
	return streamcommon.Event(agent.KindToolUseStart, payload)
}

func parseCodexExecOutputDelta(msg codexEventMsg) []agent.StreamEvent {
	// The frontend tool_input_delta channel is the closest analogue to
	// "live bytes from a running command" — claude reuses it for the
	// partial-JSON of Bash's input, codex reuses it for the partial
	// stdout/stderr of an exec.
	if msg.Chunk == "" {
		return nil
	}
	return []agent.StreamEvent{{Kind: agent.KindToolInputDelta, Content: msg.Chunk}}
}

func parseCodexExecEnd(msg codexEventMsg) []agent.StreamEvent {
	exit := 0
	if msg.ExitCode != nil {
		exit = *msg.ExitCode
	}
	body := strings.TrimSpace(msg.Stdout)
	if errPart := strings.TrimSpace(msg.Stderr); errPart != "" {
		if body != "" {
			body += "\n"
		}
		body += errPart
	}
	if body == "" {
		body = fmt.Sprintf("(exit %d)", exit)
	}
	payload := map[string]any{
		"id":        msg.CallID,
		"exit_code": exit,
		"content":   body,
	}
	return streamcommon.Event(agent.KindToolResult, payload)
}

func parseCodexTokenCount(msg codexEventMsg) []agent.StreamEvent {
	if msg.Info == nil {
		return nil
	}
	// Normalise to the same field names agent.KindUsage carries for claude so
	// the per-user usage rollup logic doesn't need to special-case the
	// codex envelope. We use last_token_usage when present (per-turn
	// granularity) and fall back to the running total otherwise.
	src := msg.Info.LastTokenUsage
	if src == nil {
		src = msg.Info.TotalTokenUsage
	}
	if src == nil {
		return nil
	}
	return codexcommon.UsageEvents(config.CLITypeCodex, "", src.common(), msg.Info.ModelContextWindow)
}

// common hands a decoded exec-stream figure to the shared translation. The
// model is deliberately not taken from the wire: codex sometimes reports the
// bare upstream id (e.g. "gpt-5" for DayMug's "gpt-5.5"), and the price table
// is keyed by DayMug's id, which StreamProcessor carries from the RunRequest.
func (u *codexTokenUsage) common() codexcommon.TokenUsage {
	return codexcommon.TokenUsage{
		InputTokens: u.InputTokens, CachedInputTokens: u.CachedInputTokens,
		OutputTokens: u.OutputTokens, ReasoningOutputTokens: u.ReasoningOutputToks,
		TotalTokens: u.TotalTokens,
	}
}

// StreamProcessor is the codex twin of StreamProcessor. Codex's
// stream is simpler than claude's — no nested content_block indices, no
// live-usage synthesis — so the processor is essentially a passthrough
// today. Carries the run's Model so agent.KindSystemInit / agent.KindUsage events
// can be enriched after parsing — codex's thread.started event omits
// the model name even though the runner knows it from agent.RunRequest, which
// is why the chat header rendered "Model: unknown" before this seam.
//
// prevTotalUsage is the most recent total_token_usage we've seen on a
// legacy "token_count" envelope. Older codex CLIs omit
// last_token_usage and only emit the running cumulative total; without
// this state every turn would surface as the cumulative figure
// (monotonically growing) and the chat header would show "this turn
// cost N tokens" with N already including every prior turn. Storing
// the previous total lets us derive a true per-turn delta. Modern CLIs
// that emit last_token_usage still update this field opportunistically
// so we have a usable baseline if the CLI silently drops the per-turn
// breakdown mid-conversation.
//
// sentReasoning / sentAssistant accumulate everything already streamed to the
// client so replayed frames can be de-duplicated. They are []byte rather than
// string because they are append-only and grow for the whole turn: a native
// `s += delta` reallocates and copies the entire accumulator on every one of
// the thousands of deltas in a long answer, which is O(n²) bytes of garbage —
// a 10 MB turn arriving in 1 KB deltas churns tens of GB and pushes the heap to
// several times the live set before the GC catches up. `append` amortises the
// growth instead. Everything that reads them keeps the accumulator itself
// un-copied (see hasPrefixBytes) so no read reintroduces the copy.
type StreamProcessor struct {
	Model                 string
	pendingResult         *agent.StreamEvent
	prevTotalUsage        *codexTokenUsage
	requiresPreviousUsage bool
	sentReasoning         []byte
	sentAssistant         []byte
}

// hasPrefixBytes reports whether s starts with prefix. It exists so the replay
// checks can compare an incoming frame (string, one frame's worth of bytes)
// against an accumulator ([]byte, a whole turn's worth) without materialising
// the accumulator as a string — strings.HasPrefix would need string(prefix),
// i.e. a full copy of the accumulator on every frame.
func hasPrefixBytes(s string, prefix []byte) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// NewStreamProcessor returns a fresh processor primed with the run's
// model name. Pass "" when the caller has no model in hand (e.g. legacy
// tests that don't go through RunWithSession).
func NewStreamProcessor(model string) *StreamProcessor {
	return &StreamProcessor{Model: model}
}

// resumeUsage seeds the cumulative total captured before a resumed codex
// process starts. Current codex builds report turn.completed.usage as the
// whole resumed thread's running total, so the processor needs the previous
// turn's total to recover the new turn's delta.
//
// A missing baseline keeps requiresPreviousUsage set. In that exceptional
// case cumulative-only usage is dropped instead of charging the whole thread
// again; answer streaming and persistence continue normally.
func (p *StreamProcessor) resumeUsage(previous *codexTokenUsage) {
	p.prevTotalUsage = previous
	p.requiresPreviousUsage = previous == nil
}

// codexBuiltinTools is the static tool list we advertise for every codex
// session. Codex has no MCP-style tool catalogue (claude's stream_json
// system_init enumerates "Read, Edit, Bash, ..."), only the built-in
// shell executor. The frontend's "Tools: N available" pill renders 0
// when this list is missing, which made every codex chat look broken.
var codexBuiltinTools = []string{"Bash"}

// Process parses one line of codex stream-json output and enriches any
// agent.KindSystemInit / agent.KindUsage events with state the upstream payload
// doesn't carry but the processor knows from agent.RunRequest — namely the
// run's model name and the static built-in tool list.
func (p *StreamProcessor) Process(line []byte) []agent.StreamEvent {
	completedReasoningItem := isCompletedReasoningItem(line)
	// Token usage envelopes need state to compute per-turn deltas from a
	// cumulative total. usageFromLine handles both the legacy token_count
	// envelope and current top-level turn.completed shape; ParseCodexLine
	// still owns every other event type and stays stateless.
	events, handled := p.usageFromLine(line)
	if !handled {
		events = ParseCodexLine(line)
	}
	out := make([]agent.StreamEvent, 0, len(events))
	for i := range events {
		switch events[i].Kind {
		case agent.KindSystemInit:
			events[i] = p.enrichSystemInit(events[i])
		case agent.KindThinkingDelta:
			evt, ok := p.normalizeThinkingDelta(events[i], completedReasoningItem)
			if !ok {
				continue
			}
			events[i] = evt
		}

		if events[i].Kind == agent.KindResult {
			evt := events[i]
			if delta, ok := p.assistantDeltaFor(evt.Content, evt.Subagent); ok {
				out = append(out, delta)
			}
			if evt.Content != "" {
				// Materialise a real copy: later deltas append into the
				// accumulator's array, which must not mutate an event that
				// has already been handed downstream.
				evt.Content = string(p.sentAssistant)
			}
			if p.pendingResult != nil {
				p.mergePendingResult(evt)
				continue
			}
			// Current codex emits turn.completed usage after the final
			// agent_message. Hold the result so downstream persistence sees
			// usage first and can attach it to result.metadata.
			p.pendingResult = &evt
			continue
		}
		if events[i].Kind == agent.KindDelta {
			p.sentAssistant = append(p.sentAssistant, events[i].Content...)
		}
		out = append(out, events[i])
		if events[i].Kind == agent.KindUsage && p.pendingResult != nil {
			out = append(out, *p.pendingResult)
			p.pendingResult = nil
		}
	}
	return out
}

// usageFromLine routes both generations of codex usage frames through the
// same stateful delta calculation. Current `codex exec resume --json` emits a
// cumulative turn.completed.usage even though the event name sounds
// turn-local; treating it as a delta repeatedly bills the session prefix.
func (p *StreamProcessor) usageFromLine(line []byte) ([]agent.StreamEvent, bool) {
	var env codexEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, false
	}
	if env.Type == "turn.completed" {
		if env.Usage == nil {
			return nil, true
		}
		if p.requiresPreviousUsage && p.prevTotalUsage == nil {
			p.prevTotalUsage = env.Usage
			p.requiresPreviousUsage = false
			return nil, true
		}
		perTurn := subtractCodexUsage(env.Usage, p.prevTotalUsage)
		p.prevTotalUsage = env.Usage
		return p.usageEvents(perTurn, 0), true
	}
	return p.tokenCountFromEnvelope(env)
}

func isCompletedReasoningItem(line []byte) bool {
	var env codexEnvelope
	if err := json.Unmarshal(line, &env); err != nil || env.Type != "item.completed" || env.Item == nil {
		return false
	}
	return env.Item.Type == "reasoning" || env.Item.Type == "agent_reasoning"
}

func (p *StreamProcessor) normalizeThinkingDelta(evt agent.StreamEvent, completedItem bool) (agent.StreamEvent, bool) {
	if evt.Content == "" {
		return evt, false
	}
	if len(p.sentReasoning) == 0 {
		p.sentReasoning = append(p.sentReasoning, evt.Content...)
		return evt, true
	}
	// One branch covers both the exact repeat (nothing left after trimming, so
	// the frame is dropped) and the legacy replay that carries the whole
	// summary so far plus a new tail.
	if hasPrefixBytes(evt.Content, p.sentReasoning) {
		evt.Content = evt.Content[len(p.sentReasoning):]
		if evt.Content == "" {
			return evt, false
		}
		p.sentReasoning = append(p.sentReasoning, evt.Content...)
		return evt, true
	}
	// Current Codex emits each completed reasoning summary as its own item.
	// Independent items do not carry a leading newline, so concatenating them
	// directly produces "...first**Second..." and breaks Markdown readability.
	// Legacy agent_reasoning_delta events are token chunks from one item and
	// must stay contiguous, hence the completed-item guard.
	if completedItem {
		switch {
		case bytes.HasSuffix(p.sentReasoning, []byte("\n\n")) || strings.HasPrefix(evt.Content, "\n\n"):
		case bytes.HasSuffix(p.sentReasoning, []byte("\n")) || strings.HasPrefix(evt.Content, "\n"):
			evt.Content = "\n" + evt.Content
		default:
			evt.Content = "\n\n" + evt.Content
		}
	}
	p.sentReasoning = append(p.sentReasoning, evt.Content...)
	return evt, true
}

// Flush returns a result event that was held waiting for a following
// turn.completed usage frame. Codex normally emits usage after the final
// assistant message; if a run exits without usage we still need to surface
// and persist the answer.
func (p *StreamProcessor) Flush() []agent.StreamEvent {
	if p == nil || p.pendingResult == nil {
		return nil
	}
	evt := *p.pendingResult
	p.pendingResult = nil
	return []agent.StreamEvent{evt}
}

func (p *StreamProcessor) assistantDeltaFor(content string, subagent bool) (agent.StreamEvent, bool) {
	if content == "" {
		return agent.StreamEvent{}, false
	}
	// Exact repeat and prefix replay share a branch: an exact repeat leaves an
	// empty tail after trimming what we already sent.
	if hasPrefixBytes(content, p.sentAssistant) {
		delta := content[len(p.sentAssistant):]
		if delta == "" {
			return agent.StreamEvent{}, false
		}
		p.sentAssistant = append(p.sentAssistant, delta...)
		return agent.StreamEvent{Kind: agent.KindDelta, Content: delta, Subagent: subagent}, true
	}
	if bytes.Contains(p.sentAssistant, []byte(content)) {
		return agent.StreamEvent{}, false
	}
	if overlap := trailingReplayPrefixLen(p.sentAssistant, content); overlap > 0 {
		delta := content[overlap:]
		if delta == "" {
			return agent.StreamEvent{}, false
		}
		p.sentAssistant = append(p.sentAssistant, delta...)
		return agent.StreamEvent{Kind: agent.KindDelta, Content: delta, Subagent: subagent}, true
	}
	delta := content
	if len(p.sentAssistant) > 0 {
		delta = "\n\n" + content
	}
	p.sentAssistant = append(p.sentAssistant, delta...)
	return agent.StreamEvent{Kind: agent.KindDelta, Content: delta, Subagent: subagent}, true
}

func trailingReplayPrefixLen(existing []byte, next string) int {
	for start := 0; start < len(existing); start++ {
		if !isStringBoundary(existing, start) {
			continue
		}
		if hasPrefixBytes(next, existing[start:]) {
			return len(existing) - start
		}
	}
	return 0
}

func isStringBoundary(s []byte, i int) bool {
	return i == 0 || i == len(s) || (i > 0 && i < len(s) && utf8.RuneStart(s[i]))
}

func (p *StreamProcessor) mergePendingResult(evt agent.StreamEvent) {
	if p.pendingResult == nil {
		p.pendingResult = &evt
		return
	}
	if evt.Content == "" || evt.Content == p.pendingResult.Content {
		return
	}
	if p.pendingResult.Content == "" {
		p.pendingResult.Content = evt.Content
		return
	}
	if strings.HasPrefix(evt.Content, p.pendingResult.Content) {
		p.pendingResult.Content = evt.Content
		return
	}
	if strings.Contains(p.pendingResult.Content, evt.Content) {
		return
	}
	p.pendingResult.Content += "\n\n" + evt.Content
}

// enrichSystemInit adds the model name and built-in tool list to a
// system_init payload when the upstream parser left them empty. We only
// fill missing fields so a legacy session_configured event that already
// carried the model wins over the run-level default.
func (p *StreamProcessor) enrichSystemInit(evt agent.StreamEvent) agent.StreamEvent {
	payload := map[string]any{}
	if evt.Content != "" {
		if err := json.Unmarshal([]byte(evt.Content), &payload); err != nil {
			return evt
		}
	}
	if _, ok := payload["model"]; !ok && p.Model != "" {
		payload["model"] = p.Model
	}
	if _, ok := payload["tools"]; !ok {
		payload["tools"] = codexBuiltinTools
	}
	return streamcommon.WithJSONContent(evt, payload)
}

// usageEvents prices one per-turn figure against the run's model. This
// transport only ever serves the first-party `codex` type — openai-compatible
// runs through codexapp — so its price table is the one to bill against.
func (p *StreamProcessor) usageEvents(perTurn *codexTokenUsage, contextWindow int) []agent.StreamEvent {
	return codexcommon.UsageEvents(config.CLITypeCodex, p.Model, perTurn.common(), contextWindow)
}

// tokenCountFromEnvelope handles the legacy "token_count" envelope so
// state-aware delta computation can happen here instead of inside the
// stateless ParseCodexLine.
//
// Older codex CLIs (the family that ran the conversations now showing
// monotonically growing per-turn token counts in the DB) omit
// last_token_usage and only emit a cumulative total_token_usage on
// each turn. The previous code path used the cumulative figure as if
// it were per-turn, which made the chat header's "this turn cost"
// chip grow with every reply and inflated the daily token_usage row
// for that user. We now subtract the previously-stored total to
// recover the true per-turn delta.
//
// Returns (events, true) when the line is a token_count envelope
// (caller skips ParseCodexLine for this line); (nil, false) otherwise.
// Returning (nil, true) is the correct response to a token_count
// envelope whose Info is empty — we matched, but produced no event.
func (p *StreamProcessor) tokenCountFromEnvelope(env codexEnvelope) ([]agent.StreamEvent, bool) {
	if env.Msg.Type != "token_count" {
		return nil, false
	}
	if env.Msg.Info == nil {
		return nil, true
	}
	info := env.Msg.Info

	var perTurn *codexTokenUsage
	switch {
	case info.LastTokenUsage != nil:
		// Modern CLI: trust the per-turn breakdown directly. Still
		// record the running total so a CLI version drift mid-session
		// (last_token_usage suddenly stops being emitted) leaves us
		// with a baseline for the delta fallback path.
		perTurn = info.LastTokenUsage
		if info.TotalTokenUsage != nil {
			p.prevTotalUsage = info.TotalTokenUsage
		}
	case info.TotalTokenUsage != nil:
		// Older CLI: derive per-turn from delta(total).
		if p.requiresPreviousUsage && p.prevTotalUsage == nil {
			p.prevTotalUsage = info.TotalTokenUsage
			p.requiresPreviousUsage = false
			return nil, true
		}
		perTurn = subtractCodexUsage(info.TotalTokenUsage, p.prevTotalUsage)
		p.prevTotalUsage = info.TotalTokenUsage
	default:
		return nil, true
	}

	return p.usageEvents(perTurn, info.ModelContextWindow), true
}

// subtractCodexUsage returns curr - prev for each token field, clamped
// to zero on any field that would have gone negative.
//
// A nil prev (first turn of a session, or a fresh processor with no
// baseline yet) means curr IS the per-turn figure — return it as-is.
//
// A regression on any field (curr.input < prev.input, etc.) is treated
// as a fresh baseline rather than a negative delta: codex's own
// /compact-like flow can rewind the running total internally, and the
// runner.go top-of-file note explicitly calls out that DayMug doesn't
// own a hook for that event. Surfacing a negative usage row would
// poison the daily token_usage rollup; treating the regression as
// "this is now the new baseline" matches what the user actually paid
// for that turn (the post-compact prompt is what the model processed).
func subtractCodexUsage(curr, prev *codexTokenUsage) *codexTokenUsage {
	if curr == nil {
		return nil
	}
	if prev == nil {
		return curr
	}
	if curr.InputTokens < prev.InputTokens || curr.OutputTokens < prev.OutputTokens {
		return curr
	}
	clamp := func(v int) int { return max(v, 0) }
	return &codexTokenUsage{
		InputTokens:         clamp(curr.InputTokens - prev.InputTokens),
		CachedInputTokens:   clamp(curr.CachedInputTokens - prev.CachedInputTokens),
		OutputTokens:        clamp(curr.OutputTokens - prev.OutputTokens),
		ReasoningOutputToks: clamp(curr.ReasoningOutputToks - prev.ReasoningOutputToks),
		TotalTokens:         clamp(curr.TotalTokens - prev.TotalTokens),
	}
}
