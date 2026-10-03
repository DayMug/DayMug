import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { reorderIds, useAgentReorder } from "./useAgentReorder";

describe("reorderIds", () => {
  it("moves an item down", () => {
    expect(reorderIds(["a", "b", "c"], 0, 2)).toEqual(["b", "c", "a"]);
  });

  it("moves an item up", () => {
    expect(reorderIds(["a", "b", "c"], 2, 0)).toEqual(["c", "a", "b"]);
  });

  it("returns an unchanged copy for a no-op move", () => {
    const src = ["a", "b", "c"];
    const out = reorderIds(src, 1, 1);
    expect(out).toEqual(src);
    expect(out).not.toBe(src);
  });

  it("returns an unchanged copy for out-of-range indices", () => {
    expect(reorderIds(["a", "b"], 5, 0)).toEqual(["a", "b"]);
    expect(reorderIds(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  });
});

function setRect(el: HTMLElement, top: number, height: number) {
  el.getBoundingClientRect = () =>
    ({
      top,
      height,
      bottom: top + height,
      left: 0,
      right: 0,
      width: 0,
      x: 0,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
}

describe("useAgentReorder", () => {
  let rail: HTMLElement;
  let rows: Record<string, HTMLElement>;

  beforeEach(() => {
    vi.useFakeTimers();
    rail = document.createElement("div");
    rail.setAttribute("data-agent-rail", "");
    rows = {};
    ["a", "b", "c"].forEach((id, i) => {
      const el = document.createElement("div");
      el.setAttribute("data-agent-id", id);
      setRect(el, i * 30, 30);
      rail.appendChild(el);
      rows[id] = el;
    });
    document.body.appendChild(rail);
  });

  afterEach(() => {
    vi.useRealTimers();
    rail.remove();
  });

  function pointerDown(el: HTMLElement, clientY: number, pointerType = "mouse") {
    return {
      button: 0,
      clientX: 5,
      clientY,
      currentTarget: el,
      pointerType,
    } as unknown as PointerEvent;
  }

  function dispatch(type: string, clientY: number, clientX = 5) {
    const ev = new Event(type) as Event & { clientX: number; clientY: number };
    ev.clientX = clientX;
    ev.clientY = clientY;
    window.dispatchEvent(ev);
  }

  it("emits a reordered list after a long-press drag", () => {
    const onReorder = vi.fn();
    const r = useAgentReorder({ ids: () => ["a", "b", "c"], onReorder });

    r.onPointerDown(pointerDown(rows.a, 10), "a");
    vi.advanceTimersByTime(350);
    expect(r.draggingId.value).toBe("a");

    // Drag the pointer down past row c's midpoint.
    dispatch("pointermove", 80);
    expect(r.displayIds()).toEqual(["b", "c", "a"]);

    dispatch("pointerup", 80);
    expect(onReorder).toHaveBeenCalledWith(["b", "c", "a"]);
    // The trailing click is swallowed so the drag doesn't also select.
    expect(r.consumeSuppressedClick()).toBe(true);
    expect(r.consumeSuppressedClick()).toBe(false);
  });

  it("does not start a drag (or suppress click) for a quick tap", () => {
    const onReorder = vi.fn();
    const r = useAgentReorder({ ids: () => ["a", "b", "c"], onReorder });

    r.onPointerDown(pointerDown(rows.a, 10), "a");
    // Release before the long-press threshold.
    vi.advanceTimersByTime(100);
    dispatch("pointerup", 10);

    expect(r.draggingId.value).toBeNull();
    expect(onReorder).not.toHaveBeenCalled();
    expect(r.consumeSuppressedClick()).toBe(false);
  });

  it("keeps a mouse drag pending when the pointer moves before long-press", () => {
    const onReorder = vi.fn();
    const r = useAgentReorder({ ids: () => ["a", "b", "c"], onReorder });

    r.onPointerDown(pointerDown(rows.a, 10, "mouse"), "a");
    dispatch("pointermove", 40); // mouse drift before timer fires should not cancel
    vi.advanceTimersByTime(350);

    expect(r.draggingId.value).toBe("a");
    dispatch("pointermove", 80);
    dispatch("pointerup", 80);
    expect(onReorder).toHaveBeenCalledWith(["b", "c", "a"]);
  });

  it("cancels the pending touch drag when the pointer moves before long-press", () => {
    const onReorder = vi.fn();
    const r = useAgentReorder({ ids: () => ["a", "b", "c"], onReorder });

    r.onPointerDown(pointerDown(rows.a, 10, "touch"), "a");
    dispatch("pointermove", 40); // > MOVE_CANCEL_PX before timer fires
    vi.advanceTimersByTime(350);

    expect(r.draggingId.value).toBeNull();
    dispatch("pointerup", 40);
    expect(onReorder).not.toHaveBeenCalled();
  });
});
