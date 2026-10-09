import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, ref, type EffectScope } from "vue";

import type { ConversationAttentionRow } from "./apiTypes";
import { activityVersion, resetAgentActivityStore } from "@/stores/agentActivityStore";
import {
  attentionRows,
  bumpAttentionVersion,
  conversationAttention,
  liveConversationTitles,
  resetConversationAttentionStore,
} from "@/stores/conversationAttentionStore";

const api = vi.hoisted(() => ({
  fetchConversationAttention: vi.fn(),
  markConversationRead: vi.fn(async () => {}),
}));
vi.mock("./apiConversations", () => api);

import { useAttentionSync } from "./useAttentionSync";

const row = (id: string, state: ConversationAttentionRow["state"] = "done") => ({
  conversation_id: id,
  agent_id: "agent",
  title: id,
  state,
  at: "2026-09-26T00:00:00Z",
});

let scope: EffectScope;
const shown = ref("");
const enabled = ref(false);

function start() {
  scope = effectScope();
  scope.run(() => useAttentionSync(shown, enabled));
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, value: state });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("useAttentionSync", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    resetConversationAttentionStore();
    resetAgentActivityStore();
    shown.value = "";
    enabled.value = false;
    setVisibility("visible");
    api.fetchConversationAttention.mockResolvedValue({
      items: [row("a"), row("b", "waiting")],
      titles: { live: "Live job" },
    });
  });

  afterEach(() => {
    scope.stop();
    vi.useRealTimers();
  });

  it("loads the list once the user is signed in", async () => {
    start();
    expect(api.fetchConversationAttention).not.toHaveBeenCalled();

    enabled.value = true;
    await vi.runAllTimersAsync();

    expect(conversationAttention.value).toEqual({ a: "done", b: "waiting" });
    expect(liveConversationTitles.value).toEqual({ live: "Live job" });
  });

  it("never lists the conversation on screen and acknowledges it on arrival", async () => {
    shown.value = "a";
    enabled.value = true;
    start();
    await vi.runAllTimersAsync();

    expect(attentionRows.value.map((r) => r.conversation_id)).toEqual(["b"]);
    expect(api.markConversationRead).toHaveBeenCalledWith("a");
  });

  it("clears a flag as soon as the user opens that conversation", async () => {
    enabled.value = true;
    start();
    await vi.runAllTimersAsync();

    shown.value = "b";
    await nextTick();

    expect(conversationAttention.value).toEqual({ a: "done" });
    expect(api.markConversationRead).toHaveBeenCalledWith("b");
  });

  it("does not count a hidden page as looking at the conversation", async () => {
    shown.value = "a";
    enabled.value = true;
    setVisibility("hidden");
    start();
    await vi.runAllTimersAsync();

    expect(conversationAttention.value).toEqual({ a: "done", b: "waiting" });
    expect(api.markConversationRead).not.toHaveBeenCalled();

    setVisibility("visible");
    await vi.runAllTimersAsync();
    expect(api.markConversationRead).toHaveBeenCalledWith("a");
  });

  it("refetches once per burst of activity or attention frames", async () => {
    enabled.value = true;
    start();
    await vi.runAllTimersAsync();
    api.fetchConversationAttention.mockClear();

    activityVersion.value += 1;
    activityVersion.value += 1;
    await nextTick();
    bumpAttentionVersion();
    await nextTick();
    await vi.runAllTimersAsync();

    expect(api.fetchConversationAttention).toHaveBeenCalledTimes(1);
  });
});
