import { onScopeDispose, ref, watch, type Ref } from "vue";

import { useFileApi } from "./useFileApi";
import { createBackoff } from "@/lib/backoff";

// Poll cadence for the degraded path. Slow enough to be free at one request
// per open file, fast enough that "the agent just rewrote this" still reads as
// immediate.
export const POLL_INTERVAL_MS = 1500;

// How long to wait for the server's `ready` before assuming the watch will
// never report anything. inotify is silently inert on NFS and on some
// container overlay filesystems: the subscription is accepted and then nothing
// ever arrives, which is indistinguishable from a quiet file unless we time it
// out on our own.
export const READY_TIMEOUT_MS = 5000;

// The slice of the browser WebSocket this composable actually uses. Narrowed
// so tests can inject a fake without reimplementing the whole interface.
export interface WebSocketLike {
  send(data: string): void;
  close(): void;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  onclose: (() => void) | null;
  onerror: (() => void) | null;
}

export interface FileWatchOptions {
  userId: Ref<string>;
  // The files currently on screen. Replacing this ref's value re-subscribes.
  paths: Ref<string[]>;
  onChanged: (path: string) => void;
  onRemoved: (path: string) => void;
}

type Transport = (url: string) => WebSocketLike;

let transport: Transport = (url) => new WebSocket(url) as unknown as WebSocketLike;

export function setFileWatchTransport(factory: Transport) {
  transport = factory;
}

function defaultURL(userId: string): string {
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/api/users/${encodeURIComponent(userId)}/files/watch`;
}

interface Validator {
  modified: string;
  size: number;
}

export function useFileWatch({ userId, paths, onChanged, onRemoved }: FileWatchOptions) {
  const { inspectFile } = useFileApi();

  const mode = ref<"ws" | "polling" | "idle">("idle");

  let socket: WebSocketLike | null = null;
  let readyTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let pollTimer: ReturnType<typeof setInterval> | null = null;
  const reconnectBackoff = createBackoff();
  let stopped = false;
  // Cleared whenever the transport changes so a socket→poll switch doesn't
  // replay every open file as "changed" from a stale baseline.
  let validators = new Map<string, Validator>();

  function clearReadyTimer() {
    if (readyTimer !== null) {
      clearTimeout(readyTimer);
      readyTimer = null;
    }
  }

  function clearReconnectTimer() {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  function stopPolling() {
    if (pollTimer !== null) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  // Both wrappers swallow errors on purpose. A socket torn down or written to
  // while still CONNECTING is routine here — the user switches files faster
  // than a handshake completes — and some implementations throw on that rather
  // than aborting quietly. Letting it escape would take the whole workbench
  // render down over a subscription that is, at worst, missing.
  function closeSocket() {
    if (!socket) return;
    const s = socket;
    socket = null;
    s.onopen = null;
    s.onmessage = null;
    s.onclose = null;
    s.onerror = null;
    try {
      s.close();
    } catch {
      // Already closing, or never opened.
    }
  }

  function trySend(s: WebSocketLike, data: string): boolean {
    try {
      s.send(data);
      return true;
    } catch {
      return false;
    }
  }

  // Degrading is one-way for the lifetime of this composable: a server that
  // just told us it cannot watch, or a watch that proved inert, will not
  // become trustworthy on a retry, and flapping between the two transports
  // would produce duplicate reloads.
  function degradeToPolling() {
    if (stopped || mode.value === "polling") return;
    clearReadyTimer();
    clearReconnectTimer();
    closeSocket();
    validators = new Map();
    mode.value = "polling";
    startPolling();
  }

  function startPolling() {
    stopPolling();
    if (stopped || paths.value.length === 0) return;
    pollTimer = setInterval(() => void pollOnce(), POLL_INTERVAL_MS);
  }

  async function pollOnce() {
    const current = [...paths.value];
    for (const path of current) {
      try {
        const info = await inspectFile(userId.value, path);
        if (stopped) return;
        const seen = validators.get(path);
        // The first sighting is the baseline, never a change — otherwise
        // every switch to polling would announce a change that never happened.
        if (seen && (seen.modified !== info.modified || seen.size !== info.size)) {
          onChanged(path);
        }
        validators.set(path, { modified: info.modified, size: info.size });
      } catch (err) {
        if (stopped) return;
        const status = (err as { status?: number }).status;
        if (status === 404 && validators.has(path)) {
          validators.delete(path);
          onRemoved(path);
        }
      }
    }
  }

  function sendWatchSet() {
    if (!socket || paths.value.length === 0) return;
    if (!trySend(socket, JSON.stringify({ action: "watch", paths: [...paths.value] }))) return;
    clearReadyTimer();
    readyTimer = setTimeout(degradeToPolling, READY_TIMEOUT_MS);
  }

  function handleFrame(raw: string) {
    let frame: { type?: string; path?: string; code?: string };
    try {
      frame = JSON.parse(raw);
    } catch {
      return;
    }
    switch (frame.type) {
      case "ready":
        clearReadyTimer();
        reconnectBackoff.reset();
        mode.value = "ws";
        break;
      case "changed":
        if (frame.path) onChanged(frame.path);
        break;
      case "removed":
        if (frame.path) onRemoved(frame.path);
        break;
      case "error":
        // Every code lands here on purpose. capacity / ignored / unavailable /
        // forbidden differ in cause but not in remedy: polling is the only
        // thing the client can still do.
        degradeToPolling();
        break;
    }
  }

  function scheduleReconnect() {
    if (stopped || mode.value === "polling") return;
    clearReconnectTimer();
    reconnectTimer = setTimeout(connect, reconnectBackoff.next());
  }

  function connect() {
    if (stopped || mode.value === "polling") return;
    if (!userId.value || paths.value.length === 0) return;
    closeSocket();

    const s = transport(defaultURL(userId.value));
    socket = s;
    s.onopen = () => sendWatchSet();
    s.onmessage = (ev) => handleFrame(ev.data);
    s.onclose = () => {
      if (socket !== s) return;
      socket = null;
      clearReadyTimer();
      scheduleReconnect();
    };
    s.onerror = () => {
      // onclose follows; reconnect scheduling lives there so an error that
      // does not close the socket doesn't spawn a second connection.
    };
  }

  function stop() {
    stopped = true;
    clearReadyTimer();
    clearReconnectTimer();
    stopPolling();
    closeSocket();
    validators = new Map();
    mode.value = "idle";
  }

  watch(
    [userId, paths],
    ([, nextPaths], previous) => {
      if (stopped) return;
      if (nextPaths.length === 0) {
        clearReadyTimer();
        clearReconnectTimer();
        stopPolling();
        closeSocket();
        mode.value = "idle";
        return;
      }
      // A different agent means a different work dir, so the socket and every
      // validator baseline belong to a workspace we are no longer looking at.
      if (previous && previous[0] !== userId.value) {
        closeSocket();
        validators = new Map();
      }
      if (mode.value === "polling") {
        startPolling();
        return;
      }
      if (socket) sendWatchSet();
      else connect();
    },
    { immediate: true, deep: true },
  );

  onScopeDispose(stop);

  return { mode, stop };
}
