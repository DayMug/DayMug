<script setup lang="ts">
import type { SwitchRootEmits, SwitchRootProps } from "reka-ui";
import type { HTMLAttributes } from "vue";
import { reactiveOmit } from "@vueuse/core";
import { SwitchRoot, SwitchThumb, useForwardPropsEmits } from "reka-ui";
import { cn } from "@/lib/utils";

// Sized variants — UI design uses both `md` and `sm`. The thumb auto-scales.
type SwitchSize = "sm" | "md";

const props = defineProps<
  SwitchRootProps & {
    class?: HTMLAttributes["class"];
    size?: SwitchSize;
  }
>();
const emits = defineEmits<SwitchRootEmits>();

const delegatedProps = reactiveOmit(props, "class", "size");
const forwarded = useForwardPropsEmits(delegatedProps, emits);

const ROOT_SIZES: Record<SwitchSize, string> = {
  sm: "h-4 w-7",
  md: "h-5 w-9",
};
const THUMB_SIZES: Record<SwitchSize, string> = {
  sm: "size-3 data-[state=checked]:translate-x-3",
  md: "size-4 data-[state=checked]:translate-x-4",
};
</script>

<template>
  <SwitchRoot
    v-bind="forwarded"
    data-slot="switch"
    :class="
      cn(
        'peer inline-flex shrink-0 items-center rounded-full border border-transparent shadow-xs transition-all outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-[var(--ink-5)]',
        ROOT_SIZES[props.size ?? 'md'],
        props.class,
      )
    "
  >
    <SwitchThumb
      data-slot="switch-thumb"
      :class="
        cn(
          'pointer-events-none block rounded-full bg-background ring-0 shadow-sm transition-transform data-[state=unchecked]:translate-x-0.5',
          THUMB_SIZES[props.size ?? 'md'],
        )
      "
    />
  </SwitchRoot>
</template>
