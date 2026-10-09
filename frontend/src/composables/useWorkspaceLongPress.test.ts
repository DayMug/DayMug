import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { useWorkspaceLongPress, type LongPressContext } from "./useWorkspaceLongPress";
import type { FileEntry } from "./useFileApi";

function pointerEvent(overrides: Partial<PointerEvent> = {}): PointerEvent {
  return {
    pointerType: "touch",
    clientX: 100,
    clientY: 100,
    ...overrides,
  } as unknown as PointerEvent;
}

const entry: FileEntry = { name: "a.txt", is_dir: false, size: 1, modified: "2024-01-01" };

describe("useWorkspaceLongPress", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("fires onLongPress after a 500ms touch hold", () => {
    const onLongPress = vi.fn();
    const lp = useWorkspaceLongPress({ onLongPress });
    lp.startLongPress(pointerEvent(), entry, "a.txt");
    vi.advanceTimersByTime(499);
    expect(onLongPress).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(onLongPress).toHaveBeenCalledWith({
      entry,
      path: "a.txt",
      x: 100,
      y: 100,
    } satisfies LongPressContext);
  });

  it("ignores non-touch pointers", () => {
    const onLongPress = vi.fn();
    const lp = useWorkspaceLongPress({ onLongPress });
    lp.startLongPress(pointerEvent({ pointerType: "mouse" }), entry, "a.txt");
    vi.advanceTimersByTime(600);
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("is cancelled when the pointer moves beyond the tolerance", () => {
    const onLongPress = vi.fn();
    const lp = useWorkspaceLongPress({ onLongPress });
    lp.startLongPress(pointerEvent(), entry, "a.txt");
    lp.moveLongPress(pointerEvent({ clientX: 120, clientY: 100 }));
    vi.advanceTimersByTime(600);
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("is cancelled when the pointer lifts before the timer fires", () => {
    const onLongPress = vi.fn();
    const lp = useWorkspaceLongPress({ onLongPress });
    lp.startLongPress(pointerEvent(), entry, "a.txt");
    lp.endLongPress();
    vi.advanceTimersByTime(600);
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("consumeLongPressClick suppresses exactly one synthetic click after firing", () => {
    const lp = useWorkspaceLongPress({ onLongPress: vi.fn() });
    expect(lp.consumeLongPressClick()).toBe(false);
    lp.startLongPress(pointerEvent(), entry, "a.txt");
    vi.advanceTimersByTime(500);
    expect(lp.consumeLongPressClick()).toBe(true);
    expect(lp.consumeLongPressClick()).toBe(false);
  });
});
