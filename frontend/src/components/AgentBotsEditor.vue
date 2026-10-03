<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { CheckCircle2, Circle, CircleHelp, Loader2, Plus, Trash2, XCircle } from "lucide-vue-next";
import { useI18n } from "vue-i18n";
import { errorMessage } from "@/lib/errorMessage";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogHeader,
  DialogScrollContent,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  createAgentBot,
  deleteAgentBot,
  fetchAgentBotRequirements,
  fetchAgentBots,
  fetchAgentBotStatuses,
  testAgentBotConnection,
  updateAgentBot,
} from "@/composables/apiUsers";
import { loadModelRegistry } from "@/composables/useModelRegistry";
import { useWeChatPairing } from "@/composables/useWeChatPairing";
import { BOT_PLATFORM_IDS, BOT_PLATFORMS, defaultChannelsFor } from "@/lib/botPlatforms";
import type {
  AgentBot,
  AgentBotCredentialField,
  AgentBotInput,
  AgentBotPlatform,
  AgentBotStatus,
  BotConnectionTestResult,
  BotPermissionRequirement,
  BotPermissionRequirements,
  ProviderEntry,
} from "@/composables/apiTypes";

const props = defineProps<{ agentId: string }>();
// The agent roster is a module-level cache seeded once at bootstrap, and it
// carries the derived bot_platforms badge. Editing bots here changes that
// derived field server-side but leaves the cache untouched, so the settings
// list would show a stale badge until a full page reload. Announcing the
// change lets the parent re-pull the roster.
const emit = defineEmits<{ changed: [] }>();
const { t } = useI18n();

function platformLabel(platform: AgentBotPlatform): string {
  const meta = BOT_PLATFORMS[platform];
  return meta ? t(meta.labelKey) : platform;
}

const platformOptions = computed(() =>
  BOT_PLATFORM_IDS.map((value) => ({
    value,
    label: platformLabel(value),
  })),
);

const bots = ref<AgentBot[]>([]);
const statuses = ref<AgentBotStatus[]>([]);
const modelGroups = ref<ProviderEntry[]>([]);
const editingId = ref<string | null>(null);
const draft = ref<AgentBotInput | null>(null);
// The bot being edited, kept only for its `*_configured` flags: credential
// inputs always open blank because the server never returns the secrets, so
// these flags are the sole signal that something is already stored.
const editingBot = ref<AgentBot | null>(null);
const saving = ref(false);
const error = ref("");
const channelRulesHelpOpen = ref(false);
// Spelled out rather than derived from BOT_PLATFORMS so the Record type forces
// a new platform to be listed here too, instead of silently defaulting to
// undefined until the server response lands.
const requirements = ref<BotPermissionRequirements>({
  slack: [],
  feishu: [],
  telegram: [],
  wechat: [],
});
const connectionTest = ref<BotConnectionTestResult | null>(null);
const testingConnection = ref(false);
const connectionError = ref("");

// A freshly added bot starts enabled: reaching this form already means the
// operator wants the bot live, and a bot saved silently disabled just goes
// quiet with no error anywhere — the transport never starts, so the platform
// never delivers a message to answer. Editing an existing bot keeps whatever
// it was set to (see editBot).
const defaultDraft = (): AgentBotInput => ({
  name: "",
  platform: "slack",
  enabled: true,
  model: "",
  max_conversation_duration: "12h",
  bot_token: "",
  bot_app_token: "",
  bot_app_id: "",
  bot_app_secret: "",
  channels: defaultChannelsFor("slack"),
  unconfigured_reply: t("settings.agentForm.unconfiguredReplyDefault"),
  unauthorized_reply: t("settings.agentForm.unauthorizedReplyDefault"),
});

const statusByBot = computed(
  () => new Map(statuses.value.map((status) => [status.bot_id, status])),
);
// Falls back to empty rather than undefined: a server older than the platform
// simply omits it from the requirements map, and an undefined v-for source
// would break the whole form instead of just showing no permissions.
const currentRequirements = computed(() =>
  draft.value ? (requirements.value[draft.value.platform] ?? []) : [],
);
// A credential counts as present when the user typed one or when the server
// says it already has one — the latter is what the test call back-fills.
function credentialStored(field: AgentBotCredentialField): boolean {
  return Boolean(editingBot.value?.[`${field}_configured`]);
}

function hasCredential(field: AgentBotCredentialField): boolean {
  return Boolean(draft.value?.[field].trim()) || credentialStored(field);
}

function credentialPlaceholder(field: AgentBotCredentialField): string {
  return credentialStored(field) ? t("settings.agentForm.credentialStored") : "";
}

// The permission badge names which credential a check depends on. "bot_token"
// means a different thing on each platform, so the label follows the draft
// rather than the credential id alone.
function credentialLabel(credential: BotPermissionRequirement["credential"]): string {
  if (credential === "app") return t("settings.agentForm.feishuAppCredential");
  if (credential === "app_token") return t("settings.agentForm.slackAppToken");
  const own = draftMeta.value.credentials.find(({ field }) => field === "bot_token");
  if (own) return t(own.labelKey);
  return t(draftMeta.value.botTokenLabelKey ?? "settings.agentForm.slackBotToken");
}

const draftMeta = computed(() => BOT_PLATFORMS[draft.value?.platform ?? "slack"]);

const canTestConnection = computed(() => {
  if (!draft.value) return false;
  // A platform with no credential inputs would vacuously pass the check below,
  // so pairing-based platforms are gated on the pairing itself instead.
  if (draft.value.platform === "wechat") return isWeChatPaired.value;
  return draftMeta.value.credentials.every(({ field }) => hasCredential(field));
});

// --- WeChat pairing ---------------------------------------------------------
// The pairing yields the token and the per-account gateway it must be sent to.
// They ride in the generic bot_token / bot_app_id slots so a new platform stays
// off the bots schema.

const {
  qrImage: pairingQR,
  scanURL: pairingScanURL,
  status: pairingStatus,
  error: pairingError,
  inFlight: pairingInFlight,
  start: beginPairing,
  reset: resetPairing,
} = useWeChatPairing({
  agentId: () => props.agentId,
  onConfirmed: ({ botToken, baseUrl }) => {
    // The scan can confirm long after the user has moved on to another bot;
    // writing into whatever draft is open then would overwrite that bot's
    // token and break it on save.
    if (!draft.value || draft.value !== pairingDraft || draft.value.platform !== "wechat") {
      return false;
    }
    draft.value.bot_token = botToken;
    draft.value.bot_app_id = baseUrl;
    return true;
  },
});

// Paired means either this session just paired, or the saved bot already has
// credentials the server never sends back.
const isWeChatPaired = computed(
  () =>
    pairingStatus.value === "confirmed" ||
    (hasCredential("bot_token") && hasCredential("bot_app_id")),
);

// The draft the running pairing belongs to. Any other draft — a different
// bot, or this one switched off WeChat — ends the pairing.
let pairingDraft: AgentBotInput | null = null;

function startPairing() {
  if (!draft.value) return;
  pairingDraft = draft.value;
  void beginPairing();
}

watch(
  () => [draft.value, draft.value?.platform] as const,
  ([current, platform]) => {
    if (pairingDraft && (current !== pairingDraft || platform !== "wechat")) {
      pairingDraft = null;
      resetPairing();
    }
  },
);

const channelRulesExample = computed(() => {
  const meta = draftMeta.value;
  return JSON.stringify(
    [
      {
        channel: meta.channelExample,
        enabled: true,
        require_mention: true,
        auto_reply: false,
        allowed_user_ids: [meta.userExample],
        extra_prompt: t("settings.agentForm.channelsExamplePrompt"),
        ...(meta.showsBotRelayExample
          ? {
              allow_bot_mentions: true,
              bot_mention_limit: 3,
              bot_mention_window_minutes: 30,
            }
          : {}),
      },
      {
        channel: "*",
        require_mention: true,
      },
      {
        channel: "dm",
        auto_reply: true,
      },
      {
        channel: meta.otherChannelExample,
        enabled: false,
      },
    ],
    null,
    2,
  );
});

async function refresh() {
  const [nextBots, nextStatuses] = await Promise.all([
    fetchAgentBots(props.agentId),
    fetchAgentBotStatuses(props.agentId),
  ]);
  bots.value = nextBots;
  statuses.value = nextStatuses.bots;
}

onMounted(() => {
  void Promise.all([
    refresh(),
    loadModelRegistry()
      .then((registry) => {
        modelGroups.value = registry.providers;
      })
      .catch(() => undefined),
    fetchAgentBotRequirements(props.agentId).then((nextRequirements) => {
      requirements.value = nextRequirements;
    }),
  ]).catch((cause) => {
    error.value = errorMessage(cause);
  });
});

watch(
  draft,
  () => {
    connectionTest.value = null;
    connectionError.value = "";
  },
  { deep: true },
);

watch(
  () => draft.value?.platform,
  (platform, previousPlatform) => {
    if (!draft.value || editingId.value !== "new" || !platform || !previousPlatform) return;
    if (draft.value.channels === defaultChannelsFor(previousPlatform)) {
      draft.value.channels = defaultChannelsFor(platform);
    }
  },
);

function addBot() {
  editingId.value = "new";
  editingBot.value = null;
  draft.value = defaultDraft();
  error.value = "";
}

function editBot(bot: AgentBot) {
  editingId.value = bot.id;
  editingBot.value = bot;
  draft.value = {
    name: bot.name,
    platform: bot.platform,
    enabled: bot.enabled,
    model: bot.model || "",
    max_conversation_duration: bot.max_conversation_duration || "12h",
    // Blank on purpose: submitting them unchanged keeps the stored secrets.
    bot_token: "",
    bot_app_token: "",
    bot_app_id: "",
    bot_app_secret: "",
    channels: bot.channels,
    unconfigured_reply: bot.unconfigured_reply,
    unauthorized_reply: bot.unauthorized_reply,
  };
  error.value = "";
}

function cancelEdit() {
  editingId.value = null;
  editingBot.value = null;
  draft.value = null;
  error.value = "";
}

async function saveBot() {
  if (!draft.value || !editingId.value || saving.value) return;
  error.value = "";
  try {
    JSON.parse(draft.value.channels || "[]");
  } catch {
    error.value = t("settings.agentForm.invalidChannels");
    return;
  }
  saving.value = true;
  try {
    if (editingId.value === "new") {
      await createAgentBot(props.agentId, draft.value);
    } else {
      await updateAgentBot(props.agentId, editingId.value, draft.value);
    }
    cancelEdit();
    await refresh();
    emit("changed");
  } catch (cause) {
    error.value = errorMessage(cause);
  } finally {
    saving.value = false;
  }
}

async function removeBot(bot: AgentBot) {
  if (!globalThis.confirm(t("settings.agentForm.deleteBotConfirm", { name: bot.name }))) return;
  error.value = "";
  try {
    await deleteAgentBot(props.agentId, bot.id);
    await refresh();
    emit("changed");
  } catch (cause) {
    error.value = errorMessage(cause);
  }
}

function permissionGranted(permission: BotPermissionRequirement): boolean | null {
  if (!connectionTest.value) return null;
  return (
    connectionTest.value.permissions.find((check) => check.key === permission.key)?.granted ?? false
  );
}

async function testConnection() {
  if (!draft.value || testingConnection.value || !canTestConnection.value) return;
  connectionTest.value = null;
  connectionError.value = "";
  testingConnection.value = true;
  try {
    connectionTest.value = await testAgentBotConnection(props.agentId, {
      ...draft.value,
      ...(editingId.value && editingId.value !== "new" ? { bot_id: editingId.value } : {}),
    });
  } catch (cause) {
    connectionError.value = errorMessage(cause);
  } finally {
    testingConnection.value = false;
  }
}
</script>

<template>
  <!-- Third and last tab of the agent form (basic / role / bots) — the panel
       id and numeral must track that position, not the section count this
       editor had before the form was collapsed into three tabs. -->
  <section id="form-section-3" class="form-section">
    <header class="form-section-head">
      <div class="form-section-roman">III.</div>
      <div class="form-section-title">{{ t("settings.agentForm.integrations") }}</div>
      <div class="form-section-sub">{{ t("settings.agentForm.attachedBotsSub") }}</div>
    </header>
    <div class="form-section-body">
      <div class="flex items-center justify-between gap-3">
        <p class="min-w-0 text-xs text-muted-foreground">
          {{ t("settings.agentForm.sharedPromptHelp") }}
        </p>
        <div class="flex shrink-0 items-center gap-1">
          <Button type="button" variant="outline" size="sm" @click="addBot">
            <Plus class="mr-1 size-3.5" />{{ t("settings.agentForm.addBot") }}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            class="size-6 shrink-0 rounded-full text-muted-foreground hover:text-foreground"
            :aria-label="t('settings.agentForm.botConfigHelpLabel')"
            data-testid="bot-config-help"
            @click="channelRulesHelpOpen = true"
          >
            <CircleHelp class="size-4" />
          </Button>
        </div>
      </div>

      <div v-if="bots.length" class="space-y-2">
        <div
          v-for="bot in bots"
          :key="bot.id"
          class="flex items-center gap-3 rounded-md border border-border bg-card px-3 py-2"
        >
          <div class="min-w-0 flex-1">
            <div class="truncate text-sm font-medium">{{ bot.name }}</div>
            <div class="text-[11px] text-muted-foreground">
              {{ platformLabel(bot.platform) }}
              ·
              <span :class="statusByBot.get(bot.id)?.running ? 'text-emerald-600' : undefined">
                {{
                  bot.enabled
                    ? statusByBot.get(bot.id)?.running
                      ? t("settings.agentForm.connected")
                      : t("settings.agentForm.disconnected")
                    : t("settings.agentForm.disabled")
                }}
              </span>
              ·
              {{ bot.model || t("settings.agentForm.inheritedModel") }}
            </div>
          </div>
          <Button type="button" variant="ghost" size="sm" @click="editBot(bot)">
            {{ t("common.edit") }}
          </Button>
          <Button type="button" variant="ghost" size="icon" @click="removeBot(bot)">
            <Trash2 class="size-4 text-destructive" />
          </Button>
        </div>
      </div>
      <p
        v-else
        class="rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground"
      >
        {{ t("settings.agentForm.noBots") }}
      </p>

      <div v-if="draft" class="space-y-4 rounded-md border border-border bg-muted/20 p-4">
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label class="field-label">{{ t("settings.agentForm.botName") }}</label>
            <Input v-model="draft.name" data-testid="bot-name-input" />
          </div>
          <div>
            <label class="field-label">{{ t("settings.agentForm.platform") }}</label>
            <select
              v-model="draft.platform"
              class="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
            >
              <option v-for="option in platformOptions" :key="option.value" :value="option.value">
                {{ option.label }}
              </option>
            </select>
          </div>
        </div>
        <div class="flex items-center justify-between rounded-md border border-border px-3 py-2">
          <span class="text-sm">{{ t("settings.agentForm.enabled") }}</span>
          <Switch v-model="draft.enabled" />
        </div>
        <div>
          <label class="field-label">{{ t("settings.agentForm.botModel") }}</label>
          <select
            v-model="draft.model"
            data-testid="bot-model"
            class="h-9 w-full rounded-md border border-input bg-background px-3 text-sm"
          >
            <option value="">{{ t("settings.agentForm.inheritModel") }}</option>
            <optgroup v-for="provider in modelGroups" :key="provider.name" :label="provider.name">
              <option v-for="model in provider.models" :key="model" :value="model">
                {{ model }}
              </option>
            </optgroup>
          </select>
          <p class="mt-1.5 text-xs text-muted-foreground">
            {{ t("settings.agentForm.botModelHelp") }}
          </p>
        </div>
        <div>
          <label class="field-label">
            {{ t("settings.agentForm.maxConversationDuration") }}
          </label>
          <Input
            v-model="draft.max_conversation_duration"
            placeholder="12h"
            autocomplete="off"
            data-testid="max-conversation-duration-input"
          />
          <p class="mt-1.5 text-xs text-muted-foreground">
            {{ t("settings.agentForm.maxConversationDurationHelp") }}
          </p>
        </div>
        <div class="grid gap-4 md:grid-cols-2">
          <div v-for="credential in draftMeta.credentials" :key="credential.field">
            <label class="field-label">{{ t(credential.labelKey) }}</label>
            <Input
              v-model="draft[credential.field]"
              :type="credential.secret ? 'password' : 'text'"
              autocomplete="off"
              :data-testid="credential.testId"
              :placeholder="credentialPlaceholder(credential.field)"
            />
          </div>
        </div>
        <div
          v-if="draft.platform === 'wechat'"
          class="space-y-3 rounded-md border border-border bg-background/70 p-3"
          data-testid="wechat-pairing"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 class="text-sm font-medium">{{ t("settings.agentForm.wechatPairing") }}</h3>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ t("settings.agentForm.wechatPairingHelp") }}
              </p>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              :disabled="pairingInFlight"
              data-testid="wechat-pair-button"
              @click="startPairing"
            >
              <Loader2 v-if="pairingInFlight" class="mr-1.5 h-3.5 w-3.5 animate-spin" />
              {{
                isWeChatPaired
                  ? t("settings.agentForm.wechatRepair")
                  : t("settings.agentForm.wechatPair")
              }}
            </Button>
          </div>
          <img
            v-if="pairingQR"
            :src="`data:image/png;base64,${pairingQR}`"
            :alt="t('settings.agentForm.wechatPairing')"
            class="h-44 w-44 rounded bg-white p-2"
            data-testid="wechat-qr"
          />
          <a
            v-if="pairingScanURL && pairingInFlight"
            :href="pairingScanURL"
            target="_blank"
            rel="noopener noreferrer"
            class="block break-all text-xs text-muted-foreground underline"
            data-testid="wechat-scan-url"
          >
            {{ pairingScanURL }}
          </a>
          <p
            v-if="pairingStatus === 'waiting'"
            class="text-xs text-muted-foreground"
            data-testid="wechat-pairing-waiting"
          >
            {{ t("settings.agentForm.wechatPairingWaiting") }}
          </p>
          <p
            v-else-if="pairingStatus === 'scanned'"
            class="text-xs text-muted-foreground"
            data-testid="wechat-pairing-scanned"
          >
            {{ t("settings.agentForm.wechatPairingScanned") }}
          </p>
          <p
            v-else-if="pairingStatus === 'blocked'"
            class="text-xs text-amber-600"
            data-testid="wechat-pairing-blocked"
          >
            {{ t("settings.agentForm.wechatPairingBlocked") }}
          </p>
          <p
            v-else-if="pairingStatus === 'confirmed'"
            class="text-xs text-emerald-600"
            data-testid="wechat-pairing-confirmed"
          >
            {{ t("settings.agentForm.wechatPairingConfirmed") }}
          </p>
          <p
            v-else-if="pairingStatus === 'expired'"
            class="text-xs text-amber-600"
            data-testid="wechat-pairing-expired"
          >
            {{ t("settings.agentForm.wechatPairingExpired") }}
          </p>
          <p v-else-if="isWeChatPaired" class="text-xs text-muted-foreground">
            {{ t("settings.agentForm.wechatPairingStored") }}
          </p>
          <p v-if="pairingError" class="text-xs text-destructive">{{ pairingError }}</p>
        </div>
        <p v-if="editingBot && !editingBot.credentials_configured" class="text-xs text-amber-600">
          {{ t("settings.agentForm.credentialsMissing") }}
        </p>
        <div
          class="space-y-3 rounded-md border border-border bg-background/70 p-3"
          data-testid="bot-permissions"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 class="text-sm font-medium">
                {{ t("settings.agentForm.requiredPermissions") }}
              </h3>
              <p class="mt-0.5 text-xs text-muted-foreground">
                {{ t(draftMeta.permissionsHelpKey) }}
              </p>
            </div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              :disabled="testingConnection || !canTestConnection"
              data-testid="test-bot-connection"
              @click="testConnection"
            >
              <Loader2 v-if="testingConnection" class="mr-1.5 size-3.5 animate-spin" />
              {{
                testingConnection
                  ? t("settings.agentForm.testingConnection")
                  : t("settings.agentForm.testConnection")
              }}
            </Button>
          </div>

          <div class="grid gap-2 sm:grid-cols-2">
            <div
              v-for="permission in currentRequirements"
              :key="permission.key"
              class="flex min-w-0 items-center gap-2 rounded border border-border/70 px-2.5 py-2 text-xs"
            >
              <CheckCircle2
                v-if="permissionGranted(permission) === true"
                class="size-4 shrink-0 text-emerald-600"
                :aria-label="t('settings.agentForm.permissionGranted')"
              />
              <XCircle
                v-else-if="permissionGranted(permission) === false"
                class="size-4 shrink-0 text-destructive"
                :aria-label="t('settings.agentForm.permissionMissing')"
              />
              <Circle
                v-else
                class="size-4 shrink-0 text-muted-foreground/60"
                :aria-label="t('settings.agentForm.permissionUntested')"
              />
              <code class="min-w-0 flex-1 break-all text-[11px]">{{ permission.key }}</code>
              <span
                class="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
              >
                {{ credentialLabel(permission.credential) }}
              </span>
              <span v-if="!permission.required" class="shrink-0 text-[10px] text-muted-foreground">
                {{ t("settings.agentForm.optionalPermission") }}
              </span>
            </div>
          </div>

          <div
            v-if="connectionTest"
            class="rounded-md px-3 py-2 text-xs"
            :class="
              connectionTest.connected && connectionTest.all_required_permissions_granted
                ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400'
                : 'bg-destructive/10 text-destructive'
            "
            data-testid="connection-test-result"
          >
            <p class="font-medium">
              {{
                !connectionTest.connected
                  ? t("settings.agentForm.connectionTestFailed")
                  : connectionTest.all_required_permissions_granted
                    ? t("settings.agentForm.connectionTestSucceeded")
                    : t("settings.agentForm.connectionTestMissingPermissions")
              }}
            </p>
            <p v-if="connectionTest.identity" class="mt-0.5 opacity-80">
              {{
                t("settings.agentForm.connectionIdentity", {
                  identity: connectionTest.identity,
                })
              }}
            </p>
            <p v-if="connectionTest.error" class="mt-0.5 break-words opacity-80">
              {{ connectionTest.error }}
            </p>
          </div>
          <p v-else-if="connectionError" class="text-xs text-destructive">
            {{ connectionError }}
          </p>
        </div>
        <div>
          <div class="mb-1 flex items-center gap-1.5">
            <label class="field-label !mb-0">{{ t("settings.agentForm.channels") }}</label>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              class="size-6 rounded-full text-muted-foreground hover:text-foreground"
              :aria-label="t('settings.agentForm.botConfigHelpLabel')"
              data-testid="channel-rules-help"
              @click="channelRulesHelpOpen = true"
            >
              <CircleHelp class="size-4" />
            </Button>
          </div>
          <Textarea v-model="draft.channels" class="font-mono text-xs" :rows="6" />
          <p class="mt-1.5 text-xs text-muted-foreground">
            {{ t("settings.agentForm.channelsHelp") }}
          </p>
        </div>
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label class="field-label">{{ t("settings.agentForm.unconfiguredReply") }}</label>
            <Textarea
              v-model="draft.unconfigured_reply"
              :rows="3"
              data-testid="unconfigured-reply-input"
            />
            <p class="mt-1.5 text-xs text-muted-foreground">
              {{ t("settings.agentForm.unconfiguredReplyHelp") }}
            </p>
          </div>
          <div>
            <label class="field-label">{{ t("settings.agentForm.unauthorizedReply") }}</label>
            <Textarea
              v-model="draft.unauthorized_reply"
              :rows="3"
              data-testid="unauthorized-reply-input"
            />
            <p class="mt-1.5 text-xs text-muted-foreground">
              {{ t("settings.agentForm.unauthorizedReplyHelp") }}
            </p>
          </div>
        </div>
        <p v-if="error" class="text-xs text-destructive">{{ error }}</p>
        <div class="flex gap-2">
          <Button type="button" size="sm" :disabled="saving || !draft.name.trim()" @click="saveBot">
            {{ t("common.save") }}
          </Button>
          <Button type="button" variant="outline" size="sm" @click="cancelEdit">
            {{ t("common.cancel") }}
          </Button>
        </div>
      </div>
      <p v-else-if="error" class="text-xs text-destructive">{{ error }}</p>
    </div>

    <Dialog v-model:open="channelRulesHelpOpen">
      <DialogScrollContent
        class="max-w-2xl gap-5 selection:bg-primary selection:text-primary-foreground"
      >
        <DialogHeader>
          <DialogTitle>{{ t("settings.agentForm.botConfigHelpTitle") }}</DialogTitle>
          <DialogDescription>
            {{ t("settings.agentForm.botConfigHelpDescription") }}
          </DialogDescription>
        </DialogHeader>

        <div>
          <h3 class="mb-2 text-sm font-semibold">
            {{ t("settings.agentForm.botConfigFieldsTitle") }}
          </h3>
          <dl class="grid gap-x-5 gap-y-2 text-xs sm:grid-cols-[auto_1fr]">
            <dt class="font-mono text-foreground">name</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldName") }}
            </dd>
            <dt class="font-mono text-foreground">platform</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldPlatform") }}
            </dd>
            <dt class="font-mono text-foreground">enabled</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldEnabled") }}
            </dd>
            <dt class="font-mono text-foreground">model</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldModel") }}
            </dd>
            <dt class="font-mono text-foreground">max_conversation_duration</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldMaxConversationDuration") }}
            </dd>
            <dt class="font-mono text-foreground">credentials</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldCredentials") }}
            </dd>
            <dt class="font-mono text-foreground">connection test</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.botConfigFieldConnectionTest") }}
            </dd>
          </dl>
        </div>

        <div class="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-xs">
          <span class="font-semibold text-foreground">
            {{ t("settings.agentForm.channelsPriorityTitle") }}
          </span>
          <span class="ml-1 text-muted-foreground">
            {{ t("settings.agentForm.channelsPriorityDescription") }}
          </span>
        </div>

        <div
          class="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-3 text-xs"
          data-testid="channel-rules-troubleshooting"
        >
          <h3 class="font-semibold text-foreground">
            {{ t("settings.agentForm.channelsTroubleshootingTitle") }}
          </h3>
          <ul class="mt-2 list-disc space-y-1.5 pl-4 text-muted-foreground">
            <li>{{ t("settings.agentForm.channelsTroubleshootingNoRule") }}</li>
            <li>{{ t("settings.agentForm.channelsTroubleshootingDisabled") }}</li>
            <li>{{ t("settings.agentForm.channelsTroubleshootingMention") }}</li>
            <li>{{ t("settings.agentForm.channelsTroubleshootingAllowlist") }}</li>
            <li>{{ t("settings.agentForm.channelsTroubleshootingBotMessage") }}</li>
          </ul>
          <p class="mt-2 text-foreground/80">
            {{ t("settings.agentForm.channelsTroubleshootingAutoReplyBefore") }}
            <code class="rounded bg-background/80 px-1 py-0.5 text-foreground"
              >"auto_reply": true</code
            >
            {{ t("settings.agentForm.channelsTroubleshootingAutoReplyAfter") }}
          </p>
        </div>

        <pre
          class="overflow-x-auto rounded-md border border-border bg-zinc-950 p-4 text-xs leading-relaxed text-zinc-100"
          data-testid="channel-rules-example"
        ><code>{{ channelRulesExample }}</code></pre>

        <div>
          <h3 class="mb-2 text-sm font-semibold">
            {{ t("settings.agentForm.channelsFieldsTitle") }}
          </h3>
          <dl class="grid gap-x-5 gap-y-2 text-xs sm:grid-cols-[auto_1fr]">
            <dt class="font-mono text-foreground">channel</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsFieldChannel") }}
            </dd>
            <dt class="font-mono text-foreground">enabled</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsFieldEnabled") }}
            </dd>
            <dt class="font-mono text-foreground">require_mention</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsFieldMention") }}
            </dd>
            <dt class="font-mono text-foreground">auto_reply</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsFieldAutoReply") }}
            </dd>
            <dt class="font-mono text-foreground">allowed_user_ids</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsFieldAllowedUsers") }}
            </dd>
            <dt class="font-mono text-foreground">extra_prompt</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsFieldPrompt") }}
            </dd>
            <template v-if="draftMeta.showsBotRelayExample">
              <dt class="font-mono text-foreground">allow_bot_mentions</dt>
              <dd class="text-muted-foreground">
                {{ t("settings.agentForm.channelsFieldBotRelay") }}
              </dd>
              <dt class="font-mono text-foreground">bot_mention_limit</dt>
              <dd class="text-muted-foreground">
                {{ t("settings.agentForm.channelsFieldBotMentionLimit") }}
              </dd>
              <dt class="font-mono text-foreground">bot_mention_window_minutes</dt>
              <dd class="text-muted-foreground">
                {{ t("settings.agentForm.channelsFieldBotMentionWindow") }}
              </dd>
            </template>
          </dl>
        </div>

        <div
          class="space-y-3 rounded-md border border-primary/20 bg-primary/5 p-3"
          data-testid="denial-replies-help"
        >
          <div>
            <h3 class="text-sm font-semibold">
              {{ t("settings.agentForm.channelsReplyFieldsTitle") }}
            </h3>
            <p class="mt-1 text-xs text-muted-foreground">
              {{ t("settings.agentForm.channelsReplyFieldsDescription") }}
            </p>
          </div>
          <dl class="grid gap-x-5 gap-y-2 text-xs sm:grid-cols-[auto_1fr]">
            <dt class="font-mono text-foreground">unconfigured_reply</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsReplyUnconfigured") }}
            </dd>
            <dt class="font-mono text-foreground">unauthorized_reply</dt>
            <dd class="text-muted-foreground">
              {{ t("settings.agentForm.channelsReplyUnauthorized") }}
            </dd>
          </dl>
          <p class="border-t border-primary/15 pt-2 text-xs text-muted-foreground">
            {{ t("settings.agentForm.channelsReplySilentCases") }}
          </p>
        </div>

        <p
          class="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2.5 text-xs text-foreground/80"
        >
          {{ t("settings.agentForm.channelsExampleNote") }}
        </p>
      </DialogScrollContent>
    </Dialog>
  </section>
</template>
