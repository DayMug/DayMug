<script setup lang="ts">
import { useLocale } from "@/composables/useLocale";
import type { SupportedLocale } from "@/i18n";

withDefaults(
  defineProps<{
    // "pill" renders the inline two-button toggle used on the login page;
    // "inline" is the right-aligned compact variant used inside settings rows.
    variant?: "pill" | "inline";
  }>(),
  { variant: "pill" },
);

const { locale, setLocale, t } = useLocale();

const options: { value: SupportedLocale; label: string }[] = [
  { value: "en", label: "EN" },
  { value: "zh", label: "中" },
];
</script>

<template>
  <div
    class="language-switcher inline-flex items-center gap-1 rounded-full border border-border bg-card/60 p-0.5 text-[12px]"
    :class="{ 'language-switcher--inline': variant === 'inline' }"
    role="group"
    :aria-label="t('language.switch')"
  >
    <button
      v-for="opt in options"
      :key="opt.value"
      type="button"
      class="language-switcher-option px-2 py-0.5 rounded-full transition-colors cursor-pointer"
      :class="
        locale === opt.value
          ? 'bg-foreground text-background'
          : 'text-muted-foreground hover:text-foreground'
      "
      :aria-pressed="locale === opt.value"
      :data-locale="opt.value"
      @click="setLocale(opt.value)"
    >
      {{ opt.label }}
    </button>
  </div>
</template>
