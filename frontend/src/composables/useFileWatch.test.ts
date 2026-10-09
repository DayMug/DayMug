import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, ref } from "vue";

import {
  POLL_INTERVAL_MS,
  READY_TIMEOUT_MS,
  setFileWatchTransport,
  useFileWatch,
  type WebSocketLike,
} from "./useFileWatch";

// FakeSocket stands in for the browser WebSocket so the tests can drive
// open/message/close deterministically under fake timers.
class FakeSocket implements WebSocketLike {
  static instances: FakeSocket[] = [];
  readonly url: string;
  readonly sent: string[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeSocket.instances.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.closed = true;
  }

  open() {
    this.onopen?.();
  }

  emit(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }

  drop() {
    this.closed = true;
    this.onclose?.();
  }
}

const inspectFile = vi.fn();
vi.mock("./useFileApi", () => ({
  useFileApi: () => ({ inspectFile }),
}));

function latest(): FakeSocket {
  const socket = FakeSocket.instances.at(-1);
  if (!socket) throw new Error("no socket was created");
  return socket;
}

function watchedPaths(socket: FakeSocket): string[] {
  const last = socket.sent.at(-1);
  if (!last) throw new Error("nothing was sent");
  return JSON.parse(last).paths;
}

describe("useFileWatch", () => {
  let scope: ReturnType<typeof effectScope>;

  beforeEach(() => {
    vi.useFakeTimers();
    FakeSocket.instances.length = 0;
    inspectFile.mockReset();
    setFileWatchTransport((url) => new FakeSocket(url));
    scope = effectScope();
  });

  afterEach(() => {
    scope.stop();
    vi.useRealTimers();
  });

  function run(paths: string[]) {
    const userId = ref("u1");
    const pathsRef = ref(paths);
    const onChanged = vi.fn();
    const onRemoved = vi.fn();
    const api = scope.run(() => useFileWatch({ userId, paths: pathsRef, onChanged, onRemoved }))!;
    return { api, pathsRef, onChanged, onRemoved };
  }

  it("stays idle with nothing open, so no socket is built", () => {
    const { api } = run([]);
    expect(api.mode.value).toBe("idle");
    expect(FakeSocket.instances).toHaveLength(0);
  });

  it("sends the watch set once the socket opens", async () => {
    run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    expect(watchedPaths(latest())).toEqual(["a.md"]);
  });

  it("pushes a changed path through to the caller", async () => {
    const { api, onChanged } = run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "ready", watching: ["a.md"] });
    latest().emit({ type: "changed", path: "a.md", mtime: "t1", size: 3 });

    expect(onChanged).toHaveBeenCalledWith("a.md");
    expect(api.mode.value).toBe("ws");
  });

  it("reports removals separately from changes", async () => {
    const { onChanged, onRemoved } = run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "ready", watching: ["a.md"] });
    latest().emit({ type: "removed", path: "a.md" });

    expect(onRemoved).toHaveBeenCalledWith("a.md");
    expect(onChanged).not.toHaveBeenCalled();
  });

  it("re-sends the watch set after a reconnect", async () => {
    run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "ready", watching: ["a.md"] });
    const first = latest();

    first.drop();
    await vi.advanceTimersByTimeAsync(2000);
    expect(FakeSocket.instances.length).toBeGreaterThan(1);

    latest().open();
    expect(watchedPaths(latest())).toEqual(["a.md"]);
  });

  it("doubles the reconnect delay and resets it once the server is ready", async () => {
    run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();

    // Two failed attempts: 1s, then 2s.
    latest().drop();
    await vi.advanceTimersByTimeAsync(999);
    expect(FakeSocket.instances).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeSocket.instances).toHaveLength(2);

    latest().drop();
    await vi.advanceTimersByTimeAsync(1999);
    expect(FakeSocket.instances).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(FakeSocket.instances).toHaveLength(3);

    // A `ready` proves the watch works, so the next drop starts over at 1s.
    latest().open();
    latest().emit({ type: "ready", watching: ["a.md"] });
    latest().drop();
    await vi.advanceTimersByTimeAsync(1000);
    expect(FakeSocket.instances).toHaveLength(4);
  });

  it("re-sends when the open file set changes", async () => {
    const { pathsRef } = run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "ready", watching: ["a.md"] });

    pathsRef.value = ["a.md", "b.md"];
    await vi.advanceTimersByTimeAsync(0);
    expect(watchedPaths(latest())).toEqual(["a.md", "b.md"]);
  });

  it("falls back to polling when the server reports capacity", async () => {
    const { api } = run(["a.md"]);
    inspectFile.mockResolvedValue({ modified: "t1", size: 1 });
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "error", code: "capacity" });

    expect(api.mode.value).toBe("polling");
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 10);
    expect(inspectFile).toHaveBeenCalledWith("u1", "a.md");
  });

  it("falls back to polling when no ready frame arrives in time", async () => {
    // Covers NFS and container overlay filesystems, where inotify accepts the
    // watch and then never reports anything at all.
    const { api } = run(["a.md"]);
    inspectFile.mockResolvedValue({ modified: "t1", size: 1 });
    await vi.advanceTimersByTimeAsync(0);
    latest().open();

    await vi.advanceTimersByTimeAsync(READY_TIMEOUT_MS + 10);
    expect(api.mode.value).toBe("polling");
  });

  it("emits changed from polling when mtime advances", async () => {
    const { api, onChanged } = run(["a.md"]);
    inspectFile.mockResolvedValue({ modified: "t1", size: 1 });
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "error", code: "unavailable" });
    expect(api.mode.value).toBe("polling");

    // First poll is the baseline; only a later difference is a change.
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 10);
    expect(onChanged).not.toHaveBeenCalled();

    inspectFile.mockResolvedValue({ modified: "t2", size: 9 });
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 10);
    expect(onChanged).toHaveBeenCalledWith("a.md");
  });

  it("reports a removal from polling when the file 404s", async () => {
    const { onRemoved } = run(["a.md"]);
    inspectFile.mockResolvedValue({ modified: "t1", size: 1 });
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "error", code: "unavailable" });
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 10);

    inspectFile.mockRejectedValue(Object.assign(new Error("not found"), { status: 404 }));
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 10);
    expect(onRemoved).toHaveBeenCalledWith("a.md");
  });

  it("stops everything on stop()", async () => {
    const { api } = run(["a.md"]);
    inspectFile.mockResolvedValue({ modified: "t1", size: 1 });
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    latest().emit({ type: "error", code: "capacity" });
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS + 10);
    const callsBefore = inspectFile.mock.calls.length;

    api.stop();
    expect(latest().closed).toBe(true);
    expect(api.mode.value).toBe("idle");

    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3);
    expect(inspectFile).toHaveBeenCalledTimes(callsBefore);
  });

  it("does not reconnect after an intentional stop", async () => {
    const { api } = run(["a.md"]);
    await vi.advanceTimersByTimeAsync(0);
    latest().open();
    const count = FakeSocket.instances.length;

    api.stop();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeSocket.instances).toHaveLength(count);
  });
});
