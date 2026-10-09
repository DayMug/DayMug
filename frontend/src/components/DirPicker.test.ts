import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import DirPicker from "./DirPicker.vue";

const mockBrowseDirs = vi.fn().mockResolvedValue({
  current: "/home/user",
  parent: "/home",
  dirs: [
    { name: "project", path: "/home/user/project" },
    { name: "docs", path: "/home/user/docs" },
  ],
});
const mockMkdir = vi.fn();

vi.mock("@/composables/useApi", () => ({
  browseDirs: (...args: unknown[]) => mockBrowseDirs(...args),
  mkdirBrowseDir: (...args: unknown[]) => mockMkdir(...args),
}));

beforeEach(() => {
  vi.clearAllMocks();
});

describe("DirPicker", () => {
  it("renders input and browse button", () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "/tmp" } });
    expect(wrapper.find("input.form-input").exists()).toBe(true);
    expect(wrapper.find(".browse-btn").exists()).toBe(true);
  });

  it("shows current value in input", () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "/home/user" } });
    const input = wrapper.find("input.form-input").element as HTMLInputElement;
    expect(input.value).toBe("/home/user");
  });

  it("opens browser on Browse click", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    expect(wrapper.find(".dir-browser").exists()).toBe(false);

    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.find(".dir-browser").exists()).toBe(true));
    expect(mockBrowseDirs).toHaveBeenCalled();
  });

  it("shows directory entries", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.findAll(".dir-item").length).toBe(2));
    expect(wrapper.findAll(".dir-item")[0].text()).toContain("project");
  });

  it("emits update:modelValue on Select This click", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.find(".select-btn").exists()).toBe(true));

    await wrapper.find(".select-btn").trigger("click");
    expect(wrapper.emitted("update:modelValue")).toEqual([["/home/user"]]);
  });

  it("navigates into directory on click", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.findAll(".dir-item").length).toBe(2));

    mockBrowseDirs.mockResolvedValueOnce({
      current: "/home/user/project",
      parent: "/home/user",
      dirs: [],
    });

    await wrapper.findAll(".dir-item")[0].trigger("click");
    expect(mockBrowseDirs).toHaveBeenCalledWith("/home/user/project");
  });

  it("closes browser on Cancel click", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.find(".dir-browser").exists()).toBe(true));

    await wrapper.find(".cancel-btn").trigger("click");
    expect(wrapper.find(".dir-browser").exists()).toBe(false);
  });

  it("creates a new folder and navigates into it", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.find(".new-folder-btn").exists()).toBe(true));

    // Inline form is hidden until "New" is clicked.
    expect(wrapper.find(".new-folder-form").exists()).toBe(false);
    await wrapper.find(".new-folder-btn").trigger("click");
    expect(wrapper.find(".new-folder-form").exists()).toBe(true);

    mockMkdir.mockResolvedValueOnce({ name: "fresh", path: "/home/user/fresh" });
    mockBrowseDirs.mockResolvedValueOnce({
      current: "/home/user/fresh",
      parent: "/home/user",
      dirs: [],
    });

    const input = wrapper.find(".new-folder-form input");
    await input.setValue("fresh");
    await input.trigger("keydown.enter");

    await vi.waitFor(() => expect(mockMkdir).toHaveBeenCalledWith("/home/user", "fresh"));
    await vi.waitFor(() => expect(mockBrowseDirs).toHaveBeenLastCalledWith("/home/user/fresh"));
    // Form collapses after a successful create.
    expect(wrapper.find(".new-folder-form").exists()).toBe(false);
  });

  it("disables Up at the jail root", async () => {
    // When the picker opens at the configured rootPath, the backend
    // already sends parent="" — but the Up button has its own
    // belt-and-suspenders check (atRoot) so even if a stale parent
    // somehow leaks through, it stays disabled.
    mockBrowseDirs.mockResolvedValueOnce({
      current: "/home/alice",
      parent: "", // backend clears parent at jail root
      dirs: [],
    });
    const wrapper = mount(DirPicker, {
      props: { modelValue: "/home/alice", rootPath: "/home/alice" },
    });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.find(".dir-browser").exists()).toBe(true));
    const upBtn = wrapper.findAll("button").find((b) => b.text().includes("Up"));
    expect(upBtn?.attributes("disabled")).toBeDefined();
  });

  it("opens at rootPath when modelValue is outside the jail", async () => {
    // The form might supply a stale modelValue (e.g. an admin previously
    // pointed an agent at /tmp). The picker should silently re-anchor
    // at the root rather than fetch /tmp and then refuse to walk back.
    const wrapper = mount(DirPicker, {
      props: { modelValue: "/tmp/elsewhere", rootPath: "/home/alice" },
    });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(mockBrowseDirs).toHaveBeenCalled());
    // First (and only) call goes to the root, not the out-of-jail value.
    expect(mockBrowseDirs).toHaveBeenLastCalledWith("/home/alice");
  });

  it("surfaces mkdir errors without closing the form", async () => {
    const wrapper = mount(DirPicker, { props: { modelValue: "" } });
    await wrapper.find(".browse-btn").trigger("click");
    await vi.waitFor(() => expect(wrapper.find(".new-folder-btn").exists()).toBe(true));

    await wrapper.find(".new-folder-btn").trigger("click");
    mockMkdir.mockRejectedValueOnce(new Error("directory already exists"));

    const input = wrapper.find(".new-folder-form input");
    await input.setValue("dupe");
    await input.trigger("keydown.enter");

    await vi.waitFor(() => expect(wrapper.text()).toContain("directory already exists"));
    // Form stays open so the user can adjust the name.
    expect(wrapper.find(".new-folder-form").exists()).toBe(true);
  });
});
