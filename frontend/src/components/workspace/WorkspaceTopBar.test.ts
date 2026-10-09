import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import WorkspaceTopBar from "./WorkspaceTopBar.vue";

const breadcrumbs = [
  { label: "Home", path: "." },
  { label: "src", path: "src" },
  { label: "components", path: "src/components" },
];

function mountBar(props: Partial<InstanceType<typeof WorkspaceTopBar>["$props"]> = {}) {
  return mount(WorkspaceTopBar, {
    props: {
      breadcrumbs,
      isDragOverTopBar: false,
      canDropToParent: false,
      viewMode: "grid" as const,
      ...props,
    },
  });
}

describe("WorkspaceTopBar", () => {
  it("renders every crumb but the implicit Home one, with the trailing crumb as a pill", () => {
    const wrapper = mountBar();
    const buttons = wrapper.findAll(".crumb-btn");
    expect(buttons.map((b) => b.text())).toEqual(["src", "components"]);
    expect(buttons[1].element.tagName).toBe("SPAN");
  });

  it("separates Home from the first crumb like every other level", () => {
    const wrapper = mountBar();
    expect(wrapper.get(".ws-breadcrumbs").text().replace(/\s+/g, "")).toBe("/src/components");
  });

  it("emits navigate with the crumb path when an intermediate crumb is clicked", async () => {
    const wrapper = mountBar();
    await wrapper.findAll("button.crumb-btn")[0].trigger("click");
    expect(wrapper.emitted("navigate")).toEqual([["src"]]);
  });

  it("emits navigate with '.' from the home button", async () => {
    const wrapper = mountBar();
    await wrapper.find(".home-btn").trigger("click");
    expect(wrapper.emitted("navigate")).toEqual([["."]]);
  });

  it("swaps the breadcrumbs for the drop hint while a droppable drag hovers", () => {
    const wrapper = mountBar({ isDragOverTopBar: true, canDropToParent: true });
    expect(wrapper.find("[data-testid='workspace-topbar-hint']").exists()).toBe(true);
    expect(wrapper.find(".ws-breadcrumbs").exists()).toBe(false);
  });

  it("keeps the breadcrumbs when the hovering drag has nowhere to drop to", () => {
    const wrapper = mountBar({ isDragOverTopBar: true, canDropToParent: false });
    expect(wrapper.find("[data-testid='workspace-topbar-hint']").exists()).toBe(false);
  });

  it("renders the location as a heading on the file canvas, not as a second chrome bar", () => {
    const wrapper = mountBar();
    const location = wrapper.get(".ws-location");

    // The action rail owns the only rule in the browser; anything below it
    // shares the canvas background so the files read as one surface.
    expect(location.classes()).toContain("bg-[#f7f7f7]");
    expect(location.classes().some((c) => c.startsWith("border-b"))).toBe(false);
  });

  it("only offers the collapse button when the parent allows collapsing", async () => {
    expect(mountBar().find("[data-testid='workspace-collapse']").exists()).toBe(false);

    const wrapper = mountBar({ canCollapse: true });
    await wrapper.find("[data-testid='workspace-collapse']").trigger("click");
    expect(wrapper.emitted("collapse")).toHaveLength(1);
  });

  it("switches the listing view from the segmented control", async () => {
    const wrapper = mountBar({ viewMode: "grid" });
    const list = wrapper.find('[data-testid="workspace-view-list"]');

    expect(wrapper.find('[data-testid="workspace-view-grid"]').attributes("aria-pressed")).toBe(
      "true",
    );
    expect(list.attributes("aria-pressed")).toBe("false");

    await list.trigger("click");
    expect(wrapper.emitted("set-view-mode")).toEqual([["list"]]);
  });

  it("exposes the reference directory actions", async () => {
    const wrapper = mountBar();

    await wrapper.get('[data-testid="workspace-new-folder"]').trigger("click");
    await wrapper.get('[data-testid="workspace-new-file"]').trigger("click");
    await wrapper.get('[data-testid="workspace-upload"]').trigger("click");
    await wrapper.get('[data-testid="workspace-refresh"]').trigger("click");

    expect(wrapper.emitted("new-folder")).toHaveLength(1);
    expect(wrapper.emitted("new-file")).toHaveLength(1);
    expect(wrapper.emitted("upload")).toHaveLength(1);
    expect(wrapper.emitted("refresh")).toHaveLength(1);
  });
});
