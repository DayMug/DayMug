import { describe, it, expect, vi, beforeEach } from "vitest";

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

const memStore: Record<string, string> = {};
vi.stubGlobal("localStorage", {
  getItem: vi.fn((k: string) => memStore[k] ?? null),
  setItem: vi.fn((k: string, v: string) => {
    memStore[k] = v;
  }),
  removeItem: vi.fn((k: string) => {
    delete memStore[k];
  }),
  clear: vi.fn(() => {
    for (const k of Object.keys(memStore)) delete memStore[k];
  }),
  length: 0,
  key: vi.fn().mockReturnValue(null),
});

vi.mock("./useWebSocket", () => {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const _store: { handler: ((msg: any) => void) | null } = { handler: null };
  const instance = {
    isConnected: { value: true },
    reconnectAttempts: { value: 0 },
    connect: vi.fn(),
    disconnect: vi.fn(),
    send: vi.fn(() => true),
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    onMessage: (handler: (msg: any) => void) => {
      _store.handler = handler;
    },
    onReconnect: vi.fn(),
  };
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).__useChatTestWs = _store;
  // Singleton: useChat captures `const ws = useWebSocket()` at module-load,
  // so the instance returned here must match what tests inspect later.
  return {
    useWebSocket: () => instance,
  };
});

vi.mock("./useApi", () => ({
  fetchConversations: vi.fn().mockResolvedValue([]),
  fetchConversationsPage: vi.fn().mockResolvedValue([]),
  createConversation: vi.fn().mockResolvedValue({ id: "conv-new" }),
  fetchMessages: vi.fn().mockResolvedValue([]),
  fetchConversation: vi.fn().mockResolvedValue({
    id: "c1",
    title: "",
    user_id: "u1",
    work_dir: "/tmp",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    provider: "",
    model: "",
    created_at: "",
    updated_at: "",
  }),
  updateConversationWorkDir: vi.fn().mockResolvedValue({}),
  clearConversationContext: vi.fn().mockResolvedValue({
    id: "c1",
    title: "",
    user_id: "u1",
    work_dir: "/tmp",
    session_id: "rotated-session",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    provider: "",
    model: "",
    last_context_usage: "",
    created_at: "",
    updated_at: "",
  }),
}));

import { useChat } from "./useChat";
import { resetChatStores } from "@/stores";
import { markAttachmentDraft } from "@/stores/chatAttachmentDraftStore";
import { RECENT_MODEL_STORAGE_KEY } from "@/lib/recentModel";
import { flushStreamWrites } from "./chat/messageDispatcher";

// Stream-row writes are coalesced per animation frame; flushing after each
// frame lets these tests assert on the row the way a painted frame shows it.
function fireMessage(msg: Record<string, unknown>) {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const store = (globalThis as any).__useChatTestWs;
  store?.handler?.(msg);
  flushStreamWrites();
}

beforeEach(() => {
  // One call clears every chat store, including the dedup set — without it a
  // stamped id from an earlier test (e.g. via input_ack / result) would make a
  // later test see its message_id as already-known and short-circuit the push
  // path.
  resetChatStores();
  for (const k of Object.keys(memStore)) delete memStore[k];
});

describe("useChat", () => {
  it("returns expected state and actions", () => {
    const chat = useChat();
    expect(chat.chatMessages).toBeDefined();
    expect(chat.isThinking).toBeDefined();
    expect(chat.sendMessage).toBeInstanceOf(Function);
    expect(chat.cancelMessage).toBeInstanceOf(Function);
    expect(chat.setChatScrollFn).toBeInstanceOf(Function);
  });

  it("handles delta messages", () => {
    const { chatMessages, streamingText } = useChat();
    fireMessage({ type: "delta", content: "Hello" });
    expect(streamingText.value).toBe("Hello");
    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0].activityType).toBe("stream");
  });

  it("handles result messages", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "result", content: "Final answer" });
    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0].role).toBe("assistant");
    expect(chatMessages.value[0].content).toBe("Final answer");
  });

  it("increments resultVersion on result message", () => {
    const { resultVersion } = useChat();
    const before = resultVersion.value;
    fireMessage({ type: "result", content: "answer" });
    expect(resultVersion.value).toBe(before + 1);
  });

  it("handles status messages", () => {
    const { isThinking, isTurnStatusKnown } = useChat();
    expect(isTurnStatusKnown.value).toBe(false);
    fireMessage({ type: "status", status: "thinking" });
    expect(isThinking.value).toBe(true);
    expect(isTurnStatusKnown.value).toBe(true);
    fireMessage({ type: "status", status: "ready" });
    expect(isThinking.value).toBe(false);
  });

  it("init_status:ready snapshot clears stale isThinking after a missed status event", () => {
    // The bug we're guarding against: tab missed the post-result
    // `status: ready` (e.g. WS gap, broadcaster GC), so isThinking stays
    // stuck at true. The post-init snapshot heals it.
    const { isThinking, queuePosition, streamingText } = useChat();
    isThinking.value = true;
    queuePosition.value = 2;
    streamingText.value = "half-finished output";

    fireMessage({ type: "init_status", status: "ready", conversation_id: "c1" });

    expect(isThinking.value).toBe(false);
    expect(queuePosition.value).toBeNull();
    expect(streamingText.value).toBe("");
  });

  it("init_status:thinking snapshot keeps the indicator on for a busy conversation", () => {
    // Reconnecting to a busy room mid-run: snapshot says "thinking", so
    // a tab that opened cold (isThinking starts false) flips it on
    // instead of flashing ready.
    const { isThinking, isTurnStatusKnown } = useChat();
    isThinking.value = false;
    expect(isTurnStatusKnown.value).toBe(false);

    fireMessage({ type: "init_status", status: "thinking", conversation_id: "c1" });

    expect(isThinking.value).toBe(true);
    expect(isTurnStatusKnown.value).toBe(true);
  });

  it("init_status:ready leaves locally-queued pendingPrompts intact", () => {
    // A soft resync (visibility-driven re-init on a healthy WS) must
    // not eat un-acked text the user just typed. Server-persisted
    // pending rows come back via history_backfill; locally-queued
    // ones have to survive until input_ack stamps them.
    const { isThinking, pendingPrompts, sendMessage } = useChat();
    isThinking.value = true;
    sendMessage("not yet acked");

    fireMessage({ type: "init_status", status: "ready", conversation_id: "c1" });

    expect(pendingPrompts.value.length).toBe(1);
    expect(pendingPrompts.value[0].content).toBe("not yet acked");
  });

  it("result event sweeps the stream but leaves the turn running", () => {
    // `result` finalizes a message, not the turn: the backend persists one row
    // per result frame and only sends `status: ready` after the agent process
    // has been reaped. Clearing isThinking here used to unlock the composer
    // while the agent was still working. The lost-`ready` safety net now lives
    // in a timer (see messageDispatcher.stream.test.ts) instead.
    const { isThinking, streamingText } = useChat();
    isThinking.value = true;
    streamingText.value = "partial";

    fireMessage({ type: "result", content: "final answer", message_id: "m-r" });

    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("");
  });

  it("handles error messages", () => {
    const { chatMessages, isThinking } = useChat();
    fireMessage({ type: "status", status: "thinking" });
    fireMessage({ type: "error", message: "Something went wrong" });
    expect(isThinking.value).toBe(false);
    expect(chatMessages.value[0].role).toBe("error");
  });

  it("contextPercent computes correctly", () => {
    const { contextUsage, contextPercent } = useChat();
    expect(contextPercent.value).toBe(0);
    contextUsage.value = { used: 500, total: 1000 };
    expect(contextPercent.value).toBe(50);
  });

  it("sendMessage stages the prompt in pendingPrompts (not chatMessages)", () => {
    // Plan C: a queued prompt must NOT show up in chat history until the
    // dispatcher worker actually starts running it. The optimistic copy
    // lives in the staging area instead.
    const { chatMessages, pendingPrompts, sendMessage } = useChat();
    sendMessage("test message");
    expect(chatMessages.value.some((m) => m.role === "user")).toBe(false);
    expect(pendingPrompts.value.length).toBe(1);
    expect(pendingPrompts.value[0].content).toBe("test message");
    expect(pendingPrompts.value[0].clientKey).toBeTruthy();
  });

  it("sendMessage forwards slash-prefixed text to the agent unchanged", () => {
    const { chatMessages, pendingPrompts, sendMessage } = useChat();
    sendMessage("/help");
    expect(chatMessages.value.some((m) => m.role === "activity")).toBe(false);
    expect(pendingPrompts.value.map((p) => p.content)).toEqual(["/help"]);
  });

  it("sendMessage forwards to the WS even while a turn is in flight", async () => {
    // The backend dispatcher persists every prompt as 'pending' on
    // arrival and runs them serially per conversation, so the frontend
    // always sends — front-end queueing is a UI affordance only.
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { sendMessage, isThinking, pendingPrompts } = useChat();
    isThinking.value = true;
    sendMessage("queue me");

    expect(pendingPrompts.value.at(-1)?.content).toBe("queue me");
    expect(wsInstance.send).toHaveBeenCalledWith(
      expect.objectContaining({ type: "input", content: "queue me" }),
    );
    expect(vi.mocked(wsInstance.send).mock.calls.at(-1)?.[0]).not.toHaveProperty("steer");
  });

  it("sendMessage hands the prompt back when the socket drops before it leaves", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockReturnValueOnce(false);

    const { sendMessage, pendingPrompts, recallText, chatMessages } = useChat();
    const queuedBefore = pendingPrompts.value.length;
    sendMessage("lost in transit");

    // No phantom queued row that the server will never claim…
    expect(pendingPrompts.value).toHaveLength(queuedBefore);
    // …the text is back in the composer, and the transcript says why.
    expect(recallText.value).toBe("lost in transit");
    expect(chatMessages.value.at(-1)).toMatchObject({ role: "error" });
    expect(chatMessages.value.at(-1)?.content).toContain("connection dropped");
  });

  it("sendMessage requests live-task steering only for explicit Insert", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { sendMessage, isThinking } = useChat();
    isThinking.value = true;
    sendMessage("change direction", undefined, true);

    expect(wsInstance.send).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "input",
        content: "change direction",
        steer: true,
      }),
    );
  });

  it("keeps uploaded image metadata on the pending row and WS input", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();
    const attachment = {
      name: "shot.png",
      mime: "image/png",
      path: ".daymug/agents/agent-1/uploads/shot.png",
      url: "/api/users/agent/files/read?path=shot.png",
    };

    const { sendMessage, pendingPrompts } = useChat();
    sendMessage(`inspect it\n\n${attachment.path}`, [attachment]);

    expect(pendingPrompts.value.at(-1)?.attachments).toEqual([attachment]);
    expect(pendingPrompts.value.at(-1)?.content).toBe("inspect it");
    expect(wsInstance.send).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "input",
        content: `inspect it\n\n${attachment.path}`,
        attachments: [attachment],
      }),
    );
  });

  it("input_ack stamps the canonical id onto the matching pending entry", () => {
    const { pendingPrompts, sendMessage } = useChat();
    sendMessage("hello");
    expect(pendingPrompts.value.at(-1)?.id).toBeUndefined();

    fireMessage({ type: "input_ack", message_id: "msg-99", conversation_id: "c1" });

    expect(pendingPrompts.value.at(-1)?.id).toBe("msg-99");
  });

  it("prompt_started promotes the matching pending entry into chatMessages", () => {
    // Cue from the dispatcher worker that a prompt has actually started
    // running. The staging entry gets removed and a canonical user
    // message is appended to chatMessages so the chat history reads
    // chronologically.
    const { chatMessages, pendingPrompts, sendMessage } = useChat();
    sendMessage("alpha");
    fireMessage({ type: "input_ack", message_id: "m-a" });
    sendMessage("beta");
    fireMessage({ type: "input_ack", message_id: "m-b" });

    fireMessage({ type: "prompt_started", message_id: "m-a", conversation_id: "c1" });

    // 'alpha' moved to history; 'beta' still queued.
    expect(pendingPrompts.value.map((p) => p.content)).toEqual(["beta"]);
    const userRows = chatMessages.value.filter((m) => m.role === "user");
    expect(userRows.length).toBe(1);
    expect(userRows[0].content).toBe("alpha");
    expect(userRows[0].id).toBe("m-a");
  });

  it("prompt_started is a no-op when no matching pending entry exists", () => {
    // Reconnect race: the dispatcher fired prompt_started before the
    // client had a chance to ingest the user prompt. The client's
    // history_backfill / REST sync will deliver the row separately.
    const { pendingPrompts, chatMessages } = useChat();
    fireMessage({ type: "prompt_started", message_id: "ghost" });
    expect(pendingPrompts.value.length).toBe(0);
    expect(chatMessages.value.length).toBe(0);
  });

  it("cancelPendingPrompt drops the entry locally and sends cancel with message_id when known", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { pendingPrompts, sendMessage, cancelPendingPrompt } = useChat();
    sendMessage("first");
    fireMessage({ type: "input_ack", message_id: "m-first" });
    sendMessage("second");
    fireMessage({ type: "input_ack", message_id: "m-second" });

    cancelPendingPrompt("m-second");

    expect(pendingPrompts.value.map((p) => p.id)).toEqual(["m-first"]);
    expect(wsInstance.send).toHaveBeenLastCalledWith(
      expect.objectContaining({ type: "cancel", message_id: "m-second" }),
    );
  });

  it("cancelPendingPrompt by clientKey skips the WS roundtrip when the id is not yet known", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { pendingPrompts, sendMessage, cancelPendingPrompt } = useChat();
    sendMessage("racing-cancel");
    const key = pendingPrompts.value[0].clientKey;

    cancelPendingPrompt(key);

    expect(pendingPrompts.value.length).toBe(0);
    // Only the original input was sent; no cancel frame.
    expect(vi.mocked(wsInstance.send).mock.calls.some((c) => c[0].type === "cancel")).toBe(false);
  });

  it("cancelPendingPrompt cancels the run instead of recalling an IM prompt", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { pendingPrompts, cancelPendingPrompt, recallText } = useChat();
    fireMessage({
      type: "user_message",
      message_id: "im-1",
      content: "\u90e8\u7f72\u4e00\u4e0b",
      queue_status: "pool_queued",
    });

    cancelPendingPrompt("im-1");

    // A whole-conversation cancel (no message_id): the per-message endpoint
    // only knows the dispatcher's own 'pending' rows and would not find this one.
    const cancels = vi.mocked(wsInstance.send).mock.calls.filter((c) => c[0].type === "cancel");
    expect(cancels).toHaveLength(1);
    expect(cancels[0][0].message_id).toBeUndefined();
    // Nothing to recall: the text was typed in the IM client, not here.
    expect(recallText.value).toBe("");
    // The card stays until the server retires it, so the UI never claims a
    // cancel that did not land.
    expect(pendingPrompts.value.map((p) => p.id)).toEqual(["im-1"]);
  });

  it("cancelMessage does not recall IM prompts into the composer", async () => {
    const { cancelMessage, recallText, sendMessage } = useChat();
    sendMessage("typed here");
    fireMessage({
      type: "user_message",
      message_id: "im-2",
      content: "from wechat",
      queue_status: "pool_queued",
    });

    cancelMessage();

    expect(recallText.value).toBe("typed here");
  });

  it("invokes the registered scroll fn with immediate=true after switching conversations", async () => {
    // Streaming auto-scroll is gated on the reader already being at the
    // tail. When the user actively switches into a conversation, that
    // gate is wrong — they should always land at the latest message.
    // Lock in the contract: switchToConversation calls the scroll fn
    // with immediate=true after messages are loaded.
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);

    const { switchToConversation, setChatScrollFn, currentConversationId } = useChat();
    currentConversationId.value = "old";
    const scrollFn = vi.fn();
    setChatScrollFn(scrollFn);

    await switchToConversation("c1");

    expect(scrollFn).toHaveBeenCalled();
    const lastCall = scrollFn.mock.calls.at(-1);
    expect(lastCall?.[0]).toBe(true);
  });

  it("clears thinking state when switching conversations so the new one starts idle", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);

    const { switchToConversation, currentConversationId, isThinking } = useChat();

    // Simulate being mid-thinking on the previous conversation.
    currentConversationId.value = "old";
    isThinking.value = true;

    await switchToConversation("c1");

    // Without the reset, isThinking would leak across and the input box
    // would stay in queue mode even though the new conversation is idle.
    expect(isThinking.value).toBe(false);
  });

  it("does not carry the previous conversation's thinking into a switched-to running turn", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { switchToConversation, currentConversationId, chatMessages } = useChat();
    currentConversationId.value = "old";
    fireMessage({ type: "thinking_delta", content: "old reasoning" });

    await switchToConversation("next");
    const init = vi
      .mocked(wsInstance.send)
      .mock.calls.map((call) => call[0])
      .findLast((msg) => msg.type === "init");
    const sub = { conversation_id: "next", subscription_id: init?.subscription_id };

    // The target turn already ran a tool, so its replay starts past the
    // `status: thinking` that would otherwise have reset the buffers.
    fireMessage({ type: "init_status", status: "thinking", ...sub });
    fireMessage({ type: "tool_use", content: "Bash", replay: true, ...sub });
    fireMessage({ type: "thinking_delta", content: "new reasoning", replay: true, ...sub });

    const thinking = chatMessages.value.filter(
      (m) => m.role === "activity" && m.activityType === "thinking",
    );
    expect(thinking.map((m) => m.content)).toEqual(["new reasoning"]);
  });

  it("reuses an open websocket and sends a new logical subscription on switch", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();
    vi.mocked(wsInstance.connect).mockClear();
    vi.mocked(wsInstance.disconnect).mockClear();

    const { switchToConversation, currentConversationId } = useChat();
    currentConversationId.value = "old";
    await switchToConversation("next");

    expect(wsInstance.disconnect).not.toHaveBeenCalled();
    expect(wsInstance.connect).not.toHaveBeenCalled();
    expect(wsInstance.send).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "init",
        conversation_id: "next",
        subscription_id: expect.stringMatching(/^sub-\d+$/),
      }),
    );
  });

  it("drops delayed frames from an earlier conversation subscription", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { switchToConversation, currentConversationId, streamingText } = useChat();
    currentConversationId.value = "old";
    await switchToConversation("next");
    const init = vi
      .mocked(wsInstance.send)
      .mock.calls.map((call) => call[0])
      .findLast((msg) => msg.type === "init");

    fireMessage({
      type: "delta",
      content: "stale",
      conversation_id: "old",
      subscription_id: "sub-stale",
    });
    expect(streamingText.value).toBe("");

    fireMessage({
      type: "delta",
      content: "fresh",
      conversation_id: "next",
      subscription_id: init?.subscription_id,
    });
    expect(streamingText.value).toBe("fresh");
  });

  it("drops a queued A frame after a rapid A to B to A switch", async () => {
    const wsMock = await import("./useWebSocket");
    const wsInstance = wsMock.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();

    const { switchToConversation, currentConversationId, streamingText } = useChat();
    currentConversationId.value = "";
    await switchToConversation("a");
    const firstA = vi
      .mocked(wsInstance.send)
      .mock.calls.map((call) => call[0])
      .findLast((msg) => msg.type === "init");
    await switchToConversation("b");
    await switchToConversation("a");
    const latestA = vi
      .mocked(wsInstance.send)
      .mock.calls.map((call) => call[0])
      .findLast((msg) => msg.type === "init");

    fireMessage({
      type: "delta",
      content: "old-a",
      conversation_id: "a",
      subscription_id: firstA?.subscription_id,
    });
    expect(streamingText.value).toBe("");

    fireMessage({
      type: "delta",
      content: "new-a",
      conversation_id: "a",
      subscription_id: latestA?.subscription_id,
    });
    expect(streamingText.value).toBe("new-a");
  });

  it("keeps user-hub lifecycle events global across conversation subscriptions", async () => {
    const { switchToConversation, currentConversationId, conversationListEvent } = useChat();
    currentConversationId.value = "old";
    await switchToConversation("active");

    fireMessage({
      type: "conversation_removed",
      conversation_id: "another-conversation",
    });

    expect(conversationListEvent.value).toMatchObject({
      kind: "removed",
      id: "another-conversation",
    });
  });

  it("switchToConversation surfaces the full workdir hint when no user message exists yet", async () => {
    const { fetchMessages, fetchConversation } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);
    vi.mocked(fetchConversation).mockResolvedValue({
      id: "cWdHint1",
      user_id: "u1",
      title: "",
      work_dir: "/srv/projects/foo",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cWdHint1");

    const hint = chatMessages.value.find((m) => m.role === "activity" && m.activityType === "info");
    expect(hint?.content).toBe(
      "Your current working directory is /srv/projects/foo. To switch, navigate in the workspace panel on the right.",
    );
  });

  it("switchToConversation surfaces a shortened workdir hint once the conversation has user messages", async () => {
    const { fetchMessages, fetchConversation } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([
      {
        id: "u-1",
        conversation_id: "cWdHint2",
        role: "user",
        content: "hi",
        created_at: "",
      },
    ]);
    vi.mocked(fetchConversation).mockResolvedValue({
      id: "cWdHint2",
      user_id: "u1",
      title: "",
      work_dir: "/srv/projects/bar",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cWdHint2");

    const hint = chatMessages.value.find((m) => m.role === "activity" && m.activityType === "info");
    expect(hint?.content).toBe("Your current working directory is /srv/projects/bar.");
  });

  it("sendMessage downgrades the workdir hint to the shortened form on first send", async () => {
    const { fetchMessages, fetchConversation } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);
    vi.mocked(fetchConversation).mockResolvedValue({
      id: "cWdHint3",
      user_id: "u1",
      title: "",
      work_dir: "/srv/projects/baz",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    const { switchToConversation, chatMessages, currentConversationId, sendMessage } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cWdHint3");

    const hintBefore = chatMessages.value.find(
      (m) => m.role === "activity" && m.activityType === "info",
    );
    expect(hintBefore?.content).toContain("To switch, navigate in the workspace panel");

    sendMessage("first prompt");

    const hintAfter = chatMessages.value.find(
      (m) => m.role === "activity" && m.activityType === "info",
    );
    expect(hintAfter?.content).toBe("Your current working directory is /srv/projects/baz.");
  });

  it("queue_status decorates the head pending entry with the pool position", () => {
    // The pool is per-account FIFO across conversations. Only the
    // head-of-queue entry is actually waiting on a pool slot, so we
    // surface the position there (the staging-area UI renders it as
    // "账号繁忙 · 前面还有 N 个任务在等待").
    const { pendingPrompts, sendMessage, queuePosition, isThinking } = useChat();
    sendMessage("hold me");

    fireMessage({
      type: "queue_status",
      status: "queued",
      queue_position: 3,
      queue_ahead: 6,
      queue_running: 4,
    });

    expect(pendingPrompts.value[0].poolPosition).toBe(3);
    expect(pendingPrompts.value[0].poolAhead).toBe(6);
    expect(pendingPrompts.value[0].poolRunning).toBe(4);
    expect(queuePosition.value).toBe(3);
    expect(isThinking.value).toBe(true);
  });

  it("status=ready clears queuePosition", () => {
    const { sendMessage, queuePosition } = useChat();
    sendMessage("a");
    fireMessage({ type: "queue_status", status: "queued", queue_position: 2 });
    expect(queuePosition.value).toBe(2);

    fireMessage({ type: "status", status: "ready" });

    expect(queuePosition.value).toBeNull();
  });

  it("broad cancel recalls the queued prompts into the editor, then cancel_ack clears the staging area", () => {
    const { pendingPrompts, chatMessages, sendMessage, cancelMessage, isThinking, recallText } =
      useChat();
    sendMessage("first");
    fireMessage({ type: "input_ack", message_id: "m-first" });
    sendMessage("second");
    fireMessage({ type: "input_ack", message_id: "m-second" });
    sendMessage("third");
    fireMessage({ type: "input_ack", message_id: "m-third" });

    // Worker has started "first" — promote it into chatMessages so the
    // remaining staging area only carries the queued ones.
    fireMessage({ type: "prompt_started", message_id: "m-first" });
    isThinking.value = true;

    // User clicks the broad Cancel button. The queued prompts must be
    // recalled into the editor *now*, client-side — not silently dropped.
    cancelMessage();
    expect(recallText.value).toBe("second\n\nthird");

    // Server confirms the drop; staging area clears, but the in-flight
    // 'first' stays in history (it actually ran, partial output anchored).
    fireMessage({
      type: "cancel_ack",
      cancelled_prompts: [
        { id: "m-second", content: "second", role: "user", conversation_id: "c1" },
        { id: "m-third", content: "third", role: "user", conversation_id: "c1" },
      ],
    });

    expect(pendingPrompts.value.length).toBe(0);
    const userRows = chatMessages.value.filter((m) => m.role === "user").map((m) => m.content);
    expect(userRows).toEqual(["first"]);
  });

  it("broad cancel recalls a single queued prompt too (count-of-one is not dropped)", () => {
    // Regression: the old cancel_ack heuristic only refilled the editor
    // when >1 row was dropped, so a lone queued message was lost on a
    // broad cancel. Recall now happens client-side from pendingPrompts
    // regardless of count.
    const { sendMessage, cancelMessage, isThinking, recallText } = useChat();
    sendMessage("first");
    fireMessage({ type: "input_ack", message_id: "m-first" });
    sendMessage("only queued");
    fireMessage({ type: "input_ack", message_id: "m-queued" });
    fireMessage({ type: "prompt_started", message_id: "m-first" });
    isThinking.value = true;

    cancelMessage();

    expect(recallText.value).toBe("only queued");
  });

  it("broad cancel marks the in-flight user message as cancelled for the muted bubble variant", () => {
    // The in-flight prompt stays in chatMessages — broad cancel only
    // drops *pending* prompts on the server side. Without a marker the
    // row would look indistinguishable from a turn that finished cleanly,
    // so cancelMessage stamps `cancelled` on the most recent user row to
    // drive the muted-bubble + "Cancelled" badge in ChatMessageItem.
    const { chatMessages, isThinking, cancelMessage } = useChat();
    chatMessages.value.push({ role: "user", content: "first", id: "m-1" });
    chatMessages.value.push({ role: "assistant", content: "partial reply" });
    chatMessages.value.push({ role: "user", content: "second", id: "m-2" });
    isThinking.value = true;

    cancelMessage();

    // The most recent user row (and only it) carries the marker; older
    // turns that completed normally must stay un-marked.
    const userRows = chatMessages.value.filter((m) => m.role === "user");
    expect(userRows.map((m) => m.content)).toEqual(["first", "second"]);
    expect(userRows[0].cancelled).toBeFalsy();
    expect(userRows[1].cancelled).toBe(true);
  });

  it("cancelMessage is a no-op on the bubble style when nothing is in flight", () => {
    // The broad-cancel UI button is hidden when no turn is running, but
    // a stale click (or a future caller invoking cancelMessage directly)
    // must not retroactively mark a long-completed row.
    const { chatMessages, isThinking, cancelMessage } = useChat();
    chatMessages.value.push({ role: "user", content: "old turn", id: "m-old" });
    isThinking.value = false;

    cancelMessage();

    expect(chatMessages.value[0].cancelled).toBeFalsy();
  });

  it("targeted cancel removes the pending entry and recalls its text into the editor", () => {
    // Per-message recall path: clicking the X on a queued bubble
    // recalls (not discards) the text — the affordance reads as "edit
    // this", and re-typing a long prompt would be hostile. The narrow
    // cancel_ack from the server is just a confirmation; recallText is
    // populated client-side at the moment of the click.
    const { pendingPrompts, sendMessage, recallText, recallAttachments, cancelPendingPrompt } =
      useChat();
    const attachment = {
      name: "queued.png",
      mime: "image/png",
      path: ".daymug/agents/a/uploads/queued.png",
      url: "/api/users/a/files/read?path=queued.png",
    };
    sendMessage("draft to edit", [attachment]);
    fireMessage({ type: "input_ack", message_id: "m-draft" });
    cancelPendingPrompt("m-draft");

    expect(pendingPrompts.value.length).toBe(0);
    expect(recallText.value).toBe("draft to edit");
    expect(recallAttachments.value).toEqual([attachment]);

    // Server confirmation arrives — single-row cancel_ack must NOT
    // overwrite the recall text we already set, and must not push the
    // dropped row back into either bucket.
    const before = recallText.value;
    fireMessage({
      type: "cancel_ack",
      cancelled_prompts: [
        { id: "m-draft", content: "draft to edit", role: "user", conversation_id: "c1" },
      ],
    });
    expect(pendingPrompts.value.length).toBe(0);
    expect(recallText.value).toBe(before);
  });

  it("history_backfill routes pending rows to the staging area", () => {
    // Reconnect path: a pending row from the DB must land in the
    // staging area (recallable) instead of inlining where it would
    // appear out-of-order against the in-flight task's tool calls.
    const { pendingPrompts, chatMessages } = useChat();
    fireMessage({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [
        {
          id: "m-h",
          role: "user",
          content: "earlier reply",
          conversation_id: "c1",
          created_at: "",
        },
        {
          id: "m-q",
          role: "user",
          content: "queued earlier",
          conversation_id: "c1",
          created_at: "",
          queue_status: "pending",
        },
      ],
    });

    expect(pendingPrompts.value.length).toBe(1);
    expect(pendingPrompts.value[0].id).toBe("m-q");
    const userInHistory = chatMessages.value.filter((m) => m.role === "user").map((m) => m.content);
    expect(userInHistory).toEqual(["earlier reply"]);
  });

  // The common case: the message arrives on WeChat, then the user opens the web
  // page. Nothing was delivered live to this tab, so the queued state has to
  // survive a plain REST load or the row renders as an ordinary sent bubble.
  it("stages an IM prompt still waiting for a slot when the conversation is loaded", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([
      {
        id: "im-1",
        role: "user",
        content: "\u90e8\u7f72\u4e00\u4e0b",
        conversation_id: "c-im",
        created_at: "",
        queue_status: "pool_queued",
      },
    ]);

    const { switchToConversation, pendingPrompts, chatMessages } = useChat();
    await switchToConversation("c-im");

    expect(pendingPrompts.value).toHaveLength(1);
    expect(pendingPrompts.value[0]).toMatchObject({ id: "im-1", fromIM: true });
    expect(chatMessages.value.filter((m) => m.role === "user")).toHaveLength(0);
  });

  it("clears queue/recall state on conversation switch so it doesn't leak", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);

    const { switchToConversation, currentConversationId, sendMessage, queuePosition, recallText } =
      useChat();
    currentConversationId.value = "old";
    sendMessage("stale");
    fireMessage({ type: "queue_status", status: "queued", queue_position: 2 });
    recallText.value = "leftover";

    await switchToConversation("c1");

    expect(queuePosition.value).toBeNull();
    expect(recallText.value).toBe("");
  });

  it("keeps the work directory locked when switching back to an attachment draft", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);
    markAttachmentDraft("c1", true);

    const { switchToConversation, currentConversationId, isWorkDirLocked } = useChat();
    currentConversationId.value = "old";
    await switchToConversation("c1");

    expect(isWorkDirLocked.value).toBe(true);
  });

  it("persist_failed surfaces an inline error row so users learn about silent save loss", () => {
    const { chatMessages } = useChat();
    fireMessage({
      type: "persist_failed",
      message: "assistant reply not saved — your view may be stale after a refresh",
    });
    const tail = chatMessages.value.at(-1);
    expect(tail?.role).toBe("error");
    expect(tail?.content).toContain("not saved");
  });

  it("user_message peer echo dedups by id against a prompt_started-promoted row", () => {
    const { chatMessages, sendMessage } = useChat();
    sendMessage("hello");
    fireMessage({ type: "input_ack", message_id: "u-1" });
    // The dispatcher worker started the prompt; staging entry moves
    // into chatMessages as a regular user row.
    fireMessage({ type: "prompt_started", message_id: "u-1" });
    expect(chatMessages.value.filter((m) => m.role === "user").length).toBe(1);

    // Server delivers the peer-tab echo with the same id — must be a no-op.
    fireMessage({ type: "user_message", content: "hello", message_id: "u-1" });
    expect(chatMessages.value.filter((m) => m.role === "user").length).toBe(1);
  });

  it("publishes titleUpdate ref for title_updated messages", () => {
    const { titleUpdate } = useChat();
    const before = titleUpdate.value?.version ?? 0;

    fireMessage({ type: "title_updated", conversation_id: "c1", title: "New Title" });

    expect(titleUpdate.value).toMatchObject({ id: "c1", title: "New Title" });
    expect(titleUpdate.value?.version).toBeGreaterThan(before);
  });

  it("ignores title_updated when conversation_id or title is missing", () => {
    const { titleUpdate } = useChat();
    const before = titleUpdate.value;

    fireMessage({ type: "title_updated", title: "no id" });
    fireMessage({ type: "title_updated", conversation_id: "c1" });

    expect(titleUpdate.value).toBe(before);
  });

  it("publishes conversationListEvent ref for conversation_added messages", () => {
    const { conversationListEvent } = useChat();
    const before = conversationListEvent.value?.version ?? 0;

    fireMessage({
      type: "conversation_added",
      conversation: {
        id: "new-conv",
        user_id: "u1",
        title: "Fresh",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    });

    expect(conversationListEvent.value?.kind).toBe("added");
    expect(conversationListEvent.value?.id).toBe("new-conv");
    expect(conversationListEvent.value?.conversation?.title).toBe("Fresh");
    expect(conversationListEvent.value!.version).toBeGreaterThan(before);
  });

  it("publishes conversationListEvent ref for conversation_updated messages", () => {
    const { conversationListEvent } = useChat();
    const before = conversationListEvent.value?.version ?? 0;

    // The backend sends the full store.Conversation row; pin/share/account
    // fields used to be dropped by a hand-rolled subset type, leaving peer
    // tabs unaware a conversation was pinned or shared elsewhere.
    fireMessage({
      type: "conversation_updated",
      conversation: {
        id: "c1",
        user_id: "u1",
        title: "Renamed by peer",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: true,
        pinned: true,
        pin_order: 2,
        shared: true,
        account_name: "codex-work",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    });

    expect(conversationListEvent.value?.kind).toBe("updated");
    expect(conversationListEvent.value?.conversation?.title).toBe("Renamed by peer");
    expect(conversationListEvent.value?.conversation?.pinned).toBe(true);
    expect(conversationListEvent.value?.conversation?.pin_order).toBe(2);
    expect(conversationListEvent.value?.conversation?.shared).toBe(true);
    expect(conversationListEvent.value?.conversation?.account_name).toBe("codex-work");
    expect(conversationListEvent.value!.version).toBeGreaterThan(before);
  });

  it("publishes conversationListEvent ref for conversation_removed messages", () => {
    const { conversationListEvent } = useChat();
    const before = conversationListEvent.value?.version ?? 0;

    fireMessage({ type: "conversation_removed", conversation_id: "c-doomed" });

    expect(conversationListEvent.value?.kind).toBe("removed");
    expect(conversationListEvent.value?.id).toBe("c-doomed");
    expect(conversationListEvent.value?.conversation).toBeUndefined();
    expect(conversationListEvent.value!.version).toBeGreaterThan(before);
  });

  it("applies authoritative running activity snapshots from websocket frames", () => {
    const { runningAgentIds, runningConversationIds, runningConversations } = useChat();

    const activity = {
      conversation_id: "conv-1",
      agent_id: "agent-1",
      model: "claude-sonnet-4-5",
      started_at: "2026-07-22T09:30:00Z",
    };

    fireMessage({
      type: "conversation_activity",
      running_agent_ids: ["agent-1"],
      running_conversation_ids: ["conv-1", "conv-2"],
      running_conversations: [activity],
    });
    expect(runningAgentIds.value).toEqual(["agent-1"]);
    expect(runningConversationIds.value).toEqual(["conv-1", "conv-2"]);
    expect(runningConversations.value).toEqual([activity]);

    fireMessage({ type: "conversation_activity" });
    expect(runningAgentIds.value).toEqual([]);
    expect(runningConversationIds.value).toEqual([]);
    expect(runningConversations.value).toEqual([]);
  });

  it("hydrates running activity from the websocket init snapshot", () => {
    const { runningAgentIds, runningConversationIds, runningConversations } = useChat();
    fireMessage({
      type: "init_ok",
      running_agent_ids: ["agent-cold"],
      running_conversation_ids: ["conv-cold"],
      running_conversations: [
        {
          conversation_id: "conv-cold",
          agent_id: "agent-cold",
          model: "gpt-5.2-codex",
          started_at: "2026-07-22T10:00:00Z",
        },
      ],
    });

    expect(runningAgentIds.value).toEqual(["agent-cold"]);
    expect(runningConversationIds.value).toEqual(["conv-cold"]);
    expect(runningConversations.value[0]?.model).toBe("gpt-5.2-codex");
  });

  it("applies queued activity separately from running activity", () => {
    const { activeAgentIds, activeConversationIds, queuedConversations, runningConversations } =
      useChat();
    const queued = {
      conversation_id: "conv-queued",
      agent_id: "agent-queued",
      agent_name: "Queued Agent",
      account_name: "codex-work",
      model: "gpt-5.6-sol",
      started_at: "2026-07-22T10:01:00Z",
    };

    fireMessage({
      type: "conversation_activity",
      running_conversations: [],
      queued_conversations: [queued],
    });

    expect(runningConversations.value).toEqual([]);
    expect(queuedConversations.value).toEqual([queued]);
    expect(activeAgentIds.value).toEqual(["agent-queued"]);
    expect(activeConversationIds.value).toEqual(["conv-queued"]);

    fireMessage({ type: "conversation_activity" });
    expect(queuedConversations.value).toEqual([]);
    expect(activeAgentIds.value).toEqual([]);
    expect(activeConversationIds.value).toEqual([]);
  });

  it("ignores conversation_added when no conversation payload is present", () => {
    const { conversationListEvent } = useChat();
    const before = conversationListEvent.value;

    fireMessage({ type: "conversation_added" });

    expect(conversationListEvent.value).toBe(before);
  });

  it("routes peer-tab user_message echo into the staging area until prompt_started fires", () => {
    // The sender's own path puts the prompt in pendingPrompts and only
    // promotes it to chatMessages on prompt_started. Peer tabs must
    // mirror that flow or the cross-tab UIs diverge ("Queued" on the
    // sender, regular chat row on the peer).
    const { chatMessages, pendingPrompts } = useChat();
    fireMessage({
      type: "user_message",
      content: "[Alice]: hello from Slack",
      message_id: "u-peer-1",
      metadata: {
        sender: { platform: "slack", id: "U_ALICE", name: "Alice" },
      },
    });
    expect(chatMessages.value.length).toBe(0);
    expect(pendingPrompts.value.length).toBe(1);
    expect(pendingPrompts.value[0].id).toBe("u-peer-1");
    expect(pendingPrompts.value[0].content).toBe("hello from Slack");
    expect(pendingPrompts.value[0].sender?.name).toBe("Alice");

    // Worker claims the prompt — staging entry promotes into chat history.
    fireMessage({ type: "prompt_started", message_id: "u-peer-1" });
    expect(pendingPrompts.value.length).toBe(0);
    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0]).toMatchObject({
      role: "user",
      content: "hello from Slack",
      sender: { platform: "slack", id: "U_ALICE", name: "Alice" },
    });
  });

  it("inherits the active queuePosition onto a peer-echoed staging entry", () => {
    // Ordering quirk: queue_status can land before user_message at a
    // peer (different goroutines, same broadcaster room). Make sure
    // the staging entry picks up the already-known pool position so
    // the head label matches the sender's "Next up".
    const { pendingPrompts, queuePosition } = useChat();
    fireMessage({ type: "queue_status", queue_position: 1 });
    expect(queuePosition.value).toBe(1);
    fireMessage({ type: "user_message", content: "queued first", message_id: "u-peer-2" });
    expect(pendingPrompts.value.length).toBe(1);
    expect(pendingPrompts.value[0].poolPosition).toBe(1);
  });

  it("falls back to chatMessages when prompt_started races ahead of user_message", () => {
    // Worker thread can win the broadcaster mutex over the input
    // handler when the pool slot is granted instantly. In that race
    // prompt_started arrives first carrying the id but no content;
    // the user_message handler must push straight to chatMessages so
    // the row isn't stranded in the staging area.
    const { chatMessages, pendingPrompts } = useChat();
    fireMessage({ type: "prompt_started", message_id: "u-peer-race" });
    expect(pendingPrompts.value.length).toBe(0);
    fireMessage({ type: "user_message", content: "raced ahead", message_id: "u-peer-race" });
    expect(pendingPrompts.value.length).toBe(0);
    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0]).toMatchObject({ role: "user", content: "raced ahead" });
  });

  it("ignores user_message with empty content", () => {
    const { chatMessages, pendingPrompts } = useChat();
    fireMessage({ type: "user_message", content: "" });
    expect(chatMessages.value.length).toBe(0);
    expect(pendingPrompts.value.length).toBe(0);
  });

  it("handles thinking_delta messages", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "thinking_delta", content: "Let me think" });
    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0].role).toBe("activity");
    expect(chatMessages.value[0].activityType).toBe("thinking");
    expect(chatMessages.value[0].content).toBe("Let me think");

    fireMessage({ type: "thinking_delta", content: " about this" });
    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0].content).toBe("Let me think about this");
  });

  it("does not append replayed assistant and thinking chunks twice", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "delta", content: "Build is running" });
    fireMessage({ type: "thinking_delta", content: "Checking status" });

    fireMessage({ type: "delta", content: "Build is running", replay: true });
    fireMessage({ type: "thinking_delta", content: "Checking status", replay: true });

    expect(chatMessages.value.find((m) => m.activityType === "stream")?.content).toBe(
      "Build is running",
    );
    expect(chatMessages.value.find((m) => m.activityType === "thinking")?.content).toBe(
      "Checking status",
    );
  });

  it("treats cumulative thinking_delta payloads as snapshots instead of appending duplicates", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "thinking_delta", content: "Checking language and instructions" });
    fireMessage({
      type: "thinking_delta",
      content: "Checking language and instructions\n\nSetting up the console",
    });

    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0].content).toBe(
      "Checking language and instructions\n\nSetting up the console",
    );
  });

  it("keeps updating the same thinking row after tool activity", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "thinking_delta", content: "Considering git commands and tags" });
    fireMessage({
      type: "tool_use_start",
      content: JSON.stringify({ id: "tool-1", name: "Bash", command: "git status" }),
    });
    fireMessage({
      type: "tool_result",
      content: JSON.stringify({
        id: "tool-1",
        name: "Bash",
        input: { command: "git status" },
        content: "clean",
      }),
    });
    fireMessage({
      type: "thinking_delta",
      content: "Considering git commands and tags\n\nExecuting a git rebase",
    });

    const thinkingRows = chatMessages.value.filter(
      (m) => m.role === "activity" && m.activityType === "thinking",
    );
    expect(thinkingRows.length).toBe(1);
    expect(thinkingRows[0].content).toBe(
      "Considering git commands and tags\n\nExecuting a git rebase",
    );
  });

  it("shows a Codex command as soon as tool_use_start arrives", () => {
    const { chatMessages } = useChat();
    fireMessage({
      type: "tool_use_start",
      content: JSON.stringify({ id: "tool-1", name: "Bash", command: "docker compose up" }),
      tool_started_at: 1000,
    });

    const tool = chatMessages.value.find((m) => m.activityType === "tool");
    expect(tool?.content).toContain("[Bash] calling...");
    expect(tool?.content).toContain("command: docker compose up");
    expect(tool?.toolStartedAt).toBe(1000);

    fireMessage({
      type: "tool_result",
      content: JSON.stringify({
        id: "tool-1",
        name: "Bash",
        input: { command: "docker compose up" },
      }),
      tool_duration_ms: 2400,
    });
    expect(tool?.toolDurationMs).toBe(2400);
    expect(tool?.toolCompleted).toBe(true);
  });

  it("treats cumulative sub-agent thinking_delta payloads as snapshots", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "thinking_delta", subagent: true, content: "Search repo" });
    fireMessage({ type: "thinking_delta", subagent: true, content: "Search repo\n\nPatch file" });

    expect(chatMessages.value.length).toBe(1);
    expect(chatMessages.value[0]).toMatchObject({
      activityType: "thinking",
      subagent: true,
      content: "Search repo\n\nPatch file",
    });
  });

  // Under the id-based architecture, dedup happens by message_id, NOT by
  // string-prefix matching. Replay events for an assistant turn whose
  // canonical id REST already loaded must be skipped — the result event
  // carries message_id and is suppressed if known.
  it("result with a known message_id sweeps the stream activity but does not double-render", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "u1", role: "user", content: "hi", conversation_id: "c2", created_at: "" },
      {
        id: "a1",
        role: "assistant",
        content: "Hello world",
        conversation_id: "c2",
        created_at: "",
      },
    ]);

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c2");

    // Simulate replay that arrives after the canonical row is in REST.
    fireMessage({ type: "status", status: "thinking" });
    fireMessage({ type: "delta", content: "Hello " });
    fireMessage({ type: "delta", content: "world" });
    fireMessage({ type: "result", content: "Hello world", message_id: "a1" });

    const assistants = chatMessages.value.filter((m) => m.role === "assistant");
    expect(assistants.length).toBe(1);
    expect(assistants[0].content).toBe("Hello world");
    expect(assistants[0].id).toBe("a1");
    // No leftover stream activity.
    expect(
      chatMessages.value.some((m) => m.role === "activity" && m.activityType === "stream"),
    ).toBe(false);
  });

  it("result without a known id pushes the assistant fresh and stamps the id for future dedup", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "status", status: "thinking" });
    fireMessage({ type: "delta", content: "answer" });
    fireMessage({ type: "result", content: "answer", message_id: "a-fresh" });
    const tail = chatMessages.value.at(-1);
    expect(tail?.role).toBe("assistant");
    expect(tail?.id).toBe("a-fresh");
    // A subsequent backfill carrying the same id is a no-op (id-based dedup).
    fireMessage({
      type: "history_backfill",
      conversation_id: "c1",
      messages: [
        {
          id: "a-fresh",
          role: "assistant",
          content: "answer",
          conversation_id: "c1",
          created_at: "",
        },
      ],
    });
    expect(chatMessages.value.filter((m) => m.role === "assistant").length).toBe(1);
  });

  it("carries generated image metadata from a live result onto the assistant message", () => {
    const { chatMessages } = useChat();
    const attachment = {
      name: "chart.png",
      mime: "image/png",
      path: "./chart.png",
      url: "/api/users/agent/files/read?path=chart.png",
    };

    fireMessage({
      type: "result",
      content: "图片已生成",
      message_id: "a-image",
      metadata: { attachments: [attachment] },
    });

    expect(chatMessages.value.at(-1)?.attachments).toEqual([attachment]);
  });

  it("thinking_delta keeps mutating the in-flight thinking row but stops once the id is claimed", () => {
    const { chatMessages } = useChat();
    chatMessages.value = [{ role: "user", content: "hi" }];

    fireMessage({ type: "status", status: "thinking" });
    fireMessage({ type: "thinking_delta", content: "Let me " });
    fireMessage({ type: "thinking_delta", content: "think" });
    let thinkingRows = chatMessages.value.filter(
      (m) => m.role === "activity" && m.activityType === "thinking",
    );
    expect(thinkingRows.length).toBe(1);
    expect(thinkingRows[0].content).toBe("Let me think");
    expect(thinkingRows[0].id).toBeUndefined();

    // result claims the in-flight thinking row by stamping its id.
    fireMessage({
      type: "result",
      content: "OK",
      message_id: "a-claim",
      thinking_message_id: "t-claim",
    });
    thinkingRows = chatMessages.value.filter(
      (m) => m.role === "activity" && m.activityType === "thinking",
    );
    expect(thinkingRows.length).toBe(1);
    expect(thinkingRows[0].id).toBe("t-claim");

    // A late replay thinking_delta must NOT mutate the now-canonical row
    // — it lands as a fresh row instead, so the canonical one stays
    // intact for any history_backfill / REST sync that re-derives it.
    fireMessage({ type: "thinking_delta", content: "stale replay" });
    thinkingRows = chatMessages.value.filter(
      (m) => m.role === "activity" && m.activityType === "thinking",
    );
    expect(thinkingRows.length).toBe(2);
    expect(thinkingRows[0].content).toBe("Let me think");
    expect(thinkingRows[0].id).toBe("t-claim");
  });

  it("maps tool role from history to activity-tool with formatted content", async () => {
    const { fetchMessages } = await import("./useApi");
    const toolContent = {
      name: "Read",
      id: "t1",
      input: { file: "foo.txt", lines: 10 },
    };
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "1", role: "user", content: "hi", conversation_id: "ct", created_at: "" },
      { id: "2", role: "tool", content: toolContent, conversation_id: "ct", created_at: "" },
      { id: "3", role: "assistant", content: "ok", conversation_id: "ct", created_at: "" },
    ]);

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("ct");

    const tool = chatMessages.value.find((m) => m.activityType === "tool");
    expect(tool).toBeDefined();
    expect(tool!.role).toBe("activity");
    // Header is `[Name]`, then `key: value` lines indented two spaces — matches
    // the live tool_result rendering verbatim. String values are unquoted,
    // numbers and objects pass through JSON.stringify.
    expect(tool!.content.startsWith("[Read]\n")).toBe(true);
    expect(tool!.content).toContain("file: foo.txt");
    expect(tool!.content).toContain("lines: 10");
  });

  it("formats live tool_result object payloads without omitted text", () => {
    const { chatMessages } = useChat();
    fireMessage({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    fireMessage({
      type: "tool_result",
      content: {
        name: "Bash",
        input: { command: "x".repeat(50), output: "y".repeat(50) },
        content: "done",
      },
      message_id: "tool-1",
    });

    const tool = chatMessages.value.find((m) => m.activityType === "tool");
    expect(tool?.content).toContain("[Bash]");
    expect(tool?.content).toContain("command: " + "x".repeat(50));
    expect(tool?.content).toContain("output: " + "y".repeat(50));
    expect(tool?.content).not.toContain("[omitted]");
  });

  it("maps thinking role from history to activity", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "1", role: "user", content: "hi", conversation_id: "c1", created_at: "" },
      {
        id: "2",
        role: "thinking",
        content: "pondering...",
        conversation_id: "c1",
        created_at: "",
      },
      {
        id: "3",
        role: "assistant",
        content: "hello",
        conversation_id: "c1",
        created_at: "",
      },
    ]);

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    // Reset so switchToConversation doesn't short-circuit
    currentConversationId.value = "";
    await switchToConversation("c1");

    const thinking = chatMessages.value.find((m) => m.activityType === "thinking");
    expect(thinking).toBeDefined();
    expect(thinking!.role).toBe("activity");
    expect(thinking!.content).toBe("pondering...");
  });

  it("persists model + context usage to localStorage and restores on conversation switch", async () => {
    const { fetchMessages, fetchConversation } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);
    vi.mocked(fetchConversation).mockResolvedValue({
      id: "c1",
      user_id: "u1",
      title: "",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    const { switchToConversation, currentConversationId, currentModel, contextUsage } = useChat();

    // Switch to c1 and simulate the WS events that would normally arrive.
    currentConversationId.value = "";
    await switchToConversation("c1");
    fireMessage({
      type: "system_init",
      content: JSON.stringify({ model: "claude-opus-4-8", tools: [] }),
    });
    fireMessage({
      type: "context_usage",
      content: JSON.stringify({ used: 12345, total: 200000 }),
    });
    expect(currentModel.value).toBe("claude-opus-4-8");
    expect(contextUsage.value).toEqual({ used: 12345, total: 200000 });

    // Switch away then back: cached values should be restored immediately,
    // before any new WS event arrives.
    currentConversationId.value = "";
    await switchToConversation("c1");
    expect(currentModel.value).toBe("claude-opus-4-8");
    expect(contextUsage.value).toEqual({ used: 12345, total: 200000 });
  });

  it("does not replace the manually selected model when a turn starts", async () => {
    const { fetchMessages, fetchConversation } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValue([]);
    vi.mocked(fetchConversation).mockResolvedValue({
      id: "c-model-memory",
      user_id: "u1",
      title: "",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "codex",
      model: "gpt-5.5",
      created_at: "",
      updated_at: "",
    });

    const { switchToConversation, currentConversationId } = useChat();
    localStorage.setItem(
      RECENT_MODEL_STORAGE_KEY,
      JSON.stringify({ provider: "claude", model: "claude-sonnet-5" }),
    );
    currentConversationId.value = "";
    await switchToConversation("c-model-memory");
    fireMessage({
      type: "system_init",
      content: JSON.stringify({ model: "gpt-5.5", tools: [] }),
    });

    expect(localStorage.getItem(RECENT_MODEL_STORAGE_KEY)).toBe(
      JSON.stringify({ provider: "claude", model: "claude-sonnet-5" }),
    );
  });

  it("attaches per-turn usage to the assistant message it describes via metadata on the result event", async () => {
    // The `result` WS event ships the turn's usage payload on
    // `metadata.usage` (the same blob that's persisted onto
    // store.Message.Metadata). The chat composable parses it into a
    // short/detail chip stored directly on the assistant ChatMessage,
    // so the chip renders intrinsically under that bubble and the
    // previous turn's stats can't be re-anchored by render-time
    // bookkeeping.
    const { switchToConversation, currentConversationId, chatMessages } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c-usage-live");

    fireMessage({
      type: "result",
      content: "the answer",
      message_id: "asst-1",
      metadata: {
        model: "claude-opus-4-8",
        usage: {
          input_tokens: 1158,
          output_tokens: 1037,
          total_cost_usd: 0.3662,
          num_turns: 4,
        },
      },
    });

    const assistant = chatMessages.value.find((m) => m.role === "assistant");
    expect(assistant).toBeDefined();
    expect(assistant!.id).toBe("asst-1");
    expect(assistant!.usage).toBeDefined();
    expect(assistant!.usage!.short).toContain("I/O: 1.2K/");
    expect(assistant!.usage!.short).toContain("Cost: $0.3662");
    expect(assistant!.usage!.detail).toContain("Input tokens");
    expect(assistant!.usage!.input).toBe(1158);
    expect(assistant!.usage!.output).toBe(1037);

    // No standalone usage activity row should exist — usage lives on
    // the assistant message itself now.
    const orphanUsage = chatMessages.value.find(
      (m) => m.role === "activity" && (m as { activityType?: string }).activityType === "usage",
    );
    expect(orphanUsage).toBeUndefined();
  });

  it("restores per-turn usage from REST history so a refresh shows every assistant turn's chip", async () => {
    // History endpoint returns the persisted metadata column on each
    // assistant row. The chat composable maps it onto ChatMessage.usage
    // during restMessageToChatMessage so chip rendering survives a
    // page refresh without any localStorage shadow.
    const { fetchMessages, fetchConversation } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "u1", role: "user", content: "first", conversation_id: "c-hist", created_at: "" },
      {
        id: "a1",
        role: "assistant",
        content: "first reply",
        conversation_id: "c-hist",
        created_at: "",
        metadata: {
          model: "claude-opus-4-8",
          usage: { input_tokens: 100, output_tokens: 50, num_turns: 1 },
        },
      },
      { id: "u2", role: "user", content: "second", conversation_id: "c-hist", created_at: "" },
      {
        id: "a2",
        role: "assistant",
        content: "second reply",
        conversation_id: "c-hist",
        created_at: "",
        metadata: {
          model: "claude-opus-4-8",
          usage: { input_tokens: 200, output_tokens: 99, num_turns: 2 },
        },
      },
    ]);
    vi.mocked(fetchConversation).mockResolvedValue({
      id: "c-hist",
      user_id: "u1",
      title: "",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    const { switchToConversation, currentConversationId, chatMessages } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c-hist");

    const assistants = chatMessages.value.filter((m) => m.role === "assistant");
    expect(assistants).toHaveLength(2);
    expect(assistants[0].usage?.short).toContain("I/O: 100/");
    expect(assistants[1].usage?.short).toContain("I/O: 200/");
  });

  it("keeps the previous turn's usage chip anchored to its assistant message when the user sends a new prompt", async () => {
    // Reproduces the original regression: turn 1 completes with a
    // usage chip, the user sends turn 2, claude streams tool calls
    // before producing a result. The previous turn's chip must stay
    // pinned under its own assistant bubble — not float down to the
    // bottom of the visible chat past the new content.
    const { switchToConversation, currentConversationId, chatMessages, sendMessage } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c-anchor");

    // Turn 1.
    fireMessage({
      type: "result",
      content: "turn 1 answer",
      message_id: "asst-1",
      metadata: { usage: { input_tokens: 50, output_tokens: 20, num_turns: 1 } },
    });

    // Turn 2 begins — staging + dispatcher pipeline.
    sendMessage("follow up");
    fireMessage({ type: "input_ack", message_id: "u2" });
    fireMessage({ type: "prompt_started", message_id: "u2" });
    fireMessage({
      type: "system_init",
      content: JSON.stringify({ model: "claude-opus-4-8", tools: [] }),
    });
    fireMessage({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    // Find the indices of the first assistant turn's chip carrier and
    // the new user message. The usage chip must sit BEFORE the new
    // user message; nothing renders it "below" the in-flight content.
    const idxAssistant1 = chatMessages.value.findIndex(
      (m) => m.role === "assistant" && m.id === "asst-1",
    );
    const idxUser2 = chatMessages.value.findIndex((m) => m.role === "user" && m.id === "u2");
    expect(idxAssistant1).toBeGreaterThanOrEqual(0);
    expect(idxUser2).toBeGreaterThan(idxAssistant1);

    // Chip is attached to the assistant row, never floats to the tail.
    expect(chatMessages.value[idxAssistant1].usage?.short).toContain("I/O: 50/");
    const tail = chatMessages.value[chatMessages.value.length - 1];
    expect(tail.role).not.toBe("assistant");
    expect("usage" in tail).toBe(false);
  });

  it("does not clear messages when clicking the already-active session", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "1", role: "user", content: "hi", conversation_id: "c1", created_at: "" },
      {
        id: "2",
        role: "assistant",
        content: "hello",
        conversation_id: "c1",
        created_at: "",
      },
    ]);

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c1");
    // 2 persisted rows + 1 prepended workdir-hint activity row.
    expect(chatMessages.value.length).toBe(3);

    // Click the same session again — should be a no-op
    await switchToConversation("c1");
    expect(chatMessages.value.length).toBe(3);
    expect(currentConversationId.value).toBe("c1");
  });

  it("clearContext resets usage state and pushes a divider activity", async () => {
    const { clearConversationContext } = await import("./useApi");
    const { clearContext, contextUsage, chatMessages, currentConversationId } = useChat();

    currentConversationId.value = "c1";
    contextUsage.value = { used: 100, total: 200000 };

    const ok = await clearContext();
    expect(ok).toBe(true);
    expect(vi.mocked(clearConversationContext)).toHaveBeenCalledWith("c1");
    expect(contextUsage.value).toBeNull();

    const tail = chatMessages.value[chatMessages.value.length - 1];
    expect(tail.role).toBe("activity");
    expect(tail.activityType).toBe("info");
    expect(tail.content).toContain("context cleared");
  });

  it("clearContext divider survives a conversation switch+return (refresh simulation)", async () => {
    // Regression: refreshing the tab after clearContext used to drop the
    // divider AND make the inline "clear context" button reappear at the
    // tail, as if nothing had happened. The marker stored in convMeta now
    // re-anchors the divider on reload while the underlying messages stay
    // unchanged.
    const { fetchMessages } = await import("./useApi");
    const c1Rows = [
      { id: "m1", role: "user", content: "hi", conversation_id: "c1", created_at: "" },
      { id: "m2", role: "assistant", content: "hello", conversation_id: "c1", created_at: "" },
    ];
    vi.mocked(fetchMessages)
      .mockResolvedValueOnce(c1Rows)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce(c1Rows);

    const { switchToConversation, clearContext, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c1");

    const ok = await clearContext();
    expect(ok).toBe(true);

    // Simulate a refresh: bounce away and back. The same two persisted
    // rows come back from REST, and the divider should be restored from
    // convMeta because m2 still anchors the marker.
    await switchToConversation("c-other");
    await switchToConversation("c1");

    const tail = chatMessages.value[chatMessages.value.length - 1];
    expect(tail.role).toBe("activity");
    expect(tail.activityType).toBe("info");
    expect(tail.content).toContain("context cleared");
  });

  it("clearContext divider is dropped once a newer persisted message lands", async () => {
    // After another turn lands, the marker no longer points at the tail —
    // selectConversation should detect the mismatch, clean up the cached
    // marker, and let the inline trigger reappear.
    const { fetchMessages } = await import("./useApi");
    const c1RowsBefore = [
      { id: "m1", role: "user", content: "hi", conversation_id: "c1", created_at: "" },
      { id: "m2", role: "assistant", content: "hello", conversation_id: "c1", created_at: "" },
    ];
    const c1RowsAfter = [
      ...c1RowsBefore,
      { id: "m3", role: "user", content: "again", conversation_id: "c1", created_at: "" },
    ];
    vi.mocked(fetchMessages)
      .mockResolvedValueOnce(c1RowsBefore)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce(c1RowsAfter);

    const { switchToConversation, clearContext, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("c1");
    await clearContext();

    await switchToConversation("c-other");
    await switchToConversation("c1");

    const tail = chatMessages.value[chatMessages.value.length - 1];
    expect(tail.content).not.toContain("context cleared");
  });

  it("clearContext is a no-op without an active conversation", async () => {
    const { clearConversationContext } = await import("./useApi");
    vi.mocked(clearConversationContext).mockClear();
    const { clearContext, currentConversationId } = useChat();
    currentConversationId.value = "";

    const ok = await clearContext();
    expect(ok).toBe(false);
    expect(vi.mocked(clearConversationContext)).not.toHaveBeenCalled();
  });

  it("clearContext surfaces server failures via the activity log without clearing usage", async () => {
    const { clearConversationContext } = await import("./useApi");
    vi.mocked(clearConversationContext).mockRejectedValueOnce(new Error("boom"));
    const { clearContext, contextUsage, chatMessages, currentConversationId } = useChat();

    currentConversationId.value = "c1";
    contextUsage.value = { used: 5, total: 200000 };

    const ok = await clearContext();
    expect(ok).toBe(false);
    // Usage stays put on failure — bar shouldn't lie about a reset that
    // didn't happen on the server.
    expect(contextUsage.value).toEqual({ used: 5, total: 200000 });
    const tail = chatMessages.value[chatMessages.value.length - 1];
    expect(tail.content).toContain("Failed to clear context");
    expect(tail.content).toContain("boom");
  });

  // history_backfill is the WS frame the backend sends right after
  // init_ok on a reconnect (when the client provided a last_message_id
  // cursor). It carries any messages persisted while the tab was
  // offline. Under the id-based architecture it just appends the rows
  // whose ids aren't already in chatMessages — no pop-tail churn.
  it("history_backfill appends missed persisted messages without disturbing the in-flight stream", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "m1", role: "user", content: "ask", conversation_id: "cBF", created_at: "t0" },
    ]);

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cBF");

    // A live stream activity present at the moment of disconnect.
    fireMessage({ type: "delta", content: "hel" });
    expect(
      chatMessages.value.some((m) => m.role === "activity" && m.activityType === "stream"),
    ).toBe(true);

    fireMessage({
      type: "history_backfill",
      conversation_id: "cBF",
      messages: [
        {
          id: "m2",
          role: "assistant",
          content: "hello world",
          conversation_id: "cBF",
          created_at: "t1",
        },
      ],
    });

    // Backfill appends m2; the original m1 (from REST) is untouched;
    // the in-flight stream activity is left alone (id-dedup never
    // touches activity rows). When the live result arrives later it
    // will sweep the stream — that's the result handler's job, not
    // backfill's.
    const ids = chatMessages.value
      .filter((m) => m.role === "user" || m.role === "assistant")
      .map((m) => m.id);
    expect(ids).toEqual(["m1", "m2"]);
  });

  it("history_backfill with empty messages does not clobber an in-flight stream", () => {
    const { chatMessages } = useChat();
    chatMessages.value = [
      { role: "user", content: "ask", id: "u1" },
      { role: "activity", activityType: "stream", content: "hel" },
    ];

    fireMessage({ type: "history_backfill", conversation_id: "c1", messages: [] });

    expect(chatMessages.value.length).toBe(2);
    expect(chatMessages.value[1].activityType).toBe("stream");
    expect(chatMessages.value[1].content).toBe("hel");
  });

  it("history_backfill dedups locally-pushed user message via input_ack id", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([]);

    const { switchToConversation, chatMessages, sendMessage, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cBF2");

    sendMessage("hi from me");
    // Server stamps the canonical id on the staging entry.
    fireMessage({ type: "input_ack", message_id: "p1" });
    // Worker claims the prompt → moves into chatMessages.
    fireMessage({ type: "prompt_started", message_id: "p1" });

    fireMessage({
      type: "history_backfill",
      conversation_id: "cBF2",
      messages: [
        {
          id: "p1",
          role: "user",
          content: "hi from me",
          conversation_id: "cBF2",
          created_at: "t0",
        },
        {
          id: "p2",
          role: "assistant",
          content: "and a reply",
          conversation_id: "cBF2",
          created_at: "t1",
        },
      ],
    });

    // p1 is already known — backfill skips it. p2 is new and gets
    // appended. End state: exactly one user row + one assistant row.
    const userEntries = chatMessages.value.filter((m) => m.role === "user");
    expect(userEntries.length).toBe(1);
    expect(userEntries[0].id).toBe("p1");
    const assistants = chatMessages.value.filter((m) => m.role === "assistant");
    expect(assistants.length).toBe(1);
    expect(assistants[0].id).toBe("p2");
  });

  // The empty-cursor recovery path on the server returns the FULL
  // conversation. Even when REST already loaded the same rows, dedup
  // by id ensures no double-render — the cornerstone of the new
  // architecture's "no missing, no duplicate" contract.
  it("history_backfill dedups by id against REST-loaded entries", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "m1", role: "user", content: "ask", conversation_id: "cDD", created_at: "t0" },
      {
        id: "m2",
        role: "assistant",
        content: "old reply",
        conversation_id: "cDD",
        created_at: "t1",
      },
    ]);

    const { switchToConversation, chatMessages, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cDD");

    fireMessage({
      type: "history_backfill",
      conversation_id: "cDD",
      messages: [
        { id: "m1", role: "user", content: "ask", conversation_id: "cDD", created_at: "t0" },
        {
          id: "m2",
          role: "assistant",
          content: "old reply",
          conversation_id: "cDD",
          created_at: "t1",
        },
        {
          id: "m3",
          role: "assistant",
          content: "new reply",
          conversation_id: "cDD",
          created_at: "t2",
        },
      ],
    });

    const ids = chatMessages.value.map((m) => m.id).filter(Boolean);
    expect(ids).toEqual(["m1", "m2", "m3"]);
    expect(chatMessages.value.filter((m) => m.id === "m2").length).toBe(1);
  });

  // First-connect init must carry the cursor we just anchored from REST.
  // Without it the empty-cursor recovery path can't tell whether the
  // server should backfill or not for an established conversation, and
  // a brand-new conversation that goes offline before REST reloads
  // would silently lose its assistant reply.
  it("first connect sends init with last_message_id from REST snapshot", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "m1", role: "user", content: "hi", conversation_id: "cIC", created_at: "t0" },
      {
        id: "m2",
        role: "assistant",
        content: "hello",
        conversation_id: "cIC",
        created_at: "t1",
      },
    ]);

    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const wsStore = (globalThis as any).__useChatTestWs as {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      handler: ((msg: any) => void) | null;
    };
    const ws = useChat();
    // Reach the underlying mock instance through the same module factory
    // that useChat captured. The `send` and `connect` mocks live there.
    const wsModule = await import("./useWebSocket");
    const wsInstance = wsModule.useWebSocket();
    vi.mocked(wsInstance.send).mockClear();
    vi.mocked(wsInstance.connect).mockClear();

    ws.currentConversationId.value = "";
    await ws.switchToConversation("cIC");

    // useChat registers a single onReconnect handler at module load
    // that fires init from current state on every successful WS open.
    // Drain that handler manually so the init send actually fires,
    // then assert the payload.
    const onReconnectCalls = vi.mocked(wsInstance.onReconnect).mock.calls;
    expect(onReconnectCalls.length).toBeGreaterThan(0);
    const initCallback = onReconnectCalls[onReconnectCalls.length - 1][0];
    initCallback?.();

    const sendCalls = vi.mocked(wsInstance.send).mock.calls;
    expect(sendCalls.length).toBeGreaterThan(0);
    const initPayload = sendCalls[sendCalls.length - 1][0];
    expect(initPayload.type).toBe("init");
    expect(initPayload.conversation_id).toBe("cIC");
    expect(initPayload.last_message_id).toBe("m2");
    void wsStore;
  });

  // History pagination: switchToConversation only pulls the latest page so
  // long conversations open without loading megabytes of stream-json.
  // Older pages come in via loadMoreHistory when the scroll listener fires.
  it("switchToConversation requests only the latest page", async () => {
    const { fetchMessages } = await import("./useApi");
    vi.mocked(fetchMessages).mockResolvedValueOnce([]);

    const { switchToConversation, currentConversationId } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cPaginate1");

    // First arg = conversation id, second arg = options object with the
    // empty before_id sentinel that opts the request into reverse paging.
    const calls = vi.mocked(fetchMessages).mock.calls;
    const last = calls[calls.length - 1];
    expect(last[0]).toBe("cPaginate1");
    expect(last[1]).toMatchObject({ beforeId: "", limit: expect.any(Number) });
  });

  it("loadMoreHistory prepends the older page and advances the cursor", async () => {
    const { fetchMessages } = await import("./useApi");

    // Build a saturated initial page so hasMoreHistory flips on (the
    // composable infers "more available" from a full response).
    const PAGE = 50;
    const initial = Array.from({ length: PAGE }, (_, i) => ({
      id: `m${100 + i}`,
      role: i % 2 === 0 ? "user" : "assistant",
      content: `latest-${i}`,
      conversation_id: "cPg",
      created_at: "",
    }));
    vi.mocked(fetchMessages).mockResolvedValueOnce(initial);

    const {
      switchToConversation,
      loadMoreHistory,
      hasMoreHistory,
      chatMessages,
      currentConversationId,
    } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cPg");

    // PAGE persisted rows + 1 prepended workdir-hint activity row.
    expect(chatMessages.value.length).toBe(PAGE + 1);
    expect(hasMoreHistory.value).toBe(true);

    // Older page returned for the next call uses oldestLoadedMessageId
    // (= initial[0].id = "m100") as the cursor.
    const older = [
      { id: "m50", role: "user", content: "older-50", conversation_id: "cPg", created_at: "" },
      { id: "m51", role: "assistant", content: "older-51", conversation_id: "cPg", created_at: "" },
    ];
    vi.mocked(fetchMessages).mockResolvedValueOnce(older);

    const added = await loadMoreHistory();
    expect(added).toBe(2);
    expect(chatMessages.value.length).toBe(PAGE + 1 + 2);
    // The synthetic workdir header stays pinned at the head; persisted older
    // rows land immediately after it and ahead of the original latest page.
    expect(chatMessages.value[0].content).toContain("Your current working directory is ");
    expect(chatMessages.value[1].content).toBe("older-50");
    expect(chatMessages.value[2].content).toBe("older-51");
    // Less-than-a-full-page response means we've hit the head; no more
    // requests should fire.
    expect(hasMoreHistory.value).toBe(false);

    // Verify the second fetch used the prior oldest id as the before_id.
    const calls = vi.mocked(fetchMessages).mock.calls;
    const second = calls[calls.length - 1];
    expect(second[1]).toMatchObject({ beforeId: "m100", limit: PAGE });
  });

  it("dedups older history against backfill received while the page is loading", async () => {
    const { fetchMessages } = await import("./useApi");
    const { switchToConversation, loadMoreHistory, chatMessages, pendingPrompts, hasMoreHistory } =
      useChat();
    vi.mocked(fetchMessages).mockResolvedValueOnce(
      Array.from({ length: 50 }, (_, i) => ({
        id: `latest-${i}`,
        role: "assistant",
        content: `reply ${i}`,
        conversation_id: "overlap",
        created_at: "",
      })),
    );
    await switchToConversation("overlap");

    const older = {
      id: "older-prompt",
      role: "user",
      content: "old prompt",
      conversation_id: "overlap",
      created_at: "",
    };
    const queued = { ...older, id: "queued-prompt", queue_status: "pending" };
    let resolvePage!: (rows: (typeof older)[]) => void;
    vi.mocked(fetchMessages).mockImplementationOnce(
      () => new Promise((resolve) => (resolvePage = resolve)),
    );
    const loading = loadMoreHistory();
    fireMessage({
      type: "history_backfill",
      conversation_id: "overlap",
      messages: [older, queued],
    });
    const unseen = { ...older, id: "unseen-prompt", content: "earlier prompt" };
    resolvePage([unseen, older, queued, unseen]);

    expect(await loading).toBe(1);
    expect(chatMessages.value.filter((m) => m.id === older.id)).toHaveLength(1);
    expect(chatMessages.value.filter((m) => m.id === unseen.id)).toHaveLength(1);
    expect(chatMessages.value.some((m) => m.id === queued.id)).toBe(false);
    expect(pendingPrompts.value.filter((p) => p.id === queued.id)).toHaveLength(1);
    expect(hasMoreHistory.value).toBe(false);
    vi.mocked(fetchMessages).mockResolvedValueOnce([]);
    await switchToConversation("empty-after-overlap");
    expect(chatMessages.value.some((m) => m.role === "user")).toBe(false);
  });

  it("keeps conversation metadata outside a tool run when older history is prepended", async () => {
    const { fetchMessages } = await import("./useApi");

    const PAGE = 50;
    const initial = Array.from({ length: PAGE }, (_, i) => ({
      id: `tool-${100 + i}`,
      role: "tool",
      content: JSON.stringify({ name: "Bash", input: { command: `latest-${i}` } }),
      conversation_id: "cToolPages",
      created_at: "",
    }));
    vi.mocked(fetchMessages).mockResolvedValueOnce(initial);

    const { switchToConversation, loadMoreHistory, chatMessages, currentConversationId } =
      useChat();
    currentConversationId.value = "";
    await switchToConversation("cToolPages");

    // Mirror the model banner that normally sits directly under the workdir
    // hint after system_init/convMeta restoration.
    chatMessages.value.splice(1, 0, {
      role: "activity",
      activityType: "model",
      content: "Model: test",
    });

    vi.mocked(fetchMessages).mockResolvedValueOnce([
      {
        id: "tool-99",
        role: "tool",
        content: JSON.stringify({ name: "Bash", input: { command: "older" } }),
        conversation_id: "cToolPages",
        created_at: "",
      },
    ]);

    await loadMoreHistory();

    expect(chatMessages.value.slice(0, 2).map((message) => message.activityType)).toEqual([
      "info",
      "model",
    ]);
    expect(chatMessages.value.slice(2).every((message) => message.activityType === "tool")).toBe(
      true,
    );
    expect(chatMessages.value[2].id).toBe("tool-99");
  });

  it("loadMoreHistory is a no-op when hasMoreHistory is false", async () => {
    const { fetchMessages } = await import("./useApi");
    // Initial page short of the page size → no more history flagged.
    vi.mocked(fetchMessages).mockResolvedValueOnce([
      { id: "only1", role: "user", content: "hi", conversation_id: "cShort", created_at: "" },
    ]);

    const { switchToConversation, loadMoreHistory, hasMoreHistory, currentConversationId } =
      useChat();
    currentConversationId.value = "";
    await switchToConversation("cShort");
    expect(hasMoreHistory.value).toBe(false);

    // Reset the mock to detect any unexpected fetch.
    vi.mocked(fetchMessages).mockClear();
    const added = await loadMoreHistory();
    expect(added).toBe(0);
    expect(vi.mocked(fetchMessages)).not.toHaveBeenCalled();
  });

  it("loadMoreHistory ignores stale page if the conversation switched mid-flight", async () => {
    const { fetchMessages } = await import("./useApi");

    const PAGE = 50;
    const initial = Array.from({ length: PAGE }, (_, i) => ({
      id: `s${i}`,
      role: i % 2 === 0 ? "user" : "assistant",
      content: `s-${i}`,
      conversation_id: "cStale",
      created_at: "",
    }));
    vi.mocked(fetchMessages).mockResolvedValueOnce(initial);

    const {
      switchToConversation,
      loadMoreHistory,
      hasMoreHistory,
      chatMessages,
      currentConversationId,
    } = useChat();
    currentConversationId.value = "";
    await switchToConversation("cStale");
    expect(hasMoreHistory.value).toBe(true);
    const initialLen = chatMessages.value.length;

    // Resolve the older page only after the user has swapped conversations.
    let resolveOlder: (msgs: unknown[]) => void = () => {};
    vi.mocked(fetchMessages).mockImplementationOnce(
      () => new Promise((res) => (resolveOlder = res as typeof resolveOlder)),
    );
    // Initial page for the second conversation arrives synchronously.
    vi.mocked(fetchMessages).mockResolvedValueOnce([]);

    const inFlight = loadMoreHistory();
    await switchToConversation("cOther");
    resolveOlder([
      { id: "stale-1", role: "user", content: "stale", conversation_id: "cStale", created_at: "" },
    ]);
    const added = await inFlight;
    expect(added).toBe(0);
    // chatMessages now belongs to cOther (empty); the stale older page must
    // not have leaked into it.
    expect(chatMessages.value.length).toBeLessThan(initialLen);
    expect(chatMessages.value.some((m) => m.id === "stale-1")).toBe(false);
  });

  // The original bug: the user reported seeing the "Model:..." line, the
  // assistant text, and "[Agent] calling..." each appearing twice in the
  // chat. Root cause was the parent and the sub-agent (Task / Agent) sharing
  // the same render path — every sub-agent event looked like a duplicate of
  // the parent's. With the subagent flag wired through, sub-agent events
  // land on a separate track and never collide with the parent's.
  describe("subagent flag", () => {
    beforeEach(() => {
      // Several module-level buffers (subagentStreamingText, thinkingBuffer,
      // …) are not exposed by useChat() for direct reset. Firing a
      // status=ready event flushes them through the same code path the
      // server uses between turns, isolating each test from the previous
      // one's leftover stream state.
      fireMessage({ type: "status", status: "ready" });
    });

    it("renders sub-agent system_init in its own track without overwriting currentModel", () => {
      const { chatMessages, currentModel } = useChat();
      fireMessage({
        type: "system_init",
        content: JSON.stringify({ model: "claude-opus-4-8[1m]", tools: ["Read", "Bash"] }),
      });
      expect(currentModel.value).toBe("claude-opus-4-8[1m]");
      expect(chatMessages.value).toHaveLength(1);
      expect(chatMessages.value[0].subagent).toBeFalsy();

      fireMessage({
        type: "system_init",
        subagent: true,
        content: JSON.stringify({ model: "claude-haiku-4-5", tools: ["Read"] }),
      });
      // Parent's model survives — header doesn't lurch when a worker spawns.
      expect(currentModel.value).toBe("claude-opus-4-8[1m]");
      expect(chatMessages.value).toHaveLength(2);
      expect(chatMessages.value[1].subagent).toBe(true);
      expect(chatMessages.value[1].content).toContain("claude-haiku-4-5");
    });

    it("isolates parent and sub-agent stream tracks so a sub-agent's text never overwrites the parent's", () => {
      const { chatMessages, streamingText } = useChat();
      // Parent starts streaming.
      fireMessage({ type: "delta", content: "Parent says: " });
      // Sub-agent kicks off mid-flight.
      fireMessage({ type: "delta", subagent: true, content: "Sub: " });
      fireMessage({ type: "delta", subagent: true, content: "exploring" });
      // Parent resumes after the worker.
      fireMessage({ type: "delta", content: "done." });

      expect(streamingText.value).toBe("Parent says: done.");
      const streams = chatMessages.value.filter(
        (m) => m.role === "activity" && m.activityType === "stream",
      );
      // Two distinct stream activities: one parent (last event resumed it),
      // one sub-agent. The sub-agent's text must not leak into the parent
      // streamingText — which would otherwise drive the persisted assistant.
      expect(streams).toHaveLength(2);
      const parentStream = streams.find((m) => !m.subagent);
      const subStream = streams.find((m) => m.subagent);
      expect(parentStream?.content).toBe("Parent says: done.");
      expect(subStream?.content).toBe("Sub: exploring");
    });

    it("sub-agent result clears its stream activity but does not push a parent assistant message", () => {
      const { chatMessages } = useChat();
      fireMessage({ type: "delta", subagent: true, content: "sub work" });
      expect(
        chatMessages.value.some(
          (m) => m.activityType === "stream" && m.subagent && m.content === "sub work",
        ),
      ).toBe(true);

      fireMessage({ type: "result", subagent: true, content: "sub work" });
      // Sub-agent stream activity gone, no assistant message added.
      expect(chatMessages.value.filter((m) => m.activityType === "stream")).toHaveLength(0);
      expect(chatMessages.value.filter((m) => m.role === "assistant")).toHaveLength(0);
    });

    it("sub-agent and parent tool_results don't cross-mutate each other's [Name] activities", () => {
      const { chatMessages } = useChat();
      // Parent calls Read.
      fireMessage({ type: "tool_use_start", content: JSON.stringify({ name: "Read" }) });
      // Sub-agent (worker) also calls Read.
      fireMessage({
        type: "tool_use_start",
        subagent: true,
        content: JSON.stringify({ name: "Read" }),
      });
      // Sub-agent's tool_result fires first.
      fireMessage({
        type: "tool_result",
        subagent: true,
        content: JSON.stringify({ name: "Read", input: { path: "/sub" } }),
      });
      // Parent's tool_result fires after.
      fireMessage({
        type: "tool_result",
        content: JSON.stringify({ name: "Read", input: { path: "/parent" } }),
      });

      const tools = chatMessages.value.filter(
        (m) => m.role === "activity" && m.activityType === "tool",
      );
      expect(tools).toHaveLength(2);
      const parentTool = tools.find((m) => !m.subagent);
      const subTool = tools.find((m) => m.subagent);
      expect(parentTool?.content).toContain("/parent");
      expect(parentTool?.content).not.toContain("/sub");
      expect(subTool?.content).toContain("/sub");
      expect(subTool?.content).not.toContain("/parent");
    });

    it("ignores sub-agent context_usage so the parent's bar isn't clobbered by a worker", () => {
      const { contextUsage } = useChat();
      fireMessage({
        type: "context_usage",
        content: JSON.stringify({ used: 1000, total: 200000 }),
      });
      expect(contextUsage.value).toEqual({ used: 1000, total: 200000 });
      fireMessage({
        type: "context_usage",
        subagent: true,
        content: JSON.stringify({ used: 50000, total: 200000 }),
      });
      // Unchanged — sub-agent frame ignored.
      expect(contextUsage.value).toEqual({ used: 1000, total: 200000 });
    });
  });

  describe("rate_limit WS events", () => {
    it("records a five_hour frame and exposes it via rateLimits", () => {
      const { rateLimits } = useChat();
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({
          type: "five_hour",
          status: "allowed",
          resets_at: 1778940000,
        }),
      });
      expect(rateLimits.value.five_hour).toEqual({
        type: "five_hour",
        status: "allowed",
        resets_at: 1778940000,
      });
    });

    it("keeps per-window frames separate when both windows fire", () => {
      const { rateLimits } = useChat();
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({ type: "five_hour", status: "warning", resets_at: 1000 }),
      });
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({ type: "seven_day", status: "allowed", resets_at: 2000 }),
      });
      expect(rateLimits.value.five_hour?.resets_at).toBe(1000);
      expect(rateLimits.value.seven_day?.resets_at).toBe(2000);
    });

    it("replaces the prior frame for the same window (status transition)", () => {
      const { rateLimits } = useChat();
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({ type: "five_hour", status: "allowed", resets_at: 1000 }),
      });
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({ type: "five_hour", status: "warning", resets_at: 1000 }),
      });
      expect(rateLimits.value.five_hour?.status).toBe("warning");
    });

    it("ignores frames with an unknown window type", () => {
      const { rateLimits } = useChat();
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({ type: "bogus", status: "allowed", resets_at: 1 }),
      });
      expect(rateLimits.value).toEqual({});
    });

    it("ignores malformed JSON without throwing", () => {
      const { rateLimits } = useChat();
      fireMessage({ type: "rate_limit", content: "not-json" });
      expect(rateLimits.value).toEqual({});
    });

    it("clears the rate-limit map on switchToConversation so a stale frame doesn't carry over", async () => {
      const { fetchMessages, fetchConversation } = await import("./useApi");
      vi.mocked(fetchMessages).mockResolvedValue([]);
      vi.mocked(fetchConversation).mockResolvedValue({
        id: "c-rl-switch",
        user_id: "u1",
        title: "",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "codex",
        model: "gpt-5.5",
        created_at: "",
        updated_at: "",
      });

      const { rateLimits, switchToConversation, currentConversationId } = useChat();
      fireMessage({
        type: "rate_limit",
        content: JSON.stringify({ type: "five_hour", status: "allowed", resets_at: 1778940000 }),
      });
      expect(rateLimits.value.five_hour?.resets_at).toBe(1778940000);

      currentConversationId.value = "";
      await switchToConversation("c-rl-switch");
      expect(rateLimits.value).toEqual({});
    });
  });
});
