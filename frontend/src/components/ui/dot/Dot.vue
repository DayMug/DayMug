<script setup lang="ts">
// Tiny colored dot — ok / warn / err / off / accent / connected / disconnected.
// UI design uses it for status pills, "unread" markers, and connection state.

type Tone = "ok" | "warn" | "err" | "off" | "accent" | "connected" | "disconnected";

const props = withDefaults(
  defineProps<{
    tone?: Tone;
    size?: number;
    pulse?: boolean;
  }>(),
  { tone: "ok", size: 6, pulse: false },
);

const TONE_VAR: Record<Tone, string> = {
  ok: "oklch(0.62 0.18 150)",
  warn: "oklch(0.72 0.18 70)",
  err: "oklch(0.62 0.22 25)",
  off: "var(--ink-5)",
  accent: "var(--primary)",
  connected: "var(--connected)",
  disconnected: "var(--disconnected)",
};
</script>

<template>
  <span
    class="inline-block rounded-full shrink-0"
    :class="pulse ? 'animate-pulse' : ''"
    :style="{
      width: props.size + 'px',
      height: props.size + 'px',
      backgroundColor: TONE_VAR[props.tone],
    }"
    aria-hidden="true"
  />
</template>
