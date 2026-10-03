<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { usePageTitle } from "@/composables/useDocumentTitle";
import AdminGlobalPricing from "./AdminGlobalPricing.vue";
import AdminProvidersEditor from "./AdminProvidersEditor.vue";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";

const { t } = useI18n();
usePageTitle(computed(() => t("settings.titles.adminModels")));
</script>

<template>
  <div class="mx-auto w-full max-w-[760px] px-4 pb-8 sm:px-6">
    <SettingsDetailHeader
      :title="t('settings.titles.adminModels')"
      :eyebrow="t('settings.adminModels.eyebrow')"
      parent-route-name="settings-admin"
    />
    <p class="mb-5 text-[12px] text-muted-foreground">
      {{ t("settings.adminModels.intro") }}
    </p>
    <div class="space-y-6">
      <AdminProvidersEditor />
      <AdminGlobalPricing />
    </div>
  </div>
</template>
