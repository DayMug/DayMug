import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AdminProvidersEditor from "./AdminProvidersEditor.vue";

const fetchProviders = vi.fn();
const saveProviders = vi.fn();
const fetchModels = vi.fn();
const saveModels = vi.fn();
const fetchTransports = vi.fn();
const saveTransports = vi.fn();
const checkAccount = vi.fn();
const checkSummaryModel = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminFetchProviders: (...args: unknown[]) => fetchProviders(...args),
  adminSaveProviders: (...args: unknown[]) => saveProviders(...args),
  adminFetchModels: (...args: unknown[]) => fetchModels(...args),
  adminSaveModels: (...args: unknown[]) => saveModels(...args),
  adminFetchTransports: (...args: unknown[]) => fetchTransports(...args),
  adminSaveTransports: (...args: unknown[]) => saveTransports(...args),
  adminCheckAccount: (...args: unknown[]) => checkAccount(...args),
  adminCheckSummaryModel: (...args: unknown[]) => checkSummaryModel(...args),
}));

const transports = {
  transports: [
    { provider: "claude", transport: "agent-sdk", options: ["agent-sdk", "cli"] },
    { provider: "codex", transport: "app-server", options: ["app-server", "cli"] },
    { provider: "claude-compatible", transport: "cli", options: ["cli"] },
    { provider: "openai-compatible", transport: "app-server", options: ["app-server"] },
  ],
};

const providers = {
  providers: [
    {
      name: "default",
      type: "claude",
      max_concurrent: 2,
      config_dir: "",
      env: {},
    },
    {
      name: "codex-qwen",
      type: "codex",
      max_concurrent: 1,
      config_dir: "",
      env: {},
    },
  ],
};

const models = {
  accounts: [
    {
      account: "default",
      provider: "claude",
      models: ["claude-opus-5-5[1m]", "claude-sonnet-5"],
      summary_model: "claude-haiku-5",
      specs: {},
      overridden: true,
    },
    {
      account: "codex-qwen",
      provider: "codex",
      models: ["Qwen/Qwen3"],
      summary_model: "Qwen/Qwen3",
      specs: {},
      overridden: true,
    },
  ],
};

async function mountEditor() {
  const wrapper = mount(AdminProvidersEditor);
  await flushPromises();
  return wrapper;
}

async function addAccount(
  wrapper: Awaited<ReturnType<typeof mountEditor>>,
  framework: "claude-code" | "codex",
  access: "login" | "api-key",
  vendor?: string,
) {
  await wrapper.get('[data-testid="add-provider"]').trigger("click");
  await wrapper.get(`[data-testid="add-framework-${framework}"]`).trigger("click");
  await wrapper.get(`[data-testid="add-access-${access}"]`).trigger("click");
  if (vendor) await wrapper.get('[data-testid="add-vendor"]').setValue(vendor);
  await wrapper.get('[data-testid="add-confirm"]').trigger("click");
}

describe("AdminProvidersEditor", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fetchProviders.mockResolvedValue(providers);
    saveProviders.mockImplementation(async (rows) => ({ providers: rows }));
    fetchModels.mockResolvedValue(models);
    saveModels.mockResolvedValue(models);
    fetchTransports.mockResolvedValue(transports);
    saveTransports.mockResolvedValue(transports);
  });

  it("adds a DeepSeek draft without saving or overwriting existing accounts", async () => {
    const wrapper = await mountEditor();
    await addAccount(wrapper, "claude-code", "api-key", "deepseek");
    const row = wrapper.get('[data-testid="provider-row-2"]');
    expect(row.get('[data-testid="api-url-2"]').element).toHaveProperty(
      "value",
      "https://api.deepseek.com/anthropic",
    );
    expect(row.get('[data-testid="api-key-2"]').attributes("type")).toBe("password");
    expect(row.get('[data-testid="model-list-deepseek"]').text()).toContain("deepseek-flash[1m]");
    expect(row.get('[data-testid="summary-model-deepseek"]').element).toHaveProperty(
      "value",
      "deepseek-flash",
    );
    expect(row.find('[data-testid="account-login"]').exists()).toBe(false);
    expect(row.get('[data-testid="account-check"]').attributes("disabled")).toBeDefined();
    expect(saveProviders).not.toHaveBeenCalled();
    expect(wrapper.get('[data-testid="provider-row-0"]').text()).toContain("default");
  });

  it("saves the DeepSeek API key and preset models together", async () => {
    const deepseek = {
      account: "deepseek",
      provider: "claude-compatible",
      models: [],
      summary_model: "",
      specs: {},
      overridden: false,
    };
    fetchModels
      .mockResolvedValueOnce(models)
      .mockResolvedValueOnce({ accounts: [...models.accounts, deepseek] });
    saveModels.mockImplementation(async (rows) => ({
      accounts: [...models.accounts, { ...deepseek, ...rows.deepseek, overridden: true }],
    }));
    const wrapper = await mountEditor();
    await addAccount(wrapper, "claude-code", "api-key", "deepseek");
    await wrapper.get('[data-testid="api-key-2"]').setValue("test-deepseek-key");
    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveProviders).toHaveBeenCalledWith(
      expect.arrayContaining([
        expect.objectContaining({
          name: "deepseek",
          type: "claude-compatible",
          env: expect.objectContaining({
            ANTHROPIC_BASE_URL: "https://api.deepseek.com/anthropic",
            ANTHROPIC_AUTH_TOKEN: "test-deepseek-key",
            ANTHROPIC_API_KEY: "",
            CLAUDE_CODE_SUBAGENT_MODEL: "deepseek-flash",
          }),
        }),
      ]),
    );
    expect(saveModels).toHaveBeenCalledWith(
      expect.objectContaining({
        deepseek: {
          models: ["deepseek-flash[1m]", "deepseek-v4-pro[1m]"],
          summary_model: "deepseek-flash",
          specs: {},
        },
      }),
    );
    expect(
      wrapper
        .get('[data-testid="provider-row-2"]')
        .get('[data-testid="account-check"]')
        .attributes("disabled"),
    ).toBeUndefined();
    expect(wrapper.get('[data-testid="save-providers"]').attributes("disabled")).toBeDefined();
  });

  it("uses unique names when adding multiple DeepSeek accounts", async () => {
    const wrapper = await mountEditor();
    await addAccount(wrapper, "claude-code", "api-key", "deepseek");
    await addAccount(wrapper, "claude-code", "api-key", "deepseek");
    expect(wrapper.find('[data-testid="model-list-deepseek"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="model-list-deepseek-2"]').exists()).toBe(true);
  });

  it("preserves API fields while editing advanced environment variables", async () => {
    fetchProviders.mockResolvedValue({
      providers: [
        {
          ...providers.providers[0],
          type: "claude-compatible",
          env: {
            ANTHROPIC_BASE_URL: "https://example.test/anthropic",
            ANTHROPIC_API_KEY: "test-existing-key",
            EXTRA: "before",
          },
        },
      ],
    });
    const wrapper = await mountEditor();
    const advanced = wrapper.get("textarea");
    expect(advanced.element).toHaveProperty("value", "EXTRA=before");
    expect(wrapper.get('[data-testid="api-key-0"]').element).toHaveProperty(
      "value",
      "test-existing-key",
    );
    await advanced.setValue("EXTRA=after=equals");
    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveProviders).toHaveBeenCalledWith([
      expect.objectContaining({
        env: {
          ANTHROPIC_BASE_URL: "https://example.test/anthropic",
          ANTHROPIC_API_KEY: "test-existing-key",
          EXTRA: "after=equals",
        },
      }),
    ]);
  });

  it("accepts API credentials pasted into the advanced environment editor", async () => {
    fetchProviders.mockResolvedValue({
      providers: [{ ...providers.providers[0], type: "claude-compatible", env: {} }],
    });
    const wrapper = await mountEditor();
    await wrapper
      .get("textarea")
      .setValue(
        "ANTHROPIC_BASE_URL=https://pasted.test/anthropic\nANTHROPIC_API_KEY=pasted-key\nEXTRA=keep",
      );
    expect(wrapper.get('[data-testid="api-url-0"]').element).toHaveProperty(
      "value",
      "https://pasted.test/anthropic",
    );
    expect(wrapper.get('[data-testid="api-key-0"]').element).toHaveProperty("value", "pasted-key");
    expect(wrapper.get("textarea").element).toHaveProperty("value", "EXTRA=keep");
    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveProviders).toHaveBeenCalledWith([
      expect.objectContaining({
        env: {
          ANTHROPIC_BASE_URL: "https://pasted.test/anthropic",
          ANTHROPIC_API_KEY: "pasted-key",
          EXTRA: "keep",
        },
      }),
    ]);
  });

  it("updates the active Anthropic credential and clears its alternate", async () => {
    fetchProviders.mockResolvedValue({
      providers: [
        {
          ...providers.providers[0],
          type: "claude-compatible",
          env: {
            ANTHROPIC_AUTH_TOKEN: "",
            ANTHROPIC_API_KEY: "active-key",
          },
        },
      ],
    });
    const wrapper = await mountEditor();
    const key = wrapper.get('[data-testid="api-key-0"]');
    expect(key.element).toHaveProperty("value", "active-key");
    await key.setValue("rotated-key");
    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveProviders).toHaveBeenCalledWith([
      expect.objectContaining({
        env: {
          ANTHROPIC_AUTH_TOKEN: "",
          ANTHROPIC_API_KEY: "rotated-key",
        },
      }),
    ]);
  });

  it("edits a Responses-compatible endpoint without dropping other environment settings", async () => {
    fetchProviders.mockResolvedValue({
      providers: [
        {
          ...providers.providers[1],
          type: "openai-compatible",
          config_dir: "/srv/codex-api",
          env: {
            OPENAI_BASE_URL: "https://before.test/v1",
            OPENAI_API_KEY: "before-key",
            EXTRA: "keep",
          },
        },
      ],
    });
    const wrapper = await mountEditor();
    expect(wrapper.get('[data-testid="api-settings-0"]').text()).toContain(
      "Chat Completions-only endpoints are unsupported",
    );
    await wrapper.get('[data-testid="api-url-0"]').setValue("https://after.test/v1");
    await wrapper.get('[data-testid="api-key-0"]').setValue("after-key");
    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveProviders).toHaveBeenCalledWith([
      expect.objectContaining({
        env: {
          OPENAI_BASE_URL: "https://after.test/v1",
          OPENAI_API_KEY: "after-key",
          EXTRA: "keep",
        },
      }),
    ]);
  });

  it("renders account settings and models in the same expandable row", async () => {
    const wrapper = await mountEditor();
    const rows = wrapper.findAll("details");

    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain("Account name");
    expect(rows[0]!.text()).toContain("Models");
    expect(rows[0]!.findAll("textarea")).toHaveLength(1);
    expect(rows[0]!.find("summary").text()).toContain("2 models");
  });

  it("adds and deletes database-backed provider rows", async () => {
    const wrapper = await mountEditor();

    await addAccount(wrapper, "claude-code", "login");
    expect(wrapper.findAll("details")).toHaveLength(3);
    expect(wrapper.get('[data-testid="provider-row-2"]').text()).toContain("claude");
    expect(wrapper.findAll('[data-testid="models-pending-save"]')).toHaveLength(1);

    const deleteButtons = wrapper
      .findAll("button")
      .filter((button) => button.text().includes("Delete"));
    await deleteButtons[2]!.trigger("click");
    expect(wrapper.findAll("details")).toHaveLength(2);
  });

  it("saves account credentials and model changes with one action", async () => {
    fetchModels.mockResolvedValueOnce(models).mockResolvedValueOnce({
      accounts: models.accounts.map((account) =>
        account.account === "default" ? { ...account, account: "primary" } : account,
      ),
    });
    const wrapper = await mountEditor();
    const first = wrapper.findAll("details")[0]!;
    await first.findAll("input")[0]!.setValue("primary");
    await first.findAll("textarea")[0]!.setValue("ANTHROPIC_AUTH_TOKEN=secret");
    await first.find('[data-testid="custom-model-primary"]').setValue("model-a");
    await first.find("form").trigger("submit");

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();

    expect(saveProviders).toHaveBeenCalledWith([
      expect.objectContaining({
        name: "primary",
        env: { ANTHROPIC_AUTH_TOKEN: "secret" },
      }),
      expect.objectContaining({ name: "codex-qwen" }),
    ]);
    expect(saveModels).toHaveBeenCalledWith({
      primary: {
        models: ["claude-opus-5-5[1m]", "claude-sonnet-5", "model-a"],
        summary_model: "claude-haiku-5",
        specs: {},
      },
      "codex-qwen": { models: ["Qwen/Qwen3"], summary_model: "Qwen/Qwen3", specs: {} },
    });
    expect(saveTransports).not.toHaveBeenCalled();
  });

  it("keeps save disabled until either account or model settings change", async () => {
    const wrapper = await mountEditor();
    const save = wrapper.get('[data-testid="save-providers"]');

    expect(save.attributes("disabled")).toBeDefined();
    await wrapper
      .findAll("details")[1]!
      .find('[data-testid="custom-model-codex-qwen"]')
      .setValue("acme/reasoner-v2");
    await wrapper.findAll("details")[1]!.find("form").trigger("submit");
    expect(save.attributes("disabled")).toBeUndefined();
  });

  it("offers reference model names as a hint without pre-filling any", async () => {
    fetchModels.mockResolvedValue({
      accounts: [{ ...models.accounts[0]!, models: [], summary_model: "", overridden: false }],
    });
    fetchProviders.mockResolvedValue({ providers: [providers.providers[0]!] });
    const wrapper = await mountEditor();

    expect(wrapper.find('[data-testid="model-suggestions-0"]').text()).toContain(
      "claude-opus-5-5, claude-sonnet-5, claude-haiku-4-5",
    );
    expect(wrapper.find('[data-testid="models-empty-default"]').exists()).toBe(true);
    expect(wrapper.find("summary").text()).toContain("0 models");
  });

  it("reorders models so the first becomes the default", async () => {
    const wrapper = await mountEditor();
    const first = wrapper.findAll("details")[0]!;
    await first.findAll('[data-testid="model-move-down"]')[0]!.trigger("click");

    const order = first
      .findAll('[data-testid="model-list-default"] li')
      .map((li) => li.attributes("data-model-value"));
    expect(order).toEqual(["claude-sonnet-5", "claude-opus-5-5[1m]"]);

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveModels).toHaveBeenCalledWith(
      expect.objectContaining({
        default: {
          models: ["claude-sonnet-5", "claude-opus-5-5[1m]"],
          summary_model: "claude-haiku-5",
          specs: {},
        },
      }),
    );
  });

  it("stores bare Opus ids as their 1M-context variants", async () => {
    const wrapper = await mountEditor();
    const first = wrapper.findAll("details")[0]!;
    await first.find('[data-testid="custom-model-default"]').setValue("claude-opus-5");
    await first.find("form").trigger("submit");

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();

    expect(saveModels).toHaveBeenCalledWith(
      expect.objectContaining({
        default: {
          models: ["claude-opus-5-5[1m]", "claude-sonnet-5", "claude-opus-5[1m]"],
          summary_model: "claude-haiku-5",
          specs: {},
        },
      }),
    );
  });

  it("adds a Codex API-key account preset for OpenAI's Responses endpoint", async () => {
    const wrapper = await mountEditor();
    await addAccount(wrapper, "codex", "api-key", "openai");
    const row = wrapper.get('[data-testid="provider-row-2"]');
    expect(row.text()).toContain("Codex · API key");
    expect(row.get('[data-testid="api-url-2"]').element).toHaveProperty(
      "value",
      "https://api.openai.com/v1",
    );
    expect(row.find('[data-testid="codex-home-hint-2"]').exists()).toBe(true);
    expect(row.get('[data-testid="model-list-openai-api"]').text()).toContain("gpt-5.6-sol");
  });

  it("switches framework and access mode independently", async () => {
    const wrapper = await mountEditor();
    await wrapper.get('[data-testid="access-0"]').setValue("api-key");
    await wrapper.get('[data-testid="framework-0"]').setValue("codex");
    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveProviders).toHaveBeenCalledWith([
      expect.objectContaining({ name: "default", type: "openai-compatible" }),
      expect.objectContaining({ name: "codex-qwen", type: "codex" }),
    ]);
  });

  it("saves a declared context window and text-only flag per model", async () => {
    const wrapper = await mountEditor();
    const qwen = wrapper.get('[data-testid="model-list-codex-qwen"]');
    await qwen.get('[data-testid="model-context-window"]').setValue("128K");
    await qwen.get('[data-testid="model-image-input"]').setValue(false);

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveModels).toHaveBeenCalledWith(
      expect.objectContaining({
        "codex-qwen": {
          models: ["Qwen/Qwen3"],
          summary_model: "Qwen/Qwen3",
          specs: { "Qwen/Qwen3": { context_window: 128_000, no_image_input: true } },
        },
      }),
    );
  });

  it("refuses to save an unparseable context window", async () => {
    const wrapper = await mountEditor();
    await wrapper
      .get('[data-testid="model-list-codex-qwen"]')
      .get('[data-testid="model-context-window"]')
      .setValue("lots");

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();
    expect(saveModels).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain('Context window "lots" is invalid');
  });

  it("refuses to save an account with models but no summary model", async () => {
    const wrapper = await mountEditor();
    await wrapper.find('[data-testid="summary-model-default"]').setValue("");

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();

    expect(saveModels).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("Account default has models — set its summary model.");
  });

  it("switches a provider type's transport and saves only the change", async () => {
    const wrapper = await mountEditor();
    const select = wrapper.find('[data-testid="transport-0"]');
    expect(select.element.tagName).toBe("SELECT");
    await select.setValue("cli");
    expect(wrapper.find('[data-testid="transport-hint-0"]').text()).toContain("queued");

    await wrapper.get('[data-testid="save-providers"]').trigger("click");
    await flushPromises();

    expect(saveTransports).toHaveBeenCalledWith({ claude: "cli" });
    expect(saveModels).not.toHaveBeenCalled();
  });
});

describe("AdminProvidersEditor account status", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fetchProviders.mockResolvedValue(providers);
    saveProviders.mockImplementation(async (rows) => ({ providers: rows }));
    fetchModels.mockResolvedValue(models);
    fetchTransports.mockResolvedValue(transports);
  });

  // Checking or logging in runs against what the server has saved, so an
  // edited row must be saved first rather than silently test stale settings.
  it("offers check and login per account and locks them while the row has edits", async () => {
    const wrapper = await mountEditor();
    const first = wrapper.findAll("details")[0]!;
    const check = first.find('[data-testid="account-check"]');
    expect(check.attributes("disabled")).toBeUndefined();
    expect(first.find('[data-testid="account-login"]').exists()).toBe(true);

    await first.findAll("input")[0]!.setValue("renamed");
    expect(first.find('[data-testid="account-check"]').attributes("disabled")).toBeDefined();
    expect(first.find('[data-testid="account-status-unsaved"]').exists()).toBe(true);
  });

  it("tests the summary model currently typed in the field", async () => {
    checkSummaryModel.mockResolvedValue({
      account: "default",
      model: "claude-haiku-5",
      ok: false,
      error: "There's an issue with the selected model (claude-haiku-5).",
    });
    const wrapper = await mountEditor();
    const first = wrapper.findAll("details")[0]!;

    await first.get('[data-testid="summary-test-default"]').trigger("click");
    await flushPromises();

    expect(checkSummaryModel).toHaveBeenCalledWith("default", "claude-haiku-5");
    const result = first.get('[data-testid="summary-test-result-default"]');
    expect(result.text()).toContain("selected model (claude-haiku-5)");

    // Editing the field retires the verdict that no longer describes it.
    await first.get('[data-testid="summary-model-default"]').setValue("claude-haiku-4-5");
    expect(first.find('[data-testid="summary-test-result-default"]').exists()).toBe(false);
  });

  it("disables the summary test until the account is saved", async () => {
    const wrapper = await mountEditor();
    const first = wrapper.findAll("details")[0]!;
    await first.findAll("input")[0]!.setValue("renamed");
    expect(first.get('[data-testid="summary-test-renamed"]').attributes("disabled")).toBeDefined();
  });
});
