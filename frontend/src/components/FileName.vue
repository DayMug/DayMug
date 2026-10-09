<script setup lang="ts">
import { computed } from "vue";
import { splitFileName } from "@/lib/fileName";

const props = defineProps<{ name: string }>();

const parts = computed(() => splitFileName(props.name));
</script>

<template>
  <!-- Two boxes rather than one string: the head shrinks and takes the
       ellipsis while the extension sits in a box that refuses to shrink, so
       the cut lands in the middle and `.ts` survives however narrow the
       column gets. Sizing stays width-driven — no character budget to retune
       when the panel resizes or the tree indents.

       `text-overflow` cuts on a whole-character boundary, so up to one
       character of slack can show up between the ellipsis and the extension.
       Closing it would mean measuring text in JS on every resize, which is a
       lot of machinery for a sub-character gap. -->
  <span class="inline-flex min-w-0 max-w-full">
    <span class="min-w-0 overflow-hidden text-ellipsis whitespace-nowrap">{{ parts.head }}</span
    ><span v-if="parts.tail" class="shrink-0 whitespace-pre">{{ parts.tail }}</span>
  </span>
</template>
