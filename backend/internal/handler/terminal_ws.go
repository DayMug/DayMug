package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// handleWebSocket is the core WebSocket logic, also used directly in tests.
func (h *TerminalHandler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, ctx, cancel, ok := acceptWS(w, r, "websocket accept")
	if !ok {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	defer cancel()

	clientID := uuid.New().String()

	// sendCh is the per-client outbound channel; a writer goroutine drains it
	// into the WebSocket.
	sendCh := make(chan []byte, 128)
	writerDone := startWSWriter(ctx, conn, sendCh, func(data []byte) (websocket.MessageType, []byte) {
		return websocket.MessageText, data
	})

	// Keep-alive: without it, when the user comes back from a backgrounded
	// tab they'd have to wait through the frontend's exponential reconnect
	// backoff before the green dot returns. The read loop below is the
	// concurrent Reader that receives the pongs.
	startWSPing(ctx, cancel, conn, h.pingIntervalOrDefault())

	var state connState

	// Single teardown defer: leave the broadcast room first (so no other client's
	// runClaudeRequest can broadcast into our sendCh after this point), then close
	// sendCh and wait for the writer to drain. Order matters: this runs before
	// cancel() / conn.CloseNow because it's deferred last.
	//
	// Leaving the room does NOT abort an in-flight Claude job: the job's context
	// is independent of this WebSocket (see "input" handler) and the Broadcaster
	// keeps the room alive while busy so a reconnect can rejoin and replay the
	// stream. Explicit aborts go through Broadcaster.CancelJob.
	defer func() {
		if state.conversationID != "" {
			h.Broadcaster.Leave(state.conversationID, clientID)
		}
		if state.userID != "" {
			h.UserHub.Leave(state.userID, clientID)
		}
		close(sendCh)
		writerDone.Wait()
	}()

	writeJSON := func(msg serverMessage) {
		if state.conversationID != "" {
			if msg.ConversationID == "" {
				msg.ConversationID = state.conversationID
			}
			if msg.SubscriptionID == "" {
				msg.SubscriptionID = state.subscriptionID
			}
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return
		}
		select {
		case sendCh <- data:
		default:
		}
	}

	// broadcastExceptSelf echoes a sender-originated event to other clients
	// in the room so other tabs viewing the same conversation see it live,
	// without re-delivering it to the sender (whose UI already pushed it
	// optimistically).
	broadcastExceptSelf := func(convID string, msg serverMessage) {
		data, err := json.Marshal(msg)
		if err != nil {
			return
		}
		h.Broadcaster.BroadcastExcept(convID, clientID, data)
	}

	writeJSON(serverMessage{Type: "status", Status: "ready"})

	readCh := startWSReader(ctx, conn)
	drainCh := h.Drainer.DrainCtx().Done()

	for {
		select {
		case <-drainCh:
			// Tell the client we're going away; let any active Claude job
			// finish naturally. The connection will be force-closed by the
			// HTTP server's Close() call once the drain timeout expires.
			writeJSON(serverMessage{Type: "status", Status: "shutdown"})
			drainCh = nil // one-shot

		case rr, ok := <-readCh:
			if !ok || rr.err != nil {
				return
			}
			h.dispatchClientFrame(ctx, rr.data, &state, clientID, sendCh, writeJSON, broadcastExceptSelf)
		}
	}
}

// wsReadResult carries one conn.Read outcome from the reader goroutine to the
// frame loop.
type wsReadResult struct {
	data []byte
	err  error
}

// startWSReader pumps conn.Read results into a channel so the frame loop can
// select between drain notifications and incoming client messages. The channel
// is closed once the reader stops, which the loop reads as "connection gone".
func startWSReader(ctx context.Context, conn *websocket.Conn) <-chan wsReadResult {
	readCh := make(chan wsReadResult, 1)
	go func() {
		defer close(readCh)
		for {
			_, data, err := conn.Read(ctx)
			select {
			case readCh <- wsReadResult{data: data, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return readCh
}

// dispatchClientFrame decodes one inbound frame and routes it to the matching
// per-type handler. Returning from this function is equivalent to the frame
// loop's old `continue`: the connection stays open and we wait for the next
// frame.
func (h *TerminalHandler) dispatchClientFrame(
	ctx context.Context,
	raw []byte,
	state *connState,
	clientID string,
	sendCh chan []byte,
	writeJSON func(serverMessage),
	broadcastExceptSelf func(string, serverMessage),
) {
	var msg clientMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		writeJSON(serverMessage{Type: "error", Message: "invalid message format"})
		return
	}

	switch msg.Type {
	case "init":
		h.handleInitMsg(ctx, msg, state, clientID, sendCh, writeJSON)
	case "input":
		if !matchesSubscription(msg, state, writeJSON) {
			return
		}
		h.handleInputMsg(ctx, msg, state, writeJSON, broadcastExceptSelf)
	case "cancel":
		if !matchesSubscription(msg, state, writeJSON) {
			return
		}
		h.handleCancelMsg(ctx, msg, state, writeJSON, broadcastExceptSelf)
	case "question_response":
		if !matchesSubscription(msg, state, writeJSON) {
			return
		}
		h.handleQuestionResponse(ctx, msg, state, writeJSON)
	default:
		writeJSON(serverMessage{Type: "error", Message: "unknown message type"})
	}
}

func (h *TerminalHandler) handleQuestionResponse(ctx context.Context, msg clientMessage, state *connState, writeJSON func(serverMessage)) {
	if state.conversationID == "" {
		writeJSON(serverMessage{Type: "error", Message: "init required before answering a question"})
		return
	}
	if msg.RequestID == "" || len(msg.Answers) == 0 {
		writeJSON(serverMessage{Type: "error", Message: "invalid user question response"})
		return
	}
	answerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	attempted, err := h.Broadcaster.AnswerJobQuestion(answerCtx, state.conversationID, msg.RequestID, msg.Answers)
	cancel()
	if !attempted && h.answerSuspendedQuestion(ctx, msg, state) {
		return
	}
	if !attempted || err != nil {
		writeJSON(serverMessage{Type: "error", Message: "the user question is no longer active"})
		return
	}
	data, err := json.Marshal(serverMessage{Type: "user_question_resolved", RequestID: msg.RequestID})
	if err == nil {
		h.Broadcaster.Broadcast(state.conversationID, data)
	}
}

// offerSuspendedQuestion shows again a question a restart ended the turn on.
// Only while the room is idle: a running turn owns the card through its own
// frames, and any new turn clears the stored question when it starts.
func (h *TerminalHandler) offerSuspendedQuestion(ctx context.Context, state *connState, writeJSON func(serverMessage)) {
	if h.Store == nil || state.conversationID == "" || h.Broadcaster.IsBusy(state.conversationID) {
		return
	}
	payload, err := h.Store.GetConversationSuspendedQuestion(ctx, state.conversationID)
	if err != nil || payload == "" {
		return
	}
	writeJSON(serverMessage{
		Type:           "user_question",
		Content:        payload,
		ConversationID: state.conversationID,
		SubscriptionID: state.subscriptionID,
	})
}

// answerSuspendedQuestion routes an answer that found no live turn to the
// question a restart may have left unanswered.
func (h *TerminalHandler) answerSuspendedQuestion(ctx context.Context, msg clientMessage, state *connState) bool {
	convID := state.conversationID
	return h.PromptIntake().AnswerSuspendedQuestion(ctx, convID, state.userID, msg.RequestID, msg.Answers,
		func(saved store.Message) {
			// Every tab, this one included: none of them staged this text.
			if data, err := json.Marshal(serverMessage{
				Type:           "user_message",
				Content:        saved.Content,
				ConversationID: convID,
				MessageID:      saved.ID,
				Metadata:       saved.Metadata,
			}); err == nil {
				h.Broadcaster.Broadcast(convID, data)
			}
		})
}

// matchesSubscription rejects frames aimed at a conversation/subscription this
// connection has already moved off of, so a late frame from the pre-switch UI
// state can't drive the newly-joined conversation.
func matchesSubscription(msg clientMessage, state *connState, writeJSON func(serverMessage)) bool {
	conversationMatches := msg.ConversationID == "" || msg.ConversationID == state.conversationID
	subscriptionMatches := msg.SubscriptionID == "" || msg.SubscriptionID == state.subscriptionID
	if conversationMatches && subscriptionMatches {
		return true
	}
	writeJSON(serverMessage{
		Type:           "error",
		Message:        "stale conversation subscription",
		ConversationID: msg.ConversationID,
		SubscriptionID: msg.SubscriptionID,
	})
	return false
}

// handleInputMsg submits a user prompt through service.PromptIntake and
// renders the frames: the echo to peer tabs and this tab's ack once the prompt
// is durable, and the stale-Insert error when a steer found no live turn.
func (h *TerminalHandler) handleInputMsg(
	ctx context.Context,
	msg clientMessage,
	state *connState,
	writeJSON func(serverMessage),
	broadcastExceptSelf func(string, serverMessage),
) {
	sub := service.PromptSubmission{
		ConversationID: state.conversationID,
		UserID:         state.userID,
		Content:        msg.Content,
		Steer:          msg.Steer,
		OnQueued: func(saved store.Message) {
			broadcastExceptSelf(state.conversationID, serverMessage{
				Type:           "user_message",
				Content:        saved.Content,
				ConversationID: state.conversationID,
				MessageID:      saved.ID,
				Metadata:       saved.Metadata,
			})
			writeJSON(serverMessage{
				Type:           "input_ack",
				ConversationID: state.conversationID,
				MessageID:      saved.ID,
			})
		},
		OnStaleSteer: func(err error) {
			writeJSON(serverMessage{
				Type:           "error",
				Message:        err.Error(),
				ConversationID: state.conversationID,
			})
		},
	}
	if len(msg.Attachments) > 0 {
		sub.AttachmentMetadata = func(agentID, homeOwnerID string) (json.RawMessage, error) {
			return buildWebUploadMetadata(agentID, homeOwnerID, msg.Attachments)
		}
	}
	if _, err := h.PromptIntake().Submit(ctx, sub); err != nil {
		var svcErr *service.ServiceError
		message := "failed to enqueue prompt"
		if errors.As(err, &svcErr) {
			message = svcErr.Message()
		}
		writeJSON(serverMessage{Type: "error", Message: message})
	}
}

// handleCancelMsg implements the two cancel shapes:
//   - msg.MessageID set → narrow cancel: drop ONLY that pending prompt, leave
//     the in-flight job alone. Powers the per-message recall affordance in the
//     staging-area UI.
//   - msg.MessageID empty → broad cancel: abort the currently-running claude
//     job (if any) AND drop every still-pending prompt for the conversation.
//     Legacy "Cancel" button behaviour.
func (h *TerminalHandler) handleCancelMsg(
	ctx context.Context,
	msg clientMessage,
	state *connState,
	writeJSON func(serverMessage),
	broadcastExceptSelf func(string, serverMessage),
) {
	if msg.MessageID != "" {
		if h.Dispatcher != nil && state.conversationID != "" {
			cancelCtx, cancelCancel := context.WithTimeout(ctx, 10*time.Second)
			dropped, err := h.Dispatcher.CancelOnePending(cancelCtx, msg.MessageID)
			cancelCancel()
			if err != nil {
				// ErrNotFound is expected when the worker
				// claimed the prompt before the user's
				// recall click landed; ack with an empty
				// list so the client can clear its
				// optimistic copy if it had one.
				log.Printf("[terminal] cancel one id=%s conv=%s: %v",
					msg.MessageID, state.conversationID, err)
				writeJSON(serverMessage{
					Type:           "cancel_ack",
					ConversationID: state.conversationID,
				})
				return
			}
			ack := serverMessage{
				Type:             "cancel_ack",
				ConversationID:   state.conversationID,
				CancelledPrompts: []store.Message{dropped},
			}
			writeJSON(ack)
			broadcastExceptSelf(state.conversationID, ack)
		}
		return
	}
	h.Broadcaster.CancelJob(state.conversationID)
	// A turn that ended with background work still running leaves its agent
	// process parked with no job to cancel; Stop ends that work too.
	agent.StopResident(state.conversationID)
	if h.Dispatcher != nil && state.conversationID != "" {
		cancelCtx, cancelCancel := context.WithTimeout(ctx, 10*time.Second)
		dropped, err := h.Dispatcher.CancelPending(cancelCtx, state.conversationID)
		cancelCancel()
		if err != nil {
			log.Printf("[terminal] cancel pending conv=%s: %v", state.conversationID, err)
		}
		ack := serverMessage{
			Type:             "cancel_ack",
			ConversationID:   state.conversationID,
			CancelledPrompts: dropped,
		}
		writeJSON(ack)
		broadcastExceptSelf(state.conversationID, ack)
	}
}

// handleInitMsg processes an "init" client message, joining the conversation room
// and loading user/conversation state from the store.
func (h *TerminalHandler) handleInitMsg(
	ctx context.Context,
	msg clientMessage,
	state *connState,
	clientID string,
	sendCh chan []byte,
	writeJSON func(serverMessage),
) {
	authedUID, ok := h.authorizeInit(ctx, &msg, writeJSON)
	if !ok {
		return
	}

	// Validation succeeded: atomically advance the logical subscription before
	// emitting init_ok. Leaving the old room stops future broadcasts, while the
	// subscription id lets the frontend reject anything already queued for the
	// old room on this same physical WebSocket.
	if state.conversationID != "" && state.conversationID != msg.ConversationID {
		h.Broadcaster.Leave(state.conversationID, clientID)
	}
	state.conversationID = msg.ConversationID
	state.subscriptionID = msg.SubscriptionID
	// These values belong to the previous init and must never leak across an
	// agent/conversation switch if a lookup below fails or returns no workdir.
	state.userWorkDir = ""
	state.currentUser = nil
	activityOwnerID := authedUID
	if activityOwnerID == "" {
		activityOwnerID = msg.UserID
	}
	initSnapshot := service.ConversationActivitySnapshot(ctx, h.Drainer, h.Store, activityOwnerID)
	initSnapshot.Type = "init_ok"
	initSnapshot.ConversationID = state.conversationID
	writeJSON(initSnapshot)

	h.sendHistoryBackfill(ctx, state.conversationID, msg.LastMessageID, writeJSON)
	h.joinConversationRoom(state, clientID, sendCh, writeJSON)
	h.offerSuspendedQuestion(ctx, state, writeJSON)
	h.bindUserSession(ctx, msg, state, authedUID, clientID, sendCh)
	h.applyConversationSettings(ctx, state, writeJSON)
}

// authorizeInit resolves the effective user id for an `init` frame and rejects
// the two cross-tenant escapes. Returns the authenticated id (empty in tests
// that bypass the middleware) and false when the frame was rejected — in which
// case the error frame has already been written.
func (h *TerminalHandler) authorizeInit(
	ctx context.Context,
	msg *clientMessage,
	writeJSON func(serverMessage),
) (string, bool) {
	// Reject `init` if the client is asking us to act as another *human* user
	// (one with a username, i.e. independently login-able). Acting as an
	// agent persona — a User row with no username, created by the human
	// through the settings UI — is allowed: the authenticated human is the
	// one driving the chat, the agent just supplies the system prompt /
	// tool config.
	//
	// Tests bypass the middleware and may not set this context value, in
	// which case the authedUID is empty and we fall through to the legacy
	// lookup.
	authedUID, _ := ctx.Value(authedUserCtxKey{}).(string)
	if authedUID != "" {
		if msg.UserID != "" && !service.CanAccessOwner(ctx, h.Store, authedUID, msg.UserID) {
			writeJSON(serverMessage{
				Type:           "error",
				Message:        "user_id mismatch",
				ConversationID: msg.ConversationID,
				SubscriptionID: msg.SubscriptionID,
			})
			return "", false
		}
		if msg.UserID == "" {
			msg.UserID = authedUID
		}
	}

	// Block joining another human's conversation room. Without this an authed
	// session that knows a foreign conversation UUID could subscribe to its
	// broadcast stream and even drive Claude requests against it. Agents (User
	// rows with no username) are shared between the humans who manage them, so
	// their conversations remain reachable.
	if h.Store != nil && msg.ConversationID != "" {
		conv, err := h.Store.GetConversation(ctx, msg.ConversationID)
		if err == nil && !service.CanAccessOwner(ctx, h.Store, authedUID, conv.UserID) {
			writeJSON(serverMessage{
				Type:           "error",
				Message:        "conversation not accessible",
				ConversationID: msg.ConversationID,
				SubscriptionID: msg.SubscriptionID,
			})
			return "", false
		}
	}
	return authedUID, true
}

// sendHistoryBackfill replays messages persisted while the client was offline.
// Sent before Join so the broadcaster's in-flight replay (and any subsequent
// live events) layer on top of an already-canonical history; the frontend's
// history_backfill handler dedups by id against any REST-loaded items.
//
// Empty cursor is intentionally NOT a short-circuit: the store treats it as
// "send everything", which is the recovery path for a reconnecting client that
// never anchored a cursor (e.g. a brand-new conversation whose first user
// message was sent right before going offline). For a first connect with a
// non-empty REST snapshot the frontend always sends a cursor, so the backfill
// returns 0 rows and no event is emitted.
func (h *TerminalHandler) sendHistoryBackfill(
	ctx context.Context,
	convID, lastMessageID string,
	writeJSON func(serverMessage),
) {
	if h.Store == nil || convID == "" {
		return
	}
	if backfill, err := h.Store.ListMessagesAfter(ctx, convID, lastMessageID, 500); err == nil && len(backfill) > 0 {
		if conv, convErr := h.Store.GetConversation(ctx, convID); convErr == nil {
			backfill = rebaseMessageArtifacts(ctx, h.Store, conv, backfill)
		}
		writeJSON(serverMessage{
			Type:           "history_backfill",
			ConversationID: convID,
			Messages:       backfill,
		})
	}
}

// joinConversationRoom subscribes the connection to the conversation's
// broadcast room and pushes the authoritative status snapshot.
func (h *TerminalHandler) joinConversationRoom(
	state *connState,
	clientID string,
	sendCh chan []byte,
	writeJSON func(serverMessage),
) {
	if state.conversationID == "" {
		return
	}
	h.Broadcaster.JoinSubscription(
		state.conversationID,
		clientID,
		state.subscriptionID,
		sendCh,
	)

	// Authoritative status snapshot for this conversation, sent on every
	// init regardless of whether the room is currently busy. The snapshot
	// heals the case where a tab missed `status: ready` during a WS gap:
	// the run finished, EndJob cleared the replay buffer, and the next
	// init's join finds an empty room — without this push the client
	// would keep its stale "thinking" indicator forever. Distinct event
	// type (init_status, not status) so existing tests that count
	// `status: ready` events around send/recv aren't perturbed by an
	// extra post-init frame.
	snapshot := "ready"
	if h.Broadcaster.IsBusy(state.conversationID) {
		snapshot = "thinking"
	}
	writeJSON(serverMessage{
		Type:           "init_status",
		Status:         snapshot,
		ConversationID: state.conversationID,
	})
}

// bindUserSession loads the acting user's config onto the connection state and
// joins the user-scoped hub room.
func (h *TerminalHandler) bindUserSession(
	ctx context.Context,
	msg clientMessage,
	state *connState,
	authedUID, clientID string,
	sendCh chan []byte,
) {
	// Look up user config via user_id from init message
	if h.Store != nil && msg.UserID != "" {
		if user, err := h.Store.GetUser(ctx, msg.UserID); err == nil {
			state.userWorkDir = user.WorkDir
			state.currentUser = &user
		}
	}

	// Subscribe this WebSocket to its user's hub so cross-tab session
	// list updates land here even when the per-conversation room would
	// not deliver them (e.g. another tab created or deleted a session
	// belonging to a different conversation). We prefer the
	// authenticated id over the init payload's UserID because acting
	// as an agent persona should still notify the underlying human.
	hubUserID := authedUID
	if hubUserID == "" {
		hubUserID = msg.UserID
	}
	if hubUserID != "" && state.userID != hubUserID {
		if state.userID != "" {
			h.UserHub.Leave(state.userID, clientID)
		}
		h.UserHub.Join(hubUserID, clientID, sendCh)
		state.userID = hubUserID
	}
}

// applyConversationSettings overlays the conversation's own work_dir on the
// user default and replays the persisted context_usage bar.
//
// We reuse this fetch as the source for the persisted last context_usage so a
// fresh client (server restart, new browser, localStorage cleared) sees the
// token bar without a round-trip.
func (h *TerminalHandler) applyConversationSettings(
	ctx context.Context,
	state *connState,
	writeJSON func(serverMessage),
) {
	if h.Store == nil || state.conversationID == "" {
		return
	}
	conv, err := h.Store.GetConversation(ctx, state.conversationID)
	if err != nil {
		return
	}
	if conv.WorkDir != "" {
		state.userWorkDir = conv.WorkDir
	}
	h.replayDBContextUsage(state.conversationID, conv.LastContextUsage, writeJSON)
	h.replayRateLimits(ctx, state, conv, writeJSON)
}

// replayRateLimits pushes the server's last plan-window readings for the
// conversation's account, so a browser that has never run a turn on it (a new
// device, cleared storage) shows the quota badge straight away. The frames
// carry observed_at; the client ignores one older than what it already holds.
func (h *TerminalHandler) replayRateLimits(
	ctx context.Context,
	state *connState,
	conv store.Conversation,
	writeJSON func(serverMessage),
) {
	if h.Pool == nil {
		return
	}
	var owner store.User
	if state.currentUser != nil && state.currentUser.ID == conv.UserID {
		owner = *state.currentUser
	} else if u, err := h.Store.GetUser(ctx, conv.UserID); err == nil {
		owner = u
	} else {
		return
	}
	accountName, err := service.ResolveConversationAccount(owner, conv.Provider, conv.AccountName)
	if err != nil {
		return
	}
	for _, payload := range h.Pool.RateLimitSnapshot(accountName) {
		writeJSON(serverMessage{
			Type:           "rate_limit",
			Content:        payload,
			ConversationID: state.conversationID,
			SubscriptionID: state.subscriptionID,
		})
	}
}

// replayDBContextUsage pushes a persisted context_usage payload to a
// freshly-connected client when the in-memory broadcaster cache is empty
// (i.e. this conversation hasn't yet seen a live usage event in this
// process). Also seeds the broadcaster so peer tabs joining shortly after
// can pick it up via the standard Join path. Skips when the DB row is
// empty (no usage observed yet) or the broadcaster already has a fresher
// cached frame.
func (h *TerminalHandler) replayDBContextUsage(
	convID, payload string,
	writeJSON func(serverMessage),
) {
	if convID == "" || payload == "" {
		return
	}
	if h.Broadcaster.HasContextUsage(convID) {
		return
	}
	msg := serverMessage{Type: "context_usage", Content: payload}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.Broadcaster.SetLastContextUsage(convID, data)
	writeJSON(msg)
}
