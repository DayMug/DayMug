import { ref } from "vue";
import { reorderIds } from "./useAgentReorder";

// Touch-first long-press-then-drag reordering for the mobile agents list.
//
// ── Why this exists alongside useAgentReorder ────────────────────────────────
// The two look interchangeable — same 350ms/8px gesture, same
// `onReorder(ids)` contract, same PUT /api/user-order behind it, and they share
// `reorderIds`. They are NOT interchangeable, and merging them would regress a
// bug that was already fixed once (see the stutter note below):
//
//   useAgentReorder      — desktop rail. Reorders a reactive `liveOrder` that
//                          the component renders from, and lets
//                          <TransitionGroup> FLIP animate the motion. Pointer
//                          events + setPointerCapture.
//   useAgentDragReorder  — mobile list. The list order never changes during the
//                          drag; the dragged row is pinned and moved by an
//                          inline transform while neighbours slide. Raw touch
//                          events, so a non-passive touchmove can preventDefault
//                          against the scroll container only once a drag owns
//                          the gesture — and it exposes isEngaging() so the
//                          screen's pull-to-refresh can disarm itself.
//
// The difference is the *rendering* contract, not the platform: swapping one in
// for the other means rewriting the consuming template (render source, FLIP vs
// inline transforms, `[data-agent-id]` vs `:scope > [data-agent-wrap]`), not
// just changing an import.
//
// Smoothness is the whole point here, so the dragged row is taken OUT of the
// list's reorder flow for the duration of the drag: it stays pinned in its
// original DOM slot and is moved purely by a `translateY` transform that equals
// the finger delta (no transition → it tracks the finger 1:1, frame-for-frame).
// The neighbours it crosses are the only things that reorder, and they do so by
// sliding a gap (the dragged row's height) open with a short eased transition.
// On release the parent persists the new order and the list re-renders.
//
// An earlier version reordered a reactive `liveOrder` through a <TransitionGroup>
// with the dragged row still in the list. That made the dragged element get
// re-slotted (and FLIP-animated) every time the order changed, which fought its
// own finger transform and stuttered — exactly the "一顿一顿" the user saw. The
// pinned-row + neighbour-shift model below is what the conversation list uses
// and what makes it feel native.
//
// Raw touch events (not pointer events) are used deliberately: a non-passive
// touchmove lets us preventDefault to suppress the browser's scroll/pull only
// once a drag has actually started, while a move before the long-press matures
// bows out so the list still scrolls normally.
const LONG_PRESS_MS = 350;
const MOVE_TOLERANCE = 8;
const EASE = "transform 0.18s cubic-bezier(0.2, 0, 0, 1)";

export interface AgentDragReorderOptions {
  // Persist a new order after a drag that actually changed it.
  //
  // Note there is deliberately no `ids` input, unlike useAgentReorder: this
  // model reads the id list off the DOM at drag start (the `data-agent-wrap`
  // shells), because the geometry it captures has to come from the same source
  // as the order it reports. Passing a second, prop-derived list would let the
  // two disagree mid-drag.
  onReorder: (ids: string[]) => void;
}

interface ShellCapture {
  shells: HTMLElement[];
  ids: string[];
  // Viewport-space vertical centre of each row, captured at drag start. Used as
  // a stable threshold set so the drop index doesn't oscillate as neighbours
  // slide around.
  centres: number[];
  draggedHeight: number;
  fromIndex: number;
}

export function useAgentDragReorder(opts: AgentDragReorderOptions) {
  const draggingId = ref<string | null>(null);

  let activeId: string | null = null;
  let activeEl: HTMLElement | null = null;
  let mode: "idle" | "pending" | "drag" = "idle";
  let startX = 0;
  let startY = 0;
  let longPressTimer: ReturnType<typeof setTimeout> | null = null;
  let capture: ShellCapture | null = null;
  let curTarget = 0;
  // Set when a drag finishes so the trailing click doesn't also select/expand.
  let justGestured = false;

  function isDragging(id: string): boolean {
    return draggingId.value === id;
  }

  // True from the moment a press could still become a reorder (the long-press is
  // pending) through the end of the drag. Pull-to-refresh consults this so a
  // long-press on a row never also arms a pull — a pull would grow the indicator
  // and push the whole rail down mid-gesture, corrupting the captured geometry.
  function isEngaging(): boolean {
    return mode !== "idle";
  }

  // The component's @click consults this once: returns false (and clears the
  // flag) when the click is the tail of a just-finished drag.
  function shouldSelect(): boolean {
    if (justGestured) {
      justGestured = false;
      return false;
    }
    return true;
  }

  function clearTimer() {
    if (longPressTimer !== null) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  }

  function setTransform(el: HTMLElement | null, y: number, animate: boolean) {
    if (!el) return;
    el.style.transition = animate ? EASE : "none";
    el.style.transform = y === 0 ? "" : `translateY(${y}px)`;
  }

  function clearStyles(el: HTMLElement) {
    el.style.transition = "";
    el.style.transform = "";
    el.style.zIndex = "";
    el.style.position = "";
    el.style.willChange = "";
  }

  // Snapshot the sibling row shells (their ids, centres and the dragged row's
  // height) so the drag can shift neighbours and compute the drop index without
  // re-measuring on every move.
  function captureShells(): ShellCapture | null {
    if (!activeEl) return null;
    const container = activeEl.closest<HTMLElement>("[data-agent-rail]");
    if (!container) return null;
    const shells = Array.from(
      container.querySelectorAll<HTMLElement>(":scope > [data-agent-wrap]"),
    );
    const fromIndex = shells.indexOf(activeEl);
    if (fromIndex === -1) return null;
    const rects = shells.map((el) => el.getBoundingClientRect());
    return {
      shells,
      ids: shells.map((el) => el.dataset.agentWrap ?? ""),
      centres: rects.map((r) => r.top + r.height / 2),
      draggedHeight: rects[fromIndex].height,
      fromIndex,
    };
  }

  function enterDrag(id: string) {
    mode = "drag";
    draggingId.value = id;
    capture = captureShells();
    curTarget = capture?.fromIndex ?? 0;
    if (activeEl) {
      activeEl.style.willChange = "transform";
      activeEl.style.position = "relative";
      activeEl.style.zIndex = "30";
    }
  }

  // Open a gap (the dragged row's height) at the current drop target by sliding
  // the rows between the origin slot and the target.
  function applyNeighbourShift() {
    if (!capture) return;
    const { shells, fromIndex, draggedHeight } = capture;
    for (let i = 0; i < shells.length; i++) {
      if (i === fromIndex) continue;
      let shift = 0;
      if (curTarget > fromIndex && i > fromIndex && i <= curTarget) shift = -draggedHeight;
      else if (curTarget < fromIndex && i >= curTarget && i < fromIndex) shift = draggedHeight;
      setTransform(shells[i], shift, true);
    }
  }

  function updateDrag(clientY: number) {
    if (!capture) return;
    // The dragged row tracks the finger exactly (no transition).
    const delta = clientY - startY;
    setTransform(capture.shells[capture.fromIndex], delta, false);
    // Drop index = how many OTHER rows have their (stable) centre above the
    // dragged row's PROJECTED centre. Hit-testing against the row's own centre
    // (origin centre + finger delta) rather than the raw finger makes the drop
    // independent of where along the row the finger grabbed it: grabbing near a
    // row's edge would otherwise offset every threshold by that grab distance,
    // which left the end slots (notably the very first) effectively unreachable.
    const { centres, fromIndex } = capture;
    const draggedCentre = centres[fromIndex] + delta;
    let target = 0;
    for (let i = 0; i < centres.length; i++) {
      if (i === fromIndex) continue;
      if (centres[i] < draggedCentre) target++;
    }
    if (target !== curTarget) {
      curTarget = target;
      applyNeighbourShift();
    }
  }

  function finishDrag() {
    if (capture) {
      const { shells, ids, fromIndex } = capture;
      for (const el of shells) clearStyles(el);
      if (curTarget !== fromIndex) {
        opts.onReorder(reorderIds(ids, fromIndex, curTarget));
      }
    }
    justGestured = true;
  }

  function begin(id: string, el: HTMLElement | null, x: number, y: number) {
    activeId = id;
    activeEl = el;
    startX = x;
    startY = y;
    mode = "pending";
    capture = null;
    justGestured = false;
    clearTimer();
    longPressTimer = setTimeout(() => {
      if (mode === "pending") enterDrag(id);
    }, LONG_PRESS_MS);
  }

  // Returns true when the gesture claimed the move (so the caller suppresses
  // scrolling via preventDefault/stopPropagation).
  function move(x: number, y: number): boolean {
    if (mode === "pending") {
      // Movement before the hold matured means the user is scrolling, not
      // holding — abandon the would-be drag and let the list scroll.
      if (Math.abs(x - startX) > MOVE_TOLERANCE || Math.abs(y - startY) > MOVE_TOLERANCE) {
        reset();
      }
      return false;
    }
    if (mode === "drag") {
      updateDrag(y);
      return true;
    }
    return false;
  }

  function end() {
    if (mode === "drag") finishDrag();
    reset();
  }

  function reset() {
    clearTimer();
    teardownMouse();
    if (capture) {
      // A cancelled drag (no finishDrag) still needs its inline styles cleared.
      for (const el of capture.shells) clearStyles(el);
    }
    mode = "idle";
    activeId = null;
    activeEl = null;
    capture = null;
    draggingId.value = null;
  }

  // ── Touch ──────────────────────────────────────────────────────────────
  function onTouchStart(e: TouchEvent, id: string) {
    if (e.touches.length !== 1) return;
    begin(id, e.currentTarget as HTMLElement, e.touches[0].clientX, e.touches[0].clientY);
  }

  function onTouchMove(e: TouchEvent, id: string) {
    if (activeId !== id) return;
    if (move(e.touches[0].clientX, e.touches[0].clientY)) {
      // Non-passive listener: claim the gesture so the scroll container (and
      // its pull-to-refresh) don't also react to this move.
      if (e.cancelable) e.preventDefault();
      e.stopPropagation();
    }
  }

  function onTouchEnd(id: string) {
    if (activeId === id) end();
  }

  function onTouchCancel() {
    reset();
  }

  // ── Mouse (press-and-hold to drag) ───────────────────────────────────────
  function onMouseDown(e: MouseEvent, id: string) {
    if (e.button !== 0) return;
    begin(id, e.currentTarget as HTMLElement, e.clientX, e.clientY);
    window.addEventListener("mousemove", onWindowMouseMove);
    window.addEventListener("mouseup", onWindowMouseUp);
  }

  function onWindowMouseMove(e: MouseEvent) {
    if (activeId !== null && move(e.clientX, e.clientY)) e.preventDefault();
  }

  function onWindowMouseUp() {
    if (activeId !== null) end();
  }

  function teardownMouse() {
    window.removeEventListener("mousemove", onWindowMouseMove);
    window.removeEventListener("mouseup", onWindowMouseUp);
  }

  return {
    draggingId,
    isDragging,
    isEngaging,
    shouldSelect,
    onTouchStart,
    onTouchMove,
    onTouchEnd,
    onTouchCancel,
    onMouseDown,
  };
}
