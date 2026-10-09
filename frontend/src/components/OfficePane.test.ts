import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

import OfficePane from "./OfficePane.vue";
import { i18n } from "@/i18n";

vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({
    readFileUrl: (_uid: string, path: string) => `/api/read?path=${path}`,
    uploadFiles: vi.fn(),
  }),
}));
vi.mock("@/composables/apiClient", () => ({ apiFetch: vi.fn() }));

function mountPane(path: string, app: "sheet" | "docs" = "sheet") {
  return mount(OfficePane, { props: { userId: "u1", path, app } });
}

function src(wrapper: ReturnType<typeof mountPane>): string {
  return wrapper.find("iframe").attributes("src") ?? "";
}

describe("OfficePane", () => {
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
