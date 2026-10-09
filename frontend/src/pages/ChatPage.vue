<script setup lang="ts">
// Conversation screen. Two layouts (mobile tabs / desktop split panes) share
// the same transcript and composer components — the layouts differ in chrome
// and spacing, not in what they show. Conversation controls live in the shared
// composer footer so they stay beside the next prompt on every viewport.
import { ref, computed, nextTick, onMounted, watch, type ComponentPublicInstance } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import { CONTEXT_CLEARED_DIVIDER, useChat } from "@/composables/useChat";
import { useAuth } from "@/composables/useAuth";
import { useUsers } from "@/composables/useUsers";
import { useConversations } from "@/composables/useConversations";
import { useIsMobile } from "@/composables/useIsMobile";
import { useSwipeBack } from "@/composables/useSwipeBack";
import { useResizablePanels } from "@/composables/useResizablePanels";
import { useChatScroll } from "@/composables/useChatScroll";
import { useChatCapabilities } from "@/composables/useChatCapabilities";
import { modelAcceptsImages, useModelRegistry } from "@/composables/useModelRegistry";
import { useChatRenderItems } from "@/composables/useChatRenderItems";
import { hasModelReplied } from "@/lib/conversationStarted";
import { requestBrowserNotificationPermission } from "@/composables/useBrowserNotifications";
import ChatFooterControls from "@/components/chat/ChatFooterControls.vue";
import ChatTranscript from "@/components/chat/ChatTranscript.vue";
import ChatComposer from "@/components/chat/ChatComposer.vue";
import ChatMobileHeader from "@/components/chat/ChatMobileHeader.vue";
import MobileWorkspaceNav from "@/components/chat/MobileWorkspaceNav.vue";
import WorkspacePanel from "@/components/WorkspacePanel.vue";
import { Button } from "@/components/ui/button";
import type { Conversation } from "@/composables/apiTypes";
import type { MessageAttachment } from "@/composables/useWebSocket";
import { PanelRightOpen } from "lucide-vue-next";

const { authMe } = useAuth();
const { currentUser } = useUsers();
const { conversations, toggleConversationNotifications } = useConversations();
const {
  currentConversationId,
  chatMessages,
  isThinking,
  backgroundConversationIds,
  isTurnStatusKnown,
  queuePosition,
  pendingPrompts,
  pendingUserQuestion,
  isAnsweringUserQuestion,
  isConnected,
  contextUsage,
  lastUsage,
  rateLimits,
  resultVersion,
  conversationProvider,
  conversationWorkDir,
  recallText,
  recallAttachments,
  sendMessage,
  cancelMessage,
  cancelPendingPrompt,
  answerUserQuestion,
  clearContext,
  setChatScrollFn,
  loadMoreHistory,
  hasMoreHistory,
  isLoadingMoreHistory,
} = useChat();

function consumeRecall() {
  recallText.value = "";
  recallAttachments.value = [];
}

// Rate-limit badge and context-bar gating live in useChatCapabilities — it
// loads the model registry and derives per-provider capability flags.
const { supportsRateLimitEvents, hasRateLimitInfo, reportsContextUsage } = useChatCapabilities({
  conversationProvider,
  rateLimits,
});
// contextUsage can outlive the backend that can actually measure it: the value
// is replayed from conversations.last_context_usage on join, so a Codex
// conversation still carries whatever row an earlier transport wrote. Render
// only when the live backend says it keeps that row current.
const showContextUsage = computed(() => Boolean(contextUsage.value) && reportsContextUsage.value);
const { t } = useI18n();
const router = useRouter();
const route = useRoute();

// Conversation label resolution. Assistant side is the active agent's display
// name (falls back to the historical "Claude" framing when the agent record
// hasn't loaded yet). User side is always the localized "You" — the human is
// reading their own transcript, so showing their own name adds noise and gets
// actively confusing when the human and the agent share a display name (a
// real-world setup we hit when both rows were named "技术").
const assistantLabel = computed(() => {
  const name = currentUser.value?.name?.trim();
  return name && name.length > 0 ? name : "Claude";
});
const userLabel = computed(() => t("chat.you"));

// Debounces double clicks during the network round-trip so we don't fire two
// clear-context POSTs.
const clearingContext = ref(false);

async function handleClearContext() {
  if (clearingContext.value) return;
  clearingContext.value = true;
  try {
    await clearContext();
  } finally {
    clearingContext.value = false;
  }
}

// True when a run is actually executing in this conversation (not merely queued
// in the per-account pool). Drives the staging area's "1 message ahead" label so
// a queued prompt visibly accounts for the in-flight turn. While the pool wait
// is still in progress we leave this false, because poolPosition already carries
// the more specific "Account busy / Next up" hint for the head entry.
const hasActiveTask = computed(() => isThinking.value && queuePosition.value === null);

// The turn is over but the agent is parked on background work it started, so
// the conversation still shows as running everywhere else. Say why here.
const isWaitingOnBackground = computed(
  () =>
    !isThinking.value &&
    !!currentConversationId.value &&
    backgroundConversationIds.value.includes(currentConversationId.value),
);

// Gate the inline "clear context" trigger. The button should be unobtrusive:
// only surface it when the user could plausibly want it. That means hiding
// while a turn is in flight, and right after a fresh clear (the divider at the
// tail already says the context is empty — no point re-offering).
const canShowClearContext = computed(() => {
  if (!currentConversationId.value) return false;
  if (chatMessages.value.length === 0) return false;
  if (isThinking.value) return false;
  if (queuePosition.value !== null) return false;
  // Anything in the staging area will run as soon as the worker frees up, so
  // offering "clear context" right above it would be confusing.
  if (pendingPrompts.value.length > 0) return false;
  const last = chatMessages.value[chatMessages.value.length - 1];
  if (
    last?.role === "activity" &&
    last.activityType === "info" &&
    last.content === CONTEXT_CLEARED_DIVIDER
  ) {
    return false;
  }
  return true;
});

// Back out of a conversation on a phone. The list opens a conversation with a
// push, so the way back out is a pop — pushing the list again instead left two
// extra entries behind on every visit, and the phone's own back affordance
// (Android's button, iOS's edge swipe) then had to walk that whole accordion of
// list/chat/list/chat before it could leave the app.
//
// vue-router's web history records the previous entry's path in
// `history.state.back`, which is the only way to tell a pop that lands on the
// list from one that lands on some unrelated page — a conversation opened from
// a deep link or a notification has no list behind it, and that case still has
// to push.
function backMobile() {
  const previous = (window.history.state as { back?: unknown } | null)?.back;
  const previousPath = typeof previous === "string" ? previous.split(/[?#]/)[0] : "";
  if (previousPath === router.resolve({ name: "conversations" }).path) {
    router.back();
    return;
  }
  void router.push({ name: "conversations" });
}

const swipeBack = useSwipeBack(backMobile);

// Conversation title for both top bars — falls back to a friendly label when
// the conversation hasn't been renamed yet.
const conversationTitle = computed(() => {
  const conv = conversations.value.find((c) => c.id === currentConversationId.value);
  return conv?.title || t("conversations.new");
});

const mobileConversationNotifEnabled = computed(() => {
  const conv = conversations.value.find((c) => c.id === currentConversationId.value);
  return !!conv?.notifications_enabled;
});

// The active conversation row, exposed so the footer ModelSelector can read its
// provider+model fields and patch them in place on success.
const currentConversation = computed<Conversation | null>(() => {
  const id = currentConversationId.value;
  if (!id) return null;
  return conversations.value.find((c) => c.id === id) ?? null;
});

// "Has the model actually answered yet?" — the backend pre-mints a session id
// at create time, so the message history is the only signal both halves agree
// on. A running turn and unloaded older history count as started; a first turn
// that failed before the model replied does not, so the user can switch to a
// working provider/account and retry.
const conversationStarted = computed(
  () => isThinking.value || hasMoreHistory.value || hasModelReplied(chatMessages.value),
);

// A text-only model (declared by the admin) can't see an attached image; the
// composer warns before the user sends one into a turn that will ignore it.
const { registry: modelRegistry } = useModelRegistry();
const imageInputUnsupported = computed(() => {
  const conv = currentConversation.value;
  if (!conv) return false;
  const account =
    conv.account_name || currentUser.value?.provider_accounts?.[conv.provider]?.[0] || "";
  return !modelAcceptsImages(modelRegistry.value, conv.provider, account, conv.model);
});

function applyModelChange(updated: Conversation) {
  const idx = conversations.value.findIndex((c) => c.id === updated.id);
  if (idx !== -1) conversations.value[idx] = { ...conversations.value[idx], ...updated };
}

const modelChangeError = ref("");
function onModelChangeError(message: string) {
  modelChangeError.value = message;
  // Auto-clear so a stale message doesn't hang around. 4s is long enough to
  // read but short enough not to clutter the header after the user moved on.
  window.setTimeout(() => {
    if (modelChangeError.value === message) modelChangeError.value = "";
  }, 4000);
}

function toggleMobileNotifications() {
  if (currentConversationId.value) {
    toggleConversationNotifications(
      currentConversationId.value,
      !mobileConversationNotifEnabled.value,
    );
  }
}

const chatPanelRef = ref<HTMLDivElement>();
const composerRef = ref<InstanceType<typeof ChatComposer>>();
const workspacePanelRef = ref<InstanceType<typeof WorkspacePanel>>();
const containerRef = ref<HTMLDivElement>();

// The mobile and desktop layouts each render their own <ChatComposer>, so the
// composer is reached through a function ref rather than a static one — the
// mount-time focus has to hang off that single hook.
function setComposerRef(el: Element | ComponentPublicInstance | null) {
  composerRef.value = (el as InstanceType<typeof ChatComposer> | null) ?? undefined;
}

const { isMobile } = useIsMobile();

// Mobile tab: "chat" or "files"
const mobileTab = ref<"chat" | "files">(route.query.view === "artifacts" ? "files" : "chat");

watch(
  () => route.query.view,
  (view) => {
    mobileTab.value = view === "artifacts" ? "files" : "chat";
  },
);

function setMobileTab(tab: "chat" | "files") {
  mobileTab.value = tab;
  const query = { ...route.query };
  if (tab === "files") query.view = "artifacts";
  else delete query.view;
  void router.replace({ query });
}

// Resizable panels. UI design pins workspace to 400px on the right; the width
// persists through localStorage so a previously dragged size survives reloads.
// `isCollapsed` zeroes the workspace pane; the entry point back is the expand
// button in the chat header. The drag handle only renders while the workspace
// is open — when collapsed there's nothing to drag against.
const {
  isCollapsed: isWorkspaceCollapsed,
  isDragging,
  chatPanelStyle,
  workspacePanelStyle,
  onDragStart,
  onTouchStart,
  collapse: collapseWorkspace,
  expand: expandWorkspace,
} = useResizablePanels(containerRef, { minWidth: 200 });
const workspaceFullscreen = ref(false);

const renderItems = useChatRenderItems(chatMessages);

const { onChatScroll } = useChatScroll({
  chatPanelRef,
  hasMoreHistory,
  isLoadingMoreHistory,
  loadMoreHistory,
  setChatScrollFn,
});

onMounted(async () => {
  // Both layouts mount their own composer and reach this hook through the same
  // function ref, so wait one flush for the ref to settle on the branch that
  // actually rendered before reaching for focus().
  await nextTick();
  composerRef.value?.focus();
});

watch(resultVersion, () => {
  workspacePanelRef.value?.refresh();
});

// Refresh the workspace listing right after the user sends. The resultVersion
// watch above covers the after-the-run case; this covers the start of a turn so
// files the user just dropped via the upload affordance show up immediately.
function handleSendMessage(text: string, attachments?: MessageAttachment[], insert?: boolean) {
  void requestBrowserNotificationPermission();
  if (insert) {
    sendMessage(text, attachments, true);
  } else if (attachments?.length) {
    sendMessage(text, attachments);
  } else {
    sendMessage(text);
  }
  workspacePanelRef.value?.refresh();
}

// Relative path from the conversation work_dir for workspace navigation. Always
// lands on the conversation's work_dir on mount and on every conversation
// switch. The user can navigate freely during the session; we just don't try to
// remember where they ended up.
//
// Returns "" while the conversation fetch is still in flight on a hard refresh —
// WorkspacePanel treats the empty string as "wait for real data" instead of
// speculatively loading the user's work_dir root and re-fetching 200ms later.
// The same "wait" signal covers the agent-switch transition: `currentUser` flips
// to agentB synchronously, but `conversationWorkDir` still points at agentA's
// path until `switchToConversation` runs. Speculatively returning "." in that
// window would make the panel load agentB's root under agentA's conversation.
const workspaceInitialPath = computed(() => {
  const userDir = currentUser.value?.work_dir;
  const convDir = conversationWorkDir.value;
  if (!userDir) return ".";
  if (!convDir) return "";
  const userBase = trimTrailingSlashes(userDir);
  const convBase = trimTrailingSlashes(convDir);
  if (convBase === userBase) return ".";
  if (convBase.startsWith(`${userBase}/`)) {
    return convBase.slice(userBase.length).replace(/^\/+/, "");
  }
  return "";
});

function trimTrailingSlashes(path: string): string {
  return path.replace(/\/+$/, "") || "/";
}
</script>

<template>
  <!-- Mobile layout — Conversation screen (UI design `MConversationScreen`).
       Top bar with back-to-Agent + title + notifications + more, then
       Chat / Files inner tabs. -->
  <div
    v-if="isMobile"
    class="relative flex flex-col flex-1 min-h-0 bg-card font-sans"
    :style="swipeBack.style.value"
    @touchstart.passive="swipeBack.onTouchStart"
    @touchmove="swipeBack.onTouchMove"
    @touchend="swipeBack.onTouchEnd"
    @touchcancel="swipeBack.onTouchCancel"
  >
    <ChatMobileHeader
      :back-label="authMe?.name || authMe?.username || currentUser?.name || t('chat.back')"
      :title="conversationTitle"
      :notifications-enabled="mobileConversationNotifEnabled"
      @back="backMobile()"
      @toggle-notifications="toggleMobileNotifications"
      @open-account="router.push({ name: 'settings' })"
    />

    <!-- The reference keeps the project identity in a quiet second bar and
         moves mode switching to persistent bottom navigation. This stops the
         transcript from looking like a tabbed settings page. -->
    <div
      class="flex h-[47px] shrink-0 items-center border-b border-[#e6e6e6] bg-card px-5 dark:border-border"
      data-testid="mobile-project-header"
    >
      <strong class="min-w-0 flex-1 truncate text-[16px] font-semibold tracking-[-0.02em]">
        {{ currentUser?.name || conversationTitle }}
      </strong>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        class="size-9 shrink-0 text-muted-foreground"
        :aria-label="t('chat.mobileArtifactsTab')"
        data-testid="mobile-open-artifacts"
        @click="setMobileTab('files')"
      >
        <PanelRightOpen class="size-[18px]" />
      </Button>
    </div>

    <!-- Chat tab -->
    <template v-if="mobileTab === 'chat'">
      <div
        ref="chatPanelRef"
        class="flex-1 overflow-y-auto overflow-x-hidden touch-pan-y overscroll-y-contain bg-background px-3.5 pb-3 pt-2.5"
        @scroll="onChatScroll"
      >
        <ChatTranscript
          :items="renderItems"
          :conversation-id="currentConversationId"
          :is-empty="chatMessages.length === 0"
          :is-loading-more-history="isLoadingMoreHistory"
          :is-thinking="isThinking"
          :is-waiting-on-background="isWaitingOnBackground"
          :user-label="userLabel"
          :assistant-label="assistantLabel"
          :can-show-clear-context="canShowClearContext"
          :clearing-context="clearingContext"
          :is-connected="isConnected"
          @clear-context="handleClearContext"
          @stop-background="cancelMessage"
        />
      </div>
      <ChatComposer
        :ref="setComposerRef"
        :prompts="pendingPrompts"
        :has-active-task="hasActiveTask"
        :is-thinking="isThinking"
        :turn-status-known="isTurnStatusKnown"
        :is-connected="isConnected"
        :conversation-id="currentConversationId"
        :recall-text="recallText"
        :recall-attachments="recallAttachments"
        :provider="conversationProvider"
        :image-input-unsupported="imageInputUnsupported"
        :question="pendingUserQuestion"
        :answering-question="isAnsweringUserQuestion"
        @send="handleSendMessage"
        @cancel="cancelMessage"
        @cancel-prompt="cancelPendingPrompt"
        @answer="answerUserQuestion"
        @recall-consumed="consumeRecall"
      >
        <template #controls>
          <ChatFooterControls
            :conversation="currentConversation"
            :conversation-started="conversationStarted"
            :provider-accounts="currentUser?.provider_accounts"
            :error="modelChangeError"
            :show-context-usage="showContextUsage"
            :context-usage="contextUsage"
            :provider="conversationProvider"
            :usage-summary="lastUsage?.short"
            :show-rate-limit="supportsRateLimitEvents && hasRateLimitInfo"
            :rate-limits="rateLimits"
            @updated="applyModelChange"
            @error="onModelChangeError"
          />
        </template>
      </ChatComposer>
    </template>

    <!-- Files tab -->
    <template v-if="mobileTab === 'files'">
      <div class="flex-1 flex flex-col min-h-0 bg-background">
        <div v-if="!currentUser" class="text-muted-foreground text-center py-10 px-4 text-sm">
          {{ t("chat.selectUserWorkspace") }}
        </div>
        <WorkspacePanel
          v-else
          ref="workspacePanelRef"
          :user-id="currentUser.id"
          :conversation-id="currentConversationId"
          :user-work-dir="currentUser.work_dir"
          :conversation-work-dir="conversationWorkDir"
          :initial-path="workspaceInitialPath"
          file-open-target="panel"
          @fullscreen-change="workspaceFullscreen = $event"
        />
      </div>
    </template>

    <MobileWorkspaceNav
      :active="mobileTab === 'chat' ? 'chat' : 'artifacts'"
      @sessions="backMobile"
      @chat="setMobileTab('chat')"
      @artifacts="setMobileTab('files')"
    />
  </div>

  <!-- Desktop layout: side-by-side panels. UI design pins the workspace at
       400px; we keep the resize handle so power users can claim more chat
       width when reading long output. -->
  <div
    v-else
    ref="containerRef"
    class="relative flex flex-1 min-h-0"
    :class="{ 'select-none': isDragging }"
  >
    <!-- Chat panel — title centered, with the workspace affordance on the
         right. The reading column is capped so long lines stay readable on
         ultrawide displays. -->
    <div
      class="chat-panel relative flex h-full w-full flex-col min-h-0 min-w-0"
      :style="chatPanelStyle"
    >
      <div
        class="panel-header relative flex min-h-12 shrink-0 items-center gap-3 border-b border-[#eeeeee] bg-card pl-[21px] pr-[18px] dark:border-border"
      >
        <div class="flex min-w-0 flex-1 items-center justify-start text-left">
          <span
            class="truncate text-[16px] font-semibold leading-6 text-[#202124] dark:text-foreground"
            :title="conversationTitle"
            data-testid="chat-title"
            >{{ conversationTitle }}</span
          >
        </div>
        <!-- Expand affordance: surfaces only while the workspace is collapsed.
             The resize-handle strip is still click-to-expand, but it's too thin
             to be discoverable on first encounter — this button gives the
             action a clear visual home in the top bar. -->
        <Button
          v-if="isWorkspaceCollapsed"
          type="button"
          variant="ghost"
          size="icon"
          class="size-7 shrink-0 text-muted-foreground"
          :title="t('chat.showWorkspacePanel')"
          data-testid="workspace-expand"
          @click="expandWorkspace"
        >
          <PanelRightOpen :size="16" />
        </Button>
      </div>
      <div
        ref="chatPanelRef"
        class="wy-scroll flex-1 overflow-y-auto overflow-x-hidden touch-pan-y overscroll-y-contain bg-background px-[clamp(26px,5vw,72px)] pb-[26px] pt-[70px]"
        @scroll="onChatScroll"
      >
        <ChatTranscript
          :items="renderItems"
          :conversation-id="currentConversationId"
          :is-empty="chatMessages.length === 0"
          :is-loading-more-history="isLoadingMoreHistory"
          :is-thinking="isThinking"
          :is-waiting-on-background="isWaitingOnBackground"
          :user-label="userLabel"
          :assistant-label="assistantLabel"
          :can-show-clear-context="canShowClearContext"
          :clearing-context="clearingContext"
          :is-connected="isConnected"
          @clear-context="handleClearContext"
          @stop-background="cancelMessage"
        />
      </div>
      <ChatComposer
        :ref="setComposerRef"
        :prompts="pendingPrompts"
        :has-active-task="hasActiveTask"
        :is-thinking="isThinking"
        :turn-status-known="isTurnStatusKnown"
        :is-connected="isConnected"
        :conversation-id="currentConversationId"
        :recall-text="recallText"
        :recall-attachments="recallAttachments"
        :provider="conversationProvider"
        :image-input-unsupported="imageInputUnsupported"
        :question="pendingUserQuestion"
        :answering-question="isAnsweringUserQuestion"
        @send="handleSendMessage"
        @cancel="cancelMessage"
        @cancel-prompt="cancelPendingPrompt"
        @answer="answerUserQuestion"
        @recall-consumed="consumeRecall"
      >
        <template #controls>
          <ChatFooterControls
            :conversation="currentConversation"
            :conversation-started="conversationStarted"
            :provider-accounts="currentUser?.provider_accounts"
            :error="modelChangeError"
            :show-context-usage="showContextUsage"
            :context-usage="contextUsage"
            :provider="conversationProvider"
            :usage-summary="lastUsage?.short"
            :show-rate-limit="supportsRateLimitEvents && hasRateLimitInfo"
            :rate-limits="rateLimits"
            @updated="applyModelChange"
            @error="onModelChangeError"
          />
        </template>
      </ChatComposer>
    </div>

    <!-- Drag handle between chat and workspace. Renders only when the workspace
         is open — collapsed-state expansion is driven by the chat-header button
         alone, so the handle has no reason to be visible in that branch.

         The divider stays 1px wide visually; the grab area is the invisible
         12px band inside it. That band needs `z-10` because it overflows the
         1px parent and the workspace column is a later sibling — without it
         the panel paints over the right half and only ~5px stay grabbable. -->
    <div
      v-if="!isWorkspaceCollapsed"
      class="resize-handle group relative w-px shrink-0 cursor-col-resize transition-colors hover:bg-primary/30"
      :class="isDragging ? 'bg-primary/30' : 'bg-[#eeeeee] dark:bg-border'"
      style="touch-action: none"
      data-testid="workspace-resize-handle"
      @mousedown="onDragStart"
      @touchstart="onTouchStart"
    >
      <span
        class="absolute inset-y-0 left-1/2 z-10 w-3 -translate-x-1/2 touch-none"
        aria-hidden="true"
      />
    </div>

    <!-- Workspace panel — pinned to 400px by default per UI design. When
         collapsed, useResizablePanels emits a zero-width style that hides the
         pane; the expand button in the chat header is the way back. -->
    <div
      class="workspace-panel flex min-h-0 flex-col bg-background"
      :class="workspaceFullscreen ? 'absolute inset-0 z-30' : ''"
      :style="workspaceFullscreen ? undefined : workspacePanelStyle"
      data-testid="desktop-artifact-panel"
    >
      <div class="flex-1 flex flex-col min-h-0 bg-background">
        <div v-if="!currentUser" class="text-muted-foreground text-center py-10 px-5 text-sm">
          {{ t("chat.selectUserWorkspace") }}
        </div>
        <WorkspacePanel
          v-else
          ref="workspacePanelRef"
          :user-id="currentUser.id"
          :conversation-id="currentConversationId"
          :user-work-dir="currentUser.work_dir"
          :conversation-work-dir="conversationWorkDir"
          :initial-path="workspaceInitialPath"
          can-collapse
          file-open-target="panel"
          @collapse="collapseWorkspace"
          @fullscreen-change="workspaceFullscreen = $event"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
@media (max-width: 1080px) and (min-width: 768px) {
  .chat-panel {
    width: 100% !important;
  }

  .workspace-panel,
  .resize-handle {
    display: none;
  }
}
</style>
