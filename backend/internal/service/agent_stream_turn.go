package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/prompts"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// turnState is everything one runOnce attempt accumulates while it consumes
// the stream. runOnce owns the loop; each event kind is folded in by its own
// method here, so what a given frame does to persistence, usage accounting and
// the wire stays readable in one place.
type turnState struct {
	s   *AgentStreamer
	ctx context.Context
	req AgentStreamRequest
	// opts is the attempt's own copy of req.Opts: it gains a default control
	// id, the residency grant, and the session id system_init reports.
	opts      agent.RunRequest
	persist   *MessagePersister
	broadcast func(ServerMessage)
	resident  atomic.Bool

	// rollbackOriginalSize == -1 means "file didn't exist before this turn" —
	// on retry rollback we remove the whole file. Any other value is the
	// pre-turn byte length; rollback truncates back to that boundary, which
	// lands cleanly on a JSONL line break because we only ever observe the
	// file at append-only end-of-turn boundaries.
	rollbackPath         string
	rollbackOriginalSize int64

	// thinking, reply and (in aggregate mode) aggregate accumulate what the
	// stream showed so we can persist whatever was actually shown to the user
	// — even when the run is interrupted (page refresh while stream-killed by
	// a service restart, or CancelJob on a follow-up prompt). Without this
	// fallback the result event either never arrives or carries an empty
	// payload, PersistResult short-circuits, and the assistant message
	// vanishes after the next reload.
	thinking, reply, aggregate strings.Builder
	persisted                  bool
	// notifyText holds the most recent assistant reply that actually landed in
	// the DB this turn. The push notification fires once, after the stream
	// drains — never per KindResult — so a codex turn that emits several
	// result chunks still pushes a single notification carrying the final
	// reply.
	notifyText string
	finalText  string
	artifacts  []Artifact
	rejections []ArtifactRejection

	// model tracks the parent agent's model id from the most recent
	// system_init event so the usage event at end-of-turn can attribute
	// tokens correctly when the CLI doesn't supply a per-model breakdown.
	model     string
	sessionID string
	// usagePayload holds the most recent parent-agent usage JSON so we can
	// splice it into the result event's metadata (and the assistant row). The
	// stream emits usage immediately before result, so by the time KindResult
	// arrives this is the current turn's blob. Empty when the run produced no
	// usage frame — metadata then carries only the model field.
	usagePayload string
	usageEventID string
	// turnCostUSD adds up what every usage report of this turn billed, so a
	// turn split across several reports (codex steps, a repeated Claude
	// result that bills nothing the second time) still shows its whole cost.
	turnCostUSD                float64
	contextUsed, contextWindow int64
	toolCalls, accountedTools  int64
	userInstructions           int64
	// toolStarted correlates tool calls by the provider's id. The track prefix
	// keeps a sub-agent id from colliding with a parent id; the anonymous
	// fallback matches the frontend's existing one-in-flight-tool-per-track
	// model.
	toolStarted map[string]time.Time

	// produced gates the transient retry: anything the user already saw makes
	// a re-run a duplicate rather than a second attempt.
	produced bool
}

// toolTiming is what one event contributes to tool-call timing.
type toolTiming struct {
	startedAt  int64
	durationMS *int64
}

func (s *AgentStreamer) newTurn(ctx context.Context, req AgentStreamRequest) *turnState {
	// Pin the transport for the whole turn: the run, its steering hook and any
	// retry rollback must all talk to the same adapter even if an admin
	// switches the provider's transport while this turn is in flight.
	req.Backend = agent.Resolve(req.Backend)
	if req.ArtifactRootDir == "" {
		req.ArtifactRootDir = req.WorkDir
	}
	opts := req.Opts
	if opts.ControlID == "" {
		opts.ControlID = req.ConversationID
	}
	broadcast := req.Broadcast
	if broadcast == nil {
		broadcast = func(ServerMessage) {}
	}
	t := &turnState{
		s:                    s,
		ctx:                  ctx,
		req:                  req,
		opts:                 opts,
		persist:              s.persister(),
		broadcast:            broadcast,
		rollbackOriginalSize: -1,
		toolStarted:          make(map[string]time.Time),
		userInstructions:     1,
	}
	if req.ResultOrigin != "" {
		t.userInstructions = 0
	}
	return t
}

// registerControls hands the room the turn's steering and question hooks.
func (t *turnState) registerControls() {
	s, convID, opts := t.s, t.req.ConversationID, t.opts
	if opts.ControlID == "" || s.Broadcaster == nil {
		return
	}
	if steeringBackend, ok := agent.SteererOf(t.req.Backend); ok {
		s.Broadcaster.SetJobSteerer(convID, func(ctx context.Context, messageID, input string) error {
			return steeringBackend.SteerTurn(ctx, opts.ControlID, messageID, input)
		})
	}
	questionBackend, providerQuestions := agent.QuestionResponderOf(t.req.Backend)
	if !providerQuestions || !opts.EnableUserQuestions {
		return
	}
	req, ctx, broadcast := t.req, t.ctx, t.broadcast
	s.Broadcaster.SetJobQuestionResponder(convID, func(answerCtx context.Context, requestID string, answers map[string][]string) error {
		if req.SlotGate == nil {
			return questionBackend.AnswerUserQuestion(answerCtx, opts.ControlID, requestID, answers)
		}
		// Delivering the answer means resuming the turn, and resuming means
		// re-entering the account queue behind whoever started while the
		// question was on screen — potentially a long wait. answerCtx belongs
		// to the websocket frame that carried the answer and expires in
		// seconds, so acknowledge the answer here and finish the handoff on
		// the turn's own context.
		go s.deliverAnswerAfterResume(ctx, req, questionBackend, opts.ControlID, requestID, answers, broadcast)
		return nil
	})
}

// grantResidency lets the backend keep its process alive past this turn.
// Residency is granted here rather than by the caller because this is the
// layer that holds both halves: the account pool that has to pay for a
// resident process, and the persistence surface that can tell the user when
// work was stopped instead of kept.
func (t *turnState) grantResidency() {
	s, req, convID := t.s, t.req, t.req.ConversationID
	if !req.AllowResidency || req.Wake == nil || s.Pool == nil || convID == "" {
		return
	}
	t.opts.Residency = &agent.Residency{
		Permit: func() (func(), bool) {
			release, err := s.Pool.EnterLive(req.AccountName)
			if err != nil {
				log.Printf("[stream] conv=%s cannot keep a resident agent process: %v", convID, err)
				return nil, false
			}
			return release, true
		},
		Parked:   func() { t.resident.Store(true) },
		Activity: req.ResidentActivity,
		Wake:     req.Wake,
		Notice:   func(reason string) { s.persistNotice(t.ctx, convID, t.broadcast, reason, nil) },
	}
}

// snapshotSessionLog records the session log's pre-turn size so a transient
// upstream failure can be retried without replaying the same prompt twice.
// The CLI appends user / assistant / tool lines to its JSONL the moment it
// receives stdin, long before the API roundtrip that may fail. Explicit
// cancellation deliberately does not use this snapshot: Cancel stops the
// current operation, while the next message in the same conversation must
// retain everything the session saw so far.
func (t *turnState) snapshotSessionLog() {
	if t.opts.SessionID == "" {
		return
	}
	p := t.req.Backend.SessionLogPath(t.req.WorkDir, t.opts.SessionID, t.opts.ConfigDir)
	if p == "" {
		return
	}
	t.rollbackPath = p
	if info, err := os.Stat(p); err == nil {
		t.rollbackOriginalSize = info.Size()
	}
}

// launch starts the run (or attaches to one already producing events) and
// returns its event stream and exit error.
func (t *turnState) launch(runCtx context.Context) (<-chan agent.StreamEvent, <-chan error) {
	outputCh := make(chan agent.StreamEvent, 64)
	errCh := make(chan error, 1)
	// Copied before the goroutine starts: the stream loop rewrites
	// t.opts.SessionID on system_init.
	req, opts := t.req, t.opts
	go func() {
		if req.Attach != nil {
			// RunWithSession closes outputCh as part of its contract; an
			// attached turn has no such call, so the close belongs here.
			// Without it the drain loop never ends and the turn hangs holding
			// the room.
			defer close(outputCh)
			errCh <- req.Attach(runCtx, outputCh)
			return
		}
		errCh <- req.Backend.RunWithSession(runCtx, req.Prompt, req.WorkDir, opts, outputCh)
	}()
	return outputCh, errCh
}

// handle folds one stream event into the turn.
func (t *turnState) handle(evt agent.StreamEvent) {
	if t.req.guardrail != nil {
		t.req.guardrail.observe(evt)
	}
	if evt.Kind == agent.KindToolUseStart {
		t.toolCalls++
	}
	t.trackQuestionSlot(evt)
	if evt.Kind == agent.KindContextUsage && !evt.Subagent {
		evt.Content = withContextWindow(evt.Content, t.opts.ContextWindow)
	}
	timing := t.timeTool(evt)
	// Rate-limit frames carry the account's reset time regardless of whether
	// they arrive on the parent or a sub-agent stream, so gate the account
	// before the sub-agent persistence filter below.
	if evt.Kind == agent.KindRateLimit {
		applyRateLimitCooldown(t.s.Pool, t.req.AccountName, t.opts.Model, evt.Content)
	}
	// Sub-agent events still flow to the UI (so the user can see progress
	// inside a Task/Agent invocation) but must not feed the parent's
	// persistence buffers — otherwise the parent's saved assistant text and
	// tool history would absorb the worker's chatter and a reload would replay
	// it as if the parent had said it.
	var toolMsgID string
	if !evt.Subagent {
		toolMsgID = t.absorb(evt, timing)
	}
	t.forward(evt, timing, toolMsgID)
	if !evt.Subagent {
		t.observe(evt, toolMsgID)
	}
}

// trackQuestionSlot gives the account slot back while a question freezes the
// turn until a human replies — the slot it holds is pure dead weight — and
// re-queues on the way out. A provider that resolves its own question (codex's
// auto-resolution window, or a torn-down turn) never reaches the answer path
// in registerControls, so resume from here too; the gate collapses the two
// into one acquisition.
func (t *turnState) trackQuestionSlot(evt agent.StreamEvent) {
	switch evt.Kind {
	case agent.KindUserQuestion:
		t.req.SlotGate.ParkForQuestion(evt.Content)
		if t.req.Notify && !evt.Subagent {
			go t.persist.MaybeNotifyUserQuestion(t.req.ConversationID, evt.Content)
		}
	case agent.KindUserQuestionResolved:
		gate, ctx := t.req.SlotGate, t.ctx
		go func() { _ = gate.Resume(ctx) }()
	}
}

func (t *turnState) timeTool(evt agent.StreamEvent) toolTiming {
	var timing toolTiming
	switch evt.Kind {
	case agent.KindToolUseStart:
		started := time.Now()
		t.toolStarted[toolTimingKey(evt.Subagent, evt.Content)] = started
		timing.startedAt = started.UnixMilli()
	case agent.KindToolResult:
		key := toolTimingKey(evt.Subagent, evt.Content)
		if started, ok := t.toolStarted[key]; ok {
			duration := time.Since(started).Milliseconds()
			if duration < 0 {
				duration = 0
			}
			timing.durationMS = &duration
			delete(t.toolStarted, key)
		}
	}
	return timing
}

// absorb folds a parent-agent event into the turn's buffers and durable state.
// It returns the persisted tool row's id for a tool result ("" when the save
// failed or the event is not a tool result).
func (t *turnState) absorb(evt agent.StreamEvent, timing toolTiming) string {
	switch evt.Kind {
	case agent.KindDelta, agent.KindThinkingDelta, agent.KindResult,
		agent.KindToolUseStart, agent.KindToolResult, agent.KindUserQuestion:
		t.produced = true
	}
	switch evt.Kind {
	case agent.KindSessionWarning:
		t.s.persistSessionWarning(t.ctx, t.req.ConversationID, t.broadcast, evt.Content, nil)
	case agent.KindSystemInit:
		t.onSystemInit(evt.Content)
	case agent.KindContextUsage:
		t.onContextUsage(evt.Content)
	case agent.KindUsage:
		t.onUsage(evt)
	case agent.KindThinkingDelta:
		t.thinking.WriteString(evt.Content)
	case agent.KindDelta:
		t.reply.WriteString(evt.Content)
	case agent.KindResult:
		if t.req.Mode == AgentStreamAggregate {
			appendAggregateChunk(&t.aggregate, evt.Content)
		}
	case agent.KindToolResult:
		return t.persistTool(evt.Content, timing.durationMS)
	}
	return ""
}

func (t *turnState) onSystemInit(content string) {
	s, convID := t.s, t.req.ConversationID
	if m := ExtractModelFromSystemInit(content); m != "" {
		t.model = m
	}
	// Codex assigns its own session id; claude echoes back the id we already
	// supplied. Persist whenever the reported id differs so the next turn
	// resumes the right rollout. Best-effort: a write failure is logged but
	// doesn't break the streaming flow.
	if sid := ExtractSessionIDFromSystemInit(content); sid != "" {
		t.sessionID = sid
		if sid != t.opts.SessionID && s.Store != nil && convID != "" {
			if err := s.Store.SetSessionID(t.ctx, convID, sid); err != nil {
				log.Printf("[stream] persist session id conv=%s: %v", convID, err)
			} else {
				t.opts.SessionID = sid
			}
		}
	}
	// First-attempt lazy snapshot: when the conversation had no session yet,
	// opts.SessionID was empty pre-launch and we couldn't predict the path.
	// Now that system_init has named the session, fill rollbackPath with the
	// file the CLI just started writing to — leaving rollbackOriginalSize at
	// -1 so a subsequent retry removes the wholly-this-attempt file instead of
	// truncating to a fictitious "original" prefix.
	if t.rollbackPath == "" && t.opts.SessionID != "" {
		if p := t.req.Backend.SessionLogPath(t.req.WorkDir, t.opts.SessionID, t.opts.ConfigDir); p != "" {
			t.rollbackPath = p
		}
	}
}

// withContextWindow replaces the agent-reported window with the declared one.
// A Claude Code or Codex CLI pointed at a third-party endpoint sizes the
// window from the first-party model it thinks it is talking to, so without
// this the context bar and auto_compact_ratio would measure against the wrong
// ceiling. Every consumer downstream (UI, stored usage, auto-compact) reads
// the rewritten frame.
func withContextWindow(content string, window int) string {
	if window <= 0 {
		return content
	}
	var usage map[string]any
	if json.Unmarshal([]byte(content), &usage) != nil {
		return content
	}
	usage["total"] = window
	if used, ok := usage["used"].(float64); ok && used > float64(window) {
		usage["used"] = window
	}
	out, err := json.Marshal(usage)
	if err != nil {
		return content
	}
	return string(out)
}

func (t *turnState) onContextUsage(content string) {
	var usage struct {
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
	}
	if json.Unmarshal([]byte(content), &usage) != nil {
		return
	}
	t.contextUsed, t.contextWindow = usage.Used, usage.Total
	if t.usageEventID == "" {
		return
	}
	if err := t.s.Store.UpdateUsageEventContext(t.ctx, t.usageEventID, usage.Used, usage.Total); err != nil {
		log.Printf("[stream] update usage context conv=%s: %v", t.req.ConversationID, err)
	}
}

// onUsage bills one usage report. Tool calls and the user instruction are
// attributed to the first report that lands after them, so a turn with several
// usage frames never counts the same work twice.
func (t *turnState) onUsage(evt agent.StreamEvent) {
	convID := t.req.ConversationID
	t.usageEventID = ""
	if report, ok := agent.UsageOf(evt); ok {
		var billed float64
		t.usageEventID, billed = t.persist.PersistTokenUsage(UsageAttribution{
			ConversationID: convID, AgentID: t.req.OwnerID, FallbackModel: t.model,
			EventID: evt.TurnID, UserInstructions: t.userInstructions,
			ModelRequests: 1, ToolCalls: t.toolCalls - t.accountedTools,
			ContextUsedTokens: t.contextUsed, ContextWindowTokens: t.contextWindow,
		}, report)
		// The budget adds up what each report billed, never the raw figure: a
		// resumed Claude session reports its whole history's running cost on
		// every turn.
		t.req.guardrail.addCost(billed)
		t.turnCostUSD += billed
		t.usagePayload = t.withCostTotals(evt.Content)
	} else {
		log.Printf("[stream] conv=%s: usage event carries no readable report", convID)
		t.usagePayload = evt.Content
	}
	if t.usageEventID != "" {
		t.userInstructions = 0
		t.accountedTools = t.toolCalls
	}
}

// withCostTotals stamps the usage JSON with the turn's billed cost and the
// conversation's running total. total_cost_usd stays as the provider reported
// it: for Claude that is the session's running figure, which neither answers
// "what did this turn cost" nor survives a compact/reset of the session.
func (t *turnState) withCostTotals(payload string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &fields) != nil || fields == nil {
		return payload
	}
	if raw, err := json.Marshal(t.turnCostUSD); err == nil {
		fields["turn_cost_usd"] = raw
	}
	if t.s.Store != nil && t.req.ConversationID != "" {
		total, err := t.s.Store.ConversationCostUSD(t.ctx, t.req.ConversationID)
		if err != nil {
			log.Printf("[stream] conversation cost conv=%s: %v", t.req.ConversationID, err)
		} else if raw, err := json.Marshal(total); err == nil {
			fields["session_cost_usd"] = raw
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return payload
	}
	return string(out)
}

// persistTool saves each tool call as its own message (role='tool') with the
// raw JSON payload. Saved as soon as the tool fires so an aborted/cancelled run
// still preserves the tools the user actually saw. The persisted id is
// forwarded onto the wire event so the client can dedup against the same row
// that REST/history_backfill will surface later. An empty id signals the save
// failed; the client receives a `persist_failed` heads-up event for that row.
func (t *turnState) persistTool(content string, durationMS *int64) string {
	convID := t.req.ConversationID
	id := t.persist.PersistTool(convID, content, durationMS)
	if id == "" {
		t.broadcast(ServerMessage{
			Type:           "persist_failed",
			ConversationID: convID,
			Message:        "tool result not saved — your view may be stale after a refresh",
		})
	}
	return id
}

// forward ships the event's wire frame, stamped with what persistence
// produced for it.
func (t *turnState) forward(evt agent.StreamEvent, timing toolTiming, toolMsgID string) {
	// The consolidated result frame of an aggregate run goes out after the
	// stream drains; individual chunks would render as separate assistant
	// rows the IM thread never had.
	skipFrame := evt.Subagent && !t.req.ForwardSubagentFrames
	skipFrame = skipFrame || (evt.Kind == agent.KindResult && !evt.Subagent && t.req.Mode == AgentStreamAggregate)
	wsMsg, ok := streamEventToMessage(evt)
	if !ok || skipFrame {
		return
	}
	if evt.Kind == agent.KindToolUseStart {
		wsMsg.ToolStartedAt = timing.startedAt
	}
	if evt.Kind == agent.KindToolResult {
		wsMsg.ToolDurationMS = timing.durationMS
	}
	// Stamp persisted ids onto the events that carry "this content is now in
	// the DB" semantics. The client uses `MessageID` for id-based dedup against
	// REST snapshots and history_backfill frames.
	if evt.Kind == agent.KindToolResult && !evt.Subagent && toolMsgID != "" {
		wsMsg.MessageID = toolMsgID
	}
	if evt.Kind == agent.KindResult && !evt.Subagent {
		t.persistResultFrame(evt.Content, &wsMsg)
	}
	t.broadcast(wsMsg)
}

// persistResultFrame turns one per-result-mode result frame into an assistant
// row and stamps the frame with what was stored.
func (t *turnState) persistResultFrame(content string, wsMsg *ServerMessage) {
	convID := t.req.ConversationID
	text := content
	if text == "" {
		text = t.reply.String()
	}
	var rejected []ArtifactRejection
	text, t.artifacts, rejected = ExtractArtifacts(text, t.req.WorkDir, t.req.ArtifactRootDir, t.req.OwnerID)
	logArtifactRejections(convID, rejected)
	t.rejections = append(t.rejections, rejected...)
	t.finalText = text
	// The per-turn metadata blob: usage tokens/cost and the model id captured
	// at reply time. Persisted onto the assistant row and shipped on the result
	// event so a refreshed chat can render the token chip from REST history
	// alone.
	metadata := t.metadata(t.artifacts)
	ids := t.persist.PersistResult(convID, &t.thinking, text, metadata)
	t.persisted = true
	// The reply is an assistant row now, so the deltas that built it must not
	// reach a late joiner as an in-flight snapshot on top of it. Mirrors the
	// client, which clears its streaming text on every result frame, and
	// PersistResult, which does the same for thinking. Runs mid-turn on codex,
	// which emits one result per step.
	t.reply.Reset()
	if ids.AssistantOK {
		t.notifyText = text
	}
	wsMsg.MessageID = ids.AssistantID
	wsMsg.ThinkingMessageID = ids.ThinkingID
	wsMsg.Metadata = metadata
	wsMsg.Content = text
	if !ids.AssistantOK && text != "" {
		t.broadcast(ServerMessage{
			Type:           "persist_failed",
			ConversationID: convID,
			Message:        "assistant reply not saved — your view may be stale after a refresh",
		})
	}
}

func (t *turnState) metadata(artifacts []Artifact) json.RawMessage {
	return BuildAssistantMetadataFor(AssistantMetadata{
		Usage: t.usagePayload, Model: t.model, Artifacts: artifacts, Origin: t.req.ResultOrigin,
	})
}

// observe runs after a parent-agent frame has gone out.
func (t *turnState) observe(evt agent.StreamEvent, toolMsgID string) {
	// The replay swap runs after the tool frame has been appended, so the
	// snapshot is the last thing a late joiner reads.
	if evt.Kind == agent.KindToolResult && toolMsgID != "" {
		t.s.replaceInFlightReplay(t.req.ConversationID, t.thinking.String(), t.reply.String())
	}
	if t.req.OnFrame != nil {
		t.req.OnFrame(AgentStreamFrame{
			Event:    evt,
			Thinking: t.thinking.String(),
			Reply:    t.reply.String(),
			Result:   t.aggregate.String(),
		})
	}
}

// finish settles the turn once the stream has drained and the run has exited:
// whatever is still unpersisted, the push notification, the error report and
// the rejected-artifact notice.
func (t *turnState) finish(runErr error) AgentStreamResult {
	convID := t.req.ConversationID
	out := AgentStreamResult{
		SessionID:            t.sessionID,
		Err:                  runErr,
		Resident:             t.resident.Load(),
		produced:             t.produced || t.persisted,
		rollbackPath:         t.rollbackPath,
		rollbackOriginalSize: t.rollbackOriginalSize,
	}

	final := t.finalize(runErr)
	if final.persistFailed != nil {
		out.Err = final.persistFailed
	}
	if final.applied {
		t.finalText = final.text
		t.artifacts = final.artifacts
		t.rejections = append(t.rejections, final.rejected...)
		if final.assistantOK {
			t.notifyText = final.text
		}
	}

	// One push notification per turn, fired once the stream has fully drained
	// with the final persisted reply. Detached from ctx so a client disconnect
	// at end-of-turn doesn't drop it. A prompt the user queued mid-turn means
	// the task isn't over yet; the turn that runs it notifies instead.
	if t.req.Notify && t.notifyText != "" &&
		!hasQueuedFollowUp(t.s.Store, convID) {
		go t.persist.MaybeNotify(convID, t.notifyText)
	}

	out.Cancelled = t.s.Broadcaster != nil && t.s.Broadcaster.WasCancelled(convID)
	t.reportRunError(runErr, &out)
	t.reportRejectedArtifacts(runErr, out.Cancelled)

	out.Content = t.finalText
	out.Artifacts = t.artifacts
	out.RejectedArtifacts = t.rejections
	return out
}

// reportRunError persists and broadcasts a failed run unless cancellation is
// expected or the caller is about to retry it.
func (t *turnState) reportRunError(runErr error, out *AgentStreamResult) {
	if runErr == nil {
		return
	}
	convID := t.req.ConversationID
	quiet := t.ctx.Err() != nil
	if quiet {
		// Suppressed on purpose (see below), but not silently: a child that
		// gets OOM-killed moments after a cancel produces a real error on a
		// cancelled ctx, and dropping it without a trace is how "the agent just
		// stopped" incidents end up with no evidence at all.
		log.Printf("[stream] suppressed post-cancel run error conv=%s: %v", convID, runErr)
		return
	}
	if t.req.willRetryTransient != nil && t.req.willRetryTransient(runErr, out.produced) {
		// A retry is coming: reporting this attempt would put one error row
		// per attempt in the transcript for a failure the user never needed
		// to act on. Run reports it if the retries run out.
		out.retryTransient = true
		return
	}
	// Cancellation is an expected control-flow outcome (a newer message
	// supersedes the turn, a client stops it, the run timeout expires) and
	// backends report the terminated child as a noisy command error, so
	// ctx.Err() gates this: never persist or broadcast that implementation
	// detail as a user-visible failure.
	errMsg := runErr.Error()
	errMsgID := ""
	if t.s.Store != nil && convID != "" {
		row := store.Message{
			ID:             uuid.New().String(),
			ConversationID: convID,
			Role:           "error",
			Content:        errMsg,
		}
		if err := t.s.Store.SaveMessage(t.ctx, row); err != nil {
			log.Printf("[stream] save error message conv=%s: %v", convID, err)
		} else {
			errMsgID = row.ID
		}
	}
	t.broadcast(ServerMessage{Type: "error", Message: errMsg, MessageID: errMsgID})
}

// reportRejectedArtifacts tells the user about publish markers that resolved
// to no file. Such a marker leaves no other trace the user can act on: the
// reply above it talks about the file as if it had arrived, and the agent
// cannot see the drop, so the next "I don't see it" gets the identical broken
// marker back. IM threads learn about this from the bridge's thread notice;
// every conversation — including a browser-only one the bridge never touches —
// learns about it here.
//
// Persisted rather than broadcast alone, for the reason persistNotice exists:
// a warning you only receive if you happened to be looking at the tab is
// barely a warning, and this one has to still be there when the user scrolls
// back to ask why the file never came.
func (t *turnState) reportRejectedArtifacts(runErr error, cancelled bool) {
	if len(t.rejections) == 0 || runErr != nil || cancelled {
		return
	}
	failures := make([]string, 0, len(t.rejections))
	for _, rejection := range t.rejections {
		failures = append(failures, rejection.String())
	}
	t.s.persistNotice(t.ctx, t.req.ConversationID, t.broadcast, prompts.ArtifactUploadFailed(failures), nil)
}

type finalizeResult struct {
	applied     bool
	text        string
	artifacts   []Artifact
	rejected    []ArtifactRejection
	assistantOK bool
	// persistFailed is set when the turn's only delivery channel is the
	// aggregate reply and it could not be stored, so the caller must report a
	// failure instead of a stale answer.
	persistFailed error
}

// finalize writes whatever the turn produced that the stream loop has not
// already persisted. In per-result mode that is only the interrupted case (the
// channel closed without a result frame); in aggregate mode it is the whole
// turn.
func (t *turnState) finalize(runErr error) finalizeResult {
	convID := t.req.ConversationID
	aggregate := t.req.Mode == AgentStreamAggregate
	if !aggregate {
		if t.persisted || (t.thinking.Len() == 0 && t.reply.Len() == 0) {
			return finalizeResult{}
		}
	}

	text := t.reply.String()
	if aggregate {
		text = strings.TrimSpace(t.aggregate.String())
		if text == "" {
			text = strings.TrimSpace(t.reply.String())
		}
		// The placeholder stands in for a silent but successful turn. A failed
		// or cancelled run must not claim the agent "returned nothing" — its
		// partial output (if any) is the honest record.
		if text == "" && runErr == nil {
			text = t.req.EmptyResultText
		}
		if text == "" && t.thinking.Len() == 0 {
			return finalizeResult{}
		}
	}

	text, artifacts, rejected := ExtractArtifacts(text, t.req.WorkDir, t.req.ArtifactRootDir, t.req.OwnerID)
	logArtifactRejections(convID, rejected)
	if aggregate && text == "" && runErr == nil {
		text = t.req.EmptyResultText
	}

	metadata := t.metadata(artifacts)
	ids := t.persist.PersistResult(convID, &t.thinking, text, metadata)
	out := finalizeResult{applied: true, text: text, artifacts: artifacts, rejected: rejected, assistantOK: ids.AssistantOK}
	if !aggregate {
		// The interrupted web path ships no result frame: clients pick the
		// partial reply up via REST on their next connect.
		return out
	}
	if runErr != nil {
		return out
	}
	if text != "" && !ids.AssistantOK {
		out.persistFailed = errors.New("保存 IM 回复失败")
		return out
	}
	t.broadcast(ServerMessage{
		Type:              "result",
		Content:           text,
		MessageID:         ids.AssistantID,
		ThinkingMessageID: ids.ThinkingID,
		Metadata:          metadata,
	})
	if t.s.Broadcaster != nil && convID != "" {
		// The reply is in the DB now; a client joining after this point
		// rebuilds it from REST, so leaving the frame in the replay buffer
		// would render it twice.
		t.s.Broadcaster.ResetReplay(convID)
	}
	return out
}
