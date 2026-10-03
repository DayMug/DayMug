import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  useConversationGestures,
  computeReorderedPinnedIds,
  SWIPE_WIDTH,
} from "./useConversationGestures";

function touch(x: number, y: number): TouchEvent {
  return {
    touches: [{ clientX: x, clientY: y }],
    preventDefault: () => {},
    stopPropagation: () => {},
  } as unknown as TouchEvent;
}

describe("computeReorderedPinnedIds", () => {
  // ids are in visual order (pinned block first); pinned[i] mirrors them.
  it("pins an unpinned row dragged to the top", () => {
    const ids = ["a", "b", "c"];
    const pinned = [false, false, false];
    // Drag c (index 2) to the very top (index 0).
    expect(computeReorderedPinnedIds(ids, pinned, 2, 0)).toEqual(["c"]);
  });

  it("reorders within the pinned block", () => {
    const ids = ["a", "b", "c", "d"];
    const pinned = [true, true, false, false];
    // Move b (1) above a (0): pinned set becomes [b, a].
    expect(computeReorderedPinnedIds(ids, pinned, 1, 0)).toEqual(["b", "a"]);
  });

  it("unpins a pinned row dragged into the unpinned region", () => {
    const ids = ["a", "b", "c", "d"];
    const pinned = [true, true, false, false];
    // Drag a (0) down to index 3 (below the pinned boundary B=1): a unpins,
    // leaving only b pinned.
    expect(computeReorderedPinnedIds(ids, pinned, 0, 3)).toEqual(["b"]);
  });

  it("keeps the pinned set when a pinned row stays within the block", () => {
    const ids = ["a", "b", "c"];
    const pinned = [true, true, false];
    expect(computeReorderedPinnedIds(ids, pinned, 0, 1)).toEqual(["b", "a"]);
  });
});

// Build a vertical list of conversation row shells the gesture code can capture
// (it climbs from the touch target to `[data-conv-shell]` and reads its
// siblings). Returns the inner element to use as the touch currentTarget.
function buildRows(rows: Array<{ id: string; pinned?: boolean }>): {
  container: HTMLElement;
  inner: (id: string) => HTMLElement;
} {
  const container = document.createElement("div");
  const inners: Record<string, HTMLElement> = {};
  for (const row of rows) {
    const shell = document.createElement("div");
    shell.setAttribute("data-conv-shell", "");
    shell.dataset.convId = row.id;
    shell.dataset.pinned = row.pinned ? "1" : "0";
    const inner = document.createElement("div");
    shell.appendChild(inner);
    container.appendChild(shell);
    inners[row.id] = inner;
  }
  document.body.appendChild(container);
  return { container, inner: (id) => inners[id] };
}

function touchAt(el: HTMLElement, x: number, y: number): TouchEvent {
  return { ...touch(x, y), currentTarget: el } as unknown as TouchEvent;
}

describe("useConversationGestures", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = "";
    document.documentElement.style.cursor = "";
  });

  it("pins the first row via an upward drag when nothing is pinned", () => {
    const onReorder = vi.fn();
    const g = useConversationGestures({ onReorder, onDelete: vi.fn() });
    const { inner } = buildRows([{ id: "a" }, { id: "b" }]);

    g.onTouchStart(touchAt(inner("a"), 100, 200), "a");
    vi.advanceTimersByTime(400); // mature the long-press into a drag
    g.onTouchMove(touch(100, 190), "a"); // drag the top row up
    g.onTouchEnd("a");

    expect(onReorder).toHaveBeenCalledWith(["a"]);
  });

  it("doesn't pin the first row on a long-press without an upward drag", () => {
    const onReorder = vi.fn();
    const g = useConversationGestures({ onReorder, onDelete: vi.fn() });
    const { inner } = buildRows([{ id: "a" }, { id: "b" }]);

    g.onTouchStart(touchAt(inner("a"), 100, 200), "a");
    vi.advanceTimersByTime(400);
    g.onTouchEnd("a"); // released in place

    expect(onReorder).not.toHaveBeenCalled();
  });

  it("leaves the swipe actions open after a committed left swipe", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(110, 52), "c1"); // -90px, past the half-width commit
    g.onTouchEnd("c1");
    expect(g.swipedId.value).toBe("c1");
    expect(g.rowStyle("c1").transform).toBe(`translate(${-SWIPE_WIDTH}px, 0px)`);
  });

  it("snaps back when the left swipe is too short to commit", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(185, 51), "c1"); // -15px, under the commit threshold
    g.onTouchEnd("c1");
    expect(g.swipedId.value).toBeNull();
    expect(g.rowStyle("c1").transform).toBe("translate(0px, 0px)");
  });

  it("uses a slower transition when a conversation row settles", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });

    expect(g.rowStyle("c1").transition).toBe("transform 0.35s ease");
  });

  it("treats a pre-long-press vertical move as a scroll, not a reorder", () => {
    const onReorder = vi.fn();
    const g = useConversationGestures({ onReorder, onDelete: vi.fn() });
    g.onTouchStart(touch(100, 200), "c1");
    g.onTouchMove(touch(100, 240), "c1"); // moved before the timer fired
    vi.advanceTimersByTime(400);
    g.onTouchEnd("c1");
    expect(onReorder).not.toHaveBeenCalled();
  });

  it("swallows the tap that follows a gesture, then selects normally", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(110, 52), "c1");
    g.onTouchEnd("c1");
    // The synthetic click right after the swipe must not select.
    expect(g.shouldSelect("c1")).toBe(false);
    // A subsequent plain tap selects.
    expect(g.shouldSelect("c2")).toBe(true);
  });

  it("closes an open swipe on tap instead of selecting", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(110, 52), "c1");
    g.onTouchEnd("c1");
    g.shouldSelect("c1"); // consumes the post-gesture click
    // Now the row is open; tapping it again closes rather than selects.
    expect(g.shouldSelect("c1")).toBe(false);
    expect(g.swipedId.value).toBeNull();
  });

  it("never reveals the delete action during a long-press drag", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    expect(g.showAction("c1")).toBe(false); // hidden at rest
    g.onTouchStart(touch(100, 200), "c1");
    vi.advanceTimersByTime(400); // long-press → drag mode
    g.onTouchMove(touch(100, 160), "c1");
    expect(g.showAction("c1")).toBe(false); // still hidden while dragging
    g.onTouchEnd("c1");
    expect(g.showAction("c1")).toBe(false);
  });

  it("does not leak the drag cursor onto the document root", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    const { inner } = buildRows([{ id: "a" }, { id: "b" }]);
    document.documentElement.style.cursor = "crosshair";

    g.onMouseDown(
      {
        button: 0,
        clientX: 100,
        clientY: 200,
        currentTarget: inner("a"),
      } as unknown as MouseEvent,
      "a",
    );
    vi.advanceTimersByTime(400);

    expect(g.isDraggingAny()).toBe(true);
    expect(document.documentElement.style.cursor).toBe("crosshair");

    window.dispatchEvent(new MouseEvent("mouseup"));
    expect(g.isDraggingAny()).toBe(false);
    expect(document.documentElement.style.cursor).toBe("crosshair");
    document.documentElement.style.cursor = "";
  });

  it("reveals the swipe actions while swiping and once held open", () => {
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn() });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(110, 52), "c1"); // mid-swipe
    expect(g.showAction("c1")).toBe(true);
    g.onTouchEnd("c1"); // committed open
    expect(g.showAction("c1")).toBe(true);
  });

  it("fires onDelete and clears the swipe when the action is tapped", () => {
    const onDelete = vi.fn();
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(110, 52), "c1");
    g.onTouchEnd("c1");
    g.deleteSwiped("c1");
    expect(onDelete).toHaveBeenCalledWith("c1");
    expect(g.swipedId.value).toBeNull();
  });

  it("fires onShare and clears the swipe when the share action is tapped", () => {
    const onShare = vi.fn();
    const g = useConversationGestures({ onReorder: vi.fn(), onDelete: vi.fn(), onShare });
    g.onTouchStart(touch(200, 50), "c1");
    g.onTouchMove(touch(110, 52), "c1");
    g.onTouchEnd("c1");
    g.shareSwiped("c1");
    expect(onShare).toHaveBeenCalledWith("c1");
    expect(g.swipedId.value).toBeNull();
  });
});
