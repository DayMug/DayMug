import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { clampMenuPosition } from "./menuPosition";

describe("clampMenuPosition", () => {
  let originalInnerWidth: number;
  let originalInnerHeight: number;

  beforeEach(() => {
    originalInnerWidth = window.innerWidth;
    originalInnerHeight = window.innerHeight;
  });

  afterEach(() => {
    Object.defineProperty(window, "innerWidth", {
      configurable: true,
      value: originalInnerWidth,
    });
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: originalInnerHeight,
    });
  });

  function setViewport(w: number, h: number) {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: w });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: h });
  }

  function makeEl(width: number, height: number): HTMLElement {
    const el = document.createElement("div");
    vi.spyOn(el, "getBoundingClientRect").mockReturnValue({
      width,
      height,
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      toJSON: () => ({}),
    } as DOMRect);
    return el;
  }

  it("returns the requested position when the menu fits", () => {
    setViewport(1024, 768);
    const el = makeEl(200, 100);
    expect(clampMenuPosition(el, 100, 100)).toEqual({ left: 100, top: 100 });
  });

  it("clamps left when the menu would overflow the right edge", () => {
    setViewport(360, 640);
    const el = makeEl(200, 100);
    // x = 300 + width 200 = 500 > 360, should clamp to 360 - 200 - 8 = 152
    expect(clampMenuPosition(el, 300, 50)).toEqual({ left: 152, top: 50 });
  });

  it("clamps top when the menu would overflow the bottom edge", () => {
    setViewport(360, 640);
    const el = makeEl(180, 300);
    // y = 500 + height 300 = 800 > 640, should clamp to 640 - 300 - 8 = 332
    expect(clampMenuPosition(el, 50, 500)).toEqual({ left: 50, top: 332 });
  });

  it("respects margin on the left/top edges", () => {
    setViewport(360, 640);
    const el = makeEl(180, 100);
    // negative coords should be clamped to the margin
    expect(clampMenuPosition(el, -10, -10)).toEqual({ left: 8, top: 8 });
  });

  it("falls back to the margin when the element is larger than the viewport", () => {
    setViewport(320, 200);
    const el = makeEl(400, 300);
    // maxLeft = max(8, 320 - 400 - 8) = 8
    expect(clampMenuPosition(el, 100, 100)).toEqual({ left: 8, top: 8 });
  });
});
