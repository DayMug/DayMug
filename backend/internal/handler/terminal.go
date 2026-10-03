package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// authedUserCtxKey carries the authenticated user's ID across the gin/WS
// boundary so the WebSocket reader loop can validate `init` messages.
type authedUserCtxKey struct{}

type clientMessage struct {
	Type           string `json:"type"`
	Content        string `json:"content,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	// Steer is set only by the composer's explicit "Insert" action. Regular
	// sends stay in the durable FIFO until the current turn finishes; a steer
	// asks a capable backend to append this message to the live turn instead.
	Steer bool `json:"steer,omitempty"`
	// SubscriptionID changes on every logical conversation switch while the
	// browser keeps one physical WebSocket alive. The server echoes it on all
	// conversation-scoped frames so the client can reject stale buffered data.
	SubscriptionID string `json:"subscription_id,omitempty"`
	// LastMessageID, when set on an "init" message, asks the server to
	// replay any persisted messages newer than this id. Used by the
	// frontend reconnect path so a tab that was offline while a Claude
	// turn finished doesn't end up with a stale chat. Empty on first
	// connect (the client just did a full REST snapshot).
	LastMessageID string `json:"last_message_id,omitempty"`
	// MessageID, when set on a "cancel" message, narrows the cancel to a
	// single pending prompt — used by the staging-area per-message recall
	// affordance. Empty cancels the in-flight job and drops every still-
	// pending prompt (the legacy whole-conversation cancel).
	MessageID string              `json:"message_id,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
	Answers   map[string][]string `json:"answers,omitempty"`
	// Attachments is populated by the web composer for uploaded files. The
	// input handler validates each relative path and rebuilds its authenticated
	// read URL before persisting it; client-provided URLs are never trusted.
	Attachments []webUploadAttachment `json:"attachments,omitempty"`
}

// serverMessage is the wire shape of every server→client WS event. The
// canonical definition moved to service.ServerMessage so the prompt-running
// pipeline (service.PromptRunner) can produce frames; the alias keeps the
// handler-local name for the WS glue in this package.
type serverMessage = service.ServerMessage

// TerminalHandler is the chat WebSocket and /compact surface. Every shared
// collaborator (store, backends, pool, sandbox, broadcaster, dispatcher, ...)
// comes from the embedded Runtime, which the cron scheduler and IM bridge
// share; the handler itself only owns per-connection concerns.
type TerminalHandler struct {
	*service.Runtime
	// PingInterval controls the WebSocket keep-alive ping cadence. Zero
	// falls back to defaultPingInterval. Tests override it to a small value
	// to exercise the keep-alive path without sleeping.
	PingInterval time.Duration
}

const defaultPingInterval = 30 * time.Second

func (h *TerminalHandler) pingIntervalOrDefault() time.Duration {
	if h.PingInterval > 0 {
		return h.PingInterval
	}
	return defaultPingInterval
}

// NewTerminalHandler binds the chat surface to the shared runtime. The
// runtime's Dispatcher already routes queued prompts through
// Runtime.ProcessPrompt, so nothing here has to be wired afterwards.
func NewTerminalHandler(rt *service.Runtime) *TerminalHandler {
	return &TerminalHandler{Runtime: rt}
}

// connState holds mutable per-connection state that is updated as messages arrive.
type connState struct {
	conversationID string
	subscriptionID string
	userWorkDir    string
	currentUser    *store.User
	// userID tracks which user-hub room this connection has joined so
	// the teardown defer knows who to leave. Mirrors currentUser.ID
	// when known but is also populated for the agent-persona case
	// where the WS init resolved the owning user but doesn't keep the
	// full row around.
	userID string
}

// HandleTerminal handles WebSocket connections via Gin.
func (h *TerminalHandler) HandleTerminal(c *gin.Context) {
	// Pass the authenticated user id through the request context so the
	// WS handler can refuse `init` messages that target a different user.
	uid := middleware.CurrentUserID(c)
	ctx := context.WithValue(c.Request.Context(), authedUserCtxKey{}, uid)
	h.handleWebSocket(c.Writer, c.Request.WithContext(ctx))
}
