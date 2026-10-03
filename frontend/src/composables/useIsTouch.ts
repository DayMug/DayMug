import { ref, onMounted, onUnmounted } from "vue";

// Reactive "is this a touch device?" — distinct from useIsMobile, which is
// viewport-width based. A tablet in landscape is wide (not "mobile") but still
// touch-first, and we want the touch affordances (swipe-to-delete) there while
// hiding the hover/click delete button that only makes sense with a pointer.
//
// `(pointer: coarse)` matches when the primary input is a touch screen.
export function useIsTouch() {
  // Resolved during setup for the same reason as useIsMobile: a value that only
  // arrives in onMounted makes the first render claim a mouse, so touch-only
  // affordances flicker in on the second frame.
  const mql =
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia("(pointer: coarse)")
      : null;
  const isTouch = ref(mql?.matches ?? false);

  function update(e: MediaQueryListEvent | MediaQueryList) {
    isTouch.value = e.matches;
  }

  onMounted(() => {
    if (!mql) return;
    isTouch.value = mql.matches;
    mql.addEventListener("change", update);
  });

  onUnmounted(() => {
    mql?.removeEventListener("change", update);
  });

  return { isTouch };
}
