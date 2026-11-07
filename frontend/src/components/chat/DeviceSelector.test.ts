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
import { loadAgentPrefs } from "@/lib/agentPrefs";

const device = {
  id: "d1",
  name: "mbp",
  os: "macos",
  created_at: "",
  last_seen_at: "",
  online: true,
};

function mountSelector(conversationStarted = false) {
  const i18n = createI18n({ legacy: false, locale: "en", messages: { en } });
  return mount(DeviceSelector, {
    props: { conversationId: "c1", agentId: "agent-1", conversationStarted },
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
    expect(loadAgentPrefs("agent-1").location).toEqual({
      kind: "device",
      device_id: "d1",
      work_dir: "/Users/alice/proj",
    });
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
    expect(loadAgentPrefs("agent-1").location).toEqual({ kind: "server" });
    wrapper.unmount();
  });

  it("only shows the folder once a bound conversation has started", async () => {
    localStorage.setItem("daymug.desktop.recentDirs", JSON.stringify(["/w", "/other"]));
    api.fetchConversationDevice.mockResolvedValue({
      device_id: "d1",
      device_name: "mbp",
      work_dir: "/w",
      online: true,
    });
    const wrapper = mountSelector(true);
    await flushPromises();
    await wrapper.get('[data-testid="device-selector-trigger"]').trigger("click");
    expect(wrapper.get('[data-testid="device-option-current"]').text()).toContain("/w");
    expect(wrapper.find('[data-testid="device-locked-note"]').exists()).toBe(true);
    for (const id of ["device-option-server", "device-option-recent", "device-option-pick"]) {
      expect(wrapper.find(`[data-testid="${id}"]`).exists()).toBe(false);
    }
    wrapper.unmount();
  });

  it("keeps a started conversation switchable while it runs on the server", async () => {
    const wrapper = mountSelector(true);
    await flushPromises();
    await wrapper.get('[data-testid="device-selector-trigger"]').trigger("click");
    expect(wrapper.find('[data-testid="device-option-pick"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="device-locked-note"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it("removes a folder from the recent list", async () => {
    localStorage.setItem("daymug.desktop.recentDirs", JSON.stringify(["/a", "/b"]));
    const wrapper = mountSelector();
    await flushPromises();
    await wrapper.get('[data-testid="device-selector-trigger"]').trigger("click");
    expect(wrapper.findAll('[data-testid="device-option-recent"]')).toHaveLength(2);
    await wrapper.findAll('[data-testid="device-recent-remove"]')[0]!.trigger("click");
    const rows = wrapper.findAll('[data-testid="device-option-recent"]');
    expect(rows.map((r) => r.text())).toEqual(["/b"]);
    expect(wrapper.find('[data-testid="device-selector-menu"]').exists()).toBe(true);
    expect(JSON.parse(localStorage.getItem("daymug.desktop.recentDirs") ?? "[]")).toEqual(["/b"]);
    expect(api.setConversationDevice).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});
