// Global singleton store: live streaming / turn-progress state for the active
// conversation. See src/stores/index.ts for the conventions every store in
// this directory follows.
import { ref } from "vue";
import type { UserQuestionRequest } from "@/composables/useWebSocket";

// Every useChat() caller shares these refs.
export const streamingText = ref("");
export const isThinking = ref(false);
// False only while a newly selected conversation is waiting for the server's
// authoritative turn snapshot. Keeping this separate from isConnected avoids
// painting the idle controls during the gap between WS open and init_status.
export const isTurnStatusKnown = ref(false);
export const pendingUserQuestion = ref<UserQuestionRequest | null>(null);
export const isAnsweringUserQuestion = ref(false);

// Tool use + thinking buffers. Grouped in one mutable object (instead of
// exported `let`s) because ESM bindings don't allow cross-module
// assignment — the WS message dispatcher and the facade both write these
// fields directly.
//
// The subagent* fields mirror the parent's tool-state vars but are kept
// separate so the parent's tool_result handler doesn't accidentally
// mutate a sub-agent's `[Name] calling…` activity (or vice versa) when
// both happen to use the same tool name. We also keep an independent
// text/thinking buffer so the sub-agent's stream activity can render
// alongside (not on top of) the parent's, and so the parent's `result`
// handler doesn't sweep the worker's stream activity away mid-flight.
export const streamBuffers = {
  currentToolName: "",
  toolInputBuffer: "",
  thinkingBuffer: "",
  subagentToolName: "",
  subagentToolInputBuffer: "",
  subagentStreamingText: "",
  subagentThinkingBuffer: "",
};

// isThinking is owned by the server's `status` / `init_status` frames, which
// the backend only emits once the agent process has actually been reaped. The
// fallback below is the escape hatch for the one case that leaves no such
// frame: a `status: ready` lost because the socket dropped between the reply
// and the status. Reconnecting heals that via init_status, but a tab that
// never reconnects would sit with a locked composer forever.
//
// Kept here rather than at dispatcher module scope so resetChatStreamStore()
// cancels it — a timer surviving a conversation switch would clear isThinking
// for whatever turn happens to be running by the time it fires.
let turnIdleFallback: ReturnType<typeof setTimeout> | null = null;

export function armTurnIdleFallback(graceMs: number) {
  clearTurnIdleFallback();
  turnIdleFallback = setTimeout(() => {
    turnIdleFallback = null;
    isThinking.value = false;
  }, graceMs);
}

export function clearTurnIdleFallback() {
  if (turnIdleFallback !== null) {
    clearTimeout(turnIdleFallback);
    turnIdleFallback = null;
  }
}

export function mergeThinkingChunk(buffer: string, chunk: string): string {
  if (!buffer) return chunk;
  if (chunk.startsWith(buffer)) return chunk;
  return buffer + chunk;
}

export function resetChatStreamStore() {
  clearTurnIdleFallback();
  streamingText.value = "";
  isThinking.value = false;
  isTurnStatusKnown.value = false;
  pendingUserQuestion.value = null;
  isAnsweringUserQuestion.value = false;
  streamBuffers.currentToolName = "";
  streamBuffers.toolInputBuffer = "";
  streamBuffers.thinkingBuffer = "";
  streamBuffers.subagentToolName = "";
  streamBuffers.subagentToolInputBuffer = "";
  streamBuffers.subagentStreamingText = "";
  streamBuffers.subagentThinkingBuffer = "";
}
