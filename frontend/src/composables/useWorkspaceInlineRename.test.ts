import { describe, expect, it, vi } from "vitest";
import { nextTick, ref } from "vue";

import type { FileEntry } from "./useFileApi";
import type { WorkspaceContextMenuTarget } from "./useWorkspaceContextMenu";
import { useWorkspaceInlineRename } from "./useWorkspaceInlineRename";

function entry(name: string, extra: Partial<FileEntry> = {}): FileEntry {
  return { name, is_dir: false, size: 0, modified: "", ...extra };
}

function setup(path = "docs") {
  const currentPath = ref(path);
  const error = ref("");
  const contextMenu = ref<WorkspaceContextMenuTarget | null>(null);
  const closeContextMenu = vi.fn(() => {
    contextMenu.value = null;
  });
  const loadDir = vi.fn(async (next: string) => {
    currentPath.value = next;
  });
  const renameFile = vi.fn(async () => {});
  const focusInput = vi.fn();
  const rename = useWorkspaceInlineRename({
    userId: ref("u1"),
    currentPath,
    error,
    contextMenu,
    closeContextMenu,
    getEntryPath: (e) => (currentPath.value === "." ? e.name : `${currentPath.value}/${e.name}`),
    loadDir,
    renameFile,
    focusInput,
  });
  return {
    rename,
    currentPath,
    error,
    contextMenu,
    closeContextMenu,
    loadDir,
    renameFile,
    focusInput,
  };
}

describe("useWorkspaceInlineRename", () => {
  it("opens the edit on an entry and focuses the input after render", async () => {
    const { rename, focusInput } = setup();
    rename.beginRename("a.txt");

    expect(rename.renamingEntry.value).toBe("a.txt");
    expect(rename.renameValue.value).toBe("a.txt");
    expect(focusInput).not.toHaveBeenCalled();
    await nextTick();
    expect(focusInput).toHaveBeenCalledTimes(1);
  });

  it("renames within the current directory and reloads it", async () => {
    const { rename, renameFile, loadDir } = setup();
    rename.beginRename("a.txt");
    rename.renameValue.value = "  b.txt ";

    await rename.confirmRename();
    expect(renameFile).toHaveBeenCalledWith("u1", "docs/a.txt", "docs/b.txt");
    expect(loadDir).toHaveBeenCalledWith("docs");
    expect(rename.renamingEntry.value).toBeNull();
  });

  it("uses bare names at the workspace root", async () => {
    const { rename, renameFile } = setup(".");
    rename.beginRename("a.txt");
    rename.renameValue.value = "b.txt";

    await rename.confirmRename();
    expect(renameFile).toHaveBeenCalledWith("u1", "a.txt", "b.txt");
  });

  it.each([
    ["an unchanged name", "a.txt"],
    ["a blank name", "   "],
  ])("treats %s as a cancel", async (_label, value) => {
    const { rename, renameFile } = setup();
    rename.beginRename("a.txt");
    rename.renameValue.value = value;

    await rename.confirmRename();
    expect(renameFile).not.toHaveBeenCalled();
    expect(rename.renamingEntry.value).toBeNull();
  });

  it("keeps the edit open until the rename has landed, then surfaces a failure", async () => {
    const { rename, renameFile, error } = setup();
    let fail!: (cause: Error) => void;
    renameFile.mockReturnValue(
      new Promise<void>((_resolve, reject) => {
        fail = reject;
      }),
    );
    rename.beginRename("a.txt");
    rename.renameValue.value = "b.txt";

    const pending = rename.confirmRename();
    expect(rename.renamingEntry.value).toBe("a.txt");
    fail(new Error("rename: 409"));
    await pending;

    expect(error.value).toBe("rename: 409");
    expect(rename.renamingEntry.value).toBeNull();
  });

  it("ignores Enter while an IME candidate is being composed", async () => {
    const { rename, renameFile } = setup();
    rename.beginRename("a.txt");
    rename.renameValue.value = "b.txt";

    rename.renameComposing.value = true;
    rename.onRenameEnter(new KeyboardEvent("keydown", { key: "Enter" }));
    rename.renameComposing.value = false;
    rename.onRenameEnter(new KeyboardEvent("keydown", { key: "Enter", isComposing: true }));
    expect(renameFile).not.toHaveBeenCalled();

    rename.onRenameEnter(new KeyboardEvent("keydown", { key: "Enter" }));
    await Promise.resolve();
    expect(renameFile).toHaveBeenCalledTimes(1);
  });

  it("renames the context menu's entry and closes the menu", async () => {
    const { rename, contextMenu, closeContextMenu, loadDir } = setup();
    contextMenu.value = { entry: entry("a.txt"), path: "docs/a.txt", x: 0, y: 0 };

    await rename.startRename();
    expect(closeContextMenu).toHaveBeenCalled();
    expect(loadDir).not.toHaveBeenCalled();
    expect(rename.renamingEntry.value).toBe("a.txt");
  });

  it("moves into a nested row's directory before renaming it", async () => {
    const { rename, contextMenu, loadDir, currentPath } = setup();
    contextMenu.value = { entry: entry("deep.txt"), path: "docs/sub/deep.txt", x: 0, y: 0 };

    await rename.startRename();
    expect(loadDir).toHaveBeenCalledWith("docs/sub");
    expect(currentPath.value).toBe("docs/sub");
    expect(rename.renamingEntry.value).toBe("deep.txt");
  });

  it("does nothing without an entry to rename", async () => {
    const { rename, contextMenu } = setup();
    contextMenu.value = { entry: null, path: "docs", x: 0, y: 0 };

    await rename.startRename();
    expect(rename.renamingEntry.value).toBeNull();
  });
});
