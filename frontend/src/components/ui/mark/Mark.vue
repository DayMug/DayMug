<script setup lang="ts">
// DayMug brand mark. The SVG lives in /public/favicon.svg so the same
// asset drives both the browser-tab icon and the in-page brand strip — one
// source of truth, no inline-SVG drift to maintain.
withDefaults(
  defineProps<{
    size?: number;
  }>(),
  { size: 22 },
);
</script>

<template>
  <img
    src="/favicon.svg"
    :width="size"
    :height="size"
    alt="DayMug"
    class="shrink-0"
    draggable="false"
  />
</template>
