import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";

import WorkspaceFileGrid from "./WorkspaceFileGrid.vue";
import type { WorkspaceEntryContext } from "./workspaceEntryContext";
import { makeEntry, makeEntryContext } from "@/test/workspaceEntryContext";

const entries = [makeEntry("src", true), makeEntry("main.ts")];

function mountGrid(overrides: Partial<WorkspaceEntryContext> = {}) {
  const { ctx, provide } = makeEntryContext(overrides);
  const wrapper = mount(WorkspaceFileGrid, {
    props: { entries },
    attachTo: document.body,
    global: { provide },
  });
  return { ctx, wrapper };
}

describe("WorkspaceFileGrid", () => {
  it("renders one tile per entry keyed by name", () => {
    const { wrapper } = mountGrid();
    const cards = wrapper.findAll(".file-card");
    expect(cards.map((c) => c.attributes("data-entry-name"))).toEqual(["src", "main.ts"]);
    expect(cards.every((card) => card.attributes("data-cursor-surface") === "pointer")).toBe(true);
  });

  it("marks selected entries and dims cut ones", async () => {
    const { ctx, wrapper } = mountGrid();
    ctx.selectedEntries.value = new Set(["src"]);
    ctx.cutNames.value = new Set(["main.ts"]);
    await nextTick();

    const cards = wrapper.findAll(".file-card");
    expect(cards[0].classes()).toContain("selected");
    expect(cards[1].classes()).toContain("is-cut");
  });

  it("insets the selection and drop-target outline so the scroll body cannot clip it", async () => {
    const { ctx, wrapper } = mountGrid();
    ctx.selectedEntries.value = new Set(["main.ts"]);
    ctx.dragOverTarget.value = "src";
    await nextTick();

    for (const card of wrapper.findAll(".file-card")) {
      expect(card.classes()).toContain("outline");
      expect(card.classes()).toContain("-outline-offset-[1.5px]");
    }
  });

  it("forwards a tile click to the panel's entry-click handler", async () => {
    const { ctx, wrapper } = mountGrid();
    await wrapper.findAll(".file-card")[1].trigger("click");
    expect(ctx.onEntryClick).toHaveBeenCalledWith(entries[1], expect.any(Object));
  });

  it("forwards a double click without a path so the panel treats it as a root entry", async () => {
    const { ctx, wrapper } = mountGrid();
    await wrapper.findAll(".file-card")[0].trigger("dblclick");
    expect(ctx.onEntryDblClick).toHaveBeenCalledWith(entries[0], expect.any(Object));
  });

  it("only routes drops onto directory tiles to the folder handler", async () => {
    const { ctx, wrapper } = mountGrid();
    const cards = wrapper.findAll(".file-card");
    await cards[0].trigger("drop");
    await cards[1].trigger("drop");
    expect(ctx.onFolderDrop).toHaveBeenCalledTimes(1);
    expect(ctx.onFolderDrop).toHaveBeenCalledWith(expect.any(Object), "src");
  });

  it("lets a drop on a file tile bubble up so the panel uploads it to the current folder", async () => {
    const { wrapper } = mountGrid();
    const onBodyDrop = vi.fn();
    wrapper.element.addEventListener("drop", onBodyDrop);
    await wrapper.findAll(".file-card")[1].trigger("drop");
    expect(onBodyDrop).toHaveBeenCalledTimes(1);
    onBodyDrop.mockClear();
    await wrapper.findAll(".file-card")[0].trigger("drop");
    // The folder handler is a mock here; the real one stops propagation.
    expect(onBodyDrop).toHaveBeenCalledTimes(1);
  });

  it("swaps the name for an editable input while that entry is being renamed", async () => {
    const { ctx, wrapper } = mountGrid();
    ctx.renamingEntry.value = "main.ts";
    ctx.renameValue.value = "main.ts";
    await nextTick();

    const inputs = wrapper.findAll("input");
    expect(inputs).toHaveLength(1);
    await inputs[0].trigger("blur");
    expect(ctx.confirmRename).toHaveBeenCalled();
  });

  it("focuses the rename input when the panel asks it to", async () => {
    const { ctx, wrapper } = mountGrid();
    ctx.renamingEntry.value = "main.ts";
    await nextTick();

    wrapper.vm.focusRenameInput();
    expect(document.activeElement).toBe(wrapper.find("input").element);
  });
});
