import { describe, it, expect, vi, beforeEach } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import AdvancedSettings from "./AdvancedSettings.vue";

const mockFetch = vi.fn();
const mockUpdate = vi.fn();
vi.mock("@/composables/useApi", () => ({
  fetchMyEnvironment: () => mockFetch(),
  updateMyEnvironment: (env: string) => mockUpdate(env),
}));

beforeEach(() => {
  mockFetch.mockReset();
  mockUpdate.mockReset();
});

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      { path: "/settings/advanced", name: "settings-advanced", component: AdvancedSettings },
    ],
  });
  router.push("/settings/advanced");
  await router.isReady();
  const wrapper = mount(AdvancedSettings, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

describe("AdvancedSettings", () => {
  it("shows a line number for each environment variable row", async () => {
    mockFetch.mockResolvedValueOnce({ env: "GH_TOKEN=old\nEMPTY=\nDEBUG=true" });
    const wrapper = await mountPage();

    expect(wrapper.findAll("[data-line-number]").map((line) => line.text())).toEqual([
      "1",
      "2",
      "3",
    ]);
  });

  it("keeps long environment variable rows on one line", async () => {
    mockFetch.mockResolvedValueOnce({ env: "LONG_VALUE=abcdefghijklmnopqrstuvwxyz" });
    const wrapper = await mountPage();
    const textarea = wrapper.find("#advanced-environment");

    expect(textarea.attributes("wrap")).toBe("off");
    expect(textarea.classes()).toContain("whitespace-pre");
  });

  it("keeps the gutter beside the editor so horizontal scroll cannot slide text under it", async () => {
    mockFetch.mockResolvedValueOnce({ env: "LONG_VALUE=abcdefghijklmnopqrstuvwxyz" });
    const wrapper = await mountPage();
    const gutter = wrapper.find("[data-line-gutter]");
    const textarea = wrapper.find("#advanced-environment");

    expect(gutter.element.parentElement).toBe(textarea.element.parentElement);
    expect(gutter.classes()).not.toContain("absolute");
    expect(textarea.classes()).not.toContain("pl-12");
  });

  it("keeps line numbers aligned while the editor scrolls vertically", async () => {
    mockFetch.mockResolvedValueOnce({ env: "ONE=1\nTWO=2\nTHREE=3" });
    const wrapper = await mountPage();
    const textarea = wrapper.find("#advanced-environment");
    const textareaElement = textarea.element as HTMLTextAreaElement;
    textareaElement.scrollTop = 20;

    await textarea.trigger("scroll");

    expect(wrapper.find('[aria-hidden="true"] > div').attributes("style")).toContain(
      "translateY(-20px)",
    );
  });

  it("loads and saves newline-delimited environment variables", async () => {
    mockFetch.mockResolvedValueOnce({ env: "GH_TOKEN=old" });
    mockUpdate.mockResolvedValueOnce({ env: "GH_TOKEN=new\nEMPTY=" });
    const wrapper = await mountPage();
    const textarea = wrapper.find("#advanced-environment");
    expect((textarea.element as HTMLTextAreaElement).value).toBe("GH_TOKEN=old");

    await textarea.setValue("GH_TOKEN=new\nEMPTY=");
    await wrapper.find("form").trigger("submit");
    await flushPromises();

    expect(mockUpdate).toHaveBeenCalledWith("GH_TOKEN=new\nEMPTY=");
    expect(wrapper.text()).toContain("Environment variables updated");
  });

  it("shows backend validation errors", async () => {
    mockFetch.mockResolvedValueOnce({ env: "" });
    mockUpdate.mockRejectedValueOnce(new Error("line 1 must use a valid VAR=VAL assignment"));
    const wrapper = await mountPage();

    await wrapper.find("#advanced-environment").setValue("INVALID");
    await wrapper.find("form").trigger("submit");
    await flushPromises();

    expect(wrapper.text()).toContain("line 1 must use a valid VAR=VAL assignment");
  });
});
