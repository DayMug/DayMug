<script setup lang="ts">
import { computed, ref } from "vue";
import { Loader2, RefreshCw } from "lucide-vue-next";
import { useI18n } from "vue-i18n";

// Generic touch-driven pull-to-refresh wrapper. Acts as the scroll
// container for its slotted content — the gesture only engages when
// scrolled to the very top so it never fights native scrolling. The
// indicator strip sits in the document flow so the content is pushed
// down by the same amount it grows, selling the "pulling down" feel.
//
// The spinner stays pinned for the full duration of `onRefresh`'s
// returned promise, independent of the touch lifecycle.
const props = defineProps<{
  // Invoked when the user releases past the trigger distance. May be
  // async; the spinner holds until it settles.
  onRefresh: () => Promise<void> | void;
  // When true the gesture is inert (e.g. non-touch / disabled contexts).
  disabled?: boolean;
}>();

const { t } = useI18n();

const TRIGGER_DISTANCE = 64;
const MAX_PULL = 96;
const RUBBER_BAND = 0.5;

const scrollContainer = ref<HTMLElement | null>(null);
const pullDistance = ref(0);
const pullActive = ref(false);
const refreshing = ref(false);
let touchStartY = 0;
let touchTracking = false;

function onTouchStart(e: TouchEvent) {
  const el = scrollContainer.value;
  if (!el || props.disabled || refreshing.value) {
    touchTracking = false;
    return;
  }
  // Only start tracking a pull when we're at the top; otherwise the
  // gesture is a normal scroll.
  if (el.scrollTop > 0) {
    touchTracking = false;
    return;
  }
  touchTracking = true;
  touchStartY = e.touches[0].clientY;
  pullDistance.value = 0;
  pullActive.value = false;
}

function onTouchMove(e: TouchEvent) {
  if (!touchTracking) return;
  const dy = e.touches[0].clientY - touchStartY;
  if (dy <= 0) {
    // Finger moved up: this is a scroll, not a pull. Release so native
    // scrolling resumes immediately.
    pullDistance.value = 0;
    pullActive.value = false;
    touchTracking = false;
    return;
  }
  // Rubber-band so the indicator decelerates as the user pulls further.
  const damped = Math.min(MAX_PULL, dy * RUBBER_BAND);
  pullDistance.value = damped;
  pullActive.value = true;
  // Suppress the browser's overscroll/bounce so the pull is smooth.
  if (e.cancelable) e.preventDefault();
}

async function onTouchEnd() {
  if (!touchTracking) return;
  touchTracking = false;
  const shouldRefresh = pullDistance.value >= TRIGGER_DISTANCE && !refreshing.value;
  pullDistance.value = 0;
  pullActive.value = false;
  if (!shouldRefresh) return;
  refreshing.value = true;
  try {
    await props.onRefresh();
  } finally {
    refreshing.value = false;
  }
}

// While refreshing, pin to TRIGGER_DISTANCE so the spinner stays visible
// until onRefresh resolves; otherwise track the live pull distance.
const indicatorOffset = computed(() => {
  if (refreshing.value) return TRIGGER_DISTANCE;
  return pullDistance.value;
});

// Arrow flips once the user has pulled far enough to commit.
const arrowFlipped = computed(() => pullDistance.value >= TRIGGER_DISTANCE);
</script>

<template>
  <div
    ref="scrollContainer"
    class="pull-to-refresh"
    @touchstart.passive="onTouchStart"
    @touchmove="onTouchMove"
    @touchend="onTouchEnd"
    @touchcancel="onTouchEnd"
  >
    <div
      class="overflow-hidden flex items-end justify-center text-muted-foreground"
      :class="{ 'transition-[height] duration-200': !pullActive }"
      :style="{ height: `${indicatorOffset}px` }"
      data-testid="pull-indicator"
      aria-hidden="true"
    >
      <div v-if="indicatorOffset > 0" class="flex items-center gap-2 pb-2 text-xs">
        <Loader2 v-if="refreshing" class="size-4 animate-spin" />
        <RefreshCw
          v-else
          class="size-4 transition-transform duration-200"
          :class="{ 'rotate-180': arrowFlipped }"
        />
        <span v-if="refreshing">{{ t("pullToRefresh.refreshing") }}</span>
        <span v-else-if="arrowFlipped">{{ t("pullToRefresh.release") }}</span>
        <span v-else>{{ t("pullToRefresh.idle") }}</span>
      </div>
    </div>
    <slot />
  </div>
</template>

<style scoped>
/* The wrapper is the scroll container. Pin to vertical scrolling and
 * contain overscroll so horizontal flicks never drag the page and a
 * top overscroll doesn't chain to the document's native pull-to-refresh
 * (we render our own). */
.pull-to-refresh {
  touch-action: pan-y;
  overscroll-behavior: contain;
}
</style>
