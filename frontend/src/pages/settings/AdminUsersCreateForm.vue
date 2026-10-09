<script setup lang="ts">
// New-user form. Lives inline on the list page (no separate route) so the
// admin keeps the list context — same pattern as the agent UI.
//
// Owns its own draft: the parent only learns about it via `submit`, which
// carries an already-normalised payload. That keeps a cancelled form from
// leaving half-typed state behind on the page.
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { AdminCreateUserData } from "@/composables/useApi";
import DirPicker from "@/components/DirPicker.vue";

const props = defineProps<{
  providerTypes: string[];
  providerOptionsByType: Record<string, string[]>;
  /** Localized section heading per CLI type, keyed by type. */
  typeLabels: Record<string, string>;
  defaultHomeRoot?: string;
  busy: boolean;
}>();

const emit = defineEmits<{ submit: [AdminCreateUserData]; cancel: [] }>();

const { t } = useI18n();

function emptyCreate(): AdminCreateUserData {
  return {
    username: "",
    password: "",
    name: "",
    email: "",
    is_admin: false,
    work_dir: "",
    provider_bindings: {},
  };
}

const form = ref<AdminCreateUserData>(emptyCreate());

// DirPicker expects a non-optional string; the API type models work_dir as
// optional. A bridging computed lets v-model write back through.
const workDir = computed<string>({
  get: () => form.value.work_dir ?? "",
  set: (v) => {
    form.value.work_dir = v;
  },
});

// prunedBindings drops entries whose value trims to empty, so an admin who
// left a picker on "none" doesn't ship a clear instruction that would step on
// a default. The update path deliberately does NOT prune — there an empty
// value means "patch this slot back to the implicit default".
function prunedBindings(b: Record<string, string> | undefined): Record<string, string> | undefined {
  if (!b) return undefined;
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(b)) {
    const trimmed = (v ?? "").trim();
    if (trimmed) out[k] = trimmed;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

// A new user must start with at least one account, otherwise every chat is
// refused until an admin binds one. Nothing to require when no provider is
// configured — the backend applies the same rule.
const hasBinding = computed(
  () =>
    props.providerTypes.length === 0 ||
    Object.values(form.value.provider_bindings ?? {}).some((v) => (v ?? "").trim() !== ""),
);

function setBinding(ptype: string, value: string) {
  if (!form.value.provider_bindings) form.value.provider_bindings = {};
  form.value.provider_bindings[ptype] = value;
}

function onSubmit() {
  const username = form.value.username.trim();
  emit("submit", {
    ...form.value,
    username,
    name: form.value.name?.trim() || username,
    email: form.value.email?.trim() || "",
    work_dir: form.value.work_dir?.trim() || "",
    provider_bindings: prunedBindings(form.value.provider_bindings),
  });
}

// reset lets the parent clear the draft after a successful create without
// re-mounting the form (which would collapse the section).
function reset() {
  form.value = emptyCreate();
}

defineExpose({ reset });
</script>

<template>
  <div class="border border-border rounded-md p-4 mb-4 bg-card" data-testid="create-user-form">
    <h2 class="text-[14px] font-semibold mb-3">{{ t("settings.adminUsers.createTitle") }}</h2>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <label class="block">
        <span class="text-[12px] text-muted-foreground">{{
          t("settings.adminUsers.username")
        }}</span>
        <input
          v-model="form.username"
          class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
        />
      </label>
      <label class="block">
        <span class="text-[12px] text-muted-foreground">{{
          t("settings.adminUsers.password")
        }}</span>
        <input
          v-model="form.password"
          type="password"
          class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
        />
      </label>
      <label class="block">
        <span class="text-[12px] text-muted-foreground">{{
          t("settings.adminUsers.displayNameOptional")
        }}</span>
        <input
          v-model="form.name"
          class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
        />
      </label>
      <label class="block">
        <span class="text-[12px] text-muted-foreground">{{ t("settings.adminUsers.email") }}</span>
        <input
          v-model="form.email"
          class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
        />
      </label>
      <div class="col-span-2">
        <span class="text-[12px] text-muted-foreground">
          {{ t("settings.adminUsers.workDirDefault", { root: props.defaultHomeRoot }) }}
        </span>
        <div class="mt-1">
          <DirPicker v-model="workDir" />
        </div>
      </div>
      <!-- One picker per CLI type configured in the YAML. Operators who only
           configured claude see a single picker; once they add a codex
           provider entry a second picker appears without a frontend deploy. -->
      <label v-for="ptype in providerTypes" :key="'create-' + ptype" class="block">
        <span class="text-[12px] text-muted-foreground">{{ typeLabels[ptype] }}</span>
        <select
          :value="form.provider_bindings?.[ptype] ?? ''"
          :data-testid="'create-binding-' + ptype"
          class="w-full mt-1 px-2 py-1 border border-border rounded bg-background text-[13px]"
          @change="setBinding(ptype, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">{{ t("common.none") }}</option>
          <option v-for="n in providerOptionsByType[ptype]" :key="n" :value="n">{{ n }}</option>
        </select>
      </label>
      <p
        v-if="!hasBinding"
        class="col-span-2 text-[12px] text-muted-foreground"
        data-testid="create-binding-required"
      >
        {{ t("settings.adminUsers.bindingRequired") }}
      </p>
      <label class="flex items-center gap-2 mt-5">
        <input v-model="form.is_admin" type="checkbox" />
        <span class="text-[13px]">{{ t("settings.adminUsers.makeAdmin") }}</span>
      </label>
    </div>
    <div class="flex gap-2 mt-4 justify-end">
      <button
        class="px-3 py-1.5 rounded-md border border-border text-[13px] cursor-pointer"
        @click="emit('cancel')"
      >
        {{ t("common.cancel") }}
      </button>
      <button
        class="px-3 py-1.5 rounded-md bg-primary text-primary-foreground text-[13px] font-medium cursor-pointer disabled:opacity-50"
        :disabled="busy || !form.username || !form.password || !hasBinding"
        @click="onSubmit"
      >
        {{ t("common.create") }}
      </button>
    </div>
  </div>
</template>
