<script setup lang="ts">
import { computed, ref, watch } from "vue";
import type { User, Conversation } from "@/composables/useApi";
import type { ConversationAttention } from "@/stores/conversationAttentionStore";
import AgentAvatar from "@/components/AgentAvatar.vue";
import { Button } from "@/components/ui/button";
import {
  HelpCircle,
  Settings as SettingsIcon,
  ChevronRight,
  Loader2,
  Plus,
  RefreshCw,
} from "lucide-vue-next";
import AgentConversationList from "@/components/AgentConversationList.vue";
import { useAgentDragReorder } from "@/composables/useAgentDragReorder";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const props = defineProps<{
  users: User[];
  // False until the roster has been fetched; "+ New agent" waits for it.
  usersLoaded?: boolean;
  // The active agent — used to auto-expand the row the user just came
  // back from (so a back-from-session lands on the same conversation
  // list they were in).
  currentUser: User | null;
  // Per-agent conversation cache. Each row hands its own slice to its
  // own AgentConversationList instance, so multiple agents can be
  // browsed without polluting each other's editing/IME state.
  conversationsByAgent: Record<string, Conversation[]>;
  // Per-agent fetch-in-flight flag. Truthy entries flip the inline list
  // into a loading placeholder so the user doesn't see a flash of stale
  // (or empty) conversations left over from a previous load while the
  // new fetch resolves.
  loadingByAgent?: Record<string, boolean>;
  currentConversationId: string;
  // Whether to show the "?" help button alongside "+ New agent". The
  // parent (App.vue) gates this on whether an admin has posted a
  // markdown help doc — absent state hides the button entirely.
  helpAvailable?: boolean;
  // Pull-to-refresh in-flight flag. Owned by the parent so the spinner
  // stays pinned for the full duration of loadUsers + per-agent refetch,
  // independent of touch lifecycle.
  refreshing?: boolean;
  // True when the signed-in human owner has configured at least one push
  // channel (Bark or PushDeer). Forwarded to each AgentConversationList so
  // the per-conversation bell icon stays hidden when there's no channel
  // to (re-)enable.
  notificationsConfigured?: boolean;
  runningAgentIds?: string[];
  runningConversationIds?: string[];
  agentAttention?: Record<string, ConversationAttention>;
  conversationAttention?: Record<string, ConversationAttention>;
}>();

const emit = defineEmits<{
  // Fires when an agent row is expanded. The parent should ensure that
  // agent's conversations are loaded into `conversationsByAgent` (cheap
  // no-op if already cached). This does NOT switch the active user;
  // that only happens on conversation selection.
  "select-agent": [user: User];
  // Carries the owning agent so the parent can switch active user (if
  // needed) before drilling into the conversation.
  "select-conversation": [user: User, id: string];
  "new-conversation": [user: User];
  "delete-conversation": [id: string, skipConfirm: boolean];
  "rename-conversation": [id: string, title: string];
  "toggle-notifications": [id: string, enabled: boolean];
  "toggle-pinned": [id: string, pinned: boolean];
  "share-conversation": [id: string];
  "unshare-conversation": [id: string];
  // Carries the owning agent id so the parent knows whose pinned set to persist.
  "reorder-pinned": [userId: string, pinnedIds: string[]];
  // New manual order of the agents after a long-press-drag reorder. Carries the
  // full agent-id order; the parent persists it via PUT /api/user-order.
  "reorder-agents": [ids: string[]];
  "open-agent-settings": [user: User];
  "open-settings": [];
  "add-agent": [];
  "open-help": [];
  // Pull-to-refresh release past threshold. Carries the currently
  // expanded agent id (or null) so the parent can refetch that agent's
  // conversations along with the users list, without having to lift the
  // expansion state out of this component.
  refresh: [expandedAgentId: string | null];
}>();

// Which agent's conversations are revealed inline. Tied to currentUser
// by default so a back-from-conversation click lands on the same
// agent's conversation list. Tapping the already-expanded row
// collapses it without changing the underlying selection.
const expandedAgentId = ref<string | null>(props.currentUser?.id ?? null);

watch(
  () => props.currentUser?.id,
  (id) => {
    if (id) expandedAgentId.value = id;
  },
);

// Eagerly request conversations for the auto-expanded agent on mount so
// the inline list isn't empty for one frame after the parent connects.
if (expandedAgentId.value) {
  const initial = props.users.find((u) => u.id === expandedAgentId.value);
  if (initial) emit("select-agent", initial);
}

function isExpanded(user: User): boolean {
  return expandedAgentId.value === user.id;
}

function conversationsFor(user: User): Conversation[] {
  return props.conversationsByAgent[user.id] ?? [];
}

function isLoadingFor(user: User): boolean {
  return props.loadingByAgent?.[user.id] === true;
}

// ── Long-press-drag row reordering ───────────────────────────────
// Every row — the human owner and the agents alike — participates, so the owner
// can be placed anywhere in the list. The order persists server-side via the
// same /api/user-order endpoint, which now stamps the owner's own row too.
const reorder = useAgentDragReorder({
  onReorder: (ids) => emit("reorder-agents", ids),
});

// The mobile "Agents" screen lists every row the caller owns: the signed-in
// human owner and its agents, all freely reorderable. The drag moves the
// picked-up row via a direct DOM transform (so it tracks the finger) and slides
// its neighbours; only on release does the parent persist a new order that flows
// back through `props.users`.
const displayUsers = computed(() => props.users);

function onAgentRowClick(user: User) {
  // Ignore the click that fires right after a drag so reordering never also
  // toggles the row's expansion.
  if (!reorder.shouldSelect()) return;
  if (isExpanded(user)) {
    expandedAgentId.value = null;
    return;
  }
  expandedAgentId.value = user.id;
  // Always ask the parent to ensure this agent's conversations are
  // loaded into the cache. Cheap no-op if already populated — the
  // composable refuses to refetch once cached so the conversation
  // count under each row stays stable instead of flickering on every
  // tap.
  emit("select-agent", user);
}

// ── Pull-to-refresh ──────────────────────────────────────────────
// Touch-driven pull on the agent list. Only engages when the scroll
// container is at the very top so it doesn't fight native scrolling.
// The indicator height tracks finger position with a rubber-band
// dampener; releasing past TRIGGER_DISTANCE emits `refresh`. The
// parent's `refreshing` prop pins the spinner until the refetch
// resolves, independent of touch lifecycle.
const TRIGGER_DISTANCE = 64;
const MAX_PULL = 96;
const RUBBER_BAND = 0.5;

const scrollContainer = ref<HTMLElement | null>(null);
const pullDistance = ref(0);
const pullActive = ref(false);
let touchStartY = 0;
let touchTracking = false;

function onTouchStart(e: TouchEvent) {
  const el = scrollContainer.value;
  if (!el || props.refreshing) {
    touchTracking = false;
    return;
  }
  // Only start tracking a pull when we're at the top of the list;
  // otherwise the gesture is a normal scroll.
  if (el.scrollTop > 0) {
    touchTracking = false;
    return;
  }
  touchTracking = true;
  touchStartY = e.touches[0].clientY;
  pullDistance.value = 0;
  pullActive.value = false;
}

function onTouchMove(e: TouchEvent) {
  if (!touchTracking) return;
  // While a long-press reorder is even *pending* on a row, it owns this touch:
  // arming a pull here would grow the indicator and push the whole rail down
  // mid-drag, corrupting the geometry the reorder captured (this is why the
  // first agent — the only row sitting at scrollTop 0 where pull arms — felt
  // immovable and nothing could be dropped above it). Stay dormant but keep
  // tracking: if the press turns out to be a scroll/pull, the reorder bows back
  // out to idle and we resume from the next move with the full travel intact.
  if (reorder.isEngaging()) {
    if (pullDistance.value !== 0 || pullActive.value) {
      pullDistance.value = 0;
      pullActive.value = false;
    }
    return;
  }
  const dy = e.touches[0].clientY - touchStartY;
  if (dy <= 0) {
    // Finger moved up: user is scrolling, not pulling. Release the
    // gesture so native scrolling resumes immediately.
    pullDistance.value = 0;
    pullActive.value = false;
    touchTracking = false;
    return;
  }
  // Rubber-band so the indicator decelerates as the user pulls further,
  // capped by MAX_PULL.
  const damped = Math.min(MAX_PULL, dy * RUBBER_BAND);
  pullDistance.value = damped;
  pullActive.value = true;
  // Suppress the browser's overscroll/bounce so the pull is smooth.
  if (e.cancelable) e.preventDefault();
}

function onTouchEnd() {
  if (!touchTracking) return;
  touchTracking = false;
  const shouldRefresh = pullDistance.value >= TRIGGER_DISTANCE && !props.refreshing;
  pullDistance.value = 0;
  pullActive.value = false;
  if (shouldRefresh) {
    emit("refresh", expandedAgentId.value);
  }
}

// Height/translate of the indicator strip. While refreshing, pin to
// TRIGGER_DISTANCE so the spinner stays visible until the parent flips
// the flag back. Otherwise tracks the current pull distance.
const indicatorOffset = computed(() => {
  if (props.refreshing) return TRIGGER_DISTANCE;
  return pullDistance.value;
});

// Arrow rotates 180° once the user has pulled far enough to commit.
const arrowFlipped = computed(() => pullDistance.value >= TRIGGER_DISTANCE);
</script>

<template>
  <!-- Mobile root screen — Agents heading + agent rows. Each row mounts
       its own AgentConversationList instance when expanded, so per-agent
       UI state (rename buffer, IME composition) stays scoped to its own
       agent and doesn't bleed across rows. -->
  <div class="flex flex-col h-full bg-card font-sans">
    <header class="px-5 pt-5 pb-4 border-b border-border bg-card shrink-0">
      <div class="flex items-end gap-3">
        <h1 class="text-[28px] font-bold tracking-[-0.02em] leading-none text-foreground">
          {{ t("agents.title") }}
        </h1>
        <span class="flex-1" />
        <button
          class="size-8 flex items-center justify-center text-muted-foreground hover:text-foreground rounded-md cursor-pointer -mb-1"
          :title="t('agents.settings')"
          @click="emit('open-settings')"
        >
          <SettingsIcon class="size-4" />
        </button>
      </div>
    </header>

    <div
      ref="scrollContainer"
      class="flex-1 overflow-y-auto relative"
      data-testid="agents-scroll"
      @touchstart.passive="onTouchStart"
      @touchmove="onTouchMove"
      @touchend="onTouchEnd"
      @touchcancel="onTouchEnd"
    >
      <!-- Pull-to-refresh indicator. Sits in the document flow so the
           agent rows are pushed down by the same amount the indicator
           grows — that's what sells the "pulling the list down" feel.
           A separate transition class smooths the snap-back when the
           user releases without committing. -->
      <div
        class="overflow-hidden flex items-end justify-center text-muted-foreground"
        :class="{ 'transition-[height] duration-200': !pullActive }"
        :style="{ height: `${indicatorOffset}px` }"
        data-testid="pull-indicator"
        aria-hidden="true"
      >
        <div v-if="indicatorOffset > 0" class="flex items-center gap-2 pb-2 text-xs">
          <Loader2 v-if="refreshing" class="size-4 animate-spin" />
          <RefreshCw
            v-else
            class="size-4 transition-transform duration-200"
            :class="{ 'rotate-180': arrowFlipped }"
          />
          <span v-if="refreshing">{{ t("agents.refreshing") }}</span>
          <span v-else-if="arrowFlipped">{{ t("agents.releaseToRefresh") }}</span>
          <span v-else>{{ t("agents.pullToRefresh") }}</span>
        </div>
      </div>

      <div data-agent-rail>
        <div
          v-for="user in displayUsers"
          :key="user.id"
          class="agent-row-wrap"
          :data-agent-wrap="user.id"
          @touchstart.passive="reorder.onTouchStart($event, user.id)"
          @touchmove="reorder.onTouchMove($event, user.id)"
          @touchend="reorder.onTouchEnd(user.id)"
          @touchcancel="reorder.onTouchCancel()"
          @mousedown="reorder.onMouseDown($event, user.id)"
          @dragstart.prevent
        >
          <button
            class="agent-row w-full flex items-center gap-3 px-5 py-3.5 border-b border-[var(--edge-soft)] cursor-pointer text-left hover:bg-[var(--paper-alt)] transition-colors select-none"
            data-cursor-surface="pointer"
            :class="{
              'bg-[var(--paper-alt)]': isExpanded(user),
              'agent-row-dragging': reorder.isDragging(user.id),
            }"
            :data-agent-id="user.id"
            :aria-expanded="isExpanded(user)"
            @click="onAgentRowClick(user)"
          >
            <AgentAvatar
              :name="user.name"
              :avatar="user.avatar"
              :bot-platforms="user.bot_platforms"
              :running="runningAgentIds?.includes(user.id)"
              :attention="agentAttention?.[user.id]"
              class="size-10 shrink-0"
              fallback-class="text-sm font-bold"
            />
            <!-- Two-line layout (name + work_dir). The conversation count
               used to live below as an optional third line, but
               appearing / disappearing on expand made the avatar bob
               relative to the text block — we hide it entirely so the
               row geometry stays fixed regardless of expansion / load
               state. -->
            <div class="flex-1 min-w-0">
              <div class="text-[15px] font-semibold text-foreground truncate">{{ user.name }}</div>
              <div
                class="text-[12px] font-mono text-muted-foreground mt-0.5 truncate"
                :title="user.work_dir"
              >
                {{ user.work_dir }}
              </div>
            </div>
            <ChevronRight
              class="size-3.5 text-muted-foreground shrink-0 transition-transform"
              :class="{ 'rotate-90': isExpanded(user) }"
            />
          </button>

          <!-- Per-agent component instance. The :key forces Vue to mount a
             fresh AgentConversationList when the expanded row changes,
             so local state (editing buffer, IME composition flag) never
             leaks from one agent to another. -->
          <AgentConversationList
            v-if="isExpanded(user)"
            :key="`conversations-${user.id}`"
            :user="user"
            :conversations="conversationsFor(user)"
            :loading="isLoadingFor(user)"
            :current-conversation-id="currentConversationId"
            :notifications-configured="notificationsConfigured"
            :running-conversation-ids="runningConversationIds"
            :conversation-attention="conversationAttention"
            @select-conversation="(u, id) => emit('select-conversation', u, id)"
            @new-conversation="(u) => emit('new-conversation', u)"
            @delete-conversation="(id, skipConfirm) => emit('delete-conversation', id, skipConfirm)"
            @rename-conversation="(id, title) => emit('rename-conversation', id, title)"
            @toggle-notifications="(id, enabled) => emit('toggle-notifications', id, enabled)"
            @toggle-pinned="(id, pinned) => emit('toggle-pinned', id, pinned)"
            @share-conversation="(id) => emit('share-conversation', id)"
            @unshare-conversation="(id) => emit('unshare-conversation', id)"
            @reorder-pinned="(ids) => emit('reorder-pinned', user.id, ids)"
            @open-agent-settings="(u) => emit('open-agent-settings', u)"
          />
        </div>
      </div>

      <div class="p-5 space-y-2">
        <Button
          v-if="helpAvailable"
          variant="ghost"
          class="w-full gap-2 h-10 text-muted-foreground"
          @click="emit('open-help')"
        >
          <HelpCircle class="size-4" />
          {{ t("agents.help") }}
        </Button>
        <Button
          v-if="usersLoaded"
          variant="outline"
          class="w-full gap-2 h-10 border-dashed"
          @click="emit('add-agent')"
        >
          <Plus class="size-4" />
          {{ t("agents.newAgent") }}
        </Button>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Lift the row under the finger so it reads as picked up. The drag itself is
   driven by inline transforms in useAgentDragReorder — the dragged row tracks
   the finger 1:1 while the neighbours it crosses slide via an eased
   transition. */
.agent-row-dragging {
  background-color: var(--paper-alt);
  box-shadow: 0 6px 16px rgb(0 0 0 / 0.18);
}
</style>
