import { describe, it, expect, vi, beforeEach } from "vitest";

const { mockReadFile, mockInspectFile } = vi.hoisted(() => ({
  mockReadFile: vi.fn(),
  mockInspectFile: vi.fn(),
}));

vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({
    inspectFile: mockInspectFile,
    readFile: mockReadFile,
    downloadFileUrl: (_uid: string, path: string) => `/api/download?path=${path}`,
    readFileUrl: (_uid: string, path: string) => `/api/read?path=${path}`,
  }),
  isHtmlFile: (name: string) => /\.html?$/i.test(name),
  shouldInspectTextFile: (name: string) =>
    ![".png", ".mp3", ".mp4", ".pdf", ".docx", ".pptx", ".xlsx"].some((ext) => name.endsWith(ext)),
}));

// The pptx path fetches bytes before handing them to its renderer.
vi.mock("@/composables/apiClient", () => ({
  apiFetch: vi.fn().mockResolvedValue(new Response(new ArrayBuffer(8))),
}));

vi.mock("pptx-preview", () => ({
  init: vi.fn(() => ({
    preview: vi.fn().mockResolvedValue(undefined),
  })),
}));

// The office editor is a 12MB iframe embed; stub it down to a marker so this
// test stays about which surface a path is routed to.
vi.mock("./OfficePane.vue", () => ({
  __isKeepAlive: false,
  __isTeleport: false,
  __isSuspense: false,
  name: "OfficePane",
  default: {
    name: "OfficePane",
    props: ["userId", "path", "app"],
    template: "<div>office editor: {{ app }}</div>",
  },
}));

// The rendered-page surface has its own test; here we only care that an
// .html file is routed to it.
vi.mock("./HtmlPreview.vue", () => ({
  __isKeepAlive: false,
  __isTeleport: false,
  __isSuspense: false,
  name: "HtmlPreview",
  default: {
    name: "HtmlPreview",
    props: ["userId", "path", "reloadToken"],
    template: "<div data-testid='html-preview-frame'>page: {{ path }}</div>",
  },
}));

vi.mock("@/composables/useMarkdown", () => ({
  renderMarkdown: (t: string) => `<p>${t}</p>`,
}));

// Must import after mocks
const { detectTypeExported } = await (async () => {
  // We test detectType indirectly via the component, but let's also test directly
  // by importing the component and inspecting its behavior
  return { detectTypeExported: null };
})();

// Since detectType is not exported, we test it via component mounting
import { flushPromises, mount } from "@vue/test-utils";
import FilePreview from "./FilePreview.vue";

void detectTypeExported; // silence unused

function mountPreview(path: string, webView?: boolean) {
  return mount(FilePreview, {
    props: { userId: "u1", path, ...(webView === undefined ? {} : { webView }) },
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  mockReadFile.mockReset();
  mockInspectFile.mockReset();
  mockReadFile.mockResolvedValue(new Response("hello"));
  mockInspectFile.mockResolvedValue({
    path: "file.custom",
    name: "file.custom",
    is_dir: false,
    size: 5,
    modified: "",
    content_type: "text/plain; charset=utf-8",
    is_text: true,
  });
});

describe("FilePreview", () => {
  it("detects image types", () => {
    const wrapper = mountPreview("photo.png");
    // Should render image element
    expect(wrapper.find("img").exists()).toBe(true);
  });

  it("detects audio types", () => {
    const wrapper = mountPreview("song.mp3");
    expect(wrapper.find("audio").exists()).toBe(true);
  });

  it("detects video types", () => {
    const wrapper = mountPreview("clip.mp4");
    expect(wrapper.find("video").exists()).toBe(true);
  });

  it("detects pdf type and serves it via the inline (read) endpoint", () => {
    const wrapper = mountPreview("doc.pdf");
    const iframe = wrapper.find("iframe");
    expect(iframe.exists()).toBe(true);
    // PDF must NOT use /download (which sends Content-Disposition: attachment)
    // — that's what was breaking inline preview.
    expect(iframe.attributes("src")).toContain("/api/read?path=doc.pdf");
  });

  it("renders an .html file as a page by default", async () => {
    const wrapper = mountPreview("site/index.html");
    await flushPromises();
    expect(wrapper.find("[data-testid='html-preview-frame']").exists()).toBe(true);
  });

  it("shows the source of an .html file when the host turns the page view off", async () => {
    const wrapper = mountPreview("site/index.html", false);
    await flushPromises();
    expect(wrapper.find("[data-testid='html-preview-frame']").exists()).toBe(false);
    expect(wrapper.find("pre code").exists()).toBe(true);
  });

  it("opens a .docx in the document editor", async () => {
    const wrapper = mountPreview("report.docx");
    await flushPromises();
    expect(wrapper.text()).toContain("office editor: docs");
  });

  it("detects pptx type", () => {
    const wrapper = mountPreview("slides.pptx");
    expect(wrapper.text()).toContain("Loading");
  });

  it.each(["data.xlsx", "data.xls", "data.ods", "data.xlsm"])(
    "opens %s in the spreadsheet editor",
    async (path) => {
      const wrapper = mountPreview(path);
      await flushPromises();
      expect(wrapper.text()).toContain("office editor: sheet");
    },
  );

  it("opens a .csv in the spreadsheet editor without reading it as text", async () => {
    const wrapper = mountPreview("data.csv");
    await flushPromises();
    expect(wrapper.text()).toContain("office editor: sheet");
    expect(mockReadFile).not.toHaveBeenCalled();
  });

  it("detects Go module files as code", async () => {
    const wrapper = mountPreview("go.mod");
    await flushPromises();
    expect(wrapper.find("pre code").exists()).toBe(true);
  });

  it("previews backend-detected text for unsupported extension", async () => {
    const wrapper = mountPreview("file.xyz");
    await flushPromises();
    expect(wrapper.find("pre").text()).toContain("hello");
  });

  it("previews backend-detected text for files without an extension", async () => {
    const wrapper = mountPreview("README");
    await flushPromises();
    expect(wrapper.find("pre").text()).toContain("hello");
  });

  it("shows the newest file's text when an earlier read resolves late", async () => {
    const slow = deferred<Response>();
    mockReadFile.mockReturnValueOnce(slow.promise).mockResolvedValueOnce(new Response("second"));

    const wrapper = mountPreview("first.txt");
    await wrapper.setProps({ path: "second.txt" });
    await flushPromises();
    slow.resolve(new Response("first"));
    await flushPromises();

    expect(wrapper.find("pre").text()).toContain("second");
    expect(wrapper.find("pre").text()).not.toContain("first");
  });

  it("ignores a read failure belonging to a file the user already switched away from", async () => {
    const slow = deferred<Response>();
    mockReadFile.mockReturnValueOnce(slow.promise).mockResolvedValueOnce(new Response("second"));

    const wrapper = mountPreview("first.txt");
    await wrapper.setProps({ path: "second.txt" });
    await flushPromises();
    slow.reject(new Error("read: 404"));
    await flushPromises();

    expect(wrapper.text()).not.toContain("read: 404");
    expect(wrapper.find("pre").text()).toContain("second");
  });
});
