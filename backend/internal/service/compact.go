package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/prompts"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// CompactSummaryHeader is the prefix every /compact-produced assistant message
// carries. Two reasons to make it visible:
//   - Users scrolling history can tell why a long "claude reply" appears
//     between turns — it wasn't a real reply, it was the compact digest.
//   - The runner's prompt-prepend code picks this same string off the
//     stored summary to label the seed it injects on the next turn so
//     claude itself knows the block is a recap, not a fresh question.
const CompactSummaryHeader = prompts.CompactSummaryHeader

// compactSummarizationPrompt is the instruction we hand claude in the existing
// session; wording lives in internal/prompts.
const compactSummarizationPrompt = prompts.CompactSummarization

// compactRunTimeout caps wall time on a single /compact run. Generous so
// long conversations have room to summarise but not so loose that a stuck
// claude process holds the conversation hostage.
const compactRunTimeout = 5 * time.Minute

// compactTicketTimeout budgets the *wait for an account slot*, which is a
// different failure from compactRunTimeout above: that one bounds a CLI which
// already holds a slot, this one bounds standing in line for one. It has to
// exist because Pool.EnterForUser's queue is an unbounded slice and Ticket.Wait has no
// deadline of its own — a /compact that just parks there keeps its StartJob
// claim forever, so the conversation stays "busy" and the user watches a
// spinner that can never resolve. Two minutes is about one long turn ahead of
// us in the queue: enough that a compact issued right after a normal chat turn
// still goes through, short enough that a genuinely saturated account produces
// an actionable "try again later" instead of an indefinite stall.
const compactTicketTimeout = 2 * time.Minute

// compactTicketWait is compactTicketTimeout everywhere except tests, which
// shrink it so the give-up path can be exercised in milliseconds.
var compactTicketWait = compactTicketTimeout

// compactPersistTimeout caps the two writes that follow a successful summary
// run (message insert + session rotation). Rooted in background rather than
// the request ctx for the same reason compactRunTimeout is — see Compact's
// doc comment — and kept short because by this point the only work left is
// two local SQLite statements.
const compactPersistTimeout = 30 * time.Second

// CompactResult is the /compact success payload. The JSON tags reproduce the
// keys this endpoint has always returned; the handler marshals it directly.
type CompactResult struct {
	Summary          string `json:"summary"`
	NewClaudeSession string `json:"new_claude_session"`
	MessageID        string `json:"message_id"`
}

// Compact runs the /compact use case: ask the conversation's backend (via its
// existing session) for a summary of the conversation so far, persist that
// summary as a regular assistant message so the user still sees it after a
// refresh, then rotate the conversation's session id. The next user message
// therefore starts a fresh CLI session while the digest stays visible in
// history.
//
// Refused while a job is in flight: like /clear-context, kicking the session
// id mid-run would orphan the running agent process from the resume that
// follows. The transport layer hides the affordance; an out-of-date tab that
// calls anyway gets 409.
//
// Gated by the account pool: the summary is a real CLI child, so it queues on
// the same per-account semaphore chat turns use (bounded by
// compactTicketTimeout) rather than spawning outside the limit.
//
// ctx is the caller's request context and is used for the pre-flight reads
// (conversation, user, access check) only — those are cheap and failing them
// early costs nothing. Everything from the summary run onwards deliberately
// runs on its own background-rooted contexts:
//   - RunOneshot gets compactRunTimeout: a browser tab closing mid-summary
//     must NOT kill the CLI, because the run is what produces the summary that
//     the session rotation is paired with. Threading the request ctx in would
//     silently degrade into "user switches page → summary killed → session
//     never rotated".
//   - the message insert + session rotation get compactPersistTimeout, for the
//     same reason one step later: having spent up to five minutes producing a
//     summary specifically so a disconnect couldn't lose it, writing it back on
//     an already-cancelled request ctx would fail with context.Canceled, leave
//     the session unrotated, and return a 500 to nobody. Detaching the run but
//     not its persistence cancels out the whole point.
//
// Keep the request ctx and the background ones separate.
//
// authedUID is the caller resolved at the transport boundary (never a
// *gin.Context down here); "" means "no auth context", the same
// unit-test-bypass convention CanAccessOwner uses.
func (r *PromptRunner) Compact(ctx context.Context, convID, authedUID string) (CompactResult, error) {
	if convID == "" {
		return CompactResult{}, BadRequest("conversation id required")
	}

	conv, err := r.Store.GetConversation(ctx, convID)
	if err != nil {
		return CompactResult{}, StoreError(err, "conversation not found")
	}
	// Per-conversation backend gate. Adapters self-report whether
	// /compact's "run one-shot CLI, stash summary, rotate session"
	// flow is supported via Capabilities().SupportsCompaction.
	// Backends that don't refuse the call here so the frontend's
	// "Compact" affordance never silently corrupts state.
	backend := r.backendFor(conv.Provider)
	if backend == nil || !backend.Capabilities().SupportsCompaction {
		return CompactResult{}, BadRequest("/compact is not available for this conversation's backend")
	}

	// Same guard as /clear-context: reject when the dispatcher pipeline has
	// the room. The frontend hides the action then; an out-of-date tab
	// hitting this gets a clear error rather than corrupting state.
	if r.Broadcaster != nil && r.Broadcaster.IsBusy(convID) {
		return CompactResult{}, Conflict("cannot compact while a request is in flight")
	}

	// Sanity-check the caller can speak for the owning user. Mirrors the
	// access check used elsewhere on conversation routes — agents are
	// reachable through their owner's session, but no cross-owner access.
	if !CanAccessOwner(ctx, r.Store, authedUID, conv.UserID) {
		return CompactResult{}, Forbidden("forbidden")
	}

	user, err := r.Store.GetUser(ctx, conv.UserID)
	if err != nil {
		return CompactResult{}, Internal("user lookup failed", err)
	}

	// Reuse the same workdir + run-options builder the dispatcher pipeline
	// uses so the summary call inherits the conversation's account, sandbox,
	// system prompt, and tool config — anything that affects how the backend
	// interprets the prior session must be identical, otherwise resume could
	// reject the call or interpret the seed differently.
	workDir := r.LockConversationWorkDir(ctx, convID, user.WorkDir)
	// Compact runs the conversation backend one-shot for the summary turn.
	accountName := user.ProviderBindings[conv.Provider]
	var resolvedAccount *config.Provider
	if r.Pool != nil {
		acc, err := ResolveRunAccount(r.Pool, user, conv.Provider, conv.AccountName)
		if err != nil {
			return CompactResult{}, NewServiceError(http.StatusBadRequest, err.Error(), err)
		}
		resolvedAccount = acc
		accountName = acc.Name
	}
	// Same gate the prompt path applies: a conversation whose model an admin
	// withdrew cannot be summarised on it either, and silently compacting on a
	// substitute would rewrite the thread's memory using a model the user
	// never picked.
	if modelErr := CheckModelAvailable(r.Cfg, conv.Provider, accountName, conv.Model); modelErr != nil {
		return CompactResult{}, modelErr
	}
	opts := r.BuildRunOptions(ctx, convID, workDir, &user, resolvedAccount)
	defer func() {
		// BuildRunOptions writes a temp file for MCP config; clean up so
		// repeated /compact calls don't accumulate detritus in /tmp.
		if opts.McpConfigPath != "" {
			_ = os.Remove(opts.McpConfigPath)
		}
	}()
	if opts.SessionID == "" {
		return CompactResult{}, BadRequest("no backend session yet — send a message first")
	}
	if summaryModel := ConfiguredSummaryModel(r.Cfg, conv.Provider, accountName); summaryModel != "" {
		if IsValidModelForAccount(r.Cfg, conv.Provider, accountName, summaryModel) {
			opts.Model = summaryModel
		} else {
			log.Printf("[compact] summary model %q is not available for provider=%s account=%s; using conversation model %q", summaryModel, conv.Provider, accountName, opts.Model)
		}
	}

	// Admission, the same one every turn path goes through:
	//   - the operator pause and a drain refuse it: /compact forks a full agent
	//     CLI, and the drainer entry is what makes a restart wait for a summary
	//     already running instead of killing the rotation it ends with;
	//   - the room is marked busy for the duration so a sibling /clear-context
	//     or another /compact bounces with 409 rather than racing us;
	//   - an account-pool slot is reserved before spawning anything. RunOneshot
	//     below forks a full agent CLI (~1GB resident in production), and this
	//     path used to resolve the account without ever entering the pool — so
	//     every /compact added a child the per-account concurrency limit never
	//     counted, which is one of the ways this box reached OOM. Entering also
	//     applies the rate-limit cooldown, so a throttled account refuses the
	//     compact instead of burning a request into a live 429.
	//
	// The slot wait is rooted in Background, not ctx, for the same reason the
	// run below is: a tab that navigates away must not abort a compact that is
	// about to produce the summary the session rotation is paired with.
	// compactTicketWait is what keeps that from becoming an unbounded park.
	jobOwner := user
	if owner, ownerErr := r.Store.GetOwner(ctx, user); ownerErr == nil {
		jobOwner = owner
	}
	lease, err := r.admission().Admit(AdmitSpec{
		ConversationID: convID,
		Target: TurnTarget{
			Job: DrainerJob{
				UserID:         jobOwner.ID,
				Username:       jobOwner.Username,
				ProviderType:   conv.Provider,
				AccountName:    accountName,
				ConversationID: convID,
				StartedAt:      time.Now().UTC(),
			},
			PoolUserID:  user.ID,
			AccountName: accountName,
			Model:       opts.Model,
		},
		QueueCtx:        context.Background(),
		QueueTimeout:    compactTicketWait,
		ActivityOwnerID: jobOwner.ID,
		Describe:        compactAdmissionError,
	})
	if err != nil {
		return CompactResult{}, err
	}
	// Every exit below this point goes through here, including the
	// BadGateway/Internal returns, so the slot can't leak on a failed run.
	defer lease.Release()

	// Independent of ctx on purpose — see the doc comment. A disconnected
	// client must not abort a summary that is already running.
	runCtx, cancel := context.WithTimeout(context.Background(), compactRunTimeout)
	defer cancel()
	summary, runErr := backend.RunOneshot(runCtx, compactSummarizationPrompt, workDir, opts)
	if runErr != nil {
		log.Printf("[compact] runner conv=%s: %v", convID, runErr)
		return CompactResult{}, BadGateway("backend run failed: "+runErr.Error(), runErr)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return CompactResult{}, BadGateway("backend returned an empty summary", nil)
	}

	// Persist the summary as an assistant message so it shows up in chat
	// history and a refreshed tab still sees what was preserved. The
	// header is the marker the runner re-uses when prepending to the next
	// user prompt.
	displayContent := CompactSummaryHeader + "\n" + summary
	asMsg := store.Message{
		ID:             uuid.New().String(),
		ConversationID: convID,
		Role:           "assistant",
		Content:        displayContent,
	}
	// Independent of ctx on purpose — see the doc comment. The client that
	// asked for this compact is very likely gone by now (that is the whole
	// scenario runCtx defends against); the summary and the rotation still
	// have to land.
	persistCtx, persistCancel := context.WithTimeout(context.Background(), compactPersistTimeout)
	defer persistCancel()
	if err := r.Persist.SaveMessageWithRetry(persistCtx, asMsg); err != nil {
		log.Printf("[compact] save summary conv=%s: %v", convID, err)
		return CompactResult{}, Internal("save summary failed", err)
	}

	// Rotate the session_id so the next message starts a fresh CLI
	// session. The summary above stays as an assistant message in
	// history — the user can scroll up to recall what was preserved —
	// but is NOT auto-injected into the next prompt's context. If the
	// user wants the model to be aware of it, they reference it
	// themselves in their next message.
	//
	// This is the design-doc behaviour: we deliberately gave up the
	// "silently prepend summary to the next user message" trick in
	// exchange for dropping a state column from the DB.
	//
	// The DB also clears last_context_usage as part of the reset.
	//
	// Deliberately the raw store call, not ConversationOps.ResetSession (the
	// canonical home of the /clear-context rotation): we are inside our own
	// StartJob claim here, so that method's IsBusy guard would bounce us with
	// 409 against ourselves. We also need the new session id it doesn't
	// return, and ownership was already checked above.
	newSessionID, err := r.Store.ResetConversationSession(persistCtx, convID)
	if err != nil {
		log.Printf("[compact] rotate session conv=%s: %v", convID, err)
		return CompactResult{}, Internal("rotate session failed", err)
	}

	// Wipe the in-memory context bar so peer tabs reflect the reset
	// instantly. Same call /clear-context makes after rotation. Ordering
	// matters: this must follow ResetConversationSession, otherwise a peer
	// tab can repopulate the bar from the pre-rotation row.
	if r.Broadcaster != nil {
		r.Broadcaster.ClearContextUsage(convID)
		// Live-broadcast the new assistant message to anyone subscribed so
		// the summary appears without waiting for a manual REST refresh.
		data, err := json.Marshal(ServerMessage{
			Type:      "result",
			Content:   displayContent,
			MessageID: asMsg.ID,
		})
		if err == nil {
			r.Broadcaster.Broadcast(convID, data)
		}
	}

	return CompactResult{
		Summary:          summary,
		NewClaudeSession: newSessionID,
		MessageID:        asMsg.ID,
	}, nil
}

// compactAdmissionError is /compact's wording for an admission refusal.
func compactAdmissionError(err error) error {
	var cooling *CooldownError
	var entry *PoolEntryError
	var timeout *QueueTimeoutError
	switch {
	case errors.Is(err, ErrTurnPaused):
		return NewServiceError(http.StatusServiceUnavailable,
			"new tasks are paused by an administrator; try /compact again shortly", err)
	case errors.Is(err, ErrTurnDraining):
		return NewServiceError(http.StatusServiceUnavailable,
			"service is restarting; try /compact again shortly", err)
	case errors.Is(err, ErrTurnBusy):
		return Conflict("cannot compact while a request is in flight")
	case errors.As(err, &cooling):
		return NewServiceError(http.StatusTooManyRequests, cooling.Error(), err)
	case errors.As(err, &entry):
		return BadRequest("cannot reserve an account slot: " + entry.Err.Error())
	case errors.As(err, &timeout):
		return NewServiceError(http.StatusServiceUnavailable,
			"account \""+timeout.Account+"\" has no free slot: /compact stopped waiting after "+
				timeout.Waited.String()+" — try again once your other runs finish", err)
	}
	return err
}
