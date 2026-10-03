import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";

import WorkspaceArtifactBar from "./WorkspaceArtifactBar.vue";

describe("WorkspaceArtifactBar", () => {
  it("uses extension-specific icons and activates tabs", async () => {
    const wrapper = mount(WorkspaceArtifactBar, {
      props: {
        paths: ["reports/summary.md", "reports/sales.xlsx", "reports/review.pptx"],
        activePath: "reports/sales.xlsx",
        directoryOpen: false,
        fullscreen: false,
      },
    });

    const tabs = wrapper.findAll('[data-testid="artifact-tab"]');
    expect(tabs[0].find(".lucide-file-text").exists()).toBe(true);
    expect(tabs[1].find(".lucide-file-spreadsheet").exists()).toBe(true);
    expect(tabs[2].find(".lucide-presentation").exists()).toBe(true);
    expect(tabs[1].attributes("data-active")).toBe("true");

    await tabs[0].trigger("click");
    expect(wrapper.emitted("activate")).toEqual([["reports/summary.md"]]);
  });

  it("merges the active tab into the pane below it, and flattens the strip while the directory is open", async () => {
    const wrapper = mount(WorkspaceArtifactBar, {
      props: {
        paths: ["summary.md"],
        activePath: "summary.md",
        directoryOpen: false,
        fullscreen: false,
      },
    });

    const tab = wrapper.get('[data-testid="artifact-tab"]');
    expect(tab.classes()).toEqual(
      expect.arrayContaining(["rounded-t-lg", "border-b-0", "bg-white"]),
    );

    // The directory browser owns the pane, so no tab may look connected to it.
    await wrapper.setProps({ directoryOpen: true });
    expect(wrapper.get('[data-testid="artifact-tab"]').classes()).not.toContain("bg-white");
    expect(wrapper.get('[data-testid="artifact-tabs"]').classes()).not.toContain("bg-[#f6f6f6]");
  });

  it("emits directory, fullscreen, close and collapse actions", async () => {
    const wrapper = mount(WorkspaceArtifactBar, {
      props: {
        paths: ["summary.md"],
        activePath: "summary.md",
        directoryOpen: false,
        fullscreen: false,
        canCollapse: true,
      },
    });

    await wrapper.get('[data-testid="artifact-directory-toggle"]').trigger("click");
    await wrapper.get('[data-testid="artifact-fullscreen-toggle"]').trigger("click");
    await wrapper.get('[data-testid="artifact-tab-close"]').trigger("click");
    await wrapper.get('[data-testid="workspace-collapse"]').trigger("click");

    expect(wrapper.emitted("directory")).toHaveLength(1);
    expect(wrapper.emitted("toggle-fullscreen")).toHaveLength(1);
    expect(wrapper.emitted("close")).toEqual([["summary.md"]]);
    expect(wrapper.emitted("collapse")).toHaveLength(1);
  });
});
