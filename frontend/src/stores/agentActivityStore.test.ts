import { beforeEach, describe, expect, it } from "vitest";

import {
  activityVersion,
  applyConversationActivity,
  resetAgentActivityStore,
  waitingConversations,
} from "./agentActivityStore";

const running = (conversationId: string, agentId: string) => ({
  conversation_id: conversationId,
  agent_id: agentId,
  account_name: "account",
  model: "model",
  started_at: "2026-08-13T00:00:00Z",
});

describe("agentActivityStore", () => {
  beforeEach(resetAgentActivityStore);

  it("bumps the version on every snapshot so attention can be refetched", () => {
    applyConversationActivity(["a"], ["c"], [running("c", "a")]);
    applyConversationActivity([], [], []);

    expect(activityVersion.value).toBe(2);
  });

  it("keeps a turn parked on a question apart from running work", () => {
    applyConversationActivity([], [], [], [], [running("c", "a")]);

    expect(waitingConversations.value.map((a) => a.conversation_id)).toEqual(["c"]);
  });
});
