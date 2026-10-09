import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import { createI18n } from "vue-i18n";
import AdminUsers from "./AdminUsers.vue";
import en from "@/i18n/locales/en";

function makeRouter() {
  // SettingsDetailHeader.back() pushes { name: "settings" } when no
  // history is available; declare that route so an accidental nav
  // doesn't blow up the test with NoMatch errors.
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "home", component: { template: "<div/>" } },
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      { path: "/settings/admin/users", name: "settings-admin-users", component: AdminUsers },
    ],
  });
  router.push("/settings/admin/users");
  return router;
}

const i18n = createI18n({
  legacy: false,
  locale: "en",
  fallbackLocale: "en",
  messages: { en },
  missingWarn: false,
  fallbackWarn: false,
});

function mountAdmin() {
  return mount(AdminUsers, {
    global: {
      plugins: [makeRouter(), i18n],
      // DirPicker depends on browse APIs we don't stub here; the
      // edit row stays in the DOM either way thanks to v-if.
      stubs: { DirPicker: { template: "<div />" } },
    },
  });
}

const mockListUsers = vi.fn();
const mockFetchPublicConfig = vi.fn();
const mockUpdateUser = vi.fn();
const mockCreateUser = vi.fn();
const mockDeleteUser = vi.fn();
const mockSetPassword = vi.fn();
const mockSetDisabled = vi.fn();
const mockBatchSetDefaultModel = vi.fn();
const mockBatchSetProviderBinding = vi.fn();
const mockBatchSetSandboxMode = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminListUsers: (...a: unknown[]) => mockListUsers(...a),
  adminFetchPublicConfig: (...a: unknown[]) => mockFetchPublicConfig(...a),
  adminUpdateUser: (...a: unknown[]) => mockUpdateUser(...a),
  adminCreateUser: (...a: unknown[]) => mockCreateUser(...a),
  adminDeleteUser: (...a: unknown[]) => mockDeleteUser(...a),
  adminSetPassword: (...a: unknown[]) => mockSetPassword(...a),
  adminSetDisabled: (...a: unknown[]) => mockSetDisabled(...a),
  adminBatchSetDefaultModel: (...a: unknown[]) => mockBatchSetDefaultModel(...a),
  adminBatchSetProviderBinding: (...a: unknown[]) => mockBatchSetProviderBinding(...a),
  adminBatchSetSandboxMode: (...a: unknown[]) => mockBatchSetSandboxMode(...a),
}));

// The model pickers read from the shared registry; stub it so the tests
// don't hit /api/models and we control which models the <optgroup>s show.
vi.mock("@/composables/useModelRegistry", () => ({
  loadModelRegistry: () =>
    Promise.resolve({
      default_provider: "claude",
      providers: [
        {
          name: "claude",
          models: ["claude-opus-4-8[1m]", "claude-haiku-4-5"],
          latest: "",
          capabilities: {},
        },
        { name: "codex", models: ["gpt-5.5"], latest: "", capabilities: {} },
      ],
    }),
}));

vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({ authMe: { id: "admin-1", username: "admin", is_admin: true } }),
}));

const baseUser = {
  id: "u1",
  name: "Alice",
  username: "alice",
  email: "alice@example.com",
  is_admin: false,
  disabled: false,
  provider_bindings: { claude: "default" },
  work_dir: "/home/alice/proj",
  avatar: "",
  role_definition: "",
  mcp_config: "",
  claude_md_content: "",
  manage_claude_md: false,
  bark_url: "",
  pushdeer_key: "",
  notification_channel: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const baseConfig = {
  default_home_root: "/srv/users",
  providers: [
    { name: "default", type: "claude", max_concurrent: 1 },
    { name: "alt", type: "claude", max_concurrent: 1 },
  ],
  sandbox: { enabled: false, type: "noop" },
  upgrade: { enabled: true },
  current_version: "v1.0.0",
  backend: "sqlite",
};

beforeEach(() => {
  vi.clearAllMocks();
  mockListUsers.mockResolvedValue([baseUser]);
  mockFetchPublicConfig.mockResolvedValue(baseConfig);
  mockBatchSetDefaultModel.mockResolvedValue({ updated: 1, model: "" });
  mockBatchSetProviderBinding.mockResolvedValue({
    updated: 1,
    provider_type: "claude",
    provider_names: ["default"],
  });
  mockBatchSetSandboxMode.mockResolvedValue({ updated: 1, sandbox_mode: "unrestricted" });
});

describe("AdminUsers account switch", () => {
  // Switching a user's account no longer migrates on-disk history, so the
  // save flow never renders a migration banner — the feature was removed.
  it("saves without ever showing a migration banner", async () => {
    mockUpdateUser.mockResolvedValue({ ...baseUser, provider_bindings: { claude: "alt" } });

    const wrapper = mountAdmin();
    await flushPromises();
    const editBtn = wrapper.findAll("button").find((b) => b.attributes("aria-label") === "Edit");
    expect(editBtn, "Edit button should be present").toBeTruthy();
    await editBtn!.trigger("click");
    await flushPromises();

    const saveBtn = wrapper.findAll("button").find((b) => b.text() === "Save");
    expect(saveBtn, "Save button should be present in edit row").toBeTruthy();
    await saveBtn!.trigger("click");
    await flushPromises();

    expect(mockUpdateUser).toHaveBeenCalledOnce();
    expect(wrapper.find('[data-testid="claude-migration-status"]').exists()).toBe(false);
  });
});

// Multi-binding rendering: when the backend ships a typed `providers`
// list the form renders one picker per CLI type, even if some types have
// no provider rows configured we still don't show them — only types
// present in the YAML appear.
describe("AdminUsers provider bindings", () => {
  const multiTypeConfig = {
    ...baseConfig,
    providers: [
      { name: "default", type: "claude", max_concurrent: 1 },
      { name: "alt", type: "claude", max_concurrent: 1 },
      { name: "default-codex", type: "codex", max_concurrent: 1 },
    ],
  };

  it("checks the granted accounts per CLI type in the edit form", async () => {
    mockFetchPublicConfig.mockResolvedValue(multiTypeConfig);
    mockListUsers.mockResolvedValue([
      {
        ...baseUser,
        provider_bindings: { claude: "alt", codex: "default-codex" },
        provider_accounts: { claude: ["alt"], codex: ["default-codex"] },
      },
    ]);

    const wrapper = mountAdmin();
    await flushPromises();
    const editBtn = wrapper.findAll("button").find((b) => b.attributes("aria-label") === "Edit");
    await editBtn!.trigger("click");
    await flushPromises();

    const altGranted = wrapper.find('[data-testid="edit-account-claude-alt"]');
    const defGranted = wrapper.find('[data-testid="edit-account-claude-default"]');
    const codexGranted = wrapper.find('[data-testid="edit-account-codex-default-codex"]');
    expect(altGranted.exists()).toBe(true);
    expect((altGranted.element as HTMLInputElement).checked).toBe(true);
    expect((defGranted.element as HTMLInputElement).checked).toBe(false);
    expect((codexGranted.element as HTMLInputElement).checked).toBe(true);
  });

  it("submits provider_accounts (default first) on save, not the legacy fields", async () => {
    mockFetchPublicConfig.mockResolvedValue(multiTypeConfig);
    mockListUsers.mockResolvedValue([
      {
        ...baseUser,
        provider_bindings: { claude: "default" },
        provider_accounts: { claude: ["default"] },
      },
    ]);
    mockUpdateUser.mockResolvedValue({ ...baseUser });

    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Edit")!
      .trigger("click");
    await flushPromises();

    // Grant a second claude account, then mark it the default.
    await wrapper.find('[data-testid="edit-account-claude-alt"]').trigger("change");
    await wrapper.find('[data-testid="edit-default-account-claude-alt"]').trigger("change");

    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click");
    await flushPromises();

    expect(mockUpdateUser).toHaveBeenCalledOnce();
    const payload = mockUpdateUser.mock.calls[0][1];
    // alt was promoted to default → first in the array.
    expect(payload.provider_accounts).toEqual({ claude: ["alt", "default"] });
    // The single-value map must NOT ride along.
    expect(payload.provider_bindings).toBeUndefined();
  });

  it("defaults a new user's provider binding to none (no implicit default)", async () => {
    mockFetchPublicConfig.mockResolvedValue(multiTypeConfig);
    mockListUsers.mockResolvedValue([]);

    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "New user")!
      .trigger("click");
    await flushPromises();

    const claudeSel = wrapper.find('[data-testid="create-binding-claude"]');
    expect(claudeSel.exists()).toBe(true);
    // Unbound by default: the empty value is selected...
    expect((claudeSel.element as HTMLSelectElement).value).toBe("");
    // ...and the empty option reads "none", not "default".
    const firstOpt = claudeSel.findAll("option")[0];
    expect(firstOpt.attributes("value")).toBe("");
    expect(firstOpt.text()).toBe("none");
  });

  it("summarises a user with no binding as 'none'", async () => {
    mockFetchPublicConfig.mockResolvedValue(multiTypeConfig);
    mockListUsers.mockResolvedValue([{ ...baseUser, provider_bindings: {} }]);

    const wrapper = mountAdmin();
    await flushPromises();

    expect(wrapper.find('[data-testid="bindings-u1"]').text()).toBe("none");
  });

  it("labels every icon-only row action so it stays operable without the glyph", async () => {
    const wrapper = mountAdmin();
    await flushPromises();

    const labels = ["Edit", "Reset password", "Disable", "Delete"];
    for (const label of labels) {
      const btn = wrapper.findAll("button").find((b) => b.attributes("aria-label") === label);
      expect(btn, `${label} action should be present`).toBeTruthy();
      expect(btn!.text()).toBe("");
    }
  });

  it("keeps the status flags in the user cell instead of a column of their own", async () => {
    mockListUsers.mockResolvedValue([{ ...baseUser, is_admin: true, disabled: true }]);

    const wrapper = mountAdmin();
    await flushPromises();

    const userCell = wrapper.findAll("td")[1];
    expect(userCell.text()).toContain("admin");
    expect(userCell.text()).toContain("disabled");
  });

  it("does not expose environment variables in the admin user editor", async () => {
    mockListUsers.mockResolvedValue([{ ...baseUser, env: "GH_TOKEN=secret" }]);

    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.find('[data-testid="create-user-env"]').exists()).toBe(false);
    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Edit")!
      .trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="edit-user-env"]').exists()).toBe(false);
  });
});

describe("AdminUsers batch default-model editing", () => {
  it("hides the batch toolbar until a row is selected", async () => {
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.find('[data-testid="batch-model-toolbar"]').exists()).toBe(false);

    await wrapper.find('[data-testid="select-u1"]').trigger("change");
    expect(wrapper.find('[data-testid="batch-model-toolbar"]').exists()).toBe(true);
  });

  it("applies the chosen model to every selected user in one call", async () => {
    mockListUsers.mockResolvedValue([
      baseUser,
      { ...baseUser, id: "u2", username: "bob", email: "bob@example.com" },
    ]);
    mockBatchSetDefaultModel.mockResolvedValue({ updated: 2, model: "claude-haiku-4-5" });

    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper.find('[data-testid="select-all"]').trigger("change");
    const sel = wrapper.find('[data-testid="batch-model-select"]');
    (sel.element as HTMLSelectElement).value = "claude-haiku-4-5";
    await sel.trigger("change");
    await wrapper.find('[data-testid="batch-model-apply"]').trigger("click");
    await flushPromises();

    expect(mockBatchSetDefaultModel).toHaveBeenCalledOnce();
    expect(mockBatchSetDefaultModel.mock.calls[0][0]).toEqual({
      user_ids: ["u1", "u2"],
      model: "claude-haiku-4-5",
    });
    // Success line reports the count and the selection is cleared (toolbar hidden).
    expect(wrapper.find('[data-testid="batch-model-status"]').text()).toContain("2");
    expect(wrapper.find('[data-testid="batch-model-toolbar"]').exists()).toBe(false);
  });

  it("applies a provider binding to every selected user in one call", async () => {
    mockFetchPublicConfig.mockResolvedValue({
      ...baseConfig,
      providers: [
        { name: "default", type: "claude", max_concurrent: 1 },
        { name: "alt", type: "claude", max_concurrent: 1 },
        { name: "default-codex", type: "codex", max_concurrent: 1 },
        { name: "backup-codex", type: "codex", max_concurrent: 1 },
      ],
    });
    mockListUsers.mockResolvedValue([
      baseUser,
      { ...baseUser, id: "u2", username: "bob", email: "bob@example.com" },
    ]);
    mockBatchSetProviderBinding.mockResolvedValue({
      updated: 2,
      provider_type: "codex",
      provider_names: ["backup-codex", "default-codex"],
    });

    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper.find('[data-testid="select-all"]').trigger("change");
    const typeSel = wrapper.find('[data-testid="batch-provider-type"]');
    (typeSel.element as HTMLSelectElement).value = "codex";
    await typeSel.trigger("change");
    await flushPromises();
    await wrapper.find('[data-testid="batch-provider-account-default-codex"]').trigger("change");
    await wrapper.find('[data-testid="batch-provider-account-backup-codex"]').trigger("change");
    await wrapper.find('[data-testid="batch-default-provider-backup-codex"]').trigger("change");
    await wrapper.find('[data-testid="batch-provider-apply"]').trigger("click");
    await flushPromises();

    expect(mockBatchSetProviderBinding).toHaveBeenCalledOnce();
    expect(mockBatchSetProviderBinding.mock.calls[0][0]).toEqual({
      user_ids: ["u1", "u2"],
      provider_type: "codex",
      provider_names: ["backup-codex", "default-codex"],
    });
    expect(wrapper.find('[data-testid="batch-model-status"]').text()).toContain("2");
    expect(wrapper.find('[data-testid="batch-model-toolbar"]').exists()).toBe(false);
  });

  it("applies a sandbox mode to every selected user in one call", async () => {
    mockListUsers.mockResolvedValue([
      baseUser,
      { ...baseUser, id: "u2", username: "bob", email: "bob@example.com" },
    ]);
    mockBatchSetSandboxMode.mockResolvedValue({ updated: 2, sandbox_mode: "unrestricted" });

    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper.find('[data-testid="select-all"]').trigger("change");
    const sel = wrapper.find('[data-testid="batch-sandbox-select"]');
    (sel.element as HTMLSelectElement).value = "unrestricted";
    await sel.trigger("change");
    await wrapper.find('[data-testid="batch-sandbox-apply"]').trigger("click");
    await flushPromises();

    expect(mockBatchSetSandboxMode).toHaveBeenCalledOnce();
    expect(mockBatchSetSandboxMode.mock.calls[0][0]).toEqual({
      user_ids: ["u1", "u2"],
      sandbox_mode: "unrestricted",
    });
    expect(wrapper.find('[data-testid="batch-model-status"]').text()).toContain("2");
    expect(wrapper.find('[data-testid="batch-model-toolbar"]').exists()).toBe(false);
  });

  it("flags unrestricted users in the list", async () => {
    mockListUsers.mockResolvedValue([{ ...baseUser, sandbox_mode: "unrestricted" }]);
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.find('[data-testid="sandbox-badge-u1"]').exists()).toBe(true);
  });
});

// Each of these sections is its own child component; assert the page really
// mounts them and routes their events, so a child that stops being wired up
// can't slip through as dead code.
describe("AdminUsers section composition", () => {
  it("toggles the create form from the header action", async () => {
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.find('[data-testid="create-user-form"]').exists()).toBe(false);

    await wrapper
      .findAll("button")
      .find((b) => b.text() === "New user")!
      .trigger("click");
    expect(wrapper.find('[data-testid="create-user-form"]').exists()).toBe(true);
  });

  it("creates the user the form submits and closes the form", async () => {
    mockCreateUser.mockResolvedValue({ ...baseUser });
    const wrapper = mountAdmin();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "New user")!
      .trigger("click");

    const inputs = wrapper.find('[data-testid="create-user-form"]').findAll("input");
    await inputs[0].setValue("bob");
    await inputs[1].setValue("hunter2");
    const sel = wrapper.find('[data-testid="create-binding-claude"]');
    (sel.element as HTMLSelectElement).value = "default";
    await sel.trigger("change");
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Create")!
      .trigger("click");
    await flushPromises();

    expect(mockCreateUser).toHaveBeenCalledOnce();
    expect(mockCreateUser.mock.calls[0][0]).toMatchObject({ username: "bob" });
    expect(wrapper.find('[data-testid="create-user-form"]').exists()).toBe(false);
  });

  it("resets a password through the dialog the row opens", async () => {
    mockSetPassword.mockResolvedValue({});
    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.find('[data-testid="password-reset-dialog"]').exists()).toBe(false);

    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Reset password")!
      .trigger("click");
    const dialog = wrapper.find('[data-testid="password-reset-dialog"]');
    expect(dialog.exists()).toBe(true);

    await dialog.find("input").setValue("hunter2");
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Set password")!
      .trigger("click");
    await flushPromises();

    expect(mockSetPassword).toHaveBeenCalledWith("u1", "hunter2");
    expect(wrapper.find('[data-testid="password-reset-dialog"]').exists()).toBe(false);
  });

  it("keeps the password dialog open and shows the reason when the reset is rejected", async () => {
    // The dialog is a full-screen overlay: reporting this on the page's error
    // line hides it behind the backdrop, leaving the admin with no feedback.
    mockSetPassword.mockRejectedValue(new Error("password too short"));
    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Reset password")!
      .trigger("click");
    await wrapper.find('[data-testid="password-reset-dialog"]').find("input").setValue("x");
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Set password")!
      .trigger("click");
    await flushPromises();

    const dialog = wrapper.find('[data-testid="password-reset-dialog"]');
    expect(dialog.exists()).toBe(true);
    expect(dialog.find('[data-testid="password-reset-error"]').text()).toContain(
      "password too short",
    );
  });

  it("clears the batch summary when the next action starts", async () => {
    mockBatchSetDefaultModel.mockResolvedValue({ updated: 1, model: "claude-haiku-4-5" });
    mockSetDisabled.mockResolvedValue({});
    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper.find('[data-testid="select-all"]').trigger("change");
    const sel = wrapper.find('[data-testid="batch-model-select"]');
    (sel.element as HTMLSelectElement).value = "claude-haiku-4-5";
    await sel.trigger("change");
    await wrapper.find('[data-testid="batch-model-apply"]').trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="batch-model-status"]').exists()).toBe(true);

    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Disable")!
      .trigger("click");
    await flushPromises();

    expect(wrapper.find('[data-testid="batch-model-status"]').exists()).toBe(false);
  });

  it("blocks the row's other action while one is in flight", async () => {
    // Without a busy flag a double-click fires a second delete against a row
    // the first one already removed, and the 404 lands after the row is gone.
    let resolveDisable: (value?: unknown) => void = () => {};
    mockSetDisabled.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveDisable = resolve;
        }),
    );
    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Disable")!
      .trigger("click");
    await wrapper.vm.$nextTick();

    const deleteBtn = wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Delete")!;
    expect((deleteBtn.element as HTMLButtonElement).disabled).toBe(true);

    resolveDisable({});
    await flushPromises();
    expect(
      (
        wrapper.findAll("button").find((b) => b.attributes("aria-label") === "Delete")!
          .element as HTMLButtonElement
      ).disabled,
    ).toBe(false);
  });

  it("keeps the old default model when the edit row is cancelled", async () => {
    mockListUsers.mockResolvedValue([{ ...baseUser, default_model: "claude-haiku-4-5" }]);
    const wrapper = mountAdmin();
    await flushPromises();

    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Edit")!
      .trigger("click");
    await flushPromises();
    await wrapper
      .findAll("button")
      .find((b) => b.text() === "Cancel")!
      .trigger("click");
    await flushPromises();

    expect(wrapper.find('[data-testid="edit-default-model"]').exists()).toBe(false);
    expect(mockUpdateUser).not.toHaveBeenCalled();
  });
});

describe("AdminUsers legacy default-model rendering", () => {
  it("renders the per-user default model in the list and seeds the edit picker", async () => {
    mockListUsers.mockResolvedValue([{ ...baseUser, default_model: "claude-haiku-4-5" }]);

    const wrapper = mountAdmin();
    await flushPromises();
    expect(wrapper.find('[data-testid="default-model-u1"]').text()).toBe("claude-haiku-4-5");

    await wrapper
      .findAll("button")
      .find((b) => b.attributes("aria-label") === "Edit")!
      .trigger("click");
    await flushPromises();
    const picker = wrapper.find('[data-testid="edit-default-model"]');
    expect((picker.element as HTMLSelectElement).value).toBe("claude-haiku-4-5");
  });
});
