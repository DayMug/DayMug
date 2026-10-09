import { effectScope, watch, type EffectScope } from "vue";
import { useUsers } from "./useUsers";
import { useChat } from "./useChat";
import { useConversations } from "./useConversations";
import { dropAttention } from "@/stores/conversationAttentionStore";

// Cross-composable wiring for the app shell. useChat publishes events (results,
// titles, user-hub list changes) that useConversations has to act on, and
// useUsers' current user has to reach the WS init payload — but useUsers
// already imports useChat and useConversations imports useChat, so wiring them
// inside either would be a circular import. The watchers live here instead and
// are installed explicitly by main.ts, so importing any composable stays free
// of side effects.

let scope: EffectScope | null = null;

// installAppWiring is idempotent: a second call while installed is a no-op, so
// the wiring can never be doubled up. The returned function tears it down,
// which only tests need — in the app it lives as long as the page.
export function installAppWiring(): () => void {
  if (!scope) {
    scope = effectScope(true);
    scope.run(wire);
  }
  return uninstallAppWiring;
}

export function uninstallAppWiring() {
  scope?.stop();
  scope = null;
}

function wire() {
  const { currentUser } = useUsers();
  const {
    chatUserId,
    resultVersion,
    titleUpdate,
    conversationListEvent,
    conversationListResync,
    currentConversationId,
  } = useChat();
  const {
    conversations,
    refreshConversations,
    applyTitleUpdate,
    applyRemoteAdded,
    applyRemoteUpdated,
    applyRemoteRemoved,
    selectConversation,
  } = useConversations();

  // Mirror the active user's id into useChat's chatUserId ref so the WS init
  // payload knows which user is acting.
  watch(currentUser, (u) => (chatUserId.value = u?.id), { immediate: true });

  // Refresh the conversation list after each assistant result so backend-set
  // fields (e.g. workdir lock, last-active timestamp) propagate to the sidebar.
  // Auto-titles arrive via the dedicated "title_updated" WS event below; the
  // claude-haiku call that generates them runs async after the assistant reply,
  // too late for this result-time refresh to catch it.
  watch(resultVersion, () => {
    const uid = currentUser.value?.id;
    if (!uid || !currentConversationId.value) return;
    void refreshConversations(uid);
  });

  // Push backend-generated titles into the conversation list in place so the
  // sidebar + document.title update live, no REST round-trip needed.
  watch(titleUpdate, (update) => {
    if (update) applyTitleUpdate(update.id, update.title);
  });

  // User-hub broadcasts: a peer tab created / deleted / renamed a
  // conversation. Patch the local conversation list in place so every
  // browser tab the user has open stays in sync without polling. The
  // actual handler logic (insert / merge / drop) lives in
  // useConversations; this watcher is just the wiring that keeps useChat
  // free of a circular dep on useConversations.
  watch(conversationListEvent, (evt) => {
    if (!evt) return;
    if (evt.kind === "added" && evt.conversation) {
      applyRemoteAdded(evt.conversation, { broadcast: true });
      return;
    }
    if (evt.kind === "updated" && evt.conversation) {
      applyRemoteUpdated(evt.conversation, { broadcast: true });
      return;
    }
    if (evt.kind === "removed") {
      dropAttention(evt.id);
      const wasActive = currentConversationId.value === evt.id;
      applyRemoteRemoved(evt.id, { broadcast: true });
      // If the active conversation just disappeared (deleted in another
      // tab), pivot to whatever's at the top of the remaining list so the
      // user isn't left looking at a dead chat. Safe no-op when the user
      // had a different conversation focused.
      if (wasActive && conversations.value.length > 0) {
        void selectConversation(conversations.value[0].id);
      }
    }
  });

  // A user-hub frame went missing on the way to this tab, so one of the
  // lifecycle events above never ran and the sidebar is stale. The hub has no
  // replay buffer, so refetch. refreshConversations rebinds the list in place —
  // unlike loadConversations it never switches or creates a conversation, so
  // this cannot yank the chat the user is reading.
  watch(conversationListResync, (version) => {
    if (!version) return; // 0 is the reset value, not a signal
    const uid = currentUser.value?.id;
    if (!uid) return;
    void refreshConversations(uid);
  });
}
