import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";

import { undo } from "@codemirror/commands";
import { EditorView } from "@codemirror/view";

import FileTextEditor from "./FileTextEditor.vue";
import { WriteConflictError } from "@/composables/useFileApi";

const mockReadFile = vi.fn();
const mockWriteFile = vi.fn();

vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({
    readFile: (...args: unknown[]) => mockReadFile(...args),
    writeFile: (...args: unknown[]) => mockWriteFile(...args),
  }),
  WriteConflictError: class extends Error {
    status = 412 as const;
    etag: string;
    constructor(etag: string) {
      super("write file: conflict");
      this.etag = etag;
    }
  },
}));

vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: vi.fn().mockResolvedValue(true) }),
}));

// The editor is the real CodeMirror view: it runs fine under happy-dom and the
// behaviour under test (buffers, save, conflicts) is only honest if typing
// goes through real transactions. The factory is routed through a mock so one
// test can make bringing the view up fail the way a stale chunk does.
const editorFactory = vi.hoisted(() => ({
  real: null as ((parent: HTMLElement) => EditorView) | null,
  create: vi.fn(),
}));
vi.mock("@/lib/codeEditor", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/codeEditor")>();
  editorFactory.real = actual.createEditorView;
  return { ...actual, createEditorView: (parent: HTMLElement) => editorFactory.create(parent) };
});

function textResponse(body: string, etag = 'W/"seed-1"') {
  return {
    text: () => Promise.resolve(body),
    headers: new Headers({ ETag: etag }),
  } as unknown as Response;
}

beforeEach(() => {
  vi.clearAllMocks();
  editorFactory.create.mockImplementation((parent: HTMLElement) => editorFactory.real!(parent));
  mockReadFile.mockResolvedValue(textResponse("original"));
  mockWriteFile.mockResolvedValue({
    path: "/notes.txt",
    size: 8,
    modified: "2026-01-01T00:00:00Z",
    etag: 'W/"saved-1"',
  });
});

async function mountEditor() {
  const wrapper = mount(FileTextEditor, {
    props: { userId: "u1", path: "/notes.txt" },
    attachTo: document.body,
  });
  await flushPromises();
  return wrapper;
}

function button(wrapper: Awaited<ReturnType<typeof mountEditor>>, label: string) {
  return wrapper.findAll("button").find((b) => b.text() === label);
}

function view(wrapper: Awaited<ReturnType<typeof mountEditor>>): EditorView {
  const v = EditorView.findFromDOM(wrapper.find(".cm-editor").element as HTMLElement);
  if (!v) throw new Error("no editor view mounted");
  return v;
}

function visibleText(wrapper: Awaited<ReturnType<typeof mountEditor>>): string {
  return view(wrapper).state.doc.toString();
}

// Simulate the user typing into the visible buffer: a real transaction, which
// is what the component's update listener reacts to.
async function typeInto(wrapper: Awaited<ReturnType<typeof mountEditor>>, text: string) {
  const v = view(wrapper);
  v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text } });
  await wrapper.vm.$nextTick();
}

describe("FileTextEditor", () => {
  it("mounts the editor once the file is read", async () => {
    const wrapper = await mountEditor();
    expect(mockReadFile).toHaveBeenCalledWith("u1", "/notes.txt");
    expect(button(wrapper, "Save")?.attributes("disabled")).toBeDefined();
  });

  it("saves the current buffer", async () => {
    const wrapper = await mountEditor();
    await typeInto(wrapper, "edited");

    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();

    // The fourth argument is the validator read off the load response — the
    // compare-and-swap that stops a concurrent agent write being clobbered.
    expect(mockWriteFile).toHaveBeenCalledWith("u1", "/notes.txt", "edited", 'W/"seed-1"');
  });

  // A failed save used to latch `error`, and the guard bailed on any non-empty
  // error. The button stayed enabled and dirty stayed true, so the retry did
  // nothing at all — the only way out was Revert, which discards the edits.
  it("retries a save after a failure without discarding the edits", async () => {
    const wrapper = await mountEditor();
    await typeInto(wrapper, "edited");

    mockWriteFile.mockRejectedValueOnce(new Error("request entity too large"));
    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("request entity too large");
    // The work is still in the buffer and still flagged unsaved.
    expect(wrapper.text()).toContain("unsaved");

    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();

    expect(mockWriteFile).toHaveBeenCalledTimes(2);
    expect(mockWriteFile).toHaveBeenLastCalledWith("u1", "/notes.txt", "edited", 'W/"seed-1"');
    // Success clears the stale failure and the unsaved marker.
    expect(wrapper.text()).not.toContain("request entity too large");
    expect(wrapper.text()).not.toContain("unsaved");
  });

  it("clears the previous failure message while a retry is in flight", async () => {
    const wrapper = await mountEditor();
    await typeInto(wrapper, "edited");

    mockWriteFile.mockRejectedValueOnce(new Error("boom"));
    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("boom");

    let release: () => void = () => {};
    mockWriteFile.mockImplementationOnce(() => new Promise<void>((r) => (release = r)));
    await button(wrapper, "Save")?.trigger("click");
    await wrapper.vm.$nextTick();

    expect(wrapper.text()).not.toContain("boom");

    release();
    await flushPromises();
  });

  // The load error is a different animal: without a buffer there is nothing
  // safe to write back, so that one still has to block saving.
  it("refuses to save when the initial read failed", async () => {
    mockReadFile.mockRejectedValueOnce(new Error("permission denied"));
    const wrapper = await mountEditor();

    expect(wrapper.text()).toContain("permission denied");
    await typeInto(wrapper, "edited");
    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();

    expect(mockWriteFile).not.toHaveBeenCalled();
  });

  // The editor bring-up used to sit outside every try/catch: a failure
  // rejected into `void loadAndMount()` and left the panel spinning on
  // "Loading…" with an empty error channel and no way out.
  it("surfaces a failure to bring up the editor instead of loading forever", async () => {
    editorFactory.create.mockImplementationOnce(() => {
      throw new Error("Failed to fetch dynamically imported module");
    });
    const wrapper = await mountEditor();

    expect(wrapper.text()).toContain("Could not load the code editor");
    expect(wrapper.text()).toContain("Failed to fetch dynamically imported module");
    expect(wrapper.text()).not.toContain("Loading");
    // A load failure is terminal: there is no buffer worth writing back.
    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();
    expect(mockWriteFile).not.toHaveBeenCalled();
  });

  it("reverting clears a stale save failure", async () => {
    const wrapper = await mountEditor();
    await typeInto(wrapper, "edited");

    mockWriteFile.mockRejectedValueOnce(new Error("boom"));
    await button(wrapper, "Save")?.trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("boom");

    await button(wrapper, "Revert")?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).not.toContain("boom");
  });

  // The reason the optimistic lock exists: an agent is editing the same
  // workspace the human has open, and a stale save must not silently win.
  // The point of holding one view and many states: leaving a tab and coming
  // back must not cost the user the edits they had not saved yet.
  describe("multiple open buffers", () => {
    function expose(wrapper: Awaited<ReturnType<typeof mountEditor>>) {
      return wrapper.vm as unknown as {
        closeFile: (path: string) => void;
        isDirty: (path: string) => boolean;
      };
    }

    it("keeps unsaved edits when another file is opened and returned to", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited in notes");

      mockReadFile.mockResolvedValue(textResponse("other contents", 'W/"other-1"'));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();
      expect(visibleText(wrapper)).toBe("other contents");

      await wrapper.setProps({ path: "/notes.txt" });
      await flushPromises();

      expect(visibleText(wrapper)).toBe("edited in notes");
    });

    it("does not re-read a file that is already open", async () => {
      const wrapper = await mountEditor();
      mockReadFile.mockResolvedValue(textResponse("other contents"));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();
      expect(mockReadFile).toHaveBeenCalledTimes(2);

      await wrapper.setProps({ path: "/notes.txt" });
      await flushPromises();

      // Switching back is a setState, not a fetch.
      expect(mockReadFile).toHaveBeenCalledTimes(2);
    });

    it("reports every dirty path, not just the visible one", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited in notes");

      mockReadFile.mockResolvedValue(textResponse("other contents"));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();
      await typeInto(wrapper, "edited in other");

      expect(wrapper.emitted("dirtyPaths")?.at(-1)?.[0]).toEqual(["/notes.txt", "/other.txt"]);
    });

    it("saves the visible buffer with its own validator", async () => {
      const wrapper = await mountEditor();
      mockReadFile.mockResolvedValue(textResponse("other contents", 'W/"other-1"'));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();
      await typeInto(wrapper, "edited in other");

      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();

      expect(mockWriteFile).toHaveBeenCalledWith(
        "u1",
        "/other.txt",
        "edited in other",
        'W/"other-1"',
      );
    });

    // Unsaved work in a background tab is exactly as easy to lose as unsaved
    // work in the visible one.
    it("warns on unload when a background buffer is dirty", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited in notes");

      mockReadFile.mockResolvedValue(textResponse("other contents"));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();

      const e = new Event("beforeunload", { cancelable: true });
      window.dispatchEvent(e);
      expect(e.defaultPrevented).toBe(true);
    });

    it("keeps undo history per buffer across a tab switch", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited in notes");

      mockReadFile.mockResolvedValue(textResponse("other contents"));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();
      await wrapper.setProps({ path: "/notes.txt" });
      await flushPromises();

      undo(view(wrapper));
      await wrapper.vm.$nextTick();

      expect(visibleText(wrapper)).toBe("original");
      // Undoing back to what's on disk is clean again, not "still edited".
      expect(wrapper.text()).not.toContain("unsaved");
    });

    // Closing a tab has to actually let go of the buffer: reopening it reads
    // the file again instead of resurrecting the discarded edits.
    it("closeFile drops a background buffer", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited in notes");
      mockReadFile.mockResolvedValue(textResponse("other contents"));
      await wrapper.setProps({ path: "/other.txt" });
      await flushPromises();

      expose(wrapper).closeFile("/notes.txt");
      mockReadFile.mockResolvedValue(textResponse("original"));
      await wrapper.setProps({ path: "/notes.txt" });
      await flushPromises();

      expect(mockReadFile).toHaveBeenCalledTimes(3);
      expect(visibleText(wrapper)).toBe("original");
      expect(wrapper.emitted("dirtyPaths")?.at(-1)?.[0]).toEqual([]);
    });

    it("closing the visible file clears it off the editor", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited in notes");

      expose(wrapper).closeFile("/notes.txt");

      expect(visibleText(wrapper)).toBe("");
      expect(view(wrapper).state.facet(EditorView.editable)).toBe(false);
      expect(expose(wrapper).isDirty("/notes.txt")).toBe(false);
    });

    it("unmounting destroys the view", async () => {
      const wrapper = await mountEditor();
      const v = view(wrapper);

      wrapper.unmount();

      expect(v.dom.isConnected).toBe(false);
    });

    it("a stale read does not yank the editor back to a file the user left", async () => {
      const wrapper = await mountEditor();

      // /slow.txt never settles before the user moves on to /fast.txt.
      let releaseSlow: (r: Response) => void = () => {};
      mockReadFile.mockReturnValueOnce(
        new Promise<Response>((resolve) => {
          releaseSlow = resolve;
        }),
      );
      await wrapper.setProps({ path: "/slow.txt" });
      mockReadFile.mockResolvedValue(textResponse("fast contents"));
      await wrapper.setProps({ path: "/fast.txt" });
      await flushPromises();
      expect(visibleText(wrapper)).toBe("fast contents");

      releaseSlow(textResponse("slow contents"));
      await flushPromises();

      expect(visibleText(wrapper)).toBe("fast contents");
    });
  });

  describe("save conflicts", () => {
    it("surfaces a conflict instead of a plain error", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");

      mockWriteFile.mockRejectedValueOnce(new WriteConflictError('W/"theirs-1"'));
      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();

      expect(wrapper.text()).toContain("changed on disk");
      // The buffer survives — the user still has somewhere to copy from.
      expect(wrapper.text()).toContain("unsaved");
    });

    it("overwrite drops the precondition so the retry cannot conflict again", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");

      mockWriteFile.mockRejectedValueOnce(new WriteConflictError('W/"theirs-1"'));
      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();

      await button(wrapper, "Overwrite anyway")?.trigger("click");
      await flushPromises();

      expect(mockWriteFile).toHaveBeenLastCalledWith("u1", "/notes.txt", "edited", undefined);
      expect(wrapper.text()).not.toContain("changed on disk");
      expect(wrapper.text()).not.toContain("unsaved");
    });

    it("a successful save adopts the returned validator for the next one", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");

      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();

      await typeInto(wrapper, "edited again");
      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();

      // Second save compares against what the first one wrote, not the
      // version originally read — otherwise every save after the first
      // would spuriously conflict with itself.
      expect(mockWriteFile).toHaveBeenLastCalledWith(
        "u1",
        "/notes.txt",
        "edited again",
        'W/"saved-1"',
      );
    });

    it("reloading after a conflict clears the banner and takes the disk version", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");

      mockWriteFile.mockRejectedValueOnce(new WriteConflictError('W/"theirs-1"'));
      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();
      expect(wrapper.text()).toContain("changed on disk");

      mockReadFile.mockResolvedValueOnce(textResponse("agent wrote this", 'W/"theirs-1"'));
      await button(wrapper, "Discard mine and reload")?.trigger("click");
      await flushPromises();

      expect(wrapper.text()).not.toContain("changed on disk");
      expect(wrapper.text()).not.toContain("unsaved");
    });
  });

  // The workbench's disk-change sync drives these two directly: it decides
  // whether overwriting is safe, and needs the editor to answer and to obey.
  describe("disk-sync surface", () => {
    interface EditorApi {
      reloadFile: (path: string) => Promise<void>;
      isDirty: (path: string) => boolean;
    }

    it("reloadFile replaces the buffer contents from the server", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");

      mockReadFile.mockResolvedValueOnce(textResponse("agent wrote this", 'W/"theirs-1"'));
      await (wrapper.vm as unknown as EditorApi).reloadFile("/notes.txt");
      await flushPromises();

      expect(visibleText(wrapper)).toBe("agent wrote this");
    });

    it("reloadFile leaves the buffer clean, so the reload isn't mistaken for an edit", async () => {
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");
      expect(wrapper.text()).toContain("unsaved");

      mockReadFile.mockResolvedValueOnce(textResponse("agent wrote this", 'W/"theirs-1"'));
      await (wrapper.vm as unknown as EditorApi).reloadFile("/notes.txt");
      await flushPromises();

      expect(wrapper.text()).not.toContain("unsaved");
    });

    it("reloadFile does not prompt — the caller already made the call", async () => {
      // revert() asks before discarding; this path must not, or the workbench
      // would double-prompt after the user already clicked Reload.
      const wrapper = await mountEditor();
      await typeInto(wrapper, "edited");

      mockReadFile.mockResolvedValueOnce(textResponse("fresh", 'W/"theirs-1"'));
      await (wrapper.vm as unknown as EditorApi).reloadFile("/notes.txt");
      await flushPromises();

      expect(visibleText(wrapper)).toBe("fresh");
    });

    it("isDirty reports true only for buffers with unsaved edits", async () => {
      const wrapper = await mountEditor();
      const api = wrapper.vm as unknown as EditorApi;
      expect(api.isDirty("/notes.txt")).toBe(false);

      await typeInto(wrapper, "edited");
      expect(api.isDirty("/notes.txt")).toBe(true);
      // A path this editor never opened is not dirty.
      expect(api.isDirty("/other.txt")).toBe(false);
    });

    it("isDirty goes false again after a save", async () => {
      const wrapper = await mountEditor();
      const api = wrapper.vm as unknown as EditorApi;
      await typeInto(wrapper, "edited");

      await button(wrapper, "Save")?.trigger("click");
      await flushPromises();

      expect(api.isDirty("/notes.txt")).toBe(false);
    });
  });
});
