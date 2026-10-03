import { describe, it, expect } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import FileContextMenu from "./FileContextMenu.vue";

const fileEntry = { name: "test.txt", is_dir: false, size: 1024, modified: "2024-01-01T00:00:00Z" };
const dirEntry = { name: "src", is_dir: true, size: 0, modified: "2024-01-01T00:00:00Z" };

function mountMenu(overrides: Record<string, unknown> = {}) {
  return mount(FileContextMenu, {
    props: {
      entry: fileEntry,
      entryPath: "test.txt",
      x: 100,
      y: 100,
      hasClipboard: false,
      ...overrides,
    },
  });
}

describe("FileContextMenu", () => {
  it("renders all menu items for a file entry", () => {
    const wrapper = mountMenu();
    const items = wrapper.findAll(".ctx-item");
    // Open, Cut, Copy (▸ File / Name / Path live inside the submenu and
    // don't carry the .ctx-item class), Rename, Delete, Download,
    // Compress to ZIP, Properties. The file/folder clipboard copy folded
    // into the "Copy ▸" submenu, so there is no separate top-level Copy
    // row anymore. The dedicated "Open in new tab" row is optional and
    // hidden by default.
    expect(items.length).toBe(8);
    expect(items[0].text()).toContain("Open");
    expect(items[1].text()).toContain("Cut");
    expect(items[2].text()).toContain("Copy");
    expect(items[3].text()).toContain("Rename");
    expect(items[4].text()).toContain("Delete");
    expect(items[5].text()).toContain("Download");
    expect(items[6].text()).toBe("Compress to ZIP");
    expect(items[7].text()).toContain("Properties");
  });

  it("renders 8 menu items for a directory entry (no preview)", () => {
    const wrapper = mountMenu({ entry: dirEntry, entryPath: "src" });
    const items = wrapper.findAll(".ctx-item");
    // Open, Cut, Copy, Rename, Delete, Download as ZIP,
    // Compress to ZIP, Properties
    expect(items.length).toBe(8);
    expect(items[0].text()).toContain("Open");
    expect(items[5].text()).toBe("Download as ZIP");
    expect(items[6].text()).toBe("Compress to ZIP");
    expect(items[7].text()).toContain("Properties");
  });

  it("folds File / Name / Path into the Copy submenu", () => {
    // Merging the clipboard copy and the two text variants under one
    // "Copy ▸" row is the whole point of the refactor — locking it in so
    // we don't accidentally re-promote them to the top level later.
    const wrapper = mountMenu();
    const topLevel = wrapper.findAll(".ctx-item").map((b) => b.text());
    expect(topLevel).not.toContain("File");
    expect(topLevel).not.toContain("Name");
    expect(topLevel).not.toContain("Path");
    // They live inside the submenu panel with the ctx-submenu-item /
    // ctx-item-copy-file / ctx-item-copy-name / ctx-item-copy-path
    // selectors used by callers.
    expect(wrapper.find(".ctx-item-copy-file").exists()).toBe(true);
    expect(wrapper.find(".ctx-item-copy-name").exists()).toBe(true);
    expect(wrapper.find(".ctx-item-copy-path").exists()).toBe(true);
  });

  it("labels the clipboard-copy item File for files and Folder for dirs", () => {
    expect(mountMenu().find(".ctx-item-copy-file").text()).toContain("File");
    expect(
      mountMenu({ entry: dirEntry, entryPath: "src" }).find(".ctx-item-copy-file").text(),
    ).toContain("Folder");
  });

  it("reveals the Copy submenu on tap (touch devices have no hover)", async () => {
    const wrapper = mountMenu();
    const panel = wrapper.find(".ctx-submenu-panel");
    // Collapsed by default — desktop reveals it on hover, but a hover class
    // can't be exercised here and is irrelevant to the touch path.
    expect(panel.classes()).toContain("hidden");
    await wrapper.find(".ctx-item-copy-as").trigger("click");
    expect(panel.classes()).not.toContain("hidden");
    expect(panel.classes()).toContain("block");
  });

  it("emits open on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[0].trigger("click");
    expect(wrapper.emitted("open")).toHaveLength(1);
  });

  it("hides the Open in new tab entry by default", () => {
    const wrapper = mountMenu({
      entry: { ...fileEntry, name: "photo.png" },
      entryPath: "photo.png",
    });
    expect(wrapper.text()).not.toContain("Open in new tab");
  });

  it("renders and emits the optional Open in new tab entry", async () => {
    const wrapper = mountMenu({
      entry: { ...fileEntry, name: "photo.png" },
      entryPath: "photo.png",
      showOpenInNewTab: true,
    });
    const item = wrapper.find(".ctx-item-open-new-tab");
    expect(item.exists()).toBe(true);
    expect(item.text()).toBe("Open in new tab");

    await item.trigger("click");
    expect(wrapper.emitted("openInNewTab")).toHaveLength(1);
  });

  it("emits cut on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[1].trigger("click");
    expect(wrapper.emitted("cut")).toHaveLength(1);
  });

  it("emits copy on click", async () => {
    const wrapper = mountMenu();
    // The clipboard copy lives inside the "Copy ▸" submenu now; the inner
    // button still carries .ctx-item-copy-file.
    await wrapper.find(".ctx-item-copy-file").trigger("click");
    expect(wrapper.emitted("copy")).toHaveLength(1);
  });

  it("emits copyName on click", async () => {
    const wrapper = mountMenu();
    // Inside the "Copy as ▸" submenu now — selector keeps working
    // because the inner button still carries .ctx-item-copy-name.
    await wrapper.find(".ctx-item-copy-name").trigger("click");
    expect(wrapper.emitted("copyName")).toHaveLength(1);
  });

  it("emits copyPath on click", async () => {
    const wrapper = mountMenu();
    await wrapper.find(".ctx-item-copy-path").trigger("click");
    expect(wrapper.emitted("copyPath")).toHaveLength(1);
  });

  it("emits rename on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[3].trigger("click");
    expect(wrapper.emitted("rename")).toHaveLength(1);
  });

  it("emits delete on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[4].trigger("click");
    expect(wrapper.emitted("delete")).toHaveLength(1);
  });

  it("emits download on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[5].trigger("click");
    expect(wrapper.emitted("download")).toHaveLength(1);
  });

  it("emits compressZip on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[6].trigger("click");
    expect(wrapper.emitted("compressZip")).toHaveLength(1);
  });

  it("emits properties on click", async () => {
    const wrapper = mountMenu();
    await wrapper.findAll(".ctx-item")[7].trigger("click");
    expect(wrapper.emitted("properties")).toHaveLength(1);
  });

  it("emits refresh on click (background)", async () => {
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    await wrapper.find(".ctx-item-refresh").trigger("click");
    expect(wrapper.emitted("refresh")).toHaveLength(1);
  });

  it("opens the Copy-as submenu rightward by default", async () => {
    // Plenty of room to the right (x=100 in a 1024-wide happy-dom window),
    // so the flyout keeps its default left-full anchor.
    const wrapper = mountMenu({ x: 100, y: 100 });
    await flushPromises();
    const panel = wrapper.find(".ctx-item-copy-name").element.closest(".ctx-submenu-panel");
    expect(panel?.classList.contains("left-full")).toBe(true);
    expect(panel?.classList.contains("right-full")).toBe(false);
  });

  it("flips the submenu leftward when the menu hugs the right edge", async () => {
    // Opening flush against the viewport's right edge leaves no room for the
    // rightward flyout, so it should flip to right-full.
    const wrapper = mountMenu({ x: window.innerWidth - 10, y: 100 });
    await flushPromises();
    const panel = wrapper.find(".ctx-item-copy-name").element.closest(".ctx-submenu-panel");
    expect(panel?.classList.contains("right-full")).toBe(true);
    expect(panel?.classList.contains("left-full")).toBe(false);
  });

  it("positions at given coordinates", () => {
    const wrapper = mountMenu({ x: 200, y: 300 });
    const menu = wrapper.find(".file-context-menu");
    expect(menu.attributes("style")).toContain("left: 200px");
    expect(menu.attributes("style")).toContain("top: 300px");
  });

  it("follows updated coordinates when the open menu is reused", async () => {
    const wrapper = mountMenu({ x: 100, y: 100 });

    await wrapper.setProps({ x: 240, y: 320, entry: dirEntry, entryPath: "src" });
    await flushPromises();

    const menu = wrapper.find(".file-context-menu");
    expect(menu.attributes("style")).toContain("left: 240px");
    expect(menu.attributes("style")).toContain("top: 320px");
  });

  it("background menu lists every workspace action when entry is null", () => {
    // Workspace top bar is intentionally minimal; all of these moved
    // here from there. Order matters for keyboard / mouse navigation
    // muscle memory, so the assertion locks it in. Upload Files /
    // Upload Folder are two flat top-level rows (the old "Upload ▸"
    // hover submenu was removed).
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    const items = wrapper.findAll(".ctx-item");
    expect(items.length).toBe(7);
    expect(items[0].text()).toBe("New File");
    expect(items[1].text()).toBe("New Folder");
    expect(items[2].text()).toBe("Upload Files");
    expect(items[3].text()).toBe("Upload Folder");
    expect(items[4].text()).toBe("View as List");
    expect(items[5].text()).toBe("Show hidden files");
    expect(items[6].text()).toBe("Refresh");
    // The two upload rows carry their dedicated selectors.
    expect(wrapper.find(".ctx-item-upload-files").exists()).toBe(true);
    expect(wrapper.find(".ctx-item-upload-folder").exists()).toBe(true);
  });

  it("the hidden-files entry inverts when showHiddenFiles is true", () => {
    const wrapper = mount(FileContextMenu, {
      props: {
        entry: null,
        entryPath: ".",
        x: 100,
        y: 100,
        hasClipboard: false,
        showHiddenFiles: true,
      },
    });
    expect(wrapper.find(".ctx-item-toggle-hidden").text()).toBe("Hide hidden files");
  });

  it("emits toggleHiddenFiles when the hidden-files entry is clicked", async () => {
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    await wrapper.find(".ctx-item-toggle-hidden").trigger("click");
    expect(wrapper.emitted("toggleHiddenFiles")).toHaveLength(1);
  });

  it("shows Paste in background menu when clipboard has items", () => {
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: true },
    });
    const items = wrapper.findAll(".ctx-item");
    expect(items.length).toBe(8);
    expect(items[0].text()).toBe("New File");
    expect(items[1].text()).toBe("New Folder");
    expect(items[2].text()).toBe("Upload Files");
    expect(items[3].text()).toBe("Upload Folder");
    expect(items[4].text()).toContain("Paste");
    expect(items[5].text()).toBe("View as List");
    expect(items[6].text()).toBe("Show hidden files");
    expect(items[7].text()).toBe("Refresh");
  });

  it("emits newFolder from background menu", async () => {
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    await wrapper.findAll(".ctx-item")[1].trigger("click");
    expect(wrapper.emitted("newFolder")).toHaveLength(1);
  });

  it("emits newFile from background menu", async () => {
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    await wrapper.find(".ctx-item-new-file").trigger("click");
    expect(wrapper.emitted("newFile")).toHaveLength(1);
  });

  it("emits upload from background menu", async () => {
    // Flat top-level row carrying .ctx-item-upload-files.
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    await wrapper.find(".ctx-item-upload-files").trigger("click");
    expect(wrapper.emitted("upload")).toHaveLength(1);
  });

  it("emits uploadFolder from background menu", async () => {
    const wrapper = mount(FileContextMenu, {
      props: { entry: null, entryPath: ".", x: 100, y: 100, hasClipboard: false },
    });
    await wrapper.find(".ctx-item-upload-folder").trigger("click");
    expect(wrapper.emitted("uploadFolder")).toHaveLength(1);
  });
});
