import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

import TaskActivityButton from "./TaskActivityButton.vue";
import {
  resetAgentActivityStore,
  runningConversations,
  waitingConversations,
  type RunningConversationActivity,
} from "@/stores/agentActivityStore";
import {
  applyAttention,
  resetConversationAttentionStore,
} from "@/stores/conversationAttentionStore";
import { conversations, resetConversationListStore } from "@/stores/conversationListStore";
import { bindConversationNavigator } from "@/stores/activeConversationStore";
import { resetUserStore, users } from "@/stores/userStore";
import type { Conversation, User } from "@/composables/useApi";

function activity(
  conversationId: string,
  agentId: string,
  extra: Partial<RunningConversationActivity> = {},
): RunningConversationActivity {
  return {
    conversation_id: conversationId,
    agent_id: agentId,
    agent_name: "Analyst",
    account_name: "acct",
    model: "m",
    started_at: new Date(Date.now() - 8 * 60_000).toISOString(),
    ...extra,
  };
}

async function openFeed() {
  const wrapper = mount(TaskActivityButton);
  await wrapper.get('[data-testid="mobile-activity-button"]').trigger("click");
  return wrapper;
}

describe("TaskActivityButton", () => {
  beforeEach(() => {
    users.value = [{ id: "mine", name: "Analyst" } as User];
  });

  afterEach(() => {
    resetAgentActivityStore();
    resetConversationAttentionStore();
    resetConversationListStore();
    resetUserStore();
    bindConversationNavigator(null);
  });

  it("stays closed until the dot is tapped", () => {
    const wrapper = mount(TaskActivityButton);
    expect(wrapper.find('[data-testid="mobile-activity-popover"]').exists()).toBe(false);
  });

  it("says so when nothing is going on", async () => {
    const wrapper = await openFeed();
    expect(wrapper.find('[data-testid="mobile-activity-empty"]').exists()).toBe(true);
  });

  it("lists waiting turns before running ones and unread results last", async () => {
    runningConversations.value = [activity("run", "mine")];
    waitingConversations.value = [activity("ask", "mine")];
    applyAttention(
      [
        { conversation_id: "done", agent_id: "mine", title: "Done job", state: "done", at: "" },
        { conversation_id: "ask", agent_id: "mine", title: "Needs me", state: "waiting", at: "" },
      ],
      { run: "Running job", ask: "Needs me" },
    );

    const wrapper = await openFeed();
    const items = wrapper.findAll('[data-testid="mobile-activity-item"]');

    expect(items.map((item) => item.attributes("data-kind"))).toEqual([
      "waiting",
      "running",
      "done",
    ]);
    expect(items[0].text()).toContain("Needs me");
    expect(items[0].text()).toContain("8m");
    expect(items[1].text()).toContain("Running job");
    expect(items[2].text()).toContain("Done job");
  });

  it("lists flagged conversations with no live job, questions first", async () => {
    applyAttention(
      [
        { conversation_id: "d", agent_id: "mine", title: "Done", state: "done", at: "" },
        { conversation_id: "e", agent_id: "mine", title: "Broke", state: "error", at: "" },
        { conversation_id: "w", agent_id: "mine", title: "Asked", state: "waiting", at: "" },
      ],
      {},
    );

    const wrapper = await openFeed();
    expect(
      wrapper.findAll('[data-testid="mobile-activity-item"]').map((i) => i.attributes("data-kind")),
    ).toEqual(["waiting", "error", "done"]);
  });

  it("leaves out work in agents this user cannot open", async () => {
    runningConversations.value = [activity("theirs", "someone-else")];

    const wrapper = await openFeed();
    expect(wrapper.findAll('[data-testid="mobile-activity-item"]')).toHaveLength(0);
  });

  it("falls back to the cached conversation title", async () => {
    conversations.value = [{ id: "run", title: "From the list" } as Conversation];
    runningConversations.value = [activity("run", "mine")];

    const wrapper = await openFeed();
    expect(wrapper.get('[data-testid="mobile-activity-item"]').text()).toContain("From the list");
  });

  it("opens the tapped conversation in its own agent and closes the feed", async () => {
    const navigate = vi.fn();
    bindConversationNavigator(navigate);
    waitingConversations.value = [activity("ask", "mine")];

    const wrapper = await openFeed();
    await wrapper.get('[data-testid="mobile-activity-item"]').trigger("click");

    expect(navigate).toHaveBeenCalledWith("mine", "ask");
    expect(wrapper.find('[data-testid="mobile-activity-popover"]').exists()).toBe(false);
  });

  it("pulses the dot only while something is running", async () => {
    const wrapper = mount(TaskActivityButton);
    const dot = () => wrapper.get('[data-testid="mobile-activity-dot"]');
    expect(dot().classes()).not.toContain("task-activity-pulse--live");

    runningConversations.value = [activity("run", "mine")];
    await wrapper.vm.$nextTick();
    expect(dot().classes()).toContain("task-activity-pulse--live");
  });
});
