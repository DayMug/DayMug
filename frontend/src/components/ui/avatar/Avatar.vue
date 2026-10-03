<script setup lang="ts">
import type { HTMLAttributes } from "vue";
import { AvatarRoot } from "reka-ui";
import { cn } from "@/lib/utils";

const props = defineProps<{
  class?: HTMLAttributes["class"];
}>();
</script>

<template>
  <!-- Rounded-square with a compact radius; callers can opt
       back into a circle by passing `rounded-full` in :class. -->
  <AvatarRoot
    data-slot="avatar"
    :class="cn('relative flex size-8 shrink-0 overflow-hidden rounded-[4px]', props.class)"
  >
    <slot />
  </AvatarRoot>
</template>
