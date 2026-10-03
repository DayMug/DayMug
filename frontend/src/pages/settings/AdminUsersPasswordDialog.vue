<script setup lang="ts">
// Password-reset modal. Owns the typed password so it never lingers in page
// state after the dialog closes; the parent only holds the target user id.
import { ref, watch } from "vue";
import { useI18n } from "vue-i18n";

// `error` is rendered here rather than on the page behind it: this dialog is a
// full-screen overlay, so a rejected reset shown in the page's error line is
// invisible to the admin who triggered it.
const props = defineProps<{ open: boolean; busy: boolean; error?: string }>();

const emit = defineEmits<{ submit: [password: string]; close: [] }>();

const { t } = useI18n();

const password = ref<string>("");

// Clear on open rather than on close so a failed submit keeps the value
// on screen for the admin to correct.
watch(
  () => props.open,
  (open) => {
    if (open) password.value = "";
  },
);

function onSubmit() {
  if (!password.value || props.busy) return;
  emit("submit", password.value);
}
</script>

<template>
  <div
    v-if="open"
    class="fixed inset-0 z-50 bg-black/40 flex items-center justify-center p-4"
    data-testid="password-reset-dialog"
    @click.self="emit('close')"
  >
    <div class="bg-card border border-border rounded-md p-4 w-full max-w-[360px]">
      <h3 class="text-[14px] font-semibold mb-3">{{ t("settings.adminUsers.resetPassword") }}</h3>
      <input
        v-model="password"
        type="password"
        :placeholder="t('settings.adminUsers.newPassword')"
        class="w-full px-2 py-1 border border-border rounded bg-background text-[13px]"
        @keyup.enter="onSubmit"
      />
      <p
        v-if="error"
        class="mt-2 text-[12px] text-red-600"
        data-testid="password-reset-error"
        role="alert"
      >
        {{ error }}
      </p>
      <div class="flex gap-2 mt-3 justify-end">
        <button
          class="px-3 py-1.5 rounded-md border border-border text-[13px] cursor-pointer"
          @click="emit('close')"
        >
          {{ t("common.cancel") }}
        </button>
        <button
          class="px-3 py-1.5 rounded-md bg-primary text-primary-foreground text-[13px] cursor-pointer disabled:opacity-50"
          :disabled="!password || busy"
          @click="onSubmit"
        >
          {{ t("settings.adminUsers.setPassword") }}
        </button>
      </div>
    </div>
  </div>
</template>
