import { beforeEach, describe, expect, it } from "vitest";

import {
  agentAttention,
  applyAttention,
  attentionRows,
  conversationAttention,
  dropAttention,
  resetConversationAttentionStore,
} from "./conversationAttentionStore";
import type { ConversationAttentionRow } from "@/composables/types/chat";

const row = (
  conversationId: string,
  agentId: string,
  state: ConversationAttentionRow["state"],
): ConversationAttentionRow => ({
  conversation_id: conversationId,
  agent_id: agentId,
  title: conversationId,
  state,
  at: "2026-09-26T00:00:00Z",
});

describe("conversationAttentionStore", () => {
  beforeEach(resetConversationAttentionStore);

  it("maps each flagged conversation to its state", () => {
    applyAttention(
      [row("done", "a", "done"), row("failed", "a", "error"), row("asked", "a", "waiting")],
      {},
    );

    expect(conversationAttention.value).toEqual({
      done: "done",
      failed: "error",
      asked: "waiting",
    });
  });

  it("shows an agent red on any failure, else yellow on a pending question, else blue", () => {
    applyAttention(
      [
        row("a-done", "a", "done"),
        row("a-failed", "a", "error"),
        row("a-asked", "a", "waiting"),
        row("b-done", "b", "done"),
        row("b-asked", "b", "waiting"),
        row("c-done", "c", "done"),
      ],
      {},
    );

    expect(agentAttention.value).toEqual({ a: "error", b: "waiting", c: "done" });
  });

  it("drops one conversation without touching the rest", () => {
    applyAttention([row("x", "a", "done"), row("y", "a", "error")], {});
    dropAttention("x");

    expect(attentionRows.value.map((r) => r.conversation_id)).toEqual(["y"]);
  });
});
