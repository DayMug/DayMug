<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowDown, ArrowUp, ChevronDown, Plus, Trash2, X } from "lucide-vue-next";
import { Button } from "@/components/ui/button";
import {
  adminCheckSummaryModel,
  adminFetchModels,
  adminFetchProviders,
  adminFetchTransports,
  adminSaveModels,
  adminSaveProviders,
  adminSaveTransports,
} from "@/composables/useApi";
import type {
  AdminAccountModels,
  AdminAccountModelsEdit,
  AdminProvider,
  AdminSummaryCheck,
  AdminTransport,
  AgentTransport,
} from "@/composables/useApi";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import { invalidateModelRegistry } from "@/composables/useModelRegistry";
import {
  AGENT_FRAMEWORKS,
  accessModeOf,
  frameworkOf,
  isCodexFamily,
  providerTypeFor,
  type AccessMode,
  type AgentFramework,
} from "@/lib/providerTypes";
import {
  formatContextWindow,
  parseContextWindow,
  presetsFor,
  type ModelSpec,
} from "@/lib/providerPresets";
import ProviderAccountStatus from "./ProviderAccountStatus.vue";
import { errorMessage } from "@/lib/errorMessage";

type ProviderRow = { id: number; value: AdminProvider };
type ModelState = {
  source: AdminAccountModels;
  models: string[];
  summaryModel: string;
  customModel: string;
  specs: Record<string, ModelSpec>;
};
type PreservedModelDraft = Pick<ModelState, "models" | "summaryModel" | "specs">;

// Reference ids shown as a hint only. Nothing is pre-filled: which models an
// account serves is entirely the admin's call, and a compiled-in list goes
// stale with every model release.
const MODEL_SUGGESTIONS: Record<string, string[]> = {
  claude: ["claude-opus-5-5", "claude-sonnet-5", "claude-haiku-4-5"],
  codex: ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"],
};

// Ids that are stored and run as another id. The bare Opus ids would pin the
// 200K tier (--model passes them verbatim); the backend applies the same map
// through service.CanonicalModel for callers that bypass this page.
const MODEL_ALIASES: Record<string, string> = {
  "claude-opus-5-5": "claude-opus-5-5[1m]",
  "claude-opus-5": "claude-opus-5[1m]",
  "gpt-5.6-sole": "gpt-5.6-sol",
};

const { t } = useI18n();
const providers = ref<ProviderRow[]>([]);
const modelStates = ref<Record<number, ModelState>>({});
const providerBaseline = ref("");
// Each row as the server last saw it, keyed by row id. Checking or logging in
// an account runs against the saved settings, so a row with pending edits
// can't be tested until it is saved.
const savedRows = ref<Record<number, string>>({});
const transports = ref<Record<string, AdminTransport>>({});
const transportDrafts = ref<Record<string, AgentTransport>>({});
const loading = ref(false);
// Keyed by row id. A result is shown only while the field still holds the
// model it tested, so editing the input can't leave a stale verdict beside it.
const summaryChecks = ref<Record<number, { busy: boolean; result?: AdminSummaryCheck }>>({});
let nextRowID = 1;

const { busy: saving, error, message, run, reset } = useAsyncOperation();

function normalizeProvider(row: AdminProvider): AdminProvider {
  return {
    name: row.name ?? "",
    type: row.type ?? "claude",
    max_concurrent: row.max_concurrent || 1,
    config_dir: row.config_dir ?? "",
    env: { ...row.env },
  };
}

function providerPayload(): AdminProvider[] {
  return providers.value.map((row) => row.value);
}

function serializeProviders(): string {
  return JSON.stringify(providerPayload());
}

function hydrateProviders(rows: AdminProvider[], preserveRows = false) {
  providers.value = rows.map((provider, index) => {
    const existing = preserveRows ? providers.value[index] : undefined;
    return {
      id: existing?.id ?? nextRowID++,
      value: normalizeProvider(provider),
    };
  });
  providerBaseline.value = serializeProviders();
  savedRows.value = Object.fromEntries(
    providers.value.map((row) => [row.id, JSON.stringify(row.value)]),
  );
}

function rowUnsaved(row: ProviderRow): boolean {
  return savedRows.value[row.id] !== JSON.stringify(row.value);
}

function normalizeModel(model: string): string {
  const trimmed = model.trim();
  return MODEL_ALIASES[trimmed] ?? trimmed;
}

function uniqueModels(models: string[]): string[] {
  return [...new Set(models.map(normalizeModel).filter(Boolean))];
}

function normalizeSpecs(specs: Record<string, ModelSpec> | undefined): Record<string, ModelSpec> {
  const out: Record<string, ModelSpec> = {};
  for (const [model, spec] of Object.entries(specs ?? {})) {
    const clean: ModelSpec = {};
    if (spec.context_window) clean.context_window = spec.context_window;
    if (spec.no_image_input) clean.no_image_input = true;
    if (Object.keys(clean).length) out[normalizeModel(model)] = clean;
  }
  return out;
}

function newModelState(source: AdminAccountModels): ModelState {
  return {
    source,
    models: uniqueModels(source.models),
    summaryModel: normalizeModel(source.summary_model),
    customModel: "",
    specs: normalizeSpecs(source.specs),
  };
}

function hydrateModels(
  accounts: AdminAccountModels[],
  preserved: Record<number, PreservedModelDraft> = {},
) {
  const byAccount = new Map(accounts.map((account) => [account.account, account]));
  const next: Record<number, ModelState> = {};
  for (const row of providers.value) {
    const source = byAccount.get(row.value.name);
    if (!source) continue;
    next[row.id] = { ...newModelState(source), ...preserved[row.id] };
  }
  modelStates.value = next;
}

function hydrateTransports(rows: AdminTransport[]) {
  transports.value = Object.fromEntries(rows.map((row) => [row.provider, row]));
  transportDrafts.value = Object.fromEntries(rows.map((row) => [row.provider, row.transport]));
}

function stateFor(row: ProviderRow): ModelState | undefined {
  return modelStates.value[row.id];
}

function modelStateDirty(state: ModelState): boolean {
  const stored = uniqueModels(state.source.models);
  if (
    state.models.length !== stored.length ||
    state.models.some((model, index) => model !== stored[index])
  ) {
    return true;
  }
  if (normalizeModel(state.summaryModel) !== normalizeModel(state.source.summary_model))
    return true;
  return specsKey(servedSpecs(state)) !== specsKey(normalizeSpecs(state.source.specs));
}

// Specs only count for models the account still serves, matching what the
// server keeps on save.
function servedSpecs(state: ModelState): Record<string, ModelSpec> {
  const served = new Set([...state.models, normalizeModel(state.summaryModel)]);
  return normalizeSpecs(
    Object.fromEntries(Object.entries(state.specs).filter(([model]) => served.has(model))),
  );
}

function specsKey(specs: Record<string, ModelSpec>): string {
  return JSON.stringify(Object.entries(specs).sort(([a], [b]) => a.localeCompare(b)));
}

// Raw text of context-window inputs the admin is editing, keyed by row and
// model, so "12" on the way to "128K" isn't reformatted under the cursor.
const windowDrafts = ref<Record<string, string>>({});

function windowKey(row: ProviderRow, model: string): string {
  return `${row.id}\n${model}`;
}

function windowText(row: ProviderRow, model: string): string {
  return (
    windowDrafts.value[windowKey(row, model)] ??
    formatContextWindow(stateFor(row)?.specs[model]?.context_window)
  );
}

function windowInvalid(row: ProviderRow, model: string): boolean {
  const draft = windowDrafts.value[windowKey(row, model)];
  return draft !== undefined && parseContextWindow(draft) === null;
}

function setWindow(row: ProviderRow, model: string, text: string) {
  const state = stateFor(row);
  if (!state) return;
  windowDrafts.value[windowKey(row, model)] = text;
  const tokens = parseContextWindow(text);
  if (tokens === null) return;
  const spec = { ...state.specs[model] };
  if (tokens) spec.context_window = tokens;
  else delete spec.context_window;
  state.specs[model] = spec;
}

function settleWindow(row: ProviderRow, model: string) {
  if (!windowInvalid(row, model)) delete windowDrafts.value[windowKey(row, model)];
}

function acceptsImages(state: ModelState, model: string): boolean {
  return !state.specs[model]?.no_image_input;
}

function setAcceptsImages(state: ModelState, model: string, accepts: boolean) {
  const spec = { ...state.specs[model] };
  if (accepts) delete spec.no_image_input;
  else spec.no_image_input = true;
  state.specs[model] = spec;
}

function changedTransports(): Record<string, AgentTransport> {
  const changed: Record<string, AgentTransport> = {};
  for (const [provider, transport] of Object.entries(transportDrafts.value)) {
    if (transports.value[provider]?.transport !== transport) changed[provider] = transport;
  }
  return changed;
}

const providersDirty = computed(() => serializeProviders() !== providerBaseline.value);
const modelsDirty = computed(() => Object.values(modelStates.value).some(modelStateDirty));
const transportsDirty = computed(() => Object.keys(changedTransports()).length > 0);
// An unparseable context window counts as a change so Save stays clickable and
// reports the problem instead of silently doing nothing.
const windowDraftInvalid = computed(() =>
  Object.values(windowDrafts.value).some((text) => parseContextWindow(text) === null),
);
const dirty = computed(
  () =>
    providersDirty.value || modelsDirty.value || transportsDirty.value || windowDraftInvalid.value,
);

function suggestionsFor(provider: string): string[] {
  return MODEL_SUGGESTIONS[provider] ?? [];
}

function transportOptions(provider: string): AgentTransport[] {
  return transports.value[provider]?.options ?? [];
}

function moveModel(state: ModelState, index: number, delta: number) {
  const target = index + delta;
  if (target < 0 || target >= state.models.length) return;
  const [model] = state.models.splice(index, 1);
  if (model) state.models.splice(target, 0, model);
}

function removeModel(state: ModelState, index: number) {
  state.models.splice(index, 1);
}

function addCustomModel(row: ProviderRow) {
  const state = stateFor(row);
  if (!state) return;
  state.models = uniqueModels([...state.models, state.customModel]);
  state.customModel = "";
}

function missingSummary(state: ModelState): boolean {
  return state.models.length > 0 && !state.summaryModel.trim();
}

function summaryResult(row: ProviderRow): AdminSummaryCheck | undefined {
  const result = summaryChecks.value[row.id]?.result;
  const state = stateFor(row);
  return result && state && result.model === normalizeModel(state.summaryModel)
    ? result
    : undefined;
}

// Runs against the account as saved on the server (credentials, transport),
// but on the model currently in the field, so a typo is caught before saving.
async function testSummaryModel(row: ProviderRow) {
  const state = stateFor(row);
  const model = state && normalizeModel(state.summaryModel);
  if (!model || rowUnsaved(row)) return;
  summaryChecks.value[row.id] = { busy: true };
  let result: AdminSummaryCheck;
  try {
    result = await adminCheckSummaryModel(row.value.name, model);
  } catch (e) {
    result = { account: row.value.name, model, ok: false, error: errorMessage(e) };
  }
  summaryChecks.value[row.id] = { busy: false, result };
}

function apiFields(provider: AdminProvider): { url: string; key: string } | undefined {
  if (provider.type === "claude-compatible") {
    const keys = ["ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"];
    return {
      url: "ANTHROPIC_BASE_URL",
      key:
        keys.find((key) => provider.env[key]) ??
        keys.find((key) => key in provider.env) ??
        keys[0]!,
    };
  }
  if (provider.type === "openai-compatible") {
    return { url: "OPENAI_BASE_URL", key: "OPENAI_API_KEY" };
  }
}

function managedEnvKeys(provider: AdminProvider): string[] {
  const fields = apiFields(provider);
  if (!fields) return [];
  return provider.type === "claude-compatible"
    ? [fields.url, "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"]
    : [fields.url, fields.key];
}

function setApiField(provider: AdminProvider, key: string, value: string) {
  // An explicit empty value also masks credentials inherited from the service.
  provider.env[key] = value;
  if (key === "ANTHROPIC_AUTH_TOKEN") provider.env.ANTHROPIC_API_KEY = "";
  if (key === "ANTHROPIC_API_KEY") provider.env.ANTHROPIC_AUTH_TOKEN = "";
}

function envText(provider: AdminProvider): string {
  const managed = managedEnvKeys(provider);
  return Object.entries(provider.env)
    .filter(([key]) => !managed.includes(key))
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
}

function setEnv(provider: AdminProvider, value: string) {
  const managed = managedEnvKeys(provider);
  const env: Record<string, string> = Object.fromEntries(
    Object.entries(provider.env).filter(([key]) => managed.includes(key)),
  );
  for (const line of value.split("\n")) {
    const separator = line.indexOf("=");
    if (separator <= 0) continue;
    const key = line.slice(0, separator).trim();
    if (key) env[key] = line.slice(separator + 1);
  }
  provider.env = env;
}

function setFramework(provider: AdminProvider, framework: AgentFramework) {
  provider.type = providerTypeFor(framework, accessModeOf(provider.type));
}

function setAccessMode(provider: AdminProvider, access: AccessMode) {
  provider.type = providerTypeFor(frameworkOf(provider.type), access);
}

function frameworkLabel(type: string): string {
  return AGENT_FRAMEWORKS.find((f) => f.id === frameworkOf(type))?.label ?? type;
}

// The "add account" panel: pick the framework, then how it authenticates,
// then (for API keys) the vendor. Everything it fills in is a page draft —
// nothing is saved or sent until the admin presses Save.
const adding = ref(false);
const addFramework = ref<AgentFramework>("claude-code");
const addAccess = ref<AccessMode>("login");
const addPresetID = ref("custom");
const addPresets = computed(() => presetsFor(addFramework.value));

function openAddPanel() {
  adding.value = true;
  if (!addPresets.value.some((preset) => preset.id === addPresetID.value))
    addPresetID.value = "custom";
}

function pickAddFramework(framework: AgentFramework) {
  addFramework.value = framework;
  if (!addPresets.value.some((preset) => preset.id === addPresetID.value))
    addPresetID.value = "custom";
}

function uniqueAccountName(base: string): string {
  let name = base;
  let suffix = 2;
  while (providers.value.some((row) => row.value.name === name)) name = `${base}-${suffix++}`;
  return name;
}

function addAccount() {
  const type = providerTypeFor(addFramework.value, addAccess.value);
  const preset =
    addAccess.value === "api-key"
      ? addPresets.value.find((candidate) => candidate.id === addPresetID.value)
      : undefined;
  const endpoint = preset?.endpoints[addFramework.value];
  const base = preset ? preset.accountName : addFramework.value === "codex" ? "codex" : "claude";
  const row: ProviderRow = {
    id: nextRowID++,
    value: {
      name: uniqueAccountName(base),
      type,
      max_concurrent: 1,
      config_dir: "",
      env: {},
    },
  };
  const fields = apiFields(row.value);
  if (fields) {
    row.value.env = { ...endpoint?.env };
    setApiField(row.value, fields.url, endpoint?.baseUrl ?? "");
    setApiField(row.value, fields.key, "");
  }
  providers.value.push(row);
  if (endpoint && endpoint.models.length) {
    modelStates.value[row.id] = {
      ...newModelState({
        account: row.value.name,
        provider: type,
        models: [],
        summary_model: "",
        specs: {},
        overridden: false,
      }),
      models: uniqueModels(endpoint.models.map((model) => model.id)),
      summaryModel: endpoint.summaryModel,
      specs: normalizeSpecs(
        Object.fromEntries(endpoint.models.map((model) => [model.id, model.spec ?? {}])),
      ),
    };
  }
  adding.value = false;
}

function removeProvider(index: number) {
  const [removed] = providers.value.splice(index, 1);
  if (removed) delete modelStates.value[removed.id];
}

function move(index: number, delta: number) {
  const target = index + delta;
  if (target < 0 || target >= providers.value.length) return;
  const [row] = providers.value.splice(index, 1);
  if (row) providers.value.splice(target, 0, row);
}

async function load() {
  reset();
  loading.value = true;
  try {
    const [providerResponse, modelResponse, transportResponse] = await Promise.all([
      adminFetchProviders(),
      adminFetchModels(),
      adminFetchTransports(),
    ]);
    hydrateProviders(providerResponse.providers);
    hydrateModels(modelResponse.accounts);
    hydrateTransports(transportResponse.transports);
  } catch (e) {
    error.value = errorMessage(e);
  } finally {
    loading.value = false;
  }
}

function modelPayload(): Record<string, AdminAccountModelsEdit> {
  const payload: Record<string, AdminAccountModelsEdit> = {};
  for (const row of providers.value) {
    const state = stateFor(row);
    if (!state) continue;
    payload[row.value.name] = {
      models: [...state.models],
      summary_model: normalizeModel(state.summaryModel),
      specs: servedSpecs(state),
    };
  }
  return payload;
}

async function save() {
  reset();
  const incomplete = providers.value.find((row) => {
    const state = stateFor(row);
    return state && missingSummary(state);
  });
  if (incomplete) {
    error.value = t("settings.adminGlobal.modelsSummaryRequired", {
      account: incomplete.value.name,
    });
    return;
  }
  const badWindow = Object.entries(windowDrafts.value).find(
    ([, text]) => parseContextWindow(text) === null,
  );
  if (badWindow) {
    error.value = t("settings.adminModels.contextWindowInvalid", { value: badWindow[1] });
    return;
  }
  const preserved = Object.fromEntries(
    providers.value.flatMap((row) => {
      const state = stateFor(row);
      return state && modelStateDirty(state)
        ? [
            [
              row.id,
              { models: [...state.models], summaryModel: state.summaryModel, specs: state.specs },
            ],
          ]
        : [];
    }),
  ) as Record<number, PreservedModelDraft>;
  const shouldSaveModels = Object.keys(preserved).length > 0;
  const transportChanges = changedTransports();

  await run(async () => {
    if (providersDirty.value) {
      const response = await adminSaveProviders(providerPayload());
      hydrateProviders(response.providers, true);
      hydrateModels((await adminFetchModels()).accounts, preserved);
    }

    if (shouldSaveModels) {
      hydrateModels((await adminSaveModels(modelPayload())).accounts);
    }

    if (Object.keys(transportChanges).length > 0) {
      hydrateTransports((await adminSaveTransports(transportChanges)).transports);
    }

    windowDrafts.value = {};
    invalidateModelRegistry();
    message.value = t("settings.adminModels.providersModelsSaved");
  });
}

onMounted(load);
</script>

<template>
  <section data-testid="provider-editor">
    <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 class="text-[14px] font-semibold">{{ t("settings.adminModels.providersTitle") }}</h2>
        <p class="mt-1 text-[12px] text-muted-foreground">
          {{ t("settings.adminModels.providersIntro") }}
        </p>
      </div>
      <div class="flex shrink-0 flex-wrap justify-end gap-2">
        <Button
          size="sm"
          variant="outline"
          class="gap-1.5"
          data-testid="add-provider"
          :disabled="loading || adding"
          @click="openAddPanel"
        >
          <Plus class="size-3.5" />
          {{ t("settings.adminModels.addProvider") }}
        </Button>
      </div>
    </div>

    <div v-if="adding" class="provider-card mb-3 p-3" data-testid="add-account-panel">
      <h3 class="text-[13px] font-semibold">{{ t("settings.adminModels.add.title") }}</h3>

      <p class="field-kicker mt-3">{{ t("settings.adminModels.add.framework") }}</p>
      <div class="grid gap-2 sm:grid-cols-2">
        <button
          v-for="framework in AGENT_FRAMEWORKS"
          :key="framework.id"
          type="button"
          class="choice"
          :class="{ 'choice-active': addFramework === framework.id }"
          :aria-pressed="addFramework === framework.id"
          :data-testid="`add-framework-${framework.id}`"
          @click="pickAddFramework(framework.id)"
        >
          <span class="text-[13px] font-medium">{{ framework.label }}</span>
          <span class="text-[11px] text-muted-foreground">
            {{ t(`settings.adminModels.frameworks.${framework.id}`) }}
          </span>
        </button>
      </div>

      <p class="field-kicker mt-3">{{ t("settings.adminModels.add.access") }}</p>
      <div class="grid gap-2 sm:grid-cols-2">
        <button
          v-for="access in ['login', 'api-key'] as const"
          :key="access"
          type="button"
          class="choice"
          :class="{ 'choice-active': addAccess === access }"
          :aria-pressed="addAccess === access"
          :data-testid="`add-access-${access}`"
          @click="addAccess = access"
        >
          <span class="text-[13px] font-medium">{{
            t(`settings.adminModels.access.${access}`)
          }}</span>
          <span class="text-[11px] text-muted-foreground">
            {{ t(`settings.adminModels.accessHints.${addFramework}.${access}`) }}
          </span>
        </button>
      </div>

      <label v-if="addAccess === 'api-key'" class="field-label mt-3">
        <span>{{ t("settings.adminModels.add.vendor") }}</span>
        <select v-model="addPresetID" class="field-input" data-testid="add-vendor">
          <option v-for="preset in addPresets" :key="preset.id" :value="preset.id">
            {{ preset.label || t("settings.adminModels.add.customVendor") }}
          </option>
        </select>
      </label>
      <p v-if="addAccess === 'api-key'" class="mt-1 text-[10px] text-muted-foreground">
        {{ t("settings.adminModels.add.vendorHint") }}
      </p>

      <div class="mt-3 flex gap-2">
        <Button size="sm" data-testid="add-confirm" @click="addAccount">
          {{ t("settings.adminModels.add.confirm") }}
        </Button>
        <Button size="sm" variant="ghost" @click="adding = false">
          {{ t("common.cancel") }}
        </Button>
      </div>
    </div>

    <p v-if="loading" class="text-[12px] text-muted-foreground">{{ t("common.loading") }}</p>
    <p
      v-else-if="providers.length === 0"
      class="rounded-lg border border-dashed border-border px-4 py-5 text-center text-[12px] text-muted-foreground"
      data-testid="providers-empty"
    >
      {{ t("settings.adminModels.providersEmpty") }}
    </p>

    <div v-else class="space-y-2">
      <details
        v-for="(row, index) in providers"
        :key="row.id"
        class="provider-card group"
        :open="index === 0"
        :data-testid="`provider-row-${index}`"
      >
        <summary class="flex cursor-pointer list-none flex-wrap items-center gap-2 px-3 py-3">
          <ChevronDown
            class="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180"
          />
          <span class="min-w-0 flex-1 truncate font-mono text-[13px]">
            {{ row.value.name || t("settings.adminModels.unnamedProvider") }}
          </span>
          <span
            v-if="index === 0"
            class="rounded-full bg-primary/10 px-2 py-0.5 text-[10px] font-medium text-primary"
          >
            {{ t("settings.adminModels.defaultProvider") }}
          </span>
          <span class="rounded bg-accent px-1.5 py-0.5 text-[10px]" :title="row.value.type">
            {{ frameworkLabel(row.value.type) }} ·
            {{ t(`settings.adminModels.access.${accessModeOf(row.value.type)}`) }}
          </span>
          <span v-if="stateFor(row)" class="text-[11px] text-muted-foreground">
            {{ t("settings.adminGlobal.modelsCount", { count: stateFor(row)!.models.length }) }}
          </span>
          <span class="text-[11px] text-muted-foreground">×{{ row.value.max_concurrent }}</span>
        </summary>

        <div class="border-t border-border/70 px-3 pb-3 pt-3">
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="field-label">
              <span>{{ t("settings.adminModels.providerName") }}</span>
              <input v-model="row.value.name" class="field-input font-mono" spellcheck="false" />
            </label>
            <label class="field-label">
              <span>{{ t("settings.adminModels.framework") }}</span>
              <select
                :value="frameworkOf(row.value.type)"
                class="field-input"
                :data-testid="`framework-${index}`"
                @change="
                  setFramework(
                    row.value,
                    ($event.target as HTMLSelectElement).value as AgentFramework,
                  )
                "
              >
                <option
                  v-for="framework in AGENT_FRAMEWORKS"
                  :key="framework.id"
                  :value="framework.id"
                >
                  {{ framework.label }}
                </option>
              </select>
            </label>
            <label class="field-label">
              <span>{{ t("settings.adminModels.accessMode") }}</span>
              <select
                :value="accessModeOf(row.value.type)"
                class="field-input"
                :data-testid="`access-${index}`"
                @change="
                  setAccessMode(row.value, ($event.target as HTMLSelectElement).value as AccessMode)
                "
              >
                <option value="login">{{ t("settings.adminModels.access.login") }}</option>
                <option value="api-key">{{ t("settings.adminModels.access.api-key") }}</option>
              </select>
            </label>
            <label class="field-label">
              <span>{{ t("settings.adminModels.transport") }}</span>
              <select
                v-if="transportOptions(row.value.type).length > 1"
                v-model="transportDrafts[row.value.type]"
                class="field-input"
                :data-testid="`transport-${index}`"
              >
                <option
                  v-for="option in transportOptions(row.value.type)"
                  :key="option"
                  :value="option"
                >
                  {{ t(`settings.adminModels.transports.${option}`) }}
                </option>
              </select>
              <span
                v-else-if="transportOptions(row.value.type).length === 1"
                class="field-input text-muted-foreground"
                :data-testid="`transport-${index}`"
              >
                {{ t(`settings.adminModels.transports.${transportOptions(row.value.type)[0]}`) }}
              </span>
            </label>
            <label class="field-label">
              <span>{{ t("settings.adminModels.providerConcurrency") }}</span>
              <input
                v-model.number="row.value.max_concurrent"
                type="number"
                min="1"
                class="field-input"
              />
            </label>
          </div>

          <p
            v-if="transportDrafts[row.value.type]"
            class="mt-1 text-[10px] text-muted-foreground"
            :data-testid="`transport-hint-${index}`"
          >
            {{
              t(`settings.adminModels.transportHints.${transportDrafts[row.value.type]}`, {
                type: row.value.type,
              })
            }}
          </p>

          <label class="field-label mt-3">
            <span>{{ t("settings.adminModels.configDir") }}</span>
            <input
              v-model="row.value.config_dir"
              class="field-input font-mono"
              spellcheck="false"
              :placeholder="isCodexFamily(row.value.type) ? '~/.codex' : '~/.claude'"
            />
          </label>
          <p
            v-if="isCodexFamily(row.value.type)"
            class="mt-1 text-[10px] text-muted-foreground"
            :data-testid="`codex-home-hint-${index}`"
          >
            {{ t("settings.adminModels.codexHomeRequired") }}
          </p>
          <div
            v-if="apiFields(row.value)"
            class="mt-3 rounded-lg border border-border p-3"
            :data-testid="`api-settings-${index}`"
          >
            <div class="grid gap-3 sm:grid-cols-2">
              <label class="field-label">
                <span>{{ t("settings.adminModels.apiBaseUrl") }}</span>
                <input
                  :value="row.value.env[apiFields(row.value)!.url] ?? ''"
                  class="field-input font-mono"
                  type="url"
                  spellcheck="false"
                  :data-testid="`api-url-${index}`"
                  @input="
                    setApiField(
                      row.value,
                      apiFields(row.value)!.url,
                      ($event.target as HTMLInputElement).value.trim(),
                    )
                  "
                />
              </label>
              <label class="field-label">
                <span>{{ t("settings.adminModels.apiKey") }}</span>
                <input
                  :value="row.value.env[apiFields(row.value)!.key] ?? ''"
                  class="field-input font-mono"
                  type="password"
                  autocomplete="new-password"
                  spellcheck="false"
                  :data-testid="`api-key-${index}`"
                  @input="
                    setApiField(
                      row.value,
                      apiFields(row.value)!.key,
                      ($event.target as HTMLInputElement).value,
                    )
                  "
                />
              </label>
            </div>
            <p class="mt-2 text-[11px] text-muted-foreground">
              {{
                t(
                  row.value.type === "claude-compatible"
                    ? "settings.adminModels.anthropicApiHint"
                    : "settings.adminModels.responsesApiHint",
                )
              }}
            </p>
          </div>
          <label class="field-label mt-3">
            <span>{{ t("settings.adminModels.environment") }}</span>
            <textarea
              :value="envText(row.value)"
              rows="3"
              class="field-input font-mono leading-5"
              spellcheck="false"
              placeholder="KEY=value"
              @input="setEnv(row.value, ($event.target as HTMLTextAreaElement).value)"
            />
          </label>
          <p class="mt-1 text-[10px] text-muted-foreground">
            {{ t("settings.adminModels.environmentHint") }}
          </p>

          <section class="mt-4 border-t border-border/70 pt-4">
            <div class="mb-3">
              <h3 class="text-[13px] font-semibold">{{ t("settings.adminGlobal.modelsTitle") }}</h3>
              <p class="mt-1 text-[11px] text-muted-foreground">
                {{ t("settings.adminGlobal.modelsIntro") }}
              </p>
            </div>

            <template v-if="stateFor(row)">
              <label class="field-kicker">{{ t("settings.adminGlobal.modelsList") }}</label>
              <p class="mb-2 text-[10px] text-muted-foreground">
                {{ t("settings.adminModels.modelSpecsHint") }}
              </p>
              <ol
                v-if="stateFor(row)!.models.length"
                class="mb-2 space-y-1"
                :data-testid="`model-list-${row.value.name}`"
              >
                <li
                  v-for="(model, modelIndex) in stateFor(row)!.models"
                  :key="model"
                  class="flex items-center gap-1.5 rounded-md border border-border/70 bg-background px-2 py-1"
                  :data-model-value="model"
                >
                  <span class="min-w-0 flex-1 truncate font-mono text-[12px]">{{ model }}</span>
                  <span
                    v-if="modelIndex === 0"
                    class="shrink-0 rounded-full bg-primary/10 px-2 py-0.5 text-[10px] font-medium text-primary"
                  >
                    {{ t("settings.adminGlobal.modelsDefault") }}
                  </span>
                  <input
                    :value="windowText(row, model)"
                    class="field-input window-input"
                    :class="{ 'field-invalid': windowInvalid(row, model) }"
                    spellcheck="false"
                    :placeholder="t('settings.adminModels.contextWindowAuto')"
                    :title="t('settings.adminModels.contextWindow')"
                    :aria-label="t('settings.adminModels.contextWindowFor', { model })"
                    :aria-invalid="windowInvalid(row, model)"
                    data-testid="model-context-window"
                    @input="setWindow(row, model, ($event.target as HTMLInputElement).value)"
                    @blur="settleWindow(row, model)"
                  />
                  <label
                    class="flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground"
                    :title="t('settings.adminModels.imageInputHint')"
                  >
                    <input
                      type="checkbox"
                      :checked="acceptsImages(stateFor(row)!, model)"
                      data-testid="model-image-input"
                      @change="
                        setAcceptsImages(
                          stateFor(row)!,
                          model,
                          ($event.target as HTMLInputElement).checked,
                        )
                      "
                    />
                    {{ t("settings.adminModels.imageInput") }}
                  </label>
                  <Button
                    size="icon"
                    variant="ghost"
                    class="size-7"
                    :disabled="modelIndex === 0"
                    :aria-label="t('settings.adminGlobal.modelsMoveUp')"
                    data-testid="model-move-up"
                    @click="moveModel(stateFor(row)!, modelIndex, -1)"
                  >
                    <ArrowUp class="size-3.5" />
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    class="size-7"
                    :disabled="modelIndex === stateFor(row)!.models.length - 1"
                    :aria-label="t('settings.adminGlobal.modelsMoveDown')"
                    data-testid="model-move-down"
                    @click="moveModel(stateFor(row)!, modelIndex, 1)"
                  >
                    <ArrowDown class="size-3.5" />
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    class="size-7 text-muted-foreground hover:text-destructive"
                    :aria-label="t('settings.adminGlobal.modelsRemove')"
                    data-testid="model-remove"
                    @click="removeModel(stateFor(row)!, modelIndex)"
                  >
                    <X class="size-3.5" />
                  </Button>
                </li>
              </ol>
              <p
                v-else
                class="mb-2 rounded-md border border-dashed border-border px-3 py-2 text-[11px] text-muted-foreground"
                :data-testid="`models-empty-${row.value.name}`"
              >
                {{ t("settings.adminGlobal.modelsNone") }}
              </p>

              <form class="mb-1 flex gap-2" @submit.prevent="addCustomModel(row)">
                <input
                  v-model="modelStates[row.id].customModel"
                  type="text"
                  spellcheck="false"
                  :placeholder="t('settings.adminGlobal.modelsCustomPlaceholder')"
                  class="field-input min-w-0 flex-1 font-mono"
                  :data-testid="`custom-model-${row.value.name}`"
                />
                <Button type="submit" variant="outline" size="sm" class="h-9 gap-1.5 px-3">
                  <Plus class="size-3.5" />
                  {{ t("settings.adminGlobal.modelsAdd") }}
                </Button>
              </form>
              <p
                class="text-[10px] text-muted-foreground"
                :data-testid="`model-suggestions-${index}`"
              >
                {{
                  suggestionsFor(row.value.type).length
                    ? t("settings.adminGlobal.modelsSuggestions", {
                        models: suggestionsFor(row.value.type).join(", "),
                      })
                    : t("settings.adminGlobal.modelsSuggestionsEndpoint")
                }}
              </p>
              <p v-if="row.value.type === 'claude'" class="text-[10px] text-muted-foreground">
                {{ t("settings.adminGlobal.modelsOpusAlias") }}
              </p>

              <label class="field-kicker mt-3">{{ t("settings.adminGlobal.modelsSummary") }}</label>
              <div class="flex gap-2">
                <input
                  v-model="modelStates[row.id].summaryModel"
                  type="text"
                  spellcheck="false"
                  :placeholder="t('settings.adminGlobal.modelsSummaryPlaceholder')"
                  class="field-input min-w-0 flex-1 font-mono"
                  :class="{
                    'field-invalid':
                      missingSummary(modelStates[row.id]) || summaryResult(row)?.ok === false,
                  }"
                  :aria-invalid="missingSummary(modelStates[row.id])"
                  :data-testid="`summary-model-${row.value.name}`"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  class="h-9 px-3"
                  :disabled="
                    summaryChecks[row.id]?.busy ||
                    rowUnsaved(row) ||
                    !modelStates[row.id].summaryModel.trim()
                  "
                  :title="
                    rowUnsaved(row)
                      ? t('settings.adminGlobal.modelsSummaryTestSaveFirst')
                      : undefined
                  "
                  :data-testid="`summary-test-${row.value.name}`"
                  @click="testSummaryModel(row)"
                >
                  {{
                    summaryChecks[row.id]?.busy
                      ? t("settings.adminGlobal.modelsSummaryTesting")
                      : t("settings.adminGlobal.modelsSummaryTest")
                  }}
                </Button>
              </div>
              <p
                v-if="summaryResult(row)"
                class="mt-1 break-words text-[11px]"
                :class="summaryResult(row)!.ok ? 'text-emerald-600' : 'text-red-600'"
                :data-testid="`summary-test-result-${row.value.name}`"
              >
                {{
                  summaryResult(row)!.ok
                    ? t("settings.adminGlobal.modelsSummaryTestOk", {
                        title: summaryResult(row)!.title,
                        ms: summaryResult(row)!.latency_ms ?? 0,
                      })
                    : t("settings.adminGlobal.modelsSummaryTestFailed", {
                        error: summaryResult(row)!.error,
                      })
                }}
              </p>
              <p class="mt-1 text-[10px] text-muted-foreground">
                {{ t("settings.adminGlobal.modelsSummaryHint") }}
              </p>
            </template>
            <p v-else class="text-[11px] text-muted-foreground" data-testid="models-pending-save">
              {{ t("settings.adminModels.modelsPendingSave") }}
            </p>
          </section>

          <ProviderAccountStatus
            :account="row.value.name"
            :type="row.value.type"
            :disabled="rowUnsaved(row)"
          />

          <div class="mt-3 flex items-center justify-between gap-2">
            <div class="flex gap-1">
              <Button
                size="icon"
                variant="ghost"
                :disabled="index === 0"
                :aria-label="t('settings.adminModels.moveUp')"
                @click="move(index, -1)"
              >
                <ArrowUp class="size-3.5" />
              </Button>
              <Button
                size="icon"
                variant="ghost"
                :disabled="index === providers.length - 1"
                :aria-label="t('settings.adminModels.moveDown')"
                @click="move(index, 1)"
              >
                <ArrowDown class="size-3.5" />
              </Button>
            </div>
            <Button
              size="sm"
              variant="ghost"
              class="gap-1.5 text-destructive hover:text-destructive"
              @click="removeProvider(index)"
            >
              <Trash2 class="size-3.5" />
              {{ t("common.delete") }}
            </Button>
          </div>
        </div>
      </details>
    </div>

    <div :class="{ savebar: dirty }" class="mt-3 flex flex-wrap items-center gap-2">
      <Button size="sm" :disabled="saving || !dirty" data-testid="save-providers" @click="save">
        {{ saving ? t("common.saving") : t("common.save") }}
      </Button>
      <span v-if="dirty && !saving" class="text-[11px] text-muted-foreground">
        {{ t("settings.adminGlobal.unsavedChanges") }}
      </span>
    </div>
    <p v-if="message" class="mt-2 text-[12px] text-muted-foreground">{{ message }}</p>
    <p v-if="error" class="mt-2 text-[12px] text-red-600">{{ error }}</p>
  </section>
</template>

<style scoped>
.provider-card {
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--card);
}
.field-label {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  font-size: 11px;
  letter-spacing: 0.04em;
  color: var(--muted-foreground);
}
.field-kicker {
  display: block;
  margin-bottom: 0.25rem;
  color: var(--muted-foreground);
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
.field-input {
  width: 100%;
  border: 1px solid var(--border);
  border-radius: 7px;
  background: var(--background);
  padding: 0.5rem 0.625rem;
  color: var(--foreground);
  font-size: 12px;
  letter-spacing: normal;
  outline: none;
}
.field-input:focus {
  box-shadow: 0 0 0 1px var(--ring);
}
.window-input {
  width: 5.5rem;
  flex: none;
  padding: 0.25rem 0.5rem;
  font-family: var(--font-mono, ui-monospace, monospace);
}
.choice {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 0.5rem 0.75rem;
  text-align: left;
  background: var(--background);
}
.choice-active {
  border-color: var(--primary);
  box-shadow: 0 0 0 1px var(--primary);
}
.field-invalid {
  border-color: var(--destructive);
}
.savebar {
  position: sticky;
  bottom: 0.75rem;
  z-index: 5;
  width: fit-content;
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 0.5rem;
  background: color-mix(in srgb, var(--background) 88%, transparent);
  box-shadow: 0 8px 24px color-mix(in srgb, var(--foreground) 8%, transparent);
  backdrop-filter: blur(12px);
}
</style>
