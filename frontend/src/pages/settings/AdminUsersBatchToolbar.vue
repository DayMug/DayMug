<script setup lang="ts">
// Batch-edit toolbar for the checked rows. Purely a picker surface: it holds
// the three draft values and emits one discriminated `apply` payload, leaving
// the requests (and the page's single error line) with the parent.
//
// Laid out as a labelled field grid rather than one wrapping flex row: with
// three control+button pairs a single row reflows into ragged lines at every
// viewport width, and nothing tells the admin which button belongs to which
// picker. The grid pins each action to its own labelled cell instead.
//
// One shared `busy` rather than a flag per button: every action mutates the
// same selection and clears it on success, so letting a second one start
// mid-flight would apply it to a selection that is about to disappear.
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { SandboxMode } from "@/composables/useApi";

export type BatchApply =
  | { kind: "model"; model: string }
  | { kind: "sandbox"; mode: SandboxMode }
  | { kind: "provider"; providerType: string; providerNames: string[] };

const props = defineProps<{
  selectedCount: number;
  modelGroups: { provider: string; models: string[] }[];
  providerTypes: string[];
  providerOptionsByType: Record<string, string[]>;
  typeLabels: Record<string, string>;
  busy: boolean;
}>();

const emit = defineEmits<{ apply: [BatchApply]; clear: [] }>();

const { t } = useI18n();

const model = ref<string>("");
const sandboxMode = ref<SandboxMode>("jailed");
const providerType = ref<string>("claude");
// The first granted account is the default, matching provider_accounts on the
// single-user editor and backend API.
const providerNames = ref<string[]>([]);

const providerOptions = computed<string[]>(
  () => props.providerOptionsByType[providerType.value] ?? [],
);

// Switching CLI type invalidates the account picked under the previous one.
function onProviderTypeChange() {
  providerNames.value = [];
}

function isProviderGranted(name: string): boolean {
  return providerNames.value.includes(name);
}

function isDefaultProvider(name: string): boolean {
  return providerNames.value[0] === name;
}

function toggleProvider(name: string) {
  providerNames.value = isProviderGranted(name)
    ? providerNames.value.filter((n) => n !== name)
    : [...providerNames.value, name];
}

function setDefaultProvider(name: string) {
  if (!isProviderGranted(name)) return;
  providerNames.value = [name, ...providerNames.value.filter((n) => n !== name)];
}

const fieldLabel = "text-[11px] font-medium uppercase tracking-wide text-muted-foreground";
const control = "h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-[13px]";
const applyBtn =
  "h-8 shrink-0 rounded-md bg-primary px-3 text-[13px] font-medium text-primary-foreground cursor-pointer hover:opacity-90 disabled:opacity-50";
</script>

<template>
  <div
    class="mb-3 rounded-md border border-border bg-accent/40 overflow-hidden"
    data-testid="batch-model-toolbar"
  >
    <div class="flex items-center justify-between gap-3 border-b border-border/70 px-3 py-2">
      <span class="text-[12px] font-medium">
        {{ t("settings.adminUsers.batchSelected", { count: selectedCount }) }}
      </span>
      <button
        class="text-[12px] text-muted-foreground hover:text-foreground cursor-pointer"
        @click="emit('clear')"
      >
        {{ t("settings.adminUsers.batchClear") }}
      </button>
    </div>

    <div class="grid gap-x-4 gap-y-3 px-3 py-3 sm:grid-cols-2">
      <div class="flex flex-col gap-1">
        <span :class="fieldLabel">{{ t("settings.adminUsers.defaultModel") }}</span>
        <div class="flex items-center gap-2">
          <select
            v-model="model"
            :aria-label="t('settings.adminUsers.batchApplyModel')"
            data-testid="batch-model-select"
            :class="control"
          >
            <option value="">{{ t("settings.adminUsers.batchClearModelOption") }}</option>
            <optgroup v-for="g in modelGroups" :key="g.provider" :label="g.provider">
              <option v-for="m in g.models" :key="m" :value="m">{{ m }}</option>
            </optgroup>
          </select>
          <button
            :class="applyBtn"
            :disabled="busy"
            data-testid="batch-model-apply"
            @click="emit('apply', { kind: 'model', model })"
          >
            {{ t("settings.adminUsers.batchApply") }}
          </button>
        </div>
      </div>

      <div class="flex flex-col gap-1">
        <span :class="fieldLabel">{{ t("settings.adminUsers.sandboxMode") }}</span>
        <div class="flex items-center gap-2">
          <select
            v-model="sandboxMode"
            :aria-label="t('settings.adminUsers.sandboxMode')"
            data-testid="batch-sandbox-select"
            :class="control"
          >
            <option value="jailed">{{ t("settings.adminUsers.sandboxJailed") }}</option>
            <option value="unrestricted">{{ t("settings.adminUsers.sandboxUnrestricted") }}</option>
          </select>
          <button
            :class="applyBtn"
            :disabled="busy"
            data-testid="batch-sandbox-apply"
            @click="emit('apply', { kind: 'sandbox', mode: sandboxMode })"
          >
            {{ t("settings.adminUsers.batchApply") }}
          </button>
        </div>
      </div>

      <div class="flex flex-col gap-1">
        <span :class="fieldLabel">{{ t("settings.adminUsers.account") }}</span>
        <div class="flex items-center gap-2">
          <select
            v-model="providerType"
            :aria-label="t('settings.adminUsers.batchProviderType')"
            data-testid="batch-provider-type"
            :class="[control, 'basis-0']"
            @change="onProviderTypeChange"
          >
            <option v-for="ptype in providerTypes" :key="'batch-type-' + ptype" :value="ptype">
              {{ typeLabels[ptype] }}
            </option>
          </select>
          <div
            class="min-w-0 basis-0 flex-1 space-y-1 rounded-md border border-border bg-background p-2"
            data-testid="batch-provider-select"
          >
            <div
              v-for="n in providerOptions"
              :key="'batch-provider-' + n"
              class="flex items-center justify-between gap-2 text-[13px]"
            >
              <label class="flex min-w-0 items-center gap-2">
                <input
                  type="checkbox"
                  :data-testid="'batch-provider-account-' + n"
                  :checked="isProviderGranted(n)"
                  @change="toggleProvider(n)"
                />
                <span class="truncate">{{ n }}</span>
              </label>
              <label
                v-if="isProviderGranted(n)"
                class="flex shrink-0 items-center gap-1 text-[11px] text-muted-foreground"
              >
                <input
                  type="radio"
                  name="batch-default-provider"
                  :data-testid="'batch-default-provider-' + n"
                  :checked="isDefaultProvider(n)"
                  @change="setDefaultProvider(n)"
                />
                {{ t("settings.adminUsers.defaultAccount") }}
              </label>
            </div>
            <p v-if="providerOptions.length === 0" class="text-[11px] text-muted-foreground">
              {{ t("common.none") }}
            </p>
          </div>
          <button
            :class="applyBtn"
            :disabled="busy || !providerType"
            data-testid="batch-provider-apply"
            @click="emit('apply', { kind: 'provider', providerType, providerNames })"
          >
            {{ t("settings.adminUsers.batchApply") }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
