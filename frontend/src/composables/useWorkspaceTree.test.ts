import { describe, it, expect, vi } from "vitest";
import { ref } from "vue";

import { useWorkspaceTree } from "./useWorkspaceTree";
import type { FileEntry } from "./useFileApi";

function entry(name: string, isDir = false): FileEntry {
  return { name, is_dir: isDir, size: 0, modified: "" };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup(listDir = vi.fn().mockResolvedValue({ entries: [entry("Button.vue")] })) {
  const entries = ref<FileEntry[]>([entry("src", true), entry(".env"), entry("main.ts")]);
  const showHiddenFiles = ref(false);
  const currentPath = ref(".");
  const userId = ref("u1");
  const tree = useWorkspaceTree({
    userId,
    currentPath,
    entries,
    showHiddenFiles,
    listDir,
  });
  return { tree, entries, showHiddenFiles, currentPath, listDir, userId };
}

describe("useWorkspaceTree", () => {
  it("lists directories before files and hides dot-files by default", () => {
    const { tree } = setup();
    expect(tree.treeRows.value.map((r) => r.path)).toEqual(["src", "main.ts"]);
  });

  it("reveals dot-files once the panel turns hidden files on", () => {
    const { tree, showHiddenFiles } = setup();
    showHiddenFiles.value = true;
    expect(tree.treeRows.value.map((r) => r.path)).toEqual(["src", ".env", "main.ts"]);
  });

  it("fetches a branch on first expand and reuses the cache afterwards", async () => {
    const { tree, listDir } = setup();
    await tree.toggleTreeDirectory("src");
    expect(tree.treeRows.value.map((r) => r.path)).toContain("src/Button.vue");

    await tree.toggleTreeDirectory("src");
    await tree.toggleTreeDirectory("src");
    expect(listDir).toHaveBeenCalledTimes(1);
  });

  it("prefixes child paths with the current directory", async () => {
    const { tree, currentPath } = setup();
    currentPath.value = "pkg";
    await tree.toggleTreeDirectory("pkg/src");
    expect(tree.treeRows.value.map((r) => r.path)).toEqual([
      "pkg/src",
      "pkg/src/Button.vue",
      "pkg/main.ts",
    ]);
  });

  it("collapses the branch again and records the message when the listing fails", async () => {
    const listDir = vi.fn().mockRejectedValue(new Error("permission denied"));
    const { tree } = setup(listDir);
    await tree.toggleTreeDirectory("src");

    expect(tree.expandedTreePaths.value.has("src")).toBe(false);
    expect(tree.treeLoadErrors.value.get("src")).toBe("permission denied");
    expect(tree.loadingTreePaths.value.has("src")).toBe(false);
  });

  it("drops every cached branch on reset so a reload can't show stale children", async () => {
    const { tree } = setup();
    await tree.toggleTreeDirectory("src");
    tree.reset();

    expect(tree.expandedTreePaths.value.size).toBe(0);
    expect(tree.treeRows.value.map((r) => r.path)).toEqual(["src", "main.ts"]);
  });

  it("refetches a branch whose listing landed after a reset instead of caching it", async () => {
    // `treeChildren` is a permanent cache, so a pre-reset response sneaking
    // back into it pinned the stale children forever.
    const pending = deferred<{ entries: FileEntry[] }>();
    const listDir = vi.fn().mockReturnValueOnce(pending.promise);
    const { tree } = setup(listDir);

    const expanding = tree.toggleTreeDirectory("src");
    tree.reset();
    pending.resolve({ entries: [entry("stale.vue")] });
    await expanding;

    listDir.mockResolvedValueOnce({ entries: [entry("fresh.vue")] });
    await tree.toggleTreeDirectory("src");

    expect(listDir).toHaveBeenCalledTimes(2);
    expect(tree.treeRows.value.map((r) => r.path)).toContain("src/fresh.vue");
  });

  it("discards a branch failure that arrives after a reset", async () => {
    const pending = deferred<{ entries: FileEntry[] }>();
    const listDir = vi.fn().mockReturnValueOnce(pending.promise);
    const { tree } = setup(listDir);

    const expanding = tree.toggleTreeDirectory("src");
    tree.reset();
    pending.reject(new Error("permission denied"));
    await expanding;

    expect(tree.treeLoadErrors.value.has("src")).toBe(false);
  });
});
