import { ref } from "vue";

// Gestures for the conversation list, for touch AND mouse:
//
//   • Long-press (touch) or press-and-hold (mouse), then drag vertically to
//     reorder. Dragging a row up into the pinned block pins it; dragging it
//     below the pinned block unpins it; dragging among pinned rows reorders
//     them. Neighbours slide out of the way to show the drop slot.
//   • Swipe LEFT (touch only) — reveals share/unshare and delete actions behind
//     the row (Gmail style). Mouse keeps the hover/context-menu actions instead.
//
// Smoothness: per-move transforms are written straight to DOM elements (not a
// reactive ref), so a drag/swipe doesn't re-render the whole list on every
// move. Reactive state flips only at gesture boundaries.

export interface ConversationGestureOptions {
  // Persist a new pinned set/order after a reorder drag. `pinnedIds` is the full
  // ordered pinned set (authoritative): ids omitted become unpinned.
  onReorder: (pinnedIds: string[]) => void;
  onDelete: (id: string) => void;
  onShare?: (id: string) => void;
}

type Mode = "idle" | "pending" | "swipe" | "drag";

// Width of a single revealed swipe action, and total reveal width for the
// share/unshare + delete pair.
export const SWIPE_ACTION_WIDTH = 72;
export const SWIPE_WIDTH = SWIPE_ACTION_WIDTH * 2;

const LONG_PRESS_MS = 400;
const MOVE_TOLERANCE = 8;
const SWIPE_COMMIT = SWIPE_WIDTH / 2;
const EASE = "transform 0.35s ease";

// computeReorderedPinnedIds returns the new authoritative pinned-id set after
// moving the row at `from` to `to`. The list is always pinned-block-first, so:
//   • B = number of pinned rows other than the dragged one.
//   • The dragged row is pinned iff it lands at or above the pinned boundary
//     (to <= B) — i.e. dragged up into the pinned region.
// Pure (no DOM) so it can be unit-tested directly.
export function computeReorderedPinnedIds(
  ids: string[],
  pinned: boolean[],
  from: number,
  to: number,
): string[] {
  const order = ids.slice();
  const [moved] = order.splice(from, 1);
  order.splice(to, 0, moved);
  let b = 0;
  for (let i = 0; i < ids.length; i++) {
    if (i !== from && pinned[i]) b++;
  }
  const draggedPinned = to <= b;
  return order.slice(0, draggedPinned ? b + 1 : b);
}

interface ShellCapture {
  shells: HTMLElement[];
  ids: string[];
  pinned: boolean[];
  fromIndex: number;
  rowStep: number;
}

export function useConversationGestures(opts: ConversationGestureOptions) {
  const swipedId = ref<string | null>(null);
  const swipingId = ref<string | null>(null);
  const draggingId = ref<string | null>(null);

  // Non-reactive gesture bookkeeping.
  let activeId: string | null = null;
  let activeEl: HTMLElement | null = null;
  let mode: Mode = "idle";
  let pointerKind: "touch" | "mouse" = "touch";
  let startX = 0;
  let startY = 0;
  let dx = 0;
  let dy = 0;
  let longPressTimer: ReturnType<typeof setTimeout> | null = null;
  let justGestured = false;
  let capture: ShellCapture | null = null;
  let curTarget = 0;
  // The topmost row can't be dragged any higher, so when nothing is pinned yet a
  // clear upward drag of row 0 can't change `curTarget` to cross into the pinned
  // block. This flag records that pin intent so finishDrag still persists it.
  let forcePin = false;

  function clearTimer() {
    if (longPressTimer !== null) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  }

  function setTransform(el: HTMLElement | null, x: number, y: number, animate: boolean) {
    if (!el) return;
    el.style.transition = animate ? EASE : "none";
    el.style.transform = `translate(${x}px, ${y}px)`;
  }

  // Snapshot the sibling row shells (and their pinned flags + step height) so
  // the reorder drag can shift neighbours and compute the drop index.
  function captureShells(): ShellCapture | null {
    if (!activeEl) return null;
    const shell = activeEl.closest<HTMLElement>("[data-conv-shell]");
    const container = shell?.parentElement;
    if (!shell || !container) return null;
    const shells = Array.from(
      container.querySelectorAll<HTMLElement>(":scope > [data-conv-shell]"),
    );
    const fromIndex = shells.indexOf(shell);
    if (fromIndex === -1) return null;
    const rects = shells.map((el) => el.getBoundingClientRect());
    const rowStep = shells.length > 1 ? rects[1].top - rects[0].top : rects[0].height;
    return {
      shells,
      ids: shells.map((el) => el.dataset.convId ?? ""),
      pinned: shells.map((el) => el.dataset.pinned === "1"),
      fromIndex,
      rowStep: rowStep || 1,
    };
  }

  function enterDrag(id: string) {
    mode = "drag";
    draggingId.value = id;
    capture = captureShells();
    curTarget = capture?.fromIndex ?? 0;
    forcePin = false;
    const shell = capture?.shells[capture.fromIndex] ?? activeEl;
    if (shell) shell.style.zIndex = "30";
  }

  // Shift the neighbour shells to open a gap at the current drop target.
  function applyNeighbourShift() {
    if (!capture) return;
    const { shells, fromIndex, rowStep } = capture;
    for (let i = 0; i < shells.length; i++) {
      if (i === fromIndex) continue;
      let shift = 0;
      if (curTarget > fromIndex && i > fromIndex && i <= curTarget) shift = -rowStep;
      else if (curTarget < fromIndex && i < fromIndex && i >= curTarget) shift = rowStep;
      setTransform(shells[i], 0, shift, true);
    }
  }

  function updateDrag() {
    const shell = capture?.shells[capture.fromIndex] ?? activeEl;
    setTransform(shell, 0, dy, false);
    if (!capture) return;
    const next = Math.max(
      0,
      Math.min(capture.shells.length - 1, capture.fromIndex + Math.round(dy / capture.rowStep)),
    );
    // Pin the very first row when nothing is pinned: it's already at the top and
    // can't move up to enter the pinned block, so read a deliberate upward drag
    // (half a row) as the intent to pin it.
    forcePin = capture.fromIndex === 0 && !capture.pinned[0] && dy <= -capture.rowStep / 2;
    if (next !== curTarget) {
      curTarget = next;
      applyNeighbourShift();
    }
  }

  function finishDrag() {
    if (capture) {
      const { shells, ids, pinned, fromIndex } = capture;
      // Clear all inline transforms; the list re-renders in the new order.
      for (const el of shells) {
        el.style.transition = "";
        el.style.transform = "";
        el.style.zIndex = "";
        el.style.willChange = "";
      }
      if (curTarget !== fromIndex || forcePin) {
        opts.onReorder(computeReorderedPinnedIds(ids, pinned, fromIndex, curTarget));
      }
    }
    capture = null;
    justGestured = true;
  }

  function endGesture() {
    if (activeEl) {
      activeEl.style.transition = EASE;
      activeEl.style.willChange = "";
    }
    clearTimer();
    teardownMouse();
    swipingId.value = null;
    draggingId.value = null;
    mode = "idle";
    activeId = null;
    activeEl = null;
    capture = null;
    forcePin = false;
    dx = 0;
    dy = 0;
  }

  // ── Touch ────────────────────────────────────────────────────────────
  function onTouchStart(e: TouchEvent, id: string) {
    if (e.touches.length !== 1) return;
    begin(id, e.currentTarget as HTMLElement, e.touches[0].clientX, e.touches[0].clientY, "touch");
  }

  function onTouchMove(e: TouchEvent, id: string) {
    if (activeId !== id) return;
    const handled = move(e.touches[0].clientX, e.touches[0].clientY);
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  }

  function onTouchEnd(id: string) {
    if (activeId !== id) return;
    end();
  }

  function onTouchCancel() {
    cancel();
  }

  // ── Mouse (press-and-hold to drag-reorder; no swipe) ───────────────────
  function onMouseDown(e: MouseEvent, id: string) {
    if (e.button !== 0) return;
    begin(id, e.currentTarget as HTMLElement, e.clientX, e.clientY, "mouse");
    window.addEventListener("mousemove", onWindowMouseMove);
    window.addEventListener("mouseup", onWindowMouseUp);
  }

  function onWindowMouseMove(e: MouseEvent) {
    if (activeId === null) return;
    if (move(e.clientX, e.clientY)) e.preventDefault();
  }

  function onWindowMouseUp() {
    if (activeId !== null) end();
  }

  function teardownMouse() {
    window.removeEventListener("mousemove", onWindowMouseMove);
    window.removeEventListener("mouseup", onWindowMouseUp);
  }

  // ── Shared gesture core ────────────────────────────────────────────────
  function begin(
    id: string,
    el: HTMLElement | null,
    x: number,
    y: number,
    kind: "touch" | "mouse",
  ) {
    if (swipedId.value && swipedId.value !== id) swipedId.value = null;
    activeEl = el;
    pointerKind = kind;
    if (activeEl) activeEl.style.willChange = "transform";
    startX = x;
    startY = y;
    activeId = id;
    mode = "pending";
    dx = swipedId.value === id ? -SWIPE_WIDTH : 0;
    dy = 0;
    justGestured = false;
    clearTimer();
    longPressTimer = setTimeout(() => {
      if (mode === "pending") enterDrag(id);
    }, LONG_PRESS_MS);
  }

  // Returns whether the gesture claimed the event (so the caller can
  // preventDefault / stopPropagation to suppress scrolling).
  function move(x: number, y: number): boolean {
    const mdx = x - startX;
    const mdy = y - startY;

    if (mode === "pending") {
      if (Math.abs(mdx) <= MOVE_TOLERANCE && Math.abs(mdy) <= MOVE_TOLERANCE) return false;
      clearTimer();
      if (pointerKind === "touch" && Math.abs(mdx) > Math.abs(mdy)) {
        mode = "swipe";
        swipingId.value = activeId;
      } else {
        // Vertical (or mouse) movement before the hold matured: for touch this
        // is a scroll, so bow out; for mouse there's nothing to scroll, so let
        // it pass as a plain click/no-op.
        endGesture();
        return false;
      }
    }

    if (mode === "swipe") {
      const base = swipedId.value === activeId ? -SWIPE_WIDTH : 0;
      dx = Math.min(0, Math.max(-SWIPE_WIDTH, base + mdx));
      setTransform(activeEl, dx, 0, false);
      return true;
    }
    if (mode === "drag") {
      dy = mdy;
      updateDrag();
      return true;
    }
    return false;
  }

  function end() {
    if (mode === "swipe") {
      const open = dx <= -SWIPE_COMMIT;
      setTransform(activeEl, open ? -SWIPE_WIDTH : 0, 0, true);
      swipedId.value = open ? activeId : swipedId.value === activeId ? null : swipedId.value;
      justGestured = true;
    } else if (mode === "drag") {
      finishDrag();
    }
    endGesture();
  }

  function cancel() {
    if (mode === "swipe")
      setTransform(activeEl, swipedId.value === activeId ? -SWIPE_WIDTH : 0, 0, true);
    if (mode === "drag" && capture) {
      for (const el of capture.shells) {
        el.style.transition = "";
        el.style.transform = "";
        el.style.zIndex = "";
        el.style.willChange = "";
      }
    }
    endGesture();
  }

  // ── Click coordination ─────────────────────────────────────────────────
  function shouldSelect(id: string): boolean {
    if (justGestured) {
      justGestured = false;
      return false;
    }
    if (swipedId.value === id) {
      swipedId.value = null;
      return false;
    }
    return true;
  }

  function closeSwipe() {
    swipedId.value = null;
  }

  function deleteSwiped(id: string) {
    swipedId.value = null;
    justGestured = true;
    opts.onDelete(id);
  }

  function shareSwiped(id: string) {
    swipedId.value = null;
    justGestured = true;
    opts.onShare?.(id);
  }

  function rowStyle(id: string): Record<string, string> {
    return {
      transform:
        swipedId.value === id ? `translate(${-SWIPE_WIDTH}px, 0px)` : "translate(0px, 0px)",
      transition: EASE,
    };
  }

  function isDragging(id: string): boolean {
    return draggingId.value === id;
  }

  function isDraggingAny(): boolean {
    return draggingId.value !== null;
  }

  function showAction(id: string): boolean {
    return swipingId.value === id || swipedId.value === id;
  }

  return {
    swipedId,
    onTouchStart,
    onTouchMove,
    onTouchEnd,
    onTouchCancel,
    onMouseDown,
    shouldSelect,
    closeSwipe,
    deleteSwiped,
    shareSwiped,
    rowStyle,
    isDragging,
    isDraggingAny,
    showAction,
  };
}
