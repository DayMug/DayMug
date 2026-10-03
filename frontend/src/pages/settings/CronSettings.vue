<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useRouter } from "vue-router";
import {
  CalendarClock,
  ChevronDown,
  CircleAlert,
  MessageSquareText,
  Pencil,
  Plus,
  Trash2,
  X,
} from "lucide-vue-next";
import AgentAvatar from "@/components/AgentAvatar.vue";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  createCronJob,
  deleteCronJob,
  fetchCronJobs,
  fetchUsers,
  updateCronJob,
  type CronJob,
  type CronJobInput,
  type ProviderEntry,
  type User,
} from "@/composables/useApi";
import { loadModelRegistry } from "@/composables/useModelRegistry";
import { fetchAgentBots } from "@/composables/apiUsers";
import type { AgentBot } from "@/composables/types/agentBot";
import { useConfirm } from "@/composables/useConfirm";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { errorMessage } from "@/lib/errorMessage";

const { t, locale } = useI18n();
const router = useRouter();
const { confirm } = useConfirm();
usePageTitle(computed(() => t("settings.crontab.title")));

const jobs = ref<CronJob[]>([]);
const agents = ref<User[]>([]);
const modelGroups = ref<ProviderEntry[]>([]);
const loading = ref(true);
const saving = ref(false);
const errorMsg = ref("");
const editorOpen = ref(false);
const editingID = ref("");

const detectedTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

function emptyDraft(): CronJobInput {
  return {
    agent_id: agents.value[0]?.id ?? "",
    model: "",
    expression: "0 9 * * 1-5",
    timezone: detectedTimezone,
    description: "",
    prompt: "",
    enabled: true,
    notifications_enabled: true,
    deliver_to_bot: false,
    bot_id: "",
  };
}

const draft = ref<CronJobInput>(emptyDraft());
const agentBots = ref<AgentBot[]>([]);

// Only enabled bots are offerable: delivery goes through a live connector, so a
// disabled one would be a target that silently never posts.
const deliverableBots = computed(() => agentBots.value.filter((bot) => bot.enabled));

// A bot configured is not a bot that has ever held a thread, and delivery needs
// a thread. So this only drives a hint, never a disabled control — an Agent that
// just got its first mention would otherwise be locked out of a working setting.
const selectedAgentHasBot = computed(() => deliverableBots.value.length > 0);

async function loadAgentBots(agentID: string) {
  if (!agentID) {
    agentBots.value = [];
    return;
  }
  try {
    agentBots.value = await fetchAgentBots(agentID);
  } catch {
    // A failed lookup must not block saving the task: an empty list degrades to
    // the "most recently used thread" option, which is the pre-picker behaviour.
    agentBots.value = [];
  }
}

watch(
  () => draft.value.agent_id,
  (agentID) => {
    void loadAgentBots(agentID).then(() => {
      // Switching Agent invalidates a bot belonging to the previous one, and the
      // backend rejects a bot_id that is not the Agent's.
      if (draft.value.bot_id && !deliverableBots.value.some((b) => b.id === draft.value.bot_id)) {
        draft.value.bot_id = "";
      }
    });
  },
);

const cronFields = computed(() => {
  const values = draft.value.expression.trim().split(/\s+/);
  const labels = [
    t("settings.crontab.fields.minute"),
    t("settings.crontab.fields.hour"),
    t("settings.crontab.fields.day"),
    t("settings.crontab.fields.month"),
    t("settings.crontab.fields.weekday"),
  ];
  return labels.map((label, index) => ({ label, value: values[index] || "—" }));
});

async function load() {
  loading.value = true;
  errorMsg.value = "";
  try {
    const [scheduled, availableUsers, registry] = await Promise.all([
      fetchCronJobs(),
      fetchUsers(),
      loadModelRegistry().catch(() => null),
    ]);
    jobs.value = scheduled;
    agents.value = availableUsers.filter((user) => !user.username && !user.archived);
    modelGroups.value = registry?.providers ?? [];
    if (!draft.value.agent_id) {
      draft.value.agent_id = agents.value[0]?.id ?? "";
    }
  } catch (error) {
    errorMsg.value = errorMessage(error, t("settings.crontab.errors.load"));
  } finally {
    loading.value = false;
  }
}

onMounted(load);

function openCreate() {
  editingID.value = "";
  draft.value = emptyDraft();
  editorOpen.value = true;
  errorMsg.value = "";
}

function openEdit(job: CronJob) {
  if (job.disabled_reason === "agent_unavailable") return;
  editingID.value = job.id;
  draft.value = {
    agent_id: job.agent_id,
    model: job.model || "",
    expression: job.expression,
    timezone: job.timezone,
    description: job.description || "",
    prompt: job.prompt,
    enabled: job.enabled,
    notifications_enabled: job.notifications_enabled,
    deliver_to_bot: job.deliver_to_bot,
    bot_id: job.bot_id,
  };
  editorOpen.value = true;
  errorMsg.value = "";
  window.scrollTo({ top: 0, behavior: "smooth" });
}

function closeEditor() {
  editorOpen.value = false;
  editingID.value = "";
  errorMsg.value = "";
}

async function save() {
  if (saving.value) return;
  if (!draft.value.agent_id || !draft.value.prompt.trim() || !draft.value.expression.trim()) {
    errorMsg.value = t("settings.crontab.errors.required");
    return;
  }
  saving.value = true;
  errorMsg.value = "";
  try {
    const input: CronJobInput = {
      ...draft.value,
      expression: draft.value.expression.trim(),
      timezone: draft.value.timezone.trim() || "UTC",
      description: draft.value.description.trim(),
      prompt: draft.value.prompt.trim(),
      // The backend rejects a bot_id with delivery switched off, and keeping a
      // stale one would silently re-target the next run if delivery came back on.
      bot_id: draft.value.deliver_to_bot ? draft.value.bot_id : "",
    };
    const saved = editingID.value
      ? await updateCronJob(editingID.value, input)
      : await createCronJob(input);
    const existing = jobs.value.findIndex((job) => job.id === saved.id);
    if (existing >= 0) {
      jobs.value.splice(existing, 1, saved);
    } else {
      jobs.value.unshift(saved);
    }
    closeEditor();
  } catch (error) {
    errorMsg.value = errorMessage(error, t("settings.crontab.errors.save"));
  } finally {
    saving.value = false;
  }
}

async function toggleJob(job: CronJob, enabled: boolean) {
  errorMsg.value = "";
  try {
    const updated = await updateCronJob(job.id, {
      agent_id: job.agent_id,
      model: job.model || "",
      expression: job.expression,
      timezone: job.timezone,
      description: job.description || "",
      prompt: job.prompt,
      enabled,
      // The row switch sends a whole job, so every field it forgets is a field
      // it silently clears — pausing must not also change delivery settings.
      notifications_enabled: job.notifications_enabled,
      deliver_to_bot: job.deliver_to_bot,
      bot_id: job.bot_id,
    });
    const index = jobs.value.findIndex((item) => item.id === job.id);
    if (index >= 0) jobs.value.splice(index, 1, updated);
  } catch (error) {
    errorMsg.value = errorMessage(error, t("settings.crontab.errors.toggle"));
  }
}

async function removeJob(job: CronJob) {
  const accepted = await confirm({
    message: t("settings.crontab.confirmDelete", { agent: job.agent_name }),
    variant: "destructive",
  });
  if (!accepted) return;
  try {
    await deleteCronJob(job.id);
    jobs.value = jobs.value.filter((item) => item.id !== job.id);
    if (editingID.value === job.id) closeEditor();
  } catch (error) {
    errorMsg.value = errorMessage(error, t("settings.crontab.errors.delete"));
  }
}

function setPreset(expression: string) {
  draft.value.expression = expression;
}

function formatTime(value?: string): string {
  if (!value) return t("settings.crontab.never");
  return new Intl.DateTimeFormat(locale.value, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

function openConversation(job: CronJob) {
  if (!job.last_conversation_id) return;
  router.push({
    name: "chat",
    params: {
      userId: job.agent_id,
      conversationId: job.last_conversation_id,
    },
  });
}
</script>

<template>
  <div class="mx-auto w-full max-w-[720px] px-4 pb-10 sm:px-6">
    <SettingsDetailHeader :title="t('settings.crontab.title')">
      <template #actions>
        <Button
          v-if="!editorOpen"
          size="sm"
          class="h-8 gap-1.5"
          :disabled="agents.length === 0"
          @click="openCreate"
        >
          <Plus class="size-3.5" />
          {{ t("settings.crontab.add") }}
        </Button>
        <Button v-else variant="ghost" size="sm" class="h-8 gap-1.5" @click="closeEditor">
          <X class="size-3.5" />
          {{ t("settings.crontab.cancel") }}
        </Button>
      </template>
    </SettingsDetailHeader>

    <p class="mb-5 text-[13px] leading-5 text-muted-foreground">
      {{ t("settings.crontab.intro") }}
    </p>

    <section
      v-if="editorOpen"
      class="cron-editor mb-6 overflow-hidden rounded-xl border border-border bg-card shadow-sm"
    >
      <div class="border-b border-border px-4 py-3">
        <h2 class="text-sm font-semibold text-foreground">
          {{ editingID ? t("settings.crontab.editTitle") : t("settings.crontab.createTitle") }}
        </h2>
        <p class="mt-0.5 text-[12px] text-muted-foreground">
          {{ t("settings.crontab.editorHint") }}
        </p>
      </div>

      <div class="space-y-5 p-4">
        <label class="block">
          <span class="mb-1.5 block text-[12px] font-medium text-foreground">
            {{ t("settings.crontab.agent") }}
          </span>
          <div class="relative">
            <select
              v-model="draft.agent_id"
              data-testid="cron-agent"
              class="h-9 w-full appearance-none rounded-md border border-input bg-background px-3 pr-10 text-sm text-foreground outline-none focus:border-ring focus:ring-2 focus:ring-ring/30"
            >
              <option value="" disabled>{{ t("settings.crontab.chooseAgent") }}</option>
              <option v-for="agent in agents" :key="agent.id" :value="agent.id">
                {{ agent.name }}
              </option>
            </select>
            <ChevronDown
              data-testid="cron-select-chevron"
              aria-hidden="true"
              class="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            />
          </div>
        </label>

        <label class="block">
          <span class="mb-1.5 block text-[12px] font-medium text-foreground">
            {{ t("settings.crontab.model") }}
          </span>
          <div class="relative">
            <select
              v-model="draft.model"
              data-testid="cron-model"
              class="h-9 w-full appearance-none rounded-md border border-input bg-background px-3 pr-10 text-sm text-foreground outline-none focus:border-ring focus:ring-2 focus:ring-ring/30"
            >
              <option value="">{{ t("settings.crontab.inheritModel") }}</option>
              <optgroup v-for="provider in modelGroups" :key="provider.name" :label="provider.name">
                <option v-for="model in provider.models" :key="model" :value="model">
                  {{ model }}
                </option>
              </optgroup>
            </select>
            <ChevronDown
              data-testid="cron-select-chevron"
              aria-hidden="true"
              class="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            />
          </div>
          <span class="mt-1 block text-[11px] text-muted-foreground">
            {{ t("settings.crontab.modelHint") }}
          </span>
        </label>

        <div>
          <div class="mb-1.5 flex items-center justify-between gap-3">
            <label for="cron-expression" class="text-[12px] font-medium text-foreground">
              {{ t("settings.crontab.expression") }}
            </label>
            <span class="text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
              {{ t("settings.crontab.posix") }}
            </span>
          </div>
          <Input
            id="cron-expression"
            v-model="draft.expression"
            data-testid="cron-expression"
            class="h-11 font-mono text-[16px] tracking-[0.08em]"
            placeholder="0 9 * * 1-5"
            autocomplete="off"
          />
          <div class="cron-ruler mt-2" aria-hidden="true">
            <div v-for="field in cronFields" :key="field.label" class="cron-ruler-cell">
              <span class="cron-ruler-value">{{ field.value }}</span>
              <span class="cron-ruler-label">{{ field.label }}</span>
            </div>
          </div>
          <div class="mt-2 flex flex-wrap gap-1.5">
            <button class="cron-preset" type="button" @click="setPreset('0 * * * *')">
              {{ t("settings.crontab.presets.hourly") }}
            </button>
            <button class="cron-preset" type="button" @click="setPreset('0 9 * * 1-5')">
              {{ t("settings.crontab.presets.weekdays") }}
            </button>
            <button class="cron-preset" type="button" @click="setPreset('0 18 * * *')">
              {{ t("settings.crontab.presets.daily") }}
            </button>
          </div>
        </div>

        <label class="block">
          <span class="mb-1.5 block text-[12px] font-medium text-foreground">
            {{ t("settings.crontab.timezone") }}
          </span>
          <Input
            v-model="draft.timezone"
            data-testid="cron-timezone"
            class="font-mono text-sm"
            placeholder="Asia/Shanghai"
            autocomplete="off"
          />
          <span class="mt-1 block text-[11px] text-muted-foreground">
            {{ t("settings.crontab.timezoneHint") }}
          </span>
        </label>

        <label class="block">
          <span class="mb-1.5 block text-[12px] font-medium text-foreground">
            {{ t("settings.crontab.description") }}
          </span>
          <Input
            v-model="draft.description"
            data-testid="cron-description"
            :placeholder="t('settings.crontab.descriptionPlaceholder')"
            autocomplete="off"
          />
          <span class="mt-1 block text-[11px] text-muted-foreground">
            {{ t("settings.crontab.descriptionHint") }}
          </span>
        </label>

        <label class="block">
          <span class="mb-1.5 block text-[12px] font-medium text-foreground">
            {{ t("settings.crontab.task") }}
          </span>
          <Textarea
            v-model="draft.prompt"
            data-testid="cron-prompt"
            class="min-h-32 resize-y text-sm leading-6"
            :placeholder="t('settings.crontab.taskPlaceholder')"
          />
          <span class="mt-1 block text-[11px] text-muted-foreground">
            {{ t("settings.crontab.taskHint") }}
          </span>
        </label>

        <div class="flex items-center justify-between rounded-lg border border-border px-3 py-2.5">
          <div>
            <div class="text-[12px] font-medium text-foreground">
              {{ t("settings.crontab.enabled") }}
            </div>
            <div class="text-[11px] text-muted-foreground">
              {{ t("settings.crontab.enabledHint") }}
            </div>
          </div>
          <Switch v-model="draft.enabled" size="sm" />
        </div>

        <div class="flex items-center justify-between rounded-lg border border-border px-3 py-2.5">
          <div>
            <div class="text-[12px] font-medium text-foreground">
              {{ t("settings.crontab.notificationsEnabled") }}
            </div>
            <div class="text-[11px] text-muted-foreground">
              {{ t("settings.crontab.notificationsEnabledHint") }}
            </div>
          </div>
          <Switch
            v-model="draft.notifications_enabled"
            size="sm"
            data-testid="cron-notifications-enabled"
          />
        </div>

        <div class="flex items-center justify-between rounded-lg border border-border px-3 py-2.5">
          <div>
            <div class="text-[12px] font-medium text-foreground">
              {{ t("settings.crontab.deliverToBot") }}
            </div>
            <div class="text-[11px] text-muted-foreground">
              {{ t("settings.crontab.deliverToBotHint") }}
            </div>
            <div
              v-if="draft.deliver_to_bot && !selectedAgentHasBot"
              class="mt-1 text-[11px] text-amber-600 dark:text-amber-500"
              data-testid="cron-deliver-unbound"
            >
              {{ t("settings.crontab.deliverToBotUnbound") }}
            </div>
          </div>
          <Switch v-model="draft.deliver_to_bot" size="sm" data-testid="cron-deliver-to-bot" />
        </div>

        <label v-if="draft.deliver_to_bot" class="block">
          <span class="mb-1.5 block text-[12px] font-medium text-foreground">
            {{ t("settings.crontab.deliverToBotTarget") }}
          </span>
          <div class="relative">
            <select
              v-model="draft.bot_id"
              data-testid="cron-delivery-bot"
              class="h-9 w-full appearance-none rounded-md border border-input bg-background px-3 pr-10 text-sm text-foreground outline-none focus:border-ring focus:ring-2 focus:ring-ring/30"
            >
              <option value="">{{ t("settings.crontab.deliverToBotAuto") }}</option>
              <option v-for="bot in deliverableBots" :key="bot.id" :value="bot.id">
                {{ bot.name }} · {{ bot.platform }}
              </option>
            </select>
            <ChevronDown
              data-testid="cron-select-chevron"
              aria-hidden="true"
              class="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            />
          </div>
          <span class="mt-1 block text-[11px] text-muted-foreground">
            {{ t("settings.crontab.deliverToBotTargetHint") }}
          </span>
        </label>
      </div>

      <div class="flex justify-end gap-2 border-t border-border bg-muted/25 px-4 py-3">
        <Button variant="ghost" size="sm" @click="closeEditor">
          {{ t("settings.crontab.cancel") }}
        </Button>
        <Button size="sm" :disabled="saving" @click="save">
          {{ saving ? t("settings.crontab.saving") : t("settings.crontab.save") }}
        </Button>
      </div>
    </section>

    <div
      v-if="errorMsg"
      role="alert"
      class="mb-4 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2.5 text-sm text-destructive"
    >
      <CircleAlert class="mt-0.5 size-4 shrink-0" />
      <span>{{ errorMsg }}</span>
    </div>

    <div
      v-if="loading"
      class="rounded-xl border border-border bg-card px-4 py-5 text-sm text-muted-foreground"
    >
      {{ t("settings.crontab.loading") }}
    </div>

    <div
      v-else-if="agents.length === 0 && jobs.length === 0"
      class="rounded-xl border border-dashed border-border bg-card px-5 py-8 text-center"
    >
      <CalendarClock class="mx-auto mb-3 size-7 text-muted-foreground" />
      <p class="text-sm font-medium text-foreground">
        {{ t("settings.crontab.noAgents") }}
      </p>
      <p class="mx-auto mt-1 max-w-sm text-[12px] leading-5 text-muted-foreground">
        {{ t("settings.crontab.noAgentsHint") }}
      </p>
    </div>

    <div
      v-else-if="jobs.length === 0"
      class="rounded-xl border border-dashed border-border bg-card px-5 py-8 text-center"
    >
      <CalendarClock class="mx-auto mb-3 size-7 text-muted-foreground" />
      <p class="text-sm font-medium text-foreground">
        {{ t("settings.crontab.empty") }}
      </p>
      <p class="mx-auto mt-1 max-w-sm text-[12px] leading-5 text-muted-foreground">
        {{ t("settings.crontab.emptyHint") }}
      </p>
      <Button size="sm" class="mt-4 gap-1.5" @click="openCreate">
        <Plus class="size-3.5" />
        {{ t("settings.crontab.add") }}
      </Button>
    </div>

    <div v-else class="space-y-3">
      <article
        v-for="job in jobs"
        :key="job.id"
        class="cron-job rounded-xl border border-border bg-card p-4"
        :class="{ 'cron-job-disabled': !job.enabled }"
      >
        <div class="flex items-start gap-3">
          <AgentAvatar
            :name="job.agent_name"
            class="mt-0.5 size-8 shrink-0"
            fallback-class="text-[11px] font-bold"
          />
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <h3 class="truncate text-sm font-semibold text-foreground">
                {{ job.agent_name || t("settings.crontab.unavailableAgent") }}
              </h3>
              <span class="cron-expression-chip">{{ job.expression }}</span>
            </div>
            <div class="mt-1 text-[11px] text-muted-foreground">
              {{ job.timezone }} · {{ job.model || t("settings.crontab.inheritedModel") }}
            </div>
          </div>
          <Switch
            :model-value="job.enabled"
            size="sm"
            :disabled="job.disabled_reason === 'agent_unavailable'"
            :aria-label="t('settings.crontab.enabled')"
            @update:model-value="toggleJob(job, $event)"
          />
        </div>

        <p
          v-if="job.description"
          data-testid="cron-job-description"
          class="mt-3 text-[13px] font-medium leading-5 text-foreground"
        >
          {{ job.description }}
        </p>
        <p
          v-else
          data-testid="cron-job-prompt-preview"
          class="mt-3 line-clamp-3 whitespace-pre-wrap text-[13px] leading-5 text-foreground/90"
        >
          {{ job.prompt }}
        </p>

        <div
          v-if="job.disabled_reason === 'agent_unavailable'"
          class="mt-3 flex items-center gap-1.5 rounded-md bg-destructive/8 px-2.5 py-2 text-[11px] text-destructive"
        >
          <CircleAlert class="size-3.5 shrink-0" />
          {{ t("settings.crontab.agentDisabled") }}
        </div>
        <div
          v-else-if="job.last_error"
          class="mt-3 flex items-start gap-1.5 rounded-md bg-destructive/8 px-2.5 py-2 text-[11px] text-destructive"
        >
          <CircleAlert class="mt-px size-3.5 shrink-0" />
          <span class="break-all">{{ job.last_error }}</span>
        </div>

        <dl class="mt-3 grid grid-cols-2 gap-3 border-t border-border pt-3 text-[11px]">
          <div>
            <dt class="text-muted-foreground">{{ t("settings.crontab.nextRun") }}</dt>
            <dd class="mt-0.5 text-foreground">
              {{ job.enabled ? formatTime(job.next_run_at) : t("settings.crontab.paused") }}
            </dd>
          </div>
          <div>
            <dt class="text-muted-foreground">{{ t("settings.crontab.lastRun") }}</dt>
            <dd class="mt-0.5 text-foreground">{{ formatTime(job.last_run_at) }}</dd>
          </div>
        </dl>

        <div class="mt-3 flex items-center justify-end gap-1">
          <Button
            v-if="job.last_conversation_id"
            variant="ghost"
            size="sm"
            class="h-7 gap-1.5 px-2 text-[12px]"
            @click="openConversation(job)"
          >
            <MessageSquareText class="size-3.5" />
            {{ t("settings.crontab.openChat") }}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="h-7 gap-1.5 px-2 text-[12px]"
            :disabled="job.disabled_reason === 'agent_unavailable'"
            @click="openEdit(job)"
          >
            <Pencil class="size-3.5" />
            {{ t("settings.crontab.edit") }}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="h-7 gap-1.5 px-2 text-[12px] text-destructive hover:text-destructive"
            @click="removeJob(job)"
          >
            <Trash2 class="size-3.5" />
            {{ t("settings.crontab.delete") }}
          </Button>
        </div>
      </article>
    </div>
  </div>
</template>

<style scoped>
.cron-ruler {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: color-mix(in srgb, var(--muted) 42%, transparent);
}

.cron-ruler-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: center;
  padding: 0.4rem 0.15rem;
}

.cron-ruler-cell + .cron-ruler-cell {
  border-left: 1px solid var(--border);
}

.cron-ruler-value {
  max-width: 100%;
  overflow: hidden;
  color: var(--foreground);
  font-family: var(--font-mono);
  font-size: 12px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cron-ruler-label {
  margin-top: 1px;
  color: var(--muted-foreground);
  font-size: 9px;
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.cron-preset {
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 0.2rem 0.55rem;
  color: var(--muted-foreground);
  font-size: 10px;
  transition:
    border-color 120ms ease,
    color 120ms ease,
    background-color 120ms ease;
}

.cron-preset:hover {
  border-color: var(--ring);
  background: var(--accent);
  color: var(--foreground);
}

.cron-expression-chip {
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 5px;
  background: var(--muted);
  padding: 0.1rem 0.35rem;
  color: var(--muted-foreground);
  font-family: var(--font-mono);
  font-size: 10px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cron-job {
  transition:
    border-color 120ms ease,
    opacity 120ms ease;
}

.cron-job:hover {
  border-color: color-mix(in srgb, var(--ring) 45%, var(--border));
}

.cron-job-disabled {
  opacity: 0.72;
}

@media (prefers-reduced-motion: reduce) {
  .cron-job,
  .cron-preset {
    transition: none;
  }
}
</style>
