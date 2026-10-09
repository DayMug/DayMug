import { ref } from "vue";

// reorderIds returns a copy of `ids` with the item at `from` moved to `to`.
// Out-of-range / no-op moves return a shallow copy unchanged. Pure — unit
// tested independently of the pointer machinery.
export function reorderIds(ids: string[], from: number, to: number): string[] {
  const next = ids.slice();
  if (from === to || from < 0 || to < 0 || from >= next.length || to >= next.length) {
    return next;
  }
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved);
  return next;
}

// Hold this long before a press turns into a reorder drag. Below it, a press is
// a tap (select). Touch movement beyond MOVE_CANCEL_PX before the timer fires
// cancels the would-be drag so the rail can still scroll. Mouse/pen movement is
// allowed during the hold because users often start moving before the threshold.
const LONG_PRESS_MS = 350;
const MOVE_CANCEL_PX = 8;

export interface AgentReorderOptions {
  // Current agent ids in display order. Read lazily so the composable always
  // sees the latest list.
  ids: () => string[];
  // Persist a new order after a drag that actually changed it.
  onReorder: (ids: string[]) => void;
}

// useAgentReorder implements long-press-then-drag reordering for the vertical
// agent rail (SidebarPanel). It owns no list state of its own: during a drag it
// exposes a `displayIds()` override that the component renders, and emits the
// final order via onReorder on drop. Selection (tap) stays with the component's
// @click, which calls consumeSuppressedClick() to ignore the click that fires
// right after a drag.
//
// ── Why this exists alongside useAgentDragReorder ────────────────────────────
// Same gesture thresholds, same `onReorder(ids)` contract, same persistence
// path, and they share `reorderIds` — but they are not interchangeable. This one
// drives motion by reordering a reactive list and letting <TransitionGroup>
// FLIP animate it, which is fine under a mouse but stuttered badly under a
// finger. useAgentDragReorder is the touch answer to exactly that problem and
// keeps the list order frozen during the drag instead. Read the header comment
// there before attempting to collapse the two: the divergence is in the
// rendering contract each imposes on its consumer, not in what they compute.
export function useAgentReorder(opts: AgentReorderOptions) {
  const draggingId = ref<string | null>(null);
  const liveOrder = ref<string[] | null>(null);

  let pressTimer: ReturnType<typeof setTimeout> | null = null;
  let startX = 0;
  let startY = 0;
  let moved = false;
  let containerEl: HTMLElement | null = null;
  let pointerEl: HTMLElement | null = null;
  let pointerId: number | null = null;
  let pointerType = "mouse";
  let suppressClick = false;

  function displayIds(): string[] {
    return liveOrder.value ?? opts.ids();
  }

  function isDragging(id: string): boolean {
    return draggingId.value === id;
  }

  // The component's @click consults this once per click: returns true (and
  // clears the flag) when the click is the tail of a just-finished drag and
  // should not select.
  function consumeSuppressedClick(): boolean {
    if (suppressClick) {
      suppressClick = false;
      return true;
    }
    return false;
  }

  function teardown() {
    if (pressTimer) {
      clearTimeout(pressTimer);
      pressTimer = null;
    }
    if (pointerEl && pointerId !== null && pointerEl.hasPointerCapture?.(pointerId)) {
      pointerEl.releasePointerCapture(pointerId);
    }
    pointerEl = null;
    pointerId = null;
    pointerType = "mouse";
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", onUp);
    window.removeEventListener("pointercancel", onCancel);
  }

  function selectorForAgentId(id: string): string {
    const escaped = globalThis.CSS?.escape ? globalThis.CSS.escape(id) : id.replace(/"/g, '\\"');
    return `[data-agent-id="${escaped}"]`;
  }

  function rowIndexAt(clientY: number, order: string[]): number {
    if (!containerEl) return -1;
    for (let i = 0; i < order.length; i++) {
      const el = containerEl.querySelector<HTMLElement>(selectorForAgentId(order[i]));
      if (!el) continue;
      const r = el.getBoundingClientRect();
      if (clientY < r.top + r.height / 2) return i;
    }
    return order.length - 1;
  }

  function onPointerDown(e: PointerEvent, id: string) {
    // Left button / touch / pen only — ignore right-click (that opens the
    // context menu) and middle-click.
    if (e.button !== 0) return;
    moved = false;
    startX = e.clientX;
    startY = e.clientY;
    containerEl = (e.currentTarget as HTMLElement).closest<HTMLElement>("[data-agent-rail]");
    pointerEl = e.currentTarget as HTMLElement;
    pointerId = typeof e.pointerId === "number" ? e.pointerId : null;
    pointerType = e.pointerType || "mouse";
    if (pointerId !== null) {
      pointerEl.setPointerCapture?.(pointerId);
    }
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onCancel);
    pressTimer = setTimeout(() => {
      pressTimer = null;
      draggingId.value = id;
      liveOrder.value = opts.ids().slice();
    }, LONG_PRESS_MS);
  }

  function onMove(e: PointerEvent) {
    if (draggingId.value === null) {
      // Still in the long-press window: meaningful movement means the user is
      // scrolling, not holding — abandon the would-be drag on touch only.
      if (
        pointerType === "touch" &&
        Math.hypot(e.clientX - startX, e.clientY - startY) > MOVE_CANCEL_PX
      ) {
        teardown();
      }
      return;
    }
    moved = true;
    e.preventDefault();
    const order = displayIds();
    const fromIdx = order.indexOf(draggingId.value);
    const toIdx = rowIndexAt(e.clientY, order);
    if (fromIdx >= 0 && toIdx >= 0 && toIdx !== fromIdx) {
      liveOrder.value = reorderIds(order, fromIdx, toIdx);
    }
  }

  function onUp() {
    const wasDragging = draggingId.value !== null;
    const finalOrder = liveOrder.value;
    teardown();
    draggingId.value = null;
    liveOrder.value = null;
    if (wasDragging) {
      // A real drag (entered drag mode) must not also select on the trailing
      // click event.
      suppressClick = true;
      if (moved && finalOrder) {
        const original = opts.ids();
        if (finalOrder.join("\0") !== original.join("\0")) {
          opts.onReorder(finalOrder);
        }
      }
    }
  }

  function onCancel() {
    teardown();
    draggingId.value = null;
    liveOrder.value = null;
  }

  return { draggingId, displayIds, isDragging, onPointerDown, consumeSuppressedClick };
}
