import { beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { createI18n } from "vue-i18n";

import FileWorkbench from "./FileWorkbench.vue";
import en from "@/i18n/locales/en";

// The workbench subscribes through useFileWatch; this stub captures its
// callbacks so a test can fire a disk change without a real socket.
const watchHooks = vi.hoisted(() => ({
  onChanged: null as ((path: string) => void) | null,
  onRemoved: null as ((path: string) => void) | null,
  paths: [] as string[],
  stopped: 0,
}));
vi.mock("@/composables/useFileWatch", () => ({
  useFileWatch: (opts: {
    paths: { value: string[] };
    onChanged: (p: string) => void;
    onRemoved: (p: string) => void;
  }) => {
    watchHooks.onChanged = opts.onChanged;
    watchHooks.onRemoved = opts.onRemoved;
    Object.defineProperty(watchHooks, "paths", {
      get: () => opts.paths.value,
      configurable: true,
    });
    return { mode: { value: "ws" }, stop: () => watchHooks.stopped++ };
  },
}));

const previewStub = vi.hoisted(() => ({ refreshed: 0 }));
vi.mock("@/components/FilePreview.vue", () => ({
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({
    name: "FilePreview",
    setup(_, { expose }) {
      expose({
        refresh() {
          previewStub.refreshed++;
        },
        save: async () => true,
        isDirty: () => false,
      });
      return () => h("div", { "data-testid": "file-preview-stub" }, "file-preview-stub");
    },
  }),
}));

const editorStub = vi.hoisted(() => ({
  dirty: new Set<string>(),
  reloaded: [] as string[],
  closed: [] as string[],
}));
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
    setup(props, { expose }) {
      expose({
        closeFile: (path: string) => editorStub.closed.push(path),
        reloadFile: async (path: string) => {
          editorStub.reloaded.push(path);
          editorStub.dirty.delete(path);
        },
        isDirty: (path: string) => editorStub.dirty.has(path),
      });
      return () => h("div", { "data-testid": "file-text-editor-stub" }, `editor:${props.path}`);
    },
  }),
}));

vi.mock("@/components/FileQuickOpen.vue", () => ({
  // eslint-disable-next-line vue/one-component-per-file
  default: defineComponent({ name: "FileQuickOpen", setup: () => () => null }),
}));

vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: async () => true }),
}));

vi.mock("@/composables/useFileApi", () => ({
  // Re-exported alongside useFileApi; the workbench imports it directly to
  // decide whether a file gets the page/source toggle.
  isHtmlFile: (name: string) => /\.html?$/i.test(name),
  useFileApi: () => ({
    inspectFile: vi.fn().mockResolvedValue({ is_text: true, modified: "", size: 0 }),
    downloadFileUrl: (_uid: string, path: string) => `/api/download?path=${path}`,
  }),
  isEditableText: (name: string) => name.endsWith(".txt") || name.endsWith(".md"),
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

describe("FileWorkbench disk sync", () => {
  beforeEach(() => {
    sessionStorage.clear();
    previewStub.refreshed = 0;
    editorStub.dirty.clear();
    editorStub.reloaded.length = 0;
    editorStub.closed.length = 0;
    watchHooks.onChanged = null;
    watchHooks.onRemoved = null;
  });

  it("watches the file on screen", async () => {
    await mountWorkbench({ path: "docs/a.md" });
    expect(watchHooks.paths).toEqual(["docs/a.md"]);
  });

  it("watches every open editor tab, not just the visible one", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    await wrapper.setProps({ path: "b.txt" });
    await flushPromises();
    expect([...watchHooks.paths].sort()).toEqual(["a.txt", "b.txt"]);
  });

  it("silently reloads the preview when the watcher reports a change", async () => {
    await mountWorkbench({ path: "docs/a.md" });
    watchHooks.onChanged!("docs/a.md");
    await flushPromises();
    expect(previewStub.refreshed).toBe(1);
  });

  it("silently reloads a clean editor buffer", async () => {
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    watchHooks.onChanged!("a.txt");
    await flushPromises();
    expect(editorStub.reloaded).toEqual(["a.txt"]);
    expect(wrapper.find('[data-testid="workbench-disk-conflict"]').exists()).toBe(false);
  });

  it("does NOT clobber unsaved editor changes — it shows the conflict bar", async () => {
    editorStub.dirty.add("a.txt");
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    watchHooks.onChanged!("a.txt");
    await flushPromises();

    expect(editorStub.reloaded).toEqual([]);
    expect(wrapper.find('[data-testid="workbench-disk-conflict"]').exists()).toBe(true);
  });

  it("reloads from disk when the conflict bar's Reload is clicked", async () => {
    editorStub.dirty.add("a.txt");
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    watchHooks.onChanged!("a.txt");
    await flushPromises();

    await wrapper.find('[data-testid="workbench-conflict-reload"]').trigger("click");
    await flushPromises();
    expect(editorStub.reloaded).toEqual(["a.txt"]);
    expect(wrapper.find('[data-testid="workbench-disk-conflict"]').exists()).toBe(false);
  });

  it("dismisses the conflict bar and keeps the buffer when Keep my changes is clicked", async () => {
    editorStub.dirty.add("a.txt");
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    watchHooks.onChanged!("a.txt");
    await flushPromises();

    await wrapper.find('[data-testid="workbench-conflict-keep"]').trigger("click");
    await flushPromises();
    expect(editorStub.reloaded).toEqual([]);
    expect(wrapper.find('[data-testid="workbench-disk-conflict"]').exists()).toBe(false);
  });

  it("shows the removed notice when the watcher reports a removal", async () => {
    const wrapper = await mountWorkbench({ path: "docs/a.md" });
    watchHooks.onRemoved!("docs/a.md");
    await flushPromises();
    expect(wrapper.find('[data-testid="workbench-disk-removed"]').exists()).toBe(true);
  });

  it("does not carry a notice across a tab switch", async () => {
    editorStub.dirty.add("a.txt");
    const wrapper = await mountWorkbench({ path: "a.txt", editMode: true });
    watchHooks.onChanged!("a.txt");
    await flushPromises();
    expect(wrapper.find('[data-testid="workbench-disk-conflict"]').exists()).toBe(true);

    await wrapper.setProps({ path: "b.txt" });
    await flushPromises();
    expect(wrapper.find('[data-testid="workbench-disk-conflict"]').exists()).toBe(false);
  });
});
