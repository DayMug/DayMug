import { beforeEach, describe, expect, it, vi } from "vitest";

import { OfficeHost, type OfficeFileGateway, type OfficeHostOptions } from "./host";

// The transport is the iframe postMessage bridge. Stubbing it lets the test
// play the editor: call the handlers the host registered and inspect what it
// sends back, with no iframe and no message plumbing involved.
const handlers: {
  onEditorReady?: () => void;
  onLoadRequest?: () => Promise<unknown>;
  onSaveRequest?: (data: { bytes: ArrayBuffer }) => Promise<unknown>;
  onError?: (data: { code?: string; message?: string }) => void;
} = {};
const sendHostHello = vi.fn();
const sendSetTheme = vi.fn();
const destroy = vi.fn();

vi.mock("@casualoffice/sheets/embed", () => ({
  EmbedHostTransport: class {
    on(h: typeof handlers) {
      Object.assign(handlers, h);
    }
    sendHostHello = sendHostHello;
    sendSetTheme = sendSetTheme;
    destroy = destroy;
  },
}));

function bytes(text: string): ArrayBuffer {
  const b = new TextEncoder().encode(text);
  return b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer;
}

/** Stands in for the editor API the embed publishes on its iframe window. */
function fakeApi(overrides: Record<string, unknown> = {}) {
  return { exportXlsx: vi.fn(async () => new Blob([new Uint8Array([1, 2, 3])])), ...overrides };
}

/** A window carrying that API, as the same-origin iframe would expose it. */
function windowWith(api: object): Window {
  return { __casualEmbedApi: api } as unknown as Window;
}

function makeHost(overrides: Partial<OfficeHostOptions> = {}) {
  const files: OfficeFileGateway = {
    read: vi.fn(async () => bytes("original")),
    write: vi.fn(async () => {}),
  };
  const opts = {
    app: "sheet" as const,
    path: "book.xlsx",
    files,
    iframeWindow: {
      addEventListener: () => {},
      removeEventListener: () => {},
    } as unknown as Window,
    origin: "http://localhost",
    confirmLossy: vi.fn(async () => true),
    onReady: vi.fn(),
    onSaved: vi.fn(),
    onError: vi.fn(),
    ...overrides,
  };
  return { host: new OfficeHost(opts), opts, files };
}

beforeEach(() => {
  for (const key of Object.keys(handlers)) delete (handlers as Record<string, unknown>)[key];
  vi.clearAllMocks();
});

describe("handshake", () => {
  it("announces load and save once the editor is up", () => {
    const { opts } = makeHost();
    handlers.onEditorReady?.();
    expect(sendHostHello).toHaveBeenCalledWith({ capabilities: ["load", "save"] });
    expect(opts.onReady).toHaveBeenCalled();
  });
});

describe("loading", () => {
  it("hands the editor the file's bytes and its name", async () => {
    const { files } = makeHost({ path: "reports/q3.xlsx" });
    const result = (await handlers.onLoadRequest?.()) as { ok: boolean; fileName: string };
    expect(files.read).toHaveBeenCalledWith("reports/q3.xlsx");
    expect(result.ok).toBe(true);
    expect(result.fileName).toBe("q3.xlsx");
  });

  it("reports a failed read to the editor rather than throwing", async () => {
    // A thrown error here would leave the iframe hanging on a request that
    // never gets an answer, i.e. a permanently blank editor.
    const { opts } = makeHost({
      files: {
        read: vi.fn(async () => {
          throw new Error("403");
        }),
        write: vi.fn(async () => {}),
      },
    });
    const result = (await handlers.onLoadRequest?.()) as { ok: boolean; code: string };
    expect(result).toMatchObject({ ok: false, code: "load_failed" });
    expect(opts.onError).toHaveBeenCalled();
  });
});

describe("saving", () => {
  it("exports a spreadsheet through the in-iframe API, not the protocol", async () => {
    // The sheets embed defines requestSave and never calls it, so nothing
    // arrives via onSaveRequest; exportXlsx on the iframe's own API object is
    // the only route to the bytes.
    const api = fakeApi();
    const { host, files } = makeHost({ iframeWindow: windowWith(api) });

    await expect(host.save()).resolves.toBe(true);
    expect(api.exportXlsx).toHaveBeenCalled();
    expect(files.write).toHaveBeenCalledWith(
      "book.xlsx",
      expect.anything(),
      "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    );
  });

  it("fails loudly when the embed exposes no export API", async () => {
    // That API is not part of the documented protocol, so an upgrade could
    // drop it. Silently saving nothing would lose the user's work.
    const { host, opts, files } = makeHost();
    await expect(host.save()).resolves.toBe(false);
    expect(opts.onError).toHaveBeenCalled();
    expect(files.write).not.toHaveBeenCalled();
  });

  it("writes docx bytes the document editor pushes on its own", async () => {
    const { files } = makeHost({ app: "docs", path: "memo.docx" });
    await handlers.onSaveRequest?.({ bytes: bytes("new") });
    expect(files.write).toHaveBeenCalledWith(
      "memo.docx",
      expect.anything(),
      "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    );
  });

  it("treats a document as already saved — it autosaves", async () => {
    // Reporting failure here would show a save error for a file that is in
    // fact on disk.
    const { host, files } = makeHost({ app: "docs", path: "memo.docx" });
    await expect(host.save()).resolves.toBe(true);
    expect(files.write).not.toHaveBeenCalled();
  });

  it("reports a failed write rather than claiming success", async () => {
    const { host, opts } = makeHost({
      iframeWindow: windowWith(fakeApi()),
      files: {
        read: vi.fn(async () => bytes("x")),
        write: vi.fn(async () => {
          throw new Error("disk full");
        }),
      },
    });
    await expect(host.save()).resolves.toBe(false);
    expect(opts.onError).toHaveBeenCalled();
  });

  it("does not export after the host is torn down", async () => {
    const api = fakeApi();
    const { host } = makeHost({ iframeWindow: windowWith(api) });
    host.destroy();
    await expect(host.save()).resolves.toBe(false);
    expect(api.exportXlsx).not.toHaveBeenCalled();
  });
});

describe("unsaved-work tracking", () => {
  /** A stand-in for the same-origin iframe window the host listens on. */
  function trackingWindow(api: object = fakeApi()) {
    const listeners: Record<string, Set<() => void>> = {};
    const win = {
      __casualEmbedApi: api,
      addEventListener: (event: string, h: () => void) => {
        (listeners[event] ??= new Set()).add(h);
      },
      removeEventListener: (event: string, h: () => void) => listeners[event]?.delete(h),
    };
    return {
      win: win as unknown as Window,
      fire: (event: string) => listeners[event]?.forEach((h) => h()),
      listening: () => Object.keys(listeners),
    };
  }

  it("treats an untouched spreadsheet as clean", () => {
    // Opening a file to look at it must not provoke a "discard changes?"
    // prompt on the way out.
    const { win } = trackingWindow();
    const { host } = makeHost({ iframeWindow: win });
    handlers.onEditorReady?.();
    expect(host.isDirty()).toBe(false);
  });

  it("treats input inside the editor as possible unsaved work", () => {
    const { win, fire } = trackingWindow();
    const { host } = makeHost({ iframeWindow: win });
    handlers.onEditorReady?.();
    fire("keydown");
    expect(host.isDirty()).toBe(true);
  });

  it("answers the handshake once, so the editor's reply cannot loop it", () => {
    // The embed replies to casual.hello with another casual.ready. Greeting
    // it back every time is an endless postMessage ping-pong.
    const { win } = trackingWindow();
    makeHost({ iframeWindow: win });
    handlers.onEditorReady?.();
    handlers.onEditorReady?.();
    handlers.onEditorReady?.();
    expect(sendHostHello).toHaveBeenCalledTimes(1);
  });

  it("clears the flag once a save lands", async () => {
    const { win, fire } = trackingWindow();
    const { host } = makeHost({ iframeWindow: win });
    handlers.onEditorReady?.();
    fire("pointerdown");
    await expect(host.save()).resolves.toBe(true);
    expect(host.isDirty()).toBe(false);
  });

  it("watches nothing for a view-only workbook", () => {
    // .xlsm cannot be edited, so a prompt about unsaved work would be noise.
    const { win, listening } = trackingWindow();
    const { host } = makeHost({ path: "build.xlsm", iframeWindow: win });
    handlers.onEditorReady?.();
    expect(listening()).toEqual([]);
    expect(host.isDirty()).toBe(false);
  });

  it("reports a document as never dirty — it autosaves", () => {
    const { win, listening } = trackingWindow();
    const { host } = makeHost({ app: "docs", path: "memo.docx", iframeWindow: win });
    handlers.onEditorReady?.();
    expect(listening()).toEqual([]);
    expect(host.isDirty()).toBe(false);
  });
});

describe("csv", () => {
  it("warns once before a lossy save, then stops asking", async () => {
    const { opts } = makeHost({ path: "data.csv" });
    await handlers.onSaveRequest?.({ bytes: await xlsxBytes() });
    await handlers.onSaveRequest?.({ bytes: await xlsxBytes() });
    expect(opts.confirmLossy).toHaveBeenCalledTimes(1);
  });

  it("abandons the save when the warning is declined", async () => {
    const { files } = makeHost({ path: "data.csv", confirmLossy: vi.fn(async () => false) });
    const result = (await handlers.onSaveRequest?.({ bytes: await xlsxBytes() })) as {
      ok: boolean;
      code: string;
    };
    expect(result).toMatchObject({ ok: false, code: "cancelled" });
    expect(files.write).not.toHaveBeenCalled();
  });

  it("writes csv bytes back, not the editor's xlsx", async () => {
    const { files } = makeHost({ path: "data.csv" });
    await handlers.onSaveRequest?.({ bytes: await xlsxBytes() });
    const [, payload, contentType] = (files.write as ReturnType<typeof vi.fn>).mock.calls[0]!;
    expect(contentType).toBe("text/csv;charset=utf-8");
    expect(new TextDecoder().decode(payload as ArrayBuffer)).toBe("a,b\r\n1,2\r\n");
  });
});

/** A real one-sheet xlsx, because the csv path actually parses what it gets. */
async function xlsxBytes(): Promise<ArrayBuffer> {
  const ExcelJS = (await import("exceljs/dist/exceljs.min.js")).default;
  const workbook = new ExcelJS.Workbook();
  const sheet = workbook.addWorksheet("Sheet1");
  sheet.addRow(["a", "b"]);
  sheet.addRow([1, 2]);
  const buffer = await workbook.xlsx.writeBuffer();
  const view = new Uint8Array(buffer);
  return view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength) as ArrayBuffer;
}
