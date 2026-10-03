import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useAgentDragReorder } from "./useAgentDragReorder";

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

describe("useAgentDragReorder", () => {
  let rail: HTMLElement;
  let wraps: Record<string, HTMLElement>;

  beforeEach(() => {
    vi.useFakeTimers();
    rail = document.createElement("div");
    rail.setAttribute("data-agent-rail", "");
    wraps = {};
    ["a", "b", "c"].forEach((id, i) => {
      const el = document.createElement("div");
      el.setAttribute("data-agent-wrap", id);
      setRect(el, i * 30, 30); // rows at 0/30/60, centres 15/45/75
      rail.appendChild(el);
      wraps[id] = el;
    });
    document.body.appendChild(rail);
  });

  afterEach(() => {
    vi.useRealTimers();
    rail.remove();
  });

  function touch(type: string, clientY: number, currentTarget?: HTMLElement): TouchEvent {
    const ev = new Event(type, { cancelable: true }) as TouchEvent;
    Object.defineProperty(ev, "touches", { value: [{ clientX: 5, clientY }] });
    if (currentTarget) Object.defineProperty(ev, "currentTarget", { value: currentTarget });
    return ev;
  }

  it("makes the dragged row track the finger 1:1 via a transform", () => {
    const r = useAgentDragReorder({ onReorder: vi.fn() });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    vi.advanceTimersByTime(350);
    expect(r.draggingId.value).toBe("a");

    r.onTouchMove(touch("touchmove", 40), "a");
    // Finger moved 30px down, so the row is offset 30px with no transition.
    expect(wraps.a.style.transform).toBe("translateY(30px)");
    expect(wraps.a.style.transition).toBe("none");

    r.onTouchMove(touch("touchmove", 55), "a");
    expect(wraps.a.style.transform).toBe("translateY(45px)");
  });

  it("emits a reordered list after dragging past the neighbours", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    vi.advanceTimersByTime(350);
    r.onTouchMove(touch("touchmove", 80), "a"); // past b (45) and c (75)
    r.onTouchEnd("a");

    expect(onReorder).toHaveBeenCalledWith(["b", "c", "a"]);
    // The trailing click is swallowed exactly once so the drag doesn't expand.
    expect(r.shouldSelect()).toBe(false);
    expect(r.shouldSelect()).toBe(true);
    // Inline transforms are cleared on drop.
    expect(wraps.a.style.transform).toBe("");
  });

  it("drags a row up into the very first slot", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    // Grab c at its centre (75) and drag up so its projected centre clears both
    // other rows' centres (15, 45) -> lands at index 0.
    r.onTouchStart(touch("touchstart", 75, wraps.c), "c");
    vi.advanceTimersByTime(350);
    r.onTouchMove(touch("touchmove", 10), "c");
    r.onTouchEnd("c");

    expect(onReorder).toHaveBeenCalledWith(["c", "a", "b"]);
  });

  it("reaches the first slot regardless of where the row was grabbed", () => {
    // Regression: the drop index must follow the dragged row's projected centre,
    // not the raw finger. Grab c near its BOTTOM edge (88) and drag the finger
    // up to 20: the row's centre (75 + (20 - 88) = 7) clears a's centre (15), so
    // c belongs in slot 0. A raw-finger hit-test would see finger 20 > a's centre
    // 15 and wrongly land it in slot 1 (["a", "c", "b"]) — the bug that made the
    // first position unreachable when grabbing a row off-centre.
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 88, wraps.c), "c");
    vi.advanceTimersByTime(350);
    r.onTouchMove(touch("touchmove", 20), "c");
    r.onTouchEnd("c");

    expect(onReorder).toHaveBeenCalledWith(["c", "a", "b"]);
  });

  it("moves the first row down when grabbed near its top edge", () => {
    // The mirror case: grabbing the first row near its top (2) and dragging down
    // to 40 projects its centre to 15 + (40 - 2) = 53, past b's centre (45), so
    // it should swap below b. Anchoring to the finger alone would need a much
    // longer drag, which is why the first row felt immovable.
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 2, wraps.a), "a");
    vi.advanceTimersByTime(350);
    r.onTouchMove(touch("touchmove", 40), "a");
    r.onTouchEnd("a");

    expect(onReorder).toHaveBeenCalledWith(["b", "a", "c"]);
  });

  it("reports engaging from the pending long-press through the drag, then idle", () => {
    const r = useAgentDragReorder({ onReorder: vi.fn() });
    expect(r.isEngaging()).toBe(false);

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    // Pending (long-press not yet matured) already counts as engaging so
    // pull-to-refresh defers to it.
    expect(r.isEngaging()).toBe(true);

    vi.advanceTimersByTime(350);
    expect(r.isEngaging()).toBe(true);

    r.onTouchEnd("a");
    expect(r.isEngaging()).toBe(false);
  });

  it("stops engaging once the press bows out to a scroll", () => {
    const r = useAgentDragReorder({ onReorder: vi.fn() });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    expect(r.isEngaging()).toBe(true);
    // Moving past tolerance before the long-press matures abandons the gesture.
    r.onTouchMove(touch("touchmove", 40), "a");
    expect(r.isEngaging()).toBe(false);
  });

  it("does not drag (or suppress the click) for a quick tap", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    vi.advanceTimersByTime(100);
    r.onTouchEnd("a");

    expect(r.draggingId.value).toBeNull();
    expect(onReorder).not.toHaveBeenCalled();
    expect(r.shouldSelect()).toBe(true);
  });

  it("bows out to a scroll when the finger moves before the long-press matures", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    r.onTouchMove(touch("touchmove", 40), "a"); // > tolerance before the timer fires
    vi.advanceTimersByTime(350);

    expect(r.draggingId.value).toBeNull();
    r.onTouchEnd("a");
    expect(onReorder).not.toHaveBeenCalled();
  });

  it("does not emit when released without crossing a neighbour", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    vi.advanceTimersByTime(350);
    r.onTouchMove(touch("touchmove", 40), "a"); // still above b's centre (45)
    r.onTouchEnd("a");

    expect(onReorder).not.toHaveBeenCalled();
  });

  it("clears inline transforms when a drag is cancelled", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    r.onTouchStart(touch("touchstart", 10, wraps.a), "a");
    vi.advanceTimersByTime(350);
    r.onTouchMove(touch("touchmove", 80), "a");
    expect(wraps.a.style.transform).toBe("translateY(70px)");

    r.onTouchCancel();
    expect(onReorder).not.toHaveBeenCalled();
    expect(wraps.a.style.transform).toBe("");
    expect(r.draggingId.value).toBeNull();
  });

  it("reorders via a mouse press-and-hold drag", () => {
    const onReorder = vi.fn();
    const r = useAgentDragReorder({ onReorder });

    const down = {
      button: 0,
      clientX: 5,
      clientY: 10,
      currentTarget: wraps.a,
    } as unknown as MouseEvent;
    r.onMouseDown(down, "a");
    vi.advanceTimersByTime(350);
    expect(r.draggingId.value).toBe("a");

    window.dispatchEvent(Object.assign(new Event("mousemove"), { clientX: 5, clientY: 80 }));
    window.dispatchEvent(new Event("mouseup"));
    expect(onReorder).toHaveBeenCalledWith(["b", "c", "a"]);
  });
});
