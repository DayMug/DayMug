import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import AdminGlobalNewUserDefaults from "./AdminGlobalNewUserDefaults.vue";

const { fetchDefaults, fetchModels, saveDefaults } = vi.hoisted(() => ({
  fetchDefaults: vi.fn(),
  fetchModels: vi.fn(),
  saveDefaults: vi.fn(),
}));
vi.mock("@/composables/apiAdmin", () => ({
  adminFetchNewUserDefaults: fetchDefaults,
  adminFetchModels: fetchModels,
  adminSaveNewUserDefaults: saveDefaults,
}));

const unbound = { provider_type: "", provider_name: "", model: "" };
const chosen = { provider_type: "codex", provider_name: "team", model: "custom-model" };
const chosenValue = JSON.stringify(["codex", "team", "custom-model"]);

beforeEach(() => {
  vi.resetAllMocks();
  fetchDefaults.mockResolvedValue(unbound);
  fetchModels.mockResolvedValue({
    accounts: [{ account: "team", provider: "codex", models: ["custom-model"] }],
  });
  saveDefaults.mockImplementation(async (data) => data);
});

describe("new user defaults", () => {
  it("loads saved defaults and lists models under their account", async () => {
    fetchDefaults.mockResolvedValue(chosen);
    const wrapper = mount(AdminGlobalNewUserDefaults);
    await flushPromises();
    expect((wrapper.get("select").element as HTMLSelectElement).value).toBe(chosenValue);
    expect(wrapper.get("optgroup").attributes("label")).toBe("team (codex)");
    expect(wrapper.get("button").attributes("disabled")).toBeDefined();
  });

  it("saves a model together with the account that grants it", async () => {
    const wrapper = mount(AdminGlobalNewUserDefaults);
    await flushPromises();
    await wrapper.get("select").setValue(chosenValue);
    await wrapper.get("button").trigger("click");
    await flushPromises();
    expect(saveDefaults).toHaveBeenCalledWith(chosen);
    expect(wrapper.get('[role="status"]').text()).toContain("saved");
  });

  it("saves an explicit no-binding policy", async () => {
    fetchDefaults.mockResolvedValue(chosen);
    const wrapper = mount(AdminGlobalNewUserDefaults);
    await flushPromises();
    await wrapper.get("select").setValue("");
    await wrapper.get("button").trigger("click");
    await flushPromises();
    expect(saveDefaults).toHaveBeenCalledWith(unbound);
  });

  it("shows load errors and prevents overwriting an unknown saved policy", async () => {
    fetchDefaults.mockRejectedValue(new Error("load failed"));
    const wrapper = mount(AdminGlobalNewUserDefaults);
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("load failed");
    expect(wrapper.get("select").attributes("disabled")).toBeDefined();
    expect(wrapper.get("button").attributes("disabled")).toBeDefined();
  });

  it("lets an admin clear a removed model and retains failed save drafts", async () => {
    fetchDefaults.mockResolvedValue({ ...chosen, model: "removed" });
    saveDefaults.mockRejectedValue(new Error("save failed"));
    const wrapper = mount(AdminGlobalNewUserDefaults);
    await flushPromises();
    expect(wrapper.text()).toContain("unavailable");
    await wrapper.get("select").setValue("");
    await wrapper.get("button").trigger("click");
    await flushPromises();
    expect(wrapper.get('[role="alert"]').text()).toContain("save failed");
    expect(wrapper.get("button").attributes("disabled")).toBeUndefined();
  });
});
