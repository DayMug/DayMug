import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";

import AdminGlobalHelpDoc from "./AdminGlobalHelpDoc.vue";

const mockFetchHelpDoc = vi.fn();
const mockSaveHelpDoc = vi.fn();

vi.mock("@/composables/useApi", () => ({
  fetchHelpDoc: (...args: unknown[]) => mockFetchHelpDoc(...args),
  adminSaveHelpDoc: (...args: unknown[]) => mockSaveHelpDoc(...args),
}));

const mockSetHelpDocMarkdown = vi.hoisted(() => vi.fn());
vi.mock("@/composables/useHelpDoc", () => ({
  useHelpDoc: () => ({ setHelpDocMarkdown: mockSetHelpDocMarkdown }),
}));

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchHelpDoc.mockResolvedValue({ markdown: "# Welcome" });
});

async function mountHelpDoc() {
  const wrapper = mount(AdminGlobalHelpDoc);
  await flushPromises();
  return wrapper;
}

function saveButton(wrapper: Awaited<ReturnType<typeof mountHelpDoc>>) {
  return wrapper.findAll("button").find((b) => b.text().includes("Save"));
}

describe("AdminGlobalHelpDoc", () => {
  it("seeds the textarea from the persisted markdown", async () => {
    const wrapper = await mountHelpDoc();
    expect(wrapper.find("textarea").element.value).toBe("# Welcome");
  });

  it("starts blank when the load fails", async () => {
    mockFetchHelpDoc.mockRejectedValueOnce(new Error("nope"));
    const wrapper = await mountHelpDoc();
    expect(wrapper.find("textarea").element.value).toBe("");
    expect(wrapper.text()).not.toContain("nope");
  });

  it("keeps Save disabled until the markdown is edited", async () => {
    const wrapper = await mountHelpDoc();
    expect(saveButton(wrapper)?.attributes("disabled")).toBeDefined();

    await wrapper.find("textarea").setValue("# Changed");
    expect(saveButton(wrapper)?.attributes("disabled")).toBeUndefined();
    expect(wrapper.text()).toContain("Unsaved changes");
  });

  it("publishes a save into the shared help-doc cache", async () => {
    mockSaveHelpDoc.mockResolvedValueOnce({ markdown: "# Changed" });
    const wrapper = await mountHelpDoc();

    await wrapper.find("textarea").setValue("# Changed");
    await saveButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(mockSaveHelpDoc).toHaveBeenCalledWith("# Changed");
    expect(mockSetHelpDocMarkdown).toHaveBeenCalledWith("# Changed");
    expect(wrapper.text()).toContain("the popup is now available to every user");
  });

  it("reports the cleared variant when the markdown is emptied", async () => {
    mockSaveHelpDoc.mockResolvedValueOnce({ markdown: "" });
    const wrapper = await mountHelpDoc();

    await wrapper.find("textarea").setValue("");
    await saveButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("the sidebar help button is now hidden");
  });

  it("clears the dirty flag once the save round-trips", async () => {
    mockSaveHelpDoc.mockResolvedValueOnce({ markdown: "# Changed" });
    const wrapper = await mountHelpDoc();

    await wrapper.find("textarea").setValue("# Changed");
    await saveButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).not.toContain("Unsaved changes");
  });

  it("surfaces a failed save", async () => {
    mockSaveHelpDoc.mockRejectedValueOnce(new Error("db is read-only"));
    const wrapper = await mountHelpDoc();

    await wrapper.find("textarea").setValue("# Changed");
    await saveButton(wrapper)?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("db is read-only");
  });
});
