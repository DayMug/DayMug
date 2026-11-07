import { afterEach, describe, expect, it, vi } from "vitest";
import { ref } from "vue";
import type { DeviceView } from "./apiDevices";

const { device } = vi.hoisted(() => ({ device: { current: null as unknown } }));
vi.mock("./useDesktopDevice", () => ({
  useDesktopDevice: () => ({ device: device.current, ensureDesktopDevice: vi.fn() }),
}));

import { setConversationDeviceBinding, useConversationReadOnly } from "./useConversationDevice";

const own = ref<DeviceView | null>(null);
device.current = own;

const binding = { device_id: "d1", device_name: "laptop", work_dir: "/w", online: true };
const nonce = "n".repeat(40);

function asClient() {
  window.__DAYMUG_CLIENT__ = { nonce, version: "1", os: "macos", hostname: "laptop" };
}

function view(id: string): DeviceView {
  return { id, name: id, os: "macos", created_at: "", last_seen_at: "", online: true };
}

afterEach(() => {
  delete window.__DAYMUG_CLIENT__;
  own.value = null;
});

describe("useConversationReadOnly", () => {
  it("leaves a server conversation writable everywhere", () => {
    setConversationDeviceBinding("c-server", null);
    expect(useConversationReadOnly(() => "c-server").value).toBe(false);
  });

  it("locks a computer-bound conversation in a plain browser", () => {
    setConversationDeviceBinding("c1", binding);
    expect(useConversationReadOnly(() => "c1").value).toBe(true);
  });

  it("keeps it writable only in the client of the bound computer", () => {
    setConversationDeviceBinding("c1", binding);
    asClient();
    const readOnly = useConversationReadOnly(() => "c1");
    expect(readOnly.value).toBe(false); // own device not resolved yet
    own.value = view("d1");
    expect(readOnly.value).toBe(false);
    own.value = view("d2");
    expect(readOnly.value).toBe(true);
  });
});
