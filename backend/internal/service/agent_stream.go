package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AgentStreamMode selects how KindResult frames become assistant rows.
type AgentStreamMode int

const (
	// AgentStreamPerResult persists one assistant row per KindResult frame and
	// ships a `result` frame for each. This is the web transcript model: the
	// conversation is an append-only stream, so a codex turn that emits several
	// agent_message frames renders as several rows, each with its own id.
	AgentStreamPerResult AgentStreamMode = iota
	// AgentStreamAggregate buffers every KindResult frame and persists a single
	// assistant row once the stream drains. An IM thread has exactly one
	// delivery slot (the reply message), so the whole turn must collapse into
	// one text — splitting it would post only the last fragment to the channel.
	AgentStreamAggregate
)

// AgentStreamFrame is handed to AgentStreamRequest.OnFrame for every
// parent-agent event, after the streamer has folded it into its buffers. The
// accumulated text is exposed so a transport can render a live progress
// preview without keeping a second copy of the stream.
type AgentStreamFrame struct {
	Event agent.StreamEvent
	// Thinking is everything KindThinkingDelta has produced so far.
	Thinking string
	// Reply is everything KindDelta has produced so far.
	Reply string
	// Result is everything KindResult has produced so far. Only populated in
	// AgentStreamAggregate mode, where result frames are buffered rather than
	// persisted one by one.
	Result string
}

// AgentStreamResult is the outcome of one consumed run.
type AgentStreamResult struct {
	// Content is the final parent-agent reply after artifact extraction. In
	// per-result mode it is the last result frame; in aggregate mode the whole
	// buffered turn.
	Content   string
	Artifacts []Artifact
	// RejectedArtifacts lists the publish markers that produced no file. The
	// caller reports them to the user: a dropped artifact is invisible to the
	// agent, which otherwise keeps answering as if the file had been attached.
	RejectedArtifacts []ArtifactRejection
	// SessionID is the id the backend reported through system_init, empty when
	// it never named one.
	SessionID string
	// Err is the backend's exit error, nil on a clean run.
	Err error
	// Cancelled reports whether the run was aborted by an explicit
	// Broadcaster.CancelJob (as opposed to a drain or a crash).
	Cancelled bool
	// Resident reports that the backend kept its process alive past this
	// turn. The session log therefore still has a live writer.
	Resident bool

	// produced reports whether this attempt put anything in front of the
	// user — streamed text, a tool row, a question. A transient failure is
	// only safe to retry when nothing was produced; otherwise the retry
	// would duplicate the part that already landed.
	produced bool
	// retryTransient is set when runOnce withheld the error report because
	// the caller signalled it intends to retry this failure.
	retryTransient bool
	// rollbackPath / rollbackOriginalSize carry this attempt's session-log
	// snapshot so a retry can erase the failed attempt's lines before
	// resuming — otherwise --resume replays the prompt twice.
	rollbackPath         string
	rollbackOriginalSize int64
}

// AgentStreamRequest describes one agent turn to run and consume.
type AgentStreamRequest struct {
	// willRetryTransient, when set, is consulted on a failed run. Returning
	// true suppresses the user-visible error report so Run can retry without
	// leaving one error row per attempt in the transcript.
	willRetryTransient func(err error, produced bool) bool

	Backend agent.Backend
	Prompt  string
	WorkDir string
	// ArtifactRootDir is the base used by /api/users/:id/files URLs. It may
	// be an ancestor of WorkDir when a conversation runs from a nested cwd;
	// empty defaults to WorkDir for callers such as the IM bridge.
	ArtifactRootDir string
	ConversationID  string
	// OwnerID attributes token usage rows and owns extracted artifacts.
	OwnerID string
	// AccountName scopes the rate-limit cooldown applied when the backend
	// reports a denial. Empty disables the cooldown (no account to gate).
	AccountName string
	Opts        agent.RunRequest

	Mode AgentStreamMode
	// ForwardSubagentFrames mirrors sub-agent (Task tool) frames to Broadcast.
	// The web chat renders them as nested activity; the IM bridge leaves this
	// off because its single progress message has no room to interleave a
	// worker's chatter with the parent's reply. Sub-agent frames never feed the
	// persistence buffers either way.
	ForwardSubagentFrames bool
	// EmptyResultText is persisted and returned when a *successful* turn
	// produced no text at all. Only meaningful in aggregate mode, where the
	// transport must post something into the thread. Empty means "persist
	// nothing", which is what the web transcript wants.
	EmptyResultText string
	// Notify fires the owner's push notification when the turn needs their
	// answer and once more when the final reply is persisted. IM runs leave it
	// off: the thread itself is the delivery, and Bark/PushDeer would duplicate
	// those messages.
	Notify bool

	// SlotGate, when set, lets the turn hand its account-pool slot back while
	// it is blocked on a provider question and re-queue for one when the answer
	// arrives.
	SlotGate *TurnSlotGate

	// Unbounded exempts this turn from guardrail accounting and reminders.
	// Case-mode IM threads set it because their context is already bounded by
	// the case and session rotation.
	Unbounded bool
	// UserInitiated marks a real inbound user message. Only these turns consume
	// the previous task's reminder and produce a reminder for the next message;
	// cron and provider-initiated wakeups remain silent.
	UserInitiated bool

	// AllowResidency lets the backend keep its process alive past this turn
	// when the turn leaves background work running, so the provider can
	// deliver a later turn on its own. Web chat sets it; the IM bridge does
	// not — an IM thread's reply slot belongs to the inbound message, and an
	// unprompted turn would have nowhere to go.
	AllowResidency bool
	// Attach replaces the launch step: when non-nil, Run consumes events the
	// backend is already producing instead of calling RunWithSession. Used by
	// provider-initiated turns, whose process an earlier turn started.
	Attach func(ctx context.Context, outputCh chan<- agent.StreamEvent) error
	// Wake supplies somewhere to put a turn the provider starts on its own.
	// Only consulted when AllowResidency is set; without it a backend has no
	// reason to keep a process alive, since nothing could consume its output.
	Wake func() (chan<- agent.StreamEvent, func(error))
	// ResidentActivity receives the background lifecycle independently from
	// the foreground turn. The conversation rail uses it to stay animated
	// after the prompt itself has reached ready.
	ResidentActivity func(active bool)
	// ResultOrigin marks how this turn began, and rides along on the assistant
	// row's metadata so a refresh sees it too. Empty means "the user asked".
	ResultOrigin string

	// Broadcast ships one wire frame to the conversation room. nil drops them.
	Broadcast func(ServerMessage)
	// OnFrame observes every parent-agent event. Optional.
	OnFrame func(AgentStreamFrame)
	// OnGuardrail mirrors a non-blocking reminder to an attached transport such
	// as an IM thread. Web delivery uses the regular persisted notice.
	OnGuardrail func(GuardrailSnapshot)

	guardrail *taskGuardrail
}

// AgentStreamer consumes one agent run's event stream. It is the single place
// that knows how a stream of agent.StreamEvent turns into durable state: token
// usage accounting, per-model rate-limit cooldowns, session-id capture, tool /
// thinking / assistant rows, artifact extraction, and the session-log rollback
// needed before retrying a failed attempt. Both the web chat
// (PromptRunner) and the IM bridge drive it; every place they legitimately
// differ is a documented field on AgentStreamRequest rather than a second copy
// of the loop.
type AgentStreamer struct {
	Store       store.Store
	Broadcaster *Broadcaster
	// Pool receives rate-limit cooldowns. Optional.
	Pool *Pool
	// Persist owns the DB writes. Optional; a nil persister is replaced by a
	// minimal one built from Store/Broadcaster so focused tests can omit it.
	Persist *MessagePersister
	// Guardrails is copied from the process config. A zero value disables all
	// reminder thresholds, which keeps focused tests and side streams opt-in.
	Guardrails config.GuardrailsConfig
	// GuardrailReminders carries a completed task's threshold reminder to the
	// next user message in the same conversation.
	GuardrailReminders *GuardrailReminderTracker
}

// deliverAnswerAfterResume re-enters the account queue on the parked turn's
// behalf and only then hands the answer to the provider, so a reply typed while
// the account is saturated waits its turn instead of running over the limit.
func (s *AgentStreamer) deliverAnswerAfterResume(
	ctx context.Context,
	req AgentStreamRequest,
	backend agent.UserQuestionBackend,
	controlID, requestID string,
	answers map[string][]string,
	broadcast func(ServerMessage),
) {
	if err := req.SlotGate.Resume(ctx); err != nil {
		if errors.Is(err, errTurnGone) || ctx.Err() != nil {
			return
		}
		// The answer arrived but the account never freed a slot for it. Left
		// alone the turn would block forever with a live child process, so say
		// why and end it rather than leak a process nobody is watching.
		broadcast(ServerMessage{Type: "error", Message: err.Error()})
		if s.Broadcaster != nil {
			s.Broadcaster.CancelJob(req.ConversationID)
		}
		return
	}
	if err := backend.AnswerUserQuestion(ctx, controlID, requestID, answers); err != nil {
		broadcast(ServerMessage{Type: "error", Message: err.Error()})
	}
}

func (s *AgentStreamer) persister() *MessagePersister {
	if s.Persist != nil {
		return s.Persist
	}
	return &MessagePersister{Store: s.Store, Broadcaster: s.Broadcaster}
}

// runOnce executes the request's backend once and consumes its stream to
// completion. It returns only after the child process has exited, so callers
// may safely touch the session log afterwards. Run wraps it with the
// transient-failure retry loop; turnState holds what the attempt accumulates
// and how each event kind is folded in.
func (s *AgentStreamer) runOnce(ctx context.Context, req AgentStreamRequest) AgentStreamResult {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	turn := s.newTurn(ctx, req)
	turn.registerControls()
	turn.grantResidency()
	turn.snapshotSessionLog()

	outputCh, errCh := turn.launch(runCtx)
	for evt := range outputCh {
		turn.handle(evt)
	}
	// Wait for the turn to finish before touching its session log — otherwise
	// a late flush could land after our truncate and leave a half-line at the
	// tail. For a non-resident turn RunWithSession has called cmd.Wait by the
	// time errCh drains, so we are then the sole writer. A resident turn is
	// the exception: its process is still alive and still holds the log, which
	// is why the retry rollback is gated on AgentStreamResult.Resident.
	return turn.finish(<-errCh)
}

// persistNotice records something the turn did to the user rather than for
// them, and that nothing else in the transcript will show: background work
// stopped rather than kept, a transient failure being retried, a published file
// that never became an attachment.
//
// It lands as a persisted error row rather than a transient frame on purpose:
// what these have in common is that the evidence is otherwise absent, so
// learning about it only if you happened to be looking at the tab is barely
// better than not learning about it. For the residency case it is also the only
// signal an operator gets that the resident cap is too low for the workload.
func (s *AgentStreamer) persistNotice(ctx context.Context, convID string, broadcast func(ServerMessage), reason string, notice *Notice) {
	if convID == "" || reason == "" {
		return
	}
	msgID := ""
	metadata := noticeMetadata("", notice)
	if s.Store != nil {
		row := store.Message{
			ID:             uuid.New().String(),
			ConversationID: convID,
			Role:           "error",
			Content:        reason,
			Metadata:       metadata,
		}
		// Detached from ctx: a notice most often fires as the turn unwinds,
		// and a cancelled turn is exactly when the user most needs to be told
		// their background work went with it.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.Store.SaveMessage(saveCtx, row); err != nil {
			log.Printf("[stream] save residency notice conv=%s: %v", convID, err)
		} else {
			msgID = row.ID
		}
	}
	broadcast(ServerMessage{Type: "error", Message: reason, MessageID: msgID, Metadata: metadata})
}

// persistSessionWarning records a non-fatal turn warning without treating it
// as the end of the turn. The error-compatible history row makes the warning
// survive reloads; its metadata restores warning severity after a refresh,
// while the dedicated live frame avoids a fatal-error state transition.
func (s *AgentStreamer) persistSessionWarning(ctx context.Context, convID string, broadcast func(ServerMessage), reason string, notice *Notice) {
	if convID == "" || reason == "" {
		return
	}
	msgID := ""
	metadata := noticeMetadata("session_warning", notice)
	if s.Store != nil {
		row := store.Message{
			ID:             uuid.New().String(),
			ConversationID: convID,
			Role:           "error",
			Content:        reason,
			Metadata:       metadata,
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.Store.SaveMessage(saveCtx, row); err != nil {
			log.Printf("[stream] save session warning conv=%s: %v", convID, err)
		} else {
			msgID = row.ID
		}
	}
	broadcast(ServerMessage{Type: "session_warning", Message: reason, MessageID: msgID, Metadata: metadata})
}

// replaceInFlightReplay swaps the broadcaster's replay buffer for a
// consolidated snapshot of everything the current turn has produced but not yet
// persisted: the reasoning so far, then the reply text so far.
//
// Persisting a tool call drops the replay buffer (see ResetReplay) because its
// frames are now redundant with REST history. The thinking and reply deltas
// that streamed alongside them are not: neither is persisted until the turn's
// result frame lands, so dropping them leaves a client that joins mid-turn with
// nothing to show for however long the turn has been running — no reasoning, no
// narration between tool calls. That is what made the same conversation look
// different in two browsers: the tab that watched the turn live had text the
// other could never obtain. In simple view mode, where activity rows are hidden
// anyway, the late tab showed an empty transcript.
//
// The snapshot mirrors what a live client holds, so the two converge: both
// buffers accumulate from the last result frame (which resets them here and
// clears the corresponding rows on the client), and both render as one thinking
// row plus one stream row. Frames are ordered thinking-then-reply because a
// turn reasons before it speaks.
func (s *AgentStreamer) replaceInFlightReplay(convID, thinking, reply string) {
	if s.Broadcaster == nil || convID == "" {
		return
	}
	snapshots := make([][]byte, 0, 2)
	for _, frame := range []ServerMessage{
		{Type: "thinking_delta", Content: thinking, ConversationID: convID},
		{Type: "delta", Content: reply, ConversationID: convID},
	} {
		if frame.Content == "" {
			continue
		}
		data, err := json.Marshal(frame)
		if err != nil {
			s.Broadcaster.ResetReplay(convID)
			return
		}
		snapshots = append(snapshots, data)
	}
	if len(snapshots) == 0 {
		s.Broadcaster.ResetReplay(convID)
		return
	}
	s.Broadcaster.ReplaceReplay(convID, snapshots...)
}

// appendAggregateChunk joins one backend result frame onto the turn's aggregate
// reply while preserving line boundaries. codex closes every step with its own
// result frame, so a raw concatenation welds the next step's first line onto
// the previous step's last one. That silently destroys a publish marker sitting
// at the end of a step — ExtractArtifacts matches whole lines, so the welded
// line stops being a marker, the file is never uploaded, and the raw
// DAYMUG_ARTIFACT text leaks into the answer instead.
func appendAggregateChunk(buf *strings.Builder, chunk string) {
	if chunk == "" {
		return
	}
	if existing := buf.String(); existing != "" && !strings.HasSuffix(existing, "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(chunk)
}

func toolTimingKey(subagent bool, content string) string {
	track := "parent:"
	if subagent {
		track = "subagent:"
	}
	var payload struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(content), &payload) == nil && payload.ID != "" {
		return track + payload.ID
	}
	return track + "anonymous"
}

func logArtifactRejections(convID string, rejected []ArtifactRejection) {
	for _, rejection := range rejected {
		log.Printf("[stream] ignore invalid agent artifact conv=%s: %s", convID, rejection.LogLine())
	}
}

func rollbackSessionLog(convID, path string, originalSize int64) {
	if originalSize >= 0 {
		if err := os.Truncate(path, originalSize); err != nil && !os.IsNotExist(err) {
			log.Printf("[stream] rollback truncate conv=%s path=%s size=%d: %v",
				convID, path, originalSize, err)
		}
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[stream] rollback remove conv=%s path=%s: %v", convID, path, err)
	}
}

// applyRateLimitCooldown puts the account into a Pool cooldown only when the
// CLI's rate_limit_event reports the limit is actually *reached* — a
// "blocked"/"rejected" frame. The CLI also emits frames that still allow the
// request: "allowed" (plain "here's your window" heartbeat) and the
// approaching-the-cap warnings ("allowed_warning"/"warning"). Those still have
// quota, so cooling the account down on them would lock the user out of
// capacity they're entitled to — which is exactly the bug this guards against.
// resets_at is the unix second the window reopens; until then new turns on the
// rejected model are refused so the user stops retrying into a live limit (the
// behaviour that escalates toward a ban). model scopes the cooldown so a denial
// on one model doesn't lock the user out of another that still has quota on the
// same account. Best-effort: a malformed payload or missing reset time is
// ignored.
func applyRateLimitCooldown(pool *Pool, accountName, model, content string) {
	if pool == nil || accountName == "" || content == "" {
		return
	}
	var rl struct {
		Status   string `json:"status"`
		ResetsAt int64  `json:"resets_at"`
	}
	if err := json.Unmarshal([]byte(content), &rl); err != nil {
		return
	}
	if rl.ResetsAt == 0 || !rateLimitDenied(rl.Status) {
		return
	}
	pool.Cooldown(accountName, model, time.Unix(rl.ResetsAt, 0))
}

// rateLimitDenied reports whether a rate_limit_event status means the request
// was actually denied (quota exhausted) rather than merely allowed or warned.
// Only a denial gates the account; warnings ("allowed_warning"/"warning") and
// any "allowed*" status keep quota and must not trigger a cooldown. Unknown
// statuses are treated as non-denial so a future heartbeat variant can't lock
// the user out of capacity they still have.
func rateLimitDenied(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "blocked", "rejected":
		return true
	default:
		return false
	}
}
