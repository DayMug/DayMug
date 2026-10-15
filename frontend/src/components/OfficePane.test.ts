import { beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

import OfficePane from "./OfficePane.vue";
import { i18n } from "@/i18n";
import type { OfficeFileGateway, OfficeHostOptions } from "@/lib/office/host";

const mocks = vi.hoisted(() => ({
  files: null as OfficeFileGateway | null,
  fetch: vi.fn(),
  upload: vi.fn(),
}));
vi.mock("@/lib/office/host", () => ({
  OfficeHost: class {
    constructor(opts: OfficeHostOptions) {
      mocks.files = opts.files;
    }
    destroy() {}
    setTheme() {}
  },
}));

vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({
    readFileUrl: (_uid: string, path: string) => `/api/read?path=${path}`,
    uploadFiles: mocks.upload,
  }),
}));
vi.mock("@/composables/apiClient", () => ({ apiFetch: mocks.fetch }));

function bytes(text: string): ArrayBuffer {
  return Uint8Array.from(text, (char) => char.charCodeAt(0)).buffer;
}

beforeEach(() => {
  mocks.files = null;
  mocks.fetch.mockReset();
  mocks.upload.mockReset().mockResolvedValue([]);
});

function mountPane(path: string, app: "sheet" | "docs" = "sheet") {
  return mount(OfficePane, { props: { userId: "u1", path, app } });
}

function src(wrapper: ReturnType<typeof mountPane>): string {
  return wrapper.find("iframe").attributes("src") ?? "";
}

describe("OfficePane", () => {
  it("compares disk bytes without reloading the embedded editor", async () => {
    const wrapper = mountPane("book.xlsx");
    const frame = wrapper.find("iframe");
    Object.defineProperty(frame.element, "contentWindow", { value: window, configurable: true });
    await frame.trigger("load");
    mocks.fetch.mockResolvedValueOnce(new Response(bytes("original")));
    await mocks.files!.read("book.xlsx");
    mocks.fetch.mockResolvedValueOnce(new Response(bytes("other editor")));
    const api = wrapper.vm as unknown as {
      checkDiskChange: () => Promise<boolean>;
      keepLocalVersion: () => void;
    };
    expect(await api.checkDiskChange()).toBe(true);
    // Autosave must wait for the conflict decision, too.
    await expect(
      mocks.files!.write("book.xlsx", bytes("mine"), "application/octet-stream"),
    ).rejects.toThrow();
    expect(mocks.upload).not.toHaveBeenCalled();
    api.keepLocalVersion();
    mocks.fetch.mockResolvedValueOnce(new Response(bytes("other editor")));
    expect(await api.checkDiskChange()).toBe(false);
    await mocks.files!.write("book.xlsx", bytes("mine"), "application/octet-stream");
    expect(mocks.upload).toHaveBeenCalledOnce();
    wrapper.unmount();
  });

  it("ignores an office save's watch event while its upload is still finishing", async () => {
    const wrapper = mountPane("book.xlsx");
    const frame = wrapper.find("iframe");
    Object.defineProperty(frame.element, "contentWindow", { value: window, configurable: true });
    await frame.trigger("load");
    mocks.fetch.mockResolvedValueOnce(new Response(bytes("original")));
    await mocks.files!.read("book.xlsx");
    let finish!: () => void;
    mocks.upload.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        finish = resolve;
      }),
    );
    const write = mocks.files!.write("book.xlsx", bytes("mine"), "application/octet-stream");
    mocks.fetch.mockResolvedValueOnce(new Response(bytes("mine")));
    const check = (
      wrapper.vm as unknown as { checkDiskChange: () => Promise<boolean> }
    ).checkDiskChange();
    finish();
    await write;
    expect(await check).toBe(false);
    expect(wrapper.emitted("saved")).toHaveLength(1);
    wrapper.unmount();
  });

  it("opens an editable workbook in editor mode", () => {
    // preview mode is the embed's default and does more than grey out edits:
    // it also hides the menu bar and the sheet tab strip, so a multi-sheet
    // workbook would look like it has one sheet.
    expect(src(mountPane("book.xlsx"))).toContain("viewMode=editor");
  });

  it.each(["build.xlsm", "old.xls", "open.ods"])("opens %s in preview mode", (path) => {
    // The only reliable read-only switch: the embed registers no handler for
    // the protocol's set.readonly command.
    expect(src(mountPane(path))).toContain("viewMode=preview");
  });

  it("passes the path to the document editor as its docId", () => {
    // docs reads app / docId / viewMode from window.location.search and has
    // no other configuration channel.
    const url = new URL(src(mountPane("notes/memo.docx", "docs")), "http://localhost");
    expect(url.searchParams.get("app")).toBe("docs");
    expect(url.searchParams.get("docId")).toBe("notes/memo.docx");
  });

  it("points at the pinned version directory", () => {
    // The runtime resolves its workers by relative URL, so the filenames
    // cannot be content-hashed; the version directory is what makes an
    // upgrade produce fresh, uncached URLs.
    expect(src(mountPane("book.xlsx"))).toMatch(/^\/casual-office\/sheets\/\d+\.\d+\.\d+\//);
  });

  it("loads the language-matched bundle, because the UI language is baked in", async () => {
    // sheets packages only EN_US, so Chinese exists solely as a separately
    // patched build — picking it is a different file, not a runtime setting.
    expect(src(mountPane("book.xlsx"))).toContain("/embed.html");

    const previous = i18n.global.locale.value;
    i18n.global.locale.value = "zh";
    try {
      const wrapper = mountPane("book.xlsx");
      await wrapper.vm.$nextTick();
      expect(src(wrapper)).toContain("/embed.zh.html");
    } finally {
      i18n.global.locale.value = previous;
    }
  });

  it("falls back to English for a locale with no baked bundle", async () => {
    const previous = i18n.global.locale.value;
    i18n.global.locale.value = "fr";
    try {
      const wrapper = mountPane("book.xlsx");
      await wrapper.vm.$nextTick();
      expect(src(wrapper)).toContain("/embed.html");
    } finally {
      i18n.global.locale.value = previous;
    }
  });
});
