import { jsonRequestInit, request } from "./apiClient";
import type {
  ChatMessage,
  Conversation,
  ConversationAttentionRow,
  SharedConversationPayload,
  ThinkLevel,
} from "./apiTypes";

// CONVERSATION_PAGE_SIZE mirrors store.ConversationPageDefault, which is what
// /api/app-state preloads. Keeping the two equal means the sidebar's offsets
// line up with the preloaded first page without the server having to say how
// big that page was.
export const CONVERSATION_PAGE_SIZE = 50;

// fetchConversations returns a user's whole list in one request. Kept for
// callers that genuinely need every row; sidebar load paths use
// fetchConversationsPage so a large account doesn't ship thousands of rows.
export async function fetchConversations(userId: string): Promise<Conversation[]> {
  const params = new URLSearchParams({ user_id: userId });
  return request<Conversation[]>(`/api/conversations?${params}`, undefined, {
    label: "fetch conversations",
  });
}

// fetchConversationsPage asks for one offset window. The response is a bare
// array on every path, so "is there another page?" is inferred from a
// full-length result rather than an envelope flag.
//
// Offset paging is safe here only because the list is ordered by updated_at
// DESC and updated_at only ever moves forward: rows drift upward, so a window
// can repeat a row the caller already has but can never skip one. Callers must
// therefore dedupe by id when appending.
export async function fetchConversationsPage(
  userId: string,
  offset = 0,
  limit = CONVERSATION_PAGE_SIZE,
): Promise<Conversation[]> {
  const params = new URLSearchParams({
    user_id: userId,
    limit: String(limit),
    offset: String(offset),
  });
  return request<Conversation[]>(`/api/conversations?${params}`, undefined, {
    label: "fetch conversations",
  });
}

export interface ConversationAttentionPayload {
  items: ConversationAttentionRow[];
  titles: Record<string, string>;
}

export async function fetchConversationAttention(): Promise<ConversationAttentionPayload> {
  return request<ConversationAttentionPayload>("/api/conversation-attention", undefined, {
    label: "fetch conversation attention",
  });
}

// markConversationRead clears the conversation's attention flag server-side;
// the server tells the user's other tabs to drop their badge.
export async function markConversationRead(id: string): Promise<void> {
  await request<void>(
    `/api/conversations/${encodeURIComponent(id)}/read`,
    { method: "POST" },
    { label: "mark conversation read", expect: "none" },
  );
}

export async function createConversation(
  userId: string,
  title?: string,
  workDir?: string,
  provider?: string,
  model?: string,
  account?: string,
  thinkLevel?: ThinkLevel,
): Promise<Conversation> {
  const body: {
    title: string;
    user_id: string;
    work_dir: string;
    provider: string;
    model: string;
    account: string;
    think_level?: ThinkLevel;
  } = {
    title: title ?? "",
    user_id: userId,
    work_dir: workDir ?? "",
    provider: provider ?? "",
    model: model ?? "",
    account: account ?? "",
  };
  if (thinkLevel !== undefined) body.think_level = thinkLevel;
  return request<Conversation>(`/api/conversations`, jsonRequestInit("POST", body), {
    label: "create conversation",
  });
}

// updateConversationModel switches a conversation to a new (provider, model)
// pair. The backend rejects provider changes once the first message has been
// sent (409); model changes within the same provider are always allowed.
// Surface the server error verbatim so the UI can render it instead of a
// generic "update failed".
export async function updateConversationModel(
  id: string,
  provider: string,
  model: string,
  account?: string,
  thinkLevel?: ThinkLevel,
): Promise<Conversation> {
  // account: omit (undefined) to leave the pin untouched, "" to reset to the
  // user's default, or a name to re-pin. The backend also auto-clears a stale
  // pin when the provider type changes.
  const body: {
    provider: string;
    model: string;
    account?: string;
    think_level?: ThinkLevel;
  } = { provider, model };
  if (account !== undefined) {
    body.account = account;
  }
  if (thinkLevel !== undefined) {
    body.think_level = thinkLevel;
  }
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/model`,
    jsonRequestInit("PUT", body),
    { errorBody: "message" },
  );
}

export async function deleteConversation(id: string): Promise<void> {
  await request<void>(
    `/api/conversations/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    { label: "delete conversation", expect: "none" },
  );
}

export interface StaleConversationCleanupResult {
  deleted_ids: string[];
  deleted_count: number;
}

export async function deleteStaleConversations(
  userId: string,
): Promise<StaleConversationCleanupResult> {
  const params = new URLSearchParams({ user_id: userId });
  return request<StaleConversationCleanupResult>(
    `/api/stale-conversations?${params}`,
    { method: "DELETE" },
    { label: "delete stale conversations" },
  );
}

export async function fetchConversation(id: string): Promise<Conversation> {
  return request<Conversation>(`/api/conversations/${encodeURIComponent(id)}`, undefined, {
    label: "fetch conversation",
  });
}

export async function renameConversation(id: string, title: string): Promise<Conversation> {
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/title`,
    jsonRequestInit("PUT", { title }),
    { label: "rename conversation" },
  );
}

export async function updateConversationWorkDir(
  id: string,
  workDir: string,
): Promise<Conversation> {
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/work-dir`,
    jsonRequestInit("PUT", { work_dir: workDir }),
    { label: "update work dir" },
  );
}

export async function updateConversationNotifications(
  id: string,
  enabled: boolean,
): Promise<Conversation> {
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/notifications`,
    jsonRequestInit("PUT", { enabled }),
    { label: "update notifications" },
  );
}

export async function updateConversationPinned(id: string, pinned: boolean): Promise<Conversation> {
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/pinned`,
    jsonRequestInit("PUT", { pinned }),
    { label: "update pinned" },
  );
}

export interface ShareConversationResult {
  conversation: Conversation;
  token: string;
  url: string;
}

export async function shareConversation(id: string): Promise<ShareConversationResult> {
  return request<ShareConversationResult>(
    `/api/conversations/${encodeURIComponent(id)}/share`,
    { method: "POST" },
    { label: "share conversation" },
  );
}

export async function unshareConversation(id: string): Promise<Conversation> {
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/share`,
    { method: "DELETE" },
    { label: "unshare conversation" },
  );
}

export async function fetchSharedConversation(token: string): Promise<SharedConversationPayload> {
  return request<SharedConversationPayload>(
    `/api/shared/conversations/${encodeURIComponent(token)}`,
    { cache: "no-store" },
    { label: "fetch shared conversation" },
  );
}

// reorderPinnedConversations persists the manual order of a user's pinned
// conversations from a drag gesture. `ids` is the full ordered pinned set and
// is authoritative: ids omitted become unpinned, ids present become pinned at
// their index. Returns the user's refreshed, re-sorted conversation list.
export async function reorderPinnedConversations(
  userId: string,
  ids: string[],
): Promise<Conversation[]> {
  return request<Conversation[]>(
    `/api/conversation-pin-order`,
    jsonRequestInit("PUT", { user_id: userId, ids }),
    { label: "reorder pinned" },
  );
}

// clearConversationContext rotates the underlying claude session id and
// wipes the cached token bar — the chat history (DB rows + UI thread)
// stays, but the next user message starts a fresh claude CLI session
// with no prior context. Distinct from "new session", which forks a new
// conversation entirely.
export async function clearConversationContext(id: string): Promise<Conversation> {
  return request<Conversation>(
    `/api/conversations/${encodeURIComponent(id)}/clear-context`,
    { method: "POST" },
    { label: "clear context" },
  );
}

// Paging shapes this endpoint supports:
//   - no options: the whole conversation in ascending insertion order.
//   - reverse paging (the chat UI's lazy-load): pass `{ beforeId, limit }`.
//     Empty `beforeId` returns the latest `limit` messages — that's the
//     initial page load. Subsequent pages pass the oldest id we've
//     loaded so far.
//   - forward incremental (the reconnect / focus-resume sync): pass
//     `{ afterId, limit }`. Empty `afterId` returns every message — same
//     recovery semantics as the WS history_backfill path. Use this on
//     every reconnect so the client confidently catches up to the DB
//     state without depending on the broadcaster's in-memory replay.
//
// All shapes return ascending (oldest-first) so the caller can splice
// the response directly. The endpoint sets `Cache-Control: no-store`
// server-side and we additionally pass `cache: 'no-store'` from the
// client to defeat any heuristic browser caching.
export async function fetchMessages(
  conversationId: string,
  options: { limit?: number; beforeId?: string; afterId?: string } = {},
): Promise<ChatMessage[]> {
  const params = new URLSearchParams();
  if (options.limit) params.set("limit", String(options.limit));
  if (options.beforeId !== undefined && options.afterId !== undefined) {
    throw new Error("fetchMessages: pass beforeId or afterId, not both");
  }
  // Always set the cursor query (even empty) so the backend takes the
  // matching paging branch — empty value is the explicit opt-in
  // signal for "first page" / "everything".
  if (options.beforeId !== undefined) params.set("before_id", options.beforeId);
  if (options.afterId !== undefined) params.set("after_id", options.afterId);
  const qs = params.toString();
  const url = `/api/conversations/${encodeURIComponent(conversationId)}/messages${qs ? `?${qs}` : ""}`;
  return request<ChatMessage[]>(url, { cache: "no-store" }, { label: "fetch messages" });
}
