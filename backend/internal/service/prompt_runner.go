package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/userenv"
)

// promptQueueTimeout budgets the *wait for an account slot* on the web and
// cron paths, the way imQueueTimeout does for IM and compactTicketTimeout does
// for /compact.
//
// It has to exist because the run context these paths use is deliberately
// rooted in context.Background() (see ProcessPrompt: a server drain must not
// kill a reply that is already streaming). That choice also left the *queueing*
// phase with no deadline at all, so only an explicit user cancel could ever
// unpark a ticket. A scheduled task that overruns its interval then stacks one
// permanently-parked ticket per fire ahead of every human prompt, each pinning
// a Drainer in-flight entry — interactive users starve and graceful shutdown
// can never complete.
//
// 30 minutes matches the IM path's budget: long enough that a prompt sent while
// a few colleagues' turns are running still goes through, short enough that a
// saturated account produces an actionable error instead of an infinite spinner.
const promptQueueTimeout = 30 * time.Minute

// promptQueueWait is promptQueueTimeout everywhere except tests, which shrink
// it so the give-up path can be exercised in milliseconds.
var promptQueueWait = promptQueueTimeout

// PromptRunner owns the provider-agnostic orchestration of a single user
// prompt: pool-slot acquisition, the pending→processing claim, queue-position
// broadcasts, running the agent CLI, and persisting every tool/result/error
// along the way. It is detached from any individual WebSocket; production
// builds it from the shared Runtime (Runtime.PromptRunner).
type PromptRunner struct {
	Store       store.Store
	Broadcaster *Broadcaster
	UserHub     *UserHub
	// Drainer tracks in-flight jobs for graceful shutdown. nil-safe.
	Drainer *Drainer
	// Pause is the operator's "stop starting new work" switch. ProcessPrompt
	// never sees a paused prompt (the Dispatcher holds it), but /compact
	// bypasses the dispatcher and is refused through the admission. nil-safe.
	Pause *PauseGate
	// Pool is the per-account concurrency limiter. Optional in tests; when
	// nil all jobs run unthrottled (no queueing).
	Pool *Pool
	// Dispatcher is consulted to register per-message pre-claim cancels so
	// a staged prompt recalled while parked on the pool queue releases its
	// slot promptly. Optional.
	Dispatcher *Dispatcher
	// Sandbox optionally wraps the spawned agent process to constrain it
	// to the user's workdir. When nil the CLI runs as the server user.
	Sandbox agent.Sandbox
	// Cfg is the live server config. Optional in focused tests.
	Cfg *config.Config
	// Persist owns the persistence/notification side effects of the turn.
	Persist *MessagePersister
	// GuardrailReminders carries a completed task's soft reminder to the next
	// user message in the same conversation.
	GuardrailReminders *GuardrailReminderTracker
	// Backends resolves the agent runner for a conversation's provider id.
	Backends *BackendRegistry
	// Observer mirrors web-chat prompt lifecycle events to an optional attached
	// transport. IM-originated runs use their own bridge and never enter this
	// dispatcher, so observing here cannot echo those messages back twice.
	Observer PromptObserver
}

type promptRunContext struct {
	Conversation store.Conversation
	User         store.User
	JobOwner     store.User
}

type promptAccountContext struct {
	ProviderType    string
	AccountName     string
	ResolvedAccount *config.Provider
}

// backendFor resolves a provider id through the registry. Nil-safe for test
// wiring that never reaches a run.
func (r *PromptRunner) backendFor(provider string) agent.Backend {
	return r.Backends.For(provider)
}

// ProcessPrompt is the dispatcher's per-prompt callback. It owns the full
// claude lifecycle for a single user message: pool-slot acquisition, the
// atomic claim that flips the row from 'pending' to 'processing' (so the
// staging-area UI is only updated once the prompt is really running),
// the prompt_started broadcast, peer-tab echo, broadcast of "thinking",
// and running the claude CLI with persistence of every tool/result/error
// along the way.
//
// The dispatcher hands us a row that is still 'pending'. We do NOT flip
// it to 'processing' or fire prompt_started until after we've actually
// acquired a per-account pool slot — otherwise a queued conversation
// would prematurely move out of the staging area and the user would see
// their prompt go silent without any visible progress (the original bug
// behind this code path).
//
// dispatcherCtx is cancelled on graceful shutdown. The actual claude run
// uses its own context.Background()-rooted reqCtx so a server drain
// doesn't kill an in-flight reply mid-stream — the Drainer + Broadcaster
// pair is what enforces graceful shutdown of the run itself.
func (r *PromptRunner) ProcessPrompt(dispatcherCtx context.Context, prompt store.Message) {
	convID := prompt.ConversationID
	if convID == "" {
		return
	}

	// Resolve the conversation owner so the run inherits the right
	// account / system prompt / tool config / sandbox bind list. A
	// missing user/conversation row is the symptom of a deletion racing
	// the dispatcher; surface a friendly error to any connected client
	// rather than silently swallowing the prompt.
	runCtx, err := r.resolvePromptRunContext(convID)
	if err != nil {
		log.Printf("[dispatcher] %v", err)
		if errors.Is(err, errPromptConversationMissing) {
			r.broadcastError(convID, "conversation no longer exists")
			return
		}
		r.broadcastError(convID, "user no longer exists")
		return
	}
	conv := runCtx.Conversation
	user := runCtx.User

	// Run is intentionally rooted in a fresh background ctx, not the
	// dispatcher ctx: a server drain shouldn't kill an in-flight claude
	// reply (doing so previously left its session id locked, so the next
	// message hit "Session ID is already in use"). Broadcaster.StartJob
	// registers the cancel func so any tab — including a fresh one
	// after reconnect — can abort via the "cancel" message.
	reqCtx, rc := context.WithCancel(context.Background())

	// Observe BEFORE registering the job. An observer may hold a
	// cross-surface lock (the IM bridge serialises a mirrored thread this
	// way), and the inbound IM path takes that lock before its own
	// StartJob. Registering the job first inverted the order: whichever
	// surface won the race forced the other's StartJob to fail — on this
	// side the prompt was then dropped without a reply, on the IM side the
	// user got a bogus "already running". One order for both paths
	// (observer lock → job → pool slot) removes the race entirely.
	var observation PromptObservation
	if r.Observer != nil {
		observation = r.Observer.ObservePrompt(reqCtx, prompt)
		if observation != nil {
			defer observation.Close()
		}
	}

	// Register pending-row cancellation before contending for the room. A
	// provider-initiated wakeup can own it independently of the dispatcher's
	// per-conversation worker; while we wait, recalling this specific prompt
	// must still unblock ProcessPrompt immediately.
	preClaimCtx, preClaimCancel := context.WithCancel(reqCtx)
	defer preClaimCancel()
	if r.Dispatcher != nil {
		r.Dispatcher.RegisterPreClaim(prompt.ID, preClaimCancel)
		defer r.Dispatcher.UnregisterPreClaim(prompt.ID)
	}

	broadcastFn := r.broadcast

	// Admission: room → drainer (queued) → account-pool slot → running. The
	// room is claimed by waiting rather than refusing: wakeup turns are
	// launched by the resident adapter, not this worker, so they can own the
	// room while a new durable user prompt arrives. Returning there used to
	// make runWorker sweep the prompt to done, producing "another run is
	// already in progress" and losing it from the queue.
	//
	// The pause gate is skipped because the Dispatcher already holds paused
	// prompts pending; one that reached this point was let through before.
	//
	// The slot wait is bounded by promptQueueWait and can be cut short by
	// either reqCtx (broad cancel via Broadcaster.CancelJob) or preClaimCancel,
	// the per-message cancel the dispatcher fires when this specific prompt is
	// recalled from the staging area while parked. Without the latter the pool
	// ticket would linger (the processor would only discover the deletion on
	// claim, after the slot was already granted), and any sibling waiter's
	// broadcast positions would report a stale queue depth — the bug behind
	// "two tabs both show 'next up' after one was cancelled".
	var accountCtx promptAccountContext
	lease, admitErr := r.admission().Admit(AdmitSpec{
		ConversationID:    convID,
		Cancel:            rc,
		WaitForRoom:       preClaimCtx,
		PauseHeldUpstream: true,
		// Resolve which provider this job runs against. The conversation's
		// stored `provider` tells us the CLI type; the user's per-type binding
		// picks the named account within that type. The binding is mandatory:
		// a user with no binding for this CLI type is refused here. There is no
		// implicit fallback to the "default" provider — that fallback used to
		// funnel every unbound user onto the shared default ~/.claude login,
		// which then tripped Anthropic's rate limit.
		Resolve: func() (TurnTarget, error) {
			resolved, err := r.resolvePromptAccount(conv, user)
			if err != nil {
				return TurnTarget{}, err
			}
			// Re-check the stored model against what the account offers right
			// now. Create/update validation only gates the picker, so an admin
			// unchecking a model left every conversation already pinned to it
			// running that model forever. Refused before the pool ticket: a
			// turn that cannot legally run must not occupy a slot or a drainer
			// entry on the way to failing.
			if modelErr := CheckModelAvailable(r.Cfg, conv.Provider, resolved.AccountName, conv.Model); modelErr != nil {
				return TurnTarget{}, modelErr
			}
			accountCtx = resolved
			return TurnTarget{
				Job: DrainerJob{
					UserID:         runCtx.JobOwner.ID,
					Username:       runCtx.JobOwner.Username,
					ProviderType:   resolved.ProviderType,
					AccountName:    resolved.AccountName,
					ConversationID: convID,
					StartedAt:      time.Now().UTC(),
				},
				PoolUserID:  user.ID,
				AccountName: resolved.AccountName,
				Model:       conv.Model,
			}, nil
		},
		QueueCtx:        preClaimCtx,
		QueueTimeout:    promptQueueWait,
		ActivityOwnerID: runCtx.JobOwner.ID,
		TrackAttention:  true,
		SkipDone:        conv.SourceType == "cron",
		// A restart ends a turn parked on a question instead of waiting for
		// the answer. Keeping the question lets the card come back after the
		// restart; answering it then continues the session as a new prompt.
		OnSuspend: func(payload string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.Store.SetConversationSuspendedQuestion(ctx, convID, payload); err != nil {
				log.Printf("[prompt] save suspended question conv=%s: %v", convID, err)
				return
			}
			log.Printf("[prompt] suspended conv=%s on an unanswered question for the drain", convID)
		},
		// Position updates are broadcast to every connected client in the room
		// as queue_status events while we wait — the staging-area UI keeps the
		// prompt visible (still 'pending') and renders a "queued" hint. The
		// relay ends when the ticket's position channel closes.
		OnTicket: func(ticket *Ticket, _ bool) {
			go relayQueuePositions(ticket, convID, broadcastFn)
		},
		OnRefused: func(err error) {
			switch {
			case errors.Is(err, ErrTurnDraining):
				broadcastFn(convID, ServerMessage{Type: "status", Status: "shutdown"})
			case errors.Is(err, context.Canceled):
				broadcastFn(convID, ServerMessage{Type: "status", Status: "ready"})
			default:
				r.Drainer.MarkConversationFailed(convID)
				broadcastFn(convID, r.persistError(convID, webAdmissionMessage(err)))
			}
		},
		Describe: webAdmissionError,
	})
	if admitErr != nil {
		if errors.Is(admitErr, ErrTurnBusy) {
			// The prompt was recalled while still waiting for the room.
			rc()
		}
		return
	}
	// The slot is handed to a gate rather than released by a plain defer: a
	// turn that stops to ask the user something gives the slot back for the
	// duration of the question and re-queues for one when the answer lands.
	// Re-acquisition is rooted in the gate's own context (reqCtx, not
	// preClaimCtx) because by then the prompt has left the staging area — only
	// a real cancel should unpark it.
	defer lease.Release()

	// Slot granted. NOW it's safe to flip the row to 'processing' and
	// promote it out of the staging area. Atomic claim-by-id (rather
	// than "claim the next pending") so a user-cancel that deleted this
	// specific row while we were parked on the pool slot resolves as
	// ErrNotFound — we release the slot and move on without firing a
	// misleading prompt_started broadcast. Uses a fresh background
	// context so a drain that already cancelled dispatcherCtx doesn't
	// block the transition we just earned a slot for.
	//
	// We loop on transient claim errors. The store's busy_timeout PRAGMA
	// already absorbs sqlite write contention internally, but historically
	// a single SQLITE_BUSY here would drop the user's prompt: ProcessPrompt
	// returned without claiming, the dispatcher's MarkPromptDone sweep
	// flipped the still-'pending' row to '', and the staging-area UI got
	// stuck on "queued" because prompt_started never arrived. Retry until
	// success or until the request is cancelled (user pressed cancel) /
	// the row vanished (cancel deleted it).
	claimed, ok := r.claimWithRetry(reqCtx, convID, prompt.ID, broadcastFn, lease.EndRoom)
	if !ok {
		return
	}
	prompt = claimed
	// Any new turn supersedes a question a restart left unanswered.
	clearCtx, clearCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := r.Store.SetConversationSuspendedQuestion(clearCtx, convID, ""); err != nil {
		log.Printf("[prompt] clear suspended question conv=%s: %v", convID, err)
	}
	clearCancel()

	// prompt_started is the cue the front-end staging area uses to move
	// a queued prompt out of the staging zone and into chat history.
	// Broadcast immediately after the claim succeeds — that's the
	// earliest moment we can promise the user "your prompt is running,
	// not just queued."
	if data, err := json.Marshal(ServerMessage{
		Type:           "prompt_started",
		ConversationID: convID,
		MessageID:      prompt.ID,
	}); err == nil {
		r.Broadcaster.Broadcast(convID, data)
	}

	broadcastFn(convID, ServerMessage{Type: "status", Status: "thinking"})
	if observation != nil {
		observation.Started(reqCtx)
	}

	// Lock the conversation's workdir to whatever cwd we're about to use.
	// Claude stores sessions per-cwd so --resume only works from the
	// same cwd as the original --session-id call.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 30*time.Second)
	workDir := r.LockConversationWorkDir(lockCtx, convID, user.WorkDir)
	// Pin the conversation to the account it first ran on so a later change to
	// the user's default binding can't silently move an in-progress
	// conversation onto a different account (and its separate history/rate
	// limit). Only when we actually resolved through the pool.
	if accountCtx.ResolvedAccount != nil {
		r.LockConversationAccount(lockCtx, convID, accountCtx.AccountName)
	}
	runOpts := r.BuildRunOptions(lockCtx, convID, workDir, &user, accountCtx.ResolvedAccount)
	lockCancel()

	var onGuardrail func(GuardrailSnapshot)
	if observer, ok := observation.(PromptGuardrailObserver); ok {
		onGuardrail = func(snapshot GuardrailSnapshot) { observer.Guardrail(reqCtx, snapshot) }
	}
	outcome := r.runAgentRequest(reqCtx, prompt.Content, convID, user.ID, workDir, user.WorkDir, conv.Provider, accountCtx.AccountName, conv.SourceType != "cron", lease, runOpts, broadcastFn, onGuardrail)
	if outcome.Err != nil && !outcome.Cancelled {
		r.Drainer.MarkConversationFailed(convID)
	}
	if observation != nil {
		observation.Finish(context.Background(), outcome)
	}
	// After runAgentRequest's deferred EndJob, so Compact's busy guard sees a
	// free room. Non-blocking; skips silently unless the window is actually
	// under pressure.
	r.MaybeAutoCompact(convID)
}

var (
	errPromptConversationMissing = errors.New("conversation missing")
	errPromptUserMissing         = errors.New("user missing")
)

func (r *PromptRunner) resolvePromptRunContext(convID string) (promptRunContext, error) {
	convCtx, convCancel := context.WithTimeout(context.Background(), 10*time.Second)
	conv, convErr := r.Store.GetConversation(convCtx, convID)
	convCancel()
	if convErr != nil {
		return promptRunContext{}, errors.Join(errPromptConversationMissing, convErr)
	}

	userCtx, userCancel := context.WithTimeout(context.Background(), 10*time.Second)
	user, userErr := r.Store.GetUser(userCtx, conv.UserID)
	userCancel()
	if userErr != nil {
		return promptRunContext{}, errors.Join(errPromptUserMissing, userErr)
	}

	jobOwner := user
	ownerCtx, ownerCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if owner, ownerErr := r.Store.GetOwner(ownerCtx, user); ownerErr == nil {
		jobOwner = owner
	}
	ownerCancel()

	return promptRunContext{
		Conversation: conv,
		User:         user,
		JobOwner:     jobOwner,
	}, nil
}

func (r *PromptRunner) resolvePromptAccount(conv store.Conversation, user store.User) (promptAccountContext, error) {
	providerType := conv.Provider
	accountName := user.ProviderBindings[providerType]
	var resolvedAccount *config.Provider
	if r.Pool != nil {
		acc, err := ResolveRunAccount(r.Pool, user, providerType, conv.AccountName)
		if err != nil {
			return promptAccountContext{}, err
		}
		resolvedAccount = acc
		accountName = acc.Name
	}
	return promptAccountContext{
		ProviderType:    providerType,
		AccountName:     accountName,
		ResolvedAccount: resolvedAccount,
	}, nil
}

// persistError stores system failures as display-only history rows. Agent
// backends resume from their own CLI session logs and ProcessPrompt passes only
// the next user prompt to them, so these rows never become model context.
func (r *PromptRunner) persistError(convID, msg string) ServerMessage {
	frame := ServerMessage{Type: "error", Message: msg}
	if r.Store == nil || convID == "" || msg == "" {
		return frame
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	row := store.Message{
		ID:             uuid.New().String(),
		ConversationID: convID,
		Role:           "error",
		Content:        msg,
	}
	if err := r.Store.SaveMessage(ctx, row); err != nil {
		log.Printf("[dispatcher] save error message conv=%s: %v", convID, err)
		return frame
	}
	frame.MessageID = row.ID
	return frame
}

// broadcast ships one frame to the conversation room, intercepting
// context_usage on the way so the conversation's stored window survives a
// refresh. A method rather than a closure because provider-initiated turns
// need the same interception: a wakeup that skipped it would leave the
// context bar frozen at whatever the last prompted turn reported.
func (r *PromptRunner) broadcast(cid string, msg ServerMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	r.Broadcaster.Broadcast(cid, data)
	if msg.Type == "context_usage" {
		r.Broadcaster.SetLastContextUsage(cid, data)
		if content, ok := msg.Content.(string); ok {
			r.Persist.PersistContextUsage(cid, content)
		}
	}
}

func (r *PromptRunner) broadcastError(convID, msg string) {
	if r.Broadcaster == nil {
		return
	}
	data, err := json.Marshal(r.persistError(convID, msg))
	if err != nil {
		return
	}
	r.Broadcaster.Broadcast(convID, data)
}

// LockConversationWorkDir returns the workdir to use for this conversation and
// persists it to conv.work_dir if not already set. This ensures every message in
// the conversation runs Claude with the same cwd so that --session-id and --resume
// look up the same per-cwd session file under ~/.claude/projects/.
//
// Resolution order: existing conv.WorkDir (locked) -> fallback (state.userWorkDir
// or os.Getwd()). Whichever wins is persisted on the first message so it cannot
// drift later.
func (r *PromptRunner) LockConversationWorkDir(ctx context.Context, convID, fallback string) string {
	if r.Store == nil || convID == "" {
		return fallback
	}
	conv, err := r.Store.GetConversation(ctx, convID)
	if err != nil {
		return fallback
	}
	if conv.WorkDir != "" {
		return conv.WorkDir
	}
	chosen := fallback
	if chosen == "" {
		if cwd, err := os.Getwd(); err == nil {
			chosen = cwd
		}
	}
	if chosen != "" {
		_ = r.Store.UpdateConversationWorkDir(ctx, convID, chosen)
	}
	return chosen
}

// LockConversationAccount pins the conversation to accountName on its first
// run, mirroring LockConversationWorkDir: whatever account the first turn
// resolves to becomes the conversation's permanent binding. Once a pin exists
// it is never overwritten — that's the invariant ResolveConversationAccount
// and the UpdateModel handler rely on to forbid mid-conversation switches.
// No-op when accountName is empty or a pin is already set.
func (r *PromptRunner) LockConversationAccount(ctx context.Context, convID, accountName string) {
	if r.Store == nil || convID == "" || accountName == "" {
		return
	}
	conv, err := r.Store.GetConversation(ctx, convID)
	if err != nil || conv.AccountName != "" {
		return
	}
	_ = r.Store.UpdateConversationAccount(ctx, convID, accountName)
}

// claimWithRetry runs ClaimPendingPromptByID in a loop, surfacing the
// claimed row on success or `(_, false)` on cancellation / row-gone.
// Callers that get false MUST broadcast `status: ready` and end the room —
// done here (through endRoom) so every exit path looks the same.
//
// Backoff is intentionally short (200ms) because the store's busy_timeout
// PRAGMA already waits up to 5s internally; reaching this loop means an
// unusual error class. A permanently broken DB would tight-loop forever,
// which is correct: silently dropping the user's prompt is worse than
// loudly retrying so the operator notices.
func (r *PromptRunner) claimWithRetry(
	reqCtx context.Context,
	convID, promptID string,
	broadcastFn func(string, ServerMessage),
	endRoom func(),
) (store.Message, bool) {
	const claimAttemptTimeout = 10 * time.Second
	const claimRetryBackoff = 200 * time.Millisecond
	for {
		claimCtx, claimCancel := context.WithTimeout(context.Background(), claimAttemptTimeout)
		claimed, err := r.Store.ClaimPendingPromptByID(claimCtx, promptID)
		claimCancel()
		if err == nil {
			return claimed, true
		}
		if errors.Is(err, store.ErrNotFound) {
			broadcastFn(convID, ServerMessage{Type: "status", Status: "ready"})
			endRoom()
			return store.Message{}, false
		}
		log.Printf("[dispatcher] claim id=%s: %v (retrying)", promptID, err)
		select {
		case <-time.After(claimRetryBackoff):
		case <-reqCtx.Done():
			broadcastFn(convID, ServerMessage{Type: "status", Status: "ready"})
			endRoom()
			return store.Message{}, false
		}
	}
}

// admission is the shared turn admission over the runner's dependencies.
func (r *PromptRunner) admission() *TurnAdmission {
	return &TurnAdmission{
		Broadcaster: r.Broadcaster,
		Drainer:     r.Drainer,
		Pause:       r.Pause,
		Pool:        r.Pool,
		UserHub:     r.UserHub,
		Store:       r.Store,
	}
}

// relayQueuePositions rebroadcasts a waiting ticket's queue positions to the
// room until the pool grants (or abandons) it and closes the channel.
func relayQueuePositions(ticket *Ticket, convID string, broadcast func(string, ServerMessage)) {
	for pos := range ticket.Positions() {
		ahead, running := ticket.QueueCounts(pos)
		broadcast(convID, ServerMessage{
			Type:           "queue_status",
			ConversationID: convID,
			Status:         "queued",
			QueuePosition:  pos,
			QueueAhead:     &ahead,
			QueueRunning:   &running,
		})
	}
}

// webAdmissionError is the browser path's wording for admission failures.
// Context errors pass through untouched: ProcessPrompt maps context.Canceled
// to a quiet "ready" (the user recalled the prompt), which is exactly why the
// admission's own queue deadline must arrive as a real error instead — or the
// prompt would vanish from the staging area with nothing shown to explain why.
func webAdmissionError(err error) error {
	var timeout *QueueTimeoutError
	var entry *PoolEntryError
	switch {
	case errors.As(err, &timeout):
		return fmt.Errorf(
			"account %q has no free slot: gave up waiting after %s — the queue is backed up, try again later",
			timeout.Account, timeout.Waited)
	case errors.As(err, &entry):
		return entry.Err
	}
	return err
}

// webAdmissionMessage is the text persisted and broadcast for a refusal.
func webAdmissionMessage(err error) string {
	var svcErr *ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Message()
	}
	return err.Error()
}

// BuildRunOptions constructs the RunRequest for a browser-chat (or /compact)
// turn: the shared NewOneshotRunRequest base — account, owner environment,
// sandbox tier inherited from the owning human, system prompt, tools, MCP — plus
// the conversation's own session, model and effort, and the web-only system
// prompts.
//
// The MCP config temp file NewOneshotRunRequest may write is left for the
// caller to remove once the turn ends (runAgentRequest / Compact delete
// opts.McpConfigPath), because the request outlives this call.
//
// Resume decision: we ask the runner whether ~/.claude/projects/<cwd>/<sid>.jsonl
// exists. That file is what Claude CLI itself locks against — if it exists,
// passing --session-id again fails with "Session ID is already in use", so we
// must use --resume. Earlier we keyed this off "DB has an assistant message",
// but that breaks when the first run produced a reply that Claude wrote to its
// jsonl yet our service crashed before persisting it: every retry would then
// pick --session-id and hit the lock error forever.
//
// ClearMessages still rotates session_id, so the rotated id has no
// jsonl on disk and IsResume is correctly false on the next send.
func (r *PromptRunner) BuildRunOptions(ctx context.Context, convID, workDir string, userConfig *store.User, resolvedAccount *config.Provider) agent.RunRequest {
	var user store.User
	if userConfig != nil {
		user = *userConfig
	}
	// The jail is the conversation's own directory. Sandbox tier inherits from
	// the owning human, not the conversation's own row: conversations run
	// under an agent row whose own sandbox_mode is always the jailed default,
	// so reading it would make "unrestricted" unreachable for every agent-run
	// turn. Admins always bypass (see SandboxUnrestrictedFor).
	opts, _ := NewOneshotRunRequest(ctx, r.Store, user, resolvedAccount, r.Cfg, r.Sandbox, workDir,
		SandboxUnrestrictedFor(ctx, r.Store, user))
	// Conversation-scoped model/effort values win over the Agent defaults the
	// base seeds. In particular, an empty conversation think_level must clear
	// the Agent default and let the provider decide. Runs after the account is
	// applied so the resume probe checks the same per-account session tree the
	// CLI will actually run against — otherwise switching a user's binding
	// points the probe at the old account's credentials dir, and the stale
	// --session-id either trips "Session ID is already in use" or quietly
	// starts a fresh session and drops history.
	r.applySessionToRequest(ctx, &opts, convID, workDir)
	if resolvedAccount != nil {
		opts.ContextWindow = ModelSpecFor(resolvedAccount.Name, opts.Model).ContextWindow
	}
	AppendSystem(&opts, WebArtifactSystemPrompt)
	AppendSystem(&opts, AbsolutePathsSystemPrompt)
	return opts
}

// mergeUserEnvIntoRequest applies the self-service environment on top of the
// provider account environment.
func mergeUserEnvIntoRequest(opts *agent.RunRequest, rawEnv string) {
	if opts == nil {
		return
	}
	if strings.TrimSpace(rawEnv) == "" {
		return
	}
	userEnv, err := userenv.Parse(rawEnv)
	if err != nil {
		return
	}
	if len(userEnv) == 0 {
		return
	}
	merged := make(map[string]string, len(opts.AccountEnv)+len(userEnv))
	for k, v := range opts.AccountEnv {
		merged[k] = v
	}
	for k, v := range userEnv {
		merged[k] = v
	}
	opts.AccountEnv = merged
}

// applySessionToRequest loads the conversation row and copies the per-conv
// Model, ThinkLevel and SessionID onto opts. The conversation row always
// carries a session id (minted with the row, rotated by /clear-context and
// /compact), so nothing is minted here. The SessionExists probe uses the same
// backend that will execute the prompt — otherwise a codex conversation would
// be checked against the claude session-dir layout and always answer "no",
// silently restarting the session every turn.
func (r *PromptRunner) applySessionToRequest(ctx context.Context, opts *agent.RunRequest, convID, workDir string) {
	if r.Store == nil || convID == "" {
		return
	}
	conv, err := r.Store.GetConversation(ctx, convID)
	if err != nil {
		return
	}
	opts.Model = conv.Model
	opts.ThinkLevel = conv.ThinkLevel
	if conv.SessionID == "" {
		return
	}
	PrepareRunSession(r.backendFor(conv.Provider), workDir, conv.SessionID, opts)
}

// wakeupRunner builds the delivery path for turns the provider starts on its
// own once background work settles.
//
// The routing is captured from the turn that parked the process — backend,
// work dir, owner and account are conversation-scoped and do not drift between
// turns. What is deliberately NOT captured is anything turn-scoped: the
// request context, the account-pool ticket, the MCP temp file. All three are
// dead by the time a wakeup fires, and closing over them is how this kind of
// deferred callback usually breaks.
//
// Existence, on the other hand, is re-checked at wakeup time: a parked bridge
// can outlive the conversation it belongs to, and a deleted conversation must
// refuse the turn rather than resurrect itself.
func (r *PromptRunner) wakeupRunner(target WakeupTarget) *WakeupRunner {
	return &WakeupRunner{
		Streamer:  r.streamer(),
		Broadcast: func(convID string, msg ServerMessage) { r.broadcast(convID, msg) },
		Resolve: func(convID string) (WakeupTarget, bool) {
			if r.Store == nil {
				return target, true
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := r.Store.GetConversation(ctx, convID); err != nil {
				return WakeupTarget{}, false
			}
			return target, true
		},
	}
}

// runAgentRequest runs the agent CLI in a goroutine, streaming events to all
// clients in the conversation room and persisting results to the store. It is
// detached from any individual WebSocket — broadcast goes through the room so
// reconnecting tabs can still receive the stream via replay.
//
// userID is the conversation owner id used to attribute token usage to a
// specific account in the per-day usage aggregate.
func (r *PromptRunner) runAgentRequest(
	ctx context.Context,
	prompt string,
	convID string,
	userID string,
	workDir string,
	artifactRootDir string,
	provider string,
	accountName string,
	userInitiated bool,
	lease *TurnLease,
	opts agent.RunRequest,
	broadcast func(string, ServerMessage),
	onGuardrail func(GuardrailSnapshot),
) PromptOutcome {
	opts.EnableUserQuestions = true
	activityJob := lease.Job()
	activitySourceID := uuid.New().String()
	residentActivity := func(active bool) {
		if !r.Drainer.SetResidentActivity(activitySourceID, activityJob, active) {
			return
		}
		// The adapter calls lifecycle hooks from its stdout router. Keep that
		// path non-blocking: the registry update above is already authoritative,
		// and this full snapshot self-corrects even if transitions coalesce.
		go BroadcastConversationActivity(
			context.Background(), r.UserHub, r.Drainer, r.Store, activityJob.UserID,
		)
	}
	defer func() {
		if opts.McpConfigPath != "" {
			_ = os.Remove(opts.McpConfigPath)
		}
		// Order matters: broadcast "ready" before ending the room so connected
		// clients see the transition; EndJob then clears the in-flight state
		// and (if no clients remain) drops the room and its replay buffer. By
		// the time it returns, cmd.Wait has already returned, so the Claude
		// session lock has been released — the next "input" will not collide
		// with this session ID. The slot and drainer entry stay with the
		// lease until ProcessPrompt returns.
		broadcast(convID, ServerMessage{Type: "status", Status: "ready"})
		lease.EndRoom()
	}()

	backend := r.backendFor(provider)
	out := r.streamer().Run(ctx, AgentStreamRequest{
		Backend:         backend,
		Prompt:          prompt,
		WorkDir:         workDir,
		ArtifactRootDir: artifactRootDir,
		ConversationID:  convID,
		OwnerID:         userID,
		AccountName:     accountName,
		UserInitiated:   userInitiated,
		Opts:            opts,
		SlotGate:        lease.Gate(),
		// A turn that leaves background work running keeps its process, so
		// the wakeup that work triggers has somewhere to land. Only the web
		// chat grants this: an IM thread's reply slot belongs to the message
		// that opened it, with nowhere to deliver an unprompted turn.
		AllowResidency:   true,
		ResidentActivity: residentActivity,
		Wake: r.wakeupRunner(WakeupTarget{
			Backend:         backend,
			WorkDir:         workDir,
			ArtifactRootDir: artifactRootDir,
			OwnerID:         userID,
			AccountName:     accountName,
			Model:           opts.Model,
		}).Sink(convID),
		// The web transcript is an append-only stream, so every result frame
		// becomes its own assistant row, sub-agent activity is rendered inline,
		// a silent turn persists nothing, and the owner gets one push
		// notification per turn.
		Mode:                  AgentStreamPerResult,
		ForwardSubagentFrames: true,
		Notify:                true,
		Broadcast:             func(msg ServerMessage) { broadcast(convID, msg) },
		OnGuardrail:           onGuardrail,
	})
	return PromptOutcome{
		Content:           out.Content,
		Artifacts:         out.Artifacts,
		RejectedArtifacts: out.RejectedArtifacts,
		Err:               out.Err,
		Cancelled:         out.Cancelled,
	}
}

// streamer builds the shared stream consumer from the runner's deps. Both the
// web chat and the IM bridge run their turns through it so persistence, usage
// accounting, rate-limit cooldowns and retry rollback stay identical.
func (r *PromptRunner) streamer() *AgentStreamer {
	var guardrails config.GuardrailsConfig
	if r.Cfg != nil {
		guardrails = r.Cfg.Guardrails
	}
	return &AgentStreamer{
		Store:              r.Store,
		Broadcaster:        r.Broadcaster,
		Pool:               r.Pool,
		Persist:            r.Persist,
		Guardrails:         guardrails,
		GuardrailReminders: r.GuardrailReminders,
	}
}

// streamEventToMessage maps an agent.StreamEvent to a ServerMessage for
// WebSocket delivery. Returns (msg, true) for known event kinds, or
// (ServerMessage{}, false) for unknown kinds.
func streamEventToMessage(evt agent.StreamEvent) (ServerMessage, bool) {
	var msg ServerMessage
	switch evt.Kind {
	case agent.KindDelta:
		msg = ServerMessage{Type: "delta", Content: evt.Content}
	case agent.KindResult:
		msg = ServerMessage{Type: "result", Content: evt.Content}
	case agent.KindContextUsage:
		msg = ServerMessage{Type: "context_usage", Content: evt.Content}
	case agent.KindSystemInit:
		msg = ServerMessage{Type: "system_init", Content: evt.Content}
	case agent.KindSessionInfo:
		msg = ServerMessage{Type: "session_info", Content: evt.Content}
	case agent.KindToolUseStart:
		msg = ServerMessage{Type: "tool_use_start", Content: evt.Content}
	case agent.KindToolInputDelta:
		msg = ServerMessage{Type: "tool_input_delta", Content: evt.Content}
	case agent.KindToolResult:
		msg = ServerMessage{
			Type:    "tool_result",
			Content: store.ToolContentForWire(store.CompactToolContent(evt.Content)),
		}
	case agent.KindUsage:
		// Usage is no longer surfaced as a standalone WS event: the
		// caller embeds it into the matching `result` event's metadata
		// so the chip renders synchronously with the assistant message
		// it describes. The event is still produced upstream for
		// PersistTokenUsage's analytics path; we just don't broadcast.
		return ServerMessage{}, false
	case agent.KindThinkingDelta:
		msg = ServerMessage{Type: "thinking_delta", Content: evt.Content}
	case agent.KindUserQuestion:
		msg = ServerMessage{Type: "user_question", Content: evt.Content}
	case agent.KindUserQuestionResolved:
		msg = ServerMessage{Type: "user_question_resolved", RequestID: evt.Content}
	case agent.KindRateLimit:
		msg = ServerMessage{Type: "rate_limit", Content: evt.Content}
	default:
		return ServerMessage{}, false
	}
	msg.Subagent = evt.Subagent
	return msg, true
}

func writeTempMcpConfig(content string) (string, error) {
	f, err := os.CreateTemp("", "mcp-config-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return "", err
	}
	_ = f.Close()
	return f.Name(), nil
}
