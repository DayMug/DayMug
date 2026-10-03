import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory, type Router } from "vue-router";
import WorkspacePanel from "./WorkspacePanel.vue";

const sharedRouter: Router = createRouter({
  history: createMemoryHistory("/"),
  routes: [
    { path: "/", name: "home", component: { template: "<div />" } },
    { path: "/file/:userId", name: "file", component: { template: "<div />" } },
  ],
});

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function mountPanelSync(opts: any) {
  return mount(WorkspacePanel, {
    ...(opts ?? {}),
    global: { plugins: [sharedRouter], ...(opts?.global ?? {}) },
  });
}

const mockListDir = vi.fn();
const mockDownloadFileUrl = vi.fn().mockReturnValue("/dl");
const mockDownloadZipUrl = vi.fn().mockReturnValue("/dlzip");
const mockUploadFiles = vi.fn();
const mockDeleteFile = vi.fn();
const mockRenameFile = vi.fn();
const mockWriteFile = vi.fn();
const mockMkdir = vi.fn();
const mockMoveFile = vi.fn();
const mockCopyFile = vi.fn();
const mockExtractArchive = vi.fn();
const mockCompressToZip = vi.fn();

// vi.mock is hoisted; declare the error class via vi.hoisted so the same
// constructor is shared between the SUT (via the mocked module) and the
// tests below (so `e instanceof UploadConflictError` matches).
const { TestUploadConflictError } = vi.hoisted(() => {
  class TestUploadConflictError extends Error {
    status = 409 as const;
    conflicts: string[];
    constructor(conflicts: string[]) {
      super("upload: conflict");
      this.conflicts = conflicts;
    }
  }
  return { TestUploadConflictError };
});

vi.mock("@/composables/useFileApi", () => ({
  // Re-exported alongside useFileApi; the preview surface and the open-file
  // routing import it directly, so the mock has to carry it too.
  isHtmlFile: (name: string) => /\.html?$/i.test(name),
  useFileApi: () => ({
    listDir: mockListDir,
    readFile: vi.fn(),
    inspectFile: vi.fn().mockResolvedValue({
      path: "README",
      name: "README",
      is_dir: false,
      size: 5,
      modified: "",
      content_type: "text/plain; charset=utf-8",
      is_text: true,
    }),
    downloadFileUrl: mockDownloadFileUrl,
    downloadZipUrl: mockDownloadZipUrl,
    uploadFiles: mockUploadFiles,
    deleteFile: mockDeleteFile,
    renameFile: mockRenameFile,
    writeFile: mockWriteFile,
    mkdir: mockMkdir,
    moveFile: mockMoveFile,
    copyFile: mockCopyFile,
    extractArchive: mockExtractArchive,
    compressToZip: mockCompressToZip,
  }),
  UploadConflictError: TestUploadConflictError,
  // FileContextMenu imports this directly to decide whether to show the
  // "Extract Here" item; the mock needs to expose it or the component
  // crashes during render in any test that mounts WorkspacePanel.
  isSupportedArchive: (name: string) => {
    const lower = name.toLowerCase();
    return (
      lower.endsWith(".zip") ||
      lower.endsWith(".tar") ||
      lower.endsWith(".tar.gz") ||
      lower.endsWith(".tgz") ||
      lower.endsWith(".tar.bz2") ||
      lower.endsWith(".tbz2")
    );
  },
  // WorkspacePreview reads this synchronously during render to gate the
  // Edit button.
  isEditableText: (name: string) => {
    const lower = name.toLowerCase();
    const base = lower.split("/").pop() ?? "";
    if (base === "go.mod" || base === "go.sum") return true;
    const dot = lower.lastIndexOf(".");
    if (dot < 0) return lower === "makefile" || lower === "dockerfile";
    return [
      ".txt",
      ".log",
      ".md",
      ".markdown",
      ".json",
      ".js",
      ".ts",
      ".tsx",
      ".jsx",
      ".vue",
      ".go",
      ".py",
      ".css",
      ".html",
      ".yaml",
      ".yml",
      ".sh",
    ].includes(lower.slice(dot));
  },
  shouldInspectTextFile: (name: string) => !name.includes("."),
}));

vi.mock("@/composables/useMarkdown", () => ({
  renderMarkdown: (s: string) => `<p>${s}</p>`,
}));

const mockConfirm = vi.hoisted(() =>
  vi.fn<(opts: { message: string }) => Promise<boolean>>(async () => true),
);
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}));

vi.mock("@/composables/useTheme", async () => {
  const { ref } = await import("vue");
  return { useTheme: () => ({ isDark: ref(false), toggleTheme: () => {} }) };
});

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.removeItem("daymug-ui-prefs-v1");
  mockListDir.mockResolvedValue({
    path: ".",
    entries: [
      { name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" },
      { name: "README.md", is_dir: false, size: 1024, modified: "2024-01-01T00:00:00Z" },
    ],
  });
  mockRenameFile.mockResolvedValue(undefined);
  mockWriteFile.mockResolvedValue({ path: "New File", size: 0, modified: "" });
  mockMkdir.mockResolvedValue(undefined);
  mockMoveFile.mockResolvedValue(undefined);
  mockCopyFile.mockResolvedValue({ path: "src/README.md" });
});

describe("WorkspacePanel", () => {
  it("renders file list after loading", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    expect(mockListDir).toHaveBeenCalledWith("u1", ".");
    expect(wrapper.findAll(".file-card").length).toBe(2);
  });

  it("shows directories first", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    const rows = wrapper.findAll(".file-card");
    expect(rows[0].find(".file-name").text()).toBe("src");
    expect(rows[1].find(".file-name").text()).toBe("README.md");
  });

  it("shows the home button at root and no path crumbs", async () => {
    // After moving to the editorial breadcrumb design, the implicit "Home"
    // is rendered as a standalone Home button (`.home-btn`) outside the path
    // list. At the root no `.crumb-btn` pills are produced — Home alone
    // signals "you are at home".
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    const home = wrapper.find(".home-btn");
    expect(home.exists()).toBe(true);
    expect(home.text()).toBe("Home");
    expect(wrapper.findAll(".crumb-btn").length).toBe(0);
  });

  it("breadcrumbs container scrolls horizontally instead of clipping deep paths", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    const bc = wrapper.find(".ws-breadcrumbs");
    expect(bc.exists()).toBe(true);
    // Must allow horizontal scroll, must not hard-clip overflow.
    expect(bc.classes()).toContain("overflow-x-auto");
    expect(bc.classes()).not.toContain("overflow-hidden");
  });

  it("renders current folder as a styled (non-button) trailing crumb", async () => {
    // After navigating into ./src, the breadcrumb list should render
    // exactly one crumb pill — the current folder — with `.current` and
    // no click handler. Intermediate paths get clickable crumbs; this
    // test exercises the "leaf only" case for clarity.
    mockListDir.mockResolvedValueOnce({
      path: ".",
      entries: [{ name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" }],
    });
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    mockListDir.mockResolvedValueOnce({ path: "src", entries: [] });
    await wrapper.findAll(".file-card")[0].trigger("dblclick");
    await flushPromises();

    const crumbs = wrapper.findAll(".crumb-btn");
    expect(crumbs.length).toBe(1);
    expect(crumbs[0].classes()).toContain("current");
    expect(crumbs[0].text()).toBe("src");
  });

  it("does not render the Finder-style bottom status bar", async () => {
    // The bottom status bar was removed at the user's request — it
    // duplicated information visible in the breadcrumb / file count and
    // ate vertical space inside the 420px workspace pane.
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    expect(wrapper.find(".ws-status").exists()).toBe(false);
  });

  it("navigates into directory on double-click", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    mockListDir.mockResolvedValue({ path: "src", entries: [] });
    await wrapper.findAll(".file-card")[0].trigger("dblclick");
    await flushPromises();

    expect(mockListDir).toHaveBeenCalledWith("u1", "src");
  });

  it("expands nested directories lazily in list view", async () => {
    localStorage.setItem("daymug-ui-prefs-v1", JSON.stringify({ viewMode: "list" }));
    mockListDir.mockImplementation((_userId: string, path: string) => {
      if (path === "src/components") {
        return Promise.resolve({
          path,
          entries: [
            {
              name: "Button.vue",
              is_dir: false,
              size: 128,
              modified: "2024-01-01T00:00:00Z",
            },
          ],
        });
      }
      if (path === "src") {
        return Promise.resolve({
          path,
          entries: [
            {
              name: "components",
              is_dir: true,
              size: 0,
              modified: "2024-01-01T00:00:00Z",
            },
            { name: "main.ts", is_dir: false, size: 64, modified: "2024-01-01T00:00:00Z" },
          ],
        });
      }
      return Promise.resolve({
        path: ".",
        entries: [{ name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" }],
      });
    });

    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await flushPromises();
    await wrapper.find("[data-tree-path='src/components'] .tree-toggle").trigger("click");
    await flushPromises();

    expect(mockListDir).toHaveBeenCalledWith("u1", "src");
    expect(mockListDir).toHaveBeenCalledWith("u1", "src/components");
    expect(wrapper.find("[data-tree-path='src/components/Button.vue']").exists()).toBe(true);
    expect(
      wrapper.find("[data-tree-path='src/components/Button.vue']").attributes("aria-level"),
    ).toBe("3");
  });

  it("opens the context menu for an expanded list row with its full path", async () => {
    localStorage.setItem("daymug-ui-prefs-v1", JSON.stringify({ viewMode: "list" }));
    mockListDir.mockImplementation((_userId: string, path: string) =>
      Promise.resolve(
        path === "src"
          ? {
              path,
              entries: [
                {
                  name: "main.ts",
                  is_dir: false,
                  size: 64,
                  modified: "2024-01-01T00:00:00Z",
                },
              ],
            }
          : {
              path: ".",
              entries: [
                {
                  name: "src",
                  is_dir: true,
                  size: 0,
                  modified: "2024-01-01T00:00:00Z",
                },
              ],
            },
      ),
    );
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await flushPromises();

    await wrapper.find("[data-tree-path='src/main.ts']").trigger("contextmenu", {
      clientX: 80,
      clientY: 120,
    });

    const menu = wrapper.findComponent({ name: "FileContextMenu" });
    expect(menu.exists()).toBe(true);
    expect(menu.props("entryPath")).toBe("src/main.ts");
  });

  it("opens a nested list-view file in the current tab without leaving the directory", async () => {
    localStorage.setItem("daymug-ui-prefs-v1", JSON.stringify({ viewMode: "list" }));
    await sharedRouter.replace({ path: "/", query: {} });
    mockListDir.mockImplementation((_userId: string, path: string) =>
      Promise.resolve(
        path === "src"
          ? {
              path,
              entries: [
                { name: "main.ts", is_dir: false, size: 64, modified: "2024-01-01T00:00:00Z" },
              ],
            }
          : {
              path: ".",
              entries: [{ name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" }],
            },
      ),
    );
    const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "same-tab" } });
    await flushPromises();
    await wrapper.find("[data-tree-path='src'] .tree-toggle").trigger("click");
    await flushPromises();

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      await wrapper.find("[data-tree-path='src/main.ts']").trigger("dblclick");
      await flushPromises();

      expect(openSpy).not.toHaveBeenCalled();
      expect(sharedRouter.currentRoute.value.name).toBe("file");
      expect(sharedRouter.currentRoute.value.query.path).toBe("src/main.ts");
      // The panel stayed at the root listing: a re-list resets the tree, so a
      // still-rendered nested row proves nothing navigated.
      expect(wrapper.find("[data-tree-path='src/main.ts']").exists()).toBe(true);
    } finally {
      openSpy.mockRestore();
    }
  });

  it("reuses a loaded branch after collapsing and reopening it", async () => {
    localStorage.setItem("daymug-ui-prefs-v1", JSON.stringify({ viewMode: "list" }));
    mockListDir.mockImplementation((_userId: string, path: string) =>
      Promise.resolve(
        path === "src"
          ? {
              path,
              entries: [
                {
                  name: "main.ts",
                  is_dir: false,
                  size: 64,
                  modified: "2024-01-01T00:00:00Z",
                },
              ],
            }
          : {
              path: ".",
              entries: [
                {
                  name: "src",
                  is_dir: true,
                  size: 0,
                  modified: "2024-01-01T00:00:00Z",
                },
              ],
            },
      ),
    );

    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    const toggle = wrapper.find("[data-tree-path='src'] .tree-toggle");
    await toggle.trigger("click");
    await flushPromises();
    await toggle.trigger("click");
    await toggle.trigger("click");
    await flushPromises();

    expect(mockListDir.mock.calls.filter((call) => call[1] === "src")).toHaveLength(1);
    expect(wrapper.find("[data-tree-path='src/main.ts']").exists()).toBe(true);
  });

  it("selects entry on single click", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[0].trigger("click");
    expect(wrapper.findAll(".file-card")[0].classes()).toContain("selected");
  });

  it("shows error state when home directory also fails", async () => {
    mockListDir.mockRejectedValue(new Error("fail"));
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    expect(wrapper.find(".ws-error").exists()).toBe(true);
  });

  it("falls back to home directory when subdirectory fails to load, and says why", async () => {
    // The fallback keeps a vanished directory from stranding the panel, but it
    // used to be silent — the user was teleported back to Home with no hint
    // that their double-click had failed rather than been ignored.
    mockListDir.mockResolvedValueOnce({
      path: ".",
      entries: [{ name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" }],
    });
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Now simulate navigating to a directory that no longer exists
    mockListDir.mockRejectedValueOnce(new Error("list dir: 404"));
    // The fallback call to "." should succeed
    mockListDir.mockResolvedValueOnce({
      path: ".",
      entries: [{ name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" }],
    });

    await wrapper.findAll(".file-card")[0].trigger("dblclick");
    await flushPromises();

    // Should have called "." as fallback
    expect(mockListDir).toHaveBeenCalledWith("u1", ".");
    expect(wrapper.find(".ws-error").text()).toContain("list dir: 404");
  });

  it("does not surface new-folder, upload, or hidden-files buttons in the top bar", async () => {
    // Every workspace action lives in the right-click context menu now;
    // the top bar is intentionally minimal (breadcrumbs + optional
    // collapse) so the panel reads as a calm path display rather than a
    // toolbar.
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    expect(wrapper.find(".ws-action-upload").exists()).toBe(false);
    expect(wrapper.find(".ws-action-new-folder").exists()).toBe(false);
    expect(wrapper.find(".ws-action-toggle-hidden").exists()).toBe(false);
    expect(wrapper.find("[data-testid='toggle-hidden-files']").exists()).toBe(false);
  });

  it("shows the collapse button only when canCollapse is set", async () => {
    const without = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    expect(without.find("[data-testid='workspace-collapse']").exists()).toBe(false);

    const withFlag = mountPanelSync({ props: { userId: "u1", canCollapse: true } });
    await flushPromises();
    expect(withFlag.find("[data-testid='workspace-collapse']").exists()).toBe(true);
  });

  it("emits collapse when the collapse button is clicked", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1", canCollapse: true } });
    await flushPromises();
    await wrapper.find("[data-testid='workspace-collapse']").trigger("click");
    expect(wrapper.emitted("collapse")).toHaveLength(1);
  });

  it("ignores Enter pressed mid-IME-composition during rename", async () => {
    // IMEs commit a candidate with Enter. Without the composition guard,
    // that Enter would close the rename and drop the in-flight characters.
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 150,
      clientY: 200,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    const renameBtn = ctx.findAll(".ctx-item").find((b) => b.text() === "Rename");
    expect(renameBtn).toBeTruthy();
    await renameBtn!.trigger("click");
    await flushPromises();

    const input = wrapper.find(".file-card input");
    expect(input.exists()).toBe(true);

    await input.trigger("compositionstart");
    await input.setValue("文档");
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(mockRenameFile).not.toHaveBeenCalled();

    await input.trigger("compositionend");
    await input.trigger("keydown", { key: "Enter" });
    await flushPromises();
    expect(mockRenameFile).toHaveBeenCalledWith("u1", "README.md", "文档");
  });

  it("creates a new folder when the context menu's New Folder entry is clicked", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    // Stall mkdir so we exit the test before the post-mkdir auto-rename path runs
    // (it tries to focus an input that doesn't exist outside a real DOM render cycle).
    mockMkdir.mockImplementation(() => new Promise(() => {}));
    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 50,
      clientY: 50,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    const newFolder = ctx.findAll(".ctx-item").find((b) => b.text() === "New Folder");
    expect(newFolder).toBeTruthy();
    await newFolder!.trigger("click");
    expect(mockMkdir).toHaveBeenCalledWith("u1", "New Folder");
  });

  it("creates an empty file from the background context menu", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    mockWriteFile.mockImplementation(() => new Promise(() => {}));

    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 50,
      clientY: 50,
      preventDefault: vi.fn(),
    });
    const newFile = wrapper.findComponent({ name: "FileContextMenu" }).find(".ctx-item-new-file");
    await newFile.trigger("click");

    expect(mockWriteFile).toHaveBeenCalledWith("u1", "New File", "");
  });

  it("has home button", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    expect(wrapper.find(".home-btn").exists()).toBe(true);
  });

  it("opens context menu on right-click", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 150,
      clientY: 200,
      preventDefault: vi.fn(),
    });

    expect(wrapper.findComponent({ name: "FileContextMenu" }).exists()).toBe(true);
  });

  it("renders FileIcon components for each entry in the grid", async () => {
    // Tree, grid, preview header and properties dialog all share one
    // monochrome `FileIcon` — the hand-drawn `UIFileIcon` and the colored
    // Catppuccin sheet it competed with are both gone.
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    const icons = wrapper.findAllComponents({ name: "FileIcon" });
    expect(icons.length).toBe(2);
  });

  it("New Folder via context menu calls mkdir API", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Right-click on the ws-body (background)
    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 100,
      clientY: 100,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    expect(ctx.exists()).toBe(true);
    await ctx.findAll(".ctx-item")[1].trigger("click");
    await flushPromises();

    expect(mockMkdir).toHaveBeenCalledWith("u1", "New Folder");
  });

  // Keyboard shortcut tests
  it("Ctrl+C sets clipboard to copy mode", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Select the file entry
    await wrapper.findAll(".file-card")[1].trigger("click");

    // Press Ctrl+C
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "c", ctrlKey: true }));
    await flushPromises();

    // Now press Ctrl+V to verify clipboard was set
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "v", ctrlKey: true }));
    await flushPromises();

    expect(mockCopyFile).toHaveBeenCalledWith("u1", "README.md", ".");
  });

  it("Ctrl+C with an active text selection defers to the browser (chat/preview copy is not hijacked)", async () => {
    // Regression: the workspace global keydown used to call
    // preventDefault on Cmd/Ctrl+C whenever a file was selected,
    // breaking text-copy in chat messages and the file-preview popup
    // because both live on the same document.
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Pre-select a file in the panel (this is what used to trigger
    // the unwanted preventDefault).
    await wrapper.findAll(".file-card")[1].trigger("click");

    // Simulate a text selection elsewhere on the page. try/finally
    // so the spy is restored even if an expectation fails — without
    // it, `vi.clearAllMocks()` in beforeEach doesn't restore spies and
    // the leak breaks every later test that depends on real getSelection.
    const getSelectionSpy = vi.spyOn(window, "getSelection").mockReturnValue({
      toString: () => "selected chat text",
    } as unknown as Selection);
    try {
      const ev = new KeyboardEvent("keydown", { key: "c", ctrlKey: true, cancelable: true });
      document.dispatchEvent(ev);
      await flushPromises();

      // defaultPrevented stays false → workspace handler returned early
      // and the browser's native copy is free to run.
      expect(ev.defaultPrevented).toBe(false);
    } finally {
      getSelectionSpy.mockRestore();
    }
  });

  it("Ctrl+C is not intercepted while the file-preview modal is open", async () => {
    // Regression: even without a text selection, the workspace handler
    // used to copy the selected file path into its clipboard while the
    // preview modal was open — surprising the user who was reading the
    // preview, not browsing the file panel. The modal is reached via the
    // route-backed `?preview=` query (double-click now opens a new window).
    await sharedRouter.replace({ path: "/", query: { preview: "README.md" } });
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    try {
      // Select a file so that, absent the guard, Ctrl+C *would* copy its
      // path — that's exactly what the open preview must suppress.
      await wrapper.findAll(".file-card")[1].trigger("click");
      await flushPromises();

      const ev = new KeyboardEvent("keydown", { key: "c", ctrlKey: true, cancelable: true });
      document.dispatchEvent(ev);
      await flushPromises();

      // Workspace handler bailed → browser keeps its native copy behavior.
      expect(ev.defaultPrevented).toBe(false);
    } finally {
      // Close the modal via the registered Escape handler so the
      // teleported overlay doesn't leak into the next test (mounted
      // wrappers don't unmount themselves in this suite).
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      await flushPromises();
    }
  });

  it("Ctrl+X sets clipboard to cut mode", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Select the file entry
    await wrapper.findAll(".file-card")[1].trigger("click");

    // Press Ctrl+X
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "x", ctrlKey: true }));
    await flushPromises();

    // Now press Ctrl+V to verify clipboard was set with cut
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "v", ctrlKey: true }));
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledWith("u1", "README.md", ".", undefined);
  });

  it("paste-cut shows conflict dialog on 409 and overwrite retries with on_conflict=overwrite", async () => {
    mockMoveFile.mockRejectedValueOnce(Object.assign(new Error("move: 409"), { status: 409 }));
    mockMoveFile.mockResolvedValueOnce(undefined);
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("click");
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "x", ctrlKey: true }));
    await flushPromises();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "v", ctrlKey: true }));
    await flushPromises();

    const dialog = wrapper.findComponent({ name: "FileConflictDialog" });
    expect(dialog.exists()).toBe(true);
    expect(dialog.props("fileName")).toBe("README.md");

    await wrapper.find(".conflict-btn-overwrite").trigger("click");
    await flushPromises();

    expect(mockMoveFile).toHaveBeenLastCalledWith("u1", "README.md", ".", "overwrite");
    // Cut clipboard should be cleared after a successful retry
    expect(wrapper.findAll(".file-card")[1].classes()).not.toContain("is-cut");
  });

  it("cut entries shown with reduced opacity", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Select and cut the file entry
    await wrapper.findAll(".file-card")[1].trigger("click");
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "x", ctrlKey: true }));
    await flushPromises();

    expect(wrapper.findAll(".file-card")[1].classes()).toContain("is-cut");
  });

  it("cut + paste moves every entry in a multi-selection, not just one", async () => {
    // Regression: cutting a multi-selection then pasting used to move only the
    // first file, because the clipboard captured a single path. Now the whole
    // selection is carried through, so every selected entry moves.
    mockListDir.mockResolvedValue({
      path: ".",
      entries: [
        { name: "a.txt", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
        { name: "b.txt", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
      ],
    });
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Select a.txt, then primary-modifier-click b.txt to extend the selection.
    await wrapper.findAll(".file-card")[0].trigger("click");
    await wrapper.findAll(".file-card")[1].trigger("click", { ctrlKey: true });

    // Cut via the context menu opened on one of the selected entries — the
    // menu keeps the whole multi-selection.
    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 10,
      clientY: 10,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    const cutBtn = ctx.findAll(".ctx-item").find((b) => b.text().startsWith("Cut"));
    expect(cutBtn).toBeTruthy();
    await cutBtn!.trigger("click");
    await flushPromises();

    // Paste through this panel's own background menu rather than a global
    // ctrl+V keydown — other tests leave their panels mounted, and their
    // document-level keydown listeners would otherwise also fire on a
    // dispatched event and move their own (stale) clipboard entries.
    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 5,
      clientY: 5,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const bgCtx = wrapper.findComponent({ name: "FileContextMenu" });
    const pasteBtn = bgCtx.findAll(".ctx-item").find((b) => b.text().startsWith("Paste"));
    expect(pasteBtn).toBeTruthy();
    await pasteBtn!.trigger("click");
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledWith("u1", "a.txt", ".", undefined);
    expect(mockMoveFile).toHaveBeenCalledWith("u1", "b.txt", ".", undefined);
    expect(mockMoveFile).toHaveBeenCalledTimes(2);
  });

  it("drops the clipboard when the panel is rebound to another agent", async () => {
    // The cut paths are relative to the agent's work_dir. Pasting them after
    // an agent switch resolved them against a workspace the user never
    // browsed — silently overwriting a same-named file there, or 404ing on a
    // file they never touched.
    const wrapper = mountPanelSync({
      props: { userId: "u1", conversationId: "c1", initialPath: "." },
    });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 10,
      clientY: 10,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    const cutBtn = ctx.findAll(".ctx-item").find((b) => b.text().startsWith("Cut"));
    await cutBtn!.trigger("click");
    await flushPromises();

    await wrapper.setProps({ userId: "u2", conversationId: "c2", initialPath: "." });
    await flushPromises();

    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 5,
      clientY: 5,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const bgCtx = wrapper.findComponent({ name: "FileContextMenu" });
    expect(bgCtx.findAll(".ctx-item").find((b) => b.text().startsWith("Paste"))).toBeUndefined();
  });

  it("keeps the clipboard across a directory change within the same agent", async () => {
    // Cut here, navigate, paste there is the whole point of the clipboard —
    // the agent-switch reset must not take this with it.
    const wrapper = mountPanelSync({
      props: { userId: "u1", conversationId: "c1", initialPath: "." },
    });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 10,
      clientY: 10,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    await ctx
      .findAll(".ctx-item")
      .find((b) => b.text().startsWith("Cut"))!
      .trigger("click");
    await flushPromises();

    mockListDir.mockResolvedValueOnce({ path: "src", entries: [] });
    await wrapper.findAll(".file-card")[0].trigger("dblclick");
    await flushPromises();

    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 5,
      clientY: 5,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const bgCtx = wrapper.findComponent({ name: "FileContextMenu" });
    const pasteBtn = bgCtx.findAll(".ctx-item").find((b) => b.text().startsWith("Paste"));
    await pasteBtn!.trigger("click");
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledWith("u1", "README.md", "src", undefined);
  });

  it("closes the properties dialog when the conversation changes", async () => {
    const wrapper = mountPanelSync({
      props: { userId: "u1", conversationId: "c1", initialPath: "." },
    });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 10,
      clientY: 10,
      preventDefault: vi.fn(),
    });
    await flushPromises();
    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    await ctx
      .findAll(".ctx-item")
      .find((b) => b.text().startsWith("Properties"))!
      .trigger("click");
    await flushPromises();
    expect(wrapper.findComponent({ name: "FilePropertiesDialog" }).exists()).toBe(true);

    await wrapper.setProps({ conversationId: "c2", initialPath: "docs" });
    await flushPromises();

    expect(wrapper.findComponent({ name: "FilePropertiesDialog" }).exists()).toBe(false);
  });

  it("exposes refresh method that reloads current directory", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    mockListDir.mockClear();
    mockListDir.mockResolvedValue({ path: ".", entries: [] });

    (wrapper.vm as unknown as { refresh: () => void }).refresh();
    await flushPromises();

    expect(mockListDir).toHaveBeenCalledWith("u1", ".");
  });

  it("snaps back to initialPath when conversationId changes after the user navigated into a subfolder", async () => {
    // Bug: starting a new conversation that resolves to the same
    // initialPath as the previous one (both rooted at the user
    // work_dir) left the panel pointing at whatever subfolder the
    // user had clicked into during the previous session — the
    // initialPath prop value didn't change, so the path-only watcher
    // never fired. Wiring conversationId as a reset trigger fixes it.
    const wrapper = mountPanelSync({
      props: { userId: "u1", conversationId: "c1", initialPath: "." },
    });
    await flushPromises();
    // Initial mount listed "." once.
    expect(mockListDir).toHaveBeenCalledWith("u1", ".");

    // Simulate the user navigating into a subfolder.
    mockListDir.mockClear();
    mockListDir.mockResolvedValueOnce({
      path: "DayMug",
      entries: [{ name: "README.md", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" }],
    });
    const subFolder = wrapper.findAll(".file-card")[0]; // "src" — first entry from beforeEach
    await subFolder.trigger("dblclick");
    await flushPromises();

    // Now switch to a fresh conversation rooted at the same initialPath.
    mockListDir.mockClear();
    mockListDir.mockResolvedValue({ path: ".", entries: [] });
    await wrapper.setProps({ conversationId: "c2", initialPath: "." });
    await flushPromises();

    expect(mockListDir).toHaveBeenCalledWith("u1", ".");
  });

  it("does not refetch when conversationId changes but the panel is already at initialPath", async () => {
    // The reset trigger should be a no-op when there's nothing to reset
    // — otherwise every conversation switch would burn an extra listDir
    // call against the agent's root.
    const wrapper = mountPanelSync({
      props: { userId: "u1", conversationId: "c1", initialPath: "." },
    });
    await flushPromises();

    mockListDir.mockClear();
    await wrapper.setProps({ conversationId: "c2", initialPath: "." });
    await flushPromises();

    expect(mockListDir).not.toHaveBeenCalled();
  });

  it("reloads after an agent switch even when initialPath was '' during the transition", async () => {
    // Regression: switching from agentA to agentB whose conversation
    // resolves to the same target string ("." at root) used to leave
    // the panel showing agentA's files. The agent-switch tick had
    // initialPath="" (parent's "hold off" signal while the new conv's
    // work_dir was in flight), the early return there consumed the
    // userId change for `oldVals`-based detection, and the next tick
    // — once initialPath flipped to "." — saw oldVals[0] already at
    // agentB so userIdChanged was false; target === currentPath then
    // skipped the load entirely.
    const wrapper = mountPanelSync({
      props: { userId: "u1", conversationId: "c1", initialPath: "." },
    });
    await flushPromises();
    expect(mockListDir).toHaveBeenLastCalledWith("u1", ".");

    mockListDir.mockClear();
    // Step 1: parent emits agentB's userId synchronously, but
    // initialPath="" because conversationWorkDir hasn't been rebound
    // yet. The watcher must NOT consume the userId change here.
    await wrapper.setProps({ userId: "u2", conversationId: "c2", initialPath: "" });
    await flushPromises();
    expect(mockListDir).not.toHaveBeenCalled();

    // Step 2: conversationWorkDir lands, initialPath flips back to ".".
    await wrapper.setProps({ initialPath: "." });
    await flushPromises();
    expect(mockListDir).toHaveBeenCalledWith("u2", ".");
  });

  it("drag-over sets visual state class on ws-body", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const wsBody = wrapper.find(".ws-body");
    await wsBody.trigger("dragover", { preventDefault: vi.fn() });
    expect(wsBody.classes()).toContain("ring-2");
  });

  it("drop event calls uploadFiles", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const testFile = new File(["content"], "dropped.txt", { type: "text/plain" });
    const dataTransfer = {
      items: [],
      files: [testFile],
    };

    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    // Upload now flows through performUpload, which passes a progress callback
    // alongside the files so the bottom progress bar can render.
    expect(mockUploadFiles).toHaveBeenCalledWith(
      "u1",
      ".",
      [testFile],
      expect.objectContaining({ onProgress: expect.any(Function) }),
    );
  });

  it("rejects folder uploads with more than 10 files and surfaces a zip hint", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const tooMany = Array.from({ length: 11 }, (_, i) => {
      const f = new File(["x"], `bigfolder/file-${i}.txt`, { type: "text/plain" });
      // happy-dom drops webkitRelativePath when File is constructed manually;
      // the helper falls back to `name`, but set it anyway so this stays
      // representative of how the folder picker hands files to us.
      Object.defineProperty(f, "webkitRelativePath", {
        value: `bigfolder/file-${i}.txt`,
        configurable: true,
      });
      return f;
    });

    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer: { items: [], files: tooMany },
    });
    await flushPromises();

    expect(mockUploadFiles).not.toHaveBeenCalled();
    expect(wrapper.find(".ws-error").text()).toContain("zip");
  });

  it("renders the upload progress bar while an upload is in flight", async () => {
    // Wrap the resolver in an object so TS5's narrowing doesn't collapse the
    // captured-by-closure ref to `never` after assignment in the mock.
    const pending: { resolve: (() => void) | null } = { resolve: null };
    mockUploadFiles.mockImplementation(
      (
        _uid: string,
        _path: string,
        _files: File[],
        opts?: { onProgress?: (p: { loaded: number; total: number }) => void },
      ) => {
        opts?.onProgress?.({ loaded: 5, total: 10 });
        return new Promise<void>((resolve) => {
          pending.resolve = resolve;
        });
      },
    );
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const testFile = new File(["content"], "dropped.txt", { type: "text/plain" });
    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer: { items: [], files: [testFile] },
    });
    await flushPromises();

    const progress = wrapper.find("[data-testid='workspace-upload-progress']");
    expect(progress.exists()).toBe(true);
    expect(progress.text()).toContain("Uploading");

    pending.resolve?.();
    await flushPromises();
    expect(wrapper.find("[data-testid='workspace-upload-progress']").exists()).toBe(false);
  });

  it("cancel button aborts the in-flight upload, keeps completed files, skips queued files", async () => {
    // Each call resolves only after we trigger it; the queue is keyed by
    // file name so we can complete one and then cancel while the second
    // is still pending.
    type Pending = {
      resolve: () => void;
      reject: (e: unknown) => void;
      signal?: AbortSignal;
      file: File;
    };
    const pendings: Pending[] = [];
    mockUploadFiles.mockImplementation(
      (
        _uid: string,
        _path: string,
        files: File[],
        opts?: {
          onProgress?: (p: { loaded: number; total: number }) => void;
          signal?: AbortSignal;
        },
      ) => {
        opts?.onProgress?.({ loaded: files[0].size, total: files[0].size });
        return new Promise<void>((resolve, reject) => {
          const entry: Pending = { resolve, reject, signal: opts?.signal, file: files[0] };
          pendings.push(entry);
          opts?.signal?.addEventListener("abort", () => {
            reject(new DOMException("aborted", "AbortError"));
          });
        });
      },
    );
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const fileA = new File(["aaaa"], "a.txt", { type: "text/plain" });
    const fileB = new File(["bbbb"], "b.txt", { type: "text/plain" });
    const fileC = new File(["cccc"], "c.txt", { type: "text/plain" });
    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer: { items: [], files: [fileA, fileB, fileC] },
    });
    await flushPromises();

    // First file is in flight — complete it.
    expect(pendings.length).toBe(1);
    expect(pendings[0].file.name).toBe("a.txt");
    pendings[0].resolve();
    await flushPromises();

    // Second file is now in flight — cancel mid-flight.
    expect(pendings.length).toBe(2);
    expect(pendings[1].file.name).toBe("b.txt");
    await wrapper.find("[data-testid='workspace-upload-cancel']").trigger("click");
    await flushPromises();

    // Cancel must abort the current request and stop the loop before file C
    // ever uploads.
    expect(pendings.length).toBe(2);
    expect(mockUploadFiles).toHaveBeenCalledTimes(2);
    expect(wrapper.find("[data-testid='workspace-upload-progress']").exists()).toBe(false);
  });

  it("upload conflict: clicking overwrite retries with on_conflict=overwrite", async () => {
    let attempt = 0;
    mockUploadFiles.mockImplementation((_uid: string, _path: string, _files: File[], opts) => {
      attempt += 1;
      if (attempt === 1) {
        return Promise.reject(new TestUploadConflictError(["dropped.txt"]));
      }
      // Second attempt receives the user's chosen mode and succeeds.
      expect(opts?.onConflict).toBe("overwrite");
      return Promise.resolve();
    });

    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const testFile = new File(["content"], "dropped.txt", { type: "text/plain" });
    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer: { items: [], files: [testFile] },
    });
    await flushPromises();

    // First request failed with 409; the conflict dialog now waits for input.
    const dialog = wrapper.find(".conflict-overlay");
    expect(dialog.exists()).toBe(true);
    await dialog.find(".conflict-btn-overwrite").trigger("click");
    await flushPromises();

    expect(attempt).toBe(2);
    expect(wrapper.find(".conflict-overlay").exists()).toBe(false);
  });

  it("upload conflict: clicking cancel aborts the batch", async () => {
    mockUploadFiles.mockRejectedValue(new TestUploadConflictError(["a.txt"]));

    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const fileA = new File(["aaaa"], "a.txt", { type: "text/plain" });
    const fileB = new File(["bbbb"], "b.txt", { type: "text/plain" });
    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer: { items: [], files: [fileA, fileB] },
    });
    await flushPromises();

    expect(wrapper.find(".conflict-overlay").exists()).toBe(true);
    await wrapper.find(".conflict-btn-cancel").trigger("click");
    await flushPromises();

    // Only the first file ever hit the API; the cancel killed the batch.
    expect(mockUploadFiles).toHaveBeenCalledTimes(1);
    expect(wrapper.find(".conflict-overlay").exists()).toBe(false);
  });

  it("upload conflict: chosen mode persists across the rest of the batch", async () => {
    const seenConflictModes: Array<string | undefined> = [];
    let calls = 0;
    mockUploadFiles.mockImplementation((_uid: string, _path: string, _files: File[], opts) => {
      calls += 1;
      seenConflictModes.push(opts?.onConflict);
      // First attempt for each file: no on_conflict yet → 409.
      // Re-attempts (or subsequent files) carry the batch choice → succeed.
      if (!opts?.onConflict) {
        return Promise.reject(new TestUploadConflictError([`f${calls}.txt`]));
      }
      return Promise.resolve();
    });

    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const fileA = new File(["aaaa"], "a.txt", { type: "text/plain" });
    const fileB = new File(["bbbb"], "b.txt", { type: "text/plain" });
    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer: { items: [], files: [fileA, fileB] },
    });
    await flushPromises();

    // First conflict prompts; user picks rename.
    expect(wrapper.find(".conflict-overlay").exists()).toBe(true);
    await wrapper.find(".conflict-btn-rename").trigger("click");
    await flushPromises();

    // File B's request should carry the same "rename" mode from the start,
    // so no second dialog appears.
    expect(wrapper.find(".conflict-overlay").exists()).toBe(false);
    // Call timeline: A(no mode) -> A(rename) -> B(rename).
    expect(seenConflictModes).toEqual([undefined, "rename", "rename"]);
  });

  it("drag a file onto a folder calls moveFile with target folder path", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const folderCard = cards[0]; // "src" directory
    const fileCard = cards[1]; // "README.md"

    // Start drag on the file
    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await fileCard.trigger("dragstart", { dataTransfer });

    // Drop on the folder
    await folderCard.trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledWith("u1", "README.md", "src", undefined);
    expect(mockUploadFiles).not.toHaveBeenCalled();
  });

  it("scopes the drag cursor to the workspace and clears it after drop", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();
    const cards = wrapper.findAll(".file-card");
    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };

    await cards[1].trigger("dragstart", { dataTransfer });
    expect(wrapper.find(".workspace-panel").classes()).toContain("workspace-internal-drag");
    expect(document.documentElement.style.cursor).toBe("");

    await cards[0].trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(wrapper.find(".workspace-panel").classes()).not.toContain("workspace-internal-drag");
    expect(document.documentElement.style.cursor).toBe("");
  });

  it("dragging a multi-selection onto a folder moves every selected entry", async () => {
    mockListDir.mockResolvedValue({
      path: ".",
      entries: [
        { name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" },
        { name: "README.md", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
        { name: "notes.txt", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
      ],
    });
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const folderCard = cards.find((card) => card.attributes("data-entry-name") === "src");
    const readmeCard = cards.find((card) => card.attributes("data-entry-name") === "README.md");
    const notesCard = cards.find((card) => card.attributes("data-entry-name") === "notes.txt");
    expect(folderCard).toBeTruthy();
    expect(readmeCard).toBeTruthy();
    expect(notesCard).toBeTruthy();

    await readmeCard!.trigger("click");
    await notesCard!.trigger("click", { ctrlKey: true });

    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await readmeCard!.trigger("dragstart", { dataTransfer });
    await folderCard!.trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(mockMoveFile).toHaveBeenNthCalledWith(1, "u1", "README.md", "src", undefined);
    expect(mockMoveFile).toHaveBeenNthCalledWith(2, "u1", "notes.txt", "src", undefined);
  });

  it("drag a file inside a subfolder uses full source path", async () => {
    mockListDir.mockResolvedValueOnce({
      path: "docs",
      entries: [
        { name: "images", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" },
        { name: "guide.md", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
      ],
    });
    const wrapper = mountPanelSync({
      props: { userId: "u1", initialPath: "docs" },
    });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const folderCard = cards[0]; // "images"
    const fileCard = cards[1]; // "guide.md"

    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await fileCard.trigger("dragstart", { dataTransfer });
    await folderCard.trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledWith("u1", "docs/guide.md", "docs/images", undefined);
  });

  it("dropping a folder onto itself is a no-op", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const folderCard = wrapper.findAll(".file-card")[0]; // "src"

    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await folderCard.trigger("dragstart", { dataTransfer });
    await folderCard.trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(mockMoveFile).not.toHaveBeenCalled();
    expect(mockUploadFiles).not.toHaveBeenCalled();
  });

  it("shows conflict dialog when move returns 409", async () => {
    mockMoveFile.mockRejectedValueOnce(Object.assign(new Error("move: 409"), { status: 409 }));
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await cards[1].trigger("dragstart", { dataTransfer });
    await cards[0].trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    const dialog = wrapper.findComponent({ name: "FileConflictDialog" });
    expect(dialog.exists()).toBe(true);
    expect(dialog.props("fileName")).toBe("README.md");
    expect(dialog.props("targetFolder")).toBe("src");
  });

  it("conflict dialog 'overwrite' retries move with on_conflict=overwrite", async () => {
    mockMoveFile.mockRejectedValueOnce(Object.assign(new Error("move: 409"), { status: 409 }));
    mockMoveFile.mockResolvedValueOnce(undefined);
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await cards[1].trigger("dragstart", { dataTransfer });
    await cards[0].trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    await wrapper.find(".conflict-btn-overwrite").trigger("click");
    await flushPromises();

    expect(mockMoveFile).toHaveBeenLastCalledWith("u1", "README.md", "src", "overwrite");
    expect(wrapper.findComponent({ name: "FileConflictDialog" }).exists()).toBe(false);
  });

  it("conflict dialog 'rename' retries move with on_conflict=rename", async () => {
    mockMoveFile.mockRejectedValueOnce(Object.assign(new Error("move: 409"), { status: 409 }));
    mockMoveFile.mockResolvedValueOnce(undefined);
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await cards[1].trigger("dragstart", { dataTransfer });
    await cards[0].trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    await wrapper.find(".conflict-btn-rename").trigger("click");
    await flushPromises();

    expect(mockMoveFile).toHaveBeenLastCalledWith("u1", "README.md", "src", "rename");
  });

  it("conflict dialog 'cancel' closes without retrying move", async () => {
    mockMoveFile.mockRejectedValueOnce(Object.assign(new Error("move: 409"), { status: 409 }));
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const cards = wrapper.findAll(".file-card");
    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await cards[1].trigger("dragstart", { dataTransfer });
    await cards[0].trigger("drop", {
      preventDefault: vi.fn(),
      stopPropagation: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledTimes(1);
    await wrapper.find(".conflict-btn-cancel").trigger("click");
    await flushPromises();

    expect(mockMoveFile).toHaveBeenCalledTimes(1);
    expect(wrapper.findComponent({ name: "FileConflictDialog" }).exists()).toBe(false);
  });

  it("double-clicking the same file opens a fresh preview tab each time", async () => {
    await sharedRouter.replace({ path: "/", query: {} });
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      await wrapper.findAll(".file-card")[1].trigger("dblclick"); // README.md
      await wrapper.findAll(".file-card")[1].trigger("dblclick"); // README.md
      await flushPromises();

      expect(openSpy).toHaveBeenCalledTimes(2);
      const href = openSpy.mock.calls[0][0] as string;
      expect(href).toContain("/file/u1");
      expect(href).toContain("path=README.md");
      expect(href).toContain("edit=false");
      expect(openSpy.mock.calls[0][1]).toBe("_blank");
      expect(openSpy.mock.calls[1][1]).toBe("_blank");
      // Same-window navigation must NOT happen — no preview query is pushed.
      expect(sharedRouter.currentRoute.value.query.preview).toBeUndefined();
    } finally {
      openSpy.mockRestore();
    }
  });

  it("double-clicking a file can navigate the current tab", async () => {
    await sharedRouter.replace({ path: "/", query: {} });
    const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "same-tab" } });
    await flushPromises();

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      await wrapper.findAll(".file-card")[1].trigger("dblclick"); // README.md
      await flushPromises();

      expect(openSpy).not.toHaveBeenCalled();
      expect(sharedRouter.currentRoute.value.name).toBe("file");
      expect(sharedRouter.currentRoute.value.params.userId).toBe("u1");
      expect(sharedRouter.currentRoute.value.query.path).toBe("README.md");
      expect(sharedRouter.currentRoute.value.query.edit).toBe("false");
    } finally {
      openSpy.mockRestore();
    }
  });

  it("modifier double-click opens a new tab even when files open in the current tab", async () => {
    await sharedRouter.replace({ path: "/", query: {} });
    const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "same-tab" } });
    await flushPromises();

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      // Both modifiers so the assertion holds on either host platform.
      await wrapper.findAll(".file-card")[1].trigger("dblclick", { ctrlKey: true, metaKey: true }); // README.md
      await flushPromises();

      expect(openSpy).toHaveBeenCalledTimes(1);
      expect(openSpy.mock.calls[0][0]).toContain("path=README.md");
      expect(openSpy.mock.calls[0][1]).toBe("_blank");
      expect(sharedRouter.currentRoute.value.name).not.toBe("file");
    } finally {
      openSpy.mockRestore();
    }
  });

  it("keeps an Open in new tab menu item when files open in the current tab", async () => {
    await sharedRouter.replace({ path: "/", query: {} });
    const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "same-tab" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 150,
      clientY: 200,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      const item = ctx.find(".ctx-item-open-new-tab");
      expect(item.exists()).toBe(true);

      await item.trigger("click");
      await flushPromises();

      expect(openSpy).toHaveBeenCalledTimes(1);
      const href = openSpy.mock.calls[0][0] as string;
      expect(href).toContain("/file/u1");
      expect(href).toContain("path=README.md");
      expect(href).toContain("edit=false");
      expect(openSpy.mock.calls[0][1]).toBe("_blank");
    } finally {
      openSpy.mockRestore();
    }
  });

  it("opens the fullscreen preview when ?preview=<path> is already in the URL", async () => {
    await sharedRouter.replace({ path: "/", query: { preview: "README.md" } });
    mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // The fullscreen overlay teleports to body; assert the preview path is read
    expect(document.body.querySelector(".fixed.inset-0.z-\\[999\\]")).not.toBeNull();
  });

  it("dropping an internal drag on the background is a no-op (no upload)", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    const fileCard = wrapper.findAll(".file-card")[1]; // README.md

    const dataTransfer = {
      effectAllowed: "",
      dropEffect: "",
      setData: vi.fn(),
      items: [],
      files: [],
    };
    await fileCard.trigger("dragstart", { dataTransfer });
    await wrapper.find(".ws-body").trigger("drop", {
      preventDefault: vi.fn(),
      dataTransfer,
    });
    await flushPromises();

    expect(mockMoveFile).not.toHaveBeenCalled();
    expect(mockUploadFiles).not.toHaveBeenCalled();
  });

  it("Copy name menu item writes the entry's bare name to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    // navigator.clipboard is read-only in happy-dom; defineProperty bypasses
    // the getter so the test can stub the API surface.
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[1].trigger("contextmenu", {
      clientX: 150,
      clientY: 200,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    const item = ctx.find(".ctx-item-copy-name");
    expect(item.exists()).toBe(true);
    await item.trigger("click");
    await flushPromises();

    expect(writeText).toHaveBeenCalledWith("README.md");
  });

  it("Copy path menu item writes a relative path (no host absolute path) to the clipboard", async () => {
    // The path must be relative to the user's work_dir so the value is
    // safe to paste into agent chats — the workspace API never exposes
    // host absolute paths to non-admins.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    mockListDir.mockResolvedValueOnce({
      path: "src",
      entries: [{ name: "main.go", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" }],
    });
    const wrapper = mountPanelSync({ props: { userId: "u1", initialPath: "src" } });
    await flushPromises();

    await wrapper.findAll(".file-card")[0].trigger("contextmenu", {
      clientX: 150,
      clientY: 200,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    await ctx.find(".ctx-item-copy-path").trigger("click");
    await flushPromises();

    expect(writeText).toHaveBeenCalledWith("src/main.go");
    const arg = writeText.mock.calls[0][0] as string;
    expect(arg.startsWith("/")).toBe(false);
  });

  it("Copy path rebases to the conversation work_dir when the conversation is rooted in a subdirectory", async () => {
    // The agent's work_dir browses /home/alice/proj; the conversation is
    // rooted at /home/alice/proj/web. A file at /home/alice/proj/web/src/main.go
    // should land in the clipboard as src/main.go — not web/src/main.go —
    // since that's what claude resolves from its CWD.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });

    mockListDir.mockResolvedValueOnce({
      path: "web/src",
      entries: [{ name: "main.go", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" }],
    });
    const wrapper = mountPanelSync({
      props: {
        userId: "u1",
        initialPath: "web/src",
        userWorkDir: "/home/alice/proj",
        conversationWorkDir: "/home/alice/proj/web",
      },
    });
    await flushPromises();

    await wrapper.findAll(".file-card")[0].trigger("contextmenu", {
      clientX: 150,
      clientY: 200,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    await ctx.find(".ctx-item-copy-path").trigger("click");
    await flushPromises();

    expect(writeText).toHaveBeenCalledWith("src/main.go");
  });

  it("background context menu shows New Folder", async () => {
    const wrapper = mountPanelSync({ props: { userId: "u1" } });
    await flushPromises();

    // Right-click on the ws-body (background)
    await wrapper.find(".ws-body").trigger("contextmenu", {
      clientX: 100,
      clientY: 100,
      preventDefault: vi.fn(),
    });
    await flushPromises();

    const ctx = wrapper.findComponent({ name: "FileContextMenu" });
    expect(ctx.exists()).toBe(true);
    expect(ctx.props("entry")).toBeNull();
  });

  describe("hidden-files toggle", () => {
    beforeEach(() => {
      // Wipe the UI prefs key between tests so default state is consistent.
      try {
        localStorage.removeItem("daymug-ui-prefs-v1");
      } catch {
        // ignore
      }
      mockListDir.mockResolvedValue({
        path: ".",
        entries: [
          { name: ".env", is_dir: false, size: 12, modified: "2024-01-01T00:00:00Z" },
          { name: ".git", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" },
          { name: "README.md", is_dir: false, size: 1024, modified: "2024-01-01T00:00:00Z" },
        ],
      });
    });

    it("hides dot-files by default", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();
      const names = wrapper.findAll(".file-name").map((w) => w.text());
      expect(names).toEqual(["README.md"]);
    });

    it("clicking 'Show hidden files' in the context menu reveals dot-files and persists", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      await wrapper.find(".ws-body").trigger("contextmenu", {
        clientX: 50,
        clientY: 50,
        preventDefault: vi.fn(),
      });
      await flushPromises();
      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      const toggle = ctx.find(".ctx-item-toggle-hidden");
      expect(toggle.exists()).toBe(true);
      expect(toggle.text()).toBe("Show hidden files");
      await toggle.trigger("click");
      await flushPromises();

      const names = wrapper.findAll(".file-name").map((w) => w.text());
      // Dirs first, then files alpha-sorted within each group.
      expect(names).toEqual([".git", ".env", "README.md"]);
      // Preference is persisted under the shared UI prefs key.
      const stored = JSON.parse(localStorage.getItem("daymug-ui-prefs-v1") || "{}");
      expect(stored.showHiddenFiles).toBe(true);
    });

    it("the context-menu toggle reflects the current showHiddenFiles state", async () => {
      // When dot-files are already visible, the menu entry inverts so the
      // user knows the next click hides them again.
      localStorage.setItem("daymug-ui-prefs-v1", JSON.stringify({ showHiddenFiles: true }));
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      await wrapper.find(".ws-body").trigger("contextmenu", {
        clientX: 50,
        clientY: 50,
        preventDefault: vi.fn(),
      });
      await flushPromises();
      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      expect(ctx.find(".ctx-item-toggle-hidden").text()).toBe("Hide hidden files");
    });

    it("restores the toggle state from localStorage on mount", async () => {
      localStorage.setItem("daymug-ui-prefs-v1", JSON.stringify({ showHiddenFiles: true }));
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();
      const names = wrapper.findAll(".file-name").map((w) => w.text());
      expect(names).toContain(".env");
      expect(names).toContain(".git");
    });
  });

  describe("multi-select & compress", () => {
    it("Ctrl+click adds an entry to the selection without dropping the first", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      // Plain click selects the first card, then Ctrl+click on the second
      // should keep both highlighted — the additive multi-select that
      // backs the new "Compress to ZIP" action.
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });

      expect(wrapper.findAll(".file-card")[0].classes()).toContain("selected");
      expect(wrapper.findAll(".file-card")[1].classes()).toContain("selected");
    });

    it("Ctrl+click again on a selected entry toggles it back off", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });
      await cards[1].trigger("click", { ctrlKey: true });

      expect(wrapper.findAll(".file-card")[0].classes()).toContain("selected");
      expect(wrapper.findAll(".file-card")[1].classes()).not.toContain("selected");
    });

    it("compress menu item POSTs every selected path to the backend", async () => {
      mockCompressToZip.mockResolvedValue({ path: "Archive.zip", name: "Archive.zip" });
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });
      // Right-click on one of the selected cards: the menu should treat
      // the whole set as its target rather than collapsing to just it.
      await cards[1].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();

      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      const compressBtn = ctx.find(".ctx-item-compress");
      expect(compressBtn.exists()).toBe(true);
      // Label reflects the selection size — gives the user a hint about
      // which items the action will actually pack.
      expect(compressBtn.text()).toBe("Compress 2 items to ZIP");
      await compressBtn.trigger("click");
      await flushPromises();

      expect(mockCompressToZip).toHaveBeenCalledTimes(1);
      const [userId, paths, targetDir] = mockCompressToZip.mock.calls[0];
      expect(userId).toBe("u1");
      // Order isn't guaranteed by Set iteration; assert by membership.
      expect(new Set(paths)).toEqual(new Set(["src", "README.md"]));
      expect(targetDir).toBe(".");
    });

    it("download menu item zips every selected path", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });
      await cards[1].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();

      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      const downloadBtn = ctx
        .findAll("button")
        .find((button) => button.text() === "Download as ZIP");
      expect(downloadBtn).toBeTruthy();
      mockDownloadFileUrl.mockClear();
      mockDownloadZipUrl.mockClear();
      await downloadBtn!.trigger("click");

      expect(mockDownloadZipUrl).toHaveBeenCalledTimes(1);
      const [userId, paths] = mockDownloadZipUrl.mock.calls[0];
      expect(userId).toBe("u1");
      expect(new Set(paths)).toEqual(new Set(["src", "README.md"]));
      expect(mockDownloadFileUrl).not.toHaveBeenCalled();
    });

    it("right-clicking outside the multi-select collapses to that single entry", async () => {
      mockCompressToZip.mockResolvedValue({ path: "src.zip", name: "src.zip" });
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      // Build a 2-item selection on entries 0 and 1...
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });
      // ...then right-click on entry 0 (which IS in the selection: keep
      // the set). Compress should target both.
      await cards[0].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();
      let ctx = wrapper.findComponent({ name: "FileContextMenu" });
      expect(ctx.find(".ctx-item-compress").text()).toMatch(/2 items/);

      // Now collapse: with only entry 1 selected, right-click on entry 0
      // (not in selection) must replace the selection. The compress label
      // drops back to the single-item variant.
      await cards[1].trigger("click");
      await cards[0].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();
      ctx = wrapper.findComponent({ name: "FileContextMenu" });
      expect(ctx.find(".ctx-item-compress").text()).toBe("Compress to ZIP");
    });

    it("dragging a marquee over files selects every intersecting entry", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const body = wrapper.find(".ws-body");
      Object.defineProperty(body.element, "getBoundingClientRect", {
        value: () => ({ left: 0, top: 0, right: 300, bottom: 300, width: 300, height: 300 }),
        configurable: true,
      });
      wrapper.findAll(".file-card").forEach((card, index) => {
        const left = 10 + index * 80;
        Object.defineProperty(card.element, "getBoundingClientRect", {
          value: () => ({
            left,
            top: 10,
            right: left + 60,
            bottom: 70,
            width: 60,
            height: 60,
          }),
          configurable: true,
        });
      });

      await body.trigger("pointerdown", {
        button: 0,
        pointerId: 1,
        pointerType: "mouse",
        clientX: 5,
        clientY: 5,
      });
      await body.trigger("pointermove", {
        pointerId: 1,
        pointerType: "mouse",
        clientX: 155,
        clientY: 75,
      });
      await body.trigger("pointerup", {
        pointerId: 1,
        pointerType: "mouse",
        clientX: 155,
        clientY: 75,
      });

      const cards = wrapper.findAll(".file-card");
      expect(cards[0].classes()).toContain("selected");
      expect(cards[1].classes()).toContain("selected");
    });
  });

  describe("delete confirmation", () => {
    // The delete flow routes through the app-wide confirm() dialog (mocked
    // above). Each test sets its own resolved value.
    const confirmSpy = mockConfirm;

    beforeEach(() => {
      confirmSpy.mockReset();
      confirmSpy.mockResolvedValue(true);
    });

    async function openCtxMenuFor(wrapper: ReturnType<typeof mountPanelSync>, index: number) {
      await wrapper.findAll(".file-card")[index].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();
      return wrapper.findComponent({ name: "FileContextMenu" });
    }

    it("plain delete click prompts the user and aborts when they cancel", async () => {
      confirmSpy.mockResolvedValue(false);
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const ctx = await openCtxMenuFor(wrapper, 1); // README.md
      await ctx.find(".ctx-item-delete").trigger("click");
      await flushPromises();

      // Confirmation must have fired with the file name in the message …
      expect(confirmSpy).toHaveBeenCalledTimes(1);
      expect(confirmSpy.mock.calls[0][0].message).toContain("README.md");
      // … and a "Cancel" answer must abort the API call entirely.
      expect(mockDeleteFile).not.toHaveBeenCalled();
    });

    it("plain delete click proceeds when the user accepts the prompt", async () => {
      confirmSpy.mockResolvedValue(true);
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const ctx = await openCtxMenuFor(wrapper, 1); // README.md
      await ctx.find(".ctx-item-delete").trigger("click");
      await flushPromises();

      expect(confirmSpy).toHaveBeenCalledTimes(1);
      expect(mockDeleteFile).toHaveBeenCalledWith("u1", "README.md");
    });

    it("plain delete click removes every selected entry when the menu opens inside the selection", async () => {
      confirmSpy.mockResolvedValue(true);
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });
      await cards[1].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();

      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      await ctx.find(".ctx-item-delete").trigger("click");
      await flushPromises();

      expect(confirmSpy).toHaveBeenCalledTimes(1);
      expect(confirmSpy.mock.calls[0][0].message).toContain("2 selected items");
      expect(mockDeleteFile).toHaveBeenCalledTimes(2);
      expect(mockDeleteFile).toHaveBeenCalledWith("u1", "src");
      expect(mockDeleteFile).toHaveBeenCalledWith("u1", "README.md");
    });

    it("plain multi-delete aborts every selected entry when the user cancels", async () => {
      confirmSpy.mockResolvedValue(false);
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });
      await cards[0].trigger("contextmenu", {
        clientX: 10,
        clientY: 10,
        preventDefault: vi.fn(),
      });
      await flushPromises();

      const ctx = wrapper.findComponent({ name: "FileContextMenu" });
      await ctx.find(".ctx-item-delete").trigger("click");
      await flushPromises();

      expect(confirmSpy).toHaveBeenCalledTimes(1);
      expect(mockDeleteFile).not.toHaveBeenCalled();
    });

    it("primary-modifier-clicking delete bypasses the confirmation prompt", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const ctx = await openCtxMenuFor(wrapper, 1); // README.md
      await ctx.find(".ctx-item-delete").trigger("click", { ctrlKey: true });
      await flushPromises();

      // No prompt, but the delete still goes through.
      expect(confirmSpy).not.toHaveBeenCalled();
      expect(mockDeleteFile).toHaveBeenCalledWith("u1", "README.md");
    });
  });

  describe("drag onto top bar moves to parent directory", () => {
    it("dropping on the top bar moves the entry up one level", async () => {
      // Mount inside `docs/` so there is a parent (`.`) to move into.
      mockListDir.mockResolvedValueOnce({
        path: "docs",
        entries: [{ name: "guide.md", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" }],
      });
      const wrapper = mountPanelSync({ props: { userId: "u1", initialPath: "docs" } });
      await flushPromises();

      const fileCard = wrapper.findAll(".file-card")[0];
      const dataTransfer = {
        effectAllowed: "",
        dropEffect: "",
        setData: vi.fn(),
        items: [],
        files: [],
      };
      await fileCard.trigger("dragstart", { dataTransfer });

      const topbar = wrapper.find("[data-testid='workspace-topbar']");
      // dragover establishes the drop target + flips on the visual hint.
      await topbar.trigger("dragover", {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        dataTransfer,
      });
      expect(
        wrapper.find("[data-testid='workspace-topbar-hint']").exists(),
        "hint must appear during a drag-over at non-root",
      ).toBe(true);

      await topbar.trigger("drop", {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        dataTransfer,
      });
      await flushPromises();

      // Target is the parent directory — "." for an entry one level deep.
      expect(mockMoveFile).toHaveBeenCalledWith("u1", "docs/guide.md", ".", undefined);
    });

    it("nested parent: dropping moves to the immediate parent path, not all the way up", async () => {
      mockListDir.mockResolvedValueOnce({
        path: "a/b",
        entries: [{ name: "x.txt", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" }],
      });
      const wrapper = mountPanelSync({ props: { userId: "u1", initialPath: "a/b" } });
      await flushPromises();

      const fileCard = wrapper.findAll(".file-card")[0];
      const dataTransfer = {
        effectAllowed: "",
        dropEffect: "",
        setData: vi.fn(),
        items: [],
        files: [],
      };
      await fileCard.trigger("dragstart", { dataTransfer });

      await wrapper.find("[data-testid='workspace-topbar']").trigger("drop", {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        dataTransfer,
      });
      await flushPromises();

      expect(mockMoveFile).toHaveBeenCalledWith("u1", "a/b/x.txt", "a", undefined);
    });

    it("dropping a multi-selection on the top bar moves every selected entry up", async () => {
      mockListDir.mockResolvedValue({
        path: "docs",
        entries: [
          { name: "a.md", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
          { name: "b.md", is_dir: false, size: 1, modified: "2024-01-01T00:00:00Z" },
        ],
      });
      const wrapper = mountPanelSync({ props: { userId: "u1", initialPath: "docs" } });
      await flushPromises();

      const cards = wrapper.findAll(".file-card");
      await cards[0].trigger("click");
      await cards[1].trigger("click", { ctrlKey: true });

      const dataTransfer = {
        effectAllowed: "",
        dropEffect: "",
        setData: vi.fn(),
        items: [],
        files: [],
      };
      await cards[0].trigger("dragstart", { dataTransfer });
      await wrapper.find("[data-testid='workspace-topbar']").trigger("drop", {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        dataTransfer,
      });
      await flushPromises();

      expect(mockMoveFile).toHaveBeenNthCalledWith(1, "u1", "docs/a.md", ".", undefined);
      expect(mockMoveFile).toHaveBeenNthCalledWith(2, "u1", "docs/b.md", ".", undefined);
    });

    it("at the workspace root the top bar is not a drop target", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const fileCard = wrapper.findAll(".file-card")[1]; // README.md
      const dataTransfer = {
        effectAllowed: "",
        dropEffect: "",
        setData: vi.fn(),
        items: [],
        files: [],
      };
      await fileCard.trigger("dragstart", { dataTransfer });

      const topbar = wrapper.find("[data-testid='workspace-topbar']");
      await topbar.trigger("dragover", {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        dataTransfer,
      });
      // At root there's nowhere to move to, so the hint must stay hidden …
      expect(wrapper.find("[data-testid='workspace-topbar-hint']").exists()).toBe(false);

      await topbar.trigger("drop", {
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
        dataTransfer,
      });
      await flushPromises();
      // … and a drop must not call moveFile.
      expect(mockMoveFile).not.toHaveBeenCalled();
    });
  });

  describe("inline file-open target", () => {
    // The chat page turns its own column into a workbench, so the panel hands
    // the path up instead of navigating or opening a window.
    it("emits open-file instead of routing", async () => {
      await sharedRouter.replace({ path: "/", query: {} });
      const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "inline" } });
      await flushPromises();

      const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
      try {
        await wrapper.findAll(".file-card")[1].trigger("dblclick");
        await flushPromises();

        expect(wrapper.emitted("open-file")).toEqual([["README.md"]]);
        expect(openSpy).not.toHaveBeenCalled();
        expect(sharedRouter.currentRoute.value.name).toBe("home");
      } finally {
        openSpy.mockRestore();
      }
    });

    it("still escapes to a new tab on Cmd/Ctrl double-click", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "inline" } });
      await flushPromises();

      const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
      try {
        await wrapper.findAll(".file-card")[1].trigger("dblclick", { ctrlKey: true });
        await flushPromises();

        expect(openSpy).toHaveBeenCalled();
        expect(wrapper.emitted("open-file")).toBeUndefined();
      } finally {
        openSpy.mockRestore();
      }
    });

    it("does not render the fullscreen preview overlay", async () => {
      // A modal on top of the inline workbench would duplicate and fight it.
      await sharedRouter.replace({ path: "/", query: { preview: "README.md" } });
      const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "inline" } });
      await flushPromises();

      expect(wrapper.findComponent({ name: "WorkspacePreview" }).exists()).toBe(false);
      await sharedRouter.replace({ path: "/", query: {} });
    });

    it("offers Open in new tab in the context menu", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1", fileOpenTarget: "inline" } });
      await flushPromises();
      await wrapper.findAll(".file-card")[1].trigger("contextmenu", { clientX: 10, clientY: 10 });

      const menu = wrapper.findComponent({ name: "FileContextMenu" });
      expect(menu.props("showOpenInNewTab")).toBe(true);
    });

    it("keeps routing to a new tab when the target is omitted", async () => {
      // Regression guard: the default behaviour must not change.
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();

      const openSpy = vi.spyOn(window, "open").mockReturnValue(null);
      try {
        await wrapper.findAll(".file-card")[1].trigger("dblclick");
        await flushPromises();

        expect(openSpy).toHaveBeenCalled();
        expect(wrapper.emitted("open-file")).toBeUndefined();
      } finally {
        openSpy.mockRestore();
      }
    });

    it("hides Open in new tab when the panel already opens files in a new tab", async () => {
      const wrapper = mountPanelSync({ props: { userId: "u1" } });
      await flushPromises();
      await wrapper.findAll(".file-card")[1].trigger("contextmenu", { clientX: 10, clientY: 10 });

      const menu = wrapper.findComponent({ name: "FileContextMenu" });
      expect(menu.props("showOpenInNewTab")).toBe(false);
    });
  });

  describe("artifact panel target", () => {
    beforeEach(() => {
      mockListDir.mockResolvedValue({
        path: ".",
        entries: [
          { name: "summary.md", is_dir: false, size: 1200, modified: "2026-09-22T00:00:00Z" },
          { name: "sales.xlsx", is_dir: false, size: 2400, modified: "2026-09-22T00:00:00Z" },
          { name: "review.pptx", is_dir: false, size: 3600, modified: "2026-09-22T00:00:00Z" },
        ],
      });
    });

    it("opens files as tabs without emitting the legacy inline event", async () => {
      const wrapper = mountPanelSync({
        props: { userId: "u1", conversationId: "c1", fileOpenTarget: "panel" },
      });
      await flushPromises();

      expect(wrapper.findAll(".file-row")).toHaveLength(3);
      await wrapper.get('.file-row[data-entry-name="summary.md"]').trigger("dblclick");
      await flushPromises();

      expect(wrapper.findComponent({ name: "FileWorkbench" }).props("path")).toBe("summary.md");
      expect(wrapper.find('[data-testid="artifact-tab"]').text()).toContain("summary.md");
      expect(wrapper.find(".ws-body").exists()).toBe(false);
      expect(wrapper.emitted("open-file")).toBeUndefined();
    });

    it("opens a file with one click like the reference artifact browser", async () => {
      const wrapper = mountPanelSync({
        props: { userId: "u1", conversationId: "c1", fileOpenTarget: "panel" },
      });
      await flushPromises();

      await wrapper.get('.file-row[data-entry-name="sales.xlsx"]').trigger("click");
      await flushPromises();

      expect(wrapper.findComponent({ name: "FileWorkbench" }).props("path")).toBe("sales.xlsx");
      expect(wrapper.find('[data-testid="artifact-tab"] .lucide-file-spreadsheet').exists()).toBe(
        true,
      );
    });

    it("keeps multiple extension-specific tabs and returns to the directory", async () => {
      const wrapper = mountPanelSync({
        props: { userId: "u1", conversationId: "c1", fileOpenTarget: "panel" },
      });
      await flushPromises();

      await wrapper.get('.file-row[data-entry-name="summary.md"]').trigger("dblclick");
      await flushPromises();
      await wrapper.get('[data-testid="artifact-directory-toggle"]').trigger("click");
      await wrapper.get('.file-row[data-entry-name="review.pptx"]').trigger("dblclick");
      await flushPromises();

      const tabs = wrapper.findAll('[data-testid="artifact-tab"]');
      expect(tabs).toHaveLength(2);
      expect(tabs[0].find(".lucide-file-text").exists()).toBe(true);
      expect(tabs[1].find(".lucide-presentation").exists()).toBe(true);

      await wrapper.get('[data-testid="artifact-directory-toggle"]').trigger("click");
      expect(wrapper.find(".ws-body").exists()).toBe(true);
      expect(wrapper.find('[data-testid="workspace-topbar"]').exists()).toBe(true);
    });

    it("emits fullscreen changes from the artifact toolbar", async () => {
      const wrapper = mountPanelSync({
        props: { userId: "u1", conversationId: "c1", fileOpenTarget: "panel" },
      });
      await flushPromises();

      await wrapper.get('[data-testid="artifact-fullscreen-toggle"]').trigger("click");
      await wrapper.get('[data-testid="artifact-fullscreen-toggle"]').trigger("click");
      expect(wrapper.emitted("fullscreen-change")).toEqual([[true], [false]]);
    });
  });
});
