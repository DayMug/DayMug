import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../useApi", () => ({
  clearConversationContext: vi.fn().mockResolvedValue({}),
  fetchMessages: vi.fn().mockResolvedValue([]),
}));

import { META_STORAGE_KEY, loadConvMetaMap } from "@/lib/convMeta";

import type { Conversation } from "../apiTypes";
import { clearConversationContext } from "../useApi";
import {
  currentConversationId,
  currentModel,
  modelInfoLine,
  resetActiveConversationStore,
} from "@/stores/activeConversationStore";
import {
  contextClearedAfterMessageId,
  contextPercent,
  contextUsage,
  lastUsage,
  resetChatContextStore,
} from "@/stores/chatContextStore";
import { chatMessages, resetChatMessageStore } from "@/stores/chatMessageStore";

import { CONTEXT_CLEARED_DIVIDER, clearContext, persistConvMeta } from "./contextTracking";

const clearApi = vi.mocked(clearConversationContext);

// The rotated row the endpoint echoes back; clearContext ignores the body,
// so only its shape matters here.
const rotatedRow: Conversation = {
  id: "c1",
  user_id: "u1",
  title: "Session",
  provider: "claude",
  model: "",
  work_dir: "/srv/work",
  session_id: "rotated",
  notifications_enabled: false,
  pinned: false,
  pin_order: 0,
  account_name: "",
  created_at: "",
  updated_at: "",
};

beforeEach(() => {
  resetChatMessageStore();
  resetChatContextStore();
  resetActiveConversationStore();
  localStorage.clear();
  clearApi.mockReset().mockResolvedValue(rotatedRow);
});

describe("contextPercent", () => {
  it("is zero until a usage frame has landed", () => {
    expect(contextPercent.value).toBe(0);
  });

  it("rounds the used/total ratio", () => {
    contextUsage.value = { used: 1234, total: 10000 };

    expect(contextPercent.value).toBe(12);
  });

  it("clamps an over-budget window to 100 instead of overflowing the bar", () => {
    contextUsage.value = { used: 300, total: 100 };

    expect(contextPercent.value).toBe(100);
  });

  it("treats a zero total as 'unknown' rather than dividing by zero", () => {
    contextUsage.value = { used: 10, total: 0 };

    expect(contextPercent.value).toBe(0);
  });
});

describe("lastUsage", () => {
  it("reads the most recent assistant turn that reported counts", () => {
    chatMessages.value = [
      { role: "assistant", content: "a", usage: { short: "1", detail: "", input: 1, output: 2 } },
      { role: "assistant", content: "b", usage: { short: "2", detail: "", input: 3, output: 4 } },
      { role: "activity", content: "tool", activityType: "tool" },
    ];

    expect(lastUsage.value).toMatchObject({ input: 3, output: 4 });
  });

  it("skips a malformed chip that carries no counts", () => {
    chatMessages.value = [
      { role: "assistant", content: "a", usage: { short: "1", detail: "", input: 1, output: 2 } },
      { role: "assistant", content: "b", usage: { short: "raw", detail: "" } },
    ];

    expect(lastUsage.value).toMatchObject({ input: 1, output: 2 });
  });

  it("is null when no assistant turn has reported usage", () => {
    chatMessages.value = [{ role: "assistant", content: "a" }];

    expect(lastUsage.value).toBeNull();
  });
});

describe("persistConvMeta", () => {
  it("caches the header state per conversation", () => {
    currentConversationId.value = "c1";
    currentModel.value = "sonnet-4";
    modelInfoLine.value = "Model: sonnet-4 | Tools: 3 available";
    contextUsage.value = { used: 10, total: 100 };

    persistConvMeta();

    expect(loadConvMetaMap()["c1"]).toMatchObject({
      model: "sonnet-4",
      modelInfo: "Model: sonnet-4 | Tools: 3 available",
      contextUsage: { used: 10, total: 100 },
    });
  });

  it("writes nothing when no conversation is active", () => {
    currentModel.value = "sonnet-4";

    persistConvMeta();

    expect(localStorage.getItem(META_STORAGE_KEY)).toBeNull();
  });
});

describe("clearContext", () => {
  it("wipes the bar, anchors the divider to the last persisted row, and renders it", async () => {
    currentConversationId.value = "c1";
    contextUsage.value = { used: 10, total: 100 };
    chatMessages.value = [
      { role: "user", content: "q", id: "m1" },
      { role: "assistant", content: "a", id: "m2" },
      { role: "activity", content: "client-side pill", activityType: "model" },
    ];

    await expect(clearContext()).resolves.toBe(true);

    expect(clearApi).toHaveBeenCalledWith("c1");
    expect(contextUsage.value).toBeNull();
    expect(contextClearedAfterMessageId.value).toBe("m2");
    expect(chatMessages.value.at(-1)).toMatchObject({
      role: "activity",
      activityType: "info",
      content: CONTEXT_CLEARED_DIVIDER,
    });
    expect(loadConvMetaMap()["c1"].contextUsage).toBeNull();
  });

  it("keeps past usage chips — they are history, not pre-clear state", async () => {
    currentConversationId.value = "c1";
    chatMessages.value = [
      { role: "assistant", content: "a", id: "m1", usage: { short: "I/O", detail: "" } },
    ];

    await clearContext();

    expect(chatMessages.value[0].usage).toBeDefined();
  });

  it("surfaces a server refusal inline and leaves the bar untouched", async () => {
    currentConversationId.value = "c1";
    contextUsage.value = { used: 10, total: 100 };
    clearApi.mockRejectedValueOnce(new Error("run in flight"));

    await expect(clearContext()).resolves.toBe(false);

    expect(contextUsage.value).toMatchObject({ used: 10 });
    expect(chatMessages.value.at(-1)?.content).toBe("Failed to clear context: run in flight");
  });

  it("does nothing without an active conversation", async () => {
    await expect(clearContext()).resolves.toBe(false);

    expect(clearApi).not.toHaveBeenCalled();
    expect(chatMessages.value).toHaveLength(0);
  });
});
