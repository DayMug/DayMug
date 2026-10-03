<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { User } from "@/composables/useApi";
import type { RunningConversationActivity } from "@/stores/agentActivityStore";
import { agentDisplayName } from "@/lib/agentDisplayName";

// Read-only snapshot of every running and queued conversation. Rendered on a
// dark inverted surface by both the agent rail tooltip and the account menu,
// so the palette below assumes white-on-dark.
const props = defineProps<{
  users: User[];
  runningConversations: RunningConversationActivity[];
  queuedConversations: RunningConversationActivity[];
  // Relative start times freeze at the moment the snapshot was opened rather
  // than ticking while it is up.
  openedAt: number;
}>();

const { t } = useI18n();

const runningConversationCount = computed(() => props.runningConversations.length);
const queuedConversationCount = computed(() => props.queuedConversations.length);
const activityConversationCount = computed(
  () => runningConversationCount.value + queuedConversationCount.value,
);
const usersById = computed(() => new Map(props.users.map((user) => [user.id, user])));

function runningAgentName(activity: RunningConversationActivity): string {
  const ownerLabel = activity.owner_name?.trim() || activity.owner_username?.trim();
  const activityAgentName = activity.agent_name?.trim();
  if (ownerLabel && activityAgentName) {
    return ownerLabel === activityAgentName
      ? activityAgentName
      : `${ownerLabel}/${activityAgentName}`;
  }
  const agent = usersById.value.get(activity.agent_id);
  if (agent) return agentDisplayName(agent, usersById.value);
  return activity.agent_name || activity.agent_id;
}

function runningStartedAt(activity: RunningConversationActivity): string {
  const startedAt = new Date(activity.started_at).getTime();
  if (Number.isNaN(startedAt)) return "";
  const seconds = Math.max(0, Math.floor((props.openedAt - startedAt) / 1000));
  if (seconds < 60) return t("sidebar.runningSecondsAgo", { count: seconds });
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return t("sidebar.runningMinutesAgo", { count: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("sidebar.runningHoursAgo", { count: hours });
  return t("sidebar.runningDaysAgo", { count: Math.floor(hours / 24) });
}
</script>

<template>
  <div>
    <div class="border-b border-white/15 px-3 py-2 text-[12px] font-semibold">
      {{
        t("sidebar.activitySummary", {
          running: runningConversationCount,
          queued: queuedConversationCount,
        })
      }}
    </div>
    <div v-if="activityConversationCount > 0" class="px-3 py-2">
      <div
        v-if="runningConversationCount > 0"
        data-testid="running-conversations-section-label"
        class="mb-1.5 flex items-center gap-1.5 border-l-2 border-emerald-400/75 pl-2 text-[10px] font-semibold uppercase tracking-[0.08em] text-emerald-200"
      >
        <span>{{ t("sidebar.runningSection") }}</span>
        <span class="text-white/45 tabular-nums">{{ runningConversationCount }}</span>
      </div>
      <table
        v-if="runningConversationCount > 0"
        class="w-full table-fixed border-collapse text-left"
        data-testid="running-conversations-table"
      >
        <colgroup>
          <col class="w-[27%]" />
          <col class="w-[36%]" />
          <col class="w-[25%]" />
          <col class="w-[12%]" />
        </colgroup>
        <thead
          class="border-b border-white/10 text-[10px] font-medium uppercase tracking-[0.06em] text-white/55"
        >
          <tr aria-hidden="true">
            <th class="pb-1.5 pr-2.5 font-medium">
              {{ t("sidebar.runningAccount") }}
            </th>
            <th class="pb-1.5 pr-2.5 font-medium">
              {{ t("sidebar.runningModel") }}
            </th>
            <th class="pb-1.5 pr-2.5 font-medium" data-testid="running-agent-header">
              {{ t("sidebar.runningAgent") }}
            </th>
            <th class="pb-1.5 text-right font-medium" data-testid="running-time-header">
              {{ t("sidebar.runningStarted") }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="activity in runningConversations"
            :key="activity.conversation_id"
            data-testid="running-conversation-row"
            class="border-b border-white/10 last:border-b-0"
          >
            <td
              class="truncate py-1.5 pr-2.5 text-[11px] font-medium"
              :title="activity.account_name"
            >
              {{ activity.account_name || t("common.unknown") }}
            </td>
            <td class="truncate py-1.5 pr-2.5 font-mono text-[11px]" :title="activity.model">
              {{ activity.model || t("common.unknown") }}
            </td>
            <td
              class="truncate py-1.5 pr-2.5 text-[11px] font-medium"
              :title="runningAgentName(activity)"
              data-testid="running-agent-cell"
            >
              {{ runningAgentName(activity) }}
            </td>
            <td class="py-1.5 text-right" data-testid="running-time-cell">
              <time
                class="whitespace-nowrap text-[11px] text-white/70 tabular-nums"
                :datetime="activity.started_at"
              >
                {{ runningStartedAt(activity) }}
              </time>
            </td>
          </tr>
        </tbody>
      </table>

      <div
        v-if="queuedConversationCount > 0"
        data-testid="queued-conversations-section-label"
        class="mb-1.5 mt-3 flex items-center gap-1.5 border-l-2 border-amber-300/80 pl-2 text-[10px] font-semibold uppercase tracking-[0.08em] text-amber-200"
      >
        <span>{{ t("sidebar.queuedSection") }}</span>
        <span class="text-white/45 tabular-nums">{{ queuedConversationCount }}</span>
      </div>
      <table
        v-if="queuedConversationCount > 0"
        class="w-full table-fixed border-collapse text-left"
        data-testid="queued-conversations-table"
      >
        <colgroup>
          <col class="w-[27%]" />
          <col class="w-[36%]" />
          <col class="w-[25%]" />
          <col class="w-[12%]" />
        </colgroup>
        <thead
          class="border-b border-white/10 text-[10px] font-medium uppercase tracking-[0.06em] text-white/55"
        >
          <tr aria-hidden="true">
            <th class="pb-1.5 pr-2.5 font-medium">
              {{ t("sidebar.runningAccount") }}
            </th>
            <th class="pb-1.5 pr-2.5 font-medium">
              {{ t("sidebar.runningModel") }}
            </th>
            <th class="pb-1.5 pr-2.5 font-medium" data-testid="queued-agent-header">
              {{ t("sidebar.runningAgent") }}
            </th>
            <th class="pb-1.5 text-right font-medium" data-testid="queued-time-header">
              {{ t("sidebar.queuedSince") }}
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="activity in queuedConversations"
            :key="activity.conversation_id"
            data-testid="queued-conversation-row"
            class="border-b border-white/10 last:border-b-0"
          >
            <td
              class="truncate py-1.5 pr-2.5 text-[11px] font-medium"
              :title="activity.account_name"
            >
              {{ activity.account_name || t("common.unknown") }}
            </td>
            <td class="truncate py-1.5 pr-2.5 font-mono text-[11px]" :title="activity.model">
              {{ activity.model || t("common.unknown") }}
            </td>
            <td
              class="truncate py-1.5 pr-2.5 text-[11px] font-medium"
              :title="runningAgentName(activity)"
              data-testid="queued-agent-cell"
            >
              {{ runningAgentName(activity) }}
            </td>
            <td class="py-1.5 text-right" data-testid="queued-time-cell">
              <time
                class="whitespace-nowrap text-[11px] text-amber-100/75 tabular-nums"
                :datetime="activity.started_at"
              >
                {{ runningStartedAt(activity) }}
              </time>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
