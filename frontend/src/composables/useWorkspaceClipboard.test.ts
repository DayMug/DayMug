import { afterEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";

import type { FileEntry } from "./useFileApi";
import type { WorkspaceContextMenuTarget } from "./useWorkspaceContextMenu";
import { useWorkspaceClipboard } from "./useWorkspaceClipboard";

function entry(name: string): FileEntry {
  return { name, is_dir: false, size: 0, modified: "" };
}

function conflict(): Error & { status: number } {
  return Object.assign(new Error("move: 409"), { status: 409 });
}

function setup() {
  const currentPath = ref("src");
  const error = ref("");
  const selected = ref(new Set<string>());
  const selectedEntry = ref<string | null>(null);
  const contextMenu = ref<WorkspaceContextMenuTarget | null>(null);
  const closeContextMenu = vi.fn(() => {
    contextMenu.value = null;
  });
  const loadDir = vi.fn(async () => {});
  const moveFile = vi.fn(async () => {});
  const copyFile = vi.fn(async () => ({ path: "" }));
  const suspended = ref(false);
  const toPath = (name: string) =>
    currentPath.value === "." ? name : `${currentPath.value}/${name}`;
  const selectionPaths = (anchor: string) => {
    const names = selected.value.has(anchor) ? Array.from(selected.value) : [anchor];
    return { paths: names.map(toPath), names };
  };
  const clip = useWorkspaceClipboard({
    userId: ref("u1"),
    currentPath,
    error,
    selectedEntry,
    contextMenu,
    closeContextMenu,
    selectionPaths,
    contextSelectionPaths: (target) =>
      target.entry && target.path !== toPath(target.entry.name)
        ? { paths: [target.path], names: [target.entry.name] }
        : selectionPaths(target.entry?.name ?? ""),
    loadDir,
    moveFile,
    copyFile,
    shortcutsSuspended: () => suspended.value,
  });
  function select(...names: string[]) {
    selected.value = new Set(names);
    selectedEntry.value = names[0] ?? null;
  }
  function key(k: string, init: KeyboardEventInit = { ctrlKey: true, metaKey: true }) {
    const event = new KeyboardEvent("keydown", { key: k, cancelable: true, ...init });
    clip.handleKeydown(event);
    return event;
  }
  return {
    clip,
    currentPath,
    error,
    contextMenu,
    closeContextMenu,
    loadDir,
    moveFile,
    copyFile,
    suspended,
    select,
    key,
  };
}

afterEach(() => {
  window.getSelection()?.removeAllRanges();
});

describe("useWorkspaceClipboard", () => {
  describe("keyboard shortcuts", () => {
    it("copies and cuts the whole selection", () => {
      const { clip, select, key } = setup();
      select("a.txt", "b.txt");

      expect(key("c").defaultPrevented).toBe(true);
      expect(clip.clipboard.value).toEqual({
        paths: ["src/a.txt", "src/b.txt"],
        names: ["a.txt", "b.txt"],
        mode: "copy",
      });
      expect(clip.cutNames.value.size).toBe(0);

      key("x");
      expect(clip.clipboard.value?.mode).toBe("cut");
      expect([...clip.cutNames.value]).toEqual(["a.txt", "b.txt"]);
    });

    it("leaves the keys alone without a modifier, a selection, or while suspended", () => {
      const { clip, select, key, suspended } = setup();
      expect(key("c").defaultPrevented).toBe(false);

      select("a.txt");
      key("c", {});
      expect(clip.clipboard.value).toBeNull();

      suspended.value = true;
      expect(key("c").defaultPrevented).toBe(false);
      expect(clip.clipboard.value).toBeNull();
    });

    it("ignores shortcuts typed into a text field", () => {
      const { clip, select } = setup();
      select("a.txt");
      const input = document.createElement("input");
      document.body.appendChild(input);
      input.addEventListener("keydown", clip.handleKeydown);
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "c", ctrlKey: true, metaKey: true }));
      input.remove();

      expect(clip.clipboard.value).toBeNull();
    });

    it("pastes only when something was copied", async () => {
      const { clip, select, key, copyFile } = setup();
      expect(key("v").defaultPrevented).toBe(false);

      select("a.txt");
      key("c");
      expect(key("v").defaultPrevented).toBe(true);
      await vi.waitFor(() => expect(copyFile).toHaveBeenCalled());
      expect(clip.clipboard.value).not.toBeNull();
    });
  });

  describe("context menu", () => {
    it("cuts and copies the menu's target set and closes the menu", () => {
      const { clip, select, contextMenu, closeContextMenu } = setup();
      select("a.txt", "b.txt");
      contextMenu.value = { entry: entry("a.txt"), path: "src/a.txt", x: 0, y: 0 };
      clip.handleCtxCut();
      expect(clip.clipboard.value).toMatchObject({ mode: "cut", names: ["a.txt", "b.txt"] });
      expect(closeContextMenu).toHaveBeenCalledTimes(1);

      contextMenu.value = { entry: entry("deep.txt"), path: "src/sub/deep.txt", x: 0, y: 0 };
      clip.handleCtxCopy();
      expect(clip.clipboard.value).toEqual({
        paths: ["src/sub/deep.txt"],
        names: ["deep.txt"],
        mode: "copy",
      });
    });

    it("ignores the background menu", () => {
      const { clip, contextMenu } = setup();
      contextMenu.value = { entry: null, path: "src", x: 0, y: 0 };
      clip.handleCtxCut();
      clip.handleCtxCopy();
      expect(clip.clipboard.value).toBeNull();
    });
  });

  describe("paste", () => {
    it("copies every entry into the current directory and keeps the clipboard", async () => {
      const { clip, select, key, copyFile, loadDir, currentPath } = setup();
      select("a.txt", "b.txt");
      key("c");
      currentPath.value = "dst";

      await clip.handlePaste();
      expect(copyFile.mock.calls).toEqual([
        ["u1", "src/a.txt", "dst"],
        ["u1", "src/b.txt", "dst"],
      ]);
      expect(loadDir).toHaveBeenCalledWith("dst");
      expect(clip.clipboard.value).not.toBeNull();
    });

    it("surfaces a failed copy", async () => {
      const { clip, select, key, copyFile, error } = setup();
      copyFile.mockRejectedValue(new Error("copy: 500"));
      select("a.txt");
      key("c");

      await clip.handlePaste();
      expect(error.value).toBe("copy: 500");
    });

    it("moves a single cut entry and clears the clipboard", async () => {
      const { clip, select, key, moveFile, currentPath } = setup();
      select("a.txt");
      key("x");
      currentPath.value = "dst";

      await clip.handlePaste();
      expect(moveFile).toHaveBeenCalledWith("u1", "src/a.txt", "dst", undefined);
      expect(clip.clipboard.value).toBeNull();
    });

    it("keeps a single cut entry on the clipboard until its conflict resolves", async () => {
      const { clip, select, key, moveFile, currentPath } = setup();
      moveFile.mockRejectedValueOnce(conflict());
      select("a.txt");
      key("x");
      currentPath.value = "dst";

      await clip.handlePaste();
      expect(clip.conflictMove.value).toEqual({
        srcPath: "src/a.txt",
        targetPath: "dst",
        fileName: "a.txt",
        clearClipboardOnSuccess: true,
      });
      expect(clip.clipboard.value).not.toBeNull();

      await clip.handleConflictOverwrite();
      expect(moveFile).toHaveBeenLastCalledWith("u1", "src/a.txt", "dst", "overwrite");
      expect(clip.conflictMove.value).toBeNull();
      expect(clip.clipboard.value).toBeNull();
    });

    it("stops a multi-entry move at the first conflict and keeps the clipboard", async () => {
      const { clip, select, key, moveFile, currentPath } = setup();
      moveFile.mockResolvedValueOnce(undefined).mockRejectedValueOnce(conflict());
      select("a.txt", "b.txt", "c.txt");
      key("x");
      currentPath.value = "dst";

      await clip.handlePaste();
      expect(moveFile).toHaveBeenCalledTimes(2);
      expect(clip.conflictMove.value?.fileName).toBe("b.txt");
      expect(clip.conflictMove.value?.clearClipboardOnSuccess).toBeUndefined();
      expect(clip.clipboard.value).not.toBeNull();
    });

    it("clears the clipboard once every cut entry has moved", async () => {
      const { clip, select, key, moveFile } = setup();
      select("a.txt", "b.txt");
      key("x");

      await clip.handlePaste();
      expect(moveFile).toHaveBeenCalledTimes(2);
      expect(clip.clipboard.value).toBeNull();
    });
  });

  describe("moves and conflicts", () => {
    it("reports a non-conflict failure instead of asking", async () => {
      const { clip, moveFile, error } = setup();
      moveFile.mockRejectedValue(new Error("move: 500"));

      expect(await clip.performMove("src/a.txt", "dst")).toBe(false);
      expect(error.value).toBe("move: 500");
      expect(clip.conflictMove.value).toBeNull();
    });

    it("does not re-open the dialog when an explicit resolution conflicts again", async () => {
      const { clip, moveFile, error } = setup();
      moveFile.mockRejectedValue(conflict());

      expect(await clip.performMove("src/a.txt", "dst", { onConflict: "rename" })).toBe(false);
      expect(clip.conflictMove.value).toBeNull();
      expect(error.value).toBe("move: 409");
    });

    it("retries with rename, or drops the move on cancel", async () => {
      const { clip, moveFile } = setup();
      moveFile.mockRejectedValueOnce(conflict());
      await clip.performMove("src/a.txt", "dst");
      await clip.handleConflictRename();
      expect(moveFile).toHaveBeenLastCalledWith("u1", "src/a.txt", "dst", "rename");

      moveFile.mockClear();
      moveFile.mockRejectedValueOnce(conflict());
      await clip.performMove("src/a.txt", "dst");
      clip.handleConflictCancel();
      await clip.handleConflictOverwrite();
      expect(moveFile).toHaveBeenCalledTimes(1);
      expect(clip.conflictMove.value).toBeNull();
    });
  });

  it("clearClipboard forgets the cut set", () => {
    const { clip, select, key } = setup();
    select("a.txt");
    key("x");
    clip.clearClipboard();
    expect(clip.clipboard.value).toBeNull();
    expect(clip.cutNames.value.size).toBe(0);
  });
});
