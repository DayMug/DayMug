import { ref } from "vue";
import type { FileEntry } from "@/composables/useFileApi";

// Long-press for touch devices. iOS Safari does not reliably dispatch a
// `contextmenu` event on plain divs, so we implement the gesture ourselves
// via pointer events: on a touch pointerdown, arm a 500ms timer; cancel it
// on move / up / cancel; if it fires, invoke `onLongPress` (the workspace
// panel opens the same FileContextMenu the desktop right-click flow uses).
const LONG_PRESS_MS = 500;
const LONG_PRESS_MOVE_TOLERANCE_PX = 10;

export interface LongPressContext {
  entry: FileEntry | null;
  path: string;
  x: number;
  y: number;
}

export interface UseWorkspaceLongPressOptions {
  onLongPress: (ctx: LongPressContext) => void;
}

// Instance-scoped (call inside setup()) — each mounted panel gets its own
// timer state machine.
export function useWorkspaceLongPress({ onLongPress }: UseWorkspaceLongPressOptions) {
  const longPressTimer = ref<ReturnType<typeof setTimeout> | null>(null);
  const longPressOrigin = ref<{ x: number; y: number } | null>(null);
  // Set to true when the timer actually fires; consumed by the click guard
  // below so the synthetic click iOS dispatches after a long-press doesn't
  // also navigate into the entry.
  const longPressFired = ref(false);

  function clearLongPressTimer() {
    if (longPressTimer.value) {
      clearTimeout(longPressTimer.value);
      longPressTimer.value = null;
    }
    longPressOrigin.value = null;
  }

  function startLongPress(event: PointerEvent, entry: FileEntry | null, path: string) {
    if (event.pointerType !== "touch") return;
    clearLongPressTimer();
    longPressFired.value = false;
    const startX = event.clientX;
    const startY = event.clientY;
    longPressOrigin.value = { x: startX, y: startY };
    longPressTimer.value = setTimeout(() => {
      longPressTimer.value = null;
      longPressFired.value = true;
      onLongPress({ entry, path, x: startX, y: startY });
    }, LONG_PRESS_MS);
  }

  function moveLongPress(event: PointerEvent) {
    if (!longPressOrigin.value || !longPressTimer.value) return;
    const dx = event.clientX - longPressOrigin.value.x;
    const dy = event.clientY - longPressOrigin.value.y;
    if (Math.hypot(dx, dy) > LONG_PRESS_MOVE_TOLERANCE_PX) {
      clearLongPressTimer();
    }
  }

  function endLongPress() {
    clearLongPressTimer();
  }

  // Wraps the existing tap handlers so the synthetic click iOS fires after a
  // long-press is dropped on the floor. Returns true when the click should
  // be suppressed; callers `return` early in that case.
  function consumeLongPressClick(): boolean {
    if (longPressFired.value) {
      longPressFired.value = false;
      return true;
    }
    return false;
  }

  return {
    startLongPress,
    moveLongPress,
    endLongPress,
    consumeLongPressClick,
  };
}
