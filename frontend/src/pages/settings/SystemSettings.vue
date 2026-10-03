<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { useTheme } from "@/composables/useTheme";
import { useTimeFormat } from "@/composables/useTimeFormat";
import { Button } from "@/components/ui/button";
import LanguageSwitcher from "@/components/LanguageSwitcher.vue";
import SettingsDetailHeader from "./SettingsDetailHeader.vue";
import { usePageTitle } from "@/composables/useDocumentTitle";

const { isDark, toggleTheme } = useTheme();
const { timeFormat, cycleTimeFormat } = useTimeFormat();
const { t } = useI18n();
usePageTitle(computed(() => t("settings.appearance.title")));

const timeFormatLabel = computed(() => {
  switch (timeFormat.value) {
    case "12h":
      return t("settings.appearance.timeFormat12h");
    case "24h":
      return t("settings.appearance.timeFormat24h");
    default:
      return t("settings.appearance.timeFormatSystem");
  }
});
</script>

<template>
  <div class="mx-auto w-full max-w-[640px] px-4 pb-8">
    <SettingsDetailHeader :title="t('settings.appearance.title')" />
    <div class="flex items-center justify-between py-2.5">
      <span class="text-sm text-muted-foreground">{{ t("settings.appearance.theme") }}</span>
      <Button variant="outline" size="sm" @click="toggleTheme">
        {{ isDark ? t("settings.appearance.themeDark") : t("settings.appearance.themeLight") }}
      </Button>
    </div>
    <div class="flex items-center justify-between py-2.5 border-t border-border">
      <span class="text-sm text-muted-foreground">{{ t("settings.appearance.language") }}</span>
      <LanguageSwitcher variant="inline" />
    </div>
    <div class="flex items-center justify-between py-2.5 border-t border-border">
      <span class="text-sm text-muted-foreground">{{ t("settings.appearance.timeFormat") }}</span>
      <Button variant="outline" size="sm" @click="cycleTimeFormat">
        {{ timeFormatLabel }}
      </Button>
    </div>
  </div>
</template>
