import { jsonRequestInit, request } from "./apiClient";

export interface DeviceView {
  id: string;
  name: string;
  os: string;
  created_at: string;
  last_seen_at: string;
  online: boolean;
  hostname?: string;
  version?: string;
  home?: string;
}

export interface ConversationDeviceBinding {
  device_id: string;
  device_name: string;
  work_dir: string;
  online: boolean;
}

export async function fetchCurrentDevice(nonce: string): Promise<DeviceView | null> {
  const params = new URLSearchParams({ nonce });
  const res = await request<{ device: DeviceView | null }>(
    `/api/devices/current?${params}`,
    undefined,
    { label: "fetch current device" },
  );
  return res.device;
}

export async function pairDevice(nonce: string, name: string, os: string): Promise<void> {
  await request<void>("/api/devices/pair", jsonRequestInit("POST", { nonce, name, os }), {
    label: "pair device",
    expect: "none",
  });
}

export async function pickDeviceFolder(deviceId: string, start = ""): Promise<string> {
  const res = await request<{ path: string }>(
    `/api/devices/${encodeURIComponent(deviceId)}/pick-folder`,
    jsonRequestInit("POST", { start }),
    { label: "pick folder" },
  );
  return res.path;
}

export async function fetchConversationDevice(
  conversationId: string,
): Promise<ConversationDeviceBinding | null> {
  const res = await request<{ binding: ConversationDeviceBinding | null }>(
    `/api/conversations/${encodeURIComponent(conversationId)}/device`,
    undefined,
    { label: "fetch conversation device" },
  );
  return res.binding;
}

export async function setConversationDevice(
  conversationId: string,
  deviceId: string,
  workDir: string,
): Promise<ConversationDeviceBinding> {
  const res = await request<{ binding: ConversationDeviceBinding }>(
    `/api/conversations/${encodeURIComponent(conversationId)}/device`,
    jsonRequestInit("PUT", { device_id: deviceId, work_dir: workDir }),
    { label: "set conversation device" },
  );
  return res.binding;
}

export async function clearConversationDevice(conversationId: string): Promise<void> {
  await request<void>(
    `/api/conversations/${encodeURIComponent(conversationId)}/device`,
    { method: "DELETE" },
    { label: "clear conversation device", expect: "none" },
  );
}
