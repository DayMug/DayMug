import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";

import WorkspacePreview from "./WorkspacePreview.vue";

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
    readFile: vi.fn().mockResolvedValue(new Response("hello")),
    downloadFileUrl: (userId: string, path: string) => `/dl/${userId}/${path}`,
    readFileUrl: (userId: string, path: string) => `/read/${userId}/${path}`,
  }),
  isEditableText: (name: string) => name.endsWith(".md") || name.endsWith(".txt"),
  shouldInspectTextFile: (name: string) => !name.includes("."),
}));

vi.mock("@/composables/useMarkdown", () => ({
  renderMarkdown: (s: string) => `<p>${s}</p>`,
}));

function mountPreview(path: string) {
  return mount(WorkspacePreview, {
    props: { userId: "u1", path },
    // Stub the Teleport so assertions can run against the wrapper instead
    // of document.body.
    global: { stubs: { teleport: true } },
  });
}

describe("WorkspacePreview", () => {
  it("renders the file name split into directory prefix and basename", () => {
    const wrapper = mountPreview("docs/guide.md");
    const header = wrapper.find("header");
    expect(header.text()).toContain("docs/");
    expect(header.text()).toContain("guide.md");
  });

  it("emits close when the backdrop is clicked", async () => {
    const wrapper = mountPreview("a.md");
    await wrapper.find(".fixed.inset-0").trigger("click");
    expect(wrapper.emitted("close")).toHaveLength(1);
  });

  it("shows the Edit button for editable preview files", () => {
    expect(
      mountPreview("notes.md")
        .findAll("button")
        .some((b) => b.text() === "Edit"),
    ).toBe(true);
    expect(
      mountPreview("photo.png")
        .findAll("button")
        .some((b) => b.text() === "Edit"),
    ).toBe(false);
  });

  it("emits edit with the previewed path", async () => {
    const wrapper = mountPreview("notes.md");
    const edit = wrapper.findAll("button").find((b) => b.text() === "Edit");
    await edit!.trigger("click");
    expect(wrapper.emitted("edit")).toEqual([["notes.md"]]);
  });

  it("shows the Edit button after inspecting extensionless text", async () => {
    const wrapper = mountPreview("README");
    await vi.dynamicImportSettled();
    expect(wrapper.findAll("button").some((b) => b.text() === "Edit")).toBe(true);
  });

  it("links the download button to the file's download URL", () => {
    const wrapper = mountPreview("docs/guide.md");
    expect(wrapper.find("a[download]").attributes("href")).toBe("/dl/u1/docs/guide.md");
  });
});
