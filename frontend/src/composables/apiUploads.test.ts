import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { uploadFile } from "./apiUploads";

// FakeXHR mirrors the fixture in useFileApi.test.ts: the upload path uses
// XMLHttpRequest (so we can surface progress events), and fetch-level mocks
// can't drive it. The smaller surface here only needs `load`, since the
// other branches (abort, error, progress) are already exercised by the
// workspace test and share the same shape.
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
  listeners: Record<string, ((e?: ProgressEvent) => void)[]> = {};
  openCalls: { method: string; url: string }[] = [];
  sendBody: unknown = null;
  constructor() {
    FakeXHR.instances.push(this);
  }
  open(method: string, url: string) {
    this.openCalls.push({ method, url });
  }
  send(body: unknown) {
    this.sendBody = body;
  }
  abort() {
    (this.listeners.abort ?? []).forEach((cb) => cb());
  }
  addEventListener(name: string, cb: (e?: ProgressEvent) => void) {
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
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("uploadFile (chat attachments)", () => {
  it("resolves on 2xx with the parsed response body", async () => {
    const file = new Blob(["hi"], { type: "text/plain" });
    const promise = uploadFile(file, "conv-1", "hi.txt");
    expect(FakeXHR.instances).toHaveLength(1);
    const xhr = FakeXHR.instances[0];
    expect(xhr.openCalls[0].method).toBe("POST");
    expect(xhr.openCalls[0].url).toContain("/api/uploads");
    xhr.triggerLoad(
      201,
      JSON.stringify({ ref: "./x", name: "hi.txt", size: 2, mime: "text/plain" }),
    );
    await expect(promise).resolves.toMatchObject({ ref: "./x", name: "hi.txt" });
  });

  it("surfaces a friendly hint on 413 (proxy / server cap)", async () => {
    // Nginx in front of DayMug typically caps uploads via `client_max_body_size`
    // and rejects with a bare 413 + HTML body. The user shouldn't have to
    // know which layer rejected them — show a localized message that names
    // the proxy as a likely cause.
    const file = new Blob(["x".repeat(1024)], { type: "application/octet-stream" });
    const promise = uploadFile(file, "conv-1", "big.bin");
    FakeXHR.instances[0].triggerLoad(413, "<html>413 Request Entity Too Large</html>");
    await expect(promise).rejects.toThrow(/Nginx|too large/i);
  });

  it("falls back to the JSON `error` field for other non-2xx statuses", async () => {
    const file = new Blob(["x"], { type: "text/plain" });
    const promise = uploadFile(file, "conv-1", "x.txt");
    FakeXHR.instances[0].triggerLoad(400, JSON.stringify({ error: "bad form" }));
    await expect(promise).rejects.toThrow("bad form");
  });
});
