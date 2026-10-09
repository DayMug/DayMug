package imbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/prompts"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge/casefile"
	"github.com/DayMug/DayMug/backend/internal/store"
)

const (
	imThinkingUpdateInterval = 3 * time.Second
	// imProgressPreviewLimit is the whole preview's rune budget, shared by the
	// reasoning and reply sections, so the two of them together stay inside one
	// platform message. imReplyPreviewLimit is the reply's guaranteed share:
	// whatever it leaves over is the reasoning window, which is therefore the
	// full budget until the agent starts answering.
	imProgressPreviewLimit = 3200
	imReplyPreviewLimit    = 2000
	// imPersistTimeout bounds the end-of-turn writes that run on a context
	// detached from the turn, so an interrupted turn still records what it
	// learned without letting a wedged database hold the goroutine forever.
	imPersistTimeout = 10 * time.Second
)

// imRun carries one IM-triggered agent run from gating to final reply. Each
// phase method reads what earlier phases resolved, so run() stays a linear
// script: resolveTarget → prepareConversation → acquireSlot → execute.
//
// The state is grouped by the phase that writes it. The groups are embedded so
// phase methods keep reading r.agentUser, r.conversation, … directly; the
// grouping exists to show which phase owns a field, not to add indirection.
type imRun struct {
	// Fixed at construction: the inbound message and where replies go.
	bridge    *IMBridge
	msg       imbot.Message
	rule      imbot.ChannelRule
	responder imbot.Responder

	imTarget
	imMirror
	imQueueState
	imCaseState

	// artifacts is what the finished turn published, set by execute.
	artifacts service.ArtifactOutcome
}

// imTarget is what resolveTarget binds the message to: the agent, its sticky
// thread, the provider/model pair, and a concrete pool account.
type imTarget struct {
	agentUser store.User
	thread    store.BotThread
	provider  string
	model     string
	// thinkLevel is what a conversation minted for this turn starts on; the
	// turn itself runs on the Agent's think level (see NewOneshotRunRequest).
	thinkLevel string
	boundName  string
	account    *config.Provider
}

// imMirror is the DayMug side of the thread, written by prepareConversation:
// the mirrored conversation and what this turn imported into it.
type imMirror struct {
	conversation  store.Conversation
	created       bool
	attachments   []IMImageMetadata
	threadContext []imbot.Message
	// promptID is the persisted user row for this turn. It carries
	// store.QueueStatusPoolQueued from the moment it is written until
	// leaveQueue clears it.
	promptID string
}

// imQueueState tracks the turn's wait for an account slot.
type imQueueState struct {
	leftQueue bool
	// announce owns the thread's progress message for the whole waiting
	// phase. Nil until the run enters the account pool.
	announce *imQueueAnnouncer
}

// imCaseState is the case-file mode state of one turn (see imbot_case.go).
type imCaseState struct {
	// homeRoot is the owning human's work_dir, the containment boundary every
	// DayMug-managed write for this agent must stay inside.
	homeRoot string
	// caseRoot is the absolute directory this agent's thread cases are
	// materialized into, resolved once in resolveTarget. Empty when the
	// agent's home root could not be resolved, which switches case mode off
	// for the turn rather than writing the document somewhere nothing reads.
	caseRoot string
	// caseDoc is the case-file mode document this turn started from, kept so
	// the post-run read-back can tell an edit from an untouched file. Empty
	// when the turn is not in case-file mode.
	caseDoc string
	// caseRotationHeld records that this turn was eligible for rotation and
	// was not rotated, because the case had not absorbed the session yet. The
	// turn therefore starts on a context that is already past the ratio, and
	// the agent is told why.
	caseRotationHeld bool
	// retiredConversationID names the conversation this turn rotated away
	// from, so its final reply can be carried into the session replacing it.
	// Empty unless this turn rotated.
	retiredConversationID string
	// retiredTitle is that conversation's title, which the replacement
	// continues under its own number (see rotatedConversationTitle).
	retiredTitle string
	// caseHandover is that carried-over reply, resolved once in prepareCase.
	caseHandover string
	// caseRejection explains why the previous turn's case update was refused
	// and rolled back, picked up from disk in prepareCase. Empty when the last
	// update was stored, which is the normal case.
	caseRejection string
}

// admitRotation is the last gate before a session is discarded.
//
// Rotation swaps a session's transcript for the case, which is sound only when
// the case actually holds what that session learned. When it does not, the
// cheaper mistake by a wide margin is to keep the session: the turn starts
// heavier, but nothing is lost, and the next turn can rotate once the case has
// caught up. Rotating instead silently voids the whole session — the thread's
// own history is not re-imported (LastMessageID is deliberately preserved) and
// Slack's backfill filters out the agent's own messages, so there is nothing
// left to reconstruct it from.
//
// The hold is not unbounded: casefile.RotateForceRatio releases it before the
// window runs out, because a session too full to take another turn is worse
// than a lossy rotation.
func (r *imRun) admitRotation(ctx context.Context, conversation store.Conversation) bool {
	if casefile.ContextOverThreshold(conversation.LastContextUsage, casefile.RotateForceRatio) {
		log.Printf("[case] forcing rotation for thread %s/%s: context over %.0f%% with the case still behind",
			r.msg.ChannelID, r.msg.ThreadID, casefile.RotateForceRatio*100)
		return true
	}
	stored, err := r.bridge.Store.GetThreadCase(ctx, r.msg.ChannelID, r.msg.ThreadID)
	if err != nil {
		// A failed lookup is not evidence that the case took delivery, and the
		// two outcomes are not symmetric: holding costs context, rotating on a
		// wrong guess costs the session.
		log.Printf("[case] rotation gate lookup %s/%s: %v", r.msg.ChannelID, r.msg.ThreadID, err)
		r.caseRotationHeld = true
		return false
	}
	if casefile.AbsorbedSession(stored.Doc, stored.UpdatedAt, conversation.CreatedAt) {
		return true
	}
	r.caseRotationHeld = true
	log.Printf("[case] holding session for thread %s/%s: case v%d has not absorbed conversation %s",
		r.msg.ChannelID, r.msg.ThreadID, stored.Version, conversation.ID)
	return false
}

// leaveQueue ends the prompt's queued phase: it drops the durable marker the
// chat UI stages on and announces the start the same way the web path does.
// Idempotent, and called on every exit — a turn that fails or is cancelled
// while queued has still left the queue, and a row left marked would show a
// queue card forever.
func (r *imRun) leaveQueue(ctx context.Context) {
	if r.leftQueue || r.promptID == "" {
		return
	}
	r.leftQueue = true
	if err := r.bridge.Store.MarkPromptDone(ctx, r.promptID); err != nil {
		log.Printf("imbot: clear queued marker for %s: %v", r.promptID, err)
	}
	r.bridge.broadcastIM(r.conversation.ID, service.ServerMessage{
		Type: "prompt_started", MessageID: r.promptID,
	})
}

// ClearStaleQueuedPrompts drops pool-queued markers left behind by a previous
// process. An IM turn cannot outlive the server — its connector, pool ticket
// and agent are all gone — so a surviving marker would show the chat UI a queue
// card for a turn nobody is waiting on, and nothing would ever clear it.
// Best effort: a boot that cannot reach the database has larger problems, and
// failing here must not stop the connectors from dialling out.
func (b *IMBridge) ClearStaleQueuedPrompts(ctx context.Context) {
	if b.Store == nil {
		return
	}
	cleared, err := b.Store.ClearPoolQueuedPrompts(ctx)
	if err != nil {
		log.Printf("imbot: clear stale queued prompts: %v", err)
		return
	}
	if cleared > 0 {
		log.Printf("imbot: cleared %d queued prompt(s) left by the previous run", cleared)
	}
}

func (b *IMBridge) run(ctx context.Context, msg imbot.Message, rule imbot.ChannelRule, responder imbot.Responder) (string, error) {
	if b.Pool == nil || b.Cfg == nil {
		return "", errors.New("IM 集成未完成初始化（缺少账号池或配置）")
	}
	// Operator pause. Refused before the run touches the store, so a paused
	// window leaves no half-prepared conversation or queue marker behind. IM
	// turns have no durable pending queue (unlike web chat, whose rows wait in
	// SQLite), so telling the sender to resend is the honest answer.
	if b.Pause.Paused() {
		return "", errors.New("管理员已暂停处理新任务，请稍后重试")
	}
	r := &imRun{bridge: b, msg: msg, rule: rule, responder: responder}
	if err := r.resolveTarget(ctx); err != nil {
		return "", err
	}
	if err := r.prepareConversation(ctx); err != nil {
		return "", err
	}

	// Every exit past this point has left the queue, whether by starting, by
	// failing, or by being cancelled. The context is detached because the two
	// interesting cases — cancel and supersede — arrive as a cancelled ctx, and
	// the marker still has to come off the row.
	defer func() { r.leaveQueue(context.WithoutCancel(ctx)) }()

	agentCtx, agentCancel := context.WithCancel(ctx)
	defer agentCancel()
	release, slotGate, err := r.acquireSlotWithGate(agentCtx, agentCancel)
	if err != nil {
		return "", err
	}
	defer release()
	// Queue time is bounded separately inside acquireSlot. By default an active
	// agent turn has no wall-clock cap; the runner watchdog still terminates a
	// stalled CLI, and operators can opt into a total IM limit in config.
	runCtx, runCancel := newIMRunContext(agentCtx, b.Cfg.IMRunTimeout.Duration)
	defer runCancel()
	content, err := r.execute(runCtx, ctx, slotGate)
	// Join the waiting-phase announcer before any final text is posted. It
	// writes the same single progress message that Complete rewrites (and that
	// HandleMessage's failure/superseded notices rewrite), so letting it run
	// past this point would let a queue position or the thinking banner land on
	// top of the answer the reader came for.
	r.announce.wait()
	if err != nil {
		return "", err
	}
	// Some backends can flush a buffered result while their cancellation is
	// being delivered. The shared streamer records that result for recovery,
	// but an explicit web cancel still owns the final publication decision:
	// never turn a post-cancel buffer into a new Slack/Feishu/Telegram reply.
	if agentCtx.Err() != nil || (b.Broadcaster != nil && b.Broadcaster.WasCancelled(r.conversation.ID)) {
		return "", context.Canceled
	}
	if content == "" {
		content = imbot.EmptyReplyText
	}
	handoffHandled := false
	if directive, ok := parseHandoffDirective(content); ok {
		handoffHandled = b.relayHandoff(r.msg, content, directive, responder)
	}
	if !handoffHandled {
		b.respondBestEffort(r.msg, func(replyCtx context.Context) error {
			return responder.Complete(replyCtx, content)
		})
	}
	b.publishArtifacts(r.msg, responder, r.artifacts)
	return content, nil
}

// publishArtifacts uploads the files a turn published and tells the thread when
// that fails. Two things it deliberately does not do the obvious way:
//
// Each file is uploaded on its own call, because PostAttachments stops at its
// first error — a batch led by one unpublishable file used to drop every file
// behind it, so a turn that produced a report plus two screenshots could
// deliver none of them.
//
// A failure is announced in the thread rather than only logged. The answer
// posted just above almost always refers to these files, so a silent drop reads
// as "the agent lied about the screenshots"; and a log-only failure is
// unrecoverable once the host's journal stops (which is exactly how this went
// unnoticed for a fortnight).
func (b *IMBridge) publishArtifacts(msg imbot.Message, responder imbot.Responder, outcome service.ArtifactOutcome) {
	// A marker rejected during extraction never becomes an attachment, so it
	// has no upload to fail — but it is exactly as invisible to the reader, and
	// exactly as invisible to the agent, as an upload that did fail. Both go
	// into the same notice.
	var failures []string
	for _, rejection := range outcome.Rejected {
		failures = append(failures, rejection.String())
	}
	artifactResponder, ok := responder.(imbot.ArtifactResponder)
	if ok && len(outcome.Published) > 0 {
		for _, attachment := range outboundAttachments(outcome.Published) {
			var uploadErr error
			b.respondBestEffort(msg, func(replyCtx context.Context) error {
				uploadErr = artifactResponder.PostAttachments(replyCtx, []imbot.OutboundAttachment{attachment})
				return uploadErr
			})
			if uploadErr != nil {
				failures = append(failures, attachment.Name+"："+uploadErr.Error())
			}
		}
	}
	if len(failures) == 0 {
		return
	}
	// Post, not Notify: this notice always follows Complete, and Notify
	// resolves to Update on an edit-capable platform. progressResponder keeps
	// messageID pointing at the answer's first chunk whenever the turn posted
	// no progress message, so an edit there would overwrite the very answer the
	// notice is annotating.
	b.respondBestEffort(msg, func(replyCtx context.Context) error {
		return responder.Post(replyCtx, prompts.ArtifactUploadFailed(failures))
	})
}

func newIMRunContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return context.WithCancel(parent)
}

func conversationWindowExpired(createdAt time.Time, maxDuration string, now time.Time) (bool, error) {
	if createdAt.IsZero() {
		return false, nil
	}
	maxDuration = strings.TrimSpace(maxDuration)
	if maxDuration == "" {
		maxDuration = store.DefaultBotMaxConversationDuration
	}
	duration, err := time.ParseDuration(maxDuration)
	if err != nil || duration < 0 {
		return false, fmt.Errorf("invalid max conversation duration %q", maxDuration)
	}
	if duration == 0 {
		return false, nil
	}
	// The configured duration is a fixed window from conversation creation,
	// not an inactivity timeout. At the boundary, the next message starts a
	// new conversation and therefore a fresh CLI session.
	return !now.Before(createdAt.Add(duration)), nil
}

// resolveTarget binds the message to its agent, sticky thread, provider/model
// pair, and a concrete pool account.
func (r *imRun) resolveTarget(ctx context.Context) error {
	b := r.bridge
	agentUser, err := b.agentFor(ctx, r.msg)
	if err != nil {
		return err
	}
	r.agentUser = agentUser
	if home, caseRoot, rootErr := agentCaseRoot(ctx, b.Store, agentUser); rootErr != nil {
		log.Printf("[case] agent %s has no case directory: %v", agentUser.ID, rootErr)
	} else {
		r.homeRoot, r.caseRoot = home, caseRoot
	}

	thread, err := b.Store.GetBotThread(ctx, threadPlatform(r.msg), r.msg.ChannelID, r.msg.ThreadID)
	if err != nil {
		thread = store.BotThread{}
	}

	configuredModel := ""
	maxConversationDuration := ""
	if b.Bots != nil && r.msg.BotID != "" {
		bot, botErr := b.Bots.GetBot(ctx, r.msg.BotID)
		if botErr != nil || bot.AgentID != agentUser.ID {
			return errors.New("IM Bot 配置不可用")
		}
		configuredModel = bot.Model
		maxConversationDuration = bot.MaxConversationDuration
	}
	// stickyAccount is the account the thread's live conversation already runs
	// on, and the reason rotation never moves a resumable session (see
	// resolveRunAccount). Stays empty when the conversation is retired below.
	stickyAccount := ""
	if thread.ConversationID != "" {
		if conversation, convErr := b.Store.GetConversation(ctx, thread.ConversationID); convErr == nil {
			expired, expiryErr := conversationWindowExpired(conversation.CreatedAt, maxConversationDuration, time.Now())
			if expiryErr != nil {
				return fmt.Errorf("IM Bot 最长会话时间无效: %w", expiryErr)
			}
			// Case-file mode adds a second reason to retire the runtime
			// identity, and it is the one that actually fires during a long
			// task: the previous turn left the model's context past the
			// rotation ratio. Rotating there is what keeps context bounded by
			// the case instead of by turn count — but only once the case
			// actually holds what the session found, which admitRotation is
			// what checks.
			//
			// This runs before the session is resumed rather than after the
			// turn that crossed the line, so the crossing turn still gets to
			// finish and write its case at full context.
			rotate := agentUser.CaseMode && r.msg.ChannelID != "" &&
				casefile.ContextOverThreshold(conversation.LastContextUsage, b.caseRotateRatio())
			// Crossing the ratio only makes the session *eligible* to be
			// retired. Whether it may actually be thrown away depends on the
			// case having taken delivery of what it found — see admitRotation.
			if rotate && !expired {
				rotate = r.admitRotation(ctx, conversation)
			}
			if expired || rotate {
				// Keep LastMessageID so platform history backfill starts after
				// the last imported message, but sever every sticky runtime
				// identity. The next phase will mint both a new conversation
				// and a new CLI session using the Bot's current model.
				thread.ConversationID = ""
				thread.SessionID = ""
				thread.Provider = ""
				thread.Model = ""
				if rotate && !expired {
					r.retiredConversationID = conversation.ID
					r.retiredTitle = conversation.Title
					log.Printf("[case] rotating session for thread %s/%s: context over %.0f%%",
						r.msg.ChannelID, r.msg.ThreadID, b.caseRotateRatio()*100)
				}
			} else {
				stickyAccount = conversation.AccountName
			}
		}
	}
	r.thread = thread
	seed, err := b.resolveConversationSeed(agentUser, thread, configuredModel)
	if err != nil {
		return err
	}
	r.provider, r.model, r.thinkLevel = seed.Provider, seed.Model, seed.ThinkLevel
	if stickyAccount == "" && thread.ConversationID != "" {
		// Conversations predate account pinning, so a live one can carry no
		// pin at all. It ran on the binding; keep it there rather than rotating
		// a resumable CLI session onto an account that never hosted it.
		stickyAccount = agentUser.ProviderBindings[r.provider]
	}
	r.boundName = b.resolveRunAccount(agentUser, stickyAccount, r.provider, r.model)
	r.account, err = b.Pool.AccountForType(r.boundName, r.provider)
	if err != nil {
		return fmt.Errorf("agent %q 没有可用的 %s 账号绑定，请管理员在后台绑定 provider", agentUser.Name, r.provider)
	}
	return nil
}

// prepareConversation mirrors the thread into a DayMug conversation, saves
// inbound images, and persists + broadcasts the user message.
func (r *imRun) prepareConversation(ctx context.Context) error {
	conversation, created, err := r.bridge.ensureConversation(ctx, r.msg, r.agentUser, r.thread, r.rotatedTitle(ctx), r.retiredConversationID, service.ConversationSeed{
		Provider: r.provider, Model: r.model, ThinkLevel: r.thinkLevel,
	})
	if err != nil {
		return err
	}
	r.conversation = conversation
	r.created = created
	r.pinAccount(ctx)
	if err := r.ingestInbound(ctx); err != nil {
		return err
	}
	r.startAutoTitle(ctx)
	return r.syncThreadCursor(ctx)
}

// pinAccount records which account this conversation ended up on, the same way
// the web path's LockConversationAccount does. Without it a rotated thread has
// nothing to resume against: the next turn would re-roll the round-robin and
// hand the CLI a session id that lives in a different account's config dir.
// Never overwrites an existing pin, and a failed write is not fatal — the run
// itself is already bound to r.boundName.
func (r *imRun) pinAccount(ctx context.Context) {
	if r.boundName == "" || r.conversation.AccountName != "" {
		return
	}
	// Only pin what ResolveConversationAccount will accept later. It rejects a
	// pin that is not in the agent's granted set, and every later reader of
	// this row — the web mirror, auto-title, the session bundle — goes through
	// it. Writing an ungranted name (a binding with no account row behind it)
	// would wedge all three.
	if !slices.Contains(r.agentUser.ProviderAccounts[r.provider], r.boundName) {
		return
	}
	if err := r.bridge.Store.UpdateConversationAccount(ctx, r.conversation.ID, r.boundName); err != nil {
		log.Printf("imbot: pin conversation %s to account %s: %v", r.conversation.ID, r.boundName, err)
		return
	}
	r.conversation.AccountName = r.boundName
}

// ingestInbound imports the thread backlog and the triggering message into the
// mirrored conversation. Both draw from one attachment budget, so a thread full
// of images cannot multiply the per-message cap by the size of the backfill
// window.
func (r *imRun) ingestInbound(ctx context.Context) error {
	b := r.bridge
	budget := newInboundAttachmentBudget()
	threadContext, err := b.persistThreadContext(ctx, r.msg, r.conversation, r.agentUser, r.thread.LastMessageID, budget)
	if err != nil {
		return err
	}
	r.threadContext = threadContext
	attachments, attachmentNotice, err := b.materializeInboundImages(ctx, r.msg, r.agentUser, budget)
	if err != nil {
		return err
	}
	r.attachments = attachments
	// Mutating the run's copy of the message is what carries the notice into
	// both persistInbound's transcript row and buildPromptWithThreadContext.
	r.msg.Text = appendInboundNotice(r.msg.Text, attachmentNotice)
	promptID, err := b.persistInbound(ctx, r.msg, r.conversation, r.created, r.attachments)
	if err != nil {
		return err
	}
	r.promptID = promptID
	return nil
}

// startAutoTitle runs off the critical path because the title only depends on
// persisted user messages, not on the agent reply. An IM turn may spend minutes
// in the account queue; its conversation should become recognizable in the web
// UI while that wait is still in progress.
//
// Only a provisional title is replaced: the fallback, or on a rotated
// conversation the fallback carrying its number, which the generated title
// keeps. A rotated conversation that inherited a real title has neither.
func (r *imRun) startAutoTitle(ctx context.Context) {
	fallback := imConversationFallbackTitle(r.msg)
	replaceable, suffix := fallback, ""
	if r.conversation.Title != fallback {
		if !strings.HasPrefix(r.conversation.Title, fallback+"-") {
			return
		}
		seq := r.conversationSeq(ctx, r.conversation.ID)
		if seq == 1 || r.conversation.Title != fallback+seqSuffix(seq) {
			return
		}
		replaceable, suffix = r.conversation.Title, seqSuffix(seq)
	}
	go r.bridge.MessagePersister().MaybeAutoTitleReplacingWithAffixes(
		r.conversation.ID,
		replaceable,
		imConversationTitlePrefix(r.msg),
		suffix,
	)
}

// rotatedTitle is the title of the conversation this turn creates to replace
// a rotated one; "" (no rotation) means the IM fallback title.
func (r *imRun) rotatedTitle(ctx context.Context) string {
	if r.retiredConversationID == "" {
		return ""
	}
	seq := r.conversationSeq(ctx, r.retiredConversationID)
	title, _ := rotatedConversationTitle(r.retiredTitle, seq, imConversationFallbackTitle(r.msg))
	return title
}

// syncThreadCursor points the sticky thread row at this conversation and
// advances the import cursor past the triggering message, so the next turn
// backfills only what arrived after it. Runs after ingestInbound, which still
// needs the previous cursor value.
func (r *imRun) syncThreadCursor(ctx context.Context) error {
	r.thread.ConversationID = r.conversation.ID
	if r.thread.SessionID == "" {
		r.thread.SessionID = r.conversation.SessionID
	}
	r.thread.Platform = threadPlatform(r.msg)
	r.thread.ChannelID = r.msg.ChannelID
	r.thread.ThreadID = r.msg.ThreadID
	r.thread.AgentID = r.agentUser.ID
	r.thread.Provider = r.provider
	r.thread.Model = r.model
	r.thread.LastMessageID = r.msg.MessageID
	if err := r.bridge.Store.UpsertBotThread(ctx, r.thread); err != nil {
		return fmt.Errorf("保存 IM 话题同步游标: %w", err)
	}
	return nil
}

// acquireSlotWithGate admits the run through the shared turn admission: the
// web UI (Broadcaster job), the admin drain view, and the account pool,
// relaying queue positions to the IM thread while waiting. The returned
// release undoes everything acquired, in reverse order; on error nothing is
// held.
func (r *imRun) acquireSlotWithGate(agentCtx context.Context, agentCancel context.CancelFunc) (func(), *service.TurnSlotGate, error) {
	b := r.bridge
	lease, err := b.TurnAdmission().Admit(service.AdmitSpec{
		// Registering the room before the pool gives the mirrored web
		// conversation the same busy/cancel semantics as a prompt submitted
		// from the browser, including while it is still queued.
		ConversationID: r.conversation.ID,
		Cancel:         agentCancel,
		Target: service.TurnTarget{
			// The admin view groups by the owning login user, not the agent.
			Job: service.DrainerJob{
				UserID:         r.activityOwnerID(),
				Username:       r.agentUser.Name,
				ProviderType:   r.provider,
				AccountName:    r.account.Name,
				ConversationID: r.conversation.ID,
			},
			PoolUserID:  r.agentUser.ID,
			AccountName: r.boundName,
			Model:       r.model,
		},
		QueueCtx:        agentCtx,
		QueueTimeout:    imQueueWait,
		ActivityOwnerID: r.activityOwnerID(),
		// The ack is the one status write still made inline, and it stays
		// inline because Responder.Update edits the message Start creates: it
		// must be posted before the announcer can touch it. It runs before the
		// pool is entered, so a slow platform here delays this run only — it
		// never pins an account slot other runs are queuing for. It also stays
		// behind the busy check: a refused message never claimed the thread.
		OnRoomClaimed: func() {
			b.respondBestEffort(r.msg, func(ctx context.Context) error { return r.responder.Start(ctx, AckText) })
		},
		// Queue positions are relayed by the announcer. A slot re-acquired after
		// a parked question has no banner to supersede, so it is not announced.
		OnTicket: func(ticket *service.Ticket, requeue bool) {
			if !requeue {
				r.announce = r.startQueueAnnouncer(ticket)
			}
		},
		// Nothing to protect here: the slot is being handed straight back, and
		// the caller posts a failure notice into the same message next, so the
		// announcer must be finished with it.
		OnRefused: func(error) {
			if r.announce != nil {
				r.announce.dropBanner()
				r.announce.wait()
			}
		},
		// Hand the banner to the announcer instead of posting it here.
		// Everything between the grant and the agent start runs with an account
		// slot held, so a platform round-trip in this window is a slot burning
		// idle. The web mirror's counterpart to postBanner: the prompt stops
		// being queued and starts running, in the same order the browser path
		// reports it.
		OnGranted: func() {
			if r.announce != nil {
				r.announce.postBanner(thinkingStatusText)
			}
			r.leaveQueue(agentCtx)
		},
		Describe: imAdmissionError,
	})
	if err != nil {
		return nil, nil, err
	}
	return lease.Release, lease.Gate(), nil
}

// imQueueAnnouncer owns every write to the thread's progress message while a
// run waits for an account slot: the queue-position updates, then the single
// banner that supersedes them once a slot is granted.
//
// It exists so that no IM round-trip sits between the slot grant and the agent
// start. Both platform calls are bounded by respondBestEffort's budget rather
// than by anything the pool knows about, and both used to happen after the
// ticket had already been granted — a wedged Slack/Feishu API could hold an
// account slot for half a minute with nothing executing on it, which is pure
// throughput loss for every other run queued on that account.
//
// The ordering the inline version provided is kept rather than dropped:
//   - the banner still follows every queue position, because one goroutine
//     posts both, in that order;
//   - a run that never won a slot still never claims it started (dropBanner);
//   - the progress message still has a single writer at a time. The two later
//     writers both join wait() first — the agent stream through the gate in
//     streamResponder, the final reply/failure notice in run.
type imQueueAnnouncer struct {
	done   chan struct{}
	banner chan string
}

// startQueueAnnouncer spawns the announcer for this run. The goroutine ends on
// its own once the pool resolves the ticket and the caller has decided whether
// a banner is due, so it cannot outlive the turn or leak across a shutdown.
func (r *imRun) startQueueAnnouncer(ticket *service.Ticket) *imQueueAnnouncer {
	a := &imQueueAnnouncer{done: make(chan struct{}), banner: make(chan string, 1)}
	go func() {
		defer close(a.done)
		r.bridge.notifyIMQueuePositions(ticket, r.conversation.ID, r.msg, r.responder)
		text, ok := <-a.banner
		if !ok {
			return
		}
		r.bridge.respondBestEffort(r.msg, func(statusCtx context.Context) error {
			return r.responder.Update(statusCtx, text)
		})
	}()
	return a
}

// postBanner hands over the status text that supersedes the queue positions.
// The channel is buffered, so the caller hands off without waiting for the
// platform.
func (a *imQueueAnnouncer) postBanner(text string) {
	a.banner <- text
	close(a.banner)
}

// dropBanner ends the announcer with no banner: a run that never got a slot
// must not tell the thread that the agent started.
func (a *imQueueAnnouncer) dropBanner() { close(a.banner) }

// wait blocks until the announcer has stopped writing to the progress message.
// Nil-safe because the announcer only exists once the run reached the account
// pool, which lets callers join unconditionally.
func (a *imQueueAnnouncer) wait() {
	if a == nil {
		return
	}
	<-a.done
}

// announcerGatedResponder holds the agent stream's first progress write until
// the queue announcer has let go of the message. Without the gate the two
// goroutines would interleave text the reader has to un-see, and would race on
// the platform message id a Responder caches on its first successful post.
// Only Update is gated: it is the sole progress write the stream makes, and the
// gate must not delay the final Complete beyond run's own join.
type announcerGatedResponder struct {
	imbot.Responder
	announced <-chan struct{}
}

func (g *announcerGatedResponder) Update(ctx context.Context, text string) error {
	<-g.announced
	return g.Responder.Update(ctx, text)
}

// imAdmissionError is the IM thread's wording for an admission refusal.
func imAdmissionError(err error) error {
	var cooling *service.CooldownError
	var entry *service.PoolEntryError
	var timeout *service.QueueTimeoutError
	switch {
	case errors.Is(err, service.ErrTurnPaused):
		return errors.New("管理员已暂停处理新任务，请稍后重试")
	case errors.Is(err, service.ErrTurnBusy):
		return errors.New("该话题已有任务正在运行")
	case errors.Is(err, service.ErrTurnDraining):
		return errors.New("服务正在重启，请稍后重试")
	case errors.As(err, &cooling):
		return fmt.Errorf("账号限流中，请 %s 后重试", time.Until(cooling.Until).Round(time.Minute))
	case errors.As(err, &entry):
		return fmt.Errorf("账号不可用: %w", entry.Err)
	case errors.As(err, &timeout):
		return errors.New("等待账号空闲超时，请稍后重试")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// A cancel from the web UI and the queue budget both end the wait. A
		// user's own cancel reported as a timeout tells them to retry
		// something they deliberately stopped, so only the admission's own
		// budget (above) reads as a timeout.
		return errors.New("排队期间已取消")
	}
	return err
}

// activityOwnerID names whose conversation-activity feed this run belongs in:
// the owning login user, or the agent itself when it is unowned.
func (r *imRun) activityOwnerID() string {
	if owner := r.agentUser.Owner(); owner != "" {
		return owner
	}
	return r.agentUser.ID
}

// execute builds the backend request, streams the agent, and persists the
// session + thread binding. agentCtx bounds the agent process; ctx (the
// longer-lived run context) covers the follow-up persistence writes.
func (r *imRun) execute(agentCtx, ctx context.Context, slotGates ...*service.TurnSlotGate) (string, error) {
	var slotGate *service.TurnSlotGate
	if len(slotGates) > 0 {
		slotGate = slotGates[0]
	}
	// Materialize the case before the request is built: the doc is what the
	// prompt is assembled from, and the baseline the post-run read-back
	// compares against.
	r.prepareCase(ctx)

	opts, cleanup := r.buildRunRequest(ctx)
	defer cleanup()

	backend := r.bridge.Backends.For(r.provider)
	workDir := r.workDir()
	sessionID := r.applySession(backend, workDir, &opts)

	// The case is attached only after applySession, because where it goes
	// depends on whether this turn opens a session or continues one. See
	// attachCase — putting a document that changes every turn at the front of
	// the prompt would invalidate the cached prefix behind it.
	prompt := r.attachCase(&opts, r.promptWithAttachments())

	content, artifacts, captured, runErr := r.bridge.runAndStream(agentCtx, backend, prompt, workDir, r.agentUser.ID, r.account.Name, opts, r.conversation.ID, r.msg, r.streamResponder(), imStreamOptions{
		slotGate:  slotGate,
		unbounded: r.unboundedTurn(),
	})
	r.artifacts = artifacts
	r.persistTurnBindings(ctx, sessionID, captured)
	// Detached for the same reason persistTurnBindings is: a preempted turn
	// arrives here with ctx already cancelled, and that is precisely the turn
	// whose case must not be dropped.
	if r.caseModeEnabled() {
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), imPersistTimeout)
		r.persistCase(persistCtx, r.caseDoc)
		cancelPersist()
	}

	if runErr != nil {
		return "", agentRunError(agentCtx, runErr)
	}
	return content, nil
}

// streamResponder is the responder the agent stream posts progress through. It
// waits out the queue announcer instead of the run waiting for it before the
// agent starts: by the time a progress frame exists the agent is already
// executing, so the join costs no account-slot time.
func (r *imRun) streamResponder() imbot.Responder {
	if r.announce == nil {
		return r.responder
	}
	return &announcerGatedResponder{Responder: r.responder, announced: r.announce.done}
}

// buildRunRequest assembles the backend request for this turn: the shared
// base (sandbox, runner watchdogs, owner environment included) plus the
// resolved model and the IM system prompt stack. ctx bounds the owner lookups
// behind the sandbox tier and environment, and the sibling-bot lookup behind
// the handoff prompt.
func (r *imRun) buildRunRequest(ctx context.Context) (agent.RunRequest, func()) {
	b := r.bridge
	// The jail root is the same directory the turn actually runs in.
	opts, cleanup := service.NewOneshotRunRequest(ctx, b.Store, r.agentUser, r.account, b.Cfg,
		b.Sandbox, r.workDir(), service.SandboxUnrestrictedFor(ctx, b.Store, r.agentUser))
	opts.Model = r.model
	service.AppendSystem(&opts, imSystemPrompt(r.msg))
	service.AppendSystem(&opts, service.IMArtifactSystemPrompt)
	service.AppendSystem(&opts, b.handoffSystemPrompt(ctx, r.msg))
	service.AppendSystem(&opts, strings.TrimSpace(r.rule.ExtraPrompt))
	return opts, cleanup
}

// workDir is the agent's configured directory, falling back to the server's own
// cwd for an agent that has none.
func (r *imRun) workDir() string {
	if r.agentUser.WorkDir != "" {
		return r.agentUser.WorkDir
	}
	dir, _ := os.Getwd()
	return dir
}

// applySession decides which CLI session the turn continues — the thread's,
// or a new one — and records it on opts.
func (r *imRun) applySession(backend agent.Backend, workDir string, opts *agent.RunRequest) string {
	return service.PrepareRunSession(backend, workDir, r.thread.SessionID, opts)
}

// promptWithAttachments lists the inbound images saved into the work dir after
// the prompt, since the CLI can only reach them by path.
func (r *imRun) promptWithAttachments() string {
	prompt := r.bridge.buildPromptWithThreadContext(r.msg, r.threadContext)
	if len(r.attachments) == 0 {
		return prompt
	}
	paths := make([]string, 0, len(r.attachments))
	for _, attachment := range r.attachments {
		paths = append(paths, attachment.LocalPath)
	}
	return strings.TrimSpace(prompt + "\n\nAttached files:\n" + strings.Join(paths, "\n"))
}

// persistTurnBindings records what the turn learned — the backend-assigned CLI
// session id and how far the thread has been imported.
//
// These two writes must outlive the turn context: preemption (beginIMTurn) and
// a web-side stop both cancel it, and database/sql refuses a cancelled context,
// so exactly when a turn is interrupted the session id would be dropped (the
// next turn starts a fresh CLI session, losing the conversation) and
// LastMessageID would not advance (the thread history is imported again).
// Preemption is the documented path for a follow-up message, so this is the
// common case, not the edge case.
//
// What makes that safe is subtle enough to spell out: detaching the *context*
// does not detach the *goroutine*. Both writes still run inline, so they finish
// inside the per-thread mutex HandleMessage holds across the whole run (see
// IMBridge.threadLock). That mutex is the ONLY thing serialising a preempted
// turn's cursor write against its successor's syncThreadCursor — the preempted
// turn writes an older msg.MessageID, so an interleaving would rewind
// LastMessageID and re-import thread history the next turn.
// Consequences: do not move these writes onto a goroutine that outlives run(),
// and do not narrow HandleMessage's lock to only cover the conversation lookup.
func (r *imRun) persistTurnBindings(ctx context.Context, sessionID, capturedSessionID string) {
	b := r.bridge
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), imPersistTimeout)
	defer cancelPersist()

	if capturedSessionID != "" {
		sessionID = capturedSessionID
		if err := b.Store.SetSessionID(persistCtx, r.conversation.ID, capturedSessionID); err != nil {
			log.Printf("imbot: persist conversation session: %v", err)
		}
	}

	if err := b.Store.UpsertBotThread(persistCtx, store.BotThread{
		Platform:       threadPlatform(r.msg),
		ChannelID:      r.msg.ChannelID,
		ThreadID:       r.msg.ThreadID,
		AgentID:        r.agentUser.ID,
		ConversationID: r.conversation.ID,
		SessionID:      sessionID,
		Provider:       r.provider,
		Model:          r.model,
		LastMessageID:  r.msg.MessageID,
	}); err != nil {
		log.Printf("imbot: persist thread binding: %v", err)
	}
}

// agentRunError maps a stream failure onto the notice posted in the thread. The
// deadline is checked on the turn context too: the streamer reports a killed
// child process as a plain command failure, so only the context still knows the
// operator-configured IM run limit is what fired.
func agentRunError(agentCtx context.Context, runErr error) error {
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(agentCtx.Err(), context.DeadlineExceeded) {
		return errors.New("agent 运行超时，已中止")
	}
	return fmt.Errorf("agent 运行失败: %w", runErr)
}

// notifyIMQueuePositions mirrors the account-pool queue into both places an
// IM-originated task is visible. The pool can rebroadcast an unchanged
// position when an unrelated waiter moves, so only the initial position and
// genuine decreases are surfaced to avoid noisy duplicate notifications.
func (b *IMBridge) notifyIMQueuePositions(ticket *service.Ticket, conversationID string, msg imbot.Message, responder imbot.Responder) {
	lastPosition := 0
	for position := range relayIMQueuePositions(ticket.Positions()) {
		if position <= 0 || (lastPosition > 0 && position >= lastPosition) {
			continue
		}
		lastPosition = position
		ahead, running := ticket.QueueCounts(position)
		text := fmt.Sprintf(queueStatusTextf, ahead, running)
		b.broadcastIM(conversationID, service.ServerMessage{
			Type:          "queue_status",
			Status:        "queued",
			QueuePosition: position,
			QueueAhead:    &ahead,
			QueueRunning:  &running,
		})
		// A notice, not a progress preview: on a platform with no edit primitive
		// the queue position is the only thing distinguishing "waiting behind
		// other work" from "ignored you", and nothing later supersedes it.
		b.respondBestEffort(msg, func(statusCtx context.Context) error {
			return imbot.Notify(statusCtx, responder, text)
		})
	}
}

// relayIMQueuePositions drains the pool's deliberately small, non-blocking
// channel into an unbounded local FIFO. Platform API calls may take seconds;
// without this relay, a busy queue could advance several times while an
// update is in flight and the pool would correctly drop intermediate values.
// IM notifications promise to surface every decrease, so they need the full
// ordered sequence even when Slack or Feishu is temporarily slow.
func relayIMQueuePositions(input <-chan int) <-chan int {
	output := make(chan int)
	go func() {
		defer close(output)
		queue := make([]int, 0, 4)
		for input != nil || len(queue) > 0 {
			var send chan int
			var next int
			if len(queue) > 0 {
				send = output
				next = queue[0]
			}
			select {
			case position, ok := <-input:
				if !ok {
					input = nil
					continue
				}
				queue = append(queue, position)
			case send <- next:
				queue = queue[1:]
			}
		}
	}()
	return output
}

// runAndStream drives one IM turn through the shared stream consumer. The
// consumer owns everything durable about the run (tool/thinking/assistant rows,
// token usage, rate-limit cooldown, retry rollback) and mirrors every
// frame to the linked web conversation; imProgress layers the thread's live
// progress message on top.
// imStreamOptions carries the per-turn knobs runAndStream's tests never need.
type imStreamOptions struct {
	slotGate *service.TurnSlotGate
	// unbounded lifts the consumption guardrail; see
	// service.AgentStreamRequest.Unbounded.
	unbounded bool
}

func (b *IMBridge) runAndStream(ctx context.Context, backend agent.Backend, prompt, workDir, ownerID, accountName string, opts agent.RunRequest, conversationID string, msg imbot.Message, responder imbot.Responder, extra ...imStreamOptions) (string, service.ArtifactOutcome, string, error) {
	progress := &imProgress{bridge: b, msg: msg, responder: responder}
	var o imStreamOptions
	if len(extra) > 0 {
		o = extra[0]
	}
	out := b.streamer().Run(ctx, service.AgentStreamRequest{
		Backend:        backend,
		Prompt:         prompt,
		WorkDir:        workDir,
		ConversationID: conversationID,
		OwnerID:        ownerID,
		AccountName:    accountName,
		UserInitiated:  !progress.msg.FromBot,
		Opts:           opts,
		// One thread reply carries the whole turn, sub-agent chatter stays out
		// of the progress message, and the push notification is skipped because
		// the thread reply is already the delivery.
		Mode:            service.AgentStreamAggregate,
		EmptyResultText: imbot.EmptyReplyText,
		Broadcast:       func(frame service.ServerMessage) { b.broadcastIM(conversationID, frame) },
		OnFrame:         progress.observe,
		OnGuardrail:     progress.guardrail,
		SlotGate:        o.slotGate,
		Unbounded:       o.unbounded,
	})
	if out.Err != nil {
		return "", service.ArtifactOutcome{}, out.SessionID, out.Err
	}
	return out.Content, service.ArtifactOutcome{Published: out.Artifacts, Rejected: out.RejectedArtifacts}, out.SessionID, nil
}

func (b *IMBridge) streamer() *service.AgentStreamer {
	var guardrails config.GuardrailsConfig
	if b.Cfg != nil {
		guardrails = b.Cfg.Guardrails
	}
	return &service.AgentStreamer{
		Store:              b.Store,
		Broadcaster:        b.Broadcaster,
		Pool:               b.Pool,
		Persist:            b.MessagePersister(),
		Guardrails:         guardrails,
		GuardrailReminders: b.GuardrailReminders,
	}
}

// imProgress renders the thread's single progress message from the agent
// stream: a status banner on the first line, the turn's output below it.
// Platform edits are rate-limited (imThinkingUpdateInterval) because both Slack
// and Feishu throttle message updates.
//
// Thinking and reply text arrive on two independent streams, so choosing the
// body by "whichever delta came last" made the message alternate between the
// reasoning tail and the answer tail every few seconds — the reader watched two
// unrelated texts swap in place. Both are shown at once instead, in a fixed
// order (see imProgressBody), and only the banner tracks the phase. The banner
// stays on top so it survives the platform's "show more" fold on a long
// preview.
type imProgress struct {
	bridge    *IMBridge
	msg       imbot.Message
	responder imbot.Responder

	phase      string
	lastUpdate time.Time
	// thinking latches the reasoning stream: the streamer resets its thinking
	// buffer every time it persists a result, and a turn that produces several
	// results would otherwise blank the reasoning section mid-flight.
	thinking string
}

func (p *imProgress) observe(frame service.AgentStreamFrame) {
	switch frame.Event.Kind {
	case agent.KindResult:
		p.render(frame, "finalizing", finalizingStatusText, true)
	case agent.KindToolUseStart:
		p.render(frame, "tool:"+frame.Event.Content, toolCallStatusText, true)
	case agent.KindDelta:
		p.render(frame, "writing", replyStatusText, false)
	case agent.KindThinkingDelta:
		p.render(frame, "thinking", thinkingStatusText, false)
	}
}

func (p *imProgress) guardrail(snapshot service.GuardrailSnapshot) {
	p.bridge.respondBestEffort(p.msg, func(ctx context.Context) error {
		return imbot.Notify(ctx, p.responder, guardrailPromptText(snapshot))
	})
}

func guardrailPromptText(snapshot service.GuardrailSnapshot) string {
	return "⚠️ " + snapshot.Prompt
}

// render rewrites the progress message. Milestones (a tool call, the wrap-up)
// are rare and mark the turn actually moving on, so they edit immediately but
// only once per phase. Delta-driven phases share one throttle clock: an agent
// that interleaves reasoning and text would otherwise spend an edit on every
// switch even though the body barely changed.
func (p *imProgress) render(frame service.AgentStreamFrame, phase, banner string, milestone bool) {
	if milestone {
		if phase == p.phase {
			return
		}
	} else if !p.lastUpdate.IsZero() && time.Since(p.lastUpdate) < imThinkingUpdateInterval {
		return
	}
	p.phase = phase
	p.lastUpdate = time.Now()
	p.update(imProgressText(banner, p.preview(frame)))
}

func (p *imProgress) preview(frame service.AgentStreamFrame) string {
	if frame.Thinking != "" {
		p.thinking = frame.Thinking
	}
	reply := frame.Reply
	if reply == "" {
		reply = frame.Result
	}
	return imProgressBody(p.thinking, reply)
}

func (p *imProgress) update(text string) {
	p.bridge.respondBestEffort(p.msg, func(statusCtx context.Context) error {
		return p.responder.Update(statusCtx, text)
	})
}

func outboundAttachments(artifacts []service.Artifact) []imbot.OutboundAttachment {
	attachments := make([]imbot.OutboundAttachment, 0, len(artifacts))
	for _, artifact := range artifacts {
		localPath := artifact.LocalPath
		attachments = append(attachments, imbot.OutboundAttachment{
			Name: artifact.Name,
			MIME: artifact.MIME,
			Size: artifact.Size,
			Open: func() (io.ReadCloser, error) {
				return os.Open(localPath)
			},
		})
	}
	return attachments
}

func imProgressText(banner, preview string) string {
	if preview == "" {
		return banner
	}
	return banner + "\n\n" + preview
}

// imProgressBody stacks the two streams the reader wants to follow at once:
// the reasoning above, what the agent has actually said below. The order is
// fixed, so neither stream can ever appear in the other's place — which is the
// whole point, since they update independently. Labels appear only when both
// halves are there; with a single stream the banner already names it.
func imProgressBody(thinking, reply string) string {
	replyTail := previewTail(reply, imReplyPreviewLimit)
	thinkingTail := previewTail(thinking, imProgressPreviewLimit-len([]rune(replyTail)))
	switch {
	case thinkingTail != "" && replyTail != "":
		return thinkingSectionLabel + "\n" + thinkingTail + "\n\n" + replySectionLabel + "\n" + replyTail
	case replyTail != "":
		return replyTail
	default:
		return thinkingTail
	}
}

// previewTail keeps the most recent limit runes — the tail, because that is
// where the agent currently is — and marks the cut so the reader knows the
// message is a window rather than the whole output.
func previewTail(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	content = strings.TrimSpace(content)
	runes := []rune(content)
	if len(runes) > limit {
		return "…" + string(runes[len(runes)-limit:])
	}
	return content
}
