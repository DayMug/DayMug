// Session-level WS frames: routing/subscription filtering, the turn-status
// family (status / init_status), history_backfill, and the user-hub
// conversation lifecycle frames.
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../useBrowserNotifications", () => ({ notifyBrowserTask: vi.fn() }));

import { resetChatStores } from "@/stores";
import {
  conversationListEvent,
  currentConversationId,
  currentSubscriptionId,
  titleUpdate,
} from "@/stores/activeConversationStore";
import {
  runningAgentIds,
  runningConversationIds,
  runningConversations,
} from "@/stores/agentActivityStore";
import { chatMessages, cursors, knownMessageIds } from "@/stores/chatMessageStore";
import { pendingPrompts, queueAhead, queuePosition, queueRunning } from "@/stores/chatQueueStore";
import { isThinking, streamBuffers, streamingText } from "@/stores/chatStreamStore";

import { notifyBrowserTask } from "../useBrowserNotifications";
import type { Conversation } from "../apiTypes";
import type { WSMessage, WSPersistedMessage } from "@/lib/wsProtocol";
import { createMessageDispatcher } from "./messageDispatcher";

const notify = vi.mocked(notifyBrowserTask);

let dispatch: (msg: WSMessage) => void;

function persisted(over: Partial<WSPersistedMessage> & { id: string }): WSPersistedMessage {
  return {
    conversation_id: "c1",
    role: "assistant",
    content: "hello",
    created_at: "2026-01-01T00:00:00Z",
    ...over,
  };
}

function conversationRow(id: string): Conversation {
  return {
    id,
    title: "row",
    user_id: "u1",
    work_dir: "/tmp",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    provider: "claude",
    model: "",
    created_at: "",
    updated_at: "",
  };
}

beforeEach(() => {
  resetChatStores();
  notify.mockClear();
  dispatch = createMessageDispatcher();
});

describe("messages_cleared", () => {
  it("drops the rendered history so a peer tab stops showing cleared messages", () => {
    currentConversationId.value = "c1";
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "m1", role: "assistant", content: "hello" })],
    });
    expect(chatMessages.value.length).toBeGreaterThan(0);

    dispatch({ type: "messages_cleared", conversation_id: "c1" });

    expect(chatMessages.value).toHaveLength(0);
  });

  it("leaves a turn that is still streaming alone", () => {
    currentConversationId.value = "c1";
    dispatch({ type: "status", conversation_id: "c1", status: "thinking" });
    dispatch({ type: "delta", conversation_id: "c1", content: "partial" });

    dispatch({ type: "messages_cleared", conversation_id: "c1" });

    // Clearing history is not cancelling: the run still persists its output.
    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("partial");
  });

  it("keeps seen ids so a replayed pre-clear delta cannot resurrect a row", () => {
    currentConversationId.value = "c1";
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "m1", role: "assistant", content: "hello" })],
    });

    dispatch({ type: "messages_cleared", conversation_id: "c1" });

    expect(knownMessageIds.has("m1")).toBe(true);
  });

  it("ignores a clear aimed at a different conversation", () => {
    currentConversationId.value = "c1";
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "m1", role: "assistant", content: "hello" })],
    });

    dispatch({ type: "messages_cleared", conversation_id: "c2" });

    expect(chatMessages.value).toHaveLength(1);
  });
});

describe("frame routing", () => {
  it("ignores a frame type this build has no handler for", () => {
    dispatch({ type: "quantum_delta", conversation_id: "c1" });

    expect(chatMessages.value).toHaveLength(0);
    expect(isThinking.value).toBe(false);
  });

  it("drops a frame addressed to a different conversation", () => {
    currentConversationId.value = "c1";

    dispatch({ type: "delta", conversation_id: "c2", content: "from elsewhere" });

    expect(streamingText.value).toBe("");
  });

  it("drops a frame from a superseded subscription generation", () => {
    currentConversationId.value = "c1";
    currentSubscriptionId.value = "sub-2";

    dispatch({
      type: "delta",
      conversation_id: "c1",
      subscription_id: "sub-1",
      content: "stale",
    });

    expect(streamingText.value).toBe("");
  });

  it("accepts an unstamped frame — the transport only labels room broadcasts", () => {
    currentConversationId.value = "c1";
    currentSubscriptionId.value = "sub-1";

    dispatch({ type: "delta", content: "unstamped" });

    expect(streamingText.value).toBe("unstamped");
  });

  it("lets user-hub frames through even when they name another conversation", () => {
    currentConversationId.value = "c1";

    dispatch({ type: "title_updated", conversation_id: "c2", title: "Peer session" });

    expect(titleUpdate.value).toMatchObject({ id: "c2", title: "Peer session" });
  });
});

describe("status", () => {
  it("clears the turn on ready and nudges the tab when a turn was running", () => {
    isThinking.value = true;
    streamingText.value = "partial";
    streamBuffers.thinkingBuffer = "thought";
    streamBuffers.subagentStreamingText = "sub";
    queuePosition.value = 3;

    dispatch({ type: "status", status: "ready", conversation_id: "c1", seq: 18 });

    expect(isThinking.value).toBe(false);
    expect(streamingText.value).toBe("");
    expect(streamBuffers.thinkingBuffer).toBe("");
    expect(streamBuffers.subagentStreamingText).toBe("");
    expect(queuePosition.value).toBeNull();
    expect(notify).toHaveBeenCalledWith({
      kind: "completed",
      conversationId: "c1",
      eventId: "seq-18",
    });
  });

  // Regression: `thinking` cleared the parent tool buffers but `ready` did
  // not, so a turn that ended mid-tool-execution kept the stale tool name as
  // the fallback label for the next turn's first tool_result.
  it("clears the parent tool buffers on ready too, not only on thinking", () => {
    isThinking.value = true;
    streamBuffers.currentToolName = "Bash";
    streamBuffers.toolInputBuffer = '{"command":"ls"}';

    dispatch({ type: "status", status: "ready" });

    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
  });

  it("does not nudge the tab when ready arrives on an idle conversation", () => {
    dispatch({ type: "status", status: "ready" });

    expect(notify).not.toHaveBeenCalled();
  });

  it("starts a turn on thinking and clears every stale stream buffer", () => {
    streamingText.value = "leftover";
    streamBuffers.currentToolName = "Bash";
    streamBuffers.toolInputBuffer = "{";
    streamBuffers.subagentToolName = "Grep";
    queuePosition.value = 2;

    dispatch({ type: "status", status: "thinking" });

    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("");
    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
    expect(streamBuffers.subagentToolName).toBe("");
    expect(queuePosition.value).toBeNull();
  });

  // Regression: prompt_runner.go emits `shutdown` when the drainer refuses
  // the job, and no `ready` ever follows. Before the fix isThinking stayed
  // true forever and every later prompt silently queued locally.
  it("releases the turn on shutdown so the composer keeps sending", () => {
    isThinking.value = true;
    streamingText.value = "half a reply";
    streamBuffers.thinkingBuffer = "thought";
    streamBuffers.currentToolName = "Bash";
    streamBuffers.toolInputBuffer = "{";
    streamBuffers.subagentThinkingBuffer = "sub thought";
    queuePosition.value = 1;
    queueAhead.value = 1;
    queueRunning.value = 1;

    dispatch({ type: "status", status: "shutdown" });

    expect(isThinking.value).toBe(false);
    expect(streamingText.value).toBe("");
    expect(streamBuffers.thinkingBuffer).toBe("");
    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
    expect(streamBuffers.subagentThinkingBuffer).toBe("");
    expect(queuePosition.value).toBeNull();
    expect(queueAhead.value).toBeNull();
    expect(queueRunning.value).toBeNull();
  });

  it("does not claim completion on shutdown — the turn never finished", () => {
    isThinking.value = true;

    dispatch({ type: "status", status: "shutdown" });

    expect(notify).not.toHaveBeenCalled();
  });

  it("keeps staged prompts across a shutdown so unsent text is not lost", () => {
    pendingPrompts.value = [{ content: "queued work", clientKey: "local-1" }];
    isThinking.value = true;

    dispatch({ type: "status", status: "shutdown" });

    expect(pendingPrompts.value).toHaveLength(1);
  });
});

describe("init_status", () => {
  it("restores a still-running turn after a reconnect", () => {
    dispatch({ type: "init_status", status: "thinking", conversation_id: "c1" });

    expect(isThinking.value).toBe(true);
  });

  it("heals a missed ready that was lost during the WS gap", () => {
    isThinking.value = true;
    streamingText.value = "orphaned";
    queuePosition.value = 4;

    dispatch({ type: "init_status", status: "ready", conversation_id: "c1" });

    expect(isThinking.value).toBe(false);
    expect(streamingText.value).toBe("");
    expect(queuePosition.value).toBeNull();
  });

  // Regression: the reconnect path repaired less state than a live `ready`
  // frame — it left the sub-agent and parent-tool buffers behind, so a stale
  // `[Tool]` fallback survived the resync.
  it("repairs the same buffers a live ready frame does", () => {
    isThinking.value = true;
    streamingText.value = "orphaned";
    streamBuffers.thinkingBuffer = "thought";
    streamBuffers.currentToolName = "Bash";
    streamBuffers.toolInputBuffer = "{";
    streamBuffers.subagentStreamingText = "sub";
    streamBuffers.subagentThinkingBuffer = "sub thought";
    streamBuffers.subagentToolName = "Grep";
    streamBuffers.subagentToolInputBuffer = "{";

    dispatch({ type: "init_status", status: "ready", conversation_id: "c1" });

    expect(streamingText.value).toBe("");
    expect(streamBuffers.thinkingBuffer).toBe("");
    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
    expect(streamBuffers.subagentStreamingText).toBe("");
    expect(streamBuffers.subagentThinkingBuffer).toBe("");
    expect(streamBuffers.subagentToolName).toBe("");
    expect(streamBuffers.subagentToolInputBuffer).toBe("");
  });

  it("keeps locally-queued prompts on a soft resync", () => {
    pendingPrompts.value = [{ content: "not yet acked", clientKey: "local-1" }];

    dispatch({ type: "init_status", status: "ready", conversation_id: "c1" });

    expect(pendingPrompts.value).toHaveLength(1);
  });
});

describe("history_backfill", () => {
  it("appends persisted rows and advances the reconnect cursor", () => {
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [
        persisted({ id: "m1", role: "user", content: "hi" }),
        persisted({ id: "m2", role: "assistant", content: "hello" }),
      ],
    });

    expect(chatMessages.value.map((m) => m.id)).toEqual(["m1", "m2"]);
    expect(cursors.lastSyncedMessageId).toBe("m2");
    expect(knownMessageIds.has("m1")).toBe(true);
  });

  it("skips rows already rendered from another source", () => {
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "m1" })],
    });
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "m1" }), persisted({ id: "m2" })],
    });

    expect(chatMessages.value.map((m) => m.id)).toEqual(["m1", "m2"]);
  });

  it("routes still-queued user rows into the staging area, not the transcript", () => {
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [
        persisted({ id: "p1", role: "user", content: "queued", queue_status: "pending" }),
        persisted({ id: "p2", role: "user", content: "claimed", queue_status: "processing" }),
      ],
    });

    expect(pendingPrompts.value).toHaveLength(1);
    expect(pendingPrompts.value[0]).toMatchObject({
      id: "p1",
      content: "queued",
      clientKey: "srv-p1",
    });
    expect(chatMessages.value.map((m) => m.id)).toEqual(["p2"]);
  });

  it("does not restage a prompt a sibling tab already showed", () => {
    pendingPrompts.value = [{ id: "p1", content: "queued", clientKey: "local-1" }];

    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "p1", role: "user", content: "queued", queue_status: "pending" })],
    });

    expect(pendingPrompts.value).toHaveLength(1);
    expect(pendingPrompts.value[0].clientKey).toBe("local-1");
    expect(knownMessageIds.has("p1")).toBe(true);
  });

  it("does not duplicate an assistant row the live result already rendered", () => {
    dispatch({ type: "result", content: "done", message_id: "m9" });
    expect(chatMessages.value).toHaveLength(1);

    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [persisted({ id: "m9", content: "done" })],
    });

    expect(chatMessages.value).toHaveLength(1);
  });

  it("strips the agent-facing sender frame from an IM-mirrored prompt", () => {
    dispatch({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [
        persisted({
          id: "m1",
          role: "user",
          content: "[Alice]: deploy now",
          metadata: { sender: { platform: "slack", id: "U1", name: "Alice" } },
        }),
      ],
    });

    expect(chatMessages.value[0].content).toBe("deploy now");
    expect(chatMessages.value[0].sender).toMatchObject({ name: "Alice" });
  });
});

describe("conversation lifecycle frames", () => {
  it("publishes an added conversation using the nested row's id", () => {
    dispatch({ type: "conversation_added", conversation: conversationRow("c9") });

    expect(conversationListEvent.value).toMatchObject({ kind: "added", id: "c9" });
  });

  it("publishes the full row on update so local fields are not dropped", () => {
    dispatch({ type: "conversation_updated", conversation: conversationRow("c9") });

    expect(conversationListEvent.value).toMatchObject({ kind: "updated", id: "c9" });
    expect(conversationListEvent.value?.conversation?.work_dir).toBe("/tmp");
  });

  it("publishes a removal by id", () => {
    dispatch({ type: "conversation_removed", conversation_id: "c9" });

    expect(conversationListEvent.value).toMatchObject({ kind: "removed", id: "c9" });
  });

  it("applies the running-job snapshot carried on init_ok", () => {
    dispatch({
      type: "init_ok",
      running_agent_ids: ["a1"],
      running_conversation_ids: ["c1"],
      running_conversations: [
        {
          conversation_id: "c1",
          agent_id: "a1",
          account_name: "acct",
          model: "sonnet",
          started_at: "2026-01-01T00:00:00Z",
        },
      ],
    });

    expect(runningAgentIds.value).toEqual(["a1"]);
    expect(runningConversations.value).toHaveLength(1);
  });

  it("treats an omitted snapshot array as 'nothing running'", () => {
    dispatch({ type: "init_ok", running_agent_ids: ["a1"], running_conversation_ids: ["c1"] });
    dispatch({ type: "conversation_activity" });

    expect(runningAgentIds.value).toEqual([]);
    expect(runningConversationIds.value).toEqual([]);
    expect(runningConversations.value).toEqual([]);
  });

  it("ignores a title update missing either half of the pair", () => {
    dispatch({ type: "title_updated", conversation_id: "c1", title: "" });

    expect(titleUpdate.value).toBeNull();
  });
});
