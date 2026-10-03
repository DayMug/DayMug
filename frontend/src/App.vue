<script setup lang="ts">
import { defineAsyncComponent, onMounted, onUnmounted, watch, computed, ref } from "vue";
import { useRouter, useRoute } from "vue-router";
import { useI18n } from "vue-i18n";
import { useDocumentTitle } from "@/composables/useDocumentTitle";
import { bindBrowserNotificationNavigation } from "@/composables/useBrowserNotifications";
import { useServerInfo } from "@/composables/useServerInfo";
import { useIsMobile } from "@/composables/useIsMobile";
import { bindConversationNavigator } from "@/stores/activeConversationStore";
import { useAttentionSync } from "@/composables/useAttentionSync";
import SidebarPanel from "@/components/SidebarPanel.vue";
import ConversationListContainer from "@/components/ConversationListContainer.vue";
import UserContextMenu from "@/components/UserContextMenu.vue";
import MobileAgentsScreen from "@/components/MobileAgentsScreen.vue";
import ChatMobileHeader from "@/components/chat/ChatMobileHeader.vue";
import MobileWorkspaceNav from "@/components/chat/MobileWorkspaceNav.vue";
import ConfirmDialog from "@/components/ConfirmDialog.vue";
import { useHelpDoc } from "@/composables/useHelpDoc";
import { useAppShell } from "@/composables/useAppShell";
import { useResizableSidebar } from "@/composables/useResizableSidebar";
import { useConversationSharing } from "@/composables/useConversationSharing";
import { useConversationListLayout } from "@/composables/useConversationListLayout";
import { fetchAppState } from "@/composables/useApi";
import { adminUpgradeCheck } from "@/composables/apiAdmin";
import { useTheme } from "@/composables/useTheme";
import { useAuth } from "@/composables/useAuth";
import { useUsers } from "@/composables/useUsers";
import { useChat } from "@/composables/useChat";
import { useConversations } from "@/composables/useConversations";
import { agentAttention, conversationAttention } from "@/stores/conversationAttentionStore";
import {
  cancelShareNoticeTimer,
  helpDialogOpen,
  marketplaceDialogOpen,
  shareNotice,
  upgradeAvailable,
} from "@/stores/appChromeStore";

// Both dialogs open on demand, so their code (and HelpDialog's markdown
// pipeline) is fetched the first time they're needed, not at startup.
const HelpDialog = defineAsyncComponent(() => import("@/components/HelpDialog.vue"));
const AppMarketplaceDialog = defineAsyncComponent(
  () => import("@/components/AppMarketplaceDialog.vue"),
);
// Mounted from its first open onwards (not v-if on `open`) so the close
// transition still plays.
const marketplaceDialogUsed = ref(false);
watch(
  marketplaceDialogOpen,
  (open) => {
    if (open) marketplaceDialogUsed.value = true;
  },
  { immediate: true },
);

const router = useRouter();
const route = useRoute();
const { t } = useI18n();
const isBareLayout = computed(() => route.meta?.layout === "bare");
// Settings is a full-page experience — UI design's mini-rail / nav /
// editor grid takes over the viewport entirely, so we hide the chat
// sidebar + conversation-list while the user is on /settings/*.
// Otherwise the chat rail would compete with the settings rail (both
// 56px) and the conversation list would peek behind the editor pane.
const isSettingsRoute = computed(() => route.path.startsWith("/settings"));
const { isDark } = useTheme();
const { authMe, loadAuthMe, applyAuthMe } = useAuth();
const {
  users,
  usersLoaded,
  currentUser,
  loadUsers,
  applyUsers,
  selectUser,
  handleArchiveUser,
  handleReorderUsers,
} = useUsers();
const {
  currentConversationId,
  disconnectWs,
  activeAgentIds,
  activeConversationIds,
  runningConversations,
  queuedConversations,
} = useChat();
const {
  conversations,
  conversationsByAgent,
  loadingAgentIds,
  loadConversationsForAgent,
  refreshConversations,
  refreshConversationsForAgent,
  applyConversations,
  isConversationPanelOpen,
  isConversationDrawerOpen,
  closeConversationDrawer,
  selectConversation,
  startNewConversation,
  deleteConversation,
  renameConversation,
  toggleConversationNotifications,
  toggleConversationPinned,
  reorderPinned,
  applyRemoteRemoved,
} = useConversations();
const { handleShareConversation, handleUnshareConversation } = useConversationSharing();

// notificationsConfigured gates the per-conversation bell icon: a toggle
// that can't fire any push notification (because the human owner hasn't
// set up a Bark URL or a PushDeer key) is visual noise, so we hide it
// entirely until at least one channel is configured. Lives on authMe
// because notification config is per-human-owner, not per-agent.
const notificationsConfigured = computed(() => {
  const me = authMe.value;
  if (!me) return false;
  return Boolean(
    (me.bark_url && me.bark_url.trim()) || (me.pushdeer_key && me.pushdeer_key.trim()),
  );
});

const { isMobile } = useIsMobile();
const { isNarrow, conversationListVisible, toggleConversationList } = useConversationListLayout();
const isMobileConversationsRoute = computed(() => isMobile.value && route.name === "conversations");
const mobileCurrentConversation = computed(
  () =>
    conversations.value.find((conversation) => conversation.id === currentConversationId.value) ??
    null,
);
const mobileConversationTitle = computed(
  () => mobileCurrentConversation.value?.title || t("conversations.new"),
);
const mobileAccountLabel = computed(
  () => authMe.value?.name || authMe.value?.username || currentUser.value?.name || t("chat.back"),
);

function openMobileConversation(view?: "artifacts") {
  const userId = currentUser.value?.id;
  const conversationId = currentConversationId.value;
  if (!userId || !conversationId) return;
  void router.push({
    name: "chat",
    params: { userId, conversationId },
    query: view ? { view } : undefined,
  });
}

function toggleMobileConversationNotifications() {
  const conversation = mobileCurrentConversation.value;
  if (!conversation) return;
  toggleConversationNotifications(conversation.id, !conversation.notifications_enabled);
}

// Rotating an iPad back to landscape (or widening a window) swaps the drawer
// out for the inline column. Without this the drawer's `true` would linger and
// re-open a modal the next time the viewport narrows.
watch(isNarrow, (narrow) => {
  if (!narrow) closeConversationDrawer();
});

const conversationPanelRef = ref<HTMLElement>();
const {
  isDragging: isConversationPanelDragging,
  panelStyle: conversationPanelStyle,
  onDragStart: onConversationPanelDragStart,
  onTouchStart: onConversationPanelTouchStart,
} = useResizableSidebar(conversationPanelRef, {
  // The reference workspace uses a 280px left column. The agent rail owns
  // 48px, so the resizable conversation column starts at the remaining 232px.
  defaultWidth: 232,
  minWidth: 190,
  maxWidth: 360,
});
useDocumentTitle(conversations, currentConversationId);

// The conversation actually on screen: the mobile list and settings keep a
// current conversation selected without showing it.
useAttentionSync(
  computed(() =>
    isBareLayout.value || isSettingsRoute.value || isMobileConversationsRoute.value
      ? ""
      : currentConversationId.value,
  ),
  computed(() => !!authMe.value),
);

function navigateToConversation(conversationId: string, agentId?: string) {
  const conversation =
    conversations.value.find((candidate) => candidate.id === conversationId) ??
    Object.values(conversationsByAgent.value)
      .flat()
      .find((candidate) => candidate.id === conversationId);
  const userId = agentId || conversation?.user_id || currentUser.value?.id;
  if (!userId) return;
  void (async () => {
    if (currentUser.value?.id !== userId) {
      await selectUser(userId, conversationId);
    } else if (currentConversationId.value !== conversationId) {
      await selectConversation(conversationId);
    }
    await router.push({ name: "chat", params: { userId, conversationId } });
  })().catch(() => undefined);
}
bindBrowserNotificationNavigation((conversationId) => navigateToConversation(conversationId));
bindConversationNavigator((agentId, conversationId) =>
  navigateToConversation(conversationId, agentId),
);

// Help popup state. The "?" button only renders when the admin has
// posted a non-empty markdown doc — the composable's reactive value is
// what drives the helpAvailable prop on SidebarPanel.
const { markdown: helpDocMarkdown, loadHelpDoc } = useHelpDoc();
const helpAvailable = computed(() => helpDocMarkdown.value.trim().length > 0);

// Shell orchestration handlers (conversation / user / workspace /
// mobile-IA actions) live in useAppShell — App.vue passes its state
// refs + router in and binds the returned handlers in the template.
const {
  handleDeleteConversation,
  contextMenuUser,
  contextMenuPos,
  handleUserContextMenu,
  handleEditUser,
  handleSelectUser,
  handleArchiveAgent,
  handleCleanupStaleConversations,
  mobileRefreshing,
  handleMobileSelectAgent,
  handleMobileSelectConversation,
  handleMobileRefresh,
  handleMobileNewConversation,
} = useAppShell({
  router,
  t,
  currentUser,
  currentConversationId,
  conversations,
  selectUser,
  selectConversation,
  startNewConversation,
  deleteConversation,
  handleArchiveUser,
  loadUsers,
  loadConversationsForAgent,
  refreshConversationsForAgent,
  applyRemoteRemoved,
});

let mediaQuery: MediaQueryList | null = null;
let mediaHandler: ((e: MediaQueryListEvent) => void) | null = null;
type PageShowEvent = Event & { persisted?: boolean };
type NavigationTimingEntry = { type?: string };
let pageShowHandler: ((e: PageShowEvent) => void) | null = null;

function isHistoryRestore(event: PageShowEvent): boolean {
  if (event.persisted) return true;
  const nav = window.performance.getEntriesByType("navigation")[0] as
    | NavigationTimingEntry
    | undefined;
  return nav?.type === "back_forward";
}

function refreshConversationsAfterHistoryRestore(event: PageShowEvent) {
  if (!isHistoryRestore(event)) return;
  const uid = currentUser.value?.id;
  if (!uid) return;
  void refreshConversations(uid);
}

// Admin-only upgrade nudge: 10 seconds after an admin lands on the
// chat surface we hit /api/admin/upgrade/check once. If the
// configured manifest reports a newer release, `upgradeAvailable`
// flips on and the account menu paints a red dot on Admin settings.
// It's a passive reminder, not a polling loop — fires once per page
// lifetime; a real upgrade still happens via the admin panel. The
// short delay surfaces the dot well within the first
// minute of session, which is when admins are most likely to glance
// at the chrome.
let upgradeCheckTimer: ReturnType<typeof setTimeout> | null = null;

watch(
  () => authMe.value?.is_admin === true && !isBareLayout.value,
  (eligible) => {
    if (upgradeCheckTimer !== null) {
      clearTimeout(upgradeCheckTimer);
      upgradeCheckTimer = null;
    }
    if (!eligible) {
      upgradeAvailable.value = false;
      return;
    }
    upgradeCheckTimer = setTimeout(() => {
      upgradeCheckTimer = null;
      void adminUpgradeCheck()
        .then((res) => {
          upgradeAvailable.value = res.has_update;
        })
        .catch(() => {
          // Manifest unconfigured / network blip — stay quiet, the
          // admin can still trigger a manual check from settings.
        });
    }, 10 * 1000);
  },
  { immediate: true },
);

// Sync route → state: if user navigates directly to /chat/:userId
watch(
  () => route.params.userId as string | undefined,
  async (userId) => {
    if (isBareLayout.value) return;
    if (userId && userId !== currentUser.value?.id) {
      await selectUser(userId);
    }
  },
);

// Sync route → state for the optional conversation segment. Picking up
// a fresh /chat/:userId/:conversationId on hard reload routes the user
// back into the same conversation; in-app navigation already calls
// switchToConversation directly, so the equality check makes this a
// no-op there.
watch(
  () => route.params.conversationId as string | undefined,
  async (conversationId) => {
    if (isBareLayout.value) return;
    if (conversationId && conversationId !== currentConversationId.value) {
      await selectConversation(conversationId);
    }
  },
);

// Sync state → route: when the active conversation changes (new
// conversation created, deleted-and-fallback, etc.) keep the URL in
// step so a refresh returns the user to the same place. Stays a no-op
// on /settings/* — otherwise the conversation that selectUser
// auto-loads on settings-page refresh would fire this watcher and
// bounce the user back to /chat.
watch(currentConversationId, (id) => {
  if (isBareLayout.value || isSettingsRoute.value || isMobileConversationsRoute.value || !id)
    return;
  const uid = currentUser.value?.id ?? (route.params.userId as string | undefined);
  if (!uid) return;
  if (route.params.conversationId === id) return;
  router.replace({ name: "chat", params: { userId: uid, conversationId: id } });
});

watch(isMobile, (mobile) => {
  if (!mobile && route.name === "conversations") {
    router.replace({
      name: "chat",
      params: {
        userId: currentUser.value?.id ?? "",
        conversationId: currentConversationId.value ?? "",
      },
    });
  }
});

onMounted(async () => {
  pageShowHandler = refreshConversationsAfterHistoryRestore;
  window.addEventListener("pageshow", pageShowHandler);

  mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
  mediaHandler = (e: MediaQueryListEvent) => {
    isDark.value = e.matches;
  };
  mediaQuery.addEventListener("change", mediaHandler);

  // Wait for initial navigation so route.meta is available
  await router.isReady();

  // Bare layout (e.g. preview page) — skip user loading and auto-redirect
  if (isBareLayout.value) return;

  // Cold-start aggregate: /api/app-state bundles /auth/me + /users +
  // /server-info + (when we can guess) the active agent's conversations
  // into a single round-trip, replacing the old four-step waterfall. We
  // hint the user_id we're about to focus on so the server can preload
  // that agent's conversation list inline — saves a second round-trip on
  // the common refresh path. Falls back to the individual endpoints when
  // the aggregate fails (e.g. an older backend that doesn't ship this
  // route) so a deploy mismatch doesn't brick the chat surface.
  const routeUserId = route.params.userId as string | undefined;
  const routeConversationId = route.params.conversationId as string | undefined;
  const savedUserId = localStorage.getItem("daymug-user-id") ?? undefined;
  const userIdHint = routeUserId ?? savedUserId;

  const { applyServerInfo } = useServerInfo();

  let bootstrapped = false;
  try {
    const state = await fetchAppState(userIdHint);
    applyAuthMe(state.auth_user);
    applyUsers(state.users);
    applyServerInfo(state.server_info);
    bootstrapped = true;

    // Conversation preload landed only when the server agreed the hinted
    // user is one the caller can access. If we got it, hand the list
    // straight to useConversations + useChat so neither has to round-trip
    // through /api/conversations or /api/conversations/:id.
    if (state.conversations && state.preloaded_user_id) {
      const target = users.value.find((u) => u.id === state.preloaded_user_id);
      if (target) {
        currentUser.value = target;
        localStorage.setItem("daymug-user-id", target.id);
        await applyConversations(
          target.id,
          state.conversations,
          routeConversationId,
          state.conversations_has_more,
        );
        if (
          !isSettingsRoute.value &&
          !isMobileConversationsRoute.value &&
          routeUserId !== target.id
        ) {
          router.replace({ name: "chat", params: { userId: target.id } });
        }
        // Fall through to the post-mount tasks below (help doc, etc.).
      }
    }
  } catch {
    // Older backend / transient error — fall back to the per-endpoint path
    // below. Logged-out clients land in this branch too because the
    // aggregate returns 401, and the existing user-load fallback already
    // redirects to /login when /users returns 401.
  }

  if (!bootstrapped) {
    await loadAuthMe();
    await loadUsers();
    // Server-info is normally piggybacked on /api/app-state; only fetch
    // it explicitly when the aggregate failed.
    void useServerInfo()
      .loadServerInfo()
      .catch(() => {});
  }

  // Defer the admin-configured help doc: it only gates the "?" button in
  // the sidebar and isn't part of the first-paint critical path. Sliding
  // it past first paint shaves one round-trip off the cold-start chain
  // without changing the UX (the button just appears a beat later when
  // an admin has actually published a help doc). The fetch itself has a
  // built-in once-per-session guard inside useHelpDoc, so calling it
  // again from a settings page is still a no-op.
  setTimeout(() => {
    void loadHelpDoc();
  }, 2000);

  // If the aggregate already focused the right agent + conversation, we
  // can short-circuit the rest of the routing logic — the heavy lifting
  // is done. The conversation-route watcher above handles any subsequent
  // URL changes from here.
  if (bootstrapped && currentUser.value) {
    return;
  }

  // Per-endpoint fallback path. Same logic as before — if the aggregate
  // didn't satisfy us (either it failed or it returned no conversation
  // preload), drop back to the explicit selectUser flow.
  if (routeUserId && users.value.some((u) => u.id === routeUserId)) {
    await selectUser(routeUserId, routeConversationId);
    return;
  }

  const savedUserExists = savedUserId && users.value.some((u) => u.id === savedUserId);
  // Don't bounce a /settings/* refresh into /chat — the user explicitly
  // navigated there and a hard reload should land back on the same page.
  // We still call selectUser so the sidebar / context info is primed for
  // the moment they leave the settings flow.
  if (savedUserExists) {
    await selectUser(savedUserId);
    if (!isSettingsRoute.value) {
      router.replace(
        isMobileConversationsRoute.value
          ? { name: "conversations" }
          : { name: "chat", params: { userId: savedUserId } },
      );
    }
  } else if (users.value.length > 0) {
    await selectUser(users.value[0].id);
    if (!isSettingsRoute.value) {
      router.replace(
        isMobileConversationsRoute.value
          ? { name: "conversations" }
          : { name: "chat", params: { userId: users.value[0].id } },
      );
    }
  }
});

onUnmounted(() => {
  bindBrowserNotificationNavigation(null);
  bindConversationNavigator(null);
  if (pageShowHandler) {
    window.removeEventListener("pageshow", pageShowHandler);
    pageShowHandler = null;
  }
  if (mediaQuery && mediaHandler) {
    mediaQuery.removeEventListener("change", mediaHandler);
  }
  if (upgradeCheckTimer !== null) {
    clearTimeout(upgradeCheckTimer);
    upgradeCheckTimer = null;
  }
  cancelShareNoticeTimer();
  disconnectWs();
});
</script>

<template>
  <div
    id="app-root"
    class="flex h-full w-full flex-col font-sans bg-background text-foreground"
    :class="[isDark ? 'dark' : '', { 'select-none': isConversationPanelDragging }]"
  >
    <!-- Desktop / tablet — original side-by-side layout -->
    <div v-if="!isMobile || isBareLayout" class="flex flex-1 min-h-0 flex-row">
      <template v-if="!isBareLayout && !isSettingsRoute">
        <SidebarPanel
          :users="users"
          :users-loaded="usersLoaded"
          :current-user="currentUser"
          :settings-active="route.path.startsWith('/settings')"
          :conversation-panel-open="conversationListVisible"
          :help-available="helpAvailable"
          :running-agent-ids="activeAgentIds"
          :running-conversations="runningConversations"
          :queued-conversations="queuedConversations"
          :agent-attention="agentAttention"
          @select-user="handleSelectUser"
          @context-menu="handleUserContextMenu"
          @reorder-agents="handleReorderUsers"
          @open-settings="router.push({ name: 'settings' })"
          @add-user="router.push({ name: 'settings-users-add' })"
          @toggle-conversations="toggleConversationList"
          @open-help="helpDialogOpen = true"
          @open-marketplace="marketplaceDialogOpen = true"
        />

        <div
          v-show="currentUser && !isNarrow && isConversationPanelOpen"
          ref="conversationPanelRef"
          class="relative h-full shrink-0"
          :style="conversationPanelStyle"
          data-testid="conversation-panel-shell"
        >
          <ConversationListContainer
            v-if="currentUser"
            :key="currentUser.id"
            class="!w-full !min-w-0"
          />
          <div
            class="group absolute -right-1 inset-y-0 z-20 w-2 cursor-col-resize"
            style="touch-action: none"
            data-testid="conversation-panel-resize-handle"
            @mousedown="onConversationPanelDragStart"
            @touchstart="onConversationPanelTouchStart"
          >
            <!-- The resting rule is the conversation list's own #eeeeee
                 border-r; this line only lights up on hover/drag. -->
            <span
              class="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 transition-colors group-hover:bg-primary/30"
              :class="isConversationPanelDragging ? 'bg-primary/30' : 'bg-transparent'"
              data-testid="conversation-panel-resize-line"
              aria-hidden="true"
            />
          </div>
        </div>

        <Teleport to="body">
          <div
            v-if="isNarrow && isConversationDrawerOpen && currentUser"
            class="fixed inset-0 z-50 flex"
            data-testid="conversation-drawer"
          >
            <div class="absolute inset-0 bg-black/40" @click="closeConversationDrawer" />
            <ConversationListContainer
              :key="currentUser.id"
              class="relative z-10 !w-[280px] !min-w-0 max-w-[80vw] shadow-xl"
              drawer
            />
          </div>
        </Teleport>

        <UserContextMenu
          v-if="contextMenuUser"
          :user="contextMenuUser"
          :x="contextMenuPos.x"
          :y="contextMenuPos.y"
          @close="contextMenuUser = null"
          @edit="handleEditUser"
          @archive="handleArchiveAgent"
          @cleanup="handleCleanupStaleConversations"
        />
      </template>

      <RouterView class="flex-1 min-h-0 min-w-0" />
    </div>

    <!-- Mobile — two stacked screens (Agents ↔ Conversation). Each
         agent on the Agents screen expands inline to show its
         conversations, so a tap on a conversation drills straight into
         the chat (no intermediate agent-detail stop). Settings and
         login bypass the IA via the bare-layout escape hatch. -->
    <div v-else class="flex flex-1 min-h-0 flex-col">
      <!-- Settings is its own bare-ish flow on mobile; show RouterView
           directly when the user is in /settings/* . The mobile IA only
           covers the chat-flow screens. -->
      <template v-if="route.path.startsWith('/settings')">
        <RouterView class="flex-1 min-h-0 min-w-0" />
      </template>

      <template v-else>
        <template v-if="isMobileConversationsRoute">
          <ChatMobileHeader
            :back-label="mobileAccountLabel"
            :title="mobileConversationTitle"
            :notifications-enabled="mobileCurrentConversation?.notifications_enabled ?? true"
            @toggle-notifications="toggleMobileConversationNotifications"
            @open-account="router.push({ name: 'settings' })"
          />
          <MobileAgentsScreen
            class="!h-auto min-h-0 flex-1"
            :users="users"
            :users-loaded="usersLoaded"
            :current-user="currentUser"
            :conversations-by-agent="conversationsByAgent"
            :loading-by-agent="loadingAgentIds"
            :current-conversation-id="currentConversationId"
            :help-available="helpAvailable"
            :refreshing="mobileRefreshing"
            :notifications-configured="notificationsConfigured"
            :running-agent-ids="activeAgentIds"
            :running-conversation-ids="activeConversationIds"
            :agent-attention="agentAttention"
            :conversation-attention="conversationAttention"
            @refresh="handleMobileRefresh"
            @select-agent="handleMobileSelectAgent"
            @select-conversation="handleMobileSelectConversation"
            @new-conversation="handleMobileNewConversation"
            @delete-conversation="handleDeleteConversation"
            @rename-conversation="renameConversation"
            @toggle-notifications="toggleConversationNotifications"
            @toggle-pinned="toggleConversationPinned"
            @share-conversation="handleShareConversation"
            @unshare-conversation="handleUnshareConversation"
            @reorder-pinned="(userId: string, ids: string[]) => reorderPinned(userId, ids)"
            @reorder-agents="handleReorderUsers"
            @open-agent-settings="handleEditUser"
            @open-settings="router.push({ name: 'settings' })"
            @add-agent="router.push({ name: 'settings-users-add' })"
            @open-help="helpDialogOpen = true"
          />
          <MobileWorkspaceNav
            active="sessions"
            @chat="openMobileConversation()"
            @artifacts="openMobileConversation('artifacts')"
          />
        </template>

        <RouterView v-else class="flex-1 min-h-0 min-w-0" />
      </template>

      <UserContextMenu
        v-if="contextMenuUser"
        :user="contextMenuUser"
        :x="contextMenuPos.x"
        :y="contextMenuPos.y"
        @close="contextMenuUser = null"
        @edit="handleEditUser"
        @archive="handleArchiveAgent"
        @cleanup="handleCleanupStaleConversations"
      />
    </div>

    <div
      v-if="shareNotice"
      class="fixed left-1/2 top-4 z-[80] -translate-x-1/2 rounded-md border border-border bg-popover px-3 py-2 text-sm text-popover-foreground shadow-md"
      role="status"
    >
      {{ shareNotice }}
    </div>

    <!-- Admin-configured help popup. Lives at the app root so it floats
         above both the desktop sidebar and the mobile agents screen. -->
    <HelpDialog
      v-if="helpDialogOpen && helpAvailable"
      :markdown="helpDocMarkdown"
      @close="helpDialogOpen = false"
    />

    <AppMarketplaceDialog v-if="marketplaceDialogUsed" v-model:open="marketplaceDialogOpen" />

    <!-- App-wide confirmation dialog host. Replaces native window.confirm(),
         which iOS Safari suppresses inside web apps. -->
    <ConfirmDialog />
  </div>
</template>

<style>
html,
body,
#app {
  margin: 0;
  padding: 0;
  width: 100%;
  height: 100%;
  height: 100dvh;
  overflow: hidden;
}

/* Animated ellipsis used by the "thinking" indicator. Three dots fade
   in and out on a staggered cycle so it reads as live activity rather
   than a static label. */
.thinking-dots {
  display: inline-flex;
  gap: 2px;
  margin-left: 2px;
}
.thinking-dots > span {
  display: inline-block;
  animation: thinking-dot-blink 1.4s infinite both;
}
.thinking-dots > span:nth-child(2) {
  animation-delay: 0.2s;
}
.thinking-dots > span:nth-child(3) {
  animation-delay: 0.4s;
}
@keyframes thinking-dot-blink {
  0%,
  80%,
  100% {
    opacity: 0.2;
  }
  40% {
    opacity: 1;
  }
}

/* App-specific semantic surfaces. Operational success/error colors remain
   colored for recognition; conversational surfaces stay achromatic. */
:root {
  --connected: oklch(0.68 0.18 152);
  --disconnected: oklch(0.62 0.22 22);
  --tool-border: oklch(0.76 0 0);
  --tool-bg: oklch(0.965 0 0);
  --tool-header: oklch(0.2 0 0);
  --thinking-border: oklch(0.76 0 0);
  --thinking-bg: oklch(0.975 0 0);
  --thinking-text: oklch(0.45 0 0);
  --user-bg: oklch(0.94 0 0);
  --assistant-bg: var(--muted);
}

.dark {
  --connected: oklch(0.78 0.12 145);
  --disconnected: oklch(0.72 0.12 22);
  --tool-border: oklch(0.36 0 0);
  --tool-bg: oklch(0.16 0 0);
  --tool-header: oklch(0.88 0 0);
  --thinking-border: oklch(0.36 0 0);
  --thinking-bg: oklch(0.16 0 0);
  --thinking-text: oklch(0.708 0 0);
  --user-bg: oklch(0.22 0 0);
  --assistant-bg: var(--muted);
}
</style>
