import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { UnauthorizedError } from "./apiClient";
import { xhrUpload } from "./xhrUpload";

class FakeXHR {
  static instances: FakeXHR[] = [];
  status = 200;
  responseText = "";
  withCredentials = false;
  upload = {
    listeners: {} as Record<string, ((e: ProgressEvent) => void)[]>,
    addEventListener(name: string, cb: (e: ProgressEvent) => void) {
      (this.listeners[name] ??= []).push(cb);
    },
  };
  listeners: Record<string, (() => void)[]> = {};
  constructor() {
    FakeXHR.instances.push(this);
  }
  open() {}
  send() {}
  abort() {
    (this.listeners.abort ?? []).forEach((cb) => cb());
  }
  addEventListener(name: string, cb: () => void) {
    (this.listeners[name] ??= []).push(cb);
  }
  triggerLoad(status: number, body = "") {
    this.status = status;
    this.responseText = body;
    (this.listeners.load ?? []).forEach((cb) => cb());
  }
}

beforeEach(() => {
  vi.stubGlobal("XMLHttpRequest", FakeXHR as unknown as typeof XMLHttpRequest);
  FakeXHR.instances = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("xhrUpload", () => {
  // Status mapping belongs to each endpoint's caller; the transport must not
  // turn a 409 or 413 into an error of its own.
  it("resolves with the finished request for non-401 statuses", async () => {
    const promise = xhrUpload("/api/x", new FormData());
    FakeXHR.instances[0].triggerLoad(409, '{"conflicts":["a.txt"]}');
    const xhr = await promise;
    expect(xhr.status).toBe(409);
    expect(xhr.responseText).toBe('{"conflicts":["a.txt"]}');
  });

  it("rejects 401 with UnauthorizedError", async () => {
    const promise = xhrUpload("/api/x", new FormData());
    FakeXHR.instances[0].triggerLoad(401);
    await expect(promise).rejects.toBeInstanceOf(UnauthorizedError);
  });

  it("reports progress and aborts through the signal", async () => {
    const controller = new AbortController();
    const onProgress = vi.fn();
    const promise = xhrUpload("/api/x", new FormData(), { onProgress, signal: controller.signal });
    const xhr = FakeXHR.instances[0];
    xhr.upload.listeners.progress[0]({
      lengthComputable: true,
      loaded: 5,
      total: 10,
    } as ProgressEvent);
    expect(onProgress).toHaveBeenCalledWith({ loaded: 5, total: 10 });

    controller.abort();
    await expect(promise).rejects.toMatchObject({ name: "AbortError" });
  });

  it("rejects without sending when the signal is already aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    await expect(
      xhrUpload("/api/x", new FormData(), { signal: controller.signal }),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(FakeXHR.instances).toHaveLength(0);
  });
});
