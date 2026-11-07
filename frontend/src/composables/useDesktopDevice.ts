import { ref } from "vue";
import { desktopClient } from "@/lib/desktopClient";
import { fetchCurrentDevice, pairDevice, type DeviceView } from "./apiDevices";

// The device this page's DayMug-Client is paired as, shared by every
// component. Pairing is silent: the logged-in page vouches for its own client
// by handing the server the client's nonce, and the client picks the
// credentials up on its own connection.
const device = ref<DeviceView | null>(null);
let inflight: Promise<DeviceView | null> | null = null;
let listening = false;

const PAIR_POLL_MS = 1000;
const PAIR_TIMEOUT_MS = 30_000;

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function resolveDevice(): Promise<DeviceView | null> {
  const client = desktopClient();
  if (!client) return null;
  const current = await fetchCurrentDevice(client.nonce);
  if (current) return current;
  await pairDevice(client.nonce, client.hostname || "DayMug-Client", client.os);
  const deadline = Date.now() + PAIR_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await sleep(PAIR_POLL_MS);
    const paired = await fetchCurrentDevice(client.nonce);
    if (paired) return paired;
  }
  return null;
}

export function ensureDesktopDevice(force = false): Promise<DeviceView | null> {
  if (!desktopClient()) return Promise.resolve(null);
  if (!listening) {
    listening = true;
    // The client dispatches this after (re)connecting, e.g. once the network
    // returns, so an offline badge clears without a reload.
    window.addEventListener("daymug-client-connected", () => void ensureDesktopDevice(true));
  }
  if (inflight && !force) return inflight;
  if (device.value?.online && !force) return Promise.resolve(device.value);
  inflight = resolveDevice()
    .then((d) => {
      device.value = d;
      return d;
    })
    .catch(() => device.value)
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

export function useDesktopDevice() {
  return { device, ensureDesktopDevice };
}
