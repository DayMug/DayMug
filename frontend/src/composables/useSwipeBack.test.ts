import { describe, it, expect, vi } from "vitest";
import { useSwipeBack } from "./useSwipeBack";

function touch(x: number, y: number): TouchEvent {
  return {
    touches: [{ clientX: x, clientY: y }],
    preventDefault: () => {},
  } as unknown as TouchEvent;
}

describe("useSwipeBack", () => {
  it("pops back on a committed left-edge rightward swipe", () => {
    const onBack = vi.fn();
    const s = useSwipeBack(onBack);
    s.onTouchStart(touch(10, 300)); // starts at the left edge
    s.onTouchMove(touch(120, 305)); // dragged 110px right, past commit
    s.onTouchEnd();
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("snaps back without popping when the swipe is too short", () => {
    const onBack = vi.fn();
    const s = useSwipeBack(onBack);
    s.onTouchStart(touch(10, 300));
    s.onTouchMove(touch(50, 302)); // only 40px, under commit
    s.onTouchEnd();
    expect(onBack).not.toHaveBeenCalled();
    expect(s.style.value.transform).toBe("translateX(0px)");
  });

  it("ignores swipes that don't start at the left edge", () => {
    const onBack = vi.fn();
    const s = useSwipeBack(onBack);
    s.onTouchStart(touch(200, 300)); // begins mid-screen — not armed
    s.onTouchMove(touch(320, 305));
    s.onTouchEnd();
    expect(onBack).not.toHaveBeenCalled();
    expect(s.active.value).toBe(false);
  });

  it("bows out when the gesture is a vertical scroll", () => {
    const onBack = vi.fn();
    const s = useSwipeBack(onBack);
    s.onTouchStart(touch(10, 300));
    s.onTouchMove(touch(14, 360)); // dominated by vertical movement
    s.onTouchEnd();
    expect(onBack).not.toHaveBeenCalled();
    expect(s.dx.value).toBe(0);
  });

  it("does not pop on a leftward drag from the edge", () => {
    const onBack = vi.fn();
    const s = useSwipeBack(onBack);
    s.onTouchStart(touch(20, 300));
    s.onTouchMove(touch(2, 302)); // moved left, not a back gesture
    s.onTouchEnd();
    expect(onBack).not.toHaveBeenCalled();
  });

  it("tracks the finger while active, then eases back at rest", () => {
    const s = useSwipeBack(vi.fn());
    s.onTouchStart(touch(10, 300));
    s.onTouchMove(touch(70, 300)); // +60px
    expect(s.style.value.transform).toBe("translateX(60px)");
    expect(s.style.value.transition).toBe("none");
    s.onTouchEnd();
    expect(s.style.value.transition).toBe("transform 0.2s ease");
  });
});
