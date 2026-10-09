import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import type { FileEntry } from "@/composables/useFileApi";
import FilePropertiesDialog from "./FilePropertiesDialog.vue";

const fileEntry: FileEntry = {
  name: "test.txt",
  is_dir: false,
  size: 2048,
  modified: "2024-06-15T10:30:00Z",
};
const dirEntry: FileEntry = {
  name: "src",
  is_dir: true,
  size: 0,
  modified: "2024-06-15T10:30:00Z",
};

function mountDialog(props: { entry: FileEntry; entryPath: string }) {
  return mount(FilePropertiesDialog, { props });
}

describe("FilePropertiesDialog", () => {
  it("renders file name and type", () => {
    const wrapper = mountDialog({ entry: fileEntry, entryPath: "docs/test.txt" });
    expect(wrapper.find(".props-name").text()).toBe("test.txt");
    const values = wrapper.findAll(".props-value");
    expect(values[0].text()).toContain("File");
    expect(values[0].text()).toContain(".txt");
  });

  it("shows Folder for directories", () => {
    const wrapper = mountDialog({ entry: dirEntry, entryPath: "src" });
    const values = wrapper.findAll(".props-value");
    expect(values[0].text().trim()).toBe("Folder");
  });

  it("shows formatted size for files", () => {
    const wrapper = mountDialog({ entry: fileEntry, entryPath: "test.txt" });
    const values = wrapper.findAll(".props-value");
    expect(values[1].text().trim()).toBe("2.0 KB");
  });

  it("shows -- size for directories", () => {
    const wrapper = mountDialog({ entry: dirEntry, entryPath: "src" });
    const values = wrapper.findAll(".props-value");
    expect(values[1].text().trim()).toBe("--");
  });

  it("shows path", () => {
    const wrapper = mountDialog({ entry: fileEntry, entryPath: "docs/test.txt" });
    const values = wrapper.findAll(".props-value");
    expect(values[3].text().trim()).toBe("docs/test.txt");
  });

  it("emits close on close button click", async () => {
    const wrapper = mountDialog({ entry: fileEntry, entryPath: "test.txt" });
    await wrapper.find(".props-close").trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("emits close on footer button click", async () => {
    const wrapper = mountDialog({ entry: fileEntry, entryPath: "test.txt" });
    await wrapper.find(".props-btn").trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("emits close on overlay click", async () => {
    const wrapper = mountDialog({ entry: fileEntry, entryPath: "test.txt" });
    await wrapper.find(".props-overlay").trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });
});
