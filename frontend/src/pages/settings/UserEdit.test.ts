import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import { ref } from "vue";
import UserEdit from "./UserEdit.vue";

function makeAgent(overrides: Record<string, unknown> = {}) {
  return {
    id: "u1",
    name: "Alice",
    username: "",
    email: "",
    is_admin: false,
    disabled: false,
    work_dir: "/home/alice",
    avatar: "A",
    role_definition: "Backend dev",
    mcp_config: "",
    claude_md_content: "",
    manage_claude_md: false,
    bark_url: "",
    pushdeer_key: "",
    notification_channel: "",
    created_at: "",
    ...overrides,
  };
}

const mockUsers = ref([makeAgent()]);

// Both helpers report their outcome instead of throwing; the page navigates
// away only on `ok`.
const okResult = { ok: true, error: "" };
const mockUpdateUser = vi.fn();
const mockDeleteUser = vi.fn();

vi.mock("@/composables/useUsers", () => ({
  useUsers: () => ({
    users: mockUsers,
    handleUpdateUser: mockUpdateUser,
    handleDeleteUser: mockDeleteUser,
  }),
}));

vi.mock("@/composables/useApi", () => ({
  browseDirs: vi.fn().mockResolvedValue({ current: "/tmp", parent: "/", dirs: [] }),
}));

vi.mock("@/composables/apiUsers", () => ({
  fetchAgentBots: vi.fn().mockResolvedValue([]),
  fetchAgentBotStatuses: vi.fn().mockResolvedValue({ bots: [] }),
  fetchAgentBotRequirements: vi.fn().mockResolvedValue({ slack: [], feishu: [] }),
  createAgentBot: vi.fn(),
  updateAgentBot: vi.fn(),
  deleteAgentBot: vi.fn(),
  testAgentBotConnection: vi.fn(),
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
      { path: "/settings/users/:id", name: "settings-users-edit", component: UserEdit },
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
    ],
  });
  router.push("/settings/users/u1");
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockUpdateUser.mockResolvedValue(okResult);
  mockDeleteUser.mockResolvedValue(okResult);
  mockUsers.value = [makeAgent()];
});

describe("UserEdit", () => {
  it("renders each settings section as a tab with only the active panel visible", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs).toHaveLength(3);
    expect(tabs[0].attributes("aria-selected")).toBe("true");
    expect(wrapper.find('[data-form-panel="1"]').attributes("style")).toBeUndefined();
    expect(wrapper.find('[data-form-panel="2"]').attributes("style")).toContain("display: none");
    expect(wrapper.find('[data-form-panel="3"]').attributes("style")).toContain("display: none");
    expect(wrapper.find('[data-form-panel="4"]').exists()).toBe(false);
  });

  it("keeps the horizontal tab strip from showing a vertical scrollbar", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const tablist = wrapper.find('[role="tablist"]');
    expect(tablist.classes()).toContain("overflow-x-auto");
    expect(tablist.classes()).toContain("overflow-y-hidden");
  });

  it("switches panels without discarding edits made in another tab", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const nameInput = wrapper.find("input[placeholder='Username']");
    await nameInput.setValue("Alice Updated");
    await wrapper.findAll('[role="tab"]')[1].trigger("click");

    expect(wrapper.find('[data-form-panel="1"]').attributes("style")).toContain("display: none");
    expect(wrapper.find('[data-form-panel="2"]').attributes("style")).toBeUndefined();
    expect((nameInput.element as HTMLInputElement).value).toBe("Alice Updated");
  });

  it("loads user data into form", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const nameInput = wrapper.find("input[placeholder='Username']").element as HTMLInputElement;
    expect(nameInput.value).toBe("Alice");
  });

  it("calls handleUpdateUser on save with all fields", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const saveBtn = wrapper.findAll("button").find((b) => b.text() === "Save");
    await saveBtn!.trigger("click");
    expect(mockUpdateUser).toHaveBeenCalledWith("u1", {
      name: "Alice",
      work_dir: "/home/alice",
      avatar: "A",
      role_definition: "Backend dev",
      default_model: "",
      think_level: "",
      case_mode: false,
      mcp_config: "",
      claude_md_content: "",
      manage_claude_md: false,
    });
  });

  it("shows delete confirmation when delete is clicked", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    // Inline confirmation section is hidden initially
    const confirmSelector = ".border-destructive";
    expect(
      wrapper.findAll(confirmSelector).filter((el) => el.classes("bg-destructive\\/10")).length,
    ).toBe(0);
    const deleteBtn = wrapper.findAll("button").find((b) => b.text() === "Delete");
    await deleteBtn!.trigger("click");
    // Confirmation section is now visible and contains username
    const confirmSection = wrapper.findAll("button").find((b) => b.text() === "Confirm Delete");
    expect(confirmSection).toBeTruthy();
    expect(wrapper.text()).toContain("Alice");
  });

  it("does not delete when username does not match", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const deleteBtn = wrapper.findAll("button").find((b) => b.text() === "Delete");
    await deleteBtn!.trigger("click");

    const deleteInput = wrapper.find('input[placeholder*="Alice"]');
    await deleteInput.setValue("wrong");

    const confirmBtn = wrapper.findAll("button").find((b) => b.text() === "Confirm Delete");
    expect((confirmBtn!.element as HTMLButtonElement).disabled).toBe(true);
  });

  it("deletes when username matches", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const deleteBtn = wrapper.findAll("button").find((b) => b.text() === "Delete");
    await deleteBtn!.trigger("click");

    const deleteInput = wrapper.find('input[placeholder*="Alice"]');
    await deleteInput.setValue("Alice");

    const confirmBtn = wrapper.findAll("button").find((b) => b.text() === "Confirm Delete");
    expect((confirmBtn!.element as HTMLButtonElement).disabled).toBe(false);
    await confirmBtn!.trigger("click");
    expect(mockDeleteUser).toHaveBeenCalledWith("u1");
  });

  it("hides confirmation on cancel", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const deleteBtn = wrapper.findAll("button").find((b) => b.text() === "Delete");
    await deleteBtn!.trigger("click");
    expect(wrapper.findAll("button").some((b) => b.text() === "Confirm Delete")).toBe(true);

    // Cancel buttons: first is from UserForm, second is inside the delete confirmation
    const cancelBtns = wrapper.findAll("button").filter((b) => b.text() === "Cancel");
    await cancelBtns[cancelBtns.length - 1].trigger("click");
    expect(wrapper.findAll("button").some((b) => b.text() === "Confirm Delete")).toBe(false);
  });

  it("navigates to settings hub on cancel", async () => {
    // The legacy /settings/users page is gone — cancelling now drops the
    // user back at the settings index where the agents list lives.
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    // The first Cancel button belongs to UserForm's action bar
    const cancelBtns = wrapper.findAll("button").filter((b) => b.text() === "Cancel");
    await cancelBtns[0].trigger("click");
    await new Promise((r) => setTimeout(r, 0));
    expect(router.currentRoute.value.name).toBe("settings");
  });

  it("debounces Save so a double-click only calls handleUpdateUser once", async () => {
    let resolveUpdate: (value?: unknown) => void = () => {};
    mockUpdateUser.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveUpdate = resolve;
        }),
    );

    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const saveBtn = wrapper.findAll("button").find((b) => b.text() === "Save");
    await saveBtn!.trigger("click");
    await saveBtn!.trigger("click");
    await saveBtn!.trigger("click");

    expect(mockUpdateUser).toHaveBeenCalledTimes(1);
    expect((saveBtn!.element as HTMLButtonElement).disabled).toBe(true);

    resolveUpdate(okResult);
    await wrapper.vm.$nextTick();
  });

  it("debounces Confirm Delete so a double-click only calls handleDeleteUser once", async () => {
    let resolveDelete: (value?: unknown) => void = () => {};
    mockDeleteUser.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveDelete = resolve;
        }),
    );

    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const deleteBtn = wrapper.findAll("button").find((b) => b.text() === "Delete");
    await deleteBtn!.trigger("click");
    const deleteInput = wrapper.find('input[placeholder*="Alice"]');
    await deleteInput.setValue("Alice");

    const confirmBtn = wrapper.findAll("button").find((b) => b.text() === "Confirm Delete");
    await confirmBtn!.trigger("click");
    await confirmBtn!.trigger("click");
    await confirmBtn!.trigger("click");

    expect(mockDeleteUser).toHaveBeenCalledTimes(1);
    expect((confirmBtn!.element as HTMLButtonElement).disabled).toBe(true);

    resolveDelete(okResult);
    await wrapper.vm.$nextTick();
  });

  it("stays on the form and shows why when the backend rejects the save", async () => {
    // Navigating back to the settings hub is the only success signal this page
    // gives, so a rejected save that navigates anyway is indistinguishable
    // from a successful one.
    mockUpdateUser.mockResolvedValue({ ok: false, error: "work_dir escapes the home directory" });

    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click");
    await flushPromises();

    expect(router.currentRoute.value.name).toBe("settings-users-edit");
    expect(wrapper.find('[data-testid="save-error"]').text()).toContain(
      "work_dir escapes the home directory",
    );
  });

  it("re-enables Save after a rejected save so the user can retry", async () => {
    mockUpdateUser.mockResolvedValue({ ok: false, error: "boom" });

    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const saveBtn = wrapper.findAll("button").find((b) => b.text() === "Save");
    await saveBtn!.trigger("click");
    await flushPromises();

    expect((saveBtn!.element as HTMLButtonElement).disabled).toBe(false);
  });

  it("stays on the form and shows why when the backend rejects the delete", async () => {
    mockDeleteUser.mockResolvedValue({ ok: false, error: "delete user: 500" });

    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Delete")!
      .trigger("click");
    await wrapper.find('input[placeholder*="Alice"]').setValue("Alice");
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Confirm Delete")!
      .trigger("click");
    await flushPromises();

    expect(router.currentRoute.value.name).toBe("settings-users-edit");
    expect(wrapper.find('[data-testid="delete-error"]').text()).toContain("delete user: 500");
  });

  it("renders no submit path while the agent roster is still empty", async () => {
    // A bookmark / F5 / deep link lands here before App.vue's fetchAppState
    // resolves, so `users` is []. Offering Save against a record we don't have
    // is what used to wipe MCP config and CLAUDE.md.
    mockUsers.value = [];
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    expect(wrapper.findAll("button").some((b) => b.text() === "Save")).toBe(false);
  });

  it("submits the loaded MCP config when the roster arrives after mount", async () => {
    mockUsers.value = [];
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    mockUsers.value = [
      makeAgent({
        mcp_config: '{"mcpServers":{"fetch":{}}}',
        claude_md_content: "# house rules",
        manage_claude_md: true,
      }),
    ];
    await wrapper.vm.$nextTick();

    const saveBtn = wrapper.findAll("button").find((b) => b.text() === "Save");
    await saveBtn!.trigger("click");

    expect(mockUpdateUser).toHaveBeenCalledWith(
      "u1",
      expect.objectContaining({
        mcp_config: '{"mcpServers":{"fetch":{}}}',
        claude_md_content: "# house rules",
        manage_claude_md: true,
      }),
    );
  });

  it("does not expose the retired Advanced tab or editors", async () => {
    const router = makeRouter();
    await router.isReady();
    const wrapper = mount(UserEdit, { global: { plugins: [router] } });

    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs).toHaveLength(3);
    expect(tabs.some((tab) => tab.text().includes("Advanced"))).toBe(false);
    expect(wrapper.find("textarea.mcp-editor").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("CLAUDE.md");
  });
});
