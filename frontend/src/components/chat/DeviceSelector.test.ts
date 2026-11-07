import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createI18n } from "vue-i18n";
import en from "@/i18n/locales/en";

const api = vi.hoisted(() => ({
  fetchCurrentDevice: vi.fn(),
  pairDevice: vi.fn(),
  pickDeviceFolder: vi.fn(),
  fetchConversationDevice: vi.fn(),
  setConversationDevice: vi.fn(),
  clearConversationDevice: vi.fn(),
}));
vi.mock("@/composables/apiDevices", () => api);

import DeviceSelector from "./DeviceSelector.vue";

const device = {
  id: "d1",
  name: "mbp",
  os: "macos",
  created_at: "",
  last_seen_at: "",
  online: true,
};

function mountSelector() {
  const i18n = createI18n({ legacy: false, locale: "en", messages: { en } });
  return mount(DeviceSelector, {
    props: { conversationId: "c1" },
    global: { plugins: [i18n] },
    attachTo: document.body,
  });
}

describe("DeviceSelector", () => {
  beforeEach(() => {
    window.__DAYMUG_CLIENT__ = { nonce: "n".repeat(40), os: "macos", hostname: "mbp" };
    localStorage.clear();
    api.fetchCurrentDevice.mockResolvedValue(device);
    api.fetchConversationDevice.mockResolvedValue(null);
  });
  afterEach(() => {
    delete window.__DAYMUG_CLIENT__;
    vi.clearAllMocks();
  });

  it("shows the server until a folder is bound", async () => {
    const wrapper = mountSelector();
    await flushPromises();
    expect(wrapper.get('[data-testid="device-selector-label"]').text()).toBe("Server");
    wrapper.unmount();
  });

  it("binds the picked folder on this computer and remembers it", async () => {
    api.pickDeviceFolder.mockResolvedValue("/Users/alice/proj");
    api.setConversationDevice.mockResolvedValue({
      device_id: "d1",
      device_name: "mbp",
      work_dir: "/Users/alice/proj",
      online: true,
    });
    const wrapper = mountSelector();
    await flushPromises();
    await wrapper.get('[data-testid="device-selector-trigger"]').trigger("click");
    await wrapper.get('[data-testid="device-option-pick"]').trigger("click");
    await flushPromises();

    expect(api.setConversationDevice).toHaveBeenCalledWith("c1", "d1", "/Users/alice/proj");
    expect(wrapper.get('[data-testid="device-selector-label"]').text()).toBe("proj");
    expect(JSON.parse(localStorage.getItem("daymug.desktop.recentDirs") ?? "[]")).toEqual([
      "/Users/alice/proj",
    ]);
    wrapper.unmount();
  });

  it("returns a bound conversation to the server", async () => {
    api.fetchConversationDevice.mockResolvedValue({
      device_id: "d1",
      device_name: "mbp",
      work_dir: "/w",
      online: true,
    });
    api.clearConversationDevice.mockResolvedValue(undefined);
    const wrapper = mountSelector();
    await flushPromises();
    await wrapper.get('[data-testid="device-selector-trigger"]').trigger("click");
    await wrapper.get('[data-testid="device-option-server"]').trigger("click");
    await flushPromises();
    expect(api.clearConversationDevice).toHaveBeenCalledWith("c1");
    expect(wrapper.get('[data-testid="device-selector-label"]').text()).toBe("Server");
    wrapper.unmount();
  });
});
