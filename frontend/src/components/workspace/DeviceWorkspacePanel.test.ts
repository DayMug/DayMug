import { afterEach, describe, expect, it, vi } from "vitest";
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
  });
}

describe("DeviceWorkspacePanel", () => {
  afterEach(() => vi.clearAllMocks());

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
    const names = wrapper.findAll('[data-testid="device-workspace-entry"]').map((e) => e.text());
    expect(names).toEqual(["src", "b.txt3 B"]);
    expect(wrapper.get('[data-testid="device-workspace-breadcrumbs"]').text()).toContain("proj");

    await wrapper.get('[data-testid="device-workspace-hidden"]').trigger("click");
    expect(wrapper.findAll('[data-testid="device-workspace-entry"]')).toHaveLength(3);
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
    await wrapper.get('[data-testid="device-workspace-entry"]').trigger("click");
    await flushPromises();
    expect(api.listDeviceFiles).toHaveBeenLastCalledWith("c1", "src", expect.anything());

    await wrapper.get('[data-testid="device-workspace-entry"]').trigger("click");
    await flushPromises();
    expect(api.readDeviceFile).toHaveBeenCalledWith("c1", "src/main.go", expect.anything());
    expect(wrapper.get('[data-testid="device-workspace-text"]').text()).toBe("package main");
    expect(wrapper.get('[data-testid="device-workspace-download"]').attributes("href")).toBe(
      "/dl/c1/src/main.go",
    );

    await wrapper.get('[data-testid="device-workspace-back"]').trigger("click");
    expect(wrapper.find('[data-testid="device-workspace-list"]').exists()).toBe(true);
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
