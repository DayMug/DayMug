import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick, ref } from "vue";

import WorkspaceFileTree from "./WorkspaceFileTree.vue";
import type { WorkspaceEntryContext } from "./workspaceEntryContext";
import { useWorkspaceTree } from "@/composables/useWorkspaceTree";
import { makeEntry, makeEntryContext } from "@/test/workspaceEntryContext";

const rootEntries = [makeEntry("src", true), makeEntry("main.ts")];

function mountTree(overrides: Partial<WorkspaceEntryContext> = {}) {
  const listDir = vi.fn().mockResolvedValue({ entries: [makeEntry("Button.vue")] });
  const tree = useWorkspaceTree({
    userId: ref("u1"),
    currentPath: ref("."),
    entries: ref(rootEntries),
    showHiddenFiles: ref(false),
    listDir,
  });
  const { ctx, provide } = makeEntryContext(overrides);
  const wrapper = mount(WorkspaceFileTree, { props: { tree }, global: { provide } });
  return { ctx, wrapper, tree, listDir };
}

describe("WorkspaceFileTree", () => {
  it("renders a row per tree path with directories first", () => {
    const { wrapper } = mountTree();
    const rows = wrapper.findAll(".file-row");
    expect(rows.map((r) => r.attributes("data-tree-path"))).toEqual(["src", "main.ts"]);
    expect(rows.every((row) => row.attributes("data-cursor-surface") === "pointer")).toBe(true);
  });

  it("expands a directory through the toggle and indents its children", async () => {
    const { wrapper, listDir } = mountTree();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await nextTick();

    expect(listDir).toHaveBeenCalledWith("u1", "src");
    const child = wrapper.find("[data-tree-path='src/Button.vue']");
    expect(child.exists()).toBe(true);
    expect(child.attributes("aria-level")).toBe("2");
  });

  it("passes the full tree path when a nested row is clicked", async () => {
    const { ctx, wrapper } = mountTree();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await nextTick();

    await wrapper.find("[data-tree-path='src/Button.vue']").trigger("click");
    expect(ctx.onEntryClick).toHaveBeenCalledWith(
      expect.objectContaining({ name: "Button.vue" }),
      expect.any(Object),
      "src/Button.vue",
    );
  });

  it("highlights the nested row the panel marked as selected", async () => {
    const { ctx, wrapper } = mountTree();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await nextTick();

    ctx.selectedTreePath.value = "src/Button.vue";
    await nextTick();
    expect(wrapper.find("[data-tree-path='src/Button.vue']").classes()).toContain("selected");
  });

  it("routes drops to the folder under the cursor, and nested files to their folder", async () => {
    const { ctx, wrapper } = mountTree();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await nextTick();

    await wrapper.find("[data-tree-path='src']").trigger("drop");
    expect(ctx.onFolderDrop).toHaveBeenLastCalledWith(expect.any(Object), "src", undefined);
    await wrapper.find("[data-tree-path='src/Button.vue']").trigger("drop");
    expect(ctx.onFolderDrop).toHaveBeenLastCalledWith(expect.any(Object), "src", "src");
    // A top-level file is left to the panel body (the current folder).
    await wrapper.find("[data-tree-path='main.ts']").trigger("drop");
    expect(ctx.onFolderDrop).toHaveBeenCalledTimes(2);
  });

  it("keeps nested rows from being dragged, which only applies to the current listing", async () => {
    const { ctx, wrapper } = mountTree();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await nextTick();

    await wrapper.find("[data-tree-path='src/Button.vue']").trigger("dragstart");
    expect(ctx.onEntryDragStart).not.toHaveBeenCalled();

    await wrapper.find("[data-tree-path='src']").trigger("dragstart");
    expect(ctx.onEntryDragStart).toHaveBeenCalledTimes(1);
  });

  it("only offers inline rename on root rows", async () => {
    const { ctx, wrapper } = mountTree();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await nextTick();

    ctx.renamingEntry.value = "Button.vue";
    await nextTick();
    expect(wrapper.findAll("input")).toHaveLength(0);

    ctx.renamingEntry.value = "main.ts";
    await nextTick();
    expect(wrapper.findAll("input")).toHaveLength(1);
  });
});
