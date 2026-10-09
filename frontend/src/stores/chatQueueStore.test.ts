import { beforeEach, describe, expect, it } from "vitest";

import {
  clearQueueMetrics,
  nextPendingPromptSeq,
  queueAhead,
  queuePosition,
  queueRunning,
  resetChatQueueStore,
  startedPromptIds,
} from "./chatQueueStore";

beforeEach(() => {
  resetChatQueueStore();
});

describe("client-key sequence", () => {
  it("hands out a fresh number per call so sibling tabs cannot collide", () => {
    expect(nextPendingPromptSeq()).toBe(1);
    expect(nextPendingPromptSeq()).toBe(2);
    expect(nextPendingPromptSeq()).toBe(3);
  });

  it("restarts from zero after a store reset", () => {
    nextPendingPromptSeq();
    resetChatQueueStore();

    expect(nextPendingPromptSeq()).toBe(1);
  });
});

describe("queue metrics", () => {
  it("clears the whole pool breakdown, not just the position", () => {
    queuePosition.value = 2;
    queueAhead.value = 5;
    queueRunning.value = 3;

    clearQueueMetrics();

    expect(queuePosition.value).toBeNull();
    expect(queueAhead.value).toBeNull();
    expect(queueRunning.value).toBeNull();
  });
});

describe("startedPromptIds", () => {
  it("is a shared latch the user_message handler can consume once", () => {
    startedPromptIds.add("m1");

    expect(startedPromptIds.has("m1")).toBe(true);
    startedPromptIds.delete("m1");
    expect(startedPromptIds.has("m1")).toBe(false);
  });
});
