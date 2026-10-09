import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";

import {
  useWorkspaceContextMenu,
  type UseWorkspaceContextMenuOptions,
} from "./useWorkspaceContextMenu";
import type { FileEntry } from "./useFileApi";

const fileA: FileEntry = { name: "a.txt", is_dir: false, size: 1, modified: "2024-01-01" };
const fileB: FileEntry = { name: "b.txt", is_dir: false, size: 2, modified: "2024-01-01" };

function mouseEvent(overrides: Partial<MouseEvent> = {}): MouseEvent {
  return {
    preventDefault: vi.fn(),
    clientX: 10,
    clientY: 20,
    ctrlKey: false,
    metaKey: false,
    ...overrides,
  } as unknown as MouseEvent;
}

function setup(overrides: Partial<UseWorkspaceContextMenuOptions> = {}) {
  const selectedEntries = ref(new Set<string>());
  const selectedEntry = ref<string | null>(null);
  const selectOnly = vi.fn((name: string | null) => {
    selectedEntries.value = name ? new Set([name]) : new Set();
    selectedEntry.value = name;
  });
  const options: UseWorkspaceContextMenuOptions = {
    currentPath: ref("docs"),
    selectedEntries,
    selectedEntry,
    selectOnly,
    getEntryPath: (entry) => `docs/${entry.name}`,
    openEntry: vi.fn(),
    getDeleteTargets: vi.fn((entry: FileEntry) => [entry]),
    deleteEntries: vi.fn(),
    confirmDelete: vi.fn(async () => true),
    getDownloadUrl: vi.fn(() => "/download"),
    showProperties: vi.fn(),
    refresh: vi.fn(),
    ...overrides,
  };
  return { options, selectedEntries, selectedEntry, menu: useWorkspaceContextMenu(options) };
}

describe("useWorkspaceContextMenu", () => {
  it("opens on an unselected entry and collapses the selection to it", () => {
    const { menu, options } = setup();
    menu.handleContextMenu(mouseEvent(), fileA);
    expect(options.selectOnly).toHaveBeenCalledWith("a.txt");
    expect(menu.contextMenu.value).toEqual({
      entry: fileA,
      path: "docs/a.txt",
      x: 10,
      y: 20,
    });
  });

  it("keeps an existing multi-selection when opened inside it", () => {
    const { menu, options, selectedEntries, selectedEntry } = setup();
    selectedEntries.value = new Set(["a.txt", "b.txt"]);
    selectedEntry.value = "a.txt";
    menu.handleContextMenu(mouseEvent(), fileB);
    expect(options.selectOnly).not.toHaveBeenCalled();
    expect(selectedEntries.value).toEqual(new Set(["a.txt", "b.txt"]));
    expect(selectedEntry.value).toBe("b.txt");
  });

  it("keeps an expanded tree row path separate from root-level selection", () => {
    const { menu, options, selectedEntries, selectedEntry } = setup();
    selectedEntries.value = new Set(["a.txt", "b.txt"]);
    selectedEntry.value = "a.txt";

    menu.handleContextMenu(mouseEvent(), fileA, "docs/nested/a.txt");

    expect(options.selectOnly).toHaveBeenCalledWith(null);
    expect(menu.contextMenu.value).toEqual({
      entry: fileA,
      path: "docs/nested/a.txt",
      x: 10,
      y: 20,
    });
  });

  it("targets the current directory and leaves the selection alone on background opens", () => {
    const { menu, options, selectedEntries } = setup();
    selectedEntries.value = new Set(["a.txt"]);
    menu.handleBackgroundContextMenu(mouseEvent({ clientX: 5, clientY: 6 }));
    expect(options.selectOnly).not.toHaveBeenCalled();
    expect(selectedEntries.value).toEqual(new Set(["a.txt"]));
    expect(menu.contextMenu.value).toEqual({ entry: null, path: "docs", x: 5, y: 6 });
  });

  it("closes via closeContextMenu", () => {
    const { menu } = setup();
    menu.handleContextMenu(mouseEvent(), fileA);
    menu.closeContextMenu();
    expect(menu.contextMenu.value).toBeNull();
  });

  it("aborts a plain-click delete when the confirmation is declined", async () => {
    const confirmDelete = vi.fn(async () => false);
    const { menu, options } = setup({ confirmDelete });
    menu.handleContextMenu(mouseEvent(), fileA);
    await menu.handleCtxDelete(mouseEvent());
    expect(confirmDelete).toHaveBeenCalledWith(fileA, [fileA]);
    expect(options.deleteEntries).not.toHaveBeenCalled();
    expect(menu.contextMenu.value).toBeNull();
  });

  it("deletes after a confirmed plain click", async () => {
    const { menu, options } = setup();
    menu.handleContextMenu(mouseEvent(), fileA);
    await menu.handleCtxDelete(mouseEvent());
    expect(options.deleteEntries).toHaveBeenCalledWith([fileA], "docs/a.txt");
  });

  it("skips the confirmation on a primary-modifier click", async () => {
    const { menu, options } = setup();
    menu.handleContextMenu(mouseEvent(), fileA);
    // Both modifier flags set so the bypass applies on mac-like and
    // non-mac platforms alike.
    await menu.handleCtxDelete(mouseEvent({ ctrlKey: true, metaKey: true }));
    expect(options.confirmDelete).not.toHaveBeenCalled();
    expect(options.deleteEntries).toHaveBeenCalledWith([fileA], "docs/a.txt");
  });

  it("routes Open through openEntry and closes", () => {
    const { menu, options } = setup();
    menu.handleContextMenu(mouseEvent(), fileA);
    menu.handleCtxOpen();
    expect(options.openEntry).toHaveBeenCalledWith(fileA, "docs/a.txt");
    expect(menu.contextMenu.value).toBeNull();
  });

  it("ignores entry-only actions when opened on the background", () => {
    const { menu, options } = setup();
    menu.handleBackgroundContextMenu(mouseEvent());
    menu.handleCtxOpen();
    menu.handleCtxProperties();
    menu.handleCtxDownload();
    expect(options.openEntry).not.toHaveBeenCalled();
    expect(options.showProperties).not.toHaveBeenCalled();
    expect(options.getDownloadUrl).not.toHaveBeenCalled();
  });

  it("refreshes and closes on the Refresh action", () => {
    const { menu, options } = setup();
    menu.handleBackgroundContextMenu(mouseEvent());
    menu.handleCtxRefresh();
    expect(options.refresh).toHaveBeenCalled();
    expect(menu.contextMenu.value).toBeNull();
  });
});
