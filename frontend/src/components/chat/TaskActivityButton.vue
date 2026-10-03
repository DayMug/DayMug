<script setup lang="ts">
// Mobile "task activity" entry: a live dot in the global bar that opens a
// cross-agent feed of the signed-in user's running, queued and waiting turns
// plus finished work they have not looked at yet. Desktop surfaces the same
// signals through the sidebar's status dots; a phone only ever shows one
// conversation, so without this feed a job in another agent is invisible.
import { computed, onUnmounted, ref, watch, type Component } from "vue";
import { useI18n } from "vue-i18n";
import { AlertCircle, Check, Clock3, Hourglass, RefreshCw, X } from "lucide-vue-next";
import {
  queuedConversations,
  runningConversations,
  waitingConversations,
  type RunningConversationActivity,
} from "@/stores/agentActivityStore";
import { attentionRows, liveConversationTitles } from "@/stores/conversationAttentionStore";
import { conversations, conversationsByAgent } from "@/stores/conversationListStore";
import { openConversation } from "@/stores/activeConversationStore";
import { users } from "@/stores/userStore";

type ActivityKind = "running" | "queued" | "waiting" | "done" | "error";

interface ActivityRow {
  kind: ActivityKind;
  conversationId: string;
  agentId: string;
  title: string;
  agentName: string;
  startedAt?: string;
  background?: boolean;
}

const { t } = useI18n();
const open = ref(false);
const now = ref(Date.now());

// The server snapshot is a global operations view; the feed only lists work
// in agents this user can open, otherwise a tap would land on a 404.
const visibleAgents = computed(() => new Map(users.value.map((user) => [user.id, user])));

function conversationTitle(conversationId: string, agentId: string, fallback?: string) {
  if (fallback) return fallback;
  if (liveConversationTitles.value[conversationId])
    return liveConversationTitles.value[conversationId];
  const cached =
    conversations.value.find((row) => row.id === conversationId) ??
    conversationsByAgent.value[agentId]?.find((row) => row.id === conversationId);
  return cached?.title || t("conversations.new");
}

function liveRows(kind: ActivityKind, source: RunningConversationActivity[]): ActivityRow[] {
  return source
    .filter((activity) => visibleAgents.value.has(activity.agent_id))
    .map((activity) => ({
      kind,
      conversationId: activity.conversation_id,
      agentId: activity.agent_id,
      title: conversationTitle(activity.conversation_id, activity.agent_id),
      agentName: activity.agent_name || visibleAgents.value.get(activity.agent_id)?.name || "",
      startedAt: activity.started_at,
      background: activity.background,
    }));
}

// Waiting on the user is listed first: it is the only state that stalls until
// they act. Unread results trail the live work they came from.
const rows = computed<ActivityRow[]>(() => {
  const seen = new Set<string>();
  const out: ActivityRow[] = [];
  const push = (row: ActivityRow) => {
    if (seen.has(row.conversationId)) return;
    seen.add(row.conversationId);
    out.push(row);
  };
  liveRows("waiting", waitingConversations.value).forEach(push);
  liveRows("running", runningConversations.value).forEach(push);
  liveRows("queued", queuedConversations.value).forEach(push);
  // Live rows win the dedupe above; a flagged row adds what the live
  // snapshot cannot know, such as a question left unanswered across a restart.
  const flaggedOrder = { waiting: 0, error: 1, done: 2 } as const;
  const flagged = [...attentionRows.value].sort(
    (a, b) => flaggedOrder[a.state] - flaggedOrder[b.state],
  );
  for (const row of flagged) {
    if (!visibleAgents.value.has(row.agent_id)) continue;
    push({
      kind: row.state,
      conversationId: row.conversation_id,
      agentId: row.agent_id,
      title: conversationTitle(row.conversation_id, row.agent_id, row.title),
      agentName: visibleAgents.value.get(row.agent_id)?.name || "",
    });
  }
  return out;
});

const hasRunning = computed(() => rows.value.some((row) => row.kind === "running"));
const needsAttention = computed(() =>
  rows.value.some((row) => row.kind === "waiting" || row.kind === "error"),
);

const kindMeta: Record<ActivityKind, { icon: Component; orb: string; label: string }> = {
  running: {
    icon: RefreshCw,
    orb: "bg-[#ececee] text-[#242424] dark:bg-white/10 dark:text-white",
    label: "chat.taskActivityRunning",
  },
  queued: {
    icon: Hourglass,
    orb: "bg-[#ececee] text-[#626a7d] dark:bg-white/10 dark:text-muted-foreground",
    label: "chat.taskActivityQueued",
  },
  waiting: {
    icon: Clock3,
    orb: "bg-[#fff5df] text-[#a66b18] dark:bg-amber-500/15 dark:text-amber-300",
    label: "chat.taskActivityWaiting",
  },
  done: {
    icon: Check,
    orb: "bg-[#e8f7f3] text-[#19785f] dark:bg-emerald-500/15 dark:text-emerald-300",
    label: "chat.taskActivityDone",
  },
  error: {
    icon: AlertCircle,
    orb: "bg-[#fdecec] text-[#c2413b] dark:bg-red-500/15 dark:text-red-300",
    label: "chat.taskActivityFailed",
  },
};

function elapsed(startedAt?: string) {
  const started = startedAt ? Date.parse(startedAt) : NaN;
  if (!Number.isFinite(started)) return "";
  const minutes = Math.max(0, Math.floor((now.value - started) / 60000));
  if (minutes < 1) return "<1m";
  if (minutes < 60) return `${minutes}m`;
  return `${Math.floor(minutes / 60)}h`;
}

function trailing(row: ActivityRow) {
  if (row.kind === "done") return t("chat.taskActivityNewTag");
  if (row.kind === "error") return t("chat.taskActivityFailedTag");
  if (row.kind === "queued") return t("chat.taskActivityQueuedTag");
  return elapsed(row.startedAt);
}

// Elapsed labels only need to move while someone is looking at them.
let ticker: number | null = null;
watch(open, (isOpen) => {
  if (ticker) window.clearInterval(ticker);
  ticker = null;
  if (!isOpen) return;
  now.value = Date.now();
  ticker = window.setInterval(() => (now.value = Date.now()), 30_000);
});
onUnmounted(() => {
  if (ticker) window.clearInterval(ticker);
});

function select(row: ActivityRow) {
  open.value = false;
  openConversation(row.agentId, row.conversationId);
}

defineExpose({ close: () => (open.value = false) });
</script>

<template>
  <button
    type="button"
    data-testid="mobile-activity-button"
    class="grid h-[54px] w-10 shrink-0 place-items-center"
    :aria-label="t('chat.taskActivityOpen')"
    aria-haspopup="dialog"
    :aria-expanded="open"
    @click.stop="open = !open"
  >
    <span
      class="task-activity-pulse size-2 rounded-full border-2 border-card shadow-[0_0_0_3px_#e9e8ff] dark:shadow-[0_0_0_3px_rgba(255,255,255,0.12)]"
      :class="[
        needsAttention
          ? 'bg-[#c47a12]'
          : hasRunning
            ? 'bg-[#242424] dark:bg-white'
            : 'bg-[#b9bdc8]',
        { 'task-activity-pulse--live': hasRunning },
      ]"
      data-testid="mobile-activity-dot"
    />
  </button>
  <button
    v-if="open"
    type="button"
    class="fixed inset-0 z-40 cursor-default bg-transparent"
    :aria-label="t('common.close')"
    @click="open = false"
  />
  <Transition
    enter-active-class="transition duration-200 ease-out"
    enter-from-class="-translate-y-1.5 opacity-0"
    leave-active-class="transition duration-200 ease-in"
    leave-to-class="-translate-y-1.5 opacity-0"
  >
    <section
      v-if="open"
      role="dialog"
      :aria-label="t('chat.taskActivity')"
      data-testid="mobile-activity-popover"
      class="fixed left-2 right-2 top-12 z-50 rounded-[15px] border border-[#e5e5e6] bg-white p-2.5 text-[#202124] shadow-[0_18px_52px_rgba(19,19,22,0.14),0_2px_8px_rgba(19,19,22,0.06)] dark:border-border dark:bg-popover dark:text-popover-foreground"
    >
      <div class="flex items-start justify-between px-1.5 pb-[11px] pt-[7px]">
        <div class="flex min-w-0 flex-col gap-[3px]">
          <strong class="text-[14px] font-bold leading-[21px]">{{ t("chat.taskActivity") }}</strong>
          <span class="text-[11px] leading-[16.5px] text-[#777980] dark:text-muted-foreground">
            {{ t("chat.taskActivitySubtitle") }}
          </span>
        </div>
        <button
          type="button"
          class="grid size-[30px] place-items-center rounded-lg text-[#626a7d] hover:bg-muted dark:text-muted-foreground"
          :aria-label="t('common.close')"
          @click="open = false"
        >
          <X class="size-4" />
        </button>
      </div>
      <div class="max-h-[min(60svh,420px)] overflow-y-auto">
        <p
          v-if="rows.length === 0"
          class="px-2 py-5 text-center text-[12px] text-[#777980] dark:text-muted-foreground"
          data-testid="mobile-activity-empty"
        >
          {{ t("chat.taskActivityEmpty") }}
        </p>
        <button
          v-for="row in rows"
          :key="row.conversationId"
          type="button"
          data-testid="mobile-activity-item"
          :data-kind="row.kind"
          class="grid w-full grid-cols-[30px_minmax(0,1fr)_auto] items-center gap-[9px] rounded-[10px] px-2 py-2.5 text-left hover:bg-[#f6f7f9] active:bg-[#f1f2f5] dark:hover:bg-muted"
          @click="select(row)"
        >
          <span
            class="grid size-7 place-items-center rounded-[9px]"
            :class="kindMeta[row.kind].orb"
          >
            <component
              :is="kindMeta[row.kind].icon"
              class="size-[13px]"
              :class="{ 'animate-spin [animation-duration:2.4s]': row.kind === 'running' }"
            />
          </span>
          <span class="flex min-w-0 flex-col gap-[3px]">
            <strong class="truncate text-[12px] font-bold leading-[18px]">{{ row.title }}</strong>
            <span
              class="truncate text-[11px] leading-[16.5px] text-[#777980] dark:text-muted-foreground"
            >
              {{
                t(row.background ? "chat.taskActivityBackground" : kindMeta[row.kind].label, {
                  agent: row.agentName,
                })
              }}
            </span>
          </span>
          <span
            class="text-[11px] font-[650] leading-[16.5px] text-[#777980] dark:text-muted-foreground"
          >
            {{ trailing(row) }}
          </span>
        </button>
      </div>
      <div
        v-if="$slots.footer"
        class="mt-1 flex items-center gap-1 border-t border-[#eeeeef] px-1 pt-1.5 dark:border-border"
      >
        <slot name="footer" />
      </div>
    </section>
  </Transition>
</template>

<style scoped>
.task-activity-pulse--live {
  animation: task-activity-pulse 2s infinite;
}
@keyframes task-activity-pulse {
  50% {
    box-shadow: 0 0 0 6px rgba(79, 70, 229, 0.08);
  }
}
@media (prefers-reduced-motion: reduce) {
  .task-activity-pulse--live {
    animation: none;
  }
}
</style>
