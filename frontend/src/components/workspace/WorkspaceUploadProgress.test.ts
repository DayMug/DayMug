import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import type { UploadResult } from "@/composables/useWorkspaceUpload";

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
      results: [],
      cancelled,
    },
  });
}

const finished: UploadResult[] = [
  { id: 1, name: "a.txt", status: "done" },
  { id: 2, name: "b.txt", status: "done", resolved: "overwrite" },
  { id: 3, name: "c.txt", status: "skipped" },
  { id: 4, name: "d.txt", status: "failed", error: "upload: 500" },
];

function mountFinished(results: UploadResult[] = finished) {
  return mount(WorkspaceUploadProgress, { props: { progress: null, results, cancelled: false } });
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

  it("summarises a finished run and only closes from the X", async () => {
    const wrapper = mountFinished();
    expect(wrapper.text()).toContain("2 succeeded · 1 failed · 1 skipped · 0 cancelled");
    expect(wrapper.find("[data-testid='workspace-upload-cancel']").exists()).toBe(false);
    await wrapper.find("[data-testid='workspace-upload-dismiss']").trigger("click");
    expect(wrapper.emitted("dismiss")).toHaveLength(1);
  });

  it("opens the file list by itself when a file failed", () => {
    const rows = mountFinished().findAll(".ws-upload-result");
    expect(rows.map((r) => r.attributes("data-status"))).toEqual([
      "done",
      "done",
      "skipped",
      "failed",
    ]);
    expect(rows[1].text()).toContain("Overwritten");
    expect(rows[3].text()).toContain("upload: 500");
  });

  it("keeps the file list folded until the header is pulled open", async () => {
    const wrapper = mountFinished(finished.slice(0, 3));
    expect(wrapper.find("[data-testid='workspace-upload-results']").exists()).toBe(false);
    await wrapper.find("[data-testid='workspace-upload-toggle']").trigger("click");
    expect(wrapper.findAll(".ws-upload-result")).toHaveLength(3);
    await wrapper.find("[data-testid='workspace-upload-toggle']").trigger("click");
    expect(wrapper.find("[data-testid='workspace-upload-results']").exists()).toBe(false);
  });
});
