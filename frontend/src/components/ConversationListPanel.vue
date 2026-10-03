<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import {
  Coffee,
  Copy,
  FileArchive,
  Link2Off,
  Loader2,
  PanelLeft,
  Pin,
  Share2,
  SquarePen,
  Trash2,
} from "lucide-vue-next";
import type { Conversation, User } from "@/composables/useApi";
import type { RunningConversationActivity } from "@/stores/agentActivityStore";
import type { ConversationAttention } from "@/stores/conversationAttentionStore";
import Button from "@/components/ui/button/Button.vue";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from "@/components/ui/context-menu";
import { relativeTime } from "@/lib/format";
import { useInlineRename } from "@/composables/useInlineRename";
import { useConversations } from "@/composables/useConversations";
import { useConversationGestures } from "@/composables/useConversationGestures";
import { useIsTouch } from "@/composables/useIsTouch";
import { isPrimaryModifier } from "@/lib/primaryModifier";
import { useI18n } from "vue-i18n";
import ConversationSwipeActions from "@/components/ConversationSwipeActions.vue";
import ConversationSearchControl from "@/components/ConversationSearchControl.vue";
import AccountMenu from "@/components/AccountMenu.vue";

const { isTouch } = useIsTouch();

const { isCreatingConversation, loadAllConversationsForAgent } = useConversations();
const { t } = useI18n();
const NEW_CONVERSATION_COOLDOWN_MS = 1000;

const props = defineProps<{
  conversations: Conversation[];
  currentConversationId: string;
  userId: string;
  runningConversationIds?: string[];
  conversationAttention?: Record<string, ConversationAttention>;
  // Paging affordance, driven from the parent so the panel stays a pure view
  // over whatever list it is handed.
  hasMore?: boolean;
  loadingMore?: boolean;
  // Gates the diagnostics export. The bundle includes the account-level
  // config dir, so the server refuses non-admins outright; hiding the entry
  // just keeps the menu honest about what the viewer can actually do.
  isAdmin?: boolean;
  accountName?: string;
  accountUsername?: string;
  // Forwarded to the account menu, which carries the global utilities the
  // agent rail hides while this panel is docked.
  helpAvailable?: boolean;
  upgradeAvailable?: boolean;
  users?: User[];
  runningConversations?: RunningConversationActivity[];
  queuedConversations?: RunningConversationActivity[];
  standalone?: boolean;
}>();

const emit = defineEmits<{
  "select-conversation": [id: string];
  "new-conversation": [];
  // skipConfirm is true for an explicit confirmation alternative:
  // swipe-delete, or primary-modifier-click on the trash button.
  "delete-conversation": [id: string, skipConfirm: boolean];
  "rename-conversation": [id: string, title: string];
  "toggle-pinned": [id: string, pinned: boolean];
  "share-conversation": [id: string];
  "unshare-conversation": [id: string];
  "copy-share-link": [id: string];
  "export-bundle": [id: string];
  // New authoritative ordered pinned-id set after a drag-reorder.
  "reorder-pinned": [pinnedIds: string[]];
  "load-more": [];
  "toggle-conversations": [];
  "open-settings": [];
  "open-admin-settings": [];
  "open-marketplace": [];
  "open-help": [];
}>();

const newConversationCoolingDown = ref(false);
const searchQuery = ref("");
const searchLoading = ref(false);
let fullSearchListLoaded = false;
let newConversationCooldownTimer: ReturnType<typeof setTimeout> | null = null;

const filteredConversations = computed(() => {
  const query = searchQuery.value.trim().toLocaleLowerCase();
  if (!query) return props.conversations;
  return props.conversations.filter((conversation) =>
    conversationTitle(conversation).toLocaleLowerCase().includes(query),
  );
});

watch(searchQuery, (query) => {
  if (!query.trim() || fullSearchListLoaded || searchLoading.value) return;
  searchLoading.value = true;
  void loadAllConversationsForAgent(props.userId).then((loaded) => {
    fullSearchListLoaded = loaded;
    searchLoading.value = false;
  });
});

function handleNewConversationClick() {
  if (newConversationCoolingDown.value || isCreatingConversation.value) return;
  newConversationCoolingDown.value = true;
  emit("new-conversation");
  newConversationCooldownTimer = setTimeout(() => {
    newConversationCoolingDown.value = false;
    newConversationCooldownTimer = null;
  }, NEW_CONVERSATION_COOLDOWN_MS);
}

onBeforeUnmount(() => {
  if (newConversationCooldownTimer) clearTimeout(newConversationCooldownTimer);
});

const {
  editingId,
  editingTitle,
  composing,
  start,
  commit: commitRename,
  cancel: cancelRename,
  onEnter: onRenameEnter,
} = useInlineRename({
  onCommit: (id, title) => emit("rename-conversation", id, title),
  focusInput: () => {
    const input = document.querySelector<HTMLInputElement>(".rename-input");
    input?.focus();
    input?.select();
  },
});

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

function handleRowClick(id: string) {
  if (gestures.shouldSelect(id)) emit("select-conversation", id);
}

function startEditing(conv: Conversation) {
  start(conv.id, conv.title || "");
}

function conversationTitle(conv: Conversation): string {
  return conv.title || t("conversations.new");
}
</script>

<template>
  <div
    class="conversation-panel flex h-full w-56 min-w-56 flex-col border-r border-[#eeeeee] bg-[#f7f7f7] dark:border-[#2a2a2a] dark:bg-[#151515]"
    :class="{
      'conversation-dragging': gestures.isDraggingAny(),
      'conversation-panel--standalone': standalone,
    }"
  >
    <div
      class="workspace-brandbar relative z-30 flex h-[52px] shrink-0 items-center justify-between px-3"
    >
      <div class="flex items-center gap-2.5">
        <span class="grid size-7 place-items-center rounded-full bg-[#191919] text-white">
          <Coffee class="size-4" aria-hidden="true" />
        </span>
        <strong
          class="text-[18px] font-medium tracking-[-0.02em] text-[#191919] dark:text-[#f5f5f5]"
          >DayMug</strong
        >
      </div>
      <button
        type="button"
        class="grid size-8 place-items-center rounded-[6px] text-[#555] transition-colors hover:bg-[#e9e9e9] hover:text-[#191919] dark:text-[#aaa] dark:hover:bg-[#252525] dark:hover:text-white"
        :aria-label="t('sidebar.hideConversations')"
        data-testid="conversation-panel-collapse"
        @click="emit('toggle-conversations')"
      >
        <PanelLeft class="size-5" />
      </button>
    </div>

    <!-- New conversation button -->
    <div class="workspace-sidebar-controls relative z-20 space-y-1.5 pb-3 pt-2.5">
      <Button
        variant="default"
        size="sm"
        class="h-9 w-full justify-start gap-1.5 rounded-[8px] bg-[#191919] px-3 text-[14px] has-[>svg]:px-3 font-normal text-white shadow-none hover:bg-[#2b2b2b] dark:bg-[#f3f3f3] dark:text-[#191919] dark:hover:bg-white"
        :disabled="isCreatingConversation || newConversationCoolingDown"
        @click="handleNewConversationClick"
      >
        <SquarePen class="size-4" aria-hidden="true" />
        <span>{{ t("conversations.new") }}</span>
      </Button>
      <ConversationSearchControl v-model="searchQuery" :loading="searchLoading" />
    </div>

    <!-- Conversation list -->
    <div class="conversation-scroll wy-scroll min-h-0 flex-1 overflow-y-auto pb-2 pt-1">
      <div
        class="conversation-card flex flex-col gap-0.5 overflow-hidden rounded-[12px] border border-[#e6e6e6] bg-[#f2f2f2] p-1 dark:border-[#2a2a2a] dark:bg-[#1c1c1c]"
      >
        <!-- Each row sits in a shell that holds a swipe-to-delete action behind
           the opaque foreground. Touch: left-swipe reveals the action. Touch +
           mouse: long-press/hold then drag vertically to reorder/pin/unpin.
           data-* attrs let the gesture composable read the live order + pinned
           state straight off the DOM. -->
        <div
          v-for="conv in filteredConversations"
          :key="conv.id"
          class="row-shell relative"
          data-conv-shell
          :data-conv-id="conv.id"
          :data-pinned="conv.pinned ? '1' : '0'"
        >
          <!-- Swipe actions revealed by a left swipe. Occluded by the opaque
             foreground at rest, so invisible to mouse users. -->
          <ConversationSwipeActions
            v-show="gestures.showAction(conv.id)"
            :shared="conv.shared"
            @share="gestures.shareSwiped(conv.id)"
            @delete="gestures.deleteSwiped(conv.id)"
          />
          <ContextMenu>
            <ContextMenuTrigger as-child>
              <div
                class="conv-row group relative z-10 flex h-[38px] cursor-pointer select-none items-center justify-between rounded-[8px] py-0 pl-3 pr-2 transition-colors hover:bg-white dark:hover:bg-[#262626]"
                data-cursor-surface="pointer"
                :class="[
                  conv.id === currentConversationId
                    ? 'bg-white dark:bg-[#262626]'
                    : 'bg-transparent',
                  gestures.isDragging(conv.id) ? 'shadow-lg ring-1 ring-primary/40' : '',
                ]"
                :style="gestures.rowStyle(conv.id)"
                @click="handleRowClick(conv.id)"
                @mousedown="gestures.onMouseDown($event, conv.id)"
                @touchstart.passive="gestures.onTouchStart($event, conv.id)"
                @touchmove="gestures.onTouchMove($event, conv.id)"
                @touchend="gestures.onTouchEnd(conv.id)"
                @touchcancel="gestures.onTouchCancel()"
              >
                <div
                  class="conversation-title relative min-w-0 flex-1 pr-5"
                  @dblclick.stop="startEditing(conv)"
                >
                  <input
                    v-if="editingId === conv.id"
                    v-model="editingTitle"
                    class="rename-input w-full text-sm bg-input border border-border rounded px-1 py-0 outline-none text-foreground"
                    @compositionstart="composing = true"
                    @compositionend="composing = false"
                    @keydown.enter="onRenameEnter($event, conv.id)"
                    @keydown.escape="cancelRename"
                    @blur="commitRename(conv.id)"
                    @click.stop
                  />
                  <div
                    v-else
                    class="truncate text-[14px] leading-[1.25] text-[#191919] dark:text-[#e6e6e6]"
                  >
                    {{ conversationTitle(conv) }}
                  </div>
                  <div
                    class="absolute right-0 top-1/2 flex -translate-y-1/2 items-center gap-1.5 text-[11px] text-muted-foreground/70"
                  >
                    <span class="sr-only">{{ relativeTime(conv.updated_at) }}</span>
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
                  </div>
                </div>
                <span class="ml-1.5 grid size-4 shrink-0 place-items-center self-center">
                  <span
                    class="col-start-1 row-start-1 grid place-items-center transition-opacity"
                    :class="isTouch ? '' : 'group-hover:opacity-0'"
                    aria-hidden="true"
                  >
                    <Loader2
                      v-if="runningConversationIds?.includes(conv.id)"
                      data-testid="conversation-running-spinner"
                      class="size-3.5 animate-spin text-muted-foreground/70"
                    />
                    <span
                      v-else-if="conversationAttention?.[conv.id]"
                      data-testid="unread-completion-conversation-dot"
                      class="attention-dot size-[8px] rounded-full"
                      :data-attention="conversationAttention[conv.id]"
                    />
                  </span>
                  <button
                    v-if="!isTouch"
                    class="delete-btn col-start-1 row-start-1 flex size-4 cursor-pointer items-center justify-center rounded text-muted-foreground/50 opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
                    :title="t('conversations.deleteTitleShiftHint')"
                    @click.stop="emit('delete-conversation', conv.id, isPrimaryModifier($event))"
                  >
                    <Trash2 class="size-3.5" />
                  </button>
                </span>
              </div>
            </ContextMenuTrigger>
            <ContextMenuContent class="min-w-36 w-max whitespace-nowrap">
              <ContextMenuItem @select="emit('toggle-pinned', conv.id, !conv.pinned)">
                <Pin class="size-4" :class="conv.pinned ? 'fill-current' : ''" />
                {{ conv.pinned ? t("conversations.unpin") : t("conversations.pin") }}
              </ContextMenuItem>
              <ContextMenuItem
                @select="
                  conv.shared
                    ? emit('unshare-conversation', conv.id)
                    : emit('share-conversation', conv.id)
                "
              >
                <Link2Off v-if="conv.shared" class="size-4" />
                <Share2 v-else class="size-4" />
                {{ conv.shared ? t("conversations.unshare") : t("conversations.share") }}
              </ContextMenuItem>
              <ContextMenuItem v-if="conv.shared" @select="emit('copy-share-link', conv.id)">
                <Copy class="size-4" />
                {{ t("conversations.copyShareLink") }}
              </ContextMenuItem>
              <ContextMenuItem v-if="isAdmin" @select="emit('export-bundle', conv.id)">
                <FileArchive class="size-4" />
                {{ t("conversations.exportBundle") }}
              </ContextMenuItem>
              <ContextMenuItem
                variant="destructive"
                @select="emit('delete-conversation', conv.id, false)"
              >
                <Trash2 class="size-4" />
                {{ t("conversations.delete") }}
              </ContextMenuItem>
            </ContextMenuContent>
          </ContextMenu>
        </div>
      </div>
      <div
        v-if="filteredConversations.length === 0"
        class="text-muted-foreground text-center text-xs py-6"
      >
        {{
          searchLoading
            ? t("conversations.searching")
            : searchQuery.trim()
              ? t("conversations.searchEmpty")
              : t("conversations.emptyShort")
        }}
      </div>
      <!-- Explicit button rather than an infinite-scroll sentinel: the list
           doubles as a drag-to-reorder surface, and auto-appending rows under
           a held pointer moves the drop target out from under it. -->
      <div v-if="hasMore" class="conversation-load-more py-3">
        <button
          type="button"
          class="w-full rounded-md border border-sidebar-border py-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-60"
          data-testid="load-more-conversations"
          :disabled="loadingMore"
          @click="emit('load-more')"
        >
          {{ loadingMore ? t("conversations.loadingMore") : t("conversations.loadMore") }}
        </button>
      </div>
    </div>

    <!-- Identity and menu are separate targets: the name is a label, the
         trailing button opens the account menu. A whole-row button used to
         jump straight to Settings, which left marketplace and help
         unreachable whenever the docked panel hid the rail footer. -->
    <div
      v-if="accountName"
      class="workspace-account-footer relative z-20 my-[15px] flex h-8 shrink-0 items-center gap-2"
    >
      <div class="flex min-w-0 flex-1 items-center gap-2">
        <span
          class="grid size-7 shrink-0 place-items-center rounded-full bg-[#25283b] text-[11px] font-semibold text-white"
          aria-hidden="true"
        >
          {{ accountName.slice(0, 1) }}
        </span>
        <strong
          class="truncate text-[14px] font-normal leading-5 text-[#191919] dark:text-[#f1f1f1]"
          >{{ accountName }}</strong
        >
      </div>
      <AccountMenu
        :name="accountName"
        :username="accountUsername"
        :help-available="helpAvailable"
        :upgrade-available="upgradeAvailable"
        :is-admin="isAdmin"
        :users="users"
        :running-conversations="runningConversations"
        :queued-conversations="queuedConversations"
        @open-marketplace="emit('open-marketplace')"
        @open-help="emit('open-help')"
        @open-settings="emit('open-settings')"
        @open-admin-settings="emit('open-admin-settings')"
      />
    </div>
  </div>
</template>

<style scoped>
/* The long-press-to-pin gesture is killed on iOS if the OS hijacks the press
   for text selection / the callout menu. Suppress both on the row, but keep
   the rename input editable and selectable. */
.conv-row {
  -webkit-touch-callout: none;
  -webkit-user-select: none;
}

.conv-row .rename-input {
  -webkit-user-select: text;
  user-select: text;
}

/* The agent rail is a 48px column to the left of this panel, and the two make
   up one sidebar. Brand bar, controls and account footer therefore bleed back
   over the rail so they measure from the sidebar's own edge, while the
   conversation card stops short of the rail and leaves the 10px gutter the
   avatars sit in. */
@media (min-width: 768px) {
  .conversation-panel:not(.conversation-panel--standalone) .workspace-brandbar {
    width: calc(100% + 48px);
    margin-left: -48px;
  }

  .conversation-panel:not(.conversation-panel--standalone) .workspace-sidebar-controls {
    width: calc(100% + 24px);
    margin-left: -36px;
  }

  /* The footer sits 18px in from the sidebar edge, a step deeper than the
     12px controls above it, so the profile reads as a quiet signature rather
     than another full-width control. */
  .conversation-panel:not(.conversation-panel--standalone) .workspace-account-footer {
    width: calc(100% + 12px);
    margin-left: -30px;
  }

  /* The card overhangs the panel edge by 2px toward the rail. Shifting the
     scroller rather than the card keeps that overhang inside the scroll
     container's clip, which otherwise shaved off the card's left border. */
  .conversation-panel:not(.conversation-panel--standalone) .conversation-scroll {
    margin-left: -2px;
  }

  /* Load more shares the card's box so the button centres under the list
     rather than under the scroller, which the card deliberately stops short
     of. */
  .conversation-panel:not(.conversation-panel--standalone) .conversation-card,
  .conversation-panel:not(.conversation-panel--standalone) .conversation-load-more {
    width: calc(100% - 11px);
  }
}

.conversation-panel--standalone .workspace-sidebar-controls {
  margin-inline: 12px;
}

.conversation-panel--standalone .workspace-account-footer {
  margin-inline: 18px;
}

.conversation-panel--standalone .conversation-card,
.conversation-panel--standalone .conversation-load-more {
  margin-inline: 8px;
}

/* Keep the cursor stable over gaps exposed while rows translate, without
   leaking a cursor override to the rest of the document. */
.conversation-dragging {
  cursor: pointer;
}
</style>
