<script setup lang="ts">
const animationDuration = `${1 + Math.random() * 0.5}s`;
</script>

<template>
  <div class="running-progress" data-testid="running-progress" aria-hidden="true">
    <span class="running-progress__segment" :style="{ animationDuration }" />
  </div>
</template>

<style scoped>
.running-progress {
  position: absolute;
  right: 0.75rem;
  bottom: 0;
  left: 0.75rem;
  z-index: 20;
  height: 2px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--running-track);
  pointer-events: none;
}

.running-progress__segment {
  display: block;
  width: 30%;
  height: 100%;
  border-radius: inherit;
  background: var(--running-highlight);
  box-shadow: 0 0 6px var(--running-glow);
  animation: conversation-running ease-in-out infinite;
}

@keyframes conversation-running {
  from {
    transform: translateX(-115%);
  }
  to {
    transform: translateX(445%);
  }
}

/* Same reason as the rail's running dots: a frozen bar is indistinguishable
   from a rule under the row, so the travelling segment becomes a full-width
   bar that breathes in place. The inline duration still applies, which keeps
   concurrent rows out of lockstep. */
@media (prefers-reduced-motion: reduce) {
  .running-progress__segment {
    width: 100%;
    animation-name: conversation-running-still;
  }
}

@keyframes conversation-running-still {
  0%,
  100% {
    opacity: 0.3;
  }
  50% {
    opacity: 0.95;
  }
}
</style>
