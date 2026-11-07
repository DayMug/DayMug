import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createI18n } from "vue-i18n";
import en from "@/i18n/locales/en";

const api = vi.hoisted(() => ({
  listDeviceFiles: vi.fn(),
  readDeviceFile: vi.fn(),
  deviceFileDownloadUrl: vi.fn((id: string, path: string) => `/dl/${id}/${path}`),
}));
vi.mock("@/composables/apiDevices", () => api);

import DeviceWorkspacePanel from "./DeviceWorkspacePanel.vue";

const binding = {
  device_id: "d1",
  device_name: "mbp",
  work_dir: "/Users/alice/proj",
  online: true,
};

function listing(path: string, entries: { name: string; is_dir: boolean; size?: number }[]) {
  return {
    root: binding.work_dir,
    path,
    truncated: false,
    entries: entries.map((e) => ({ size: 0, modified: "", ...e })),
  };
}

function mountPanel() {
  const i18n = createI18n({ legacy: false, locale: "en", messages: { en } });
  return mount(DeviceWorkspacePanel, {
    props: { conversationId: "c1", binding },
    global: { plugins: [i18n] },
    attachTo: document.body,
  });
}

const rows = ".file-row";

function menuLabels() {
  return Array.from(document.querySelectorAll(".file-context-menu .ctx-item")).map((el) =>
    el.textContent?.trim(),
  );
}

describe("DeviceWorkspacePanel", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    vi.clearAllMocks();
    document.body.innerHTML = "";
  });

  it("lists the bound folder, folders first and dot-files hidden", async () => {
    api.listDeviceFiles.mockResolvedValue(
      listing(".", [
        { name: "b.txt", is_dir: false, size: 3 },
        { name: ".git", is_dir: true },
        { name: "src", is_dir: true },
      ]),
    );
    const wrapper = mountPanel();
    await flushPromises();
    expect(api.listDeviceFiles).toHaveBeenCalledWith("c1", ".", expect.anything());
    const names = wrapper.findAll(rows).map((e) => e.text());
    expect(names).toEqual(["srcFolder", "b.txt3 B"]);
    expect(wrapper.get('[data-testid="workspace-topbar"]').text()).toContain("proj");

    // Hidden files are toggled from the background menu, as on the server.
    await wrapper.get('[data-testid="device-workspace-list"]').trigger("contextmenu");
    (document.querySelector(".ctx-item-toggle-hidden") as HTMLElement).click();
    await flushPromises();
    expect(wrapper.findAll(rows)).toHaveLength(3);
  });

  it("opens folders and previews text files", async () => {
    api.listDeviceFiles
      .mockResolvedValueOnce(listing(".", [{ name: "src", is_dir: true }]))
      .mockResolvedValueOnce(listing("src", [{ name: "main.go", is_dir: false, size: 13 }]));
    api.readDeviceFile.mockResolvedValue(
      new Response("package main\n", { headers: { "Content-Type": "text/plain; charset=utf-8" } }),
    );
    const wrapper = mountPanel();
    await flushPromises();
    await wrapper.get(rows).trigger("click");
    await flushPromises();
    expect(api.listDeviceFiles).toHaveBeenLastCalledWith("c1", "src", expect.anything());

    await wrapper.get(rows).trigger("click");
    await flushPromises();
    expect(api.readDeviceFile).toHaveBeenCalledWith("c1", "src/main.go", expect.anything());
    expect(wrapper.get('[data-testid="device-workspace-text"]').text()).toBe("package main");
    expect(wrapper.get('[data-testid="device-workspace-download"]').attributes("href")).toBe(
      "/dl/c1/src/main.go",
    );

    await wrapper.get('[data-testid="device-workspace-back"]').trigger("click");
    expect(wrapper.find('[data-testid="device-workspace-list"]').exists()).toBe(true);
  });

  it("uses the server panel's read-only context menu", async () => {
    api.listDeviceFiles.mockResolvedValue(
      listing(".", [
        { name: "src", is_dir: true },
        { name: "a.txt", is_dir: false, size: 1 },
      ]),
    );
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.findAll(rows)[1]!.trigger("contextmenu");
    expect(menuLabels()).toEqual(["Open", "Copy›", "Download"]);
    expect(document.querySelector(".ctx-item-delete")).toBeNull();

    await wrapper.findAll(rows)[0]!.trigger("contextmenu");
    expect(menuLabels()).toEqual(["Open", "Copy›"]);

    await wrapper.get('[data-testid="device-workspace-list"]').trigger("contextmenu");
    expect(document.querySelector(".ctx-item-new-file")).toBeNull();
    expect(document.querySelector(".ctx-item-upload-files")).toBeNull();
    expect(document.querySelector(".ctx-item-refresh")).not.toBeNull();

    expect(wrapper.find('[data-testid="workspace-new-folder"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="workspace-upload"]').exists()).toBe(false);
  });

  it("switches to the grid view and expands folders in the tree", async () => {
    api.listDeviceFiles
      .mockResolvedValueOnce(listing(".", [{ name: "src", is_dir: true }]))
      .mockResolvedValueOnce(listing("src", [{ name: "main.go", is_dir: false, size: 1 }]));
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.get(".tree-toggle").trigger("click");
    await flushPromises();
    expect(api.listDeviceFiles).toHaveBeenLastCalledWith("c1", "src");
    expect(wrapper.findAll(rows).map((r) => r.attributes("data-tree-path"))).toEqual([
      "src",
      "src/main.go",
    ]);

    await wrapper.get('[data-testid="workspace-view-grid"]').trigger("click");
    expect(wrapper.findAll(".file-card")).toHaveLength(1);
  });

  it("shows the client's error instead of an empty folder", async () => {
    api.listDeviceFiles.mockRejectedValue(
      new Error("update DayMug-Client to browse this computer's files"),
    );
    const wrapper = mountPanel();
    await flushPromises();
    expect(wrapper.get('[data-testid="device-workspace-error"]').text()).toContain(
      "update DayMug-Client",
    );
  });
});
