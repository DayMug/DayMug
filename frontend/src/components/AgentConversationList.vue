<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import type { User, Conversation } from "@/composables/useApi";
import type { ConversationAttention } from "@/stores/conversationAttentionStore";
import { Button } from "@/components/ui/button";
import { Plus, MoreHorizontal, Trash2, BellOff, Pin, Share2 } from "lucide-vue-next";
import { relativeTime } from "@/lib/format";
import { useInlineRename } from "@/composables/useInlineRename";
import { useConversationGestures } from "@/composables/useConversationGestures";
import { useIsTouch } from "@/composables/useIsTouch";
import { isPrimaryModifier } from "@/lib/primaryModifier";
import { useI18n } from "vue-i18n";
import RunningProgressBar from "@/components/RunningProgressBar.vue";
import ConversationSwipeActions from "@/components/ConversationSwipeActions.vue";
import ConversationSearchControl from "@/components/ConversationSearchControl.vue";
import { useConversations } from "@/composables/useConversations";

const { isTouch } = useIsTouch();

const { t } = useI18n();
const { loadAllConversationsForAgent } = useConversations();
const NEW_CONVERSATION_COOLDOWN_MS = 1000;

// One instance is mounted per expanded agent row, so each agent owns
// its own copy of the inline-rename / IME state below. That avoids the
// quirks of sharing a single editing buffer across the whole agents
// list (a stale `editingId` belonging to agent A would briefly leak
// into agent B's row when the user expanded B).
const props = defineProps<{
  user: User;
  conversations: Conversation[];
  currentConversationId: string;
  // True while the parent is fetching this agent's conversations. Used
  // to flip the empty-state placeholder to "Loading conversations…" so
  // the user doesn't see "No conversations yet." flash for an agent
  // that's mid-fetch and may actually have conversations.
  loading?: boolean;
  // Mirrors the desktop panel's prop: when the human owner has no
  // notification channel configured, the muted-state bell stays hidden
  // since there's no channel to (re-)enable.
  notificationsConfigured?: boolean;
  runningConversationIds?: string[];
  conversationAttention?: Record<string, ConversationAttention>;
}>();

const emit = defineEmits<{
  "select-conversation": [user: User, id: string];
  "new-conversation": [user: User];
  // skipConfirm is true for an explicit confirmation alternative:
  // swipe-delete, or primary-modifier-click on the trash button.
  "delete-conversation": [id: string, skipConfirm: boolean];
  "rename-conversation": [id: string, title: string];
  "toggle-notifications": [id: string, enabled: boolean];
  "toggle-pinned": [id: string, pinned: boolean];
  "share-conversation": [id: string];
  "unshare-conversation": [id: string];
  // New authoritative ordered pinned-id set after a drag-reorder.
  "reorder-pinned": [pinnedIds: string[]];
  "open-agent-settings": [user: User];
}>();

const rootEl = ref<HTMLElement | null>(null);
const newConversationCoolingDown = ref(false);
const searchQuery = ref("");
const searchLoading = ref(false);
let fullSearchListLoaded = false;
let newConversationCooldownTimer: ReturnType<typeof setTimeout> | null = null;

const filteredConversations = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase();
  if (!query) return props.conversations;
  const fallbackTitle = t("conversations.new").toLocaleLowerCase();
  return props.conversations.filter((conversation) =>
    (conversation.title || fallbackTitle).toLocaleLowerCase().includes(query),
  );
});

watch(searchQuery, (query) => {
  if (!query.trim() || fullSearchListLoaded || searchLoading.value) return;
  searchLoading.value = true;
  void loadAllConversationsForAgent(props.user.id).then((loaded) => {
    fullSearchListLoaded = loaded;
    searchLoading.value = false;
  });
});

const {
  editingId,
  editingTitle,
  composing,
  start,
  commit: commitRename,
  onEnter: onRenameEnter,
} = useInlineRename({
  onCommit: (id, title) => emit("rename-conversation", id, title),
  // Scope the input lookup to this agent's panel — multiple agents
  // could be expanded in the future and a global query would grab the
  // wrong row.
  focusInput: () => {
    const input = rootEl.value?.querySelector<HTMLInputElement>(".m-rename-input");
    input?.focus();
    input?.select();
  },
});

// Touch gestures (shared with the desktop panel): long-press then drag up to
// pin / down to unpin, and swipe left to reveal a delete action.
const gestures = useConversationGestures({
  onReorder: (pinnedIds) => emit("reorder-pinned", pinnedIds),
  // Swipe-left + tapping the revealed destructive button is itself a deliberate
  // two-step confirmation (native iOS Mail / Gmail delete immediately, no extra
  // dialog). Skip the window.confirm prompt: it's redundant here and, worse, is
  // suppressed in many mobile in-app browsers / WebViews — where it returns
  // falsy without showing, silently aborting the only delete path on touch.
  onDelete: (id) => emit("delete-conversation", id, true),
  onShare: (id) => {
    const conv = props.conversations.find((c) => c.id === id);
    if (conv?.shared) emit("unshare-conversation", id);
    else emit("share-conversation", id);
  },
});

function handleCardClick(c: Conversation) {
  if (gestures.shouldSelect(c.id)) emit("select-conversation", props.user, c.id);
}

function startEditing(c: Conversation, e: Event) {
  e.stopPropagation();
  start(c.id, c.title || "");
}

function handleNewConversationClick() {
  if (newConversationCoolingDown.value) return;
  newConversationCoolingDown.value = true;
  emit("new-conversation", props.user);
  newConversationCooldownTimer = setTimeout(() => {
    newConversationCoolingDown.value = false;
    newConversationCooldownTimer = null;
  }, NEW_CONVERSATION_COOLDOWN_MS);
}

onBeforeUnmount(() => {
  if (newConversationCooldownTimer) clearTimeout(newConversationCooldownTimer);
});
</script>

<template>
  <div
    ref="rootEl"
    class="agent-conversations border-b border-[var(--edge-soft)] bg-[var(--paper-alt)]"
    :class="{ 'conversation-dragging': gestures.isDraggingAny() }"
    :data-agent-id="user.id"
  >
    <div class="flex items-center justify-between px-5 pt-3 pb-1">
      <div class="text-[10px] font-bold uppercase tracking-[0.06em] text-muted-foreground">
        {{ t("conversations.active") }}
      </div>
      <button
        class="size-6 flex items-center justify-center text-muted-foreground hover:text-foreground cursor-pointer"
        :title="t('conversations.editAgent')"
        @click.stop="emit('open-agent-settings', user)"
      >
        <MoreHorizontal class="size-4" />
      </button>
    </div>

    <div class="space-y-2 px-3 pb-2">
      <Button
        variant="ink"
        class="w-full gap-2 h-9 cursor-pointer transition-[transform,box-shadow] duration-150 ease-out hover:-translate-y-0.5 hover:shadow-md active:translate-y-0 active:scale-[0.98] active:shadow-sm motion-reduce:transform-none motion-reduce:transition-none"
        :disabled="newConversationCoolingDown"
        @click.stop="handleNewConversationClick"
      >
        <Plus class="size-4" />
        {{ t("conversations.new") }}
      </Button>
      <ConversationSearchControl v-model="searchQuery" :loading="searchLoading" />
    </div>

    <div
      v-if="filteredConversations.length === 0"
      class="text-muted-foreground text-center text-sm py-6"
    >
      {{
        props.loading || searchLoading
          ? t(searchLoading ? "conversations.searching" : "conversations.loading")
          : searchQuery.trim()
            ? t("conversations.searchEmpty")
            : t("conversations.empty")
      }}
    </div>

    <div class="pb-3">
      <!-- Each card sits in a shell holding a swipe-to-delete action behind
           the opaque card. Touch: swipe left reveals delete. Touch + mouse:
           long-press/hold then drag vertically to reorder/pin/unpin. data-*
           attrs expose live order + pinned state to the gesture composable. -->
      <div
        v-for="conv in filteredConversations"
        :key="conv.id"
        class="relative mx-3 mb-2"
        data-conv-shell
        :data-conv-id="conv.id"
        :data-pinned="conv.pinned ? '1' : '0'"
      >
        <ConversationSwipeActions
          v-show="gestures.showAction(conv.id)"
          :shared="conv.shared"
          @share="gestures.shareSwiped(conv.id)"
          @delete="gestures.deleteSwiped(conv.id)"
        />
        <div
          class="m-conversation-card conv-row relative z-10 px-3.5 py-3 rounded-md bg-card border cursor-pointer transition-colors select-none"
          data-cursor-surface="pointer"
          :class="[
            conv.id === currentConversationId
              ? 'border-primary'
              : 'border-border hover:bg-[var(--paper-alt)]',
            gestures.isDragging(conv.id) ? 'shadow-lg ring-1 ring-primary/40' : '',
          ]"
          :style="gestures.rowStyle(conv.id)"
          @click="handleCardClick(conv)"
          @mousedown="gestures.onMouseDown($event, conv.id)"
          @touchstart.passive="gestures.onTouchStart($event, conv.id)"
          @touchmove="gestures.onTouchMove($event, conv.id)"
          @touchend="gestures.onTouchEnd(conv.id)"
          @touchcancel="gestures.onTouchCancel()"
        >
          <RunningProgressBar v-if="runningConversationIds?.includes(conv.id)" />
          <div
            v-if="conv.id === currentConversationId"
            class="absolute left-0 top-3 bottom-3 w-[3px] rounded-r bg-primary"
            aria-hidden="true"
          />
          <div class="relative flex items-center gap-1.5 mb-1">
            <span
              v-if="conversationAttention?.[conv.id]"
              data-testid="unread-completion-conversation-dot"
              class="attention-dot absolute -left-1.5 -top-0.5 size-[7px] rounded-full"
              :data-attention="conversationAttention[conv.id]"
              aria-hidden="true"
            />
            <input
              v-if="editingId === conv.id"
              v-model="editingTitle"
              class="m-rename-input flex-1 min-w-0 text-[14px] font-semibold bg-input border border-border rounded px-1 py-0 outline-none text-foreground"
              @compositionstart="composing = true"
              @compositionend="composing = false"
              @keydown.enter="onRenameEnter($event, conv.id)"
              @keydown.escape="editingId = null"
              @blur="commitRename(conv.id)"
              @click.stop
            />
            <span
              v-else
              class="flex-1 text-[14px] font-semibold text-foreground truncate"
              @dblclick.stop="startEditing(conv, $event)"
            >
              {{ conv.title || t("conversations.new") }}
            </span>
          </div>
          <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
            <span class="font-mono shrink-0">{{ relativeTime(conv.updated_at, "compact") }}</span>
            <span
              v-if="conv.pinned"
              class="conversation-status text-primary"
              :title="t('conversations.pinnedStatus')"
              :aria-label="t('conversations.pinnedStatus')"
            >
              <Pin class="size-3 fill-current" aria-hidden="true" />
            </span>
            <span
              v-if="conv.shared"
              class="conversation-status conversation-shared-status text-primary"
              :title="t('conversations.sharedStatus')"
              :aria-label="t('conversations.sharedStatus')"
            >
              <Share2 class="size-3" aria-hidden="true" />
            </span>
            <button
              v-if="notificationsConfigured && !conv.notifications_enabled"
              class="cursor-pointer flex items-center gap-1 text-[oklch(0.62_0.18_70)] hover:text-[oklch(0.55_0.20_70)]"
              :title="t('conversations.notificationsSilencedTap')"
              @click.stop="emit('toggle-notifications', conv.id, true)"
            >
              <BellOff class="size-3" />
            </button>
            <span class="flex-1" />
            <!-- On touch devices swipe-left deletes, so the always-on trash
                 button is redundant clutter — hide it there. -->
            <button
              v-if="!isTouch"
              class="delete-btn cursor-pointer text-muted-foreground/60 hover:text-destructive transition-colors"
              :title="t('conversations.deleteTitleShiftHint')"
              @click.stop="emit('delete-conversation', conv.id, isPrimaryModifier($event))"
            >
              <Trash2 class="size-3" />
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* iOS hijacks a long-press for text selection / the callout menu, which kills
   the long-press-to-pin gesture. Suppress both on the card, but keep the
   rename input editable and selectable. */
.conv-row {
  -webkit-touch-callout: none;
  -webkit-user-select: none;
}

.conv-row .m-rename-input {
  -webkit-user-select: text;
  user-select: text;
}

.conversation-dragging {
  cursor: pointer;
}
</style>
