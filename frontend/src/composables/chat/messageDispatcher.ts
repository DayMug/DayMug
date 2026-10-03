import { formatToolInput } from "@/lib/chatFormat";
import { noticeText } from "@/lib/notice";
import { i18n } from "@/i18n";

import type {
  WSCancelAckFrame,
  WSConversationActivityFrame,
  WSConversationAddedFrame,
  WSConversationRemovedFrame,
  WSConversationUpdatedFrame,
  WSErrorFrame,
  WSHistoryBackfillFrame,
  WSInitOkFrame,
  WSInitStatusFrame,
  WSInputAckFrame,
  WSJSONStreamFrame,
  WSKnownMessage,
  WSMessage,
  WSMessageType,
  WSPersistFailedFrame,
  WSPromptStartedFrame,
  WSQueueStatusFrame,
  WSResultFrame,
  WSSessionInfoFrame,
  WSSessionWarningFrame,
  WSStatusFrame,
  WSTextStreamFrame,
  WSTitleUpdatedFrame,
  WSToolResultFrame,
  WSUserMessageFrame,
  WSUserQuestionFrame,
  WSUserQuestionResolvedFrame,
} from "@/lib/wsProtocol";
import {
  isContextUsage,
  isRateLimitInfo,
  isSystemInitPayload,
  isToolUseStartPayload,
  isUserQuestionRequest,
  parseWirePayload,
} from "@/lib/wsProtocol";
import { notifyBrowserTask } from "../useBrowserNotifications";
import {
  currentConversationId,
  currentModel,
  currentSubscriptionId,
  modelInfoLine,
  publishConversationListEvent,
  publishConversationListResync,
  publishTitleUpdate,
} from "@/stores/activeConversationStore";
import { applyConversationActivity } from "@/stores/agentActivityStore";
import { bumpAttentionVersion } from "@/stores/conversationAttentionStore";
import { contextUsage, rateLimits } from "@/stores/chatContextStore";
import {
  chatMessages,
  knownMessageIds,
  rememberMessageId,
  resultVersion,
  type ChatMessage,
} from "@/stores/chatMessageStore";
import {
  clearQueueMetrics,
  isStagedPromptStatus,
  nextPendingPromptSeq,
  pendingPrompts,
  PROMPT_QUEUE_STATUS_IM,
  queueAhead,
  queuePosition,
  queueRunning,
  startedPromptIds,
} from "@/stores/chatQueueStore";
import {
  armTurnIdleFallback,
  clearTurnIdleFallback,
  isThinking,
  isAnsweringUserQuestion,
  isTurnStatusKnown,
  mergeThinkingChunk,
  streamBuffers,
  streamingText,
  pendingUserQuestion,
} from "@/stores/chatStreamStore";
import { deliveryResync, deliveryWatermarks } from "@/stores/wsDeliveryStore";

import { persistConvMeta } from "./contextTracking";
import {
  claimTrailingThinking,
  displayMessageContent,
  parseUsageFromMetadata,
  WAKEUP_ORIGIN,
  parseToolResultPayload,
  pushActivity,
  restMessageToChatMessage,
  scrollChat,
  syncFromCursor,
  updateLastStream,
  wireContentToString,
} from "./messageState";

// Each WS frame type gets a named handler writing straight into the stores
// imported above. State isn't threaded through a deps object because it's
// shared app-wide by design (multiple useChat() callers see the same refs) —
// handlers reaching for the stores directly keeps the data flow identical to
// the pre-split useChat.ts. Tests isolate themselves with resetChatStores().

// How long a turn may go without any authoritative status frame after a
// `result` before the UI assumes the `status: ready` was lost and unlocks the
// composer anyway. Generous on purpose: on a healthy socket `ready` always
// follows, so anything this timer catches is already an anomaly, and undercutting
// the agent's own post-reply shutdown (MCP teardown, Stop hooks) would
// reintroduce the false "idle" this fallback replaced.
const TURN_IDLE_FALLBACK_MS = 30_000;

const GLOBAL_MESSAGE_TYPES = new Set<string>([
  "conversation_added",
  "conversation_updated",
  "conversation_removed",
  "conversation_activity",
  "attention_changed",
  "title_updated",
]);

// Keyed by WSMessageType so adding a member to the union without teaching
// the dispatcher about it is a compile error rather than a silently
// dropped frame. Values are unused; only key presence matters.
const KNOWN_MESSAGE_TYPES: Record<WSMessageType, true> = {
  history_backfill: true,
  user_message: true,
  delta: true,
  thinking_delta: true,
  tool_input_delta: true,
  result: true,
  context_usage: true,
  rate_limit: true,
  system_init: true,
  tool_use_start: true,
  tool_result: true,
  init_ok: true,
  title_updated: true,
  conversation_added: true,
  conversation_updated: true,
  conversation_removed: true,
  conversation_activity: true,
  attention_changed: true,
  messages_cleared: true,
  init_status: true,
  status: true,
  queue_status: true,
  input_ack: true,
  prompt_started: true,
  persist_failed: true,
  cancel_ack: true,
  error: true,
  session_info: true,
  session_warning: true,
  user_question: true,
  user_question_resolved: true,
};

function handleUserQuestion(msg: WSUserQuestionFrame) {
  if (!msg.content) return;
  // Malformed provider control data is dropped; transcript streaming continues.
  const request = parseWirePayload(msg.type, msg.content, isUserQuestionRequest);
  if (!request) return;
  pendingUserQuestion.value = request;
  isAnsweringUserQuestion.value = false;
  scrollChat();
  void notifyBrowserTask({
    kind: "waiting",
    conversationId: msg.conversation_id ?? currentConversationId.value,
    eventId: request.request_id,
  });
}

function handleUserQuestionResolved(msg: WSUserQuestionResolvedFrame) {
  if (pendingUserQuestion.value?.request_id === msg.request_id) {
    pendingUserQuestion.value = null;
  }
  isAnsweringUserQuestion.value = false;
}

// Frames from a newer backend carry a type this build has no handler for.
// They were always ignored (the switch below has no default arm); the
// guard makes that explicit and lets the switch narrow the union soundly.
function isKnownFrame(msg: WSMessage): msg is WSKnownMessage {
  return Object.hasOwn(KNOWN_MESSAGE_TYPES, msg.type);
}

function belongsToCurrentSubscription(msg: WSMessage): boolean {
  if (GLOBAL_MESSAGE_TYPES.has(msg.type)) return true;
  if (
    currentConversationId.value &&
    msg.conversation_id &&
    msg.conversation_id !== currentConversationId.value
  ) {
    return false;
  }
  if (
    currentSubscriptionId.value &&
    msg.subscription_id &&
    msg.subscription_id !== currentSubscriptionId.value
  ) {
    return false;
  }
  return true;
}

// advanceWatermark records a frame's sequence number and reports whether
// anything went missing before it.
//
// Both server-side fanouts drop frames for a backed-up client rather than
// stalling the stream, so a jump in `seq` is the only evidence we get. Two
// non-gaps have to be excluded: the very first sequenced frame (no baseline —
// a mid-turn join legitimately starts at seq 87, because the replay buffer
// was reset at the last persisted checkpoint), and a sequence that fails to
// advance, which means the server-side counter restarted (a Broadcaster room
// recreated after idle GC, or a socket that re-joined the hub).
function advanceWatermark(stream: "conversation" | "hub", seq: number): boolean {
  const previous = deliveryWatermarks[stream];
  deliveryWatermarks[stream] = seq;
  return previous > 0 && seq > previous + 1;
}

// noteFrameSeq routes a frame to its stream's watermark and, on a gap, to
// that stream's repair. The two streams share one socket but count
// separately, so they must never be compared against each other.
//
// Conversation frames (service.Broadcaster): re-pull persisted rows from the
// message cursor. Tool calls, assistant replies and errors all have DB rows
// and come back intact. In-flight deltas can't be recovered and don't need to
// be — the turn's `result` frame replaces the streamed text wholesale.
//
// Hub frames (service.UserHub): the hub keeps no replay buffer, so a missed
// conversation_added / _removed / _updated / title_updated leaves the sidebar
// showing a list the server has moved past. Refetching it is the only repair.
function noteFrameSeq(msg: WSMessage) {
  const seq = msg.seq;
  if (typeof seq !== "number" || seq <= 0) return; // unsequenced frame

  if (GLOBAL_MESSAGE_TYPES.has(msg.type)) {
    if (advanceWatermark("hub", seq)) publishConversationListResync();
    return;
  }

  if (!advanceWatermark("conversation", seq)) return;
  if (deliveryResync.conversation) return;
  const pull = syncFromCursor().finally(() => {
    if (deliveryResync.conversation === pull) deliveryResync.conversation = null;
  });
  deliveryResync.conversation = pull;
}

function handleHistoryBackfill(msg: WSHistoryBackfillFrame) {
  // Persisted messages from the server. Two callers share this path:
  //   - reconnect with a cursor: server returns only items > cursor.
  //   - empty cursor (brand-new conv whose first message landed
  //     before the client could anchor): server returns everything.
  // Dedup by id, then route by queue_status:
  //   - staged rows (the dispatcher's own 'pending', and an IM turn waiting
  //     for an account slot) go to the staging area so they keep their queued
  //     semantics (position-displayed, cancellable) instead of showing up in
  //     chat history out-of-order.
  //   - everything else (including 'processing' — partial output is
  //     already tied to it) goes into chatMessages as normal.
  if (msg.messages?.length) {
    for (const m of msg.messages) {
      if (knownMessageIds.has(m.id)) continue;
      if (m.role === "user" && isStagedPromptStatus(m.queue_status)) {
        // Resync staging area on reconnect: drop the rebroadcast
        // copy if a sibling tab is already showing it.
        if (!pendingPrompts.value.some((p) => p.id === m.id)) {
          pendingPrompts.value.push({
            id: m.id,
            content: displayMessageContent(
              wireContentToString(m.content),
              m.metadata?.sender,
              m.metadata?.attachments,
            ),
            attachments: m.metadata?.attachments,
            sender: m.metadata?.sender,
            clientKey: `srv-${m.id}`,
            fromIM: m.queue_status === PROMPT_QUEUE_STATUS_IM,
          });
        }
        rememberMessageId(m.id);
        continue;
      }
      chatMessages.value.push(restMessageToChatMessage(m));
      rememberMessageId(m.id);
    }
    scrollChat();
  }
}

function handleUserMessage(msg: WSUserMessageFrame) {
  // Peer-tab echo of a user prompt. The sender's path puts the
  // optimistic copy into pendingPrompts (the staging area) and only
  // promotes it into chatMessages when prompt_started fires —
  // peer tabs must follow the same flow or they'd render the prompt
  // as a regular chat row while the sender's tab still shows it as
  // "Queued" (the cross-tab divergence behind this code path).
  // Dedup by id against rows already in chatMessages or already
  // staged so the echo is a no-op for tabs that have caught up.
  const content = displayMessageContent(
    wireContentToString(msg.content),
    msg.metadata?.sender,
    msg.metadata?.attachments,
  );
  if (!content && !msg.metadata?.attachments?.length) {
    // Nothing to render — but the row does exist server-side, so the cursor
    // has to move past it. Returning before rememberMessageId left the
    // cursor stranded behind this id and the next history_backfill resent
    // the same row, making the cursor non-monotonic in server event order.
    rememberMessageId(msg.message_id);
    return;
  }
  if (knownMessageIds.has(msg.message_id)) return;
  // Guard on message_id before comparing: staging entries carry an
  // undefined id until input_ack lands, so a malformed frame without
  // one would otherwise match the first un-acked local prompt.
  if (msg.message_id && pendingPrompts.value.some((p) => p.id === msg.message_id)) {
    rememberMessageId(msg.message_id);
    return;
  }
  // Race: prompt_started already fired for this id (worker won
  // the broadcaster mutex over the input handler). Skip the
  // staging area or the row would be stranded waiting for a
  // prompt_started that already came and went.
  if (startedPromptIds.has(msg.message_id)) {
    startedPromptIds.delete(msg.message_id);
    chatMessages.value.push({
      role: "user",
      content,
      id: msg.message_id,
      created_at: new Date().toISOString(),
      attachments: msg.metadata?.attachments,
      sender: msg.metadata?.sender,
    });
    rememberMessageId(msg.message_id);
    scrollChat();
    return;
  }
  pendingPrompts.value.push({
    id: msg.message_id,
    content,
    attachments: msg.metadata?.attachments,
    sender: msg.metadata?.sender,
    // peer-side stable handle. The backend only emits the echo after
    // the row is persisted, so message_id is always set; the sequence
    // suffix guards against a malformed frame leaving the key empty.
    clientKey: `peer-${msg.message_id || `t${Date.now()}-${nextPendingPromptSeq()}`}`,
    // Inherit any pool position the queue_status already pushed
    // before user_message arrived, so the head-of-staging label
    // matches the sender's "Next up" / "N tasks ahead" instead of
    // dropping back to a bare "Queued".
    poolPosition: queuePosition.value,
    poolAhead: queueAhead.value,
    poolRunning: queueRunning.value,
    fromIM: msg.queue_status === PROMPT_QUEUE_STATUS_IM,
  });
  rememberMessageId(msg.message_id);
  scrollChat();
}

function handleDelta(msg: WSTextStreamFrame) {
  const content = wireContentToString(msg.content);
  if (content) {
    if (msg.subagent) {
      streamBuffers.subagentStreamingText = msg.replay
        ? mergeReplayChunk("subagentText", streamBuffers.subagentStreamingText, content)
        : endReplayBurst("subagentText", streamBuffers.subagentStreamingText + content);
      markStreamDirty("subagent");
      return;
    }
    streamingText.value = msg.replay
      ? mergeReplayChunk("text", streamingText.value, content)
      : endReplayBurst("text", streamingText.value + content);
    markStreamDirty("main");
  }
}

// Stream-row write coalescing. A fast model emits dozens of deltas per frame,
// and every write to the stream row re-renders its markdown from scratch — so
// the buffers above take every chunk immediately, but the row itself is
// written at most once per animation frame. Anything that reads or replaces
// the row (every non-delta frame, a turn reset, a conversation switch) must
// see the full text, so the dispatcher flushes synchronously ahead of those;
// the frame callback is only the "nothing else happened" path.
type StreamTrack = "main" | "subagent";

// Insertion-ordered so the first track to receive text also creates its row
// first, exactly as the per-delta writes used to.
const dirtyStreams = new Set<StreamTrack>();
let cancelScheduledFlush: (() => void) | null = null;

// A hidden tab never runs rAF callbacks; the timer keeps the row from falling
// arbitrarily far behind there (and gives rAF-less environments a clock).
const STREAM_FLUSH_FALLBACK_MS = 100;

function markStreamDirty(track: StreamTrack) {
  dirtyStreams.add(track);
  if (cancelScheduledFlush) return;
  let raf: number | null = null;
  const timer = setTimeout(flushStreamWrites, STREAM_FLUSH_FALLBACK_MS);
  if (typeof requestAnimationFrame === "function") {
    raf = requestAnimationFrame(() => flushStreamWrites());
  }
  cancelScheduledFlush = () => {
    clearTimeout(timer);
    if (raf !== null && typeof cancelAnimationFrame === "function") cancelAnimationFrame(raf);
  };
}

function cancelStreamFlush() {
  cancelScheduledFlush?.();
  cancelScheduledFlush = null;
}

// flushStreamWrites writes any buffered stream text into its row now. Safe to
// call at any time; a no-op when nothing is pending.
export function flushStreamWrites() {
  cancelStreamFlush();
  if (dirtyStreams.size === 0) return;
  const tracks = [...dirtyStreams];
  dirtyStreams.clear();
  for (const track of tracks) {
    const text = track === "subagent" ? streamBuffers.subagentStreamingText : streamingText.value;
    // An emptied buffer means something already consumed the turn (result,
    // reset) without going through a flush; writing "" would resurrect a row.
    if (!text) continue;
    updateLastStream(text, track === "subagent" ? { subagent: true } : {});
  }
}

// Pending text belongs to buffers that are about to be discarded.
function discardStreamWrites() {
  cancelStreamFlush();
  dirtyStreams.clear();
}

// One replay walk per stream buffer that can receive replayed chunks.
type ReplayTrack = "text" | "thinking" | "subagentText" | "subagentThinking";

// How far into each buffer the current replay burst has walked. A burst is the
// broadcaster's in-flight buffer resent verbatim on Join, so its chunks arrive
// in original order and whatever the tab already holds is a prefix of that
// sequence — which makes "did we already append this chunk?" a question about
// a *position*, not about the buffer as a whole. The old
// `buffer.includes(chunk)` test answered the whole-buffer question and so
// swallowed every legitimate repeat of a short chunk (`\n\n`, `()`, a word
// used twice); a plain tail test would instead duplicate every mid-buffer
// chunk, because a full reconnect replays the turn from its first chunk.
//
// A walk only survives while nothing else has touched its buffer: a live
// chunk (endReplayBurst) or a turn boundary (resetTurnStreamBuffers) drops it,
// and the recorded buffer is compared before the position is trusted. That is
// what makes a second reconnect inside one turn restart its walk from the top
// instead of appending a duplicate.
const replayWalks = new Map<ReplayTrack, { buffer: string; at: number }>();

function mergeReplayChunk(track: ReplayTrack, buffer: string, chunk: string): string {
  const walk = replayWalks.get(track);
  const at = walk?.buffer === buffer ? walk.at : 0;
  if (!buffer) return advanceReplayWalk(track, chunk, chunk.length);
  // The chunk sits exactly where the walk expects it: we appended it live
  // before the socket dropped, so only the position moves.
  if (buffer.startsWith(chunk, at)) return advanceReplayWalk(track, buffer, at + chunk.length);
  // Cumulative snapshot superseding what we hold — Codex resends whole
  // reasoning blocks rather than deltas.
  if (chunk.startsWith(buffer)) return advanceReplayWalk(track, chunk, chunk.length);
  const merged = buffer + chunk;
  return advanceReplayWalk(track, merged, merged.length);
}

function advanceReplayWalk(track: ReplayTrack, buffer: string, at: number): string {
  replayWalks.set(track, { buffer, at });
  return buffer;
}

// A live chunk means the burst (if any) is over: the recorded position no
// longer describes the buffer, so the next burst must walk it from the start.
function endReplayBurst(track: ReplayTrack, buffer: string): string {
  replayWalks.delete(track);
  return buffer;
}

function handleResult(msg: WSResultFrame) {
  if (msg.subagent) {
    // Finalize the sub-agent's stream activity and reset its
    // buffers. We don't push a real assistant message: the parent's
    // upcoming tool_result already carries whatever output the
    // worker returned.
    chatMessages.value = chatMessages.value.filter(
      (m) => !(m.role === "activity" && m.activityType === "stream" && m.subagent),
    );
    streamBuffers.subagentStreamingText = "";
    streamBuffers.subagentThinkingBuffer = "";
    scrollChat();
    return;
  }
  // Server may echo a result whose persisted id we've already
  // rendered (e.g. REST sync delivered it first, then the live
  // event arrived). In that case just sweep the in-flight stream
  // activity — the canonical row is already in chatMessages. This
  // branch must NOT return: it still has to fall through to the
  // turn-ended reset below, or the echo leaves isThinking stuck at
  // true and locks the composer for good.
  if (msg.message_id && knownMessageIds.has(msg.message_id)) {
    chatMessages.value = chatMessages.value.filter(
      (m) => !(m.role === "activity" && m.activityType === "stream" && !m.subagent),
    );
    scrollChat();
    resultVersion.value++;
  } else {
    const content = wireContentToString(msg.content);
    if (content) {
      // Drop the in-flight stream activity (it's about to be
      // replaced by the persisted assistant row).
      chatMessages.value = chatMessages.value.filter(
        (m) => !(m.role === "activity" && m.activityType === "stream" && !m.subagent),
      );
      // Promote the trailing thinking activity into a persisted row
      // (so backfill won't duplicate it on the next reconnect).
      if (msg.thinking_message_id) {
        claimTrailingThinking(msg.thinking_message_id);
      }
      const assistant: ChatMessage = {
        role: "assistant",
        content,
        id: msg.message_id,
        created_at: new Date().toISOString(),
        attachments: msg.metadata?.attachments,
      };
      // Per-turn usage rides along on metadata.usage; backend embeds it
      // here so the chip renders synchronously with the assistant
      // bubble it describes, without a separate `usage` WS event and
      // without a localStorage shadow.
      const usage = parseUsageFromMetadata(msg.metadata);
      if (usage) assistant.usage = usage;
      // A turn nobody prompted. Same metadata field the REST restore reads,
      // so the badge survives a refresh rather than being a live-only cue.
      if (msg.metadata?.origin === WAKEUP_ORIGIN) assistant.wakeup = true;
      chatMessages.value.push(assistant);
      rememberMessageId(msg.message_id);
      scrollChat();
      resultVersion.value++;
    }
  }
  // A parent `result` frame does NOT mean the turn is over. The web
  // transcript runs the backend's AgentStreamPerResult mode — one assistant
  // row per result frame — so a codex turn that emits several agent_message
  // frames sends several, and even a single-result claude turn keeps its CLI
  // alive afterwards to flush the session JSONL and tear down MCP servers.
  // `status: ready` is the only end-of-turn signal, and the backend withholds
  // it until cmd.Wait has reaped the process. Clearing isThinking here
  // unlocked the composer while the agent was demonstrably still working.
  //
  // What the frame does prove is that the agent was alive at this instant, so
  // assert the turn is running and re-arm the lost-`ready` safety net rather
  // than firing it: if no status frame lands within the grace window the
  // fallback clears isThinking, so the composer still can't strand.
  isThinking.value = true;
  armTurnIdleFallback(TURN_IDLE_FALLBACK_MS);
  streamingText.value = "";
  streamBuffers.thinkingBuffer = "";
}

function handleContextUsage(msg: WSJSONStreamFrame) {
  // Sub-agents have their own (smaller) context window; surfacing it in
  // the parent's bar would make the bar lurch. Backend already drops
  // these, but guard here so a future change can't surprise the UI.
  if (msg.subagent) return;
  const content = wireContentToString(msg.content);
  if (!content) return;
  const usage = parseWirePayload(msg.type, content, isContextUsage);
  if (!usage) return;
  contextUsage.value = usage;
  persistConvMeta();
}

function handleRateLimit(msg: WSJSONStreamFrame) {
  const content = wireContentToString(msg.content);
  if (!content) return;
  const info = parseWirePayload(msg.type, content, isRateLimitInfo);
  // Windows the badge doesn't render are skipped, not malformed.
  if (!info || (info.type !== "five_hour" && info.type !== "seven_day")) return;
  // Replace, don't merge — Anthropic resends the full window payload on every
  // transition, so trusting the latest frame gives us the freshest
  // status/overage flags too.
  rateLimits.value = { ...rateLimits.value, [info.type]: info };
}

function handleSystemInit(msg: WSJSONStreamFrame) {
  const content = wireContentToString(msg.content);
  if (!content) return;
  const info = parseWirePayload(msg.type, content, isSystemInitPayload);
  if (!info) return;
  const model = info.model || "unknown";
  const toolCount = info.tools?.length || 0;
  if (msg.subagent) {
    // Sub-agent's model + tool list belongs to the worker, not the
    // header. Render it as an indented activity so the user sees
    // the worker started, but don't mutate currentModel.
    pushActivity("info", i18n.global.t("slashCommands.modelInfo", { model, count: toolCount }), {
      subagent: true,
    });
  } else {
    currentModel.value = model;
    const line = i18n.global.t("slashCommands.modelInfo", { model, count: toolCount });
    modelInfoLine.value = line;
    // Drop any previously-rendered model pill (cached from a refresh,
    // or from an earlier system_init in the same session) so the
    // header doesn't accumulate duplicates.
    chatMessages.value = chatMessages.value.filter(
      (m) => !(m.role === "activity" && m.activityType === "model" && !m.subagent),
    );
    persistConvMeta();
    pushActivity("model", line);
  }
}

function handleToolUseStart(msg: WSJSONStreamFrame) {
  const content = wireContentToString(msg.content);
  if (!content) return;
  const info = parseWirePayload(msg.type, content, isToolUseStartPayload);
  if (!info) return;
  const name = info.name || "unknown";
  const detail =
    info.command === undefined ? "" : formatToolInput(JSON.stringify({ command: info.command }));
  const activity = detail ? `[${name}] calling...\n${detail}` : `[${name}] calling...`;
  if (msg.subagent) {
    streamBuffers.subagentToolName = name;
    streamBuffers.subagentToolInputBuffer = "";
    pushActivity("tool", activity, {
      subagent: true,
      toolStartedAt: msg.tool_started_at ?? Date.now(),
      toolCallId: info.id,
    });
  } else {
    streamBuffers.currentToolName = name;
    streamBuffers.toolInputBuffer = "";
    pushActivity("tool", activity, {
      toolStartedAt: msg.tool_started_at ?? Date.now(),
      toolCallId: info.id,
    });
  }
}

function handleToolInputDelta(msg: WSTextStreamFrame) {
  const content = wireContentToString(msg.content);
  if (content) {
    if (msg.subagent) {
      streamBuffers.subagentToolInputBuffer += content;
    } else {
      streamBuffers.toolInputBuffer += content;
    }
  }
}

function handleToolResult(msg: WSToolResultFrame) {
  const wantSub = !!msg.subagent;
  // Already-persisted dedup: if REST/backfill delivered the canonical tool
  // row first, the live event renders nothing.
  const alreadyRendered = !wantSub && !!msg.message_id && knownMessageIds.has(msg.message_id);
  // Only the rendering half is conditional on content. A tool whose payload
  // compacts to nothing arrives as a contentless frame (Go `omitempty`), and
  // gating the whole handler on content leaked this call's name/input into the
  // next tool chip's fallback and kept the row out of the dedup set.
  if (msg.content && !alreadyRendered) {
    try {
      const info = parseToolResultPayload(msg.content);
      if (!info) throw new Error("invalid tool result payload");
      const fallbackName = wantSub ? streamBuffers.subagentToolName : streamBuffers.currentToolName;
      const name = info.name || fallbackName || "tool";
      const fallbackInput = wantSub
        ? streamBuffers.subagentToolInputBuffer
        : streamBuffers.toolInputBuffer;
      const input = info.input ? JSON.stringify(info.input) : fallbackInput;
      const formatted = formatToolInput(input);
      const msgs = chatMessages.value;
      let mutated = false;
      // Prefer the provider call id because parallel tools can share a name
      // and finish out of order. Older providers without ids fall back to the
      // most recent unfinished chip, never one already marked complete.
      let targetIndex = info.id
        ? msgs.findIndex(
            (candidate) =>
              candidate.role === "activity" &&
              candidate.activityType === "tool" &&
              !!candidate.subagent === wantSub &&
              !candidate.toolCompleted &&
              candidate.toolCallId === info.id,
          )
        : -1;
      if (targetIndex < 0) {
        for (let i = msgs.length - 1; i >= 0; i--) {
          const candidate = msgs[i];
          if (
            candidate.role === "activity" &&
            candidate.activityType === "tool" &&
            !!candidate.subagent === wantSub &&
            !candidate.toolCompleted &&
            candidate.content.startsWith(`[${name}]`)
          ) {
            targetIndex = i;
            break;
          }
        }
      }
      if (targetIndex >= 0) {
        const target = msgs[targetIndex];
        if (
          target.role === "activity" &&
          target.activityType === "tool" &&
          !!target.subagent === wantSub
        ) {
          target.content = `[${name}]\n${formatted}`;
          const startedAt = target.toolStartedAt;
          target.toolDurationMs =
            msg.tool_duration_ms ?? (startedAt === undefined ? undefined : Date.now() - startedAt);
          target.toolCompleted = true;
          if (info.id) target.toolCallId = info.id;
          if (!wantSub && msg.message_id) {
            target.id = msg.message_id;
            rememberMessageId(msg.message_id);
          }
          mutated = true;
        }
      }
      // No in-flight chip to mutate (e.g. reconnect after replay
      // was already cleared). Append a fresh canonical entry.
      if (!mutated && !wantSub) {
        chatMessages.value.push({
          role: "activity",
          content: `[${name}]\n${formatted}`,
          activityType: "tool",
          id: msg.message_id,
          toolCallId: info.id,
          toolDurationMs: msg.tool_duration_ms,
          toolCompleted: true,
        });
        rememberMessageId(msg.message_id);
      }
      scrollChat();
    } catch {
      // ignore
    }
  } else if (!msg.content && !wantSub) {
    // Nothing renderable, yet the row is persisted server-side: register the
    // id so the reconnect cursor advances and the next history_backfill
    // doesn't resend it. Content that *is* present but unparseable stays
    // unregistered on purpose — backfill is then the recovery path for it.
    rememberMessageId(msg.message_id);
  }
  if (wantSub) {
    streamBuffers.subagentToolName = "";
    streamBuffers.subagentToolInputBuffer = "";
  } else {
    streamBuffers.currentToolName = "";
    streamBuffers.toolInputBuffer = "";
  }
}

function handleThinkingDelta(msg: WSTextStreamFrame) {
  const content = wireContentToString(msg.content);
  if (content) {
    if (msg.subagent) {
      streamBuffers.subagentThinkingBuffer = msg.replay
        ? mergeReplayChunk("subagentThinking", streamBuffers.subagentThinkingBuffer, content)
        : endReplayBurst(
            "subagentThinking",
            mergeThinkingChunk(streamBuffers.subagentThinkingBuffer, content),
          );
      const existing = findMutableThinkingActivity(true);
      if (existing) {
        existing.content = streamBuffers.subagentThinkingBuffer;
      } else {
        pushActivity("thinking", streamBuffers.subagentThinkingBuffer, { subagent: true });
      }
      scrollChat();
      return;
    }
    streamBuffers.thinkingBuffer = msg.replay
      ? mergeReplayChunk("thinking", streamBuffers.thinkingBuffer, content)
      : endReplayBurst("thinking", mergeThinkingChunk(streamBuffers.thinkingBuffer, content));
    const existing = findMutableThinkingActivity(false);
    if (existing) {
      existing.content = streamBuffers.thinkingBuffer;
    } else {
      pushActivity("thinking", streamBuffers.thinkingBuffer);
    }
    scrollChat();
  }
}

function findMutableThinkingActivity(subagent: boolean): ChatMessage | null {
  const msgs = chatMessages.value;
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m.role === "user" || m.role === "assistant" || m.role === "warning" || m.role === "error") {
      return null;
    }
    // Mutate an existing in-flight thinking activity on the same track even
    // when tool activity has arrived after it. Codex reasoning summaries are
    // cumulative snapshots, so opening a fresh row after each tool call repeats
    // the earlier thinking text.
    if (m.role === "activity" && m.activityType === "thinking" && !!m.subagent === subagent) {
      return m.id ? null : m;
    }
  }
  return null;
}

function handleTitleUpdated(msg: WSTitleUpdatedFrame) {
  // Backend finished auto-summarizing the conversation title (async after
  // the assistant reply). Surface it via titleUpdate so the app shell can
  // push it into the conversation list without a manual refresh. The
  // version counter ensures repeat titles still trigger the watcher.
  if (msg.conversation_id && msg.title) {
    publishTitleUpdate(msg.conversation_id, msg.title);
  }
}

function handleConversationAdded(msg: WSConversationAddedFrame) {
  // A peer tab (or the same user on another device) created a new
  // session. Publish so the app shell can splice it into the
  // sidebar list. The id comes off the nested row rather than the
  // frame's conversation_id — the cron scheduler's variant of this
  // frame omits the top-level id entirely. The optional chain is a
  // runtime guard, not a contract statement: every backend emitter sets
  // `conversation`, but a frame is untrusted JSON until we've read it.
  if (msg.conversation?.id) {
    publishConversationListEvent("added", msg.conversation.id, msg.conversation);
  }
}

function handleConversationUpdated(msg: WSConversationUpdatedFrame) {
  // A peer tab renamed, toggled notifications, or moved the work
  // dir of a session. Publish the full row so the listener can
  // patch in place without losing fields the local copy already
  // had.
  if (msg.conversation?.id) {
    publishConversationListEvent("updated", msg.conversation.id, msg.conversation);
  }
}

function handleConversationRemoved(msg: WSConversationRemovedFrame) {
  // A peer tab deleted a session — drop it from the local caches.
  if (msg.conversation_id) {
    publishConversationListEvent("removed", msg.conversation_id);
  }
}

function handleConversationActivity(msg: WSInitOkFrame | WSConversationActivityFrame) {
  applyConversationActivity(
    msg.running_agent_ids,
    msg.running_conversation_ids,
    msg.running_conversations,
    msg.queued_conversations,
    msg.waiting_conversations,
  );
}

// Every per-turn stream buffer, cleared as one unit. A turn boundary — live
// (`status`) or reconstructed after a WS gap (`init_status`) — invalidates all
// of them, and the two paths listing their own subsets is how the reconnect
// path came to repair less state than a live frame: a stale parent-tool name
// survived as the fallback label for the *next* tool call. A conversation
// switch is a boundary too: once the target turn has run a tool its replay no
// longer starts with `status: thinking`, so nothing else would clear the
// previous conversation's thinking before the target's replay appends to it.
export function resetTurnStreamBuffers() {
  discardStreamWrites();
  replayWalks.clear();
  streamingText.value = "";
  streamBuffers.thinkingBuffer = "";
  streamBuffers.currentToolName = "";
  streamBuffers.toolInputBuffer = "";
  streamBuffers.subagentStreamingText = "";
  streamBuffers.subagentThinkingBuffer = "";
  streamBuffers.subagentToolName = "";
  streamBuffers.subagentToolInputBuffer = "";
}

function handleInitStatus(msg: WSInitStatusFrame) {
  // Authoritative status snapshot delivered right after init_ok. Heals
  // the case where a tab missed `status: ready` during a WS gap (the
  // run finished, the broadcaster GC'd the replay buffer, the next
  // init's join found an empty room). Without this push isThinking
  // would stay stuck at true and new messages would route into the
  // local pending queue instead of being sent — visibly: "still
  // thinking" on this tab while a fresh tab opened to the same
  // conversation shows it as idle.
  //
  // We do NOT clear pendingPrompts here: server-persisted pending
  // rows are restored by the matching history_backfill frame,
  // and locally-queued (not-yet-acked) prompts must survive a
  // soft resync so the user doesn't lose unsent text.
  //
  // Authoritative either way, so the post-`result` fallback is moot: drop it
  // before applying the snapshot or it would fire later and clear a turn this
  // frame just confirmed is running.
  clearTurnIdleFallback();
  isTurnStatusKnown.value = true;
  if (msg.status === "thinking") {
    isThinking.value = true;
  } else if (msg.status === "ready") {
    isThinking.value = false;
    clearQueueMetrics();
    resetTurnStreamBuffers();
    pendingUserQuestion.value = null;
    isAnsweringUserQuestion.value = false;
  }
}

function handleStatus(msg: WSStatusFrame) {
  // The signal the post-`result` fallback was waiting for. Whatever this
  // frame decides is the truth, so retire the timer first.
  clearTurnIdleFallback();
  isTurnStatusKnown.value = true;
  if (msg.status === "ready") {
    const wasThinking = isThinking.value;
    isThinking.value = false;
    clearQueueMetrics();
    resetTurnStreamBuffers();
    pendingUserQuestion.value = null;
    isAnsweringUserQuestion.value = false;
    // Genuine idle — nudge the tab if the user looked away while Claude
    // was working. Skip when wasThinking is false: this fires on initial
    // WS connect / conversation switch and we don't want a phantom flash.
    if (wasThinking) {
      void notifyBrowserTask({
        kind: "completed",
        conversationId: msg.conversation_id ?? currentConversationId.value,
        eventId: msg.seq ? `seq-${msg.seq}` : "turn-ready",
      });
    }
  } else if (msg.status === "thinking") {
    isThinking.value = true;
    // Slot just granted; clear any prior queue indicator.
    clearQueueMetrics();
    resetTurnStreamBuffers();
  } else if (msg.status === "shutdown") {
    // The server is going away. Two emitters, one consequence for the UI:
    // nothing more about the in-flight turn will arrive on this socket.
    //   - handler/terminal_ws.go — drain started; the connection is
    //     force-closed once the drain timeout expires. Whatever job is
    //     running finishes server-side, but its `status: ready` may never
    //     reach us.
    //   - service/prompt_runner.go — the drainer refused the job outright,
    //     so the prompt never runs and no `ready` follows, ever.
    // Leaving isThinking at true strands the composer: every subsequent
    // prompt silently piles up in the local pending queue instead of being
    // sent. Reset the turn state the same way `ready` does; a reconnect's
    // init_status re-establishes the truth if the run was still alive.
    // Deliberately no browser notification — nothing completed.
    isThinking.value = false;
    clearQueueMetrics();
    resetTurnStreamBuffers();
    pendingUserQuestion.value = null;
    isAnsweringUserQuestion.value = false;
  }
}

function handleQueueStatus(msg: WSQueueStatusFrame) {
  // Server holds this conversation's about-to-run prompt in the
  // per-account pool because another account-mate is already
  // occupying every slot. The position is one-based among jobs waiting
  // for a slot, surfaced on the head of the staging area (it's
  // always the head that the dispatcher is trying to claim).
  queuePosition.value = typeof msg.queue_position === "number" ? msg.queue_position : 1;
  // Read the counts straight off the frame: the backend sends them as *int so
  // "nothing ahead of you" arrives as a real 0. Deriving a fallback from
  // queuePosition (as this did while the fields were erasable) overstated the
  // queue by one whenever the answer was genuinely zero.
  queueAhead.value = typeof msg.queue_ahead === "number" ? msg.queue_ahead : 0;
  queueRunning.value = typeof msg.queue_running === "number" ? msg.queue_running : 0;
  if (pendingPrompts.value.length > 0) {
    pendingPrompts.value[0].poolPosition = queuePosition.value;
    pendingPrompts.value[0].poolAhead = queueAhead.value;
    pendingPrompts.value[0].poolRunning = queueRunning.value;
  }
  isThinking.value = true;
}

function handleInputAck(msg: WSInputAckFrame) {
  // Server has persisted our prompt and assigned it a canonical
  // id. Stamp it onto the matching staging entry so cancel_ack /
  // prompt_started / history_backfill can identify the row.
  const target = pendingPrompts.value.find((p) => !p.id);
  if (target) {
    target.id = msg.message_id;
  }
  rememberMessageId(msg.message_id);
}

function handlePromptStarted(msg: WSPromptStartedFrame) {
  // Dispatcher worker just claimed a pending prompt and is about to
  // run it. Promote the matching staging entry into chatMessages as
  // a regular user turn — that's the moment the prompt visibly
  // "starts" from the user's perspective. Pool position no longer
  // applies once we're processing, so clear queuePosition too.
  if (msg.message_id) {
    const idx = pendingPrompts.value.findIndex((p) => p.id === msg.message_id);
    if (idx >= 0) {
      const promoted = pendingPrompts.value[idx];
      pendingPrompts.value.splice(idx, 1);
      chatMessages.value.push({
        role: "user",
        content: promoted.content,
        id: promoted.id,
        created_at: new Date().toISOString(),
        attachments: promoted.attachments,
        sender: promoted.sender,
      });
      scrollChat();
    } else if (!knownMessageIds.has(msg.message_id)) {
      // Peer-tab race: the user_message echo carrying the prompt's
      // content hasn't landed yet. Record the id so that handler
      // pushes the row directly into chatMessages instead of the
      // staging area when it does arrive.
      startedPromptIds.add(msg.message_id);
    }
    clearQueueMetrics();
  }
}

function handlePersistFailed(msg: WSPersistFailedFrame) {
  // Server tells us a save was permanently rejected (SQLITE_BUSY
  // exhausted, disk full, constraint…). Surface it inline so the
  // user immediately knows their view is going to drift from the
  // DB on next refresh, instead of silently learning about it
  // when the bark notification arrives without a matching chat
  // row. We mark it as an error so the existing styling carries.
  if (msg.message) {
    chatMessages.value.push({ role: "error", content: msg.message });
    scrollChat();
  }
}

function handleMessagesCleared() {
  // The server dropped the persisted history for this conversation. Only the
  // rendered list goes — deliberately NOT the live turn or the dedup memory:
  //
  //   - streamingText / streamBuffers / isThinking belong to a turn that may
  //     still be running. Clearing history is not cancelling, and the run's
  //     output is persisted after the wipe, so it must still land.
  //   - knownMessageIds stays because the room's replay buffer can still hold
  //     deltas for pre-clear messages. Forgetting their ids would let a
  //     reconnect re-render rows the user just deleted.
  //   - the sync cursor stays for the same reason: it still truthfully means
  //     "everything up to here has been seen", and the rows behind it are gone
  //     from the DB, so backfill cannot resurrect them.
  chatMessages.value = [];
}

function handleCancelAck(msg: WSCancelAckFrame) {
  // Server confirmation that the listed pending prompts were dropped.
  // This is purely reconciliation: we strip the dropped rows from the
  // staging area (and from history for back-compat with tabs that
  // promoted before staging existed). The editor refill is NOT done
  // here — both cancel paths already recall their text client-side at
  // click time (broad cancel in cancelMessage, per-message recall in
  // cancelPendingPrompt), so refilling again on the ack would either
  // double-fill or clobber edits the user already started.
  if (msg.cancelled_prompts && msg.cancelled_prompts.length > 0) {
    const cancelledIds = new Set(msg.cancelled_prompts.map((p) => p.id).filter(Boolean));
    if (cancelledIds.size > 0) {
      pendingPrompts.value = pendingPrompts.value.filter((p) => !(p.id && cancelledIds.has(p.id)));
      chatMessages.value = chatMessages.value.filter(
        (m) => !(m.role === "user" && m.id && cancelledIds.has(m.id)),
      );
    }
  }
}

// Errors that reject one inbound client frame without touching whatever the
// agent is doing. Every string here is written by handler/terminal_ws.go
// straight back to the socket that sent the offending frame: the run in
// flight (if any) keeps streaming and its own `status: ready` still follows,
// so ending the turn here would unlock the composer mid-answer and drop the
// partial text. All other emitters (service/agent_stream.go — CLI failure,
// service/prompt_runner.go — account/ticket refusal) do end the turn.
//
// The frame carries no fatal/non-fatal flag, and message_id can't stand in
// for one: prompt_runner's turn-ending errors are unpersisted too. So the
// classification lives here, as an exact-match list kept deliberately narrow —
// anything unlisted is treated as turn-ending, which is the safe default
// (a stuck "thinking" composer is worse than an early unlock).
const NON_FATAL_ERROR_MESSAGES = new Set([
  "invalid message format",
  "unknown message type",
  "stale conversation subscription",
  "prompt dispatcher unavailable",
  "init required before input",
  "conversation not found",
  "invalid attachment metadata",
  "failed to enqueue prompt",
  "init required before answering a question",
  "invalid user question response",
  "the user question is no longer active",
]);

function handleError(msg: WSErrorFrame) {
  // A retry notice announces a wait inside a turn that is still running.
  if (msg.metadata?.notice?.kind === "transient_retry") {
    if (!msg.message_id || !knownMessageIds.has(msg.message_id)) {
      chatMessages.value.push({
        role: "error",
        content: noticeText(msg.metadata.notice) ?? msg.message,
        id: msg.message_id,
      });
      rememberMessageId(msg.message_id);
      scrollChat();
    }
    return;
  }
  isAnsweringUserQuestion.value = false;
  // Server sends MessageID when the error row was persisted to
  // the DB. Skip the visible push if we already have that id
  // (REST sync delivered it first). Persist-time-only errors
  // (handler-level rejections like "init required before input")
  // arrive without an id and always render.
  if (msg.message && (!msg.message_id || !knownMessageIds.has(msg.message_id))) {
    chatMessages.value.push({
      role: "error",
      content: noticeText(msg.metadata?.notice) ?? msg.message,
      id: msg.message_id,
    });
    rememberMessageId(msg.message_id);
    scrollChat();
  }
  if (!NON_FATAL_ERROR_MESSAGES.has(msg.message)) {
    // A fatal error ends the turn here; nothing is left for the fallback to
    // rescue, and letting it survive would only clear a later turn's state.
    clearTurnIdleFallback();
    const wasThinking = isThinking.value;
    isThinking.value = false;
    streamingText.value = "";
    streamBuffers.thinkingBuffer = "";
    if (wasThinking) {
      void notifyBrowserTask({
        kind: "failed",
        conversationId: msg.conversation_id ?? currentConversationId.value,
        eventId: msg.message_id ?? (msg.seq ? `seq-${msg.seq}` : `error-${msg.message}`),
      });
    }
  }
}

function handleSessionInfo(msg: WSSessionInfoFrame) {
  if (msg.content) pushActivity("info", msg.content);
}

function handleSessionWarning(msg: WSSessionWarningFrame) {
  if (msg.message && (!msg.message_id || !knownMessageIds.has(msg.message_id))) {
    chatMessages.value.push({
      role: "warning",
      content: noticeText(msg.metadata?.notice) ?? msg.message,
      id: msg.message_id,
    });
    rememberMessageId(msg.message_id);
    scrollChat();
  }
}

// createMessageDispatcher returns the WS onMessage handler. The facade
// registers it once at module load, mirroring the original inline
// `ws.onMessage((msg) => { switch … })` block.
export function createMessageDispatcher(): (msg: WSMessage) => void {
  return (msg: WSMessage) => {
    // Before anything else, so every handler below — and anything a handler
    // triggers — sees the stream row as complete as the buffers are.
    if (msg.type !== "delta") flushStreamWrites();
    if (!belongsToCurrentSubscription(msg)) return;
    // Ahead of the isKnownFrame guard on purpose: a frame this build has no
    // handler for still occupies a seq, so skipping it here would look like a
    // dropped frame and trigger a pointless re-sync.
    noteFrameSeq(msg);
    if (!isKnownFrame(msg)) return;
    switch (msg.type) {
      case "history_backfill":
        handleHistoryBackfill(msg);
        break;
      case "user_message":
        handleUserMessage(msg);
        break;
      case "delta":
        handleDelta(msg);
        break;
      case "result":
        handleResult(msg);
        break;
      case "user_question":
        handleUserQuestion(msg);
        break;
      case "user_question_resolved":
        handleUserQuestionResolved(msg);
        break;
      case "context_usage":
        handleContextUsage(msg);
        break;
      case "rate_limit":
        handleRateLimit(msg);
        break;
      case "system_init":
        handleSystemInit(msg);
        break;
      case "tool_use_start":
        handleToolUseStart(msg);
        break;
      case "tool_input_delta":
        handleToolInputDelta(msg);
        break;
      case "tool_result":
        handleToolResult(msg);
        break;
      case "thinking_delta":
        handleThinkingDelta(msg);
        break;
      case "init_ok":
        handleConversationActivity(msg);
        break;
      case "title_updated":
        handleTitleUpdated(msg);
        break;
      case "conversation_added":
        handleConversationAdded(msg);
        break;
      case "conversation_updated":
        handleConversationUpdated(msg);
        break;
      case "conversation_removed":
        handleConversationRemoved(msg);
        break;
      case "conversation_activity":
        handleConversationActivity(msg);
        break;
      case "attention_changed":
        bumpAttentionVersion();
        break;
      case "init_status":
        handleInitStatus(msg);
        break;
      case "status":
        handleStatus(msg);
        break;
      case "queue_status":
        handleQueueStatus(msg);
        break;
      case "input_ack":
        handleInputAck(msg);
        break;
      case "prompt_started":
        handlePromptStarted(msg);
        break;
      case "persist_failed":
        handlePersistFailed(msg);
        break;
      case "messages_cleared":
        handleMessagesCleared();
        break;
      case "cancel_ack":
        handleCancelAck(msg);
        break;
      case "error":
        handleError(msg);
        break;
      case "session_info":
        handleSessionInfo(msg);
        break;
      case "session_warning":
        handleSessionWarning(msg);
        break;
    }
  };
}
