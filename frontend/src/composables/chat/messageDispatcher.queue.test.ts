// The prompt-lifecycle frames: the staging area (user_message / input_ack /
// prompt_started / queue_status / cancel_ack) and the two failure frames
// that terminate a turn (persist_failed / error).
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../useBrowserNotifications", () => ({ notifyBrowserTask: vi.fn() }));

import { resetChatStores } from "@/stores";
import {
  chatMessages,
  cursors,
  knownMessageIds,
  rememberMessageId,
} from "@/stores/chatMessageStore";
import {
  pendingPrompts,
  queueAhead,
  queuePosition,
  queueRunning,
  startedPromptIds,
} from "@/stores/chatQueueStore";
import { isThinking, streamBuffers, streamingText } from "@/stores/chatStreamStore";

import { notifyBrowserTask } from "../useBrowserNotifications";
import type { WSMessage } from "@/lib/wsProtocol";
import { createMessageDispatcher } from "./messageDispatcher";

const notify = vi.mocked(notifyBrowserTask);

let dispatch: (msg: WSMessage) => void;

beforeEach(() => {
  resetChatStores();
  notify.mockClear();
  dispatch = createMessageDispatcher();
});

describe("user_message", () => {
  it("stages a peer tab's prompt rather than inlining it", () => {
    dispatch({ type: "user_message", message_id: "m1", content: "from the other tab" });

    expect(chatMessages.value).toHaveLength(0);
    expect(pendingPrompts.value[0]).toMatchObject({
      id: "m1",
      content: "from the other tab",
      clientKey: "peer-m1",
    });
    expect(knownMessageIds.has("m1")).toBe(true);
    expect(cursors.lastSyncedMessageId).toBe("m1");
  });

  it("inherits the pool position queue_status already pushed", () => {
    dispatch({ type: "queue_status", status: "queued", queue_position: 2, queue_ahead: 3 });

    dispatch({ type: "user_message", message_id: "m1", content: "queued behind others" });

    expect(pendingPrompts.value[0]).toMatchObject({
      poolPosition: 2,
      poolAhead: 3,
      poolRunning: 0,
    });
  });

  it("ignores an echo whose row is already rendered", () => {
    rememberMessageId("m1");

    dispatch({ type: "user_message", message_id: "m1", content: "already here" });

    expect(pendingPrompts.value).toHaveLength(0);
  });

  it("does not double-stage a prompt this tab already sent", () => {
    dispatch({ type: "input_ack", message_id: "m1" });
    pendingPrompts.value = [{ id: "m1", content: "mine", clientKey: "local-1" }];

    dispatch({ type: "user_message", message_id: "m1", content: "mine" });

    expect(pendingPrompts.value).toHaveLength(1);
    expect(pendingPrompts.value[0].clientKey).toBe("local-1");
  });

  it("inlines the prompt when prompt_started already won the race", () => {
    dispatch({ type: "prompt_started", message_id: "m1" });
    expect(startedPromptIds.has("m1")).toBe(true);

    dispatch({ type: "user_message", message_id: "m1", content: "raced" });

    expect(pendingPrompts.value).toHaveLength(0);
    expect(chatMessages.value[0]).toMatchObject({ role: "user", content: "raced", id: "m1" });
    expect(startedPromptIds.has("m1")).toBe(false);
  });

  it("stages an attachment-only prompt that carries no text", () => {
    dispatch({
      type: "user_message",
      message_id: "m1",
      metadata: {
        attachments: [{ name: "a.png", mime: "image/png", path: "./a.png", url: "/f" }],
      },
    });

    expect(pendingPrompts.value[0].attachments).toHaveLength(1);
  });

  // Regression: an unrenderable echo returned before rememberMessageId, so
  // the cursor stayed behind that id and the next history_backfill resent the
  // same row.
  it("stages nothing for an echo with neither text nor attachments but still advances the cursor", () => {
    dispatch({ type: "user_message", message_id: "m1", content: "" });

    expect(pendingPrompts.value).toHaveLength(0);
    expect(chatMessages.value).toHaveLength(0);
    expect(knownMessageIds.has("m1")).toBe(true);
    expect(cursors.lastSyncedMessageId).toBe("m1");
  });

  it("strips the agent-facing sender frame from an IM-mirrored prompt", () => {
    dispatch({
      type: "user_message",
      message_id: "m1",
      content: "[Alice]: deploy now",
      metadata: { sender: { platform: "slack", id: "U1", name: "Alice" } },
    });

    expect(pendingPrompts.value[0].content).toBe("deploy now");
    expect(pendingPrompts.value[0].sender).toMatchObject({ name: "Alice" });
  });
});

describe("input_ack", () => {
  it("stamps the canonical id onto the un-acked staging entry", () => {
    pendingPrompts.value = [
      { id: "m0", content: "older", clientKey: "local-0" },
      { content: "just sent", clientKey: "local-1" },
    ];

    dispatch({ type: "input_ack", message_id: "m1" });

    expect(pendingPrompts.value[1].id).toBe("m1");
    expect(pendingPrompts.value[0].id).toBe("m0");
    expect(cursors.lastSyncedMessageId).toBe("m1");
  });

  it("still advances the cursor when nothing is waiting for an id", () => {
    dispatch({ type: "input_ack", message_id: "m1" });

    expect(knownMessageIds.has("m1")).toBe(true);
    expect(cursors.lastSyncedMessageId).toBe("m1");
  });
});

describe("prompt_started", () => {
  it("promotes the staged prompt into the transcript", () => {
    pendingPrompts.value = [
      { id: "m1", content: "run it", clientKey: "local-1", poolPosition: 2 },
      { id: "m2", content: "then this", clientKey: "local-2" },
    ];
    queuePosition.value = 2;

    dispatch({ type: "prompt_started", message_id: "m1" });

    expect(pendingPrompts.value.map((p) => p.id)).toEqual(["m2"]);
    expect(chatMessages.value[0]).toMatchObject({ role: "user", content: "run it", id: "m1" });
    expect(queuePosition.value).toBeNull();
  });

  it("records the id when the matching echo has not arrived yet", () => {
    dispatch({ type: "prompt_started", message_id: "m1" });

    expect(startedPromptIds.has("m1")).toBe(true);
    expect(chatMessages.value).toHaveLength(0);
  });

  it("does not arm the race latch for a prompt already in the transcript", () => {
    rememberMessageId("m1");

    dispatch({ type: "prompt_started", message_id: "m1" });

    expect(startedPromptIds.has("m1")).toBe(false);
  });
});

describe("queue_status", () => {
  it("labels the head of the staging area with the pool breakdown", () => {
    pendingPrompts.value = [
      { id: "m1", content: "head", clientKey: "local-1" },
      { id: "m2", content: "tail", clientKey: "local-2" },
    ];

    dispatch({
      type: "queue_status",
      status: "queued",
      queue_position: 2,
      queue_ahead: 4,
      queue_running: 2,
    });

    expect(pendingPrompts.value[0]).toMatchObject({
      poolPosition: 2,
      poolAhead: 4,
      poolRunning: 2,
    });
    expect(pendingPrompts.value[1].poolPosition).toBeUndefined();
    expect(isThinking.value).toBe(true);
  });

  it("defaults an omitted position to the head of the queue", () => {
    dispatch({ type: "queue_status", status: "queued" });

    expect(queuePosition.value).toBe(1);
    expect(queueAhead.value).toBe(0);
    expect(queueRunning.value).toBe(0);
  });

  // Regression: the counts are *int on the Go side precisely so that a real
  // zero survives omitempty. Deriving queue_ahead from the position (the old
  // fallback) claimed one task was ahead when none was.
  it("keeps a genuine zero instead of inferring it from the position", () => {
    dispatch({
      type: "queue_status",
      status: "queued",
      queue_position: 1,
      queue_ahead: 0,
      queue_running: 0,
    });

    expect(queuePosition.value).toBe(1);
    expect(queueAhead.value).toBe(0);
    expect(queueRunning.value).toBe(0);
  });
});

describe("cancel_ack", () => {
  it("drops the acknowledged prompts from staging and from history", () => {
    pendingPrompts.value = [
      { id: "m1", content: "cancel me", clientKey: "local-1" },
      { id: "m2", content: "keep me", clientKey: "local-2" },
    ];
    chatMessages.value = [
      { role: "user", content: "promoted", id: "m3" },
      { role: "assistant", content: "reply", id: "m4" },
    ];

    dispatch({
      type: "cancel_ack",
      conversation_id: "c1",
      cancelled_prompts: [
        { id: "m1", conversation_id: "c1", role: "user", content: "", created_at: "" },
        { id: "m3", conversation_id: "c1", role: "user", content: "", created_at: "" },
      ],
    });

    expect(pendingPrompts.value.map((p) => p.id)).toEqual(["m2"]);
    expect(chatMessages.value.map((m) => m.id)).toEqual(["m4"]);
  });

  it("keeps a locally-staged prompt that has no id yet", () => {
    pendingPrompts.value = [{ content: "not acked", clientKey: "local-1" }];

    dispatch({
      type: "cancel_ack",
      conversation_id: "c1",
      cancelled_prompts: [
        { id: "m1", conversation_id: "c1", role: "user", content: "", created_at: "" },
      ],
    });

    expect(pendingPrompts.value).toHaveLength(1);
  });

  it("is a no-op when the cancel matched nothing", () => {
    pendingPrompts.value = [{ id: "m1", content: "stays", clientKey: "local-1" }];

    dispatch({ type: "cancel_ack", conversation_id: "c1" });

    expect(pendingPrompts.value).toHaveLength(1);
  });
});

describe("persist_failed", () => {
  it("surfaces the failure inline so the drift is visible", () => {
    dispatch({ type: "persist_failed", conversation_id: "c1", message: "disk full" });

    expect(chatMessages.value[0]).toMatchObject({ role: "error", content: "disk full" });
  });

  it("ignores an empty notice", () => {
    dispatch({ type: "persist_failed", conversation_id: "c1", message: "" });

    expect(chatMessages.value).toHaveLength(0);
  });
});

describe("CAS session diagnostics", () => {
  it("shows healthy transitions as activity", () => {
    dispatch({ type: "session_info", content: "CAS resumed thread t1" });

    expect(chatMessages.value[0]).toMatchObject({
      role: "activity",
      activityType: "info",
      content: "CAS resumed thread t1",
    });
  });

  it("shows a persisted fallback warning without ending the replacement turn", () => {
    isThinking.value = true;
    streamingText.value = "replacement reply";

    dispatch({
      type: "session_warning",
      message: "CAS resume failed; started a fresh thread",
      message_id: "warning-1",
    });

    expect(chatMessages.value[0]).toMatchObject({
      role: "warning",
      content: "CAS resume failed; started a fresh thread",
      id: "warning-1",
    });
    expect(knownMessageIds.has("warning-1")).toBe(true);
    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("replacement reply");
  });
});

describe("structured notices", () => {
  it("renders a guardrail warning from its notice in the active locale", () => {
    dispatch({
      type: "session_warning",
      message: "上一条消息触发的任务已达到模型请求次数提醒",
      message_id: "warning-2",
      metadata: {
        notice_type: "session_warning",
        notice: {
          kind: "task_guardrail",
          triggers: ["model_calls"],
          model_calls: 3,
          tool_calls: 1,
        },
      },
    });

    expect(chatMessages.value[0]).toMatchObject({ role: "warning", id: "warning-2" });
    expect(chatMessages.value[0].content).toMatch(/^The previous message's task reached/);
  });

  it("keeps the turn running while the server waits to retry", () => {
    isThinking.value = true;

    dispatch({
      type: "error",
      message: "上游服务过载，1 分钟后自动重试（最多 3 次）。",
      message_id: "retry-1",
      metadata: { notice: { kind: "transient_retry", delay_seconds: 60, max_attempts: 3 } },
    });

    expect(chatMessages.value[0]).toMatchObject({ role: "error", id: "retry-1" });
    expect(chatMessages.value[0].content).toContain("Retrying automatically in 1 min");
    expect(isThinking.value).toBe(true);
  });
});

describe("error", () => {
  it("renders the error and ends the turn", () => {
    isThinking.value = true;
    streamingText.value = "partial";
    streamBuffers.thinkingBuffer = "thought";

    dispatch({
      type: "error",
      message: "claude exited 1",
      message_id: "e1",
      conversation_id: "c1",
    });

    expect(chatMessages.value[0]).toMatchObject({
      role: "error",
      content: "claude exited 1",
      id: "e1",
    });
    expect(knownMessageIds.has("e1")).toBe(true);
    expect(isThinking.value).toBe(false);
    expect(streamingText.value).toBe("");
    expect(streamBuffers.thinkingBuffer).toBe("");
    expect(notify).toHaveBeenCalledWith({
      kind: "failed",
      conversationId: "c1",
      eventId: "e1",
    });
  });

  it("does not re-render an error row REST already delivered", () => {
    rememberMessageId("e1");

    dispatch({ type: "error", message: "claude exited 1", message_id: "e1" });

    expect(chatMessages.value).toHaveLength(0);
  });

  it("always renders a handler-level rejection that was never persisted", () => {
    dispatch({ type: "error", message: "init required before input" });
    dispatch({ type: "error", message: "init required before input" });

    expect(chatMessages.value).toHaveLength(2);
  });

  it("does not nudge the tab when no turn was running", () => {
    dispatch({ type: "error", message: "boom" });

    expect(notify).not.toHaveBeenCalled();
  });

  it("shows a stale Insert failure and leaves the running state", () => {
    isThinking.value = true;
    streamingText.value = "stale partial";

    dispatch({ type: "error", message: "no active turn", conversation_id: "c1" });

    expect(chatMessages.value[0]).toMatchObject({ role: "error", content: "no active turn" });
    expect(isThinking.value).toBe(false);
    expect(streamingText.value).toBe("");
  });

  // Regression: handler-level rejections answer one inbound frame and leave
  // the running turn alone, but clearing isThinking for them unlocked the
  // composer mid-answer and threw away the partial text.
  it("keeps the turn running for a handler-level rejection", () => {
    isThinking.value = true;
    streamingText.value = "partial";
    streamBuffers.thinkingBuffer = "thought";

    dispatch({ type: "error", message: "init required before input" });

    expect(chatMessages.value[0]).toMatchObject({ role: "error" });
    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("partial");
    expect(streamBuffers.thinkingBuffer).toBe("thought");
    expect(notify).not.toHaveBeenCalled();
  });
});

// An IM prompt is echoed the moment it is stored, which is before it has an
// account slot. The marker is what tells the staging area this card is not
// recallable — its text was typed in Slack or WeChat, not in this composer.
describe("IM prompts waiting for an account slot", () => {
  it("stages a pool-queued user_message as IM-sourced", () => {
    dispatch({
      type: "user_message",
      message_id: "im1",
      content: "部署一下",
      queue_status: "pool_queued",
    });

    expect(chatMessages.value).toHaveLength(0);
    expect(pendingPrompts.value[0]).toMatchObject({ id: "im1", fromIM: true });
  });

  it("leaves a web prompt recallable", () => {
    dispatch({ type: "user_message", message_id: "w1", content: "typed here" });

    expect(pendingPrompts.value[0].fromIM).toBe(false);
  });

  it("promotes the card into the transcript once the slot is granted", () => {
    dispatch({
      type: "user_message",
      message_id: "im1",
      content: "部署一下",
      queue_status: "pool_queued",
    });
    dispatch({ type: "prompt_started", message_id: "im1" });

    expect(pendingPrompts.value).toHaveLength(0);
    expect(chatMessages.value.at(-1)).toMatchObject({ id: "im1", role: "user" });
  });

  it("stages a pool-queued row arriving through history_backfill", () => {
    dispatch({
      type: "history_backfill",
      messages: [
        {
          id: "im1",
          role: "user",
          content: "部署一下",
          conversation_id: "c1",
          created_at: "",
          queue_status: "pool_queued",
        },
        { id: "a1", role: "assistant", content: "done", conversation_id: "c1", created_at: "" },
      ],
    });

    expect(pendingPrompts.value[0]).toMatchObject({ id: "im1", fromIM: true });
    expect(chatMessages.value.map((m) => m.id)).toEqual(["a1"]);
  });
});
