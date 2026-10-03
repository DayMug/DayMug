<script setup lang="ts">
import { computed, ref } from "vue";
import {
  Activity,
  Coffee,
  HelpCircle,
  LayoutGrid,
  PanelLeftClose,
  PanelLeftOpen,
  Plus,
  Settings,
} from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import type { User } from "@/composables/useApi";
import type { RunningConversationActivity } from "@/stores/agentActivityStore";
import { agentDisplayName } from "@/lib/agentDisplayName";
import type { ConversationAttention } from "@/stores/conversationAttentionStore";
import { useAgentReorder } from "@/composables/useAgentReorder";
import AgentAvatar from "@/components/AgentAvatar.vue";
import RunningActivityTable from "@/components/RunningActivityTable.vue";
import { Badge } from "@/components/ui/badge";
import Button from "@/components/ui/button/Button.vue";
import Tooltip from "@/components/ui/tooltip/Tooltip.vue";
import TooltipContent from "@/components/ui/tooltip/TooltipContent.vue";
import TooltipProvider from "@/components/ui/tooltip/TooltipProvider.vue";
import TooltipTrigger from "@/components/ui/tooltip/TooltipTrigger.vue";

const props = defineProps<{
  users: User[];
  currentUser: User | null;
  // False until the roster has been fetched; the "+" stays hidden until then
  // so it never renders alone above an empty rail.
  usersLoaded?: boolean;
  settingsActive?: boolean;
  conversationPanelOpen?: boolean;
  mobile?: boolean;
  // Whether to show the "?" help button above the "+". The parent gates
  // this on whether an admin has configured a help document — when no
  // doc is set, the button must not render at all.
  helpAvailable?: boolean;
  runningAgentIds?: string[];
  runningConversations?: RunningConversationActivity[];
  queuedConversations?: RunningConversationActivity[];
  agentAttention?: Record<string, ConversationAttention>;
}>();

const { t } = useI18n();

const emit = defineEmits<{
  "select-user": [id: string];
  "context-menu": [event: MouseEvent, user: User];
  "reorder-agents": [ids: string[]];
  "open-settings": [];
  "add-user": [];
  "toggle-conversations": [];
  "open-help": [];
  "open-marketplace": [];
}>();

function handleContextMenu(e: MouseEvent, user: User) {
  e.preventDefault();
  emit("context-menu", e, user);
}

// Long-press-then-drag reordering for the desktop rail. Every row — the human
// owner and the agents alike — participates, so the owner can be placed anywhere
// in the list. The order is persisted server-side via the same /api/user-order
// endpoint, which now stamps the owner's own row too.
const reorder = useAgentReorder({
  ids: () => props.users.map((u) => u.id),
  onReorder: (ids) => emit("reorder-agents", ids),
});

// displayUsers is the rail's render order: all rows (human owner + agents) in the
// reorder composable's current order — the live drag order mid-drag, otherwise
// the prop order. Any user the composable hasn't placed yet keeps its prop
// position at the end.
const displayUsers = computed(() => {
  const byId = new Map(props.users.map((u) => [u.id, u] as const));
  const ordered: User[] = [];
  for (const id of reorder.displayIds()) {
    const u = byId.get(id);
    if (u) {
      ordered.push(u);
      byId.delete(id);
    }
  }
  for (const u of props.users) {
    if (byId.has(u.id)) ordered.push(u);
  }
  return ordered;
});

// Selection for desktop agent rows. Ignores the click that fires right after a
// drag (the composable suppresses it once).
function onAgentClick(id: string) {
  if (reorder.consumeSuppressedClick()) return;
  emit("select-user", id);
}

// Attached bots are transports, not a different kind of chat subject. The
// tooltip therefore names only the connected platforms; every row is already
// known to be an Agent.
function botPlatformsLabel(user: User): string {
  const labels = (user.bot_platforms ?? []).map((platform) => {
    if (platform === "slack") return t("settings.agentForm.platformSlack");
    if (platform === "feishu") return t("settings.agentForm.platformFeishu");
    if (platform === "telegram") return t("settings.agentForm.platformTelegram");
    if (platform === "wechat") return t("settings.agentForm.platformWeChat");
    return platform;
  });
  return [...new Set(labels.filter(Boolean))].join(" · ");
}

const runningConversationCount = computed(() => props.runningConversations?.length ?? 0);
const queuedConversationCount = computed(() => props.queuedConversations?.length ?? 0);
const usersById = computed(() => new Map(props.users.map((user) => [user.id, user])));
const runningTooltipOpenedAt = ref(Date.now());
const runningTooltipOpen = ref(false);

function handleRunningTooltipOpen(open: boolean) {
  if (open) runningTooltipOpenedAt.value = Date.now();
}
</script>

<template>
  <TooltipProvider :delay-duration="500">
    <!-- Mobile: horizontal bottom bar. Same reason as the desktop rail for
         the inset rule instead of `border-t` — the selected tab has to paint
         over it, upwards, into the content sitting above the bar. -->
    <div
      v-if="mobile"
      class="agent-bar-shell agent-rail-scroll flex items-center h-12 w-full px-1 shrink-0 overflow-x-auto"
    >
      <!-- Conversation panel toggle — expand/fold; icon swaps with
           state so the direction of the next click is visually obvious. -->
      <Button
        variant="ghost"
        size="icon"
        class="conversations-toggle mr-0.5 size-10 shrink-0 text-foreground/70 hover:bg-[var(--agent-rail-hover)]"
        :class="
          conversationPanelOpen ? 'bg-[var(--agent-rail-hover)] text-sidebar-accent-foreground' : ''
        "
        @click="emit('toggle-conversations')"
      >
        <PanelLeftClose v-if="conversationPanelOpen" class="size-5" />
        <PanelLeftOpen v-else class="size-5" />
      </Button>

      <!-- Avatars. Fixed 44px cells mirror the desktop rail and keep every
           avatar centre aligned regardless of activity state. -->
      <template v-for="user in users" :key="user.id">
        <div
          class="agent-bar-item relative flex h-full w-11 shrink-0 cursor-pointer items-center justify-center rounded-md"
          data-cursor-surface="pointer"
          @click="emit('select-user', user.id)"
          @contextmenu="handleContextMenu($event, user)"
        >
          <template v-if="currentUser?.id === user.id">
            <span data-testid="agent-rail-tab" class="agent-bar-tab" aria-hidden="true" />
            <span class="agent-bar-notch agent-bar-notch--left" aria-hidden="true" />
            <span class="agent-bar-notch agent-bar-notch--right" aria-hidden="true" />
          </template>
          <div class="relative z-10">
            <AgentAvatar
              :name="user.name"
              :avatar="user.avatar"
              :bot-platforms="user.bot_platforms"
              :running="runningAgentIds?.includes(user.id)"
              :selected="currentUser?.id === user.id"
              :attention="agentAttention?.[user.id]"
              class="size-7"
              fallback-class="text-[13px] font-bold"
            />
          </div>
        </div>
      </template>

      <!-- Help (admin-configured) -->
      <Button
        v-if="helpAvailable"
        variant="ghost"
        size="icon"
        class="ml-0.5 size-8 shrink-0 rounded-full text-muted-foreground hover:bg-[var(--agent-rail-hover)] hover:text-sidebar-accent-foreground"
        :aria-label="t('sidebar.help')"
        @click="emit('open-help')"
      >
        <HelpCircle class="size-5" />
      </Button>

      <!-- Add user -->
      <Button
        variant="outline"
        size="icon"
        class="ml-0.5 size-8 shrink-0 rounded-full border-dashed bg-transparent text-lg text-muted-foreground hover:border-sidebar-primary hover:bg-[var(--agent-rail-hover)]"
        @click="emit('add-user')"
      >
        +
      </Button>

      <div class="flex-1" />

      <!-- Application marketplace -->
      <Button
        variant="ghost"
        size="icon"
        class="size-10 shrink-0 text-muted-foreground hover:bg-[var(--agent-rail-hover)] hover:text-sidebar-accent-foreground"
        :aria-label="t('sidebar.marketplace')"
        data-testid="marketplace-trigger-mobile"
        @click="emit('open-marketplace')"
      >
        <LayoutGrid class="size-5" />
      </Button>

      <!-- Settings -->
      <Button
        variant="ghost"
        size="icon"
        class="relative size-10 shrink-0 text-muted-foreground hover:bg-[var(--agent-rail-hover)]"
        :class="settingsActive ? 'bg-[var(--agent-rail-hover)] text-sidebar-accent-foreground' : ''"
        :aria-label="t('sidebar.settings')"
        @click="emit('open-settings')"
      >
        <Settings class="size-5" />
      </Button>
    </div>

    <!-- Desktop: vertical agent rail. Avatars sit in the middle; settings
         cog pinned to the bottom. (Brand mark removed by user request.) -->
    <!-- The right-hand rule is an inset shadow rather than `border-r` so the
         selected agent's connected tab (a descendant) can paint over it and
         merge the rail into the content area. A real border would also eat
         1px of content width and knock the avatars off the pixel grid.
         `--joined` tells the tab which surface sits to its right: the
         conversation panel when that panel is docked, the chat otherwise. -->
    <div
      v-else
      class="agent-rail-shell flex h-full w-12 min-w-12 flex-col overflow-hidden"
      :class="conversationPanelOpen ? 'agent-rail-shell--joined' : ''"
    >
      <!-- Avatar list -->
      <div
        class="agent-rail-scroll flex flex-1 flex-col items-center gap-4 overflow-y-auto pb-4"
        :class="conversationPanelOpen ? 'pt-[164px]' : 'pt-2'"
        data-agent-rail
      >
        <!-- Conversation panel toggle — expand/fold; icon swaps with
             state so the direction of the next click is visually obvious. -->
        <Tooltip v-if="!conversationPanelOpen">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="conversations-toggle mb-2 size-9 rounded-xl text-white hover:bg-[#252525] hover:text-white"
              @click="emit('toggle-conversations')"
            >
              <span class="grid size-7 place-items-center rounded-full bg-[#191919]">
                <Coffee class="size-4" />
              </span>
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{{
            conversationPanelOpen ? t("sidebar.hideConversations") : t("sidebar.showConversations")
          }}</TooltipContent>
        </Tooltip>

        <!-- Fixed 44px cells keep every avatar centred, so the rail's rhythm
             remains independent of selection and activity state. -->
        <TransitionGroup
          tag="div"
          name="agent-rail"
          class="flex w-full flex-col items-center gap-4"
        >
          <div
            v-for="user in displayUsers"
            :key="user.id"
            class="agent-rail-item relative flex size-6 cursor-pointer select-none touch-none items-center justify-center rounded-md"
            data-cursor-surface="pointer"
            :data-agent-id="user.id"
            @click="onAgentClick(user.id)"
            @contextmenu="handleContextMenu($event, user)"
            @dragstart.prevent
            @pointerdown="reorder.onPointerDown($event, user.id)"
          >
            <!-- The held cell wears the selected agent's connected tab rather
                 than an outline: an outline draws a hard rectangle over a rail
                 whose only other affordance is this recessed tab, so the two
                 states read as unrelated widgets. Reusing the tab keeps one
                 vocabulary — the cell you are holding sinks into the surface
                 exactly like the one you have selected. -->
            <Tooltip>
              <TooltipTrigger as-child>
                <div class="relative z-10">
                  <AgentAvatar
                    :name="user.name"
                    :avatar="user.avatar"
                    :bot-platforms="user.bot_platforms"
                    :running="runningAgentIds?.includes(user.id)"
                    :selected="currentUser?.id === user.id"
                    :attention="agentAttention?.[user.id]"
                    class="agent-rail-avatar size-6 rounded-[6px]"
                    :class="
                      currentUser?.id === user.id || reorder.isDragging(user.id)
                        ? 'agent-rail-avatar--active'
                        : ''
                    "
                    fallback-class="text-[11px] font-semibold"
                  />
                </div>
              </TooltipTrigger>
              <TooltipContent side="right" class="flex items-center gap-1.5">
                <span class="inline-flex h-5 items-center leading-none">{{
                  agentDisplayName(user, usersById)
                }}</span>
                <Badge
                  v-if="botPlatformsLabel(user)"
                  variant="mono"
                  class="h-5 py-0 text-[10px] leading-none"
                >
                  {{ botPlatformsLabel(user) }}
                </Badge>
              </TooltipContent>
            </Tooltip>
          </div>
        </TransitionGroup>

        <!-- Add user -->
        <Tooltip v-if="usersLoaded">
          <TooltipTrigger as-child>
            <Button
              variant="outline"
              size="icon"
              class="size-6 rounded-[7px] border border-[var(--agent-rail-add-edge)] bg-[var(--agent-rail-add-surface)] p-0 text-[#202124] shadow-none hover:bg-[var(--agent-rail-hover)] dark:text-[#e6e6e6]"
              :aria-label="t('sidebar.addUser')"
              data-testid="agent-rail-add"
              @click="emit('add-user')"
            >
              <Plus class="size-[15px]" :stroke-width="1.75" aria-hidden="true" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{{ t("sidebar.addUser") }}</TooltipContent>
        </Tooltip>
      </div>

      <!-- Footer. While the conversation panel is docked, marketplace, help and
           settings move into its account menu, but the running-conversations
           snapshot has no other home, so it stays on the rail in both states. -->
      <div class="flex flex-shrink-0 flex-col items-center gap-0 py-2.5">
        <!-- Application marketplace -->
        <Tooltip v-if="!conversationPanelOpen">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="size-9 text-muted-foreground hover:bg-[var(--agent-rail-hover)] hover:text-sidebar-accent-foreground"
              :aria-label="t('sidebar.marketplace')"
              data-testid="marketplace-trigger"
              @click="emit('open-marketplace')"
            >
              <LayoutGrid class="size-5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{{ t("sidebar.marketplace") }}</TooltipContent>
        </Tooltip>

        <!-- Running conversations — a read-only snapshot, opened by hover on a
             pointer device. Hover alone is not enough: reka-ui drops the
             tooltip's pointermove path entirely for `pointerType === "touch"`,
             so on a tablet (touch input, but wide enough for this rail) the
             panel was unreachable by any gesture. A real button that opens on
             click gives touch a way in; `disableClosingTrigger` stops reka's
             own click handler from closing what the tap just opened. Click
             opens but never closes, so mouse behaviour is unchanged: hover
             opens, leaving the icon closes, clicking mid-hover does nothing.
             While the conversation panel is docked its account footer sits
             over the bottom of the rail, so the snapshot moves into the
             account menu instead, like help and settings. -->
        <Tooltip
          v-if="!conversationPanelOpen"
          v-model:open="runningTooltipOpen"
          :delay-duration="0"
          :disable-closing-trigger="true"
          :disable-hoverable-content="true"
          @update:open="handleRunningTooltipOpen"
        >
          <TooltipTrigger as-child>
            <button
              type="button"
              class="flex size-9 cursor-default items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-[var(--agent-rail-hover)] hover:text-sidebar-accent-foreground"
              :aria-label="
                t('sidebar.activitySummary', {
                  running: runningConversationCount,
                  queued: queuedConversationCount,
                })
              "
              :aria-expanded="runningTooltipOpen"
              data-testid="running-conversations-trigger"
              @click="runningTooltipOpen = true"
            >
              <Activity class="size-5" />
            </button>
          </TooltipTrigger>
          <TooltipContent
            side="right"
            :side-offset="8"
            class="w-[440px] max-w-[calc(100vw-5rem)] p-0 text-left whitespace-normal"
            data-testid="running-conversations-popup"
          >
            <RunningActivityTable
              :users="users"
              :running-conversations="runningConversations ?? []"
              :queued-conversations="queuedConversations ?? []"
              :opened-at="runningTooltipOpenedAt"
            />
          </TooltipContent>
        </Tooltip>

        <!-- Help (admin-configured). Sits directly above the settings
             cog so the two "global utility" affordances anchor the
             bottom of the rail together; matches the settings button's
             size so they read as a pair. Only renders when an admin has
             posted a markdown doc — absent state collapses to just the
             settings cog. -->
        <Tooltip v-if="helpAvailable && !conversationPanelOpen">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="size-9 text-muted-foreground hover:bg-[var(--agent-rail-hover)] hover:text-sidebar-accent-foreground"
              :aria-label="t('sidebar.help')"
              @click="emit('open-help')"
            >
              <HelpCircle class="size-5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{{ t("sidebar.help") }}</TooltipContent>
        </Tooltip>

        <!-- Settings button -->
        <Tooltip v-if="!conversationPanelOpen">
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              class="relative size-9 text-muted-foreground hover:bg-[var(--agent-rail-hover)] hover:text-sidebar-accent-foreground"
              :class="
                settingsActive ? 'bg-[var(--agent-rail-hover)] text-sidebar-accent-foreground' : ''
              "
              @click="emit('open-settings')"
            >
              <Settings class="size-5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="right">{{ t("sidebar.settings") }}</TooltipContent>
        </Tooltip>
      </div>
    </div>
  </TooltipProvider>
</template>

<style scoped>
.agent-rail-item {
  will-change: transform;
}

.agent-rail-move {
  transition: transform 180ms cubic-bezier(0.2, 0, 0, 1);
}

/* The desktop rail is the left gutter of the sidebar, not a surface of its
   own: while the conversation panel is docked the two share one colour and one
   right-hand edge, so the agent column reads as an indent inside the sidebar
   rather than a second chrome strip. Undocked it still has to separate itself
   from the white chat canvas, hence the edge rule below. The mobile bar keeps
   its own darker surface — there it really is a separate bar.
   `--agent-rail-hover` replaces --sidebar-accent for controls sitting on the
   rail, which would otherwise hover to a shade barely off the surface. */
.agent-rail-shell,
.agent-bar-shell {
  --agent-rail-surface: var(--sidebar);
  --agent-rail-hover: oklch(0.91 0 0);
  --agent-rail-add-surface: #f5f5f5;
  --agent-rail-add-edge: #eeeeee;
  background-color: var(--agent-rail-surface);
}

.agent-bar-shell {
  --agent-rail-surface: oklch(0.93 0 0);
  --agent-rail-hover: oklch(0.875 0 0);
}

.dark .agent-rail-shell,
.dark .agent-bar-shell {
  --agent-rail-hover: oklch(0.22 0 0);
  --agent-rail-add-surface: oklch(0.18 0 0);
  --agent-rail-add-edge: oklch(0.28 0 0);
}

.dark .agent-bar-shell {
  --agent-rail-surface: oklch(0.06 0 0);
}

/* Edge rule only while the rail stands alone against the chat canvas. Docked,
   the conversation panel to its right draws the single sidebar edge. */
.agent-rail-shell {
  box-shadow: inset -1px 0 0 var(--sidebar-border);
}

.agent-rail-shell--joined {
  box-shadow: none;
}

/* Selected agent: a hairline drawn inside the tile's own edge. An outset ring
   made the selected tile read 4px larger than its neighbours, breaking the
   column's even rhythm. */
.agent-rail-item :deep(.agent-rail-avatar--active) {
  outline: 2px solid var(--sidebar-primary);
  outline-offset: -2px;
}

.agent-rail-item :deep(.agent-rail-avatar) {
  transition: transform 150ms ease;
}

.agent-rail-item:hover :deep(.agent-rail-avatar) {
  transform: translateY(-1px);
}

@media (prefers-reduced-motion: reduce) {
  .agent-rail-item :deep(.agent-rail-avatar) {
    transition: none;
  }
}

/* The tab bleeds onto the edge rule; a scrollbar there would slice through the
   join. The rail holds a handful of avatars, so wheel/touch scrolling without
   a visible track is no loss. */
.agent-rail-scroll {
  scrollbar-width: none;
}

.agent-rail-scroll::-webkit-scrollbar {
  display: none;
}

/* Mobile bottom bar: the same connected tab rotated a quarter turn, since the
   content it joins sits above the bar rather than beside it — and that content
   is always the chat, so the tab surface is fixed. */
.agent-bar-shell {
  --agent-rail-tab-surface: var(--background);
  box-shadow: inset 0 1px 0 var(--sidebar-border);
}

.agent-bar-tab,
.agent-bar-notch {
  --agent-rail-seam: var(--sidebar-border);
  position: absolute;
  background-color: var(--agent-rail-tab-surface);
  pointer-events: none;
}

.dark .agent-bar-tab,
.dark .agent-bar-notch {
  --agent-rail-seam: oklch(1 0 0 / 16%);
}

/* Full-height for the same reason the desktop tab is full-width. */
.agent-bar-tab {
  z-index: 0;
  inset: 0 2px;
  border-radius: 0 0 10px 10px;
  box-shadow:
    -1px 0 0 var(--agent-rail-seam),
    1px 0 0 var(--agent-rail-seam),
    0 1px 0 var(--agent-rail-seam);
}

.agent-bar-notch {
  z-index: 1;
  top: 0;
  width: 10px;
  height: 10px;
  overflow: hidden;
}

.agent-bar-notch::after {
  content: "";
  position: absolute;
  inset: 0;
  background-color: var(--agent-rail-surface);
}

.agent-bar-notch--left {
  left: -8px;
}

.agent-bar-notch--left::after {
  border-top-right-radius: 10px;
  box-shadow: inset -1px 1px 0 var(--agent-rail-seam);
}

.agent-bar-notch--right {
  right: -8px;
}

.agent-bar-notch--right::after {
  border-top-left-radius: 10px;
  box-shadow: inset 1px 1px 0 var(--agent-rail-seam);
}
</style>
