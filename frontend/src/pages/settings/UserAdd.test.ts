import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import { ref } from "vue";
import UserAdd from "./UserAdd.vue";

const mockCreateUser = vi.fn();

vi.mock("@/composables/useApi", () => ({
  browseDirs: vi.fn().mockResolvedValue({ current: "/home/admin", parent: "", dirs: [] }),
  mkdirBrowseDir: vi.fn(),
}));

// DirPicker is rendered whenever the human owner has a work_dir set.
vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({
    authMe: ref({
      id: "u0",
      username: "admin",
      name: "Admin",
      is_admin: true,
      bark_url: "",
      pushdeer_key: "",
      notification_channel: "",
      work_dir: "/home/admin",
    }),
  }),
}));

vi.mock("@/composables/useUsers", () => ({
  useUsers: () => ({
    handleCreateUser: mockCreateUser,
    currentUser: ref({ id: "u0", work_dir: "/home/admin" }),
  }),
}));

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings/users/add", name: "settings-users-add", component: UserAdd },
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
    ],
  });
  router.push("/settings/users/add");
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("UserAdd", () => {
  it("renders add user form", async () => {
    // After the hub redesign UserAdd renders its title via the shared
    // SettingsDetailHeader (an h1), and the page now leads with the
    // unified Agent / Bot title above the form.
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserAdd, { global: { plugins: [router] } });
    expect(wrapper.find("h1").text()).toBe("New Agent");
    expect(wrapper.text()).toContain("New Agent");
  });

  it("calls handleCreateUser on submit with data object", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserAdd, { global: { plugins: [router] } });

    const nameInput = wrapper.find("input[placeholder='Username']");
    await nameInput.setValue("Alice");

    const avatarInput = wrapper.find(
      "input[placeholder='Image URL or initials, e.g. https://... / BG']",
    );
    await avatarInput.setValue("https://example.com/avatar.png");

    await wrapper
      .find('textarea[placeholder="Describe this agent\'s role..."]')
      .setValue("Answer as a product-minded engineer.");

    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    dirPicker.vm.$emit("update:modelValue", "/home/admin/proj");
    await wrapper.vm.$nextTick();

    const submitBtn = wrapper.findAll("button").find((b) => b.text() === "Add");
    await submitBtn!.trigger("click");

    expect(mockCreateUser).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "Alice",
        work_dir: "/home/admin/proj",
        avatar: "https://example.com/avatar.png",
        role_definition: "Answer as a product-minded engineer.",
      }),
    );
  });

  it("disables add button when name or workDir is empty", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserAdd, { global: { plugins: [router] } });
    const submitBtn = wrapper.findAll("button").find((b) => b.text() === "Add");
    expect((submitBtn!.element as HTMLButtonElement).disabled).toBe(true);
  });

  it("navigates to settings hub on cancel", async () => {
    // The legacy /settings/users page is gone — cancelling now drops the
    // user back at the settings index where the agents list lives.
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserAdd, { global: { plugins: [router] } });
    const cancelBtn = wrapper.findAll("button").find((b) => b.text() === "Cancel");
    await cancelBtn!.trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    expect(router.currentRoute.value.name).toBe("settings");
  });

  it("does not render preset persona controls", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserAdd, { global: { plugins: [router] } });
    expect(wrapper.text()).not.toContain("Persona template");
    expect(wrapper.text()).not.toContain("Code Reviewer");
  });

  it("debounces submit so a double-click only calls handleCreateUser once", async () => {
    // Guard against duplicate POSTs when the user double-clicks Add before
    // the in-flight request resolves. The form should disable its submit
    // button while submitting; clicking it again must not re-emit.
    let resolveCreate: (value?: unknown) => void = () => {};
    mockCreateUser.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveCreate = resolve;
        }),
    );

    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserAdd, { global: { plugins: [router] } });

    await wrapper.find("input[placeholder='Username']").setValue("Alice");
    const dirPicker = wrapper.findComponent({ name: "DirPicker" });
    dirPicker.vm.$emit("update:modelValue", "/home/admin/proj");
    await wrapper.vm.$nextTick();

    const submitBtn = wrapper.findAll("button").find((b) => b.text() === "Add");
    await submitBtn!.trigger("click");
    await submitBtn!.trigger("click");
    await submitBtn!.trigger("click");

    expect(mockCreateUser).toHaveBeenCalledTimes(1);
    expect((submitBtn!.element as HTMLButtonElement).disabled).toBe(true);

    resolveCreate();
    await wrapper.vm.$nextTick();
  });
});
