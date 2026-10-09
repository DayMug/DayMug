// Global singleton store: the conversation lists every surface renders from
// (sidebar, mobile agents screen, agent drill-downs) plus the sidebar panel's
// persisted open/closed preference. The fetching, sorting and mutation logic
// that maintains these lists lives in composables/useConversations.ts. See
// src/stores/index.ts for the conventions every store in this directory
// follows.
import { ref } from "vue";

import type { Conversation } from "@/composables/apiTypes";

export const conversations = ref<Conversation[]>([]);
// In-flight guard for createNewConversation. Without this, a fast
// double-click on the "+ New Conversation" button (or the same handler
// firing from multiple mount points before the first POST resolves)
// produces two separate conversation rows because the backend mints a
// fresh UUID per request.
export const isCreatingConversation = ref(false);
// Per-id mutation guards. The trash icon and rename input live on every
// row, on every list (sidebar / agents screen / mobile), so the easiest
// place to debounce is here at the store. Re-entries on the same id
// short-circuit; concurrent ops on different ids still pass through.
export const deletingIds = new Set<string>();
export const renamingIds = new Set<string>();
// Per-agent conversation cache. Lets surfaces like the mobile Agents
// screen render each agent's conversation list inline without disturbing
// the active conversation. Mutations in useConversations keep this in sync
// with any rows that match the touched conversation id.
export const conversationsByAgent = ref<Record<string, Conversation[]>>({});
// Per-agent fetch-in-flight markers. The mobile Agents screen reads
// these to show a "Loading conversations…" placeholder while a
// tap-to-expand fetch is pending — without it the inline list briefly
// renders an empty-or-stale cache before the network round-trip
// completes.
export const loadingAgentIds = ref<Record<string, boolean>>({});
// Tracks which agent's conversations are currently materialised in
// `conversations.value`. Updated whenever loadConversations /
// refreshConversations rebinds the list to a specific agent. Used by
// applyRemoteAdded to refuse splicing in a conversation whose owner
// doesn't match the active list — that happened when the user switched
// agents while a "+ new" POST was mid-flight, leaving the response to
// land in the wrong agent's sidebar until a refresh.
export const activeAgentId = ref<string>("");

// Paging state for the *active* sidebar list only. The per-agent buckets stay
// single-page: they back a collapsed drill-down where a "load more" affordance
// has nowhere to render, and giving each bucket its own paging state would mean
// tracking N scroll positions for lists nobody is looking at.
//
// There is no cursor here on purpose — the offset for the next page is simply
// `conversations.value.length`, so the state can never disagree with what is
// rendered.
export const conversationsHasMore = ref(false);
// In-flight guard for loadMoreConversations. Doubles as the button's disabled
// state; without it a fast double-click issues two requests for the same
// offset window.
export const isLoadingMoreConversations = ref(false);

const PANEL_STATE_KEY = "daymug-conversation-panel";

// IMPORTANT: declare the storage key BEFORE readPanelState's first call
// site below. The key is `const` (block-scoped) so it lives in the
// temporal dead zone until this point — placing the ref's initial-read
// before this declaration would throw a ReferenceError that the
// try/catch silently swallows, leaving the ref stuck at `false` on
// every fresh page load even when the user had explicitly opened the
// panel. That was the actual cause of "panel persistence doesn't work"
// reports — the WRITE on toggle worked, but the READ on init never did.
function readPanelState(): boolean {
  try {
    const v = localStorage.getItem(PANEL_STATE_KEY);
    return v === null ? true : v === "true";
  } catch {
    return true;
  }
}

export const isConversationPanelOpen = ref(readPanelState());

export function toggleConversationPanel() {
  isConversationPanelOpen.value = !isConversationPanelOpen.value;
  localStorage.setItem(PANEL_STATE_KEY, String(isConversationPanelOpen.value));
}

// Narrow viewports (< 1024px, i.e. phones and tablet portrait) render the
// conversation list as a modal drawer over the whole app instead of an inline
// column. That is a fundamentally different interaction, so it gets its own
// state rather than reusing isConversationPanelOpen:
//
// isConversationPanelOpen defaults to `true` and is persisted, which is right
// for a column that merely occupies layout space. Driving the drawer from the
// same flag meant every fresh load on an iPad in portrait opened with a
// backdrop covering the app — a modal the user never asked for, dismissable
// only by tapping the scrim (which then wrote `false` and silently changed
// their wide-screen column preference too).
//
// Deliberately not persisted: a modal should never survive a reload.
export const isConversationDrawerOpen = ref(false);

export function toggleConversationDrawer() {
  isConversationDrawerOpen.value = !isConversationDrawerOpen.value;
}

export function closeConversationDrawer() {
  isConversationDrawerOpen.value = false;
}

// Resets the conversation data only. isConversationPanelOpen is a persisted
// user preference rather than session state, so it keeps whatever the user
// last chose — re-reading localStorage restores exactly the value a fresh
// page load would compute.
export function resetConversationListStore() {
  conversations.value = [];
  conversationsByAgent.value = {};
  loadingAgentIds.value = {};
  activeAgentId.value = "";
  conversationsHasMore.value = false;
  isLoadingMoreConversations.value = false;
  isCreatingConversation.value = false;
  deletingIds.clear();
  renamingIds.clear();
  isConversationPanelOpen.value = readPanelState();
  isConversationDrawerOpen.value = false;
}
