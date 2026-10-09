import { ref } from "vue";
import type { Ref } from "vue";
import type { Router } from "vue-router";

import type { User } from "@/composables/useApi";
import type { Conversation } from "@/composables/apiTypes";
import { deleteStaleConversations } from "@/composables/apiConversations";
import { useConfirm } from "@/composables/useConfirm";

// Everything the shell handlers close over, passed explicitly so the
// factory has no hidden coupling to the app-state composables' singletons —
// App.vue wires the real refs in; tests can wire fakes.
export interface AppShellDeps {
  router: Router;
  t: (key: string) => string;
  currentUser: Ref<User | null>;
  currentConversationId: Ref<string | null>;
  conversations: Ref<Conversation[]>;
  selectUser: (userId: string, preferredSessionId?: string) => Promise<unknown>;
  selectConversation: (id: string) => Promise<unknown>;
  startNewConversation: (userId: string) => Promise<unknown>;
  deleteConversation: (id: string) => Promise<unknown>;
  handleArchiveUser: (userId: string) => Promise<unknown>;
  loadUsers: () => Promise<unknown>;
  loadConversationsForAgent: (userId: string) => Promise<unknown>;
  refreshConversationsForAgent: (userId: string) => Promise<unknown>;
  applyRemoteRemoved: (id: string, options?: { broadcast?: boolean }) => boolean;
}

export type ConversationActionDeps = Pick<
  AppShellDeps,
  "router" | "t" | "currentUser" | "selectConversation" | "deleteConversation"
>;

// The conversation-row actions every conversation list shares (the docked
// panel, the narrow-viewport drawer, the mobile agents screen). Split out of
// useAppShell so the list container can bind them without instantiating the
// rest of the shell's state.
export function useConversationActions(deps: ConversationActionDeps) {
  const { router, t, currentUser, selectConversation, deleteConversation } = deps;
  const { confirm } = useConfirm();

  // Conversation delete gets a confirmation prompt by default so a stray
  // click never wipes a chat; primary-modifier-click bypasses it for power
  // users who delete rapidly. The conversation-list components still emit on
  // every click; the confirmation lives here.
  async function handleDeleteConversation(id: string, skipConfirm: boolean) {
    if (!skipConfirm) {
      const ok = await confirm({
        message: t("conversations.confirmDelete"),
        variant: "destructive",
      });
      if (!ok) return;
    }
    void deleteConversation(id);
  }

  // selectConversation wrapper that also reflects the chosen conversation
  // in the URL so refreshes / shares deep-link to the same place.
  async function handleSelectConversation(id: string) {
    await selectConversation(id);
    if (currentUser.value) {
      router.replace({
        name: "chat",
        params: { userId: currentUser.value.id, conversationId: id },
      });
    }
  }

  return { handleDeleteConversation, handleSelectConversation };
}

// useAppShell groups App.vue's orchestration handlers (conversation
// actions, user/agent actions, mobile IA) so the shell component stays
// mostly template + bootstrap. Pure extraction: behaviour is identical
// to the previous inline handlers.
export function useAppShell(deps: AppShellDeps) {
  const {
    router,
    t,
    currentUser,
    currentConversationId,
    conversations,
    selectUser,
    selectConversation,
    startNewConversation,
    handleArchiveUser,
    loadUsers,
    loadConversationsForAgent,
    refreshConversationsForAgent,
    applyRemoteRemoved,
  } = deps;

  const { confirm } = useConfirm();
  const { handleDeleteConversation, handleSelectConversation } = useConversationActions(deps);

  // User / agent actions ───────────────────────────────────────────

  const contextMenuUser = ref<User | null>(null);
  const contextMenuPos = ref({ x: 0, y: 0 });

  function handleUserContextMenu(event: MouseEvent, user: User) {
    contextMenuUser.value = user;
    contextMenuPos.value = { x: event.clientX, y: event.clientY };
  }

  function handleEditUser(user: User) {
    contextMenuUser.value = null;
    router.push({ name: "settings-users-edit", params: { id: user.id } });
  }

  async function handleSelectUser(userId: string) {
    await selectUser(userId);
    // After selectUser, the new agent's first conversation is already
    // loaded and currentConversationId is set — but the URL still has the
    // previous user's conversationId (or empty, after our own earlier
    // write). Push the new user + conversation in one shot so a refresh
    // deep-links back to the same place and the watcher on
    // currentConversationId doesn't see a transient empty value. Falling
    // back to "" if the new user has zero conversations (rare;
    // loadConversations auto-creates one in that case, but be defensive).
    router.push({
      name: "chat",
      params: { userId, conversationId: currentConversationId.value ?? "" },
    });
  }

  // Archive hides an agent from the sidebar (restorable from settings). A
  // confirm guards the click; the action itself is reversible so it isn't
  // flagged destructive.
  async function handleArchiveAgent(user: User) {
    contextMenuUser.value = null;
    const ok = await confirm({ message: t("agentMenu.confirmArchive") });
    if (!ok) return;
    void handleArchiveUser(user.id);
  }

  async function handleCleanupStaleConversations(user: User) {
    contextMenuUser.value = null;
    const ok = await confirm({
      message: t("agentMenu.confirmCleanupStale"),
      variant: "destructive",
    });
    if (!ok) return;
    const result = await deleteStaleConversations(user.id);
    const activeWasDeleted = result.deleted_ids.includes(currentConversationId.value ?? "");
    for (const id of result.deleted_ids) applyRemoteRemoved(id, { broadcast: true });
    if (currentUser.value?.id === user.id && activeWasDeleted) {
      if (conversations.value.length > 0) {
        await handleSelectConversation(conversations.value[0].id);
      } else {
        await startNewConversation(user.id);
      }
    }
  }

  // Mobile IA handlers ─────────────────────────────────────────────
  // Tapping an agent row inline-expands its conversation list (the
  // screen owns the expansion state). We lazy-load that agent's
  // conversations into the per-agent cache so the inline list renders
  // independently of whichever agent is currently active. After the
  // first load the composable refuses to refetch — see
  // `loadConversationsForAgent` for why. The active user is only
  // switched when the user actually drills into a conversation below.
  async function handleMobileSelectAgent(user: User) {
    await loadConversationsForAgent(user.id);
  }

  async function handleMobileSelectConversation(user: User, id: string) {
    // If the tapped conversation belongs to a different agent, switch
    // active user first so the chat surface mounts with the right
    // context. selectUser drives loadConversations which also primes the
    // cache, so the conversation-id is guaranteed to be present in the
    // list.
    if (currentUser.value?.id !== user.id) {
      await selectUser(user.id, id);
    } else {
      await selectConversation(id);
    }
    await router.push({
      name: "chat",
      params: { userId: user.id, conversationId: id },
    });
  }

  // Pull-to-refresh on the mobile Agents screen. Refetch the agent list
  // plus the expanded agent's conversations in parallel so both panes
  // reflect server state in one round-trip from the user's perspective.
  // `mobileRefreshing` is what keeps the spinner pinned on the indicator
  // for the full duration, independent of the touch lifecycle.
  const mobileRefreshing = ref(false);

  async function handleMobileRefresh(expandedAgentId: string | null) {
    if (mobileRefreshing.value) return;
    mobileRefreshing.value = true;
    try {
      const tasks: Promise<unknown>[] = [loadUsers()];
      if (expandedAgentId) {
        tasks.push(refreshConversationsForAgent(expandedAgentId));
      }
      await Promise.all(tasks);
    } finally {
      mobileRefreshing.value = false;
    }
  }

  function handleMobileNewConversation(user: User) {
    void (async () => {
      if (currentUser.value?.id !== user.id) {
        await selectUser(user.id);
      }
      await startNewConversation(user.id);
      await router.push({
        name: "chat",
        params: {
          userId: user.id,
          conversationId: currentConversationId.value ?? "",
        },
      });
    })();
  }

  return {
    // conversation actions
    handleDeleteConversation,
    handleSelectConversation,
    // user / agent actions
    contextMenuUser,
    contextMenuPos,
    handleUserContextMenu,
    handleEditUser,
    handleSelectUser,
    handleArchiveAgent,
    handleCleanupStaleConversations,
    // mobile IA
    mobileRefreshing,
    handleMobileSelectAgent,
    handleMobileSelectConversation,
    handleMobileRefresh,
    handleMobileNewConversation,
  };
}
