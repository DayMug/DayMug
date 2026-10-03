<script setup lang="ts">
import { computed } from "vue";
import type { HTMLAttributes } from "vue";
import { Bot } from "lucide-vue-next";
import Avatar from "@/components/ui/avatar/Avatar.vue";
import AvatarFallback from "@/components/ui/avatar/AvatarFallback.vue";
import AvatarImage from "@/components/ui/avatar/AvatarImage.vue";
import { avatarInitials } from "@/lib/avatar";
import type { ConversationAttention } from "@/stores/conversationAttentionStore";

// Attributes (notably `class`) belong on the avatar itself, not on the
// positioning wrapper that carries the activity border.
defineOptions({ inheritAttrs: false });

const props = defineProps<{
  name: string;
  avatar?: string;
  // Platforms of the agent's attached bots. Non-empty renders an identity
  // treatment so bot-capable agents are distinguishable in the chat picker.
  botPlatforms?: string[];
  running?: boolean;
  selected?: boolean;
  attention?: ConversationAttention;
  fallbackClass?: HTMLAttributes["class"];
}>();

const hasConnectedBots = computed(() => (props.botPlatforms?.length ?? 0) > 0);
const avatarUrl = computed(() => {
  const value = props.avatar?.trim() ?? "";
  return /^https?:\/\//i.test(value) ? value : "";
});
const avatarIdentityKey = computed(
  () => `${props.name}\u0000${avatarUrl.value || props.avatar || ""}`,
);

// The root is `flex`, not `inline-flex`: as an inline box it takes part in
// baseline alignment, and its baseline is whatever its content offers — the
// bottom edge for an uploaded <img>, the text baseline for the initials
// fallback. An image avatar therefore sat on the line's baseline with the
// strut's descender padding the line box below it, pushing the tile a few
// pixels above the centre of whatever box was centring it, while initials
// avatars looked fine. Every other call site puts this inside a flex row, where
// a flex item is blockified anyway, so nothing else moves.
</script>

<template>
  <span class="agent-avatar relative flex shrink-0">
    <Avatar
      :key="avatarIdentityKey"
      v-bind="$attrs"
      :data-avatar-bots="hasConnectedBots ? botPlatforms!.length : undefined"
      :data-running="running ? 'true' : undefined"
      :data-selected="selected ? 'true' : undefined"
    >
      <AvatarImage v-if="avatarUrl" :src="avatarUrl" :alt="name" class="object-cover" />
      <AvatarFallback v-else :name="name" :class="fallbackClass">
        <span>{{ avatar || avatarInitials(name) }}</span>
      </AvatarFallback>
      <span
        v-if="hasConnectedBots"
        data-testid="bot-connected-avatar"
        :title="botPlatforms!.join(' · ')"
        class="absolute bottom-0 right-0 z-20 flex size-[46%] items-center justify-center rounded-tl-sm bg-emerald-600 text-white"
      >
        <Bot class="size-[68%]" />
      </span>
      <!-- The selected agent keeps its solid black frame even while running:
           the chasing border would replace it and leave the user unsure which
           agent is open. Its conversations already show their own spinners. -->
      <span
        v-if="running && !selected"
        data-testid="running-avatar-border"
        class="agent-running-border"
        aria-hidden="true"
      >
        <span class="agent-running-border-flow" />
      </span>
    </Avatar>
    <span
      v-if="attention"
      data-testid="unread-completion-agent-dot"
      class="attention-dot absolute -right-1 -top-1 z-40 size-[8px] rounded-full"
      :data-attention="attention"
      aria-hidden="true"
    />
  </span>
</template>

<style scoped>
.agent-running-border {
  position: absolute;
  inset: -0.5px;
  z-index: 30;
  box-sizing: border-box;
  overflow: hidden;
  /* Safari anti-aliases a masked rounded edge inward. Extend the mask by half
     a pixel and let the avatar clip it, so visible paint reaches the edge while
     the extra padding preserves the 1.5px/2px inward line thickness. */
  padding: calc(clamp(1.5px, 5%, 2px) + 0.5px);
  /* Follow the tile's own radius. A fixed radius smaller than the tile's (the
     rail's 6px tiles) let the tile's rounded clip shave the ring's outer edge
     at every corner, so the line looked cut there. */
  border-radius: inherit;
  pointer-events: none;
  /* The chasing segments are only legible where they happen to contrast with
     whatever the avatar shows underneath: a dark uploaded image on a dark row
     swallowed them entirely. The mask clips this background to the same ring
     band, so it gives the highlight a known surface on both themes — the same
     track-plus-highlight reading as the conversation progress bar. */
  background: var(--running-ring-track);
  -webkit-mask:
    linear-gradient(#000 0 0) content-box,
    linear-gradient(#000 0 0);
  -webkit-mask-composite: xor;
  mask-composite: exclude;
}

/* Keep the rounded-square outline fixed and rotate only its paint. Mobile
   browsers can composite this transform without repainting an SVG stroke, and
   a full turn has no path-length seam to expose when the animation restarts. */
.agent-running-border-flow {
  position: absolute;
  inset: -50%;
  background: repeating-conic-gradient(
    var(--running-highlight) 0deg 65deg,
    transparent 65deg 90deg
  );
  will-change: transform;
  animation: agent-running-border-spin 3.5s linear infinite;
}

@keyframes agent-running-border-spin {
  to {
    transform: rotate(1turn);
  }
}

@media (prefers-reduced-motion: reduce) {
  .agent-running-border-flow {
    animation: none;
  }
}
</style>
