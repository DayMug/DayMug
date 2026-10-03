import { beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import { ref } from "vue";
import UsageStats from "./UsageStats.vue";
import type { UsageInsightsResponse } from "@/composables/useApi";

const adminFetchUsageInsights = vi.fn();
const fetchUsageInsights = vi.fn();
const authMe = ref<{ is_admin: boolean } | null>({ is_admin: true });

vi.mock("@/composables/useApi", () => ({
  adminFetchUsageInsights: (...args: unknown[]) => adminFetchUsageInsights(...args),
  fetchUsageInsights: (...args: unknown[]) => fetchUsageInsights(...args),
}));

vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({ authMe }),
}));

function fixture(overrides: Partial<UsageInsightsResponse> = {}): UsageInsightsResponse {
  return {
    summary: {
      total_tokens: 1_250_000_000,
      cost_usd: 12.5,
      user_instructions: 7,
      active_conversations: 3,
      model_requests: 22,
      tool_calls: 41,
      cache_read_ratio: 0.96,
      historical_estimate: false,
      comparison: {
        total_tokens: 12,
        cost_usd: -5,
        user_instructions: 0,
        active_conversations: null,
        model_requests: 4,
        tool_calls: 3,
        cache_read_ratio: 1,
      },
    },
    composition: [
      {
        key: "2026-09-24",
        label: "2026-09-24",
        input_tokens: 100,
        cache_read_input_tokens: 9600,
        cache_creation_input_tokens: 100,
        output_tokens: 200,
        reasoning_output_tokens: 50,
        cost_usd: 12.5,
      },
    ],
    rankings: [
      {
        key: "conv-1",
        owner_id: "owner-1",
        owner_name: "Alice",
        agent_id: "agent-1",
        agent_name: "Research agent",
        conversation_id: "conv-1",
        conversation_title: "Investigate cache growth",
        cron_job_id: "",
        cron_job_name: "",
        provider: "claude",
        model: "opus",
        source_type: "manual",
        user_instructions: 1,
        model_requests: 22,
        tool_calls: 41,
        input_tokens: 100,
        cache_read_input_tokens: 9600,
        cache_creation_input_tokens: 100,
        output_tokens: 200,
        reasoning_output_tokens: 0,
        cache_read_ratio: 0.96,
        cost_usd: 12.5,
        context_usage_ratio: 0.85,
        last_activity: "2026-09-24 09:00:00",
        historical_estimate: false,
        request_count_scope: "provider_reported",
        anomalies: [{ code: "high_cache_read", value: 0.96, threshold: 0.95 }],
      },
    ],
    facets: {
      users: [{ id: "owner-1", label: "Alice" }],
      agents: [{ id: "agent-1", label: "Research agent" }],
      providers: [{ id: "claude", label: "claude" }],
      models: [{ id: "opus", label: "opus" }],
      conversations: [{ id: "conv-1", label: "Investigate cache growth" }],
      cron_jobs: [],
    },
    page: 1,
    page_size: 20,
    total_rows: 42,
    timezone: "Asia/Shanghai",
    group_by: "day",
    rank_by: "conversation",
    thresholds: {
      cache_read_ratio: 0.95,
      model_requests: 20,
      tool_calls: 30,
      context_warning_ratio: 0.8,
      conversation_cost_usd: 10,
    },
    ...overrides,
  };
}

async function mountPage(initial = "/settings/usage") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/settings/usage", name: "settings-usage", component: UsageStats },
      { path: "/chat/:userId/:conversationId", name: "chat", component: { template: "<div/>" } },
    ],
  });
  await router.push(initial);
  await router.isReady();
  const wrapper = mount(UsageStats, { global: { plugins: [router] } });
  await flushPromises();
  return { wrapper, router };
}

beforeEach(() => {
  vi.clearAllMocks();
  authMe.value = { is_admin: true };
  adminFetchUsageInsights.mockResolvedValue(fixture());
  fetchUsageInsights.mockResolvedValue(fixture());
});

describe("UsageStats", () => {
  it("renders explanatory summary cards and K/M/B formatting", async () => {
    const { wrapper } = await mountPage();
    expect(adminFetchUsageInsights).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain("1.3B");
    expect(wrapper.text()).toContain("Includes cache tokens");
    expect(wrapper.text()).toContain("User instructions");
    expect(wrapper.text()).toContain("Model requests");
    expect(wrapper.text()).toContain("Cache read share");
  });

  it("restores filters from the URL and persists later changes", async () => {
    const { wrapper, router } = await mountPage(
      "/settings/usage?start=2026-09-01&provider=claude&group_by=model&rank_by=agent",
    );
    expect(adminFetchUsageInsights.mock.calls[0][0]).toMatchObject({
      start: "2026-09-01",
      provider: "claude",
      group_by: "model",
      rank_by: "agent",
    });
    const sourceSelect = wrapper
      .findAll("label")
      .find((label) => label.text().includes("Source"))!
      .find("select");
    await sourceSelect.setValue("cron");
    await flushPromises();
    expect(router.currentRoute.value.query.source_type).toBe("cron");
    expect(adminFetchUsageInsights.mock.calls.at(-1)?.[0]).toMatchObject({ source_type: "cron" });
  });

  it("changes ranking, sorting, and pages through server queries", async () => {
    const { wrapper } = await mountPage();
    const userTab = wrapper.findAll('[role="tab"]').find((button) => button.text() === "Users")!;
    await userTab.trigger("click");
    await flushPromises();
    expect(adminFetchUsageInsights.mock.calls.at(-1)?.[0]).toMatchObject({
      rank_by: "user",
      page: 1,
    });

    await wrapper.get('[data-testid="sort-order"]').trigger("click");
    await flushPromises();
    expect(adminFetchUsageInsights.mock.calls.at(-1)?.[0]).toMatchObject({ sort_order: "asc" });

    const next = wrapper.findAll("button").find((button) => button.text() === "Next")!;
    await next.trigger("click");
    await flushPromises();
    expect(adminFetchUsageInsights.mock.calls.at(-1)?.[0]).toMatchObject({ page: 2 });
  });

  it("renders an actionable anomaly and opens its conversation", async () => {
    const { wrapper, router } = await mountPage();
    expect(wrapper.get('[data-testid="usage-anomalies"]').text()).toContain("Compact");
    await wrapper.find("tbody button").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.fullPath).toBe("/chat/agent-1/conv-1");
  });

  it("omits the historical warning and keeps the guided empty state", async () => {
    adminFetchUsageInsights.mockResolvedValueOnce(
      fixture({
        summary: { ...fixture().summary, historical_estimate: true },
        composition: [],
        rankings: [],
        total_rows: 0,
      }),
    );
    const { wrapper } = await mountPage();
    expect(wrapper.find('[data-testid="historical-warning"]').exists()).toBe(false);
    expect(wrapper.text()).toContain("Widen the date range");
  });

  it("uses the owner-scoped endpoint for non-admins", async () => {
    authMe.value = { is_admin: false };
    await mountPage();
    expect(fetchUsageInsights).toHaveBeenCalledOnce();
    expect(adminFetchUsageInsights).not.toHaveBeenCalled();
  });
});
