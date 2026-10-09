import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import CronSettings from "./CronSettings.vue";

const mockFetchJobs = vi.fn();
const mockFetchUsers = vi.fn();
const mockCreate = vi.fn();
const mockUpdate = vi.fn();
const mockDelete = vi.fn();
const mockFetchModels = vi.fn();
const mockFetchAgentBots = vi.hoisted(() => vi.fn());

vi.mock("@/composables/useApi", () => ({
  fetchCronJobs: (...args: unknown[]) => mockFetchJobs(...args),
  fetchUsers: (...args: unknown[]) => mockFetchUsers(...args),
  createCronJob: (...args: unknown[]) => mockCreate(...args),
  updateCronJob: (...args: unknown[]) => mockUpdate(...args),
  deleteCronJob: (...args: unknown[]) => mockDelete(...args),
}));
vi.mock("@/composables/apiModels", () => ({
  fetchModels: (...args: unknown[]) => mockFetchModels(...args),
}));
vi.mock("@/composables/apiUsers", () => ({
  fetchAgentBots: (...args: unknown[]) => mockFetchAgentBots(...args),
}));

const mockConfirm = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}));

function makeRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings", name: "settings", component: { template: "<div/>" } },
      {
        path: "/settings/crontab",
        name: "settings-crontab",
        component: CronSettings,
      },
      {
        path: "/chat/:userId/:conversationId",
        name: "chat",
        component: { template: "<div/>" },
      },
    ],
  });
  router.push("/settings/crontab");
  return router;
}

async function mountPage() {
  const router = makeRouter();
  await router.isReady();
  const wrapper = mount(CronSettings, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

beforeEach(() => {
  mockFetchJobs.mockReset();
  mockFetchUsers.mockReset();
  mockCreate.mockReset();
  mockUpdate.mockReset();
  mockDelete.mockReset();
  mockFetchModels.mockReset();
  mockFetchAgentBots.mockReset();
  mockFetchAgentBots.mockResolvedValue([]);
  mockConfirm.mockReset();
  mockConfirm.mockResolvedValue(true);
  mockFetchUsers.mockResolvedValue([
    { id: "agent-1", name: "Researcher", username: "", archived: false },
  ]);
  mockFetchJobs.mockResolvedValue([]);
  mockFetchModels.mockResolvedValue({
    providers: [
      {
        name: "claude",
        models: ["claude-sonnet", "claude-opus"],
        latest: "claude-sonnet",
        capabilities: {},
      },
    ],
    default_provider: "claude",
  });
});

describe("CronSettings", () => {
  it("uses consistent custom chevrons for every dropdown", async () => {
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");

    const selects = wrapper.findAll("select");
    expect(selects).toHaveLength(2);
    for (const select of selects) {
      expect(select.classes()).toContain("appearance-none");
      expect(select.classes()).toContain("pr-10");
    }
    expect(wrapper.findAll('[data-testid="cron-select-chevron"]')).toHaveLength(2);

    await wrapper.get('[data-testid="cron-deliver-to-bot"]').trigger("click");

    expect(wrapper.get('[data-testid="cron-delivery-bot"]').classes()).toContain("appearance-none");
    const chevrons = wrapper.findAll('[data-testid="cron-select-chevron"]');
    expect(chevrons).toHaveLength(3);
    expect(chevrons.every((chevron) => chevron.attributes("aria-hidden") === "true")).toBe(true);
  });

  it("creates a scheduled task with an Agent, cron expression, timezone, and prompt", async () => {
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    expect(add).toBeTruthy();
    await add!.trigger("click");

    await wrapper.get('[data-testid="cron-expression"]').setValue("15 8 * * 1-5");
    await wrapper.get('[data-testid="cron-timezone"]').setValue("Asia/Shanghai");
    await wrapper.get('[data-testid="cron-model"]').setValue("claude-opus");
    await wrapper.get('[data-testid="cron-description"]').setValue("Weekday build summary");
    await wrapper
      .get('[data-testid="cron-prompt"]')
      .setValue("Review the overnight build and summarize failures.");

    mockCreate.mockResolvedValue({
      id: "cron-1",
      owner_id: "owner-1",
      agent_id: "agent-1",
      agent_name: "Researcher",
      model: "claude-opus",
      expression: "15 8 * * 1-5",
      timezone: "Asia/Shanghai",
      description: "Weekday build summary",
      prompt: "Review the overnight build and summarize failures.",
      enabled: true,
      created_at: "2026-07-24T00:00:00Z",
      updated_at: "2026-07-24T00:00:00Z",
    });

    const save = wrapper.findAll("button").find((button) => button.text() === "Save task");
    expect(save).toBeTruthy();
    await save!.trigger("click");
    await flushPromises();

    expect(mockCreate).toHaveBeenCalledWith({
      agent_id: "agent-1",
      model: "claude-opus",
      expression: "15 8 * * 1-5",
      timezone: "Asia/Shanghai",
      description: "Weekday build summary",
      prompt: "Review the overnight build and summarize failures.",
      enabled: true,
      notifications_enabled: true,
      deliver_to_bot: false,
      bot_id: "",
    });
    expect(wrapper.get('[data-testid="cron-job-description"]').text()).toBe(
      "Weekday build summary",
    );
    expect(wrapper.find('[data-testid="cron-job-prompt-preview"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain("Review the overnight build");
  });

  it("shows only a clamped prompt preview when a task has no description", async () => {
    mockFetchJobs.mockResolvedValue([
      {
        id: "cron-1",
        owner_id: "owner-1",
        agent_id: "agent-1",
        agent_name: "Researcher",
        model: "",
        expression: "0 9 * * *",
        timezone: "UTC",
        description: "",
        prompt: "First line\nSecond line\nThird line\nFourth line",
        enabled: true,
        created_at: "2026-07-24T00:00:00Z",
        updated_at: "2026-07-24T00:00:00Z",
      },
    ]);

    const { wrapper } = await mountPage();
    const preview = wrapper.get('[data-testid="cron-job-prompt-preview"]');

    expect(wrapper.find('[data-testid="cron-job-description"]').exists()).toBe(false);
    expect(preview.text()).toContain("First line");
    expect(preview.classes()).toContain("line-clamp-3");
  });

  it("can disable completion notifications for a scheduled task", async () => {
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");
    await wrapper.get('[data-testid="cron-prompt"]').setValue("Quiet maintenance.");
    await wrapper.get('[data-testid="cron-notifications-enabled"]').trigger("click");
    mockCreate.mockResolvedValue({ id: "cron-1", notifications_enabled: false });

    const save = wrapper.findAll("button").find((button) => button.text() === "Save task");
    await save!.trigger("click");
    await flushPromises();

    expect(mockCreate).toHaveBeenCalledWith(
      expect.objectContaining({ notifications_enabled: false, prompt: "Quiet maintenance." }),
    );
  });

  it("opts a scheduled task into delivering its answer to the bound bot", async () => {
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");
    await wrapper.get('[data-testid="cron-prompt"]').setValue("Summarize yesterday's incidents.");
    await wrapper.get('[data-testid="cron-deliver-to-bot"]').trigger("click");

    mockCreate.mockResolvedValue({
      id: "cron-1",
      owner_id: "owner-1",
      agent_id: "agent-1",
      agent_name: "Researcher",
      model: "",
      expression: "0 9 * * 1-5",
      timezone: "UTC",
      prompt: "Summarize yesterday's incidents.",
      enabled: true,
      deliver_to_bot: true,
      created_at: "2026-07-24T00:00:00Z",
      updated_at: "2026-07-24T00:00:00Z",
    });
    const save = wrapper.findAll("button").find((button) => button.text() === "Save task");
    await save!.trigger("click");
    await flushPromises();

    expect(mockCreate).toHaveBeenCalledWith(
      expect.objectContaining({ deliver_to_bot: true, prompt: "Summarize yesterday's incidents." }),
    );
  });

  it("pins delivery to the chosen bot instead of the most recent thread", async () => {
    mockFetchAgentBots.mockResolvedValue([
      { id: "bot-1", agent_id: "agent-1", name: "Ops", platform: "slack", enabled: true },
      { id: "bot-2", agent_id: "agent-1", name: "Reports", platform: "feishu", enabled: true },
    ]);
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");
    await wrapper.get('[data-testid="cron-prompt"]').setValue("Daily digest.");
    await wrapper.get('[data-testid="cron-deliver-to-bot"]').trigger("click");
    await flushPromises();

    await wrapper.get('[data-testid="cron-delivery-bot"]').setValue("bot-2");
    mockCreate.mockResolvedValue({ id: "cron-1", agent_id: "agent-1", bot_id: "bot-2" });
    const save = wrapper.findAll("button").find((button) => button.text() === "Save task");
    await save!.trigger("click");
    await flushPromises();

    expect(mockCreate).toHaveBeenCalledWith(
      expect.objectContaining({ deliver_to_bot: true, bot_id: "bot-2" }),
    );
  });

  it("offers only enabled bots as delivery targets", async () => {
    mockFetchAgentBots.mockResolvedValue([
      { id: "bot-1", agent_id: "agent-1", name: "Ops", platform: "slack", enabled: true },
      { id: "bot-2", agent_id: "agent-1", name: "Retired", platform: "feishu", enabled: false },
    ]);
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");
    await wrapper.get('[data-testid="cron-deliver-to-bot"]').trigger("click");
    await flushPromises();

    // A disabled bot has no live connector, so offering it would be a target
    // that silently never posts.
    const options = wrapper.get('[data-testid="cron-delivery-bot"]').findAll("option");
    expect(options.map((o) => o.text())).toEqual([
      "Automatic · the Agent's most recent thread",
      "Ops · slack",
    ]);
  });

  it("drops a pinned bot that does not belong to the newly chosen Agent", async () => {
    mockFetchUsers.mockResolvedValue([
      { id: "agent-1", name: "Researcher", username: "", archived: false },
      { id: "agent-2", name: "Builder", username: "", archived: false },
    ]);
    mockFetchAgentBots.mockImplementation(async (agentID: string) =>
      agentID === "agent-1"
        ? [{ id: "bot-1", agent_id: "agent-1", name: "Ops", platform: "slack", enabled: true }]
        : [],
    );
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");
    await wrapper.get('[data-testid="cron-deliver-to-bot"]').trigger("click");
    await flushPromises();
    await wrapper.get('[data-testid="cron-delivery-bot"]').setValue("bot-1");

    // The backend rejects a bot that is not the target Agent's, so a stale
    // selection would turn an Agent switch into an unexplained save failure.
    await wrapper.get('[data-testid="cron-agent"]').setValue("agent-2");
    await flushPromises();

    expect(
      (wrapper.get('[data-testid="cron-delivery-bot"]').element as HTMLSelectElement).value,
    ).toBe("");
  });

  it("warns when the chosen Agent has never talked to a bot", async () => {
    const { wrapper } = await mountPage();
    const add = wrapper.findAll("button").find((button) => button.text() === "Add task");
    await add!.trigger("click");

    expect(wrapper.find('[data-testid="cron-deliver-unbound"]').exists()).toBe(false);
    await wrapper.get('[data-testid="cron-deliver-to-bot"]').trigger("click");
    expect(wrapper.find('[data-testid="cron-deliver-unbound"]').exists()).toBe(true);
  });

  it("keeps bot delivery on when the row switch pauses a task", async () => {
    mockFetchJobs.mockResolvedValue([
      {
        id: "cron-1",
        owner_id: "owner-1",
        agent_id: "agent-1",
        agent_name: "Researcher",
        model: "",
        expression: "0 9 * * *",
        timezone: "UTC",
        prompt: "Prepare the report",
        enabled: true,
        deliver_to_bot: true,
        created_at: "2026-07-24T00:00:00Z",
        updated_at: "2026-07-24T00:00:00Z",
      },
    ]);
    mockUpdate.mockImplementation(async (_id: string, input: Record<string, unknown>) => ({
      id: "cron-1",
      owner_id: "owner-1",
      agent_id: "agent-1",
      agent_name: "Researcher",
      created_at: "2026-07-24T00:00:00Z",
      updated_at: "2026-07-24T00:00:00Z",
      ...input,
    }));
    const { wrapper } = await mountPage();

    // The row switch PUTs a whole job, so a field it omits comes back cleared.
    await wrapper.get('[aria-label="Enabled"]').trigger("click");
    await flushPromises();

    expect(mockUpdate).toHaveBeenCalledWith(
      "cron-1",
      expect.objectContaining({ enabled: false, deliver_to_bot: true }),
    );
  });

  it("opens the latest scheduled run in Chat", async () => {
    mockFetchJobs.mockResolvedValue([
      {
        id: "cron-1",
        owner_id: "owner-1",
        agent_id: "agent-1",
        agent_name: "Researcher",
        model: "",
        expression: "0 9 * * *",
        timezone: "UTC",
        prompt: "Prepare the report",
        enabled: true,
        last_conversation_id: "conv-1",
        created_at: "2026-07-24T00:00:00Z",
        updated_at: "2026-07-24T00:00:00Z",
      },
    ]);
    const { wrapper, router } = await mountPage();

    const open = wrapper.findAll("button").find((button) => button.text() === "Open chat");
    expect(open).toBeTruthy();
    await open!.trigger("click");
    await flushPromises();

    expect(router.currentRoute.value.name).toBe("chat");
    expect(router.currentRoute.value.params).toMatchObject({
      userId: "agent-1",
      conversationId: "conv-1",
    });
  });

  it("explains why a task was disabled after its Agent became unavailable", async () => {
    mockFetchJobs.mockResolvedValue([
      {
        id: "cron-1",
        owner_id: "owner-1",
        agent_id: "agent-1",
        agent_name: "Researcher",
        model: "",
        expression: "0 9 * * *",
        timezone: "UTC",
        prompt: "Prepare the report",
        enabled: false,
        disabled_reason: "agent_unavailable",
        created_at: "2026-07-24T00:00:00Z",
        updated_at: "2026-07-24T00:00:00Z",
      },
    ]);
    const { wrapper } = await mountPage();

    expect(wrapper.text()).toContain(
      "This task was disabled automatically because its Agent was archived or deleted.",
    );
  });
});
