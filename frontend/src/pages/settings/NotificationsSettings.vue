<script setup lang="ts">
import { computed, ref, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { fetchMe, updateMyNotifications } from "@/composables/useApi";
import { useAuth } from "@/composables/useAuth";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";
import { errorMessage } from "@/lib/errorMessage";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.notifications.title")));

const BARK_HOMEPAGE = "https://bark.day.app/#/";
const PUSHDEER_HOMEPAGE = "https://www.pushdeer.com/";

const barkURL = ref("");
const pushDeerKey = ref("");
// "" lets the runtime pick whichever single channel is configured; "bark"
// or "pushdeer" disambiguates when both fields are populated.
const channel = ref<"" | "bark" | "pushdeer">("");
const errorMsg = ref("");
const successMsg = ref("");
const submitting = ref(false);
const loading = ref(true);

const { authMe, loadAuthMe } = useAuth();

// Channel picker only matters when both are configured — until then the
// runtime auto-selects whichever single channel is set, so showing a
// disabled selector adds visual noise without informing a choice.
const bothConfigured = computed(
  () => barkURL.value.trim() !== "" && pushDeerKey.value.trim() !== "",
);

onMounted(async () => {
  try {
    const me = await fetchMe();
    barkURL.value = me.bark_url ?? "";
    pushDeerKey.value = me.pushdeer_key ?? "";
    const c = me.notification_channel ?? "";
    channel.value = c === "bark" || c === "pushdeer" ? c : "";
  } catch (e) {
    errorMsg.value = errorMessage(e, t("settings.notifications.errors.generic"));
  } finally {
    loading.value = false;
  }
});

async function onSubmit() {
  if (submitting.value) return;
  errorMsg.value = "";
  successMsg.value = "";
  submitting.value = true;
  try {
    // Drop the explicit channel when only one (or neither) is configured —
    // the backend will auto-pick the populated one, and storing a stale
    // "bark" preference after the user just cleared their Bark URL would
    // mean any future re-add silently overrides the channel they intend.
    const effectiveChannel = bothConfigured.value ? channel.value : "";
    await updateMyNotifications({
      bark_url: barkURL.value.trim(),
      pushdeer_key: pushDeerKey.value.trim(),
      notification_channel: effectiveChannel,
    });
    // Refresh the cached AuthUser so the conversation list can re-evaluate
    // whether to show the bell icon without forcing a full page reload.
    await loadAuthMe();
    if (authMe.value && !bothConfigured.value) {
      channel.value = "";
    }
    successMsg.value = t("settings.notifications.success");
  } catch (e) {
    errorMsg.value = errorMessage(e, t("settings.notifications.errors.generic"));
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-[640px] px-4 pb-8">
    <SettingsDetailHeader :title="t('settings.notifications.title')" />

    <form class="space-y-5 max-w-md" autocomplete="off" @submit.prevent="onSubmit">
      <div>
        <label
          for="notifications-bark-url"
          class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
        >
          {{ t("settings.notifications.bark.label") }}
          <span class="font-normal normal-case tracking-normal opacity-70">
            {{ t("settings.notifications.bark.example") }}
          </span>
        </label>
        <Input
          id="notifications-bark-url"
          v-model="barkURL"
          class="font-mono text-[13px]"
          :placeholder="t('settings.notifications.bark.placeholder')"
          :disabled="submitting || loading"
        />
        <div class="mt-1.5 text-[12px] text-muted-foreground/80">
          {{ t("settings.notifications.bark.help") }}
          <a
            :href="BARK_HOMEPAGE"
            target="_blank"
            rel="noopener noreferrer"
            class="underline underline-offset-2 hover:text-foreground"
          >
            {{ t("settings.notifications.bark.docsLink") }}
          </a>
        </div>
      </div>

      <div>
        <label
          for="notifications-pushdeer-key"
          class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
        >
          {{ t("settings.notifications.pushdeer.label") }}
          <span class="font-normal normal-case tracking-normal opacity-70">
            {{ t("settings.notifications.pushdeer.example") }}
          </span>
        </label>
        <Input
          id="notifications-pushdeer-key"
          v-model="pushDeerKey"
          class="font-mono text-[13px]"
          :placeholder="t('settings.notifications.pushdeer.placeholder')"
          :disabled="submitting || loading"
        />
        <div class="mt-1.5 text-[12px] text-muted-foreground/80">
          {{ t("settings.notifications.pushdeer.help") }}
          <a
            :href="PUSHDEER_HOMEPAGE"
            target="_blank"
            rel="noopener noreferrer"
            class="underline underline-offset-2 hover:text-foreground"
          >
            {{ t("settings.notifications.pushdeer.docsLink") }}
          </a>
        </div>
      </div>

      <!-- Channel selector only surfaces when both channels are populated;
           otherwise the runtime picks the single channel that is configured
           and a disabled selector would only confuse the user. -->
      <div v-if="bothConfigured">
        <div
          class="block text-[11px] font-semibold uppercase tracking-wide text-muted-foreground mb-1.5"
        >
          {{ t("settings.notifications.channel.label") }}
        </div>
        <div class="space-y-1.5">
          <label class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="radio"
              name="notification-channel"
              value="bark"
              :checked="channel === 'bark' || channel === ''"
              :disabled="submitting || loading"
              @change="channel = 'bark'"
            />
            <span>{{ t("settings.notifications.channel.bark") }}</span>
          </label>
          <label class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="radio"
              name="notification-channel"
              value="pushdeer"
              :checked="channel === 'pushdeer'"
              :disabled="submitting || loading"
              @change="channel = 'pushdeer'"
            />
            <span>{{ t("settings.notifications.channel.pushdeer") }}</span>
          </label>
        </div>
        <div class="mt-1.5 text-[12px] text-muted-foreground/80">
          {{ t("settings.notifications.channel.help") }}
        </div>
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
        <Button type="submit" size="sm" :disabled="submitting || loading">
          {{ submitting ? t("settings.notifications.saving") : t("settings.notifications.submit") }}
        </Button>
      </div>
    </form>
  </div>
</template>
