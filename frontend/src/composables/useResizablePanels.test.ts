// Covers the workspace-panel collapse behavior added so a user can
// reclaim chat width with a single click. The width-resize path is
// already exercised end-to-end by ChatPage.test.ts; this file focuses
// on the collapse state machine and its localStorage contract.
import { describe, it, expect, beforeEach } from "vitest";
import { ref, nextTick } from "vue";
import { useResizablePanels } from "./useResizablePanels";

const COLLAPSE_KEY = "test-workspace-collapsed";
const WIDTH_KEY = "test-workspace-px";

beforeEach(() => {
  localStorage.removeItem(COLLAPSE_KEY);
  localStorage.removeItem(WIDTH_KEY);
});

function setup() {
  return useResizablePanels(ref<HTMLElement | undefined>(undefined), {
    storageKey: WIDTH_KEY,
    collapseStorageKey: COLLAPSE_KEY,
    defaultWidth: 420,
  });
}

describe("useResizablePanels — collapse state", () => {
  it("defaults to expanded with the configured width", () => {
    const p = setup();
    expect(p.isCollapsed.value).toBe(false);
    expect(p.workspacePanelStyle.value).toEqual({ width: "420px" });
    expect(p.chatPanelStyle.value).toEqual({ width: "calc(100% - 420px)" });
  });

  it("collapse() hides the workspace pane while preserving the saved width", () => {
    const p = setup();
    p.workspaceWidthPx.value = 500;
    p.collapse();
    expect(p.isCollapsed.value).toBe(true);
    expect(p.workspacePanelStyle.value).toEqual({ width: "0px", overflow: "hidden" });
    expect(p.chatPanelStyle.value).toEqual({ width: "100%" });
    // Width preference is independent of collapse — expand should restore it.
    expect(p.workspaceWidthPx.value).toBe(500);
  });

  it("expand() restores the previously saved width", () => {
    const p = setup();
    p.workspaceWidthPx.value = 540;
    p.collapse();
    p.expand();
    expect(p.isCollapsed.value).toBe(false);
    expect(p.workspacePanelStyle.value).toEqual({ width: "540px" });
  });

  it("persists collapse state to localStorage so a reload keeps the layout", async () => {
    const first = setup();
    first.collapse();
    // useLocalStorage's writeback is queued on the watcher; flush it
    // before asserting the persisted value.
    await nextTick();
    expect(localStorage.getItem(COLLAPSE_KEY)).toBe("true");

    const second = setup();
    expect(second.isCollapsed.value).toBe(true);
  });

  it("ignores drag attempts while collapsed — the handle is the click expander then", () => {
    const p = setup();
    p.collapse();
    const e = new MouseEvent("mousedown", { clientX: 600 });
    let prevented = false;
    Object.defineProperty(e, "preventDefault", { value: () => (prevented = true) });
    p.onDragStart(e);
    expect(p.isDragging.value).toBe(false);
    expect(prevented).toBe(false);
  });
});
