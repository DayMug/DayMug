<script setup lang="ts">
// Small keyboard-shortcut chip. Used in preview footers, command menus,
// and the login screen ("↵ to sign in"). Mono font + paper-alt fill.
</script>

<template>
  <kbd
    class="inline-flex items-center justify-center px-1.5 py-0.5 rounded border border-border bg-card font-mono text-[10px] text-[var(--ink-3)] shadow-sm"
  >
    <slot />
  </kbd>
</template>
