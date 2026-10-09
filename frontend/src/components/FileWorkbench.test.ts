import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { createI18n } from "vue-i18n";

import FileWorkbench from "./FileWorkbench.vue";
import en from "@/i18n/locales/en";
import { loadTabs } from "@/composables/editorTabStorage";

const previewStub = vi.hoisted(() => ({ dirty: false }));
vi.mock("@/components/FilePreview.vue", () => ({
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({
    name: "FilePreview",
    props: {
      userId: { type: String, required: true },
      path: { type: String, required: true },
      webView: { type: Boolean, default: true },
    },
    setup(props, { expose }) {
      expose({ refresh() {}, save: async () => true, isDirty: () => previewStub.dirty });
      return () =>
        h(
          "div",
          { "data-testid": "file-preview-stub", "data-web-view": String(props.webView) },
          "file-preview-stub",
        );
    },
  }),
}));

const editorStub = vi.hoisted(() => ({ closed: [] as string[] }));
vi.mock("@/components/FileTextEditor.vue", () => ({
  __isTeleport: false,
  __isKeepAlive: false,
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({
    name: "FileTextEditor",
    props: {
      userId: { type: String, required: true },
      path: { type: String, required: true },
      revealLine: { type: Number, default: 0 },
    },
    emits: ["preview", "dirtyPaths"],
    setup(props, { emit, expose }) {
      expose({
        closeFile(path: string) {
          editorStub.closed.push(path);
        },
      });
      return () =>
        h("div", { "data-testid": "file-text-editor-stub" }, [
          h("span", `editor:${props.userId}:${props.path}`),
          h(
            "button",
            {
              type: "button",
              "data-testid": "mark-dirty",
              onClick: () => emit("dirtyPaths", [props.path]),
            },
            "MarkDirty",
          ),
        ]);
    },
  }),
}));

vi.mock("@/components/FileQuickOpen.vue", () => ({
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({
    name: "FileQuickOpen",
    props: {
      userId: { type: String, required: true },
      open: { type: Boolean, default: false },
      initialMode: { type: String, default: "name" },
    },
    emits: ["update:open", "select"],
    setup(props) {
      return () =>
        props.open ? h("div", { "data-testid": "quick-open-stub" }, "quick-open") : null;
    },
  }),
}));

const confirmMock = vi.hoisted(() => ({ result: true, calls: 0 }));
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({
    confirm: async () => {
      confirmMock.calls++;
      return confirmMock.result;
    },
  }),
}));

// The disk-change watcher is stubbed here: these cases are about the file
// surface, not the sync. FileWorkbench.watch.test.ts drives the real
// composable through an injected transport.
vi.mock("@/composables/useFileWatch", () => ({
  useFileWatch: () => ({ mode: { value: "idle" }, stop: () => {} }),
}));

vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({
    inspectFile: vi.fn().mockResolvedValue({
      path: "README",
      name: "README",
      is_dir: false,
      size: 5,
      modified: "",
      content_type: "text/plain; charset=utf-8",
      is_text: true,
    }),
    downloadFileUrl: (_uid: string, path: string) => `/api/download?path=${path}`,
  }),
  isEditableText: (name: string) => {
    const lower = name.toLowerCase();
    const dot = lower.lastIndexOf(".");
    if (dot < 0) return false;
    return [".txt", ".md", ".json", ".ts"].includes(lower.slice(dot));
  },
  isHtmlFile: (name: string) => /\.html?$/i.test(name),
  shouldInspectTextFile: (name: string) => !name.includes("."),
}));

const i18n = createI18n({ legacy: false, locale: "en", messages: { en } });

type WorkbenchProps = InstanceType<typeof FileWorkbench>["$props"];

async function mountWorkbench(props: Omit<WorkbenchProps, "userId">) {
  const wrapper = mount(FileWorkbench, {
    props: { userId: "u1", ...props },
    global: { plugins: [i18n] },
  });
  await flushPromises();
  return wrapper;
}

describe("FileWorkbench", () => {
  beforeEach(() => {
    sessionStorage.clear();
    previewStub.dirty = false;
    editorStub.closed.length = 0;
    confirmMock.result = true;
    confirmMock.calls = 0;
  });

  it("renders the preview surface for a file that is not being edited", async () => {
    const wrapper = await mountWorkbench({ path: "docs/readme.md" });
    expect(wrapper.find('[data-testid="file-preview-stub"]').exists()).toBe(true);
    expect(wrapper.find("header").text()).toContain("readme.md");
  });

  it("switches to the editor when edit mode is requested for a text file", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    expect(wrapper.find('[data-testid="file-text-editor-stub"]').exists()).toBe(true);
  });

  it("hides its internal tabs when the artifact panel owns the tab strip", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true, showTabs: false });
    expect(wrapper.find('[data-testid="file-text-editor-stub"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="editor-tabs"]').exists()).toBe(false);
  });

  it("guards artifact navigation while the visible file is dirty", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true, showTabs: false });
    await wrapper.get('[data-testid="mark-dirty"]').trigger("click");
    confirmMock.result = false;

    const canLeave = await (
      wrapper.vm as unknown as { requestNavigateAway: () => Promise<boolean> }
    ).requestNavigateAway();

    expect(canLeave).toBe(false);
    expect(confirmMock.calls).toBe(1);
  });

  it("closes only the requested artifact buffer", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true, showTabs: false });

    const closed = await (
      wrapper.vm as unknown as { requestClosePath: (path: string) => Promise<boolean> }
    ).requestClosePath("a.txt");

    expect(closed).toBe(true);
    expect(editorStub.closed).toEqual(["a.txt"]);
    expect(wrapper.emitted("close")).toBeUndefined();
  });

  it("reports the resolved mode back to the host", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    const emitted = wrapper.emitted("update:editMode");
    expect(emitted?.at(-1)).toEqual([true]);
  });

  it("resolves a requested edit of a binary back to preview", async () => {
    // The host only *asks*; a file that cannot be edited must not leave the
    // workbench showing an editor over nothing.
    const wrapper = await mountWorkbench({ path: "photo.png", editMode: true });
    expect(wrapper.find('[data-testid="file-text-editor-stub"]').exists()).toBe(false);
    expect(wrapper.emitted("update:editMode")?.at(-1)).toEqual([false]);
  });

  it("opens a spreadsheet straight in edit mode without being asked", async () => {
    const wrapper = await mountWorkbench({ path: "sheet.xlsx" });
    expect(wrapper.emitted("update:editMode")?.at(-1)).toEqual([true]);
    // Edit-only, so no Edit button to switch into — but Download stays: the
    // embedded editor has no download of its own, and this is the only route
    // to the original bytes of a file it opens read-only.
    expect(wrapper.find("a[download]").exists()).toBe(true);
    expect(wrapper.text()).not.toContain("Edit");
  });

  it("opens a .docx in the document editor rather than a read-only render", async () => {
    const wrapper = await mountWorkbench({ path: "memo.docx" });
    expect(wrapper.emitted("update:editMode")?.at(-1)).toEqual([true]);
    // docs autosaves and takes no save command from the host, so offering a
    // Save button there would be a button that does nothing.
    expect(wrapper.text()).not.toContain("Save");
  });

  it("offers no Save for a macro workbook it refuses to write", async () => {
    const wrapper = await mountWorkbench({ path: "build.xlsm" });
    expect(wrapper.text()).not.toContain("Save");
  });

  it("emits update:editMode when the Edit button is clicked", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt" });
    const buttons = wrapper.findAll("button");
    const edit = buttons.find((b) => b.text() === "Edit");
    expect(edit).toBeDefined();
    await edit!.trigger("click");
    expect(wrapper.emitted("update:editMode")?.at(-1)).toEqual([true]);
  });

  it("opens an .html file as a rendered page, and switches it to source on request", async () => {
    const wrapper = await mountWorkbench({ path: "site/index.html" });
    expect(wrapper.find("[data-testid='file-preview-stub']").attributes("data-web-view")).toBe(
      "true",
    );

    await wrapper.find("[data-testid='html-view-source']").trigger("click");
    expect(wrapper.find("[data-testid='file-preview-stub']").attributes("data-web-view")).toBe(
      "false",
    );

    await wrapper.find("[data-testid='html-view-page']").trigger("click");
    expect(wrapper.find("[data-testid='file-preview-stub']").attributes("data-web-view")).toBe(
      "true",
    );
  });

  it("goes back to the page view when the next .html file opens", async () => {
    const wrapper = await mountWorkbench({ path: "site/index.html" });
    await wrapper.find("[data-testid='html-view-source']").trigger("click");

    await wrapper.setProps({ path: "site/other.html" });
    await flushPromises();
    expect(wrapper.find("[data-testid='file-preview-stub']").attributes("data-web-view")).toBe(
      "true",
    );
  });

  it("offers the page/source toggle only for html", async () => {
    const wrapper = await mountWorkbench({ path: "docs/readme.md" });
    expect(wrapper.find("[data-testid='html-view-toggle']").exists()).toBe(false);
  });

  it("renders the close button only when show-close is set", async () => {
    const without = await mountWorkbench({ path: "a.md" });
    expect(without.find('[data-testid="workbench-close"]').exists()).toBe(false);

    const withClose = await mountWorkbench({ path: "a.md", showClose: true });
    expect(withClose.find('[data-testid="workbench-close"]').exists()).toBe(true);
  });

  it("emits close when the X is clicked", async () => {
    const wrapper = await mountWorkbench({ path: "a.md", showClose: true });
    await wrapper.find('[data-testid="workbench-close"]').trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("keeps the workbench open when closing with unsaved text edits is cancelled", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true, showClose: true });
    await wrapper.find('[data-testid="mark-dirty"]').trigger("click");
    await flushPromises();

    confirmMock.result = false;
    await wrapper.find('[data-testid="workbench-close"]').trigger("click");
    await flushPromises();

    expect(confirmMock.calls).toBe(1);
    expect(wrapper.emitted("close")).toBeUndefined();
  });

  it("closes the workbench after unsaved text edits are confirmed", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true, showClose: true });
    await wrapper.find('[data-testid="mark-dirty"]').trigger("click");
    await flushPromises();

    await wrapper.find('[data-testid="workbench-close"]').trigger("click");
    await flushPromises();

    expect(confirmMock.calls).toBe(1);
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("guards closing a spreadsheet with unsaved changes", async () => {
    previewStub.dirty = true;
    confirmMock.result = false;
    const wrapper = await mountWorkbench({ path: "sheet.xlsx", showClose: true });

    await wrapper.find('[data-testid="workbench-close"]').trigger("click");
    await flushPromises();

    expect(confirmMock.calls).toBe(1);
    expect(wrapper.emitted("close")).toBeUndefined();
  });

  it("keeps a close affordance in edit mode, where the preview header is gone", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true, showClose: true });
    expect(wrapper.find('[data-testid="workbench-close"]').exists()).toBe(true);
  });

  it("emits open when a background tab is activated", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    await wrapper.setProps({ path: "b.txt" });
    await flushPromises();

    const labels = wrapper.findAll('[data-testid="editor-tab-label"]');
    expect(labels).toHaveLength(2);
    await labels[0].trigger("click");
    expect(wrapper.emitted("open")?.at(-1)).toEqual([{ path: "a.txt", line: 0 }]);
  });

  it("asks for confirmation before closing a dirty editor tab", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    await wrapper.find('[data-testid="mark-dirty"]').trigger("click");
    await flushPromises();

    confirmMock.result = false;
    await wrapper.find('[data-testid="editor-tab-close"]').trigger("click");
    await flushPromises();

    expect(confirmMock.calls).toBe(1);
    expect(editorStub.closed).toEqual([]);
    expect(wrapper.findAll('[data-testid="editor-tab"]')).toHaveLength(1);
  });

  it("releases the buffer when a dirty tab close is confirmed", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    await wrapper.find('[data-testid="mark-dirty"]').trigger("click");
    await flushPromises();

    confirmMock.result = true;
    await wrapper.find('[data-testid="editor-tab-close"]').trigger("click");
    await flushPromises();

    expect(editorStub.closed).toEqual(["a.txt"]);
  });

  it("scopes editor tab storage so two mounts don't share tabs", async () => {
    await mountWorkbench({ path: "a.txt", editMode: true, storageScope: "chat" });
    await flushPromises();

    expect(loadTabs("chat", "u1")).toEqual(["a.txt"]);
    expect(loadTabs("preview", "u1")).toEqual([]);
  });

  it("omits quick open when the host disables it", async () => {
    const wrapper = await mountWorkbench({ path: "a.md", enableQuickOpen: false });
    // Cmd+P must not open a picker the host said it doesn't want.
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "p", metaKey: true }));
    await flushPromises();
    expect(wrapper.find('[data-testid="quick-open-stub"]').exists()).toBe(false);
  });

  it("shows the empty state when there is no path", async () => {
    const wrapper = await mountWorkbench({ path: "" });
    expect(wrapper.text()).toContain(en.files.preview.noPath);
  });
});
