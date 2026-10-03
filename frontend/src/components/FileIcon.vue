<script setup lang="ts">
import { computed } from "vue";
import { ICONS, resolveIcon } from "./fileIconMap";

const props = withDefaults(
  defineProps<{
    isDir: boolean;
    fileName?: string;
    size?: number;
    /** Directories only — renders the open-folder glyph. */
    expanded?: boolean;
  }>(),
  { fileName: undefined, size: 16, expanded: false },
);

const icon = computed(() => ICONS[resolveIcon(props.fileName ?? "", props.isDir, props.expanded)]);

// The glyph scales with `size` but lucide's stroke does not, so the default
// 1.5 hairline reads as chunky once the tile grid blows it up to 40px+.
const strokeWidth = computed(() => (props.size >= 40 ? 1.25 : 1.5));
</script>

<template>
  <span class="file-icon-wrapper" :style="{ width: props.size + 'px', height: props.size + 'px' }">
    <!-- Inherits `currentColor`, so callers set the tone with `text-*`. -->
    <component :is="icon" :size="props.size" :stroke-width="strokeWidth" aria-hidden="true" />
  </span>
</template>

<style scoped>
.file-icon-wrapper {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
</style>
