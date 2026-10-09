<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { changeMyPassword } from "@/composables/useApi";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { errorMessage } from "@/lib/errorMessage";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.account.title")));

const currentPassword = ref("");
const newPassword = ref("");
const confirmPassword = ref("");
const errorMsg = ref("");
const successMsg = ref("");
const submitting = ref(false);

function reset() {
  currentPassword.value = "";
  newPassword.value = "";
  confirmPassword.value = "";
}

async function onSubmit() {
  if (submitting.value) return;
  errorMsg.value = "";
  successMsg.value = "";
  if (!currentPassword.value || !newPassword.value || !confirmPassword.value) {
    errorMsg.value = t("settings.account.errors.missingFields");
    return;
  }
  if (newPassword.value !== confirmPassword.value) {
    errorMsg.value = t("settings.account.errors.confirmMismatch");
    return;
  }
  if (newPassword.value === currentPassword.value) {
    errorMsg.value = t("settings.account.errors.sameAsCurrent");
    return;
  }
  submitting.value = true;
  try {
    await changeMyPassword(currentPassword.value, newPassword.value);
    successMsg.value = t("settings.account.success");
    reset();
  } catch (e) {
    errorMsg.value = errorMessage(e, t("settings.account.errors.generic"));
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-[640px] px-4 pb-8">
    <SettingsDetailHeader :title="t('settings.account.title')" />

    <form class="space-y-4 max-w-md" autocomplete="off" @submit.prevent="onSubmit">
      <h4 class="text-[13px] font-semibold text-foreground">
        {{ t("settings.account.changePassword") }}
      </h4>
      <p class="text-[12px] text-muted-foreground -mt-2">
        {{ t("settings.account.changePasswordHelp") }}
      </p>

      <div>
        <label
          for="current-password"
          class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
        >
          {{ t("settings.account.currentPassword") }}
        </label>
        <Input
          id="current-password"
          v-model="currentPassword"
          type="password"
          autocomplete="current-password"
          :disabled="submitting"
        />
      </div>

      <div>
        <label
          for="new-password"
          class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
        >
          {{ t("settings.account.newPassword") }}
        </label>
        <Input
          id="new-password"
          v-model="newPassword"
          type="password"
          autocomplete="new-password"
          :disabled="submitting"
        />
      </div>

      <div>
        <label
          for="confirm-password"
          class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
        >
          {{ t("settings.account.confirmPassword") }}
        </label>
        <Input
          id="confirm-password"
          v-model="confirmPassword"
          type="password"
          autocomplete="new-password"
          :disabled="submitting"
        />
      </div>

      <div
        v-if="errorMsg"
        role="alert"
        class="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        {{ errorMsg }}
      </div>
      <div
        v-if="successMsg"
        role="status"
        class="rounded-md border border-green-700/30 bg-green-700/10 px-3 py-2 text-sm text-green-700"
      >
        {{ successMsg }}
      </div>

      <div class="pt-1">
        <Button type="submit" size="sm" :disabled="submitting">
          {{ submitting ? t("settings.account.saving") : t("settings.account.submit") }}
        </Button>
      </div>
    </form>
  </div>
</template>
