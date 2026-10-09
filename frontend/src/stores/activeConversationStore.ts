// Global singleton store: metadata about the conversation currently open in
// the chat surface, plus the one-way pub/sub channels the app shell watches
// for backend-pushed conversation lifecycle news. See src/stores/index.ts for
// the conventions every store in this directory follows.
import { ref } from "vue";

import type { Conversation } from "@/composables/apiTypes";
import type { MessageAttachment } from "@/composables/useWebSocket";

// Shared by every useChat() caller.
export const currentConversationId = ref("");
// Logical subscription generation for the active conversation. It changes on
// every switch even though the browser keeps one physical WebSocket alive.
export const currentSubscriptionId = ref("");

// Lookup hook for the conversation row cached by useConversations. We
// can't import useConversations here without forming a circular module
// dep (it already imports useChat for switchToConversation), so the
// dependency goes the other way: useConversations injects a lookup at
// startup and switchToConversation prefers it over the network fetch.
// Returns null when the id isn't in cache (e.g. cross-agent navigation
// where the new agent's list hasn't been loaded yet) — callers fall
// back to fetchConversation in that case.
let conversationLookup: ((id: string) => Conversation | null) | null = null;
export function bindConversationLookup(fn: (id: string) => Conversation | null) {
  conversationLookup = fn;
}
export function lookupConversation(id: string): Conversation | null {
  return conversationLookup ? conversationLookup(id) : null;
}

// Navigation hook installed by the app shell, which owns agent switching.
// Chrome rendered by pages (the mobile task feed) jumps to a conversation in
// any agent through this instead of importing the shell's composables.
let conversationNavigator: ((agentId: string, conversationId: string) => void) | null = null;
export function bindConversationNavigator(
  fn: ((agentId: string, conversationId: string) => void) | null,
) {
  conversationNavigator = fn;
}
export function openConversation(agentId: string, conversationId: string) {
  conversationNavigator?.(agentId, conversationId);
}

// chatUserId is the user-id the WS init message reports as the acting user.
// The app shell mirrors `useUsers().currentUser.id` into it via a watcher,
// avoiding a circular module-level import (useUsers itself imports useChat).
export const chatUserId = ref<string | undefined>(undefined);

// titleUpdate carries the most recent backend-pushed conversation title.
// Components observing it (currently the app shell) react via watch(); the
// version field guarantees the watcher fires even when the same conv gets
// titled twice in succession.
export const titleUpdate = ref<{ id: string; title: string; version: number } | null>(null);
let titleUpdateVersion = 0;
export function publishTitleUpdate(id: string, title: string) {
  titleUpdate.value = { id, title, version: ++titleUpdateVersion };
}

// The conversation row shape carried on conversation_added / _updated WS
// frames (and republished on conversationListEvent below). The backend
// sends the full persisted row, so this is the REST Conversation type —
// a previous hand-rolled subset silently dropped pin_order / shared /
// account_name when peer tabs spliced updates into their sidebar.
export type ConversationListEventRow = Conversation;

// conversationListEvent carries the most recent user-hub broadcast about
// conversation lifecycle (created in another tab, deleted, renamed,
// notifications toggled). The app shell watches it and patches
// useConversations.conversations in place — that keeps every browser
// tab the user has open in sync without re-fetching the list. Direction
// is one-way: useChat publishes, appWiring consumes; we don't import
// useConversations here because that would close the import cycle (it
// already imports useChat for switchToConversation).
//
// Variants:
//   - kind="added"   → conversation is the new row, append at the head
//                       and update per-agent caches
//   - kind="removed" → only id is set; drop matching rows from caches
//   - kind="updated" → conversation carries the post-update row;
//                       splice in place (or noop if not cached locally)
export const conversationListEvent = ref<{
  kind: "added" | "removed" | "updated";
  id: string;
  conversation?: ConversationListEventRow;
  version: number;
} | null>(null);
let conversationListEventVersion = 0;
export function publishConversationListEvent(
  kind: "added" | "removed" | "updated",
  id: string,
  conversation?: ConversationListEventRow,
) {
  conversationListEvent.value = {
    kind,
    id,
    ...(conversation ? { conversation } : {}),
    version: ++conversationListEventVersion,
  };
}

// conversationListResync fires when the dispatcher notices the user-hub
// stream skipped a frame: some conversation_added / _updated / _removed /
// title_updated event never reached this tab, so the sidebar is showing a
// list the server has since moved on from. The hub keeps no replay buffer,
// which leaves refetching as the only repair.
//
// A bare counter rather than a payload — there is nothing to describe, only
// "your list is stale". Same one-way wiring as conversationListEvent above:
// the dispatcher publishes, appWiring consumes, so neither side needs the
// import that would close the cycle.
export const conversationListResync = ref(0);
export function publishConversationListResync() {
  conversationListResync.value++;
}

export const currentModel = ref("");
export const conversationProvider = ref("");
export const conversationAccount = ref("");
// Pre-formatted "Model: X | Tools: N available" string from the most recent
// system_init. Cached separately from currentModel so the post-refresh status
// pill keeps its tool count without us having to re-derive it.
export const modelInfoLine = ref("");
export const conversationWorkDir = ref("");
// recallText is set whenever the server's cancel_ack carries a list of
// pending prompts that were dropped from the dispatcher queue. ChatInput
// watches this and stuffs the joined text back into the editor so the
// user can re-edit and resend. Empty string = nothing to recall.
export const recallText = ref<string>("");
// Attachment metadata travels beside recallText when a queued prompt is
// returned to the composer. The prompt text already contains the original
// absolute refs; this metadata restores the visible chips and keeps the next
// send's persisted attachment list intact.
export const recallAttachments = ref<MessageAttachment[]>([]);

// Resets the observable state only. The injected conversationLookup hook
// above is app wiring installed once at module load, not per-conversation
// state — clearing it would leave the app permanently unwired rather than
// merely empty.
export function resetActiveConversationStore() {
  currentConversationId.value = "";
  currentSubscriptionId.value = "";
  chatUserId.value = undefined;
  titleUpdate.value = null;
  conversationListEvent.value = null;
  conversationListResync.value = 0;
  currentModel.value = "";
  conversationProvider.value = "";
  conversationAccount.value = "";
  modelInfoLine.value = "";
  conversationWorkDir.value = "";
  recallText.value = "";
  recallAttachments.value = [];
}
