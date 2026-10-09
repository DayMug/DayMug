import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  useFileApi,
  isHtmlFile,
  isSupportedArchive,
  shouldInspectTextFile,
  workspacePathFromPreviewUrl,
  WriteConflictError,
} from "./useFileApi";

const mockFetch = vi.fn();

// FakeXHR captures calls to open/send and exposes hooks for tests to drive
// `load`, `error`, and `upload.progress` events. The XHR upload path can't
// be exercised via fetch mocks, so we install this in place of the global.
class FakeXHR {
  static instances: FakeXHR[] = [];
  status = 200;
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
  triggerLoad(status = 200) {
    this.status = status;
    (this.listeners.load ?? []).forEach((cb) => cb());
  }
  triggerProgress(loaded: number, total: number) {
    (this.upload.listeners.progress ?? []).forEach((cb) =>
      cb({ lengthComputable: true, loaded, total } as ProgressEvent),
    );
  }
}

beforeEach(() => {
  vi.stubGlobal("fetch", mockFetch);
  vi.stubGlobal("XMLHttpRequest", FakeXHR as unknown as typeof XMLHttpRequest);
  mockFetch.mockReset();
  FakeXHR.instances = [];
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("useFileApi", () => {
  it("listDir calls correct endpoint", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ path: ".", entries: [] }),
    });
    const result = await api.listDir("u1", ".");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("/api/users/u1/files?");
    expect(result.entries).toEqual([]);
  });

  it("listDir throws on error", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({ ok: false, status: 404 });
    await expect(api.listDir("u1", ".")).rejects.toThrow("list dir: 404");
  });

  it("readFile calls correct endpoint", async () => {
    const api = useFileApi();
    const mockRes = { ok: true };
    mockFetch.mockResolvedValue(mockRes);
    const res = await api.readFile("u1", "test.txt");
    expect(res).toBe(mockRes);
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("/files/read?");
  });

  it("inspectFile calls correct endpoint and returns file inspection", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({
      ok: true,
      json: () =>
        Promise.resolve({
          path: "README",
          name: "README",
          is_dir: false,
          size: 5,
          modified: "",
          content_type: "text/plain; charset=utf-8",
          is_text: true,
        }),
    });
    const res = await api.inspectFile("u1", "README");
    expect(res.is_text).toBe(true);
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("/files/inspect?");
    expect(url).toContain("path=README");
  });

  it("downloadFileUrl returns correct URL", () => {
    const api = useFileApi();
    const url = api.downloadFileUrl("u1", "file.txt");
    expect(url).toContain("/api/users/u1/files/download?");
    expect(url).toContain("path=file.txt");
  });

  it("downloadZipUrl returns correct URL", () => {
    const api = useFileApi();
    const url = api.downloadZipUrl("u1", "mydir");
    expect(url).toContain("/files/download-zip?");
    expect(url).toContain("path=mydir");
  });

  it("downloadZipUrl repeats path for multi-selection downloads", () => {
    const api = useFileApi();
    const url = api.downloadZipUrl("u1", ["src", "README.md"]);
    expect(url).toContain("/files/download-zip?");
    expect(url).toContain("path=src");
    expect(url).toContain("path=README.md");
  });

  it("uploadFiles sends multipart form via XHR and resolves on 2xx", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve({ conflicts: [] }) });
    const api = useFileApi();
    const file = new File(["hello"], "test.txt");
    const promise = api.uploadFiles("u1", ".", [file]);
    await vi.waitFor(() => expect(FakeXHR.instances).toHaveLength(1));
    const xhr = FakeXHR.instances[0];
    expect(xhr.openCalls[0].method).toBe("POST");
    expect(xhr.openCalls[0].url).toContain("/files/upload?");
    expect(xhr.sendBody).toBeInstanceOf(FormData);
    xhr.triggerLoad(200);
    await expect(promise).resolves.toBeUndefined();
  });

  it("uploadFiles surfaces progress events to onProgress", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve({ conflicts: [] }) });
    const api = useFileApi();
    const file = new File(["hello"], "test.txt");
    const events: { loaded: number; total: number }[] = [];
    const promise = api.uploadFiles("u1", ".", [file], {
      onProgress: (p) => events.push(p),
    });
    await vi.waitFor(() => expect(FakeXHR.instances).toHaveLength(1));
    const xhr = FakeXHR.instances[0];
    xhr.triggerProgress(50, 100);
    xhr.triggerProgress(100, 100);
    xhr.triggerLoad(200);
    await promise;
    expect(events).toEqual([
      { loaded: 50, total: 100 },
      { loaded: 100, total: 100 },
    ]);
  });

  it("uploadFiles rejects on non-2xx", async () => {
    mockFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve({ conflicts: [] }) });
    const api = useFileApi();
    const file = new File(["x"], "x.txt");
    const promise = api.uploadFiles("u1", ".", [file]);
    await vi.waitFor(() => expect(FakeXHR.instances).toHaveLength(1));
    FakeXHR.instances[0].triggerLoad(500);
    await expect(promise).rejects.toThrow("upload: 500");
  });

  it("uploadFiles surfaces a friendly hint on 413 (proxy / server cap)", async () => {
    // Nginx's `client_max_body_size` rejection arrives as a bare 413; we want
    // the user to learn this came from a server-side limit (most often a
    // reverse proxy) rather than reading the raw status code.
    mockFetch.mockResolvedValue({ ok: true, json: () => Promise.resolve({ conflicts: [] }) });
    const api = useFileApi();
    const file = new File(["x"], "x.txt");
    const promise = api.uploadFiles("u1", ".", [file]);
    await vi.waitFor(() => expect(FakeXHR.instances).toHaveLength(1));
    FakeXHR.instances[0].triggerLoad(413);
    await expect(promise).rejects.toThrow(/Nginx|too large/i);
  });

  it("uploadFiles reports a preflight conflict without sending the file body", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ conflicts: ["report.txt"] }),
    });
    const api = useFileApi();

    await expect(api.uploadFiles("u1", ".", [new File(["new"], "report.txt")])).rejects.toEqual(
      expect.objectContaining({ conflicts: ["report.txt"] }),
    );
    expect(FakeXHR.instances).toHaveLength(0);
  });

  it("uploadFiles sends an approved overwrite body without another preflight", async () => {
    const api = useFileApi();
    const promise = api.uploadFiles("u1", ".", [new File(["new"], "report.txt")], {
      onConflict: "overwrite",
    });

    expect(mockFetch).not.toHaveBeenCalled();
    expect(FakeXHR.instances).toHaveLength(1);
    FakeXHR.instances[0].triggerLoad(200);
    await expect(promise).resolves.toBeUndefined();
  });

  it("deleteFile calls DELETE", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({ ok: true });
    await api.deleteFile("u1", "old.txt");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain("/api/users/u1/files?");
    expect(opts.method).toBe("DELETE");
  });

  it("deleteFile throws on error", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({ ok: false, status: 403 });
    await expect(api.deleteFile("u1", "..")).rejects.toThrow("delete: 403");
  });

  it("renameFile calls PUT with correct body", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({ ok: true });
    await api.renameFile("u1", "old.txt", "new.txt");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain("/api/users/u1/files/rename");
    expect(opts.method).toBe("PUT");
    expect(JSON.parse(opts.body)).toEqual({ old_path: "old.txt", new_path: "new.txt" });
  });

  it("renameFile throws on error", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({ ok: false, status: 409 });
    await expect(api.renameFile("u1", "a.txt", "b.txt")).rejects.toThrow("rename: 409");
  });

  it("extractArchive POSTs the archive path and returns the new dir path", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ path: "bundle", extracted: 3 }),
    });
    const res = await api.extractArchive("u1", "bundle.zip");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain("/api/users/u1/files/extract");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({ path: "bundle.zip" });
    expect(res).toEqual({ path: "bundle", extracted: 3 });
  });

  it("extractArchive surfaces the backend error message on failure", async () => {
    // The unsupported-format and zip-slip rejections both arrive as 400 with
    // a useful `error` field; a bare "extract: 400" would mask them.
    const api = useFileApi();
    mockFetch.mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({ error: "unsupported archive format" }),
    });
    await expect(api.extractArchive("u1", "foo.rar")).rejects.toThrow(/unsupported archive format/);
  });

  it("compressToZip POSTs the selection and returns the new archive path", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({
      ok: true,
      json: async () => ({ path: "Archive.zip", name: "Archive.zip" }),
    });
    const res = await api.compressToZip("u1", ["a.txt", "src"], ".");
    expect(mockFetch).toHaveBeenCalledTimes(1);
    const [url, opts] = mockFetch.mock.calls[0];
    expect(url).toContain("/api/users/u1/files/compress");
    expect(opts.method).toBe("POST");
    expect(JSON.parse(opts.body)).toEqual({
      paths: ["a.txt", "src"],
      target_dir: ".",
      name: "",
    });
    expect(res).toEqual({ path: "Archive.zip", name: "Archive.zip" });
  });

  it("compressToZip surfaces the backend error message on failure", async () => {
    const api = useFileApi();
    mockFetch.mockResolvedValue({
      ok: false,
      status: 404,
      json: async () => ({ error: "source not found: missing.txt" }),
    });
    await expect(api.compressToZip("u1", ["missing.txt"], ".")).rejects.toThrow(/source not found/);
  });
});

describe("isSupportedArchive", () => {
  it("matches open archive extensions (case-insensitive)", () => {
    expect(isSupportedArchive("foo.zip")).toBe(true);
    expect(isSupportedArchive("foo.ZIP")).toBe(true);
    expect(isSupportedArchive("foo.tar")).toBe(true);
    expect(isSupportedArchive("foo.tar.gz")).toBe(true);
    expect(isSupportedArchive("foo.tgz")).toBe(true);
    expect(isSupportedArchive("foo.tar.bz2")).toBe(true);
    expect(isSupportedArchive("foo.tbz2")).toBe(true);
  });

  it("rejects closed or non-archive formats", () => {
    expect(isSupportedArchive("foo.rar")).toBe(false);
    expect(isSupportedArchive("foo.7z")).toBe(false);
    expect(isSupportedArchive("foo.txt")).toBe(false);
    expect(isSupportedArchive("foo")).toBe(false);
  });
});

describe("writeFile optimistic lock", () => {
  it("sends If-Match when given a validator", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ path: "a.txt", size: 3, modified: "", etag: 'W/"new"' }),
    });

    await useFileApi().writeFile("u1", "a.txt", "abc", 'W/"old"');

    const init = mockFetch.mock.calls[0][1] as { headers: Record<string, string> };
    expect(init.headers["If-Match"]).toBe('W/"old"');
  });

  // Omitting the validator is how the UI says "overwrite anyway" — the header
  // must be absent, not empty, or the server would compare against "".
  it("omits If-Match when no validator is given", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ path: "a.txt", size: 3, modified: "", etag: 'W/"new"' }),
    });

    await useFileApi().writeFile("u1", "a.txt", "abc");

    const init = mockFetch.mock.calls[0][1] as { headers: Record<string, string> };
    expect(init.headers).not.toHaveProperty("If-Match");
  });

  it("turns a 412 into a WriteConflictError carrying the current validator", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 412,
      json: () => Promise.resolve({ error: "file was modified", etag: 'W/"theirs"' }),
    });

    await expect(useFileApi().writeFile("u1", "a.txt", "abc", 'W/"old"')).rejects.toMatchObject({
      etag: 'W/"theirs"',
    });
    await expect(useFileApi().writeFile("u1", "a.txt", "abc", 'W/"old"')).rejects.toBeInstanceOf(
      WriteConflictError,
    );
  });

  it("leaves other failures as plain errors", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 413,
      json: () => Promise.resolve({ error: "file too large" }),
    });

    await expect(useFileApi().writeFile("u1", "a.txt", "abc")).rejects.toThrow("file too large");
  });
});

describe("searchFiles", () => {
  it("defaults to a bare query and passes options through", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      json: () => Promise.resolve({ query: "x", mode: "name", truncated: false, results: [] }),
    });

    await useFileApi().searchFiles("u1", { query: "needle" });
    expect(mockFetch.mock.calls[0][0]).toContain("q=needle");

    mockFetch.mockClear();
    await useFileApi().searchFiles("u1", {
      query: "needle",
      mode: "content",
      path: "src",
      limit: 10,
    });
    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toContain("mode=content");
    expect(url).toContain("path=src");
    expect(url).toContain("limit=10");
  });
});

describe("shouldInspectTextFile", () => {
  it("only asks the backend for unknown or extensionless names", () => {
    expect(shouldInspectTextFile("README")).toBe(true);
    expect(shouldInspectTextFile("data.custom")).toBe(true);
    expect(shouldInspectTextFile("notes.md")).toBe(false);
    expect(shouldInspectTextFile("photo.png")).toBe(false);
    expect(shouldInspectTextFile("sheet.xlsx")).toBe(false);
  });
});

describe("preview URLs", () => {
  it("round-trips a workspace path through the path-shaped preview URL", () => {
    const { previewFileUrl } = useFileApi();
    const path = "site one/assets/app #2.wasm";
    const url = previewFileUrl("u1", path);
    expect(url).toContain("/api/users/u1/files/preview/");
    // Slashes stay structural; everything else is escaped.
    expect(url).toContain("site%20one/assets/app%20%232.wasm");
    expect(workspacePathFromPreviewUrl("u1", url)).toBe(path);
  });

  it("maps a resource the page loaded back to its file, and foreign URLs to null", () => {
    const { previewFileUrl } = useFileApi();
    const url = `${previewFileUrl("u1", "site/app.js")}?v=3#top`;
    expect(workspacePathFromPreviewUrl("u1", url)).toBe("site/app.js");
    expect(workspacePathFromPreviewUrl("u1", "https://cdn.example.com/lib.js")).toBeNull();
    expect(workspacePathFromPreviewUrl("u2", previewFileUrl("u1", "site/app.js"))).toBeNull();
  });
});

describe("isHtmlFile", () => {
  it("matches the files that open as a rendered page", () => {
    expect(isHtmlFile("site/index.html")).toBe(true);
    expect(isHtmlFile("SITE/INDEX.HTM")).toBe(true);
    expect(isHtmlFile("notes.md")).toBe(false);
    expect(isHtmlFile("template.html.tmpl")).toBe(false);
  });
});
