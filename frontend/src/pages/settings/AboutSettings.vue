<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import PoweredBy from "@/components/PoweredBy.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.about.title")));
const version = __APP_VERSION__;
</script>

<template>
  <div class="mx-auto w-full max-w-[640px] px-4 pb-8">
    <SettingsDetailHeader :title="t('settings.about.title')" />
    <div class="space-y-3 max-w-md">
      <div class="flex items-center justify-between py-2">
        <span class="text-sm text-muted-foreground">{{ t("settings.about.application") }}</span>
        <span class="text-sm font-medium">DayMug</span>
      </div>
      <div class="flex items-center justify-between py-2 border-t border-border">
        <span class="text-sm text-muted-foreground">{{ t("settings.about.version") }}</span>
        <code class="rounded bg-muted px-2 py-0.5 text-xs font-mono">{{ version }}</code>
      </div>
      <div class="border-t border-border py-2 text-sm text-muted-foreground">
        <PoweredBy />
      </div>
    </div>
  </div>
</template>
