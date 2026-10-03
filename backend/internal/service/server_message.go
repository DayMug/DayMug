package service

import (
	"encoding/json"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// RunningConversationActivity is the user-facing detail behind the compact
// activity snapshots. Running and queued jobs use separate arrays on the wire
// so older clients keep their historical running-count semantics.
type RunningConversationActivity struct {
	ConversationID string    `json:"conversation_id"`
	AgentID        string    `json:"agent_id"`
	AgentName      string    `json:"agent_name"`
	OwnerName      string    `json:"owner_name,omitempty"`
	OwnerUsername  string    `json:"owner_username,omitempty"`
	AccountName    string    `json:"account_name"`
	Model          string    `json:"model"`
	StartedAt      time.Time `json:"started_at"`
}

// ServerMessage is the wire shape of every server→client WebSocket event.
// It lives in service (rather than handler) because the prompt-processing
// pipeline — which runs detached from any individual connection — produces
// these frames too; the handler package aliases it as `serverMessage` for
// its WS read/write glue.
type ServerMessage struct {
	Type    string `json:"type"`
	Content any    `json:"content,omitempty"`
	// ToolStartedAt is the server-observed Unix timestamp in milliseconds for
	// a tool_use_start frame. ToolDurationMS is attached to the matching
	// tool_result. Together they keep the live browser timer anchored to the
	// actual run and let it settle on the same value that is persisted.
	ToolStartedAt  int64  `json:"tool_started_at,omitempty"`
	ToolDurationMS *int64 `json:"tool_duration_ms,omitempty"`
	// Replay marks an in-flight event resent to a newly joined WebSocket.
	// Live clients use it to merge replayed text with the stream they already
	// rendered instead of appending the same delta after a reconnect.
	Replay bool `json:"replay,omitempty"`
	// Seq is a per-room monotonic counter stamped by Broadcaster.Broadcast on
	// every conversation frame, live and replayed alike. The fanout is
	// deliberately non-blocking (a slow client must never stall the stream),
	// so frames *are* dropped under backpressure; without a counter the client
	// has no way to notice. A client that sees seq jump forward knows its view
	// is incomplete and re-syncs persisted rows over REST.
	//
	// Zero (omitted) means "not sequenced" and must be ignored for gap
	// detection: frames written straight to one socket (init_ok,
	// history_backfill, input_ack), UserHub fanout, and the cached
	// context_usage tail replayed on join all travel without a seq.
	//
	// Counting restarts at 1 whenever a Room is recreated after idle GC, so
	// clients treat a non-increasing seq as a restart rather than a gap.
	Seq            uint64 `json:"seq,omitempty"`
	Status         string `json:"status,omitempty"`
	Message        string `json:"message,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	// SubscriptionID identifies the client's current logical conversation
	// subscription on a long-lived WebSocket. Conversation-scoped frames are
	// stamped per client by Broadcaster so delayed frames from an earlier
	// A→B→A switch can be discarded without recreating the physical socket.
	SubscriptionID string `json:"subscription_id,omitempty"`
	// Running ids carry the complete user-scoped activity snapshot on
	// conversation_activity frames. Full snapshots avoid ref-count drift when
	// multiple conversations for one agent finish close together.
	RunningAgentIDs        []string                      `json:"running_agent_ids,omitempty"`
	RunningConversationIDs []string                      `json:"running_conversation_ids,omitempty"`
	RunningConversations   []RunningConversationActivity `json:"running_conversations,omitempty"`
	QueuedConversations    []RunningConversationActivity `json:"queued_conversations,omitempty"`
	// WaitingConversations are turns parked on an AskUserQuestion prompt;
	// FailedConversationIDs are conversations whose latest turn recently
	// ended in an error. Together they let the navigation tell "finished",
	// "needs a reply" and "failed" apart without joining each room.
	WaitingConversations  []RunningConversationActivity `json:"waiting_conversations,omitempty"`
	FailedConversationIDs []string                      `json:"failed_conversation_ids,omitempty"`
	Title                 string                        `json:"title,omitempty"`
	// QueuePosition is the one-based FIFO position among tasks waiting for an
	// account slot. Active tasks are excluded, so the first task beyond the
	// concurrency limit has position 1. Zero is omitted from the JSON.
	QueuePosition int `json:"queue_position,omitempty"`
	// QueueAhead is the total number of tasks ahead of the queued task,
	// including both active tasks and earlier waiters. QueueRunning is the
	// active subset, allowing clients to explain the queue without knowing the
	// account's configured concurrency limit.
	//
	// Both are pointers because zero is a meaningful value here: "nothing is
	// ahead of you" must reach the client. A plain int with omitempty erased
	// it, and the frontend's fallback then reported one task too many. nil
	// still means "not a queue frame", so other event types stay unaffected.
	QueueAhead   *int `json:"queue_ahead,omitempty"`
	QueueRunning *int `json:"queue_running,omitempty"`
	// Messages carries a slice of persisted messages, populated on
	// type=history_backfill events sent right after init_ok when a
	// reconnecting client included a last_message_id cursor.
	Messages []store.Message `json:"messages,omitempty"`
	// Subagent flags events that originated from a sub-agent invocation
	// (parent_tool_use_id was set on the upstream stream-json line). The
	// frontend uses this to render sub-agent activity in a separate visual
	// track and to skip dedup paths that only apply to the parent's stream.
	Subagent bool `json:"subagent,omitempty"`
	// MessageID carries the persisted DB id of the row this event
	// represents:
	//   - input_ack: the user prompt's id (lets the client promote its
	//     optimistic row to the canonical id)
	//   - user_message (peer echo): the same id for cross-tab dedup
	//   - tool_result: the persisted tool row's id
	//   - result: the persisted assistant reply's id
	//   - error: the persisted error row's id (empty if save failed)
	//   - persist_failed: omitted (no row exists)
	// The client keys off MessageID for id-based dedup against REST
	// snapshots and history_backfill frames, replacing the legacy
	// string-prefix heuristics.
	MessageID string `json:"message_id,omitempty"`
	// RequestID identifies an ephemeral provider-side interaction such as a
	// user question. Unlike MessageID it never refers to a persisted row.
	RequestID string `json:"request_id,omitempty"`
	// QueueStatus mirrors the persisted row's store.Message.QueueStatus on
	// user_message frames, so a live client stages the row exactly as it would
	// after reloading it over REST. Only the IM bridge sets it today
	// (store.QueueStatusPoolQueued); web prompts echo through input_ack.
	QueueStatus string `json:"queue_status,omitempty"`
	// ThinkingMessageID is the persisted id of the thinking row that
	// goes alongside an assistant reply. Set on `result` events when
	// the run produced a thinking block. Empty otherwise.
	ThinkingMessageID string `json:"thinking_message_id,omitempty"`
	// CancelledPrompts carries the full Message rows for any pending
	// prompts that were dropped by a cancel. The client uses the ids to
	// remove the matching optimistic rows from history and the contents
	// to repopulate the editor for re-editing.
	CancelledPrompts []store.Message `json:"cancelled_prompts,omitempty"`
	// Conversation carries the persisted conversation row on user-hub
	// lifecycle events: conversation_added, conversation_updated. The
	// frontend merges this snapshot into its session list so peer tabs
	// stay in sync without an extra REST round-trip. For
	// conversation_removed only ConversationID is populated.
	Conversation *store.Conversation `json:"conversation,omitempty"`
	// Metadata carries persisted per-turn JSON. Assistant result events use
	// it for usage/model details; user_message events use it for uploaded
	// image previews. REST history exposes the same store.Message.Metadata.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
