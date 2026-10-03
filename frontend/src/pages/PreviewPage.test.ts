import { beforeEach, describe, it, expect, vi } from "vitest";
import { defineComponent, h, nextTick } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import PreviewPage from "./PreviewPage.vue";

// Replace FilePreview with a lightweight stub whose unsaved-changes flag the
// test can flip, so the beforeunload guard runs without the Univer grid. A
// hoisted holder is the only state vi.mock's factory may legally reference.
const previewStub = vi.hoisted(() => ({ dirty: false }));
const mocks = vi.hoisted(() => ({
  listDir: vi.fn(),
}));
vi.mock("@/components/FilePreview.vue", () => ({
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({
    name: "FilePreview",
    setup(_, { expose }) {
      expose({ refresh() {}, save: async () => true, isDirty: () => previewStub.dirty });
      return () => h("div", "file-preview-stub");
    },
  }),
}));

// Records which buffers the page asked the editor to drop, so the tab tests
// can assert the model actually gets released rather than just delisted.
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
          h("button", { type: "button", onClick: () => emit("preview") }, "Preview"),
          h(
            "button",
            { type: "button", onClick: () => emit("dirtyPaths", [props.path]) },
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
        props.open
          ? h("div", { "data-testid": "quick-open-stub" }, `mode:${props.initialMode}`)
          : null;
    },
  }),
}));

vi.mock("@/components/WorkspacePanel.vue", () => ({
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({
    name: "WorkspacePanel",
    props: {
      userId: { type: String, required: true },
      initialPath: { type: String, default: "." },
      canCollapse: { type: Boolean, default: false },
      fileOpenTarget: { type: String, default: "new-tab" },
    },
    emits: ["collapse"],
    setup(props, { emit }) {
      return () =>
        h(
          "div",
          { "data-testid": "workspace-panel-stub", "data-can-collapse": props.canCollapse },
          [
            h("span", `workspace:${props.userId}:${props.initialPath}:${props.fileOpenTarget}`),
            props.canCollapse
              ? h(
                  "button",
                  {
                    type: "button",
                    "data-testid": "workspace-collapse",
                    onClick: () => emit("collapse"),
                  },
                  "collapse",
                )
              : null,
          ],
        );
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
  // Re-exported alongside useFileApi; the preview surface and the open-file
  // routing import it directly, so the mock has to carry it too.
  isHtmlFile: (name: string) => /\.html?$/i.test(name),
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
    readFile: vi.fn().mockResolvedValue(new Response("test")),
    listDir: mocks.listDir,
    downloadFileUrl: (_uid: string, path: string) => `/api/download?path=${path}`,
  }),
  // Re-exported alongside useFileApi in the real module; PreviewPage
  // imports it directly to gate the Edit button, so the mock must
  // surface it or the component crashes during render.
  isEditableText: (name: string) => {
    const lower = name.toLowerCase();
    const base = lower.split("/").pop() ?? "";
    if (base === "go.mod" || base === "go.sum") return true;
    const dot = lower.lastIndexOf(".");
    if (dot < 0) return lower === "makefile" || lower === "dockerfile";
    return [".txt", ".md", ".markdown", ".json", ".js", ".ts"].includes(lower.slice(dot));
  },
  shouldInspectTextFile: (name: string) => !name.includes("."),
}));

vi.mock("@/composables/useMarkdown", () => ({
  renderMarkdown: (t: string) => `<p>${t}</p>`,
}));

async function mountPage(path: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
  });
  await router.push({ name: "file", params: { userId: "u1" }, query: { path } });
  await router.isReady();
  return mount(PreviewPage, {
    props: { userId: "u1" },
    global: { plugins: [router] },
  });
}

describe("PreviewPage", () => {
  beforeEach(() => {
    localStorage.removeItem("daymug.preview.filePanelWidth");
    localStorage.removeItem("daymug.preview.filePanelCollapsed");
    localStorage.removeItem("daymug.preview.filePanelOpen");
    previewStub.dirty = false;
    editorStub.closed.length = 0;
    sessionStorage.clear();
    mocks.listDir.mockResolvedValue({
      path: "docs",
      entries: [
        { name: "readme.md", is_dir: false, size: 1, modified: "" },
        { name: "notes.md", is_dir: false, size: 1, modified: "" },
        { name: "archive", is_dir: true, size: 0, modified: "" },
      ],
    });
  });

  it("renders filename in header", async () => {
    const wrapper = await mountPage("docs/readme.md");
    expect(wrapper.find("header").text()).toContain("readme.md");
  });

  it("renders download button", async () => {
    const wrapper = await mountPage("test.txt");
    expect(wrapper.find("a[download]").exists()).toBe(true);
  });

  it("lazy-loads the text editor only when edit=true", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "go.mod", edit: "true" },
    });
    await router.isReady();

    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    await flushPromises();

    expect(wrapper.find('[data-testid="file-text-editor-stub"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="file-text-editor-stub"]').text()).toContain(
      "editor:u1:go.mod",
    );
    expect(wrapper.text()).not.toContain("file-preview-stub");
    expect(router.currentRoute.value.query.edit).toBe("true");
  });

  it("enters edit mode for backend-detected extensionless text with edit=true", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "README", edit: "true" },
    });
    await router.isReady();

    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    await flushPromises();

    expect(wrapper.find('[data-testid="file-text-editor-stub"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="file-text-editor-stub"]').text()).toContain(
      "editor:u1:README",
    );
  });

  it("keeps edit mode when navigating to another editable file", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "docs/a.md", edit: "true" },
    });
    await router.isReady();
    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    await flushPromises();

    await router.push({ query: { path: "docs/b.md", edit: "true" } });
    await flushPromises();

    expect(wrapper.find('[data-testid="file-text-editor-stub"]').text()).toContain(
      "editor:u1:docs/b.md",
    );
    expect(router.currentRoute.value.query.edit).toBe("true");
  });

  it("renders the workspace file panel at the current file folder", async () => {
    const wrapper = await mountPage("docs/readme.md");
    expect(wrapper.find('[data-testid="preview-file-panel"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="workspace-panel-stub"]').text()).toContain(
      "workspace:u1:docs:same-tab",
    );
    expect(
      wrapper.find('[data-testid="workspace-panel-stub"]').attributes("data-can-collapse"),
    ).toBe("true");
    expect(wrapper.find('[data-testid="preview-file-panel-resize-handle"]').exists()).toBe(true);
  });

  it("keeps the file panel root put when another file opens in the same tab", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({ name: "file", params: { userId: "u1" }, query: { path: "docs/a.md" } });
    await router.isReady();
    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    await flushPromises();

    // List view opens files from folders the panel hasn't navigated into; the
    // panel must stay where the user left it instead of following the file.
    await router.push({ query: { path: "docs/archive/deep/nested.md" } });
    await flushPromises();

    expect(wrapper.find('[data-testid="workspace-panel-stub"]').text()).toContain(
      "workspace:u1:docs:same-tab",
    );
  });

  it("keeps the workspace file panel visible even if old local state hid it", async () => {
    localStorage.setItem("daymug.preview.filePanelOpen", "0");
    const reopened = await mountPage("docs/readme.md");
    expect(reopened.find('[data-testid="preview-file-panel"]').exists()).toBe(true);
    reopened.unmount();
  });

  it("collapses and expands the workspace file panel from the preview page", async () => {
    const wrapper = await mountPage("docs/readme.md");
    await wrapper.find('[data-testid="workspace-collapse"]').trigger("click");

    expect(wrapper.find('[data-testid="preview-file-panel-resize-handle"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="preview-file-panel"]').attributes("style")).toContain(
      "width: 0px",
    );
    expect(wrapper.find('[data-testid="preview-file-panel-expand"]').exists()).toBe(true);

    await wrapper.find('[data-testid="preview-file-panel-expand"]').trigger("click");
    expect(wrapper.find('[data-testid="preview-file-panel-resize-handle"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="preview-file-panel"]').attributes("style")).toContain(
      "width: 340px",
    );
  });

  it("resizes the workspace file panel with the same drag handle pattern as chat", async () => {
    const wrapper = await mountPage("docs/readme.md");
    const layout = wrapper.find('[data-testid="preview-layout"]').element as HTMLElement;
    layout.getBoundingClientRect = () =>
      ({
        left: 0,
        right: 1000,
        top: 0,
        bottom: 600,
        width: 1000,
        height: 600,
        x: 0,
        y: 0,
        toJSON: () => {},
      }) as DOMRect;

    await wrapper.find('[data-testid="preview-file-panel-resize-handle"]').trigger("mousedown");
    document.dispatchEvent(new MouseEvent("mousemove", { clientX: 600 }));
    await nextTick();

    expect(wrapper.find('[data-testid="preview-file-panel"]').attributes("style")).toContain(
      "width: 400px",
    );

    document.dispatchEvent(new MouseEvent("mouseup"));
  });

  it("renders the file-panel divider as a one-pixel rule", async () => {
    const wrapper = await mountPage("docs/readme.md");
    const handle = wrapper.find('[data-testid="preview-file-panel-resize-handle"]');

    expect(handle.classes()).toContain("w-px");
    expect(handle.classes()).not.toContain("w-1.5");
    expect(wrapper.find('[data-testid="preview-file-panel"]').classes()).toContain("md:border-l-0");
  });

  it("gives the one-pixel divider a wider raised grab band", async () => {
    // The band overflows the 1px handle, so without z-10 the file panel
    // (a later sibling) paints over its right half and half the grab area
    // silently disappears.
    const wrapper = await mountPage("docs/readme.md");
    const band = wrapper.find('[data-testid="preview-file-panel-resize-handle"] span');

    expect(band.exists()).toBe(true);
    expect(band.classes()).toContain("w-3");
    expect(band.classes()).toContain("z-10");
    expect(band.classes()).toContain("touch-none");
  });

  it("matches the preview header height to the workspace action rail", async () => {
    // Both bars are border-box `h-11`; a `py-*` header would render a different
    // height and the two bottom borders would visibly step across the divider.
    const wrapper = await mountPage("docs/readme.md");
    const header = wrapper.find("header");

    expect(header.classes()).toContain("h-11");
    expect(header.classes()).not.toContain("py-2");
  });

  it("overlays save controls on the ribbon row in spreadsheet edit mode", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "data/sheet.xlsx", edit: "true" },
    });
    await router.isReady();
    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    const header = wrapper.find("header");
    // A stacked bar, not an overlay: the embedded editor draws its own menu
    // bar across the top of its frame, so a floating header would land on top
    // of File/Edit/View instead of sharing the row.
    expect(header.classes()).not.toContain("pointer-events-none");
    expect(header.classes()).not.toContain("absolute");
    // Spreadsheets are edit-only: just a Save button, no "Save & Preview"
    // (there is no read-only preview to drop back to).
    expect(wrapper.text()).toContain("Save");
    expect(wrapper.text()).not.toContain("Save & Preview");
  });

  it("opens a spreadsheet straight in edit mode without edit=true", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    // No edit flag — a spreadsheet should still land in the edit-only view,
    // which renders the Save bar rather than the read-only bar's Edit button.
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "data/sheet.xlsx" },
    });
    await router.isReady();
    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    expect(wrapper.text()).toContain("Save");
    expect(wrapper.text()).not.toContain("Edit");
  });

  it("warns on tab close when the spreadsheet has unsaved edits", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "data/sheet.xlsx" },
    });
    await router.isReady();
    previewStub.dirty = true;
    mount(PreviewPage, { props: { userId: "u1" }, global: { plugins: [router] } });
    const e = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(true);
  });

  it("does not warn on tab close when there are no unsaved edits", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({
      name: "file",
      params: { userId: "u1" },
      query: { path: "data/sheet.xlsx" },
    });
    await router.isReady();
    previewStub.dirty = false;
    mount(PreviewPage, { props: { userId: "u1" }, global: { plugins: [router] } });
    const e = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
  });

  it("shows message when no path", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
    });
    await router.push({ name: "file", params: { userId: "u1" } });
    await router.isReady();
    const wrapper = mount(PreviewPage, {
      props: { userId: "u1" },
      global: { plugins: [router] },
    });
    expect(wrapper.text()).toContain("No file path specified");
  });

  describe("editor tabs", () => {
    const TAB = '[data-testid="editor-tab"]';

    function tabPaths(wrapper: {
      findAll: (s: string) => { attributes: (a: string) => string | undefined }[];
    }) {
      return wrapper.findAll(TAB).map((t) => t.attributes("data-path"));
    }

    // Edit mode is decided when the path changes, so a page mounted without
    // ?edit=true never flips into it by pushing the flag onto the same path.
    async function mountEditing(path: string) {
      const router = createRouter({
        history: createMemoryHistory(),
        routes: [{ path: "/file/:userId", name: "file", component: PreviewPage, props: true }],
      });
      await router.push({
        name: "file",
        params: { userId: "u1" },
        query: { path, edit: "true" },
      });
      await router.isReady();
      const wrapper = mount(PreviewPage, {
        props: { userId: "u1" },
        global: { plugins: [router] },
      });
      await flushPromises();
      return wrapper;
    }

    async function openSecondFile(wrapper: Awaited<ReturnType<typeof mountPage>>) {
      await wrapper.vm.$router.push({ query: { path: "b.txt", edit: "true" } });
      await flushPromises();
    }

    it("opening a text file in edit mode adds a tab", async () => {
      const wrapper = await mountEditing("a.txt");

      expect(tabPaths(wrapper)).toEqual(["a.txt"]);
    });

    it("a second file joins the first rather than replacing it", async () => {
      const wrapper = await mountEditing("a.txt");
      await openSecondFile(wrapper);

      expect(tabPaths(wrapper)).toEqual(["a.txt", "b.txt"]);
    });

    // A `line` belongs to the file it was searched in; carrying it across
    // would scroll the newly activated tab somewhere unrelated.
    it("activating a tab drops a stale line from the URL", async () => {
      const wrapper = await mountEditing("a.txt");
      await openSecondFile(wrapper);
      await wrapper.vm.$router.push({ query: { path: "b.txt", edit: "true", line: "42" } });
      await flushPromises();

      const first = wrapper.findAll('[data-testid="editor-tab-label"]')[0];
      await first.trigger("click");
      await flushPromises();

      expect(wrapper.vm.$route.query.path).toBe("a.txt");
      expect(wrapper.vm.$route.query.line).toBeUndefined();
    });

    it("closing a background tab releases its buffer", async () => {
      const wrapper = await mountEditing("a.txt");
      await openSecondFile(wrapper);

      await wrapper.findAll('[data-testid="editor-tab-close"]')[0].trigger("click");
      await flushPromises();

      expect(tabPaths(wrapper)).toEqual(["b.txt"]);
      expect(editorStub.closed).toEqual(["a.txt"]);
      // The visible file is untouched.
      expect(wrapper.vm.$route.query.path).toBe("b.txt");
    });

    it("closing the visible tab falls back to the one on its left", async () => {
      const wrapper = await mountEditing("a.txt");
      await openSecondFile(wrapper);

      await wrapper.findAll('[data-testid="editor-tab-close"]')[1].trigger("click");
      await flushPromises();

      expect(tabPaths(wrapper)).toEqual(["a.txt"]);
      expect(wrapper.vm.$route.query.path).toBe("a.txt");
    });

    // Closing the last tab must not strand the user on a blank page with the
    // file panel gone; dropping to preview keeps them oriented.
    it("closing the last tab drops to preview instead of emptying the page", async () => {
      const wrapper = await mountEditing("a.txt");

      await wrapper.findAll('[data-testid="editor-tab-close"]')[0].trigger("click");
      await flushPromises();

      expect(wrapper.findAll(TAB)).toHaveLength(0);
      expect(wrapper.vm.$route.query.path).toBe("a.txt");
      expect(wrapper.vm.$route.query.edit).toBe("false");
    });

    it("marks a tab whose buffer has unsaved changes", async () => {
      const wrapper = await mountEditing("a.txt");
      expect(wrapper.find('[data-testid="editor-tab-dirty"]').exists()).toBe(false);

      await wrapper
        .findAll("button")
        .find((b) => b.text() === "MarkDirty")!
        .trigger("click");
      await flushPromises();

      expect(wrapper.find('[data-testid="editor-tab-dirty"]').exists()).toBe(true);
    });

    it("a non-text file does not become a tab", async () => {
      const wrapper = await mountPage("photo.png");
      await flushPromises();

      expect(wrapper.findAll(TAB)).toHaveLength(0);
    });

    it("restores the tab set after a reload", async () => {
      const first = await mountEditing("a.txt");
      await openSecondFile(first);
      first.unmount();

      const second = await mountEditing("a.txt");

      expect(tabPaths(second)).toEqual(["a.txt", "b.txt"]);
    });
  });

  describe("quick open", () => {
    function press(key: string, extra: KeyboardEventInit = {}) {
      const e = new KeyboardEvent("keydown", { key, metaKey: true, cancelable: true, ...extra });
      window.dispatchEvent(e);
      return e;
    }

    it("Cmd+P opens the palette in name mode", async () => {
      const wrapper = await mountPage("notes.txt");
      await flushPromises();

      const e = press("p");
      await nextTick();

      // The browser's print dialog must not also fire.
      expect(e.defaultPrevented).toBe(true);
      expect(wrapper.get('[data-testid="quick-open-stub"]').text()).toBe("mode:name");
    });

    it("Cmd+Shift+F opens the palette in content mode", async () => {
      const wrapper = await mountPage("notes.txt");
      await flushPromises();

      press("f", { shiftKey: true });
      await nextTick();

      expect(wrapper.get('[data-testid="quick-open-stub"]').text()).toBe("mode:content");
    });

    // Navigating the workspace is not an editing action, so the shortcut has
    // to work from the read-only preview too.
    it("works in preview mode", async () => {
      const wrapper = await mountPage("photo.png");
      await flushPromises();

      press("p");
      await nextTick();

      expect(wrapper.find('[data-testid="quick-open-stub"]').exists()).toBe(true);
    });

    it("a content hit navigates with the matched line in the URL", async () => {
      const wrapper = await mountPage("notes.txt");
      await flushPromises();

      wrapper.getComponent({ name: "FileQuickOpen" }).vm.$emit("select", {
        path: "src/app.ts",
        line: 42,
      });
      await flushPromises();

      expect(wrapper.vm.$route.query.path).toBe("src/app.ts");
      expect(wrapper.vm.$route.query.line).toBe("42");
    });

    // A name-mode hit has no line; a stale `line` from a previous jump would
    // otherwise scroll the new file to an unrelated place.
    it("a name hit clears a stale line from the URL", async () => {
      const wrapper = await mountPage("notes.txt");
      await flushPromises();

      const palette = wrapper.getComponent({ name: "FileQuickOpen" });
      palette.vm.$emit("select", { path: "src/app.ts", line: 42 });
      await flushPromises();
      palette.vm.$emit("select", { path: "src/other.ts", line: 0 });
      await flushPromises();

      expect(wrapper.vm.$route.query.path).toBe("src/other.ts");
      expect(wrapper.vm.$route.query.line).toBeUndefined();
    });
  });
});
