// The in-turn stream family: delta / thinking_delta / tool_* / result, plus
// the two JSON side-channels (context_usage, rate_limit).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { watch } from "vue";

vi.mock("../useBrowserNotifications", () => ({ notifyBrowserTask: vi.fn() }));

import { RECENT_MODEL_STORAGE_KEY } from "@/lib/recentModel";

import { resetChatStores } from "@/stores";
import {
  conversationProvider,
  currentModel,
  modelInfoLine,
} from "@/stores/activeConversationStore";
import { contextUsage, rateLimits } from "@/stores/chatContextStore";
import {
  chatMessages,
  knownMessageIds,
  rememberMessageId,
  resultVersion,
  type ChatMessage,
} from "@/stores/chatMessageStore";
import { isThinking, streamBuffers, streamingText } from "@/stores/chatStreamStore";

import type { WSMessage } from "@/lib/wsProtocol";
import {
  createMessageDispatcher,
  flushStreamWrites,
  resetTurnStreamBuffers,
} from "./messageDispatcher";

let dispatch: (msg: WSMessage) => void;

function activities(): ChatMessage[] {
  return chatMessages.value.filter((m) => m.role === "activity");
}

function streamRows(subagent = false): ChatMessage[] {
  return activities().filter((m) => m.activityType === "stream" && !!m.subagent === subagent);
}

let consoleError: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  resetChatStores();
  localStorage.clear();
  const raw = createMessageDispatcher();
  // Stream-row writes are coalesced per animation frame (covered in the
  // "stream write coalescing" block); the rest of this file asserts on the row
  // as a painted frame would show it.
  dispatch = (msg) => {
    raw(msg);
    flushStreamWrites();
  };
  consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  consoleError.mockRestore();
});

describe("delta", () => {
  it("accumulates chunks into a single stream activity", () => {
    dispatch({ type: "delta", content: "Hel" });
    dispatch({ type: "delta", content: "lo" });

    expect(streamingText.value).toBe("Hello");
    expect(streamRows()).toHaveLength(1);
    expect(streamRows()[0].content).toBe("Hello");
  });

  it("ignores an empty chunk", () => {
    dispatch({ type: "delta" });

    expect(streamingText.value).toBe("");
    expect(chatMessages.value).toHaveLength(0);
  });

  it("replaces the buffer when a replayed chunk extends what we already have", () => {
    dispatch({ type: "delta", content: "Hello" });

    dispatch({ type: "delta", content: "Hello world", replay: true });

    expect(streamingText.value).toBe("Hello world");
  });

  it("drops a replayed chunk the buffer already contains", () => {
    dispatch({ type: "delta", content: "Hello world" });

    dispatch({ type: "delta", content: "Hello", replay: true });

    expect(streamingText.value).toBe("Hello world");
  });

  it("appends a replayed chunk that overlaps nothing", () => {
    dispatch({ type: "delta", content: "abc" });

    dispatch({ type: "delta", content: "xyz", replay: true });

    expect(streamingText.value).toBe("abcxyz");
  });

  // Regression: replay dedup used to be `buffer.includes(chunk)`, which threw
  // away any chunk whose text had appeared anywhere earlier in the buffer —
  // every repeated paragraph break, bracket pair or repeated word vanished
  // from a replayed answer.
  it("keeps a repeated short chunk while replaying", () => {
    dispatch({ type: "delta", content: "first", replay: true });
    dispatch({ type: "delta", content: "\n\n", replay: true });
    dispatch({ type: "delta", content: "second", replay: true });
    dispatch({ type: "delta", content: "\n\n", replay: true });

    expect(streamingText.value).toBe("first\n\nsecond\n\n");
  });

  it("does not duplicate a mid-stream chunk when a full turn is replayed", () => {
    dispatch({ type: "delta", content: "abc" });
    dispatch({ type: "delta", content: "def" });
    dispatch({ type: "delta", content: "ghi" });

    // Reconnect: the broadcaster resends the turn from its first chunk.
    dispatch({ type: "delta", content: "abc", replay: true });
    dispatch({ type: "delta", content: "def", replay: true });
    dispatch({ type: "delta", content: "ghi", replay: true });
    dispatch({ type: "delta", content: "jkl", replay: true });

    expect(streamingText.value).toBe("abcdefghijkl");
  });

  it("keeps the sub-agent stream on its own track", () => {
    dispatch({ type: "delta", content: "parent" });
    dispatch({ type: "delta", content: "worker", subagent: true });
    dispatch({ type: "delta", content: " more" });

    expect(streamingText.value).toBe("parent more");
    expect(streamBuffers.subagentStreamingText).toBe("worker");
    expect(streamRows()[0].content).toBe("parent more");
    expect(streamRows(true)[0].content).toBe("worker");
  });
});

describe("result", () => {
  it("replaces the in-flight stream with the persisted assistant row", () => {
    isThinking.value = true;
    dispatch({ type: "delta", content: "partial" });

    dispatch({ type: "result", content: "final answer", message_id: "m1" });

    expect(streamRows()).toHaveLength(0);
    expect(chatMessages.value).toHaveLength(1);
    expect(chatMessages.value[0]).toMatchObject({
      role: "assistant",
      content: "final answer",
      id: "m1",
    });
    expect(knownMessageIds.has("m1")).toBe(true);
    expect(resultVersion.value).toBe(1);
    // The row is final, the turn is not: only `status: ready` ends that.
    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("");
  });

  it("attaches the per-turn usage chip carried on metadata", () => {
    dispatch({
      type: "result",
      content: "answer",
      message_id: "m1",
      metadata: { usage: { input_tokens: 100, output_tokens: 20 } },
    });

    expect(chatMessages.value[0].usage).toMatchObject({ input: 100, output: 20 });
  });

  it("marks a result the agent produced without being prompted", () => {
    dispatch({
      type: "result",
      content: "the codex leg finished",
      message_id: "m1",
      metadata: { origin: "background_wakeup" },
    });

    expect(chatMessages.value[0].wakeup).toBe(true);
  });

  it("leaves an ordinary prompted result unmarked", () => {
    dispatch({ type: "result", content: "answer", message_id: "m1" });

    expect(chatMessages.value[0].wakeup).toBeUndefined();
  });

  it("claims the trailing thinking row so a later backfill won't duplicate it", () => {
    dispatch({ type: "thinking_delta", content: "reasoning" });

    dispatch({
      type: "result",
      content: "answer",
      message_id: "m1",
      thinking_message_id: "t1",
    });

    const thinking = activities().find((m) => m.activityType === "thinking");
    expect(thinking?.id).toBe("t1");
    expect(knownMessageIds.has("t1")).toBe(true);
  });

  it("only sweeps the stream when the persisted row is already rendered", () => {
    rememberMessageId("m1");
    dispatch({ type: "delta", content: "partial" });

    dispatch({ type: "result", content: "final answer", message_id: "m1" });

    expect(chatMessages.value).toHaveLength(0);
    expect(resultVersion.value).toBe(1);
  });

  // Regression: the already-known branch used to return before the reset at
  // the bottom of handleResult, so a result echoed after a REST cursor sync
  // left the stream buffers populated and the fallback unarmed.
  it("still sweeps the stream on an already-known result", () => {
    rememberMessageId("m1");
    isThinking.value = true;
    streamingText.value = "partial";
    streamBuffers.thinkingBuffer = "thought";

    dispatch({ type: "result", content: "final answer", message_id: "m1" });

    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("");
    expect(streamBuffers.thinkingBuffer).toBe("");
  });

  it("still sweeps the stream for a silent result with no text", () => {
    isThinking.value = true;
    streamingText.value = "leftover";

    dispatch({ type: "result" });

    expect(isThinking.value).toBe(true);
    expect(streamingText.value).toBe("");
    expect(chatMessages.value).toHaveLength(0);
    expect(resultVersion.value).toBe(0);
  });

  // The bug this file's `result` contract was rewritten for. The backend runs
  // the web transcript in AgentStreamPerResult mode, so a codex turn emits one
  // result frame per agent_message and keeps working afterwards. Treating the
  // first as end-of-turn unlocked the composer and told the user the agent was
  // done while its CLI process was still running tool calls.
  it("keeps the turn running when a mid-turn result arrives", () => {
    isThinking.value = true;

    dispatch({ type: "result", content: "first of several", message_id: "m1" });

    expect(isThinking.value).toBe(true);
  });

  it("ends the turn only once the server says ready", () => {
    isThinking.value = true;
    dispatch({ type: "result", content: "answer", message_id: "m1" });
    expect(isThinking.value).toBe(true);

    dispatch({ type: "status", status: "ready" });

    expect(isThinking.value).toBe(false);
  });

  describe("lost-ready fallback", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    // The safety net the old unconditional clear provided: a `ready` that never
    // arrives (socket dropped between the reply and the status, on a tab that
    // never reconnects) must not strand the composer forever.
    it("releases the turn when no status frame follows the result", () => {
      isThinking.value = true;
      dispatch({ type: "result", content: "answer", message_id: "m1" });

      vi.advanceTimersByTime(30_000);

      expect(isThinking.value).toBe(false);
    });

    it("does not release the turn while results keep arriving", () => {
      isThinking.value = true;
      dispatch({ type: "result", content: "first", message_id: "m1" });

      // Each result re-arms the window, so a codex turn that keeps talking
      // never trips the fallback no matter how long it runs.
      for (let i = 0; i < 5; i++) {
        vi.advanceTimersByTime(25_000);
        dispatch({ type: "result", content: `chunk ${i}`, message_id: `m${i + 2}` });
        expect(isThinking.value).toBe(true);
      }
    });

    it("disarms once ready lands so it cannot clear the next turn", () => {
      isThinking.value = true;
      dispatch({ type: "result", content: "answer", message_id: "m1" });
      dispatch({ type: "status", status: "ready" });

      // A new turn starts well inside the old fallback's window.
      dispatch({ type: "status", status: "thinking" });
      vi.advanceTimersByTime(30_000);

      expect(isThinking.value).toBe(true);
    });
  });

  it("finalizes a sub-agent turn without touching the parent's", () => {
    isThinking.value = true;
    dispatch({ type: "delta", content: "parent" });
    dispatch({ type: "delta", content: "worker", subagent: true });
    dispatch({ type: "thinking_delta", content: "worker thought", subagent: true });

    dispatch({ type: "result", content: "worker done", subagent: true });

    expect(streamRows(true)).toHaveLength(0);
    expect(streamRows()).toHaveLength(1);
    expect(streamBuffers.subagentStreamingText).toBe("");
    expect(streamBuffers.subagentThinkingBuffer).toBe("");
    expect(isThinking.value).toBe(true);
  });
});

describe("thinking_delta", () => {
  it("opens one thinking activity and mutates it in place", () => {
    dispatch({ type: "thinking_delta", content: "step one" });
    dispatch({ type: "thinking_delta", content: "step one, step two" });

    const rows = activities().filter((m) => m.activityType === "thinking");
    expect(rows).toHaveLength(1);
    expect(rows[0].content).toBe("step one, step two");
    expect(streamBuffers.thinkingBuffer).toBe("step one, step two");
  });

  it("concatenates incremental chunks that are not cumulative snapshots", () => {
    dispatch({ type: "thinking_delta", content: "abc" });
    dispatch({ type: "thinking_delta", content: "def" });

    expect(streamBuffers.thinkingBuffer).toBe("abcdef");
  });

  it("merges a replayed chunk the buffer already contains", () => {
    dispatch({ type: "thinking_delta", content: "abcdef" });

    dispatch({ type: "thinking_delta", content: "abc", replay: true });

    expect(streamBuffers.thinkingBuffer).toBe("abcdef");
  });

  it("keeps mutating the same row across an interleaved tool call", () => {
    dispatch({ type: "thinking_delta", content: "reasoning" });
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    dispatch({ type: "thinking_delta", content: "reasoning more" });

    expect(activities().filter((m) => m.activityType === "thinking")).toHaveLength(1);
  });

  it("opens a fresh row once a user turn has landed in between", () => {
    dispatch({ type: "thinking_delta", content: "old turn" });
    chatMessages.value.push({ role: "user", content: "next question" });
    // The turn boundary a real server sends; it is what resets the buffer,
    // the walk-back in findMutableThinkingActivity only decides the row.
    dispatch({ type: "status", status: "thinking" });

    dispatch({ type: "thinking_delta", content: "new turn" });

    const rows = activities().filter((m) => m.activityType === "thinking");
    expect(rows).toHaveLength(2);
    expect(rows[1].content).toBe("new turn");
  });

  it("never rewrites a thinking row that was already persisted", () => {
    dispatch({ type: "thinking_delta", content: "claimed" });
    dispatch({ type: "result", content: "answer", message_id: "m1", thinking_message_id: "t1" });

    dispatch({ type: "thinking_delta", content: "next turn" });

    const rows = activities().filter((m) => m.activityType === "thinking");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ id: "t1", content: "claimed" });
  });

  it("keeps sub-agent reasoning on its own track", () => {
    dispatch({ type: "thinking_delta", content: "parent thought" });
    dispatch({ type: "thinking_delta", content: "worker thought", subagent: true });

    const rows = activities().filter((m) => m.activityType === "thinking");
    expect(rows).toHaveLength(2);
    expect(rows[1].subagent).toBe(true);
    expect(streamBuffers.thinkingBuffer).toBe("parent thought");
    expect(streamBuffers.subagentThinkingBuffer).toBe("worker thought");
  });
});

describe("tool frames", () => {
  it("renders the calling chip and arms the fallback buffers", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    expect(activities()[0]).toMatchObject({
      activityType: "tool",
      content: "[Bash] calling...",
    });
    expect(streamBuffers.currentToolName).toBe("Bash");
    expect(streamBuffers.toolInputBuffer).toBe("");
  });

  it("includes the shell command Codex ships on the start frame", () => {
    dispatch({
      type: "tool_use_start",
      content: JSON.stringify({ name: "Shell", command: "ls -la" }),
    });

    expect(activities()[0].content).toBe("[Shell] calling...\n  command: ls -la");
  });

  it("buffers streamed tool input per track", () => {
    dispatch({ type: "tool_input_delta", content: '{"cmd"' });
    dispatch({ type: "tool_input_delta", content: ':"ls"}' });
    dispatch({ type: "tool_input_delta", content: "sub", subagent: true });

    expect(streamBuffers.toolInputBuffer).toBe('{"cmd":"ls"}');
    expect(streamBuffers.subagentToolInputBuffer).toBe("sub");
  });

  it("rewrites the in-flight chip and stamps the persisted id", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    dispatch({
      type: "tool_result",
      message_id: "tool-1",
      content: JSON.stringify({ name: "Bash", input: { command: "ls" } }),
    });

    expect(activities()).toHaveLength(1);
    expect(activities()[0]).toMatchObject({
      content: "[Bash]\n  command: ls",
      id: "tool-1",
    });
    expect(knownMessageIds.has("tool-1")).toBe(true);
    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
  });

  it("matches parallel same-name tools by call id when they finish out of order", () => {
    dispatch({
      type: "tool_use_start",
      content: JSON.stringify({ id: "call-1", name: "Bash", command: "slow" }),
    });
    dispatch({
      type: "tool_use_start",
      content: JSON.stringify({ id: "call-2", name: "Bash", command: "fast" }),
    });

    dispatch({
      type: "tool_result",
      message_id: "row-2",
      content: JSON.stringify({ id: "call-2", name: "Bash", input: { command: "fast" } }),
    });
    dispatch({
      type: "tool_result",
      message_id: "row-1",
      content: JSON.stringify({ id: "call-1", name: "Bash", input: { command: "slow" } }),
    });

    expect(activities()).toMatchObject([
      {
        content: "[Bash]\n  command: slow",
        id: "row-1",
        toolCallId: "call-1",
        toolCompleted: true,
      },
      {
        content: "[Bash]\n  command: fast",
        id: "row-2",
        toolCallId: "call-2",
        toolCompleted: true,
      },
    ]);
  });

  it("does not reuse a completed same-name chip when call ids are absent", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    dispatch({
      type: "tool_result",
      content: JSON.stringify({ name: "Bash", input: { command: "fast" } }),
    });
    dispatch({
      type: "tool_result",
      content: JSON.stringify({ name: "Bash", input: { command: "slow" } }),
    });

    expect(activities().every((message) => message.toolCompleted)).toBe(true);
    expect(activities().map((message) => message.content)).toEqual([
      "[Bash]\n  command: slow",
      "[Bash]\n  command: fast",
    ]);
  });

  it("falls back to the buffered name and input when the payload omits them", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    dispatch({ type: "tool_input_delta", content: '{"command":"ls"}' });

    dispatch({ type: "tool_result", content: "{}" });

    expect(activities()[0].content).toBe("[Bash]\n  command: ls");
  });

  it("appends a canonical row when there is no chip left to mutate", () => {
    dispatch({
      type: "tool_result",
      message_id: "tool-1",
      content: JSON.stringify({ name: "Read", input: { file: "a.ts" } }),
    });

    expect(activities()).toHaveLength(1);
    expect(activities()[0]).toMatchObject({
      activityType: "tool",
      content: "[Read]\n  file: a.ts",
      id: "tool-1",
    });
  });

  it("is a no-op when the canonical tool row is already rendered", () => {
    rememberMessageId("tool-1");
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    dispatch({
      type: "tool_result",
      message_id: "tool-1",
      content: JSON.stringify({ name: "Bash", input: { command: "ls" } }),
    });

    expect(activities()[0].content).toBe("[Bash] calling...");
    expect(streamBuffers.currentToolName).toBe("");
  });

  it("never appends a canonical row for a sub-agent tool result", () => {
    dispatch({
      type: "tool_result",
      subagent: true,
      content: JSON.stringify({ name: "Grep", input: { pattern: "x" } }),
    });

    expect(chatMessages.value).toHaveLength(0);
    expect(streamBuffers.subagentToolName).toBe("");
  });

  it("mutates the sub-agent chip without disturbing the parent's", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    dispatch({
      type: "tool_use_start",
      subagent: true,
      content: JSON.stringify({ name: "Bash" }),
    });

    dispatch({
      type: "tool_result",
      subagent: true,
      content: JSON.stringify({ name: "Bash", input: { command: "pwd" } }),
    });

    expect(activities()[0].content).toBe("[Bash] calling...");
    expect(activities()[1].content).toBe("[Bash]\n  command: pwd");
    expect(streamBuffers.currentToolName).toBe("Bash");
  });

  it("still clears the buffers when the payload cannot be parsed", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    dispatch({ type: "tool_result", content: "not json" });

    expect(activities()[0].content).toBe("[Bash] calling...");
    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
  });

  // Regression: `omitempty` makes a contentless tool_result reachable, and
  // short-circuiting the whole handler on it leaked the buffers into the next
  // tool call (wrong fallback name/input) and kept the row out of the dedup
  // set, so the next history_backfill re-added it.
  it("resets the buffers and remembers the id on a contentless tool_result", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    dispatch({ type: "tool_input_delta", content: '{"command":"ls"}' });

    dispatch({ type: "tool_result", message_id: "tool-1" });

    expect(streamBuffers.currentToolName).toBe("");
    expect(streamBuffers.toolInputBuffer).toBe("");
    expect(knownMessageIds.has("tool-1")).toBe(true);
  });

  it("does not leak a contentless call's name into the next tool chip", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });
    dispatch({ type: "tool_result", message_id: "tool-1" });

    // Second call whose payload omits the name: the fallback must be empty
    // rather than the previous call's "Bash".
    dispatch({ type: "tool_result", message_id: "tool-2", content: "{}" });

    expect(activities().at(-1)?.content).toBe("[tool]\n");
  });
});

describe("system_init", () => {
  it("adopts the model and renders a single header pill", () => {
    dispatch({
      type: "system_init",
      content: JSON.stringify({ model: "sonnet-4", tools: ["Bash", "Read"] }),
    });

    expect(currentModel.value).toBe("sonnet-4");
    expect(modelInfoLine.value).toBe("Model: sonnet-4 | Tools: 2 available");
    expect(activities().filter((m) => m.activityType === "model")).toHaveLength(1);
  });

  it("replaces a previously rendered model pill instead of stacking one", () => {
    dispatch({ type: "system_init", content: JSON.stringify({ model: "a" }) });
    dispatch({ type: "system_init", content: JSON.stringify({ model: "b" }) });

    const pills = activities().filter((m) => m.activityType === "model");
    expect(pills).toHaveLength(1);
    expect(pills[0].content).toBe("Model: b | Tools: 0 available");
  });

  it("does not replace the manually selected model", () => {
    conversationProvider.value = "claude";
    localStorage.setItem(
      RECENT_MODEL_STORAGE_KEY,
      JSON.stringify({ provider: "codex", model: "gpt-5.6", account: "default" }),
    );

    dispatch({ type: "system_init", content: JSON.stringify({ model: "opus-4" }) });

    expect(localStorage.getItem(RECENT_MODEL_STORAGE_KEY)).toBe(
      JSON.stringify({ provider: "codex", model: "gpt-5.6", account: "default" }),
    );
  });

  it("renders a sub-agent's model as indented activity only", () => {
    dispatch({
      type: "system_init",
      subagent: true,
      content: JSON.stringify({ model: "haiku", tools: ["Read"] }),
    });

    expect(currentModel.value).toBe("");
    expect(modelInfoLine.value).toBe("");
    expect(activities()[0]).toMatchObject({
      activityType: "info",
      subagent: true,
      content: "Model: haiku | Tools: 1 available",
    });
  });

  it("falls back to 'unknown' when the payload omits the model", () => {
    dispatch({ type: "system_init", content: "{}" });

    expect(currentModel.value).toBe("unknown");
  });

  it("drops and reports a malformed payload", () => {
    dispatch({ type: "system_init", content: "{not json" });

    expect(currentModel.value).toBe("");
    expect(chatMessages.value).toHaveLength(0);
    expect(consoleError).toHaveBeenCalledOnce();
  });

  it.each([["null"], ['"sonnet"'], ['{"model":42}'], ['{"tools":"Bash"}']])(
    "drops and reports a payload of the wrong shape: %s",
    (content) => {
      dispatch({ type: "system_init", content });

      expect(currentModel.value).toBe("");
      expect(chatMessages.value).toHaveLength(0);
      expect(consoleError).toHaveBeenCalledOnce();
    },
  );
});

describe("context_usage", () => {
  it("adopts the parent's window snapshot", () => {
    dispatch({ type: "context_usage", content: JSON.stringify({ used: 1200, total: 200000 }) });

    expect(contextUsage.value).toMatchObject({ used: 1200, total: 200000 });
  });

  it("ignores a sub-agent's window so the bar doesn't lurch", () => {
    dispatch({
      type: "context_usage",
      subagent: true,
      content: JSON.stringify({ used: 10, total: 100 }),
    });

    expect(contextUsage.value).toBeNull();
  });

  it("drops and reports a malformed payload", () => {
    dispatch({ type: "context_usage", content: "{oops" });

    expect(contextUsage.value).toBeNull();
    expect(consoleError).toHaveBeenCalledOnce();
  });

  it.each([["null"], ["[]"], ['{"used":1}'], ['{"used":"1","total":2}']])(
    "drops and reports a payload of the wrong shape: %s",
    (content) => {
      dispatch({ type: "context_usage", content: JSON.stringify({ used: 5, total: 10 }) });
      dispatch({ type: "context_usage", content });

      // The last good snapshot survives a bad frame.
      expect(contextUsage.value).toEqual({ used: 5, total: 10 });
      expect(consoleError).toHaveBeenCalledOnce();
    },
  );
});

describe("rate_limit", () => {
  it("keeps the latest frame per window", () => {
    dispatch({
      type: "rate_limit",
      content: JSON.stringify({ type: "five_hour", status: "warning", resets_at: 100 }),
    });
    dispatch({
      type: "rate_limit",
      content: JSON.stringify({ type: "seven_day", status: "allowed", resets_at: 200 }),
    });
    dispatch({
      type: "rate_limit",
      content: JSON.stringify({ type: "five_hour", status: "blocked", resets_at: 300 }),
    });

    expect(rateLimits.value.five_hour).toMatchObject({ status: "blocked", resets_at: 300 });
    expect(rateLimits.value.seven_day).toMatchObject({ status: "allowed" });
  });

  it("rejects an unknown window or a non-numeric reset", () => {
    dispatch({
      type: "rate_limit",
      content: JSON.stringify({ type: "one_minute", status: "warning", resets_at: 100 }),
    });
    dispatch({
      type: "rate_limit",
      content: JSON.stringify({ type: "five_hour", status: "warning", resets_at: "soon" }),
    });

    expect(rateLimits.value).toEqual({});
  });

  it("skips an unrendered window quietly but reports a malformed payload", () => {
    dispatch({
      type: "rate_limit",
      content: JSON.stringify({ type: "seven_day_opus", status: "warning", resets_at: 100 }),
    });
    expect(consoleError).not.toHaveBeenCalled();

    dispatch({ type: "rate_limit", content: "null" });
    expect(consoleError).toHaveBeenCalledOnce();
    expect(rateLimits.value).toEqual({});
  });
});

describe("tool_use_start payload", () => {
  it("drops and reports a payload of the wrong shape", () => {
    dispatch({ type: "tool_use_start", content: JSON.stringify({ name: 7 }) });
    dispatch({ type: "tool_use_start", content: "[]" });

    expect(activities()).toHaveLength(0);
    expect(streamBuffers.currentToolName).toBe("");
    expect(consoleError).toHaveBeenCalledTimes(2);
  });
});

describe("stream write coalescing", () => {
  let raw: (msg: WSMessage) => void;

  beforeEach(() => {
    vi.useFakeTimers();
    raw = createMessageDispatcher();
  });

  afterEach(() => {
    flushStreamWrites();
    vi.useRealTimers();
  });

  it("writes the stream row once per frame however many deltas arrive", () => {
    const chunks = Array.from({ length: 50 }, (_, i) => `c${i} `);
    const writes: string[] = [];
    const stop = watch(
      () => streamRows()[0]?.content,
      (content) => {
        if (content !== undefined) writes.push(content);
      },
      { flush: "sync" },
    );

    for (const content of chunks) raw({ type: "delta", content });

    // The buffer is current immediately; the row waits for the frame.
    expect(streamingText.value).toBe(chunks.join(""));
    expect(streamRows()).toHaveLength(0);

    vi.advanceTimersToNextFrame();
    stop();

    expect(writes).toEqual([chunks.join("")]);
    expect(streamRows()).toHaveLength(1);
  });

  it("keeps writing on later frames as more text arrives", () => {
    raw({ type: "delta", content: "Hel" });
    vi.advanceTimersToNextFrame();
    expect(streamRows()[0].content).toBe("Hel");

    raw({ type: "delta", content: "lo" });
    vi.advanceTimersToNextFrame();

    expect(streamRows()).toHaveLength(1);
    expect(streamRows()[0].content).toBe("Hello");
  });

  it("falls back to a timer when no animation frame runs", () => {
    vi.stubGlobal("requestAnimationFrame", undefined);
    try {
      raw({ type: "delta", content: "background" });
      vi.advanceTimersByTime(100);
    } finally {
      vi.unstubAllGlobals();
    }

    expect(streamRows()[0]?.content).toBe("background");
  });

  it("flushes pending text before a tool call so the row keeps its place", () => {
    raw({ type: "delta", content: "Let me check." });
    raw({ type: "tool_use_start", content: JSON.stringify({ name: "Bash" }) });

    expect(activities().map((m) => m.activityType)).toEqual(["stream", "tool"]);
    expect(activities()[0].content).toBe("Let me check.");
  });

  it("lets a result replace the row with the exact final text", () => {
    raw({ type: "delta", content: "Hello " });
    raw({ type: "delta", content: "world" });
    raw({ type: "result", content: "Hello world", message_id: "a1" });
    vi.runAllTimers();

    expect(streamRows()).toHaveLength(0);
    const assistants = chatMessages.value.filter((m) => m.role === "assistant");
    expect(assistants.map((m) => m.content)).toEqual(["Hello world"]);
  });

  it("does not resurrect the row after a turn reset", () => {
    raw({ type: "delta", content: "partial" });
    resetTurnStreamBuffers();
    chatMessages.value = [];
    vi.runAllTimers();

    expect(streamRows()).toHaveLength(0);
  });

  it("keeps parent and sub-agent tracks apart when both are pending", () => {
    raw({ type: "delta", content: "parent" });
    raw({ type: "delta", content: "worker", subagent: true });
    flushStreamWrites();

    expect(streamRows().map((m) => m.content)).toEqual(["parent"]);
    expect(streamRows(true).map((m) => m.content)).toEqual(["worker"]);
  });
});
