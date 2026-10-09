// Global singleton store: the rendered transcript of the active conversation,
// the dedup set that keeps four delivery paths from double-rendering a row,
// and the paging cursors. The mappers, fetchers and scroll plumbing that
// operate on this state live in composables/chat/messageState.ts — this file
// holds nothing but state and the invariants that bind two pieces of it
// together. See src/stores/index.ts for the conventions every store follows.
import { ref } from "vue";

import type { FormattedUsage } from "@/lib/chatFormat";

import type { MessageAttachment, MessageSender } from "@/composables/useWebSocket";

export interface ChatMessage {
  role: "user" | "assistant" | "warning" | "error" | "activity";
  content: string;
  activityType?: "stream" | "tool" | "thinking" | "info" | "model";
  // Tool activity starts as a live timer and settles to the persisted duration
  // when tool_result arrives. Historical tool rows only need the latter.
  toolStartedAt?: number;
  toolDurationMs?: number;
  toolCompleted?: boolean;
  // Provider-side tool call id. Parallel calls can share the same display
  // name, so completion frames must use this instead of matching by label.
  toolCallId?: string;
  // Persisted DB message id. Set the moment we know which DB row this
  // entry corresponds to — from REST, from history_backfill, from
  // input_ack on the sender's optimistic row, from a tool_result /
  // result / user_message event that carries the persisted id. The
  // single primary key for cross-source dedup; if an id is in
  // `knownMessageIds`, we never render it twice regardless of which
  // path delivers it (REST / WS history_backfill / WS live event).
  id?: string;
  // True when this entry was produced by a sub-agent (Task / Agent tool)
  // invocation. Sub-agent activity is rendered as an indented track so
  // it's visually obvious it belongs *inside* the parent's tool call —
  // without this flag the duplicate-looking "Model:..." / "[Tool]
  // calling..." lines from the worker were indistinguishable from the
  // parent's own events. Sub-agent rows never carry a DB id (we don't
  // persist them server-side).
  subagent?: boolean;
  // Per-turn usage chip rendered under the assistant bubble. The short
  // form is the visible "I/O … CR … CW … Cost … Turns …" line and the
  // detail form is the multi-line hover breakdown. Set on assistant
  // rows that arrived (or were restored from REST) carrying a
  // metadata.usage payload; absent on every other row. Survives a
  // refresh because the backend persists the same payload on the DB
  // row's metadata column — no localStorage shadow involved.
  usage?: FormattedUsage;
  attachments?: MessageAttachment[];
  // Structured identity for a message mirrored from Slack / Feishu. Web
  // prompts leave this absent and continue to use ChatPage's userLabel.
  sender?: MessageSender;
  // ISO-8601 timestamp of when this message was sent. REST/backfill
  // rows carry the server's persisted `created_at` so timestamps stay
  // correct after a refresh; live event handlers (result, user echo,
  // prompt_started) stamp `new Date().toISOString()` at arrival time
  // since the server doesn't echo created_at on those frames. The chat
  // bubble uses this to render the small "sent at" hint next to the
  // username. Absent on activity/error rows that have nothing
  // user-facing to date.
  created_at?: string;
  // Set when the user broad-cancels the in-flight turn this prompt
  // triggered. Only applies to user-role rows that already landed in
  // history (queued prompts that get recalled are wiped entirely). The
  // chat bubble swaps to a muted variant + "Cancelled" badge so it's
  // visually obvious the turn didn't complete normally. Client-side
  // only — the backend doesn't persist a cancellation flag, so a page
  // refresh restores the row to its default style.
  cancelled?: boolean;
  // True when this assistant row is a turn the agent started on its own,
  // after background work it had armed earlier finished. The bubble carries
  // a badge so a conversation that moves while nobody is typing reads as
  // deliberate rather than uncanny. Unlike `cancelled` this survives a
  // refresh: the backend persists it on the row's metadata.
  wakeup?: boolean;
}

// Shared by every useChat() caller.
export const chatMessages = ref<ChatMessage[]>([]);

// Set of every persisted message id currently rendered in chatMessages,
// used to dedup across the four sources of truth (REST snapshot, WS
// history_backfill, WS live events, REST sync-on-reconnect). Replaces
// the old fromRest + string-prefix heuristics.
export const knownMessageIds = new Set<string>();

// Mutable cursors grouped in one object (instead of exported `let`s)
// because ESM bindings don't allow cross-module assignment.
export const cursors = {
  // id of the most recently persisted message we know about for the
  // active conversation. Drives the reconnect protocol: we send it as
  // `last_message_id` on init AND as `?after_id=` on the belt-and-
  // suspenders REST sync so the server returns anything that landed
  // while we were offline. Advanced on every persisted-event path:
  // REST, history_backfill, input_ack, result, tool_result, error,
  // user_message peer echo. Empty = "no cursor", server treats as
  // "send everything" — the recovery path for a brand-new conversation.
  lastSyncedMessageId: "",
  // Cursor for paging *backwards* through history. Tracks the id of the
  // oldest message currently rendered. The lazy-load handler in ChatPage
  // passes this to fetchMessages as `before_id` to pull the next page when
  // the user scrolls up to the top.
  oldestLoadedMessageId: "",
};

// True while there's likely older history we haven't yet fetched. Flipped
// to false once a paginated REST request returns fewer rows than the page
// size, which we take to mean "we hit the head of the conversation".
export const hasMoreHistory = ref(false);
// Set during the in-flight call so the scroll handler can debounce
// repeated triggers (otherwise a fast scroll can fire many parallel
// requests).
export const isLoadingMoreHistory = ref(false);
// How many messages a single page-load returns. Sized so the latest page
// covers the bulk of "active reading" sessions without forcing a second
// request for typical conversation lengths.
export const HISTORY_PAGE_SIZE = 50;

export const resultVersion = ref(0);

// rememberMessageId records a persisted DB message id and advances the
// reconnect cursor. Idempotent: a duplicate id is a no-op. Callers MUST
// invoke this whenever they push a row into chatMessages that has a
// known persisted id, so the dedup set stays consistent and the
// reconnect cursor keeps moving forward in event order.
export function rememberMessageId(id: string | undefined) {
  if (!id) return;
  knownMessageIds.add(id);
  cursors.lastSyncedMessageId = id;
}

// lastPersistedMessageId returns the id of the most recent DB-backed row
// in chatMessages — the natural anchor for the clear-context divider.
// Activity rows minted client-side (workdir hint, model pill, stream
// buffers) carry no id, so an `id` check naturally skips them; persisted
// thinking/tool rows render as activity but keep their id and stay
// eligible.
export function lastPersistedMessageId(): string {
  for (let i = chatMessages.value.length - 1; i >= 0; i--) {
    const m = chatMessages.value[i];
    if (m.id) return m.id;
  }
  return "";
}

export function resetChatMessageStore() {
  chatMessages.value = [];
  knownMessageIds.clear();
  cursors.lastSyncedMessageId = "";
  cursors.oldestLoadedMessageId = "";
  hasMoreHistory.value = false;
  isLoadingMoreHistory.value = false;
  resultVersion.value = 0;
}
