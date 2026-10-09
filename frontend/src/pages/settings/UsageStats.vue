<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";
import {
  adminFetchUsageInsights,
  fetchUsageInsights,
  type UsageAnomaly,
  type UsageGroupBy,
  type UsageInsightsQuery,
  type UsageInsightsResponse,
  type UsageRankBy,
  type UsageRankingRow,
  type UsageSortBy,
} from "@/composables/useApi";
import { useAuth } from "@/composables/useAuth";
import { usePageTitle } from "@/composables/useDocumentTitle";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import UsageCompositionChart from "@/components/UsageCompositionChart.vue";
import { errorMessage as describeError } from "@/lib/errorMessage";

const { t } = useI18n();
const route = useRoute();
const router = useRouter();
const { authMe } = useAuth();
const isAdmin = computed(() => authMe.value?.is_admin === true);
usePageTitle(
  computed(() => t(isAdmin.value ? "settings.titles.usageAdmin" : "settings.titles.usage")),
);

function dateInShanghai(offsetDays = 0): string {
  const date = new Date(Date.now() + offsetDays * 86_400_000);
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(date);
}

function queryString(key: string, fallback = ""): string {
  const value = route.query[key];
  return typeof value === "string" ? value : fallback;
}

function oneOf<T extends string>(value: string, allowed: readonly T[], fallback: T): T {
  return allowed.includes(value as T) ? (value as T) : fallback;
}

const groupOptions = ["day", "user", "agent", "model", "conversation"] as const;
const rankOptions = ["user", "agent", "model", "conversation", "cron"] as const;
const sortOptions = [
  "cost",
  "total_tokens",
  "cache_ratio",
  "user_instructions",
  "model_requests",
  "tool_calls",
  "last_activity",
] as const;

const startDate = ref(queryString("start", dateInShanghai(-6)));
const endDate = ref(queryString("end", dateInShanghai()));
const userFilter = ref(queryString("user_id"));
const agentFilter = ref(queryString("agent_id"));
const providerFilter = ref(queryString("provider"));
const modelFilter = ref(queryString("model"));
const conversationFilter = ref(queryString("conversation_id"));
const sourceFilter = ref(queryString("source_type"));
const groupBy = ref<UsageGroupBy>(oneOf(queryString("group_by"), groupOptions, "day"));
const rankBy = ref<UsageRankBy>(oneOf(queryString("rank_by"), rankOptions, "conversation"));
const sortBy = ref<UsageSortBy>(oneOf(queryString("sort_by"), sortOptions, "cost"));
const sortOrder = ref<"asc" | "desc">(queryString("sort_order") === "asc" ? "asc" : "desc");
const page = ref(Math.max(1, Number(queryString("page", "1")) || 1));
const pageSize = 20;

const response = ref<UsageInsightsResponse | null>(null);
const loading = ref(false);
const errorMessage = ref("");
const mounted = ref(false);

function currentQuery(): UsageInsightsQuery {
  return {
    start: startDate.value,
    end: endDate.value,
    user_id: isAdmin.value ? userFilter.value || undefined : undefined,
    agent_id: agentFilter.value || undefined,
    provider: providerFilter.value || undefined,
    model: modelFilter.value || undefined,
    conversation_id: conversationFilter.value || undefined,
    source_type: (sourceFilter.value || undefined) as "manual" | "cron" | undefined,
    group_by: groupBy.value,
    rank_by: rankBy.value,
    sort_by: sortBy.value,
    sort_order: sortOrder.value,
    page: page.value,
    page_size: pageSize,
  };
}

function urlQuery(): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(currentQuery())) {
    if (value !== undefined && value !== "") out[key] = String(value);
  }
  return out;
}

let requestVersion = 0;
async function load() {
  if (authMe.value === null) return;
  const version = ++requestVersion;
  loading.value = true;
  errorMessage.value = "";
  try {
    const result = isAdmin.value
      ? await adminFetchUsageInsights(currentQuery())
      : await fetchUsageInsights(currentQuery());
    if (version === requestVersion) response.value = result;
  } catch (error) {
    if (version === requestVersion) {
      response.value = null;
      errorMessage.value = describeError(error);
    }
  } finally {
    if (version === requestVersion) loading.value = false;
  }
}

function untilAuthSettled(): Promise<void> {
  if (authMe.value !== null) return Promise.resolve();
  return new Promise((resolve) => {
    const stop = watch(authMe, (value) => {
      if (value === null) return;
      stop();
      resolve();
    });
  });
}

const filterState = [
  startDate,
  endDate,
  userFilter,
  agentFilter,
  providerFilter,
  modelFilter,
  conversationFilter,
  sourceFilter,
  groupBy,
  rankBy,
  sortBy,
  sortOrder,
  page,
];

watch(filterState, async (_values, previous) => {
  if (!mounted.value) return;
  const pageOnly =
    previous && filterState.slice(0, -1).every((item, index) => item.value === previous[index]);
  if (!pageOnly && page.value !== 1) {
    page.value = 1;
    return;
  }
  await router.replace({ query: urlQuery() });
  await load();
});

onMounted(async () => {
  await untilAuthSettled();
  mounted.value = true;
  await router.replace({ query: urlQuery() });
  await load();
});

const summary = computed(() => response.value?.summary);
const facets = computed(() => response.value?.facets);
const totalPages = computed(() =>
  Math.max(1, Math.ceil((response.value?.total_rows ?? 0) / pageSize)),
);

function formatUsageNumber(value: number): string {
  const absolute = Math.abs(value);
  if (absolute >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`;
  if (absolute >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (absolute >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return Math.round(value).toLocaleString();
}

function formatCost(value: number): string {
  if (value > 0 && value < 0.01) return `$${value.toFixed(4)}`;
  return `$${value.toFixed(2)}`;
}

function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`;
}

function exact(value: number): string {
  return value.toLocaleString("en-US", { maximumFractionDigits: 6 });
}

function comparison(value: number | null | undefined): string {
  if (value === null || value === undefined) return t("settings.usage.newPeriod");
  const arrow = value > 0 ? "↑" : value < 0 ? "↓" : "→";
  return `${arrow} ${Math.abs(value).toFixed(1)}%`;
}

function comparisonClass(value: number | null | undefined): string {
  if (!value) return "text-muted-foreground";
  return value > 0
    ? "text-amber-600 dark:text-amber-400"
    : "text-emerald-600 dark:text-emerald-400";
}

const cards = computed(() => {
  const s = summary.value;
  if (!s) return [];
  return [
    {
      key: "totalTokens",
      value: formatUsageNumber(s.total_tokens),
      exact: exact(s.total_tokens),
      change: s.comparison.total_tokens,
      note: t("settings.usage.totalTokensNote"),
    },
    {
      key: "estimatedCost",
      value: formatCost(s.cost_usd),
      exact: exact(s.cost_usd),
      change: s.comparison.cost_usd,
    },
    {
      key: "userInstructions",
      value: formatUsageNumber(s.user_instructions),
      exact: exact(s.user_instructions),
      change: s.comparison.user_instructions,
    },
    {
      key: "activeConversations",
      value: formatUsageNumber(s.active_conversations),
      exact: exact(s.active_conversations),
      change: s.comparison.active_conversations,
    },
    {
      key: "modelRequests",
      value: formatUsageNumber(s.model_requests),
      exact: exact(s.model_requests),
      change: s.comparison.model_requests,
    },
    {
      key: "toolCalls",
      value: formatUsageNumber(s.tool_calls),
      exact: exact(s.tool_calls),
      change: s.comparison.tool_calls,
    },
    {
      key: "cacheReadRatio",
      value: formatPercent(s.cache_read_ratio),
      exact: formatPercent(s.cache_read_ratio),
      change: s.comparison.cache_read_ratio,
    },
  ];
});

function clearFilters() {
  userFilter.value = "";
  agentFilter.value = "";
  providerFilter.value = "";
  modelFilter.value = "";
  conversationFilter.value = "";
  sourceFilter.value = "";
}

function tokenTriplet(row: UsageRankingRow): string {
  return `${formatUsageNumber(row.input_tokens)} / ${formatUsageNumber(row.cache_read_input_tokens + row.cache_creation_input_tokens)} / ${formatUsageNumber(row.output_tokens)}`;
}

function anomalyText(anomaly: UsageAnomaly): string {
  return t(`settings.usage.anomalies.${anomaly.code}`);
}

function openConversation(row: UsageRankingRow) {
  if (rankBy.value !== "conversation" || !row.conversation_id || !row.agent_id) return;
  void router.push({
    name: "chat",
    params: { userId: row.agent_id, conversationId: row.conversation_id },
  });
}

function rowTitle(row: UsageRankingRow): string {
  if (rankBy.value === "cron") return row.cron_job_name || row.cron_job_id;
  if (rankBy.value === "model") return row.model;
  if (rankBy.value === "agent") return row.agent_name || row.agent_id;
  if (rankBy.value === "user") return row.owner_name || row.owner_id;
  return row.conversation_title || row.conversation_id;
}
</script>

<template>
  <div class="mx-auto w-full max-w-[1500px] p-4 pb-12 md:p-6">
    <SettingsDetailHeader
      :title="t('settings.usage.title')"
      :eyebrow="t('settings.usage.eyebrowObservability')"
      icon="chart-no-axes-combined"
    />

    <div
      class="mb-5 flex flex-col gap-2 border-b border-border pb-5 md:flex-row md:items-end md:justify-between"
    >
      <p class="max-w-3xl text-sm leading-6 text-muted-foreground">
        {{ t("settings.usage.introObservability") }}
      </p>
      <span class="shrink-0 font-mono text-[11px] text-muted-foreground">
        {{ response?.timezone ?? "Asia/Shanghai" }}
      </span>
    </div>

    <section
      class="mb-5 rounded-xl border border-border bg-card p-3 shadow-sm"
      data-testid="usage-filters"
    >
      <div class="grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-8">
        <label class="usage-filter">
          <span>{{ t("settings.usage.start") }}</span>
          <input v-model="startDate" type="date" />
        </label>
        <label class="usage-filter">
          <span>{{ t("settings.usage.end") }}</span>
          <input v-model="endDate" type="date" />
        </label>
        <label v-if="isAdmin" class="usage-filter">
          <span>{{ t("settings.usage.user") }}</span>
          <select v-model="userFilter">
            <option value="">{{ t("settings.usage.allUsers") }}</option>
            <option v-for="item in facets?.users ?? []" :key="item.id" :value="item.id">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label class="usage-filter">
          <span>{{ t("settings.usage.agent") }}</span>
          <select v-model="agentFilter">
            <option value="">{{ t("settings.usage.allAgents") }}</option>
            <option v-for="item in facets?.agents ?? []" :key="item.id" :value="item.id">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label class="usage-filter">
          <span>{{ t("settings.usage.provider") }}</span>
          <select v-model="providerFilter">
            <option value="">{{ t("settings.usage.allProviders") }}</option>
            <option v-for="item in facets?.providers ?? []" :key="item.id" :value="item.id">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label class="usage-filter">
          <span>{{ t("settings.usage.model") }}</span>
          <select v-model="modelFilter">
            <option value="">{{ t("settings.usage.allModels") }}</option>
            <option v-for="item in facets?.models ?? []" :key="item.id" :value="item.id">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label class="usage-filter">
          <span>{{ t("settings.usage.conversation") }}</span>
          <select v-model="conversationFilter">
            <option value="">{{ t("settings.usage.allConversations") }}</option>
            <option v-for="item in facets?.conversations ?? []" :key="item.id" :value="item.id">
              {{ item.label }}
            </option>
          </select>
        </label>
        <label class="usage-filter">
          <span>{{ t("settings.usage.source") }}</span>
          <select v-model="sourceFilter">
            <option value="">{{ t("settings.usage.allSources") }}</option>
            <option value="manual">{{ t("settings.usage.manual") }}</option>
            <option value="cron">{{ t("settings.usage.cron") }}</option>
          </select>
        </label>
      </div>
      <div class="mt-2 flex justify-end">
        <button
          class="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
          @click="clearFilters"
        >
          {{ t("settings.usage.clearFilters") }}
        </button>
      </div>
    </section>

    <div
      v-if="errorMessage"
      class="mb-5 rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive"
    >
      {{ errorMessage }}
    </div>

    <section
      class="mb-6 grid grid-cols-2 gap-2 md:grid-cols-4 xl:grid-cols-7"
      :aria-label="t('settings.usage.summaryAria')"
    >
      <article
        v-for="card in cards"
        :key="card.key"
        class="min-h-32 rounded-xl border border-border bg-card p-4 shadow-sm"
        :class="
          card.key === 'totalTokens'
            ? 'col-span-2 border-l-4 border-l-blue-500 md:col-span-2 xl:col-span-1'
            : ''
        "
      >
        <p class="text-[11px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
          {{ t(`settings.usage.${card.key}`) }}
        </p>
        <p class="mt-3 font-mono text-2xl font-semibold tracking-tight" :title="card.exact">
          {{ card.value }}
        </p>
        <p class="mt-1 text-xs" :class="comparisonClass(card.change)">
          {{ comparison(card.change) }}
        </p>
        <p v-if="card.note" class="mt-2 text-[10px] leading-4 text-muted-foreground">
          {{ card.note }}
        </p>
      </article>
    </section>

    <section class="mb-6 rounded-xl border border-border bg-card p-4 shadow-sm md:p-5">
      <div class="mb-4 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 class="text-base font-semibold">{{ t("settings.usage.tokenComposition") }}</h2>
          <p class="mt-1 text-xs text-muted-foreground">
            {{ t("settings.usage.tokenCompositionHint") }}
          </p>
        </div>
        <label class="flex items-center gap-2 text-xs text-muted-foreground">
          {{ t("settings.usage.groupBy") }}
          <select v-model="groupBy" class="usage-select min-w-36">
            <option v-for="group in groupOptions" :key="group" :value="group">
              {{ t(`settings.usage.groups.${group}`) }}
            </option>
          </select>
        </label>
      </div>
      <div v-if="loading" class="py-28 text-center text-sm text-muted-foreground">
        {{ t("settings.usage.loading") }}
      </div>
      <div
        v-else-if="!response?.composition.length"
        class="py-28 text-center text-sm text-muted-foreground"
      >
        {{ t("settings.usage.emptyGuidance") }}
      </div>
      <UsageCompositionChart v-else :points="response.composition" />
    </section>

    <section class="overflow-hidden rounded-xl border border-border bg-card shadow-sm">
      <div class="border-b border-border p-4 md:p-5">
        <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
          <div>
            <h2 class="text-base font-semibold">{{ t("settings.usage.highConsumption") }}</h2>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ t("settings.usage.highConsumptionHint") }}
            </p>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <label class="flex items-center gap-2 text-xs text-muted-foreground">
              {{ t("settings.usage.sortBy") }}
              <select v-model="sortBy" class="usage-select">
                <option v-for="sort in sortOptions" :key="sort" :value="sort">
                  {{ t(`settings.usage.sorts.${sort}`) }}
                </option>
              </select>
            </label>
            <button
              class="usage-button"
              data-testid="sort-order"
              @click="sortOrder = sortOrder === 'desc' ? 'asc' : 'desc'"
            >
              {{ sortOrder === "desc" ? "↓" : "↑" }} {{ t(`settings.usage.${sortOrder}`) }}
            </button>
          </div>
        </div>
        <div class="mt-4 flex gap-1 overflow-x-auto" role="tablist">
          <button
            v-for="rank in rankOptions"
            :key="rank"
            role="tab"
            :aria-selected="rankBy === rank"
            class="shrink-0 rounded-md px-3 py-1.5 text-xs font-medium transition-colors"
            :class="
              rankBy === rank
                ? 'bg-foreground text-background'
                : 'text-muted-foreground hover:bg-muted hover:text-foreground'
            "
            @click="rankBy = rank"
          >
            {{ t(`settings.usage.ranks.${rank}`) }}
          </button>
        </div>
      </div>

      <div
        v-if="!loading && !response?.rankings.length"
        class="py-20 text-center text-sm text-muted-foreground"
      >
        {{ t("settings.usage.emptyGuidance") }}
      </div>
      <div v-else class="overflow-x-auto">
        <table class="w-full min-w-[1180px] text-xs">
          <thead
            class="bg-muted/40 text-left text-[10px] uppercase tracking-[0.08em] text-muted-foreground"
          >
            <tr>
              <th class="px-4 py-3">{{ t("settings.usage.task") }}</th>
              <th class="px-3 py-3">{{ t("settings.usage.userAgent") }}</th>
              <th class="px-3 py-3 text-right">{{ t("settings.usage.userInstructions") }}</th>
              <th class="px-3 py-3 text-right">{{ t("settings.usage.modelRequests") }}</th>
              <th class="px-3 py-3 text-right">{{ t("settings.usage.toolCalls") }}</th>
              <th class="px-3 py-3 text-right">
                {{ t("settings.usage.inputCacheOutput") }}
              </th>
              <th class="px-3 py-3 text-right">{{ t("settings.usage.cacheRatio") }}</th>
              <th class="px-3 py-3 text-right">{{ t("settings.usage.cost") }}</th>
              <th class="px-4 py-3 text-right">{{ t("settings.usage.lastActivity") }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in response?.rankings ?? []"
              :key="row.key"
              class="border-t border-border align-top hover:bg-muted/20"
            >
              <td class="max-w-[260px] px-4 py-3">
                <button
                  class="max-w-full truncate text-left font-medium"
                  :class="
                    rankBy === 'conversation' && row.conversation_id
                      ? 'hover:text-blue-600 hover:underline dark:hover:text-blue-400'
                      : ''
                  "
                  :disabled="rankBy !== 'conversation' || !row.conversation_id"
                  @click="openConversation(row)"
                >
                  {{ rowTitle(row) || t("settings.usage.unknown") }}
                </button>
                <div class="mt-1 flex flex-wrap gap-1">
                  <span class="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {{ row.source_type || t("settings.usage.historical") }}
                  </span>
                  <span
                    v-if="row.provider"
                    class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                  >
                    {{ row.provider }} · {{ row.model }}
                  </span>
                </div>
                <div
                  v-if="row.anomalies.length"
                  class="mt-2 space-y-1"
                  data-testid="usage-anomalies"
                >
                  <p
                    v-for="anomaly in row.anomalies"
                    :key="anomaly.code"
                    class="rounded border border-amber-500/25 bg-amber-500/10 px-2 py-1 text-[10px] leading-4 text-amber-800 dark:text-amber-200"
                  >
                    {{ anomalyText(anomaly) }}
                  </p>
                </div>
              </td>
              <td class="px-3 py-3">
                <div>{{ row.owner_name || row.owner_id || "—" }}</div>
                <div class="mt-1 text-muted-foreground">
                  {{ row.agent_name || row.agent_id || "—" }}
                </div>
              </td>
              <td class="px-3 py-3 text-right font-mono" :title="exact(row.user_instructions)">
                {{ formatUsageNumber(row.user_instructions) }}
              </td>
              <td
                class="px-3 py-3 text-right font-mono"
                :title="`${exact(row.model_requests)} · ${row.request_count_scope}`"
              >
                {{ formatUsageNumber(row.model_requests) }}
              </td>
              <td class="px-3 py-3 text-right font-mono" :title="exact(row.tool_calls)">
                {{ formatUsageNumber(row.tool_calls) }}
              </td>
              <td
                class="px-3 py-3 text-right font-mono"
                :title="`${exact(row.input_tokens)} / ${exact(row.cache_read_input_tokens + row.cache_creation_input_tokens)} / ${exact(row.output_tokens)}`"
              >
                {{ tokenTriplet(row) }}
              </td>
              <td class="px-3 py-3 text-right font-mono">
                {{ formatPercent(row.cache_read_ratio) }}
              </td>
              <td class="px-3 py-3 text-right font-mono" :title="exact(row.cost_usd)">
                {{ formatCost(row.cost_usd) }}
              </td>
              <td class="px-4 py-3 text-right font-mono text-[11px] text-muted-foreground">
                {{ row.last_activity || "—" }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div
        v-if="response && response.total_rows > 0"
        class="flex items-center justify-between border-t border-border px-4 py-3 text-xs text-muted-foreground"
      >
        <span>
          {{
            t("settings.usage.pageStatus", { page, total: totalPages, rows: response.total_rows })
          }}
        </span>
        <div class="flex gap-2">
          <button class="usage-button" :disabled="page <= 1" @click="page -= 1">
            {{ t("common.previous") }}
          </button>
          <button class="usage-button" :disabled="page >= totalPages" @click="page += 1">
            {{ t("common.next") }}
          </button>
        </div>
      </div>
    </section>

    <details class="mt-6 rounded-xl border border-border bg-card p-4 text-sm">
      <summary class="cursor-pointer font-medium">
        {{ t("settings.usage.metricDefinitions") }}
      </summary>
      <div class="mt-4 grid gap-4 text-xs leading-5 text-muted-foreground md:grid-cols-2">
        <p>
          <strong class="text-foreground">{{ t("settings.usage.userInstructions") }}</strong>
          — {{ t("settings.usage.userInstructionsDefinition") }}
        </p>
        <p>
          <strong class="text-foreground">{{ t("settings.usage.modelRequests") }}</strong>
          — {{ t("settings.usage.modelRequestsDefinition") }}
        </p>
        <p>
          <strong class="text-foreground">{{ t("settings.usage.toolCalls") }}</strong>
          — {{ t("settings.usage.toolCallsDefinition") }}
        </p>
        <p>
          <strong class="text-foreground">{{ t("settings.usage.reasoning") }}</strong>
          — {{ t("settings.usage.reasoningDefinition") }}
        </p>
        <p>
          <strong class="text-foreground">Claude / Codex</strong>
          — {{ t("settings.usage.providerDifference") }}
        </p>
        <p>
          <strong class="text-foreground">{{ t("settings.usage.costContribution") }}</strong>
          — {{ t("settings.usage.costContributionDefinition") }}
        </p>
      </div>
    </details>
  </div>
</template>

<style scoped>
.usage-filter {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 0.25rem;
  color: var(--muted-foreground);
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.usage-filter input,
.usage-filter select,
.usage-select {
  height: 2.25rem;
  min-width: 0;
  border: 1px solid var(--input);
  border-radius: 0.375rem;
  background: var(--background);
  padding-inline: 0.5rem;
  color: var(--foreground);
  font-size: 0.75rem;
  font-weight: 400;
  letter-spacing: normal;
  text-transform: none;
  outline: none;
  transition:
    border-color 150ms,
    box-shadow 150ms;
}

.usage-filter input:focus,
.usage-filter select:focus,
.usage-select:focus {
  border-color: var(--ring);
  box-shadow: 0 0 0 1px var(--ring);
}

.usage-button {
  border: 1px solid var(--input);
  border-radius: 0.375rem;
  background: var(--background);
  padding: 0.375rem 0.75rem;
  color: var(--foreground);
  font-size: 0.75rem;
  transition: background 150ms;
}

.usage-button:hover {
  background: var(--muted);
}

.usage-button:disabled {
  cursor: not-allowed;
  opacity: 0.4;
}
</style>
