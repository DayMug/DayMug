<script setup lang="ts">
import type { AvatarFallbackProps } from "reka-ui";
import type { HTMLAttributes } from "vue";
import { computed } from "vue";
import { reactiveOmit } from "@vueuse/core";
import { AvatarFallback } from "reka-ui";
import { cn } from "@/lib/utils";
import { avatarColors } from "@/lib/avatar";

const props = defineProps<
  AvatarFallbackProps & {
    class?: HTMLAttributes["class"];
    // When provided, picks a deterministic warm hue from the name and uses
    // it as the fallback's background / foreground. Lets every agent get a
    // stable color across the app without hand-assigning palettes.
    name?: string;
  }
>();

const delegatedProps = reactiveOmit(props, "class", "name");

const colorStyle = computed(() => {
  if (!props.name) return undefined;
  const { bg, fg } = avatarColors(props.name);
  return { backgroundColor: bg, color: fg };
});
</script>

<template>
  <AvatarFallback
    data-slot="avatar-fallback"
    v-bind="delegatedProps"
    :class="cn('bg-muted flex size-full items-center justify-center rounded-[4px]', props.class)"
    :style="colorStyle"
  >
    <slot />
  </AvatarFallback>
</template>
