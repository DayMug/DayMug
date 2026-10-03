import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";

import WorkspaceUploadProgress from "./WorkspaceUploadProgress.vue";

function mountProgress(
  overrides: Partial<{ loaded: number; total: number }> = {},
  cancelled = false,
) {
  return mount(WorkspaceUploadProgress, {
    props: {
      progress: {
        fileCount: 3,
        doneCount: 1,
        loaded: 512,
        total: 1024,
        speed: 2048,
        ...overrides,
      },
      cancelled,
    },
  });
}

describe("WorkspaceUploadProgress", () => {
  it("sizes the bar to the transferred fraction", () => {
    const bar = mountProgress().find(".bg-green-500");
    expect(bar.attributes("style")).toContain("width: 50%");
  });

  it("clamps the bar at 100% when the reported total is zero", () => {
    const bar = mountProgress({ loaded: 10, total: 0 }).find(".bg-green-500");
    expect(bar.attributes("style")).toContain("width: 0%");
  });

  it("emits cancel when the cancel button is pressed", async () => {
    const wrapper = mountProgress();
    await wrapper.find("[data-testid='workspace-upload-cancel']").trigger("click");
    expect(wrapper.emitted("cancel")).toHaveLength(1);
  });

  it("disables the cancel button once cancellation is in flight", () => {
    const wrapper = mountProgress({}, true);
    expect(
      wrapper.find("[data-testid='workspace-upload-cancel']").attributes("disabled"),
    ).toBeDefined();
  });
});
