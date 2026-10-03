import { beforeEach, describe, expect, it } from "vitest";
import { nextTick, ref } from "vue";
import { useResizableSidebar } from "./useResizableSidebar";

const STORAGE_KEY = "test-conversation-panel-px";

beforeEach(() => {
  localStorage.removeItem(STORAGE_KEY);
});

function setup() {
  const panel = document.createElement("div");
  panel.getBoundingClientRect = () =>
    ({
      left: 56,
      right: 344,
      top: 0,
      bottom: 800,
      width: 288,
      height: 800,
      x: 56,
      y: 0,
      toJSON: () => {},
    }) as DOMRect;

  return useResizableSidebar(ref<HTMLElement | undefined>(panel), {
    storageKey: STORAGE_KEY,
    defaultWidth: 288,
    minWidth: 240,
    maxWidth: 480,
  });
}

describe("useResizableSidebar", () => {
  it("resizes from the panel's left edge and persists the width", async () => {
    const sidebar = setup();
    sidebar.onDragStart(new MouseEvent("mousedown"));
    document.dispatchEvent(new MouseEvent("mousemove", { clientX: 416 }));
    document.dispatchEvent(new MouseEvent("mouseup"));
    await nextTick();

    expect(sidebar.widthPx.value).toBe(360);
    expect(sidebar.panelStyle.value).toEqual({ width: "360px", minWidth: "360px" });
    expect(localStorage.getItem(STORAGE_KEY)).toBe("360");
  });

  it("restores and clamps a persisted width", () => {
    localStorage.setItem(STORAGE_KEY, "900");
    const sidebar = setup();

    expect(sidebar.widthPx.value).toBe(480);
    expect(sidebar.panelStyle.value).toEqual({ width: "480px", minWidth: "480px" });
  });
});
