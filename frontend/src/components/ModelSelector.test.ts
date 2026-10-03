import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import ModelSelector from "./ModelSelector.vue";
import { invalidateModelRegistry, loadModelRegistry } from "@/composables/useModelRegistry";
import type { Conversation } from "@/composables/apiTypes";
import { RECENT_MODEL_STORAGE_KEY } from "@/lib/recentModel";

const fetchModelsMock = vi.fn();
const updateConversationModelMock = vi.fn();

vi.mock("@/composables/apiModels", () => ({
  fetchModels: (...args: unknown[]) => fetchModelsMock(...args),
}));

vi.mock("@/composables/apiConversations", () => ({
  updateConversationModel: (...args: unknown[]) => updateConversationModelMock(...args),
}));

const baseConversation = (over: Partial<Conversation> = {}): Conversation => ({
  id: "c1",
  user_id: "u1",
  title: "",
  provider: "claude",
  model: "claude-opus-4-8",
  think_level: "high",
  account_name: "default",
  work_dir: "",
  session_id: "session-uuid",
  notifications_enabled: true,
  pinned: false,
  pin_order: 0,
  created_at: "",
  updated_at: "",
  ...over,
});

const claudeCaps = {
  supports_compaction: true,
  supports_thinking_stream: true,
  supports_rate_limit_events: true,
  reports_context_usage: true,
  reports_cost_usd: true,
};
const codexCaps = {
  supports_compaction: false,
  supports_thinking_stream: false,
  supports_rate_limit_events: false,
  reports_context_usage: false,
  reports_cost_usd: false,
};
// Registry mirrors the multi-account shape: codex has two accounts with
// disjoint model lists (stock gpt-5.x vs a local Qwen), which is exactly the
// case the per-account level-2 list exists to disambiguate.
const registry = {
  providers: [
    {
      name: "claude",
      models: ["claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5"],
      latest: "claude-opus-4-8",
      capabilities: claudeCaps,
    },
    {
      name: "codex",
      models: [
        "gpt-5.6",
        "gpt-5.6-sol",
        "gpt-5.6-terra",
        "gpt-5.6-luna",
        "gpt-5.5",
        "gpt-5.4",
        "gpt-5.4-mini",
        "Qwen/Qwen3",
      ],
      latest: "gpt-5.6",
      capabilities: codexCaps,
    },
  ],
  accounts: [
    {
      provider: "claude",
      account: "default",
      models: ["claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5"],
      latest: "claude-opus-4-8",
    },
    {
      provider: "codex",
      account: "codex",
      models: [
        "gpt-5.6",
        "gpt-5.6-sol",
        "gpt-5.6-terra",
        "gpt-5.6-luna",
        "gpt-5.5",
        "gpt-5.4",
        "gpt-5.4-mini",
      ],
      latest: "gpt-5.6",
    },
    {
      provider: "codex",
      account: "codex-qwen",
      models: ["Qwen/Qwen3"],
      latest: "Qwen/Qwen3",
    },
  ],
  default_provider: "claude",
};

// Level-1 rows are addressed by the "{type}\n{account}" composite.
const acctValue = (provider: string, account: string) => `${provider}\n${account}`;

type Props = InstanceType<typeof ModelSelector>["$props"];

function mountSelector(props: Partial<Props> = {}) {
  return mount(ModelSelector, {
    props: {
      conversation: baseConversation(),
      hasMessages: false,
      providerAccounts: { claude: ["default"] },
      ...props,
    } as Props,
  });
}

async function openMenu(wrapper: VueWrapper) {
  await wrapper.get('[data-testid="model-selector-trigger"]').trigger("click");
  await flushPromises();
}

function accountRow(wrapper: VueWrapper, provider: string, account: string) {
  return wrapper.get(
    `[data-testid="model-account-row"][data-value="${acctValue(provider, account)}"]`,
  );
}

function modelValues(wrapper: VueWrapper) {
  return wrapper
    .findAll('[data-testid="model-option"]')
    .map((b) => b.attributes("data-value") ?? "");
}

beforeEach(() => {
  // The registry cache is module-scope (shared across every instance), so each
  // case has to start from a cold one.
  invalidateModelRegistry();
  fetchModelsMock.mockReset();
  updateConversationModelMock.mockReset();
  fetchModelsMock.mockResolvedValue(registry);
  localStorage.removeItem(RECENT_MODEL_STORAGE_KEY);
});

describe("ModelSelector", () => {
  // The whole reason this control collapsed into a menu: three dropdowns on
  // the composer's control line never fit a phone, and the flex row resolved
  // the overflow by squeezing them toward zero width.
  it("collapses the whole profile into one compact trigger", async () => {
    const wrapper = mountSelector({
      conversation: baseConversation(),
      providerAccounts: { claude: ["default"], codex: ["codex"] },
    });
    await flushPromises();

    expect(wrapper.findAll("button")).toHaveLength(1);
    const trigger = wrapper.get('[data-testid="model-selector-trigger"]');
    expect(trigger.get('[data-testid="model-selector-model"]').text()).toBe("claude-opus-4-8");
    expect(trigger.get('[data-testid="model-selector-account"]').text()).toBe("default");
    expect(trigger.get('[data-testid="model-selector-think"]').text()).toBe("High");
    expect(wrapper.find('[data-testid="model-selector-menu"]').exists()).toBe(false);
  });

  // A single bound account is not a choice, so its label is pure noise on a
  // line the send button also has to fit on.
  it("drops the account prefix from the trigger when there is nothing to switch to", async () => {
    const wrapper = mountSelector();
    await flushPromises();
    expect(wrapper.find('[data-testid="model-selector-account"]').exists()).toBe(false);
  });

  it("hides the thinking badge while the provider default is in effect", async () => {
    const wrapper = mountSelector({ conversation: baseConversation({ think_level: "" }) });
    await flushPromises();
    expect(wrapper.find('[data-testid="model-selector-think"]').exists()).toBe(false);
  });

  it("lists one level-1 row per bound account and unfolds the current one", async () => {
    const wrapper = mountSelector({
      conversation: baseConversation({
        provider: "codex",
        model: "gpt-5.5",
        account_name: "codex",
      }),
      providerAccounts: { claude: ["default"], codex: ["codex", "codex-qwen"] },
    });
    await flushPromises();
    await openMenu(wrapper);

    const rows = wrapper.findAll('[data-testid="model-account-row"]');
    expect(rows.map((r) => r.text())).toEqual(["default", "codex", "codex-qwen"]);
    // Opening lands on the account in use, so its models are one tap away.
    expect(modelValues(wrapper)).toContain("gpt-5.5");
    expect(modelValues(wrapper)).not.toContain("claude-sonnet-5");
  });

  it("scopes the level-2 model list to the account whose row was expanded", async () => {
    const wrapper = mountSelector({
      conversation: baseConversation({
        provider: "codex",
        model: "gpt-5.5",
        account_name: "codex",
      }),
      providerAccounts: { codex: ["codex", "codex-qwen"] },
    });
    await flushPromises();
    await openMenu(wrapper);

    await accountRow(wrapper, "codex", "codex-qwen").trigger("click");
    expect(modelValues(wrapper)).toEqual(["Qwen/Qwen3"]);
  });

  it("resolves the current row from any bound provider type, not just the first", async () => {
    const wrapper = mountSelector({
      conversation: baseConversation({
        provider: "codex",
        model: "Qwen/Qwen3",
        account_name: "codex-qwen",
      }),
      providerAccounts: { claude: ["default"], codex: ["codex", "codex-qwen"] },
    });
    await flushPromises();
    await openMenu(wrapper);

    expect(modelValues(wrapper)).toEqual(["Qwen/Qwen3"]);
  });

  it("hides bindings whose provider account was removed from the live registry", async () => {
    const wrapper = mountSelector({
      conversation: baseConversation({
        provider: "codex",
        model: "gpt-5.6-sol",
        account_name: "codex",
      }),
      providerAccounts: { codex: ["codex", "removed-codex"] },
    });
    await flushPromises();
    await openMenu(wrapper);

    expect(wrapper.text()).not.toContain("removed-codex");
  });

  it("defaults to the user's first bound account when the conversation pins none", async () => {
    const wrapper = mountSelector({
      conversation: baseConversation({ account_name: "" }),
      providerAccounts: { claude: ["default", "ollama"] },
    });
    await flushPromises();
    await openMenu(wrapper);

    expect(modelValues(wrapper)).toContain("claude-opus-4-8");
  });

  // The point of merging the two levels: one tap pins the account and the
  // model together, so the control never has to guess a model on the user's
  // behalf after an account switch.
  it("sets provider, account and model in a single pick", async () => {
    updateConversationModelMock.mockResolvedValue(
      baseConversation({ provider: "codex", model: "Qwen/Qwen3", account_name: "codex-qwen" }),
    );
    const wrapper = mountSelector({
      conversation: baseConversation({
        provider: "codex",
        model: "gpt-5.5",
        account_name: "codex",
      }),
      providerAccounts: { codex: ["codex", "codex-qwen"] },
    });
    await flushPromises();
    await openMenu(wrapper);
    await accountRow(wrapper, "codex", "codex-qwen").trigger("click");
    await wrapper.get('[data-testid="model-option"]').trigger("click");
    await flushPromises();

    expect(updateConversationModelMock).toHaveBeenCalledWith(
      "c1",
      "codex",
      "Qwen/Qwen3",
      "codex-qwen",
      "high",
    );
  });

  it("switching the model keeps the pinned account and records it as recent", async () => {
    updateConversationModelMock.mockResolvedValue(baseConversation({ model: "claude-sonnet-5" }));
    const wrapper = mountSelector();
    await flushPromises();
    await openMenu(wrapper);
    await wrapper
      .get('[data-testid="model-option"][data-value="claude-sonnet-5"]')
      .trigger("click");
    await flushPromises();

    expect(updateConversationModelMock).toHaveBeenCalledWith(
      "c1",
      "claude",
      "claude-sonnet-5",
      "default",
      "high",
    );
    expect(wrapper.emitted("updated")?.length).toBe(1);
    expect(localStorage.getItem(RECENT_MODEL_STORAGE_KEY)).toBe(
      JSON.stringify({
        provider: "claude",
        model: "claude-sonnet-5",
        account: "default",
        think_level: "high",
      }),
    );
  });

  it("closes the menu once a model is picked", async () => {
    updateConversationModelMock.mockResolvedValue(baseConversation({ model: "claude-sonnet-5" }));
    const wrapper = mountSelector();
    await flushPromises();
    await openMenu(wrapper);
    await wrapper
      .get('[data-testid="model-option"][data-value="claude-sonnet-5"]')
      .trigger("click");

    expect(wrapper.find('[data-testid="model-selector-menu"]').exists()).toBe(false);
  });

  it("locks the other accounts once the conversation has messages", async () => {
    const wrapper = mountSelector({
      hasMessages: true,
      providerAccounts: { claude: ["default"], codex: ["codex"] },
    });
    await flushPromises();
    await openMenu(wrapper);

    expect(accountRow(wrapper, "codex", "codex").attributes("disabled")).toBeDefined();
    expect(accountRow(wrapper, "claude", "default").attributes("disabled")).toBeUndefined();
  });

  it("keeps the current account's models swappable after messages exist", async () => {
    updateConversationModelMock.mockResolvedValue(baseConversation({ model: "claude-haiku-4-5" }));
    const wrapper = mountSelector({ hasMessages: true });
    await flushPromises();
    await openMenu(wrapper);
    await wrapper
      .get('[data-testid="model-option"][data-value="claude-haiku-4-5"]')
      .trigger("click");
    await flushPromises();

    expect(updateConversationModelMock).toHaveBeenCalledWith(
      "c1",
      "claude",
      "claude-haiku-4-5",
      "default",
      "high",
    );
  });

  it("updates the thinking level without changing account or model", async () => {
    updateConversationModelMock.mockResolvedValue(baseConversation({ think_level: "low" }));
    const wrapper = mountSelector({ hasMessages: true });
    await flushPromises();
    await openMenu(wrapper);
    await wrapper.get('[data-testid="model-think-row"]').trigger("click");
    await wrapper.get('[data-testid="model-think-option"][data-value="low"]').trigger("click");
    await flushPromises();

    expect(updateConversationModelMock).toHaveBeenCalledWith(
      "c1",
      "claude",
      "claude-opus-4-8",
      "default",
      "low",
    );
  });

  // Accordion, not multi-open: two unfolded lists make the panel taller than a
  // phone's viewport and reintroduce the scrolling this redesign removed.
  it("keeps only one section unfolded at a time", async () => {
    const wrapper = mountSelector({ hasMessages: false });
    await flushPromises();
    await openMenu(wrapper);
    expect(modelValues(wrapper).length).toBeGreaterThan(0);

    await wrapper.get('[data-testid="model-think-row"]').trigger("click");
    expect(modelValues(wrapper)).toEqual([]);
    expect(wrapper.findAll('[data-testid="model-think-option"]')).toHaveLength(5);
  });

  it("emits an error message when the backend rejects the change", async () => {
    updateConversationModelMock.mockRejectedValue(
      new Error("cannot change provider after the conversation has started"),
    );
    const wrapper = mountSelector({
      conversation: baseConversation({
        provider: "codex",
        model: "gpt-5.5",
        account_name: "codex",
      }),
      providerAccounts: { codex: ["codex", "codex-qwen"] },
    });
    await flushPromises();
    await openMenu(wrapper);
    await accountRow(wrapper, "codex", "codex-qwen").trigger("click");
    await wrapper.get('[data-testid="model-option"]').trigger("click");
    await flushPromises();

    const errorEvents = wrapper.emitted("error");
    expect(errorEvents?.length).toBe(1);
    expect((errorEvents?.[0] as [string])[0]).toContain("cannot change provider");
  });

  // The cache used to live inside <script setup>, i.e. one copy per instance:
  // every open chat header paid for its own GET /api/models.
  it("fetches the registry once for concurrently mounted selectors", async () => {
    const first = mountSelector();
    const second = mountSelector();
    await flushPromises();

    expect(fetchModelsMock).toHaveBeenCalledTimes(1);
    // The instance that never fetched still renders from the shared registry.
    await openMenu(second);
    expect(modelValues(second)).toContain("claude-sonnet-5");
    expect(first.get('[data-testid="model-selector-model"]').text()).toBe("claude-opus-4-8");
  });

  // The picker used to keep a private cache that invalidateModelRegistry
  // couldn't reach, so an admin's model-list edit needed a hard refresh.
  it("picks up a registry invalidated after an admin provider edit", async () => {
    const wrapper = mountSelector();
    await flushPromises();
    await openMenu(wrapper);
    expect(modelValues(wrapper)).not.toContain("claude-fable-5-1");

    const claudeModels = ["claude-fable-5-1", "claude-opus-4-8"];
    fetchModelsMock.mockResolvedValue({
      ...registry,
      providers: registry.providers.map((p) =>
        p.name === "claude" ? { ...p, models: claudeModels } : p,
      ),
      accounts: registry.accounts.map((a) =>
        a.provider === "claude" ? { ...a, models: claudeModels } : a,
      ),
    });
    invalidateModelRegistry();
    await loadModelRegistry();
    await flushPromises();

    expect(modelValues(wrapper)).toContain("claude-fable-5-1");
  });

  it("does nothing when the picked model is already the current one", async () => {
    const wrapper = mountSelector();
    await flushPromises();
    await openMenu(wrapper);
    await wrapper
      .get('[data-testid="model-option"][data-value="claude-opus-4-8"]')
      .trigger("click");
    await flushPromises();

    expect(updateConversationModelMock).not.toHaveBeenCalled();
  });
});
