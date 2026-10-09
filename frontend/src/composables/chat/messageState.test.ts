import { beforeEach, describe, expect, it, vi } from "vitest";

const fetchMessages = vi.fn();

vi.mock("../useApi", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../useApi")>()),
  fetchMessages: (...args: unknown[]) => fetchMessages(...args),
}));

import { resetChatStores } from "@/stores";
import { currentConversationId } from "@/stores/activeConversationStore";
import { chatMessages, rememberMessageId } from "@/stores/chatMessageStore";
import { pendingPrompts } from "@/stores/chatQueueStore";

import { restMessageToChatMessage, syncFromCursor } from "./messageState";

describe("restMessageToChatMessage", () => {
  it("extracts text blocks from structured message content", () => {
    const msg = restMessageToChatMessage({
      id: "m1",
      role: "assistant",
      content: [
        { type: "text", text: "first paragraph" },
        { type: "text", text: "second paragraph" },
      ],
    });

    expect(msg.content).toBe("first paragraph\n\nsecond paragraph");
    expect(msg.content).not.toContain('"type"');
  });

  // The badge has to survive a refresh, which is the whole reason the marker
  // rides on persisted metadata instead of being a live-only WS cue.
  it("restores the background-wakeup marker from persisted metadata", () => {
    const msg = restMessageToChatMessage({
      id: "m-wake",
      role: "assistant",
      content: "the codex leg finished",
      metadata: { origin: "background_wakeup" },
    });

    expect(msg.wakeup).toBe(true);
  });

  it("leaves an ordinary persisted assistant row unmarked", () => {
    const msg = restMessageToChatMessage({ id: "m1", role: "assistant", content: "sure" });

    expect(msg.wakeup).toBeUndefined();
  });

  it("restores persisted session warnings with warning severity", () => {
    const msg = restMessageToChatMessage({
      id: "warning-1",
      role: "error",
      content: "The previous task reached its request threshold.",
      metadata: { notice_type: "session_warning" },
    });

    expect(msg.role).toBe("warning");
  });

  it("renders a persisted notice from its structured form", () => {
    const msg = restMessageToChatMessage({
      id: "retry-1",
      role: "error",
      content: "上游服务过载，1 分钟后自动重试（最多 3 次）。",
      metadata: { notice: { kind: "transient_retry", delay_seconds: 60, max_attempts: 3 } },
    });

    expect(msg.content).toContain("Retrying automatically in 1 min (up to 3 attempts)");
  });

  it("restores guardrail reminders persisted before warning metadata existed", () => {
    const msg = restMessageToChatMessage({
      id: "legacy-warning",
      role: "error",
      content:
        "上一条消息触发的任务已达到模型请求次数提醒：模型请求 108 次。本条消息仍会正常执行；建议新建对话。",
    });

    expect(msg.role).toBe("warning");
  });

  it("restores a completed tool duration from persisted metadata", () => {
    const msg = restMessageToChatMessage({
      id: "tool-1",
      role: "tool",
      content: { name: "Bash", input: { command: "sleep 2" } },
      metadata: { duration_ms: 2400 },
    });

    expect(msg).toMatchObject({
      activityType: "tool",
      toolDurationMs: 2400,
      toolCompleted: true,
    });
  });

  it("derives a history tool's start from its persisted end time", () => {
    const msg = restMessageToChatMessage({
      id: "tool-1",
      role: "tool",
      content: { name: "Bash", input: { command: "sleep 2" } },
      created_at: "2026-10-04T04:08:33Z",
      metadata: { duration_ms: 2400 },
    });

    expect(msg.toolStartedAt).toBe(Date.parse("2026-10-04T04:08:33Z") - 2400);
  });

  it("restores inbound IM image metadata for persisted user messages", () => {
    const msg = restMessageToChatMessage({
      id: "m-image",
      role: "user",
      content: "[Alice]",
      metadata: {
        attachments: [
          {
            name: "photo.png",
            mime: "image/png",
            path: ".daymug/agents/agent-1/uploads/photo.png",
            url: "/api/users/agent/files/read?path=photo.png",
          },
        ],
      },
    });

    expect(msg.attachments).toHaveLength(1);
    expect(msg.attachments?.[0].name).toBe("photo.png");
  });

  it("hides appended upload refs while preserving chat text and attachment metadata", () => {
    const attachment = {
      name: "photo.png",
      mime: "image/png",
      path: ".daymug/agents/agent-1/uploads/photo.png",
      url: "/api/users/agent/files/read?path=photo.png",
    };
    const msg = restMessageToChatMessage({
      id: "m-upload",
      role: "user",
      content: `请看这张图\n\n${attachment.path}`,
      metadata: { attachments: [attachment] },
    });

    expect(msg.content).toBe("请看这张图");
    expect(msg.attachments).toEqual([attachment]);
  });

  it("renders an attachment-only prompt without its internal upload ref", () => {
    const attachment = {
      name: "brief.pdf",
      mime: "application/pdf",
      path: ".daymug/agents/agent-1/uploads/brief.pdf",
      url: "/api/users/agent/files/read?path=brief.pdf",
    };
    const msg = restMessageToChatMessage({
      id: "m-file",
      role: "user",
      content: attachment.path,
      metadata: { attachments: [attachment] },
    });

    expect(msg.content).toBe("");
    expect(msg.attachments).toEqual([attachment]);
  });

  it("hides an absolute agent-facing ref when metadata stores the relative path", () => {
    const attachment = {
      name: "brief.pdf",
      mime: "application/pdf",
      path: "uploads/brief.pdf",
      url: "/api/users/agent/files/read?path=brief.pdf",
    };
    const msg = restMessageToChatMessage({
      id: "m-absolute-file",
      role: "user",
      content: `/home/agent/work/${attachment.path}`,
      metadata: { attachments: [attachment] },
    });

    expect(msg.content).toBe("");
    expect(msg.attachments).toEqual([attachment]);
  });

  it("restores an IM sender and removes its duplicate agent-facing content prefix", () => {
    const msg = restMessageToChatMessage({
      id: "m-slack",
      role: "user",
      content: "[Alice]: deploy now",
      metadata: {
        sender: { platform: "slack", id: "U_ALICE", name: "Alice" },
      },
    });

    expect(msg.sender).toEqual({ platform: "slack", id: "U_ALICE", name: "Alice" });
    expect(msg.content).toBe("deploy now");
  });
});

// A staged card is normally retired by the live `prompt_started` frame, and
// that frame cannot be replayed: the cursor already sits at the prompt's id,
// so every later backfill starts after it. Losing it once stranded the card
// under the very answer it asked for.
describe("syncFromCursor staging reconciliation", () => {
  const queuedRow = {
    id: "m-prompt",
    conversation_id: "c1",
    role: "user",
    content: "still relevant?",
    created_at: "2026-07-29T01:02:16Z",
  };
  const reply = {
    id: "m-reply",
    conversation_id: "c1",
    role: "assistant",
    content: "answered",
    created_at: "2026-07-29T01:03:05Z",
  };

  // syncFromCursor asks for rows after the cursor; the reconciliation pass
  // asks for the latest page (empty beforeId).
  function serve(latestPage: unknown[]) {
    fetchMessages.mockImplementation((_id: string, opts: { beforeId?: string }) =>
      Promise.resolve(opts?.beforeId === "" ? latestPage : []),
    );
  }

  function stagePrompt() {
    pendingPrompts.value.push({
      id: queuedRow.id,
      content: queuedRow.content,
      clientKey: `srv-${queuedRow.id}`,
    });
    rememberMessageId(queuedRow.id);
  }

  beforeEach(() => {
    resetChatStores();
    fetchMessages.mockReset();
    currentConversationId.value = "c1";
  });

  it("promotes a staged prompt the server has already dequeued, in timestamp order", async () => {
    stagePrompt();
    chatMessages.value.push(restMessageToChatMessage(reply));
    serve([{ ...queuedRow, queue_status: "" }, reply]);

    await syncFromCursor();

    expect(pendingPrompts.value).toHaveLength(0);
    expect(chatMessages.value.map((m) => m.id)).toEqual(["m-prompt", "m-reply"]);
  });

  it("leaves a prompt the server still reports as queued in the staging area", async () => {
    stagePrompt();
    serve([{ ...queuedRow, queue_status: "pending" }]);

    await syncFromCursor();

    expect(pendingPrompts.value.map((p) => p.id)).toEqual(["m-prompt"]);
    expect(chatMessages.value).toHaveLength(0);
  });

  it("skips the reconciliation request when nothing is staged", async () => {
    serve([]);

    await syncFromCursor();

    expect(fetchMessages).toHaveBeenCalledTimes(1);
  });
});
