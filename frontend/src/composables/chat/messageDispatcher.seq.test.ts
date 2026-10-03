// Transport-level gap detection. The broadcaster's room fanout drops frames
// for a client whose socket buffer is full rather than stalling the stream, so
// the `seq` watermark is the client's only evidence that its view is
// incomplete.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../useBrowserNotifications", () => ({ notifyBrowserTask: vi.fn() }));

const syncFromCursor = vi.fn<() => Promise<void>>();

vi.mock("./messageState", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./messageState")>()),
  syncFromCursor: () => syncFromCursor(),
}));

import { resetChatStores } from "@/stores";
import { conversationListResync } from "@/stores/activeConversationStore";
import { deliveryWatermarks, resetConversationDeliveryWatermark } from "@/stores/wsDeliveryStore";

import type { WSMessage } from "@/lib/wsProtocol";
import { createMessageDispatcher } from "./messageDispatcher";

let dispatch: (msg: WSMessage) => void;

// Lets the dispatcher's `syncFromCursor().finally(...)` settle so the
// in-flight guard is cleared before the next assertion.
async function settle() {
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  resetChatStores();
  syncFromCursor.mockReset();
  syncFromCursor.mockResolvedValue(undefined);
  dispatch = createMessageDispatcher();
});

describe("seq gap detection", () => {
  it("tracks the watermark across contiguous frames without re-syncing", () => {
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 2 });
    dispatch({ type: "delta", content: "c", seq: 3 });

    expect(deliveryWatermarks.conversation).toBe(3);
    expect(syncFromCursor).not.toHaveBeenCalled();
  });

  it("re-syncs when the sequence jumps", () => {
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "d", seq: 4 });

    expect(syncFromCursor).toHaveBeenCalledTimes(1);
    expect(deliveryWatermarks.conversation).toBe(4);
  });

  it("ignores unsequenced frames", () => {
    // init_ok / history_backfill / UserHub frames and the cached
    // context_usage tail all arrive without a seq.
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "context_usage", content: '{"used":1}' });
    dispatch({ type: "title_updated", conversation_id: "c1", title: "t" });
    dispatch({ type: "delta", content: "b", seq: 2 });

    expect(syncFromCursor).not.toHaveBeenCalled();
    expect(deliveryWatermarks.conversation).toBe(2);
  });

  it("does not treat the first sequenced frame as a gap", () => {
    // Joining mid-turn: the replay buffer was reset at the last persisted
    // checkpoint, so the first frame we see is seq 87, not seq 1.
    dispatch({ type: "delta", content: "a", seq: 87 });

    expect(syncFromCursor).not.toHaveBeenCalled();
    expect(deliveryWatermarks.conversation).toBe(87);
  });

  it("treats a non-advancing sequence as a room restart, not a gap", () => {
    // A room GC'd while idle and recreated starts counting from 1 again.
    dispatch({ type: "delta", content: "a", seq: 9 });
    dispatch({ type: "delta", content: "b", seq: 1 });
    dispatch({ type: "delta", content: "c", seq: 2 });

    expect(syncFromCursor).not.toHaveBeenCalled();
    expect(deliveryWatermarks.conversation).toBe(2);
  });

  it("collapses a burst of gaps into a single re-sync", async () => {
    let release: () => void = () => {};
    syncFromCursor.mockReturnValue(
      new Promise<void>((resolve) => {
        release = resolve;
      }),
    );

    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 10 });
    dispatch({ type: "delta", content: "c", seq: 20 });
    dispatch({ type: "delta", content: "d", seq: 30 });

    expect(syncFromCursor).toHaveBeenCalledTimes(1);

    release();
    await settle();

    // Once the in-flight sync settles a later gap can trigger a fresh one.
    dispatch({ type: "delta", content: "e", seq: 40 });
    expect(syncFromCursor).toHaveBeenCalledTimes(2);
  });

  it("re-syncs on a gap between conversation-scoped frame types", () => {
    // The dropped frames need not be deltas — a missed tool_result is exactly
    // the case REST recovers, since it has a persisted row.
    dispatch({ type: "tool_use_start", content: '{"name":"Bash"}', seq: 5 });
    dispatch({ type: "tool_result", content: "done", message_id: "m1", seq: 8 });

    expect(syncFromCursor).toHaveBeenCalledTimes(1);
  });

  it("starts a new conversation without inheriting the old watermark", () => {
    dispatch({ type: "delta", content: "a", seq: 50 });
    // What switchToConversation does: the next conversation is a different
    // broadcaster room counting from its own baseline.
    resetConversationDeliveryWatermark();

    expect(deliveryWatermarks.conversation).toBe(0);

    dispatch({ type: "delta", content: "a", seq: 3 });
    expect(syncFromCursor).not.toHaveBeenCalled();
  });
});

// The in-flight guard is delivery state like the watermarks, so it has to be
// reset with them: a resync still pending when one test ends must not
// suppress the next test's (or the next session's) gap repair.
describe("gap resync guard isolation", () => {
  it("leaves a resync pending forever", () => {
    syncFromCursor.mockReturnValue(new Promise<void>(() => {}));
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 5 });

    expect(syncFromCursor).toHaveBeenCalledTimes(1);
  });

  it("still repairs a gap after the stores are reset", () => {
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 5 });

    expect(syncFromCursor).toHaveBeenCalledTimes(1);
  });

  it("does not let a stale pull clear the guard of a newer one", async () => {
    let releaseStale: () => void = () => {};
    syncFromCursor.mockReturnValueOnce(new Promise<void>((r) => (releaseStale = r)));
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 5 });

    resetChatStores();
    syncFromCursor.mockReturnValueOnce(new Promise<void>(() => {}));
    dispatch({ type: "delta", content: "a", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 5 });
    expect(syncFromCursor).toHaveBeenCalledTimes(2);

    releaseStale();
    await settle();

    // The second pull is still running, so this gap is folded into it.
    dispatch({ type: "delta", content: "c", seq: 9 });
    expect(syncFromCursor).toHaveBeenCalledTimes(2);
  });
});

// User-hub frames (conversation lifecycle + title updates) travel on the same
// socket but come from a different fanout with its own per-client counter, so
// they need their own watermark and their own repair: the hub has no replay
// buffer, and a missed frame leaves the sidebar stale.
describe("user-hub seq gap detection", () => {
  it("signals a list refetch when a hub frame goes missing", () => {
    const before = conversationListResync.value;
    dispatch({ type: "conversation_updated", seq: 1, conversation: undefined });
    dispatch({ type: "conversation_removed", conversation_id: "c1", seq: 4 });

    expect(conversationListResync.value).toBeGreaterThan(before);
    // The message cursor is untouched: no conversation frame was lost.
    expect(syncFromCursor).not.toHaveBeenCalled();
  });

  it("stays quiet while hub frames arrive contiguously", () => {
    const before = conversationListResync.value;
    dispatch({ type: "title_updated", conversation_id: "c1", title: "a", seq: 1 });
    dispatch({ type: "title_updated", conversation_id: "c1", title: "b", seq: 2 });

    expect(conversationListResync.value).toBe(before);
  });

  it("counts the two streams separately", () => {
    // Interleaved frames whose numbers cross would look like wild jumps if
    // they shared a watermark. Each stream advances by one.
    const before = conversationListResync.value;
    dispatch({ type: "delta", content: "a", seq: 40 });
    dispatch({ type: "title_updated", conversation_id: "c1", title: "t", seq: 1 });
    dispatch({ type: "delta", content: "b", seq: 41 });
    dispatch({ type: "conversation_removed", conversation_id: "c1", seq: 2 });

    expect(syncFromCursor).not.toHaveBeenCalled();
    expect(conversationListResync.value).toBe(before);
    expect(deliveryWatermarks.conversation).toBe(41);
    expect(deliveryWatermarks.hub).toBe(2);
  });

  it("keeps its watermark across a conversation switch", () => {
    // The hub stream is per-socket and spans conversations, so switching must
    // not reset it — a reset would silently forfeit one detection.
    dispatch({ type: "title_updated", conversation_id: "c1", title: "a", seq: 7 });
    resetConversationDeliveryWatermark();

    expect(deliveryWatermarks.hub).toBe(7);

    const before = conversationListResync.value;
    dispatch({ type: "title_updated", conversation_id: "c2", title: "b", seq: 9 });
    expect(conversationListResync.value).toBeGreaterThan(before);
  });
});
