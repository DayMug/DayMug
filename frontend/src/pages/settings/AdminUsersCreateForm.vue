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
const useDefaults = ref(true);

// DirPicker expects a non-optional string; the API type models work_dir as
// optional. A bridging computed lets v-model write back through.
const workDir = computed<string>({
  get: () => form.value.work_dir ?? "",
  set: (v) => {
    form.value.work_dir = v;
  },
});

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
    provider_bindings: useDefaults.value
      ? undefined
      : Object.fromEntries(
          Object.entries(form.value.provider_bindings ?? {})
            .map(([key, value]) => [key, value.trim()])
            .filter(([, value]) => value),
        ),
  });
}

// reset lets the parent clear the draft after a successful create without
// re-mounting the form (which would collapse the section).
function reset() {
  form.value = emptyCreate();
  useDefaults.value = true;
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
      <label class="col-span-2 flex items-center gap-2">
        <input v-model="useDefaults" type="checkbox" data-testid="create-use-defaults" />
        <span class="text-[13px]">{{ t("settings.adminUsers.useNewUserDefaults") }}</span>
      </label>
      <template v-if="!useDefaults">
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
        <p class="col-span-2 text-[12px] text-muted-foreground">
          {{ t("settings.adminUsers.unboundAllowed") }}
        </p>
      </template>
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
        :disabled="busy || !form.username || !form.password"
        @click="onSubmit"
      >
        {{ t("common.create") }}
      </button>
    </div>
  </div>
</template>
