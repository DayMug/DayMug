<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import {
  adminFetchModels,
  adminFetchNewUserDefaults,
  adminSaveNewUserDefaults,
  type AdminAccountModels,
  type NewUserDefaults,
} from "@/composables/apiAdmin";
import { useAsyncOperation } from "@/composables/useAsyncOperation";
import { Button } from "@/components/ui/button";

const { t } = useI18n();
const accounts = ref<AdminAccountModels[]>([]);
const selected = ref("");
const saved = ref("");
const loaded = ref(false);
const { busy, error, message, run } = useAsyncOperation();

function optionValue(d: NewUserDefaults): string {
  return d.provider_name ? JSON.stringify([d.provider_type, d.provider_name, d.model]) : "";
}

function defaultsFor(account: AdminAccountModels, model: string): NewUserDefaults {
  return { provider_type: account.provider, provider_name: account.account, model };
}

const groups = computed(() =>
  accounts.value.map((account) => ({
    label: `${account.account} (${account.provider})`,
    options: ["", ...account.models].map((model) => ({
      value: optionValue(defaultsFor(account, model)),
      label: model || t("settings.adminGlobal.newUserAccountDefault"),
    })),
  })),
);
const unavailable = computed(
  () =>
    !!selected.value &&
    !groups.value.some((g) => g.options.some((o) => o.value === selected.value)),
);

onMounted(async () => {
  await run(async () => {
    const [defaults, models] = await Promise.all([adminFetchNewUserDefaults(), adminFetchModels()]);
    accounts.value = models.accounts;
    selected.value = saved.value = optionValue(defaults);
    loaded.value = true;
  });
});

async function save() {
  await run(async () => {
    const [provider_type, provider_name, model] = selected.value
      ? (JSON.parse(selected.value) as [string, string, string])
      : ["", "", ""];
    const result = await adminSaveNewUserDefaults({ provider_type, provider_name, model });
    selected.value = saved.value = optionValue(result);
    message.value = t("settings.adminGlobal.newUserDefaultsSaved");
  });
}
</script>

<template>
  <section class="border border-border rounded-md bg-card p-4" data-testid="new-user-defaults">
    <h2 class="text-[14px] font-semibold mb-2">
      <label for="new-user-model">{{ t("settings.adminGlobal.newUserDefaults") }}</label>
    </h2>
    <p class="text-[12px] text-muted-foreground mb-3">
      {{ t("settings.adminGlobal.newUserDefaultsIntro") }}
    </p>
    <select
      id="new-user-model"
      v-model="selected"
      :disabled="!loaded || busy"
      class="w-full px-2 py-1.5 border border-border rounded bg-background text-[13px]"
    >
      <option value="">{{ t("settings.adminGlobal.newUserUnbound") }}</option>
      <option v-if="unavailable" :value="selected" disabled>
        {{ t("settings.adminGlobal.newUserDefaultsUnavailable") }}
      </option>
      <optgroup v-for="group in groups" :key="group.label" :label="group.label">
        <option v-for="option in group.options" :key="option.value" :value="option.value">
          {{ option.label }}
        </option>
      </optgroup>
    </select>
    <p v-if="unavailable" class="mt-2 text-[12px] text-muted-foreground">
      {{ t("settings.adminGlobal.newUserDefaultsUnavailable") }}
    </p>
    <Button
      class="mt-3"
      variant="outline"
      size="sm"
      :disabled="!loaded || busy || unavailable || selected === saved"
      @click="save"
    >
      {{ busy ? t("common.saving") : t("common.save") }}
    </Button>
    <p v-if="message" class="mt-2 text-[12px] text-muted-foreground" role="status">{{ message }}</p>
    <p v-if="error" class="mt-2 text-[12px] text-red-600" role="alert">{{ error }}</p>
  </section>
</template>
