package claudecli

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// --- JSON wire types for stream-json parsing ---

type streamMessage struct {
	Type       string                       `json:"type"`
	Subtype    string                       `json:"subtype,omitempty"`
	Event      json.RawMessage              `json:"event,omitempty"`
	Result     string                       `json:"result,omitempty"`
	Usage      *usageInfo                   `json:"usage,omitempty"`
	ModelUsage map[string]*modelUsageDetail `json:"modelUsage,omitempty"`
	TotalCost  *float64                     `json:"total_cost_usd,omitempty"`
	NumTurns   int                          `json:"num_turns,omitempty"`
	// Result correlation is populated by the Agent SDK. A resident query can
	// emit provider-initiated background turns between user turns, so `result`
	// alone is not enough to decide which waiting caller has completed.
	UserMessageUUID string `json:"user_message_uuid,omitempty"`
	// UserMessageUUIDs is the full consumption list. A message the session
	// absorbs mid-turn never gets a result of its own — it appears only
	// here, on the result of the turn that swallowed it.
	UserMessageUUIDs []string       `json:"user_message_uuids,omitempty"`
	Origin           *messageOrigin `json:"origin,omitempty"`

	// ParentToolUseID, when non-empty, marks the line as belonging to a
	// sub-agent invocation (e.g. the Task / Agent tool spawning a worker).
	// The id matches the parent's tool_use block; we don't track the
	// hierarchy beyond "is it a sub-agent?", so any non-empty value flips
	// the resulting events to Subagent=true.
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`

	// system init fields
	Model     string   `json:"model,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Tools     []string `json:"tools,omitempty"`

	// assistant message fields
	Message *assistantMessage `json:"message,omitempty"`

	// background_tasks_changed payload: the complete set of live background
	// tasks after the change. Empty is a meaningful value ("no background
	// work left"), which is why the subtype rather than the field's
	// emptiness decides whether to emit.
	Tasks []backgroundTask `json:"tasks,omitempty"`

	// task_notification fields: one settled background task.
	TaskID  string `json:"task_id,omitempty"`
	Status  string `json:"status,omitempty"`
	Summary string `json:"summary,omitempty"`

	// rate_limit_event payload. Anthropic uses camelCase here while the rest
	// of the stream is snake_case; we keep the wire shape and re-emit
	// normalised snake_case in parseRateLimit so handler/UI stays in line
	// with the rest of the agent.StreamEvent payloads.
	RateLimitInfo *rateLimitInfo `json:"rate_limit_info,omitempty"`
}

type messageOrigin struct {
	Kind    string `json:"kind,omitempty"`
	Subkind string `json:"subkind,omitempty"`
}

// backgroundTask is one entry of a background_tasks_changed level frame.
// The payload carries ids only — it is deliberately not correlatable with
// the task_started / task_notification edge stream, whose ordering relative
// to the level is unspecified.
type backgroundTask struct {
	TaskID      string `json:"task_id"`
	TaskType    string `json:"task_type,omitempty"`
	Description string `json:"description,omitempty"`
	// Ambient tasks are SDK-owned watchers, not user work. They remain in the
	// process-level set after a command settles and must not keep a resident
	// bridge alive on their own.
	Ambient bool `json:"ambient,omitempty"`
}

type rateLimitInfo struct {
	Status                string `json:"status,omitempty"`
	ResetsAt              int64  `json:"resetsAt,omitempty"`
	RateLimitType         string `json:"rateLimitType,omitempty"`
	OverageStatus         string `json:"overageStatus,omitempty"`
	OverageDisabledReason string `json:"overageDisabledReason,omitempty"`
	IsUsingOverage        bool   `json:"isUsingOverage,omitempty"`
}

type usageInfo struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	// CacheCreation breaks the cache_creation_input_tokens total into 5min
	// vs 1h ephemeral writes. Anthropic prices the two TTLs at different
	// rates (1h ≈ 2x of 5m) so without the split a turn whose 165k cache
	// write was overwhelmingly 5m looks suspiciously cheap relative to the
	// raw token count. Optional: older CLI builds omit the sub-object.
	CacheCreation *cacheCreationDetail `json:"cache_creation,omitempty"`
}

type cacheCreationDetail struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

type modelUsageDetail struct {
	ContextWindow int     `json:"contextWindow"`
	InputTokens   int     `json:"inputTokens,omitempty"`
	OutputTokens  int     `json:"outputTokens,omitempty"`
	CostUSD       float64 `json:"costUSD,omitempty"`
	// The cache totals are read only to price claude-compatible locally;
	// see agent.ModelUsage.PricingCacheReadInputTokens.
	CacheReadInputTokens     int `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens,omitempty"`
}

// resultUsageIsCumulative is the one place that states what a Claude Code
// `result` frame's usage means. Measured on CLI 2.1.280 by running a
// session and then `--resume`-ing it: modelUsage (→ per_model) and
// total_cost_usd are running totals for the whole provider session, while
// the top-level usage block is the turn's own. That is a property of the
// Claude Code client, so it holds for every provider this adapter serves —
// first-party claude and claude-compatible alike — and for the Agent SDK,
// which runs the same client.
const resultUsageIsCumulative = true

type assistantMessage struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	ID    string          `json:"id,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type apiEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index,omitempty"`
	Delta        json.RawMessage `json:"delta,omitempty"`
	ContentBlock json.RawMessage `json:"content_block,omitempty"`
	// Message is populated on Anthropic message_start events; we read its
	// usage subfield to drive the context bar live (without waiting for the
	// terminal `result` event which only fires at end of turn).
	Message json.RawMessage `json:"message,omitempty"`
}

// messageStartEnvelope mirrors the subset of the API "message" object we care
// about — essentially message.usage for live context tracking.
type messageStartEnvelope struct {
	Usage *usageInfo `json:"usage,omitempty"`
}

type textDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
}

type contentBlockInfo struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

// ParseStreamLine parses a single line of claude stream-json output and
// returns events. Stateless: when picking the primary model out of a result's
// modelUsage map it falls back to "largest inputTokens wins", which is fine
// for fixture-driven unit tests but mis-attributes a heavy sub-agent's window
// to the parent's bar in production. StreamProcessor.Process calls the
// internal helper directly with currentModel from system_init so the parent's
// own window wins regardless of which sub-agent burned more tokens.
func ParseStreamLine(line []byte) []agent.StreamEvent {
	return parseStreamLineInternal(line, "")
}

func parseStreamLineInternal(line []byte, preferredModel string) []agent.StreamEvent {
	var msg streamMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil
	}

	var events []agent.StreamEvent
	switch msg.Type {
	case "stream_event":
		events = parseAPIEvent(msg.Event)

	case "system":
		// Only these three subtypes map to events; the CLI emits a dozen
		// more (thinking_tokens, session_state_changed, hook_*, ...) and
		// silently ignoring them is deliberate — an unknown subtype is a
		// newer CLI telling us about something we have no consumer for.
		switch msg.Subtype {
		case "init":
			events = parseSystemInit(msg)
		case "background_tasks_changed":
			events = parseBackgroundTasks(msg)
		case "task_notification":
			events = parseTaskNotification(msg)
		}

	case "result":
		events = parseResult(msg, preferredModel)

	case "assistant":
		events = parseAssistantMessage(msg)

	case "rate_limit_event":
		events = parseRateLimit(msg)
	}

	if len(events) == 0 {
		return nil
	}

	if msg.ParentToolUseID != "" {
		for i := range events {
			events[i].Subagent = true
		}
	}
	return events
}

func parseSystemInit(msg streamMessage) []agent.StreamEvent {
	info := map[string]any{
		"model": msg.Model,
	}
	if msg.SessionID != "" {
		info["session_id"] = msg.SessionID
	}
	if len(msg.Tools) > 0 {
		info["tools"] = msg.Tools
	}
	return streamcommon.Event(agent.KindSystemInit, info)
}

// parseBackgroundTasks re-emits the level frame verbatim. The empty set is
// the important case — it is what tells a resident bridge that the work it
// was staying alive for has finished — so the tasks array is always present
// in the payload, never elided when empty.
func parseBackgroundTasks(msg streamMessage) []agent.StreamEvent {
	tasks := msg.Tasks
	if tasks == nil {
		tasks = []backgroundTask{}
	}
	return streamcommon.Event(agent.KindBackgroundTasks, map[string]any{"tasks": tasks})
}

// parseTaskNotification carries the settled-task cue. A notification with no
// summary has nothing to show a user, so it is dropped rather than rendered
// as an empty line.
func parseTaskNotification(msg streamMessage) []agent.StreamEvent {
	if msg.Summary == "" {
		return nil
	}
	info := map[string]any{"summary": msg.Summary}
	if msg.TaskID != "" {
		info["task_id"] = msg.TaskID
	}
	if msg.Status != "" {
		info["status"] = msg.Status
	}
	return streamcommon.Event(agent.KindTaskNotification, info)
}

// resultUsageReport lifts a result frame's usage into the typed report.
func resultUsageReport(msg streamMessage) agent.UsageReport {
	u := msg.Usage
	report := agent.UsageReport{
		InputTokens:              int64(u.InputTokens),
		OutputTokens:             int64(u.OutputTokens),
		CacheReadInputTokens:     int64(max(u.CacheReadInputTokens, 0)),
		CacheCreationInputTokens: int64(max(u.CacheCreationInputTokens, 0)),
		TotalCostUSD:             msg.TotalCost,
		NumTurns:                 int64(max(msg.NumTurns, 0)),
		Cumulative:               resultUsageIsCumulative,
	}
	// Forward the 5m/1h split when the CLI provides it. Lets the frontend
	// render "Cache write: 165k (5m: 1k / 1h: 164k)" so a reader can
	// sanity-check why this turn cost what it cost — 1h writes price ~2x the
	// 5m rate, so the same 165k figure can mean very different bills
	// depending on the mix.
	if cc := u.CacheCreation; cc != nil && (cc.Ephemeral5mInputTokens > 0 || cc.Ephemeral1hInputTokens > 0) {
		report.CacheCreation = &agent.CacheCreationSplit{
			Ephemeral5mInputTokens: int64(max(cc.Ephemeral5mInputTokens, 0)),
			Ephemeral1hInputTokens: int64(max(cc.Ephemeral1hInputTokens, 0)),
		}
	}
	// Include the per-model breakdown so accounting can attribute the turn's
	// tokens to the right model name (a single turn can land on the primary
	// model plus any sub-agent worker model).
	for name, detail := range msg.ModelUsage {
		if detail == nil {
			continue
		}
		if report.PerModel == nil {
			report.PerModel = make(map[string]agent.ModelUsage, len(msg.ModelUsage))
		}
		report.PerModel[name] = agent.ModelUsage{
			InputTokens:                     int64(detail.InputTokens),
			OutputTokens:                    int64(detail.OutputTokens),
			CostUSD:                         agent.USD(detail.CostUSD),
			PricingCacheReadInputTokens:     int64(max(detail.CacheReadInputTokens, 0)),
			PricingCacheCreationInputTokens: int64(max(detail.CacheCreationInputTokens, 0)),
		}
	}
	return report
}

func parseResult(msg streamMessage, preferredModel string) []agent.StreamEvent {
	var events []agent.StreamEvent

	// Context usage (backward compatible)
	if cu := extractContextUsage(msg, preferredModel); cu.Kind != "" {
		events = append(events, cu)
	}

	// Detailed usage
	if msg.Usage != nil {
		usageEvents := streamcommon.UsageEvent(resultUsageReport(msg))
		for i := range usageEvents {
			usageEvents[i].TurnID = msg.UserMessageUUID
			usageEvents[i].TurnIDs = msg.UserMessageUUIDs
		}
		events = append(events, usageEvents...)
	}

	// Result text
	result := agent.StreamEvent{
		Kind:    agent.KindResult,
		Content: msg.Result,
		TurnID:  msg.UserMessageUUID,
		TurnIDs: msg.UserMessageUUIDs,
	}
	if msg.Origin != nil {
		result.TurnOrigin = msg.Origin.Kind
		if msg.Origin.Subkind != "" {
			result.TurnOrigin += ":" + msg.Origin.Subkind
		}
	}
	events = append(events, result)

	return events
}

// parseRateLimit normalises the Anthropic-flavoured camelCase rate_limit_event
// into snake_case agent.StreamEvent content. A frame without a window type or
// without a resets_at value is dropped — both are required for the
// frontend countdown to make sense, and the CLI never omits them in
// practice.
func parseRateLimit(msg streamMessage) []agent.StreamEvent {
	info := msg.RateLimitInfo
	if info == nil || info.RateLimitType == "" || info.ResetsAt == 0 {
		return nil
	}
	payload := map[string]any{
		"type":      info.RateLimitType,
		"status":    info.Status,
		"resets_at": info.ResetsAt,
	}
	if info.OverageStatus != "" {
		payload["overage_status"] = info.OverageStatus
	}
	if info.IsUsingOverage {
		payload["is_using_overage"] = true
	}
	return streamcommon.Event(agent.KindRateLimit, payload)
}

func parseAssistantMessage(msg streamMessage) []agent.StreamEvent {
	if msg.Message == nil {
		return nil
	}
	var events []agent.StreamEvent
	for _, block := range msg.Message.Content {
		if block.Type == "tool_use" && block.Name != "" {
			info := map[string]any{
				"name": block.Name,
			}
			if block.ID != "" {
				info["id"] = block.ID
			}
			if len(block.Input) > 0 {
				info["input"] = block.Input
			}
			events = append(events, streamcommon.Event(agent.KindToolResult, info)...)
		}
	}
	return events
}

func extractContextUsage(msg streamMessage, preferredModel string) agent.StreamEvent {
	if msg.Usage == nil {
		return agent.StreamEvent{}
	}

	// "Used" is the size of the input context window for this turn:
	// uncached input + cache reads + new cache writes. Output tokens are
	// the model's response and don't count against the input window
	// (extended-thinking output in particular can be tens of thousands of
	// tokens, which previously pushed the bar past 100%).
	used := msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens

	// Pick the "primary" model for this turn. modelUsage may contain
	// multiple entries when the turn invoked sub-agents (e.g. a Task tool
	// worker running a different model / tier). Prefer the entry whose
	// key matches the caller-supplied preferredModel — that's the parent
	// conversation model from system_init, and its contextWindow is what
	// the user's bar should reflect. Without that anchor "largest
	// inputTokens wins" mis-attributes a heavy sub-agent's window to the
	// parent: when the user picks plain "claude-opus-4-8" (200K) but
	// claude-code spawns an Opus[1m] sub-agent that reads big files, the
	// sub-agent's 1M entry can outweigh the parent's tokens and the bar
	// flips to /1M for the turn.
	//
	// The "largest" fallback still runs when preferredModel is empty
	// (ParseStreamLine's stateless path) or when there is no exact match
	// in modelUsage (rare — a turn the parent did zero input work in).
	var primaryName string
	var primaryDetail *modelUsageDetail
	if preferredModel != "" {
		if d, ok := msg.ModelUsage[preferredModel]; ok && d != nil {
			primaryName = preferredModel
			primaryDetail = d
		}
	}
	if primaryDetail == nil {
		for name, detail := range msg.ModelUsage {
			if detail == nil {
				continue
			}
			if primaryDetail == nil || detail.InputTokens > primaryDetail.InputTokens {
				primaryName = name
				primaryDetail = detail
			}
		}
	}

	total := 0
	if primaryDetail != nil && primaryDetail.ContextWindow > 0 {
		total = primaryDetail.ContextWindow
	} else if primaryName != "" {
		total = lookupContextWindow(primaryName)
	}

	if total == 0 {
		return agent.StreamEvent{}
	}
	if used > total {
		used = total
	}

	// Include the cache breakdown so the frontend can show input vs cached
	// tokens in the tooltip without parsing a separate "usage" event. These
	// fields are additive to the legacy {used,total} contract.
	payload := map[string]int{
		"used":           used,
		"total":          total,
		"input_tokens":   msg.Usage.InputTokens,
		"cache_read":     msg.Usage.CacheReadInputTokens,
		"cache_creation": msg.Usage.CacheCreationInputTokens,
	}
	content, _ := json.Marshal(payload)
	return agent.StreamEvent{Kind: agent.KindContextUsage, Content: string(content)}
}

// lookupContextWindow returns a fallback context-window size for a Claude
// model ID when the CLI's modelUsage payload doesn't carry contextWindow
// (older CLI versions, or future schema drift). The "[1m]" suffix marks the
// 1M-context variant of a Claude model. The mimo-v2.5 prefix is matched on
// the model id, not the provider type: Xiaomi's endpoint is now reached as a
// generic claude-compatible provider, and its chat models still advertise a
// 1M window.
func lookupContextWindow(model string) int {
	if strings.Contains(model, "[1m]") {
		return 1_000_000
	}
	if strings.HasPrefix(model, "mimo-v2.5") {
		return 1_000_000
	}
	// The Claude Fable line (5, 5.1, …) ships a single 1M-context tier (no
	// 200K variant, so no [1m] suffix above) — map the whole prefix before
	// the generic claude- 200K fallback.
	if strings.HasPrefix(model, "claude-fable-5") {
		return 1_000_000
	}
	if strings.HasPrefix(model, "claude-") {
		return 200_000
	}
	return 0
}

func parseAPIEvent(raw json.RawMessage) []agent.StreamEvent {
	if raw == nil {
		return nil
	}

	var evt apiEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return nil
	}

	switch evt.Type {
	case "content_block_start":
		return parseContentBlockStart(evt)
	case "content_block_delta":
		return parseContentBlockDelta(evt)
	case "message_start":
		return parseMessageStart(evt)
	}

	return nil
}

func parseMessageStart(evt apiEvent) []agent.StreamEvent {
	if evt.Message == nil {
		return nil
	}
	var env messageStartEnvelope
	if err := json.Unmarshal(evt.Message, &env); err != nil || env.Usage == nil {
		return nil
	}
	used := env.Usage.InputTokens + env.Usage.CacheReadInputTokens + env.Usage.CacheCreationInputTokens
	if used <= 0 {
		return nil
	}
	payload := map[string]int{
		"used":           used,
		"input_tokens":   env.Usage.InputTokens,
		"cache_read":     env.Usage.CacheReadInputTokens,
		"cache_creation": env.Usage.CacheCreationInputTokens,
	}
	return streamcommon.Event(agent.KindLiveUsage, payload)
}

func parseContentBlockStart(evt apiEvent) []agent.StreamEvent {
	if evt.ContentBlock == nil {
		return nil
	}
	var cb contentBlockInfo
	if err := json.Unmarshal(evt.ContentBlock, &cb); err != nil {
		return nil
	}

	if cb.Type == "tool_use" && cb.Name != "" {
		info := map[string]string{"name": cb.Name}
		if cb.ID != "" {
			info["id"] = cb.ID
		}
		return streamcommon.Event(agent.KindToolUseStart, info)
	}

	return nil
}

func parseContentBlockDelta(evt apiEvent) []agent.StreamEvent {
	if evt.Delta == nil {
		return nil
	}
	var d textDelta
	if err := json.Unmarshal(evt.Delta, &d); err != nil {
		return nil
	}

	switch d.Type {
	case "text_delta":
		if d.Text != "" {
			return []agent.StreamEvent{{Kind: agent.KindDelta, Content: d.Text, BlockIndex: evt.Index}}
		}
	case "input_json_delta":
		if d.PartialJSON != "" {
			return []agent.StreamEvent{{Kind: agent.KindToolInputDelta, Content: d.PartialJSON, BlockIndex: evt.Index}}
		}
	case "thinking_delta":
		if d.Thinking != "" {
			return []agent.StreamEvent{{Kind: agent.KindThinkingDelta, Content: d.Thinking, BlockIndex: evt.Index}}
		}
	}

	return nil
}

// StreamProcessor wraps ParseStreamLine with the bit of cross-line state we
// need to keep the context-usage bar accurate during a turn:
//
//   - currentModel: captured from system_init so we can fall back to a
//     hard-coded context window when the CLI's modelUsage is missing or
//     incomplete (e.g. the very first turn in a fresh session).
//   - lastTotal: the most recently observed authoritative context window
//     (from a `result` event's modelUsage). Used to convert the partial
//     agent.KindLiveUsage events emitted at message_start into full
//     agent.KindContextUsage events (`{used,total}`) so the bar refreshes during
//     a long turn instead of only at end-of-turn.
//
// ParseStreamLine itself stays stateless so unit tests don't need fixtures.
type StreamProcessor struct {
	// provider is DayMug's CLIType for the conversation this stream
	// belongs to ("claude" or "claude-compatible"). Drives the per-turn
	// cost override path: for "claude-compatible" the CLI's
	// total_cost_usd is computed against Anthropic's price list, which is
	// not the list the third-party endpoint bills against, so enrichUsage
	// replaces it with a local computation. For "claude" and the legacy
	// empty value, enrichUsage is a no-op.
	provider string
	// runModel is the DayMug UI-label model the conversation requested
	// (RunRequest.Model). Used to key the
	// pricing table; the CLI's own system_init model string can
	// legitimately diverge (it strips the [1m] suffix when the CLI
	// silently upgrades to the 1M tier) so we don't reuse currentModel
	// for the price lookup.
	runModel     string
	currentModel string
	lastTotal    int
	// sawLiveUsageThisTurn / lastLive* mirror the most recent
	// message_start's per-API-call usage snapshot in the current turn,
	// and reset on agent.KindResult. They feed the merge at end-of-turn:
	// claude's result.usage sums {input,cache_read,cache_creation}_tokens
	// across every internal Anthropic call (each tool iteration and
	// sub-agent appends another row), so its `used` routinely exceeds the
	// model context window and gets clamped to 100% — but its `total`
	// (from modelUsage.contextWindow) is authoritative. The live event's
	// `total` is only a lookupContextWindow guess off system_init's model
	// name, which lies when the CLI silently upgrades a non-[1m] model to
	// the 1M tier (modelUsage reports the [1m] key + 1M window but
	// system_init reports the plain name). So we keep result's `total`
	// and substitute live's per-call `used` + breakdown, which corrects
	// both the cumulative-vs-snapshot and the 200K-vs-1M lies in one
	// pass. When no message_start arrived (one-shot calls, older CLIs
	// without stream_event lines) sawLive stays false and the
	// result-derived event flows through unchanged.
	sawLiveUsageThisTurn  bool
	lastLiveUsed          int
	lastLiveInputTokens   int
	lastLiveCacheRead     int
	lastLiveCacheCreation int
	// lastTextBlockIdx / lastThinkingBlockIdx remember which content_block
	// the previous parent-track delta belonged to. When a new delta arrives
	// from a different block (because the model split its output across
	// multiple text blocks, or interleaved a tool_use between two text
	// blocks), Process injects a "\n\n" separator into the new block's
	// first delta so the accumulated stream the frontend renders contains
	// the paragraph boundary the model intended. Without this, "prose."
	// from block N and "### Heading" from block N+1 are concatenated as
	// "prose.### Heading" and markdown-it stops recognising the heading.
	// -1 means "no parent text/thinking block has been emitted in this
	// turn yet" (i.e. don't insert a separator — there's nothing to
	// separate from).
	lastTextBlockIdx     int
	lastThinkingBlockIdx int
}

// NewStreamProcessor returns a fresh processor with no provider context.
// The legacy entry point — kept for tests that don't need the compatible-
// endpoint cost injection. Production paths use NewStreamProcessorForRun.
func NewStreamProcessor() *StreamProcessor {
	return &StreamProcessor{
		lastTextBlockIdx:     -1,
		lastThinkingBlockIdx: -1,
	}
}

// NewStreamProcessorForRun returns a processor wired to a specific
// conversation's provider + model. Both are required for the local cost
// override (claude-compatible only); when provider is "claude" or empty,
// the processor behaves identically to NewStreamProcessor.
func NewStreamProcessorForRun(provider, model string) *StreamProcessor {
	p := NewStreamProcessor()
	p.provider = provider
	p.runModel = model
	return p
}

// Process parses one line of stream-json output and returns any events the
// downstream consumer should see. agent.KindLiveUsage events are absorbed and
// converted into synthesized agent.KindContextUsage events when total is known
// (or recoverable via the model-name fallback table); they're never
// surfaced to consumers directly.
func (p *StreamProcessor) Process(line []byte) []agent.StreamEvent {
	// Feed the parent model from system_init into the primary-model
	// selection so a heavy sub-agent's window can't poison the parent's bar.
	raw := parseStreamLineInternal(line, p.currentModel)
	if len(raw) == 0 {
		return nil
	}
	out := make([]agent.StreamEvent, 0, len(raw))
	for _, evt := range raw {
		switch evt.Kind {
		case agent.KindSystemInit:
			// Only update tracked state from the parent agent's init.
			// Sub-agents have their own model + tool list; letting them
			// rewrite p.currentModel would corrupt the context-window
			// fallback used for the parent's bar (and the model shown
			// in the chat header).
			if !evt.Subagent {
				var info struct {
					Model string `json:"model"`
				}
				if json.Unmarshal([]byte(evt.Content), &info) == nil && info.Model != "" {
					p.currentModel = info.Model
				}
			}
			out = append(out, evt)

		case agent.KindContextUsage:
			// Same reasoning as system_init: a sub-agent's context window
			// (e.g. a Haiku worker) must not overwrite the parent's
			// authoritative total used for live-usage synthesis.
			if !evt.Subagent {
				var cu struct {
					Total int `json:"total"`
				}
				if json.Unmarshal([]byte(evt.Content), &cu) == nil && cu.Total > 0 {
					p.lastTotal = cu.Total
				}
			}
			// Merge with the live snapshot when this turn had one (see
			// the StreamProcessor comment for why). Keep result's `total`
			// — that's the only authoritative source for 200K vs 1M.
			if !evt.Subagent && p.sawLiveUsageThisTurn && p.lastLiveUsed > 0 {
				evt = mergeLiveIntoResultContextUsage(
					evt,
					p.lastLiveUsed,
					p.lastLiveInputTokens,
					p.lastLiveCacheRead,
					p.lastLiveCacheCreation,
				)
			}
			out = append(out, evt)

		case agent.KindLiveUsage:
			// Sub-agent live usage events would mix the worker's input
			// tokens into the parent's bar — drop them entirely.
			if evt.Subagent {
				// Keep only the internal model-call edge. The request is already
				// accepted upstream at message_start, but this is the earliest
				// boundary Claude Code exposes for subagent calls.
				out = append(out, agent.StreamEvent{Kind: agent.KindModelCall, Content: `{}`, Subagent: true})
				continue
			}
			synth := p.synthesizeFromLive(evt.Content)
			if synth.Kind != "" {
				// Capture from the RAW live event, not the synth output:
				// synth clamps used to lastTotal, which loses information
				// in the 200K→1M transition (claude silently upgrades the
				// session and the first message_start of the new tier
				// reports raw used > stale 200K total). Letting raw flow
				// into the snapshot lets the next merge use the result's
				// authoritative 1M total without the bar getting stuck at
				// the clamped 200K value.
				p.captureLiveSnapshot(evt.Content)
				synth.ModelCall = true
				out = append(out, synth)
			} else {
				// A malformed or context-window-free usage snapshot still proves
				// that Claude accepted a model request. Keep the accounting edge
				// even when there is no usable context bar to render.
				out = append(out, agent.StreamEvent{Kind: agent.KindModelCall, Content: `{}`})
			}
			// Drop the raw agent.KindLiveUsage event either way — it is a
			// processor-internal signal, never delivered to consumers.

		case agent.KindDelta:
			// Insert a paragraph separator the first time a parent-track
			// text delta arrives from a NEW content_block (cf. comment on
			// lastTextBlockIdx). Sub-agent deltas use the worker's own
			// frontend buffer, so leave their cross-block joins to the
			// sub-agent track's accumulator.
			if !evt.Subagent {
				if p.lastTextBlockIdx >= 0 && evt.BlockIndex != p.lastTextBlockIdx {
					evt.Content = "\n\n" + evt.Content
				}
				p.lastTextBlockIdx = evt.BlockIndex
			}
			out = append(out, evt)

		case agent.KindUsage:
			out = append(out, p.enrichUsage(evt))

		case agent.KindThinkingDelta:
			if !evt.Subagent {
				if p.lastThinkingBlockIdx >= 0 && evt.BlockIndex != p.lastThinkingBlockIdx {
					evt.Content = "\n\n" + evt.Content
				}
				p.lastThinkingBlockIdx = evt.BlockIndex
			}
			out = append(out, evt)

		case agent.KindResult:
			// End of turn: clear the per-turn block trackers so the next
			// turn's first delta is emitted without a stray leading "\n\n",
			// and the live-snapshot fields so the next turn's first
			// message_start is the one that re-arms the merge path.
			p.lastTextBlockIdx = -1
			p.lastThinkingBlockIdx = -1
			p.sawLiveUsageThisTurn = false
			p.lastLiveUsed = 0
			p.lastLiveInputTokens = 0
			p.lastLiveCacheRead = 0
			p.lastLiveCacheCreation = 0
			out = append(out, evt)

		default:
			out = append(out, evt)
		}
	}
	return out
}

// enrichUsage replaces the cost when the conversation is bound to a provider
// whose CLI-reported cost is wrong — claude-compatible, where Claude Code
// computes total_cost_usd against Anthropic's price list while the actual
// bill comes from a third-party endpoint. For claude / unset provider the
// event passes through unchanged: the CLI's own cost matches Anthropic
// billing exactly.
//
// The local price must keep the report's semantics intact. With a per_model
// breakdown every figure it prices is a session running total (see
// resultUsageIsCumulative), so each model is priced on its own cumulative
// tokens and cache counters and total_cost_usd becomes their sum — still
// cumulative, differenced downstream exactly like first-party Claude. Mixing
// the per-turn top-level cache counters into a cumulative per-model price
// (what this used to do) produced a number that was neither.
//
// modelUsage does not split cache writes by TTL, so cumulative cache writes
// are priced at the 5m rate. That under-bills by (1h cache-write tokens) ×
// (1h rate − 5m rate) when an endpoint actually writes 1h entries; the 5m
// fallback is the same conservative choice the per-turn branch below makes
// when the split is missing, and pricing the cumulative total by this turn's split
// would let the running cost fall between turns, which the downstream
// differencing treats as a session reset and bills in full.
//
// Without per_model the report only carries this turn's own counters, so it
// is priced from them — including the 5m/1h split — and marked non-cumulative.
func (p *StreamProcessor) enrichUsage(evt agent.StreamEvent) agent.StreamEvent {
	if p.provider != config.CLITypeClaudeCompatible {
		return evt
	}
	report, ok := agent.UsageOf(evt)
	if !ok {
		return evt
	}
	if len(report.PerModel) > 0 {
		names := make([]string, 0, len(report.PerModel))
		for name := range report.PerModel {
			names = append(names, name)
		}
		sort.Strings(names)
		perModel := make(map[string]agent.ModelUsage, len(report.PerModel))
		var total float64
		for _, name := range names {
			entry := report.PerModel[name]
			cost := pricing.ComputeClaudeCost(p.provider, name,
				int(entry.InputTokens), int(entry.PricingCacheReadInputTokens),
				int(entry.PricingCacheCreationInputTokens), 0, int(entry.OutputTokens))
			entry.CostUSD = agent.USD(cost)
			perModel[name] = entry
			total += cost
		}
		report.PerModel = perModel
		report.TotalCostUSD = agent.USD(total)
	} else {
		fiveMin, oneHour := report.CacheCreationInputTokens, int64(0)
		if report.CacheCreation != nil {
			fiveMin, oneHour = report.CacheCreation.Ephemeral5mInputTokens, report.CacheCreation.Ephemeral1hInputTokens
		}
		report.TotalCostUSD = agent.USD(pricing.ComputeClaudeCost(p.provider, p.runModel,
			int(report.InputTokens), int(report.CacheReadInputTokens), int(fiveMin), int(oneHour), int(report.OutputTokens)))
		report.Cumulative = false
	}
	enriched, err := evt.WithUsage(report)
	if err != nil {
		return evt
	}
	return enriched
}

// synthesizeFromLive turns a partial agent.KindLiveUsage payload into a full
// agent.KindContextUsage event using whatever total we already know about. Returns
// a zero agent.StreamEvent when total can't be resolved — better to skip a frame
// than to show a misleading 0-of-0 bar.
func (p *StreamProcessor) synthesizeFromLive(content string) agent.StreamEvent {
	var lu struct {
		Used          int `json:"used"`
		InputTokens   int `json:"input_tokens"`
		CacheRead     int `json:"cache_read"`
		CacheCreation int `json:"cache_creation"`
	}
	if err := json.Unmarshal([]byte(content), &lu); err != nil || lu.Used <= 0 {
		return agent.StreamEvent{}
	}
	total := p.lastTotal
	if total == 0 {
		total = lookupContextWindow(p.currentModel)
	}
	if total == 0 {
		return agent.StreamEvent{}
	}
	used := lu.Used
	if used > total {
		used = total
	}
	payload := map[string]int{
		"used":           used,
		"total":          total,
		"input_tokens":   lu.InputTokens,
		"cache_read":     lu.CacheRead,
		"cache_creation": lu.CacheCreation,
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return agent.StreamEvent{}
	}
	return agent.StreamEvent{Kind: agent.KindContextUsage, Content: string(out)}
}

// captureLiveSnapshot records the per-API-call usage values from the raw
// agent.KindLiveUsage payload so the next agent.KindContextUsage from a result event
// can merge in `used` + breakdown without re-parsing in two places.
// Reads from the raw live payload (not the post-synth one) on purpose:
// synthesizeFromLive clamps used to lastTotal, which throws away the
// true input count if the session just crossed from 200K into 1M tier
// — the snapshot needs the unclamped value so the merge step can use
// result's authoritative new total as the only cap.
func (p *StreamProcessor) captureLiveSnapshot(content string) {
	var s struct {
		Used          int `json:"used"`
		InputTokens   int `json:"input_tokens"`
		CacheRead     int `json:"cache_read"`
		CacheCreation int `json:"cache_creation"`
	}
	if json.Unmarshal([]byte(content), &s) != nil {
		return
	}
	p.sawLiveUsageThisTurn = true
	p.lastLiveUsed = s.Used
	p.lastLiveInputTokens = s.InputTokens
	p.lastLiveCacheRead = s.CacheRead
	p.lastLiveCacheCreation = s.CacheCreation
}

// mergeLiveIntoResultContextUsage rewrites the result-derived
// context_usage event so that `total` (authoritative — the only signal
// that distinguishes a 200K window from a 1M window when the CLI
// silently upgrades a non-[1m] model) is kept, while `used` + breakdown
// are replaced by the most recent per-API-call snapshot (avoiding the
// cumulative sum that pins the bar at 100% on multi-iteration turns).
// Returns the original event unchanged on any unmarshal/marshal error;
// emitting the un-merged cumulative bar is still better than dropping
// the event entirely.
func mergeLiveIntoResultContextUsage(evt agent.StreamEvent, used, input, cacheRead, cacheCreation int) agent.StreamEvent {
	var payload map[string]int
	if json.Unmarshal([]byte(evt.Content), &payload) != nil || payload == nil {
		return evt
	}
	payload["used"] = used
	payload["input_tokens"] = input
	payload["cache_read"] = cacheRead
	payload["cache_creation"] = cacheCreation
	if total, ok := payload["total"]; ok && total > 0 && payload["used"] > total {
		payload["used"] = total
	}
	return streamcommon.WithJSONContent(evt, payload)
}
