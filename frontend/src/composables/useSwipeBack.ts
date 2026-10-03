import { computed, ref } from "vue";

// iOS-style edge-swipe back gesture for mobile screens: drag from the left
// edge toward the right to pop back to the previous screen (here, chat →
// conversation list). Touch-only — pointer/mouse never fires these handlers.
//
// The gesture must START near the left edge so it doesn't fight horizontal
// scrolling inside the content (code blocks, file tables sit away from the
// edge). Once a rightward, horizontal-dominant drag is locked in we follow the
// finger; releasing past the commit distance pops, otherwise it snaps back.

export interface SwipeBackOptions {
  // How close to the left edge (px) a touch must start to arm the gesture.
  edge?: number;
  // Rightward travel (px) needed on release to count as a back navigation.
  commit?: number;
}

const DEFAULT_EDGE = 30;
const DEFAULT_COMMIT = 80;
// Movement tolerated before we lock the gesture axis.
const MOVE_TOLERANCE = 10;

export function useSwipeBack(onBack: () => void, options: SwipeBackOptions = {}) {
  const edge = options.edge ?? DEFAULT_EDGE;
  const commit = options.commit ?? DEFAULT_COMMIT;

  const dx = ref(0);
  const active = ref(false);
  let startX = 0;
  let startY = 0;
  let decided = false;
  let horizontal = false;

  function onTouchStart(e: TouchEvent) {
    if (e.touches.length !== 1) return;
    const touch = e.touches[0];
    if (touch.clientX > edge) return; // only arm from the left edge
    startX = touch.clientX;
    startY = touch.clientY;
    active.value = true;
    decided = false;
    horizontal = false;
    dx.value = 0;
  }

  function onTouchMove(e: TouchEvent) {
    if (!active.value) return;
    const touch = e.touches[0];
    const ddx = touch.clientX - startX;
    const ddy = touch.clientY - startY;

    if (!decided) {
      if (Math.abs(ddx) <= MOVE_TOLERANCE && Math.abs(ddy) <= MOVE_TOLERANCE) return;
      decided = true;
      // Commit only to a clearly rightward, horizontal-dominant drag; anything
      // else is a vertical scroll, so bow out and let the content scroll.
      horizontal = Math.abs(ddx) > Math.abs(ddy) && ddx > 0;
      if (!horizontal) {
        active.value = false;
        dx.value = 0;
        return;
      }
    }

    if (horizontal) {
      e.preventDefault();
      dx.value = Math.max(0, ddx);
    }
  }

  function onTouchEnd() {
    if (!active.value) return;
    const committed = horizontal && dx.value >= commit;
    active.value = false;
    dx.value = 0;
    if (committed) onBack();
  }

  function onTouchCancel() {
    active.value = false;
    dx.value = 0;
  }

  // Bound to the screen container so it tracks the finger, then eases back to
  // rest (or stays put as the screen unmounts on a committed pop).
  const style = computed<Record<string, string>>(() => {
    if (active.value && dx.value > 0) {
      return { transform: `translateX(${dx.value}px)`, transition: "none" };
    }
    return { transform: "translateX(0px)", transition: "transform 0.2s ease" };
  });

  return { onTouchStart, onTouchMove, onTouchEnd, onTouchCancel, style, dx, active };
}
