import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";

import { useWorkspaceDragDrop } from "./useWorkspaceDragDrop";
import type { FileEntry } from "./useFileApi";

function makeEntry(name: string, isDir = false): FileEntry {
  return { name, is_dir: isDir, size: 0, modified: "2024-01-01T00:00:00Z" };
}

function dragEvent(overrides: Partial<DragEvent> = {}): DragEvent {
  return {
    preventDefault: vi.fn(),
    stopPropagation: vi.fn(),
    dataTransfer: null,
    ...overrides,
  } as unknown as DragEvent;
}

function setup(currentPathValue = ".") {
  const currentPath = ref(currentPathValue);
  const renamingEntry = ref<string | null>(null);
  const performUpload = vi.fn().mockResolvedValue(undefined);
  const performMove = vi.fn().mockResolvedValue(undefined);
  const dnd = useWorkspaceDragDrop({
    currentPath,
    renamingEntry,
    getEntryPath: (entry) =>
      currentPath.value === "." ? entry.name : currentPath.value + "/" + entry.name,
    performUpload,
    performMove,
  });
  return { dnd, currentPath, renamingEntry, performUpload, performMove };
}

describe("useWorkspaceDragDrop", () => {
  it("dragstart records the dragged entry and its path", () => {
    const { dnd } = setup();
    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    expect(dnd.draggedEntry.value).toEqual({
      name: "a.txt",
      path: "a.txt",
      paths: ["a.txt"],
      isDir: false,
    });
  });

  it("dragstart is suppressed while the entry is being renamed", () => {
    const { dnd, renamingEntry } = setup();
    renamingEntry.value = "a.txt";
    const ev = dragEvent();
    dnd.handleEntryDragStart(ev, makeEntry("a.txt"));
    expect(ev.preventDefault).toHaveBeenCalled();
    expect(dnd.draggedEntry.value).toBeNull();
  });

  it("tracks a file drag without overriding the page cursor", () => {
    const { dnd } = setup();
    document.documentElement.style.cursor = "crosshair";

    dnd.handleEntryDragStart(dragEvent(), makeEntry("docs", true));
    expect(dnd.draggedEntry.value?.name).toBe("docs");
    expect(document.documentElement.style.cursor).toBe("crosshair");

    dnd.handleEntryDragEnd();
    expect(dnd.draggedEntry.value).toBeNull();
    expect(document.documentElement.style.cursor).toBe("crosshair");
    document.documentElement.style.cursor = "";
  });

  it("dropping an internal drag on the background is a no-op", async () => {
    const { dnd, performUpload, performMove } = setup();
    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    await dnd.handleDrop(dragEvent());
    expect(performUpload).not.toHaveBeenCalled();
    expect(performMove).not.toHaveBeenCalled();
    expect(dnd.draggedEntry.value).toBeNull();
  });

  it("dropping OS files on the background uploads into the current directory", async () => {
    const { dnd, performUpload } = setup();
    const file = new File(["x"], "x.txt");
    await dnd.handleDrop(dragEvent({ dataTransfer: { files: [file] } as unknown as DataTransfer }));
    expect(performUpload).toHaveBeenCalledWith(".", [file]);
  });

  it("dropping an internal drag on a folder moves the entry into it", async () => {
    const { dnd, performMove } = setup();
    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    await dnd.handleFolderDrop(dragEvent(), "docs");
    expect(performMove).toHaveBeenCalledWith("a.txt", "docs");
  });

  it("dropping a selected set on a folder moves every selected entry into it", async () => {
    const currentPath = ref(".");
    const renamingEntry = ref<string | null>(null);
    const performUpload = vi.fn().mockResolvedValue(undefined);
    const performMove = vi.fn().mockResolvedValue(undefined);
    const dnd = useWorkspaceDragDrop({
      currentPath,
      renamingEntry,
      getEntryPath: (entry) => entry.name,
      getDragPaths: () => ["a.txt", "b.txt"],
      performUpload,
      performMove,
    });

    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    await dnd.handleFolderDrop(dragEvent(), "docs");

    expect(performMove).toHaveBeenNthCalledWith(1, "a.txt", "docs");
    expect(performMove).toHaveBeenNthCalledWith(2, "b.txt", "docs");
  });

  it("stops a multi-entry folder drop when a move is deferred", async () => {
    const currentPath = ref(".");
    const renamingEntry = ref<string | null>(null);
    const performUpload = vi.fn().mockResolvedValue(undefined);
    const performMove = vi.fn().mockResolvedValueOnce(false).mockResolvedValue(undefined);
    const dnd = useWorkspaceDragDrop({
      currentPath,
      renamingEntry,
      getEntryPath: (entry) => entry.name,
      getDragPaths: () => ["a.txt", "b.txt"],
      performUpload,
      performMove,
    });

    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    await dnd.handleFolderDrop(dragEvent(), "docs");

    expect(performMove).toHaveBeenCalledTimes(1);
  });

  it("dropping OS files on a nested folder uploads into that folder's path", async () => {
    const { dnd, performUpload } = setup("sub");
    const file = new File(["x"], "x.txt");
    await dnd.handleFolderDrop(
      dragEvent({ dataTransfer: { files: [file] } as unknown as DataTransfer }),
      "sub/docs/img",
      "sub/docs/img",
    );
    expect(performUpload).toHaveBeenCalledWith("sub/docs/img", [file]);
  });

  it("never moves a folder into its own subtree", async () => {
    const { dnd, performMove } = setup();
    dnd.handleEntryDragStart(dragEvent(), makeEntry("docs", true));
    dnd.handleFolderDragOver(dragEvent(), "docs/img", "docs/img");
    expect(dnd.dragOverTarget.value).toBeNull();
    await dnd.handleFolderDrop(dragEvent(), "docs/img", "docs/img");
    expect(performMove).not.toHaveBeenCalled();
  });

  it("dropping an entry on itself does nothing", async () => {
    const { dnd, performMove } = setup();
    dnd.handleEntryDragStart(dragEvent(), makeEntry("docs", true));
    await dnd.handleFolderDrop(dragEvent(), "docs");
    expect(performMove).not.toHaveBeenCalled();
  });

  it("canDropToParent requires an internal drag and a non-root path", () => {
    const { dnd, currentPath } = setup("sub/dir");
    expect(dnd.canDropToParent.value).toBe(false);
    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    expect(dnd.canDropToParent.value).toBe(true);
    currentPath.value = ".";
    expect(dnd.canDropToParent.value).toBe(false);
  });

  it("top-bar drop moves the dragged entry to the parent directory", async () => {
    const { dnd, performMove } = setup("sub/dir");
    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    await dnd.handleTopBarDrop(dragEvent());
    expect(performMove).toHaveBeenCalledWith("sub/dir/a.txt", "sub");
    expect(dnd.draggedEntry.value).toBeNull();
  });

  it("top-bar drop moves every dragged selected entry to the parent directory", async () => {
    const currentPath = ref("sub/dir");
    const renamingEntry = ref<string | null>(null);
    const performUpload = vi.fn().mockResolvedValue(undefined);
    const performMove = vi.fn().mockResolvedValue(undefined);
    const dnd = useWorkspaceDragDrop({
      currentPath,
      renamingEntry,
      getEntryPath: (entry) => currentPath.value + "/" + entry.name,
      getDragPaths: () => ["sub/dir/a.txt", "sub/dir/b.txt"],
      performUpload,
      performMove,
    });

    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    await dnd.handleTopBarDrop(dragEvent());

    expect(performMove).toHaveBeenNthCalledWith(1, "sub/dir/a.txt", "sub");
    expect(performMove).toHaveBeenNthCalledWith(2, "sub/dir/b.txt", "sub");
  });

  it("a folder is not highlighted as a drop target for itself", () => {
    const { dnd } = setup();
    dnd.handleEntryDragStart(dragEvent(), makeEntry("docs", true));
    dnd.handleFolderDragOver(dragEvent(), "docs");
    expect(dnd.dragOverTarget.value).toBeNull();
    dnd.handleFolderDragOver(dragEvent(), "other");
    expect(dnd.dragOverTarget.value).toBe("other");
  });

  it("a selected folder remains a drop target when other dragged entries can move into it", () => {
    const currentPath = ref(".");
    const renamingEntry = ref<string | null>(null);
    const dnd = useWorkspaceDragDrop({
      currentPath,
      renamingEntry,
      getEntryPath: (entry) => entry.name,
      getDragPaths: () => ["docs", "a.txt"],
      performUpload: vi.fn().mockResolvedValue(undefined),
      performMove: vi.fn().mockResolvedValue(undefined),
    });

    dnd.handleEntryDragStart(dragEvent(), makeEntry("a.txt"));
    dnd.handleFolderDragOver(dragEvent(), "docs");

    expect(dnd.dragOverTarget.value).toBe("docs");
  });
});
