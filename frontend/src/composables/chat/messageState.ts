// Mappers, fetchers and scroll plumbing for the chat transcript. The state
// they operate on lives in @/stores/chatMessageStore — this module holds the
// behaviour only.
import { formatToolInput, formatUsage, type FormattedUsage } from "@/lib/chatFormat";
import { noticeText } from "@/lib/notice";

import {
  HISTORY_PAGE_SIZE,
  chatMessages,
  cursors,
  hasMoreHistory,
  isLoadingMoreHistory,
  knownMessageIds,
  rememberMessageId,
  type ChatMessage,
} from "@/stores/chatMessageStore";
import { currentConversationId, isWorkDirLocked } from "@/stores/activeConversationStore";
import {
  isStagedPromptStatus,
  pendingPrompts,
  PROMPT_QUEUE_STATUS_IM,
} from "@/stores/chatQueueStore";

import type {
  AssistantMetadata,
  MessageAttachment,
  MessageSender,
  WireContent,
} from "@/lib/wsProtocol";
import { fetchMessages } from "../useApi";
import { WORK_DIR_HINT_PREFIX } from "./workDir";

export type { ChatMessage } from "@/stores/chatMessageStore";

// Scroll helpers
//
// `immediate=true` jumps to the bottom unconditionally — used when the user
// has just opened/entered the chat surface (mount, conversation switch, mobile
// tab toggle). The default (`false`) keeps the live-streaming behaviour: only
// auto-scroll when the reader is already at the tail, so manual scrolling up
// to read older content isn't yanked back when new tokens arrive.
let chatScrollFn: ((immediate?: boolean) => void) | null = null;

export function setChatScrollFn(fn: (immediate?: boolean) => void) {
  chatScrollFn = fn;
}

export function scrollChat(immediate = false) {
  chatScrollFn?.(immediate);
}

export function pushActivity(
  type: "stream" | "tool" | "thinking" | "info" | "model",
  content: string,
  opts: { subagent?: boolean; toolStartedAt?: number; toolCallId?: string } = {},
) {
  const entry: ChatMessage = { role: "activity", content, activityType: type };
  if (opts.subagent) entry.subagent = true;
  if (opts.toolStartedAt !== undefined) entry.toolStartedAt = opts.toolStartedAt;
  if (opts.toolCallId) entry.toolCallId = opts.toolCallId;
  chatMessages.value.push(entry);
  scrollChat();
}

// WAKEUP_ORIGIN is the metadata.origin value the backend writes on a turn the
// agent started by itself once background work settled. Mirrors
// service.WakeupOrigin in Go; the two are compared as plain strings, so keep
// them spelled identically.
export const WAKEUP_ORIGIN = "background_wakeup";

// parseUsageFromMetadata turns the wire-shape `metadata.usage` object
// into the chip-ready { short, detail, input, output } via the shared
// formatUsage helper. Returns undefined when there's nothing to render
// — every other call site treats the absence of `usage` on a
// ChatMessage as "no chip", which is what we want.
export function parseUsageFromMetadata(
  metadata: AssistantMetadata | undefined,
): FormattedUsage | undefined {
  if (!metadata || !metadata.usage) return undefined;
  try {
    const formatted = formatUsage(JSON.stringify(metadata.usage));
    return formatted.short ? formatted : undefined;
  } catch {
    return undefined;
  }
}

// Wire shape of a tool event's JSON content — shared by persisted tool
// rows (REST /messages, history_backfill) and live tool_result WS frames.
// Only the fields the UI renders are declared; the agent CLIs attach
// more, which we deliberately ignore.
export interface ToolResultPayload {
  id?: string;
  name?: string;
  // Raw tool input object — only ever re-serialized via JSON.stringify
  // for the `[Name]\n  key: value` chip, so its inner shape is opaque.
  input?: unknown;
}

export function wireContentToString(content: WireContent): string {
  if (typeof content === "string") return content;
  if (content == null) return "";
  if (Array.isArray(content)) {
    const text = content
      .map((block) => {
        if (typeof block === "string") return block;
        if (!block || typeof block !== "object") return "";
        const record = block as Record<string, unknown>;
        if (typeof record.text === "string") return record.text;
        if (typeof record.content === "string") return record.content;
        return "";
      })
      .filter(Boolean)
      .join("\n\n");
    if (text) return text;
  }
  try {
    return JSON.stringify(content);
  } catch {
    return String(content);
  }
}

export function parseToolResultPayload(content: WireContent): ToolResultPayload | null {
  if (typeof content === "string") {
    try {
      return JSON.parse(content) as ToolResultPayload | null;
    } catch {
      return null;
    }
  }
  if (content && typeof content === "object") {
    return content as ToolResultPayload;
  }
  return null;
}

export function formatToolMessageContent(content: WireContent): string {
  const info = parseToolResultPayload(content);
  if (!info) return wireContentToString(content);
  const name = info.name || "tool";
  const input = info.input ? JSON.stringify(info.input) : "";
  return input ? `[${name}]\n${formatToolInput(input)}` : `[${name}]`;
}

function isSessionWarning(role: string, content: string, metadata?: AssistantMetadata): boolean {
  if (role !== "error") return false;
  if (metadata?.notice_type === "session_warning") return true;

  // Guardrail reminders persisted before notice_type existed. Keep this
  // deliberately narrow so ordinary errors that happen to mention usage
  // remain errors.
  return (
    content.startsWith("上一条消息触发的任务已达到") && content.includes("本条消息仍会正常执行")
  );
}

// Map a REST-shaped message (from /api/conversations/:id/messages or
// from a history_backfill WS frame) onto the local ChatMessage variant.
// The persisted id is preserved for cross-source dedup via
// knownMessageIds. Assistant rows also carry their persisted
// metadata.usage forward so the per-turn token chip renders from REST
// history alone, without any localStorage shadow.
export function restMessageToChatMessage(m: {
  id?: string;
  role: string;
  content: WireContent;
  created_at?: string;
  metadata?: AssistantMetadata;
}): ChatMessage {
  const content =
    noticeText(m.metadata?.notice) ??
    displayMessageContent(
      wireContentToString(m.content),
      m.metadata?.sender,
      m.metadata?.attachments,
    );
  if (m.role === "thinking") {
    return { id: m.id, role: "activity", content, activityType: "thinking" };
  }
  if (m.role === "tool") {
    return {
      id: m.id,
      role: "activity",
      content: formatToolMessageContent(m.content),
      activityType: "tool",
      toolDurationMs: m.metadata?.duration_ms,
      toolCompleted: true,
    };
  }
  const role = isSessionWarning(m.role, content, m.metadata) ? "warning" : m.role;
  const out: ChatMessage = { id: m.id, role: role as ChatMessage["role"], content };
  if (m.metadata?.attachments?.length) {
    out.attachments = m.metadata.attachments;
  }
  if (m.metadata?.sender) {
    out.sender = m.metadata.sender;
  }
  if (m.role === "assistant") {
    const usage = parseUsageFromMetadata(m.metadata);
    if (usage) out.usage = usage;
    if (m.metadata?.origin === WAKEUP_ORIGIN) out.wakeup = true;
  }
  // Only user / assistant bubbles surface a sent-at hint. Activity rows
  // are already filtered above; warning/error rows have no header to attach the
  // timestamp to, so leave created_at undefined for them.
  if ((m.role === "user" || m.role === "assistant") && m.created_at) {
    out.created_at = m.created_at;
  }
  return out;
}

// Remove transport-only framing from the text shown in the transcript. IM
// identity belongs in the message header, while uploaded paths belong in the
// attachment cards. The persisted/agent-facing content keeps both intact.
export function displayMessageContent(
  content: string,
  sender?: MessageSender,
  attachments?: Array<Pick<MessageAttachment, "path">>,
): string {
  const labels = [sender?.name, sender?.id].filter(
    (label, index, all): label is string => !!label && all.indexOf(label) === index,
  );
  for (const label of labels) {
    const framed = `[${label}]`;
    if (content === framed) return "";
    if (content.startsWith(`${framed}:`)) {
      content = content.slice(framed.length + 1).trimStart();
      break;
    }
  }

  const attachmentPaths = attachments?.map((attachment) => attachment.path).filter(Boolean) ?? [];
  if (attachmentPaths.length > 0) {
    let end = content.length;
    for (let i = attachmentPaths.length - 1; i >= 0; i--) {
      const head = content.slice(0, end).trimEnd();
      const tokenStart =
        Math.max(head.lastIndexOf(" "), head.lastIndexOf("\n"), head.lastIndexOf("\t")) + 1;
      const token = head.slice(tokenStart);
      const path = attachmentPaths[i];
      // Metadata carries a workspace-relative path, while the agent-facing
      // prompt may carry the absolute ref it needs to open. Match either form
      // exactly at the tail; anything else is ordinary user text and stays.
      if (token !== path && !token.endsWith(`/${path}`)) return content;
      end = tokenStart;
    }
    return content.slice(0, end).trimEnd();
  }
  return content;
}

// Workdir and model rows are synthetic conversation metadata, not persisted
// transcript entries. Keep them pinned ahead of every history page so loading
// older messages cannot move them into the middle of the conversation (where
// they would also split an otherwise-consecutive tool-call group).
function isConversationHeaderMessage(message: ChatMessage): boolean {
  if (message.id || message.role !== "activity") return false;
  if (message.activityType === "model") return true;
  return message.activityType === "info" && message.content.startsWith(WORK_DIR_HINT_PREFIX);
}

// Update the most recent "stream" activity on the requested track (parent or
// sub-agent). The two tracks must not collide: a sub-agent's text deltas
// during a Task/Agent call would otherwise overwrite the parent's stream
// activity (or vice versa). When the parent resumes streaming after a
// sub-agent has been talking, walk back past the sub-agent's stream to find
// the parent's existing one — pushing a new entry would orphan the old text
// and double-render the parent's reply.
export function updateLastStream(text: string, opts: { subagent?: boolean } = {}) {
  const wantSub = !!opts.subagent;
  const msgs = chatMessages.value;
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m.role !== "activity") break;
    if (m.activityType !== "stream") continue;
    if (!!m.subagent !== wantSub) continue;
    m.content = text;
    scrollChat();
    return;
  }
  const entry: ChatMessage = { role: "activity", content: text, activityType: "stream" };
  if (wantSub) entry.subagent = true;
  chatMessages.value.push(entry);
  scrollChat();
}

// claimTrailingThinking finds the most recent in-flight thinking
// activity on the parent track (same heuristic the live thinking_delta
// handler uses to know which row to mutate) and stamps it with the
// persisted id. Called when a `result` event arrives carrying
// ThinkingMessageID, so the row that's been visible all along becomes
// the canonical one — a future history_backfill / REST sync recognises
// it via knownMessageIds and won't add a duplicate. Returns true if it
// claimed a row, false if there was no trailing thinking to claim.
export function claimTrailingThinking(id: string): boolean {
  if (!id) return false;
  const msgs = chatMessages.value;
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m.role === "user") return false;
    if (m.role === "activity" && m.activityType === "thinking" && !m.subagent) {
      m.id = id;
      rememberMessageId(id);
      return true;
    }
  }
  return false;
}

// syncFromCursor pulls every message persisted after lastSyncedMessageId
// via REST and folds them into chatMessages with id-based dedup.
// Belt-and-suspenders for the WS history_backfill path: REST is
// stateless and unaffected by broadcaster room GC, so a message that
// was persisted while the WS was down (or that fell into a narrow race
// between persistResult and EndJob) still surfaces here. Safe to call
// concurrently with WS events — the dedup set keeps us idempotent.
export async function syncFromCursor() {
  const id = currentConversationId.value;
  if (!id) return;
  try {
    const newer = await fetchMessages(id, {
      limit: 500,
      afterId: cursors.lastSyncedMessageId,
    });
    if (id !== currentConversationId.value) return;
    for (const m of newer) {
      if (m.id && knownMessageIds.has(m.id)) continue;
      // A still-queued prompt belongs in the staging area, not the transcript.
      // Pushing it inline here made a gap-triggered resync render it as an
      // ordinary sent bubble, and prompt_started could no longer promote it —
      // knownMessageIds already had the id, so the handler no-ops.
      if (m.role === "user" && isStagedPromptStatus(m.queue_status)) {
        if (!pendingPrompts.value.some((p) => p.id === m.id)) {
          pendingPrompts.value.push({
            id: m.id,
            content: wireContentToString(m.content),
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
    if (newer.length > 0) scrollChat();
  } catch {
    // Best-effort sync; the next reconnect / event will retry.
  }
  // After the transcript has caught up, so a prompt promoted here lands in
  // front of the replies this sync just appended rather than behind them.
  await reconcileStagedPrompts();
}

// insertByCreatedAt splices a persisted row into the transcript at its
// chronological place instead of appending it. A prompt recovered late is the
// only row that arrives out of order — its own answer is usually already on
// screen — and appending it would print the question below the answer.
// Rows minted client-side (stream/tool activity) carry no created_at and are
// skipped: they can't move the insertion point, only rows with a real
// timestamp can.
function insertByCreatedAt(row: ChatMessage) {
  const at = Date.parse(row.created_at ?? "");
  if (!Number.isFinite(at)) {
    chatMessages.value.push(row);
    return;
  }
  const idx = chatMessages.value.findIndex((m) => {
    const t = Date.parse(m.created_at ?? "");
    return Number.isFinite(t) && t > at;
  });
  if (idx < 0) chatMessages.value.push(row);
  else chatMessages.value.splice(idx, 0, row);
}

// reconcileStagedPrompts retires staging-area cards the server has already
// moved on from.
//
// A staged card is otherwise only ever cleared by the live `prompt_started`
// frame, and nothing can replay that frame: `input_ack` already fed the row's
// id to rememberMessageId, so the reconnect cursor sits at or past it and
// every later history_backfill / syncFromCursor both starts after it and
// dedups it via knownMessageIds. Lose that one frame — a phone that sleeps
// through the claim, a broadcaster room GC'd mid-turn — and the card outlives
// the answer it asked for, sitting under a reply to a question the UI still
// calls "queued".
//
// The latest page is the source of truth for the rows we have staged. A card
// whose id is missing from that window is left alone: it's older than the
// window (nothing to conclude) or the row was deleted by a cancel, which
// cancel_ack already handles.
async function reconcileStagedPrompts() {
  const id = currentConversationId.value;
  const staged = pendingPrompts.value.filter((p) => p.id);
  if (!id || staged.length === 0) return;
  // Best-effort: a failed page leaves every card where it is and the next
  // recovery pass retries.
  const latest = await fetchMessages(id, { beforeId: "", limit: HISTORY_PAGE_SIZE }).catch(
    () => null,
  );
  if (!latest || id !== currentConversationId.value) return;
  const byId = new Map(latest.filter((m) => m.id).map((m) => [m.id, m]));
  let promoted = false;
  for (const p of staged) {
    const row = p.id ? byId.get(p.id) : undefined;
    if (!row || isStagedPromptStatus(row.queue_status)) continue;
    const idx = pendingPrompts.value.findIndex((q) => q.id === p.id);
    if (idx >= 0) pendingPrompts.value.splice(idx, 1);
    if (!chatMessages.value.some((m) => m.id === row.id)) {
      insertByCreatedAt(restMessageToChatMessage(row));
      promoted = true;
    }
  }
  if (promoted) scrollChat();
}

// Pull the next older page of history and prepend it to chatMessages.
// Caller (ChatPage) is responsible for snapshotting/restoring the scroll
// position around the call so the user's viewport stays anchored — we
// can't do it from here without coupling the composable to the DOM.
//
// Returns the number of messages spliced in, so the caller can decide
// whether DOM patching actually happened (zero means: nothing to do, no
// scroll work needed).
export async function loadMoreHistory(): Promise<number> {
  if (isLoadingMoreHistory.value) return 0;
  if (!hasMoreHistory.value) return 0;
  const id = currentConversationId.value;
  if (!id) return 0;
  const cursor = cursors.oldestLoadedMessageId;
  if (!cursor) return 0;

  isLoadingMoreHistory.value = true;
  try {
    const older = await fetchMessages(id, {
      limit: HISTORY_PAGE_SIZE,
      beforeId: cursor,
    });
    // The user may have switched conversations or another loadMore won
    // the race. Bail rather than splicing stale older-page entries into
    // a different conversation's transcript.
    if (id !== currentConversationId.value) return 0;
    if (cursor !== cursors.oldestLoadedMessageId) return 0;

    if (older.length === 0) {
      hasMoreHistory.value = false;
      return 0;
    }

    // A backfill or live frame can deliver these rows while the page request
    // is in flight. Dedup before mapping, including repeats within the page,
    // so history never acquires a second copy of an already-rendered row.
    const unseen = older.filter((m) => {
      if (m.id && knownMessageIds.has(m.id)) return false;
      if (m.id) knownMessageIds.add(m.id);
      return true;
    });

    // Pending rows in older pages route to the staging area, same as
    // the initial REST snapshot load — keeps the staging/history
    // contract consistent across paginated history.
    const olderInline = unseen.filter(
      (m) => !(m.role === "user" && isStagedPromptStatus(m.queue_status)),
    );
    const olderPending = unseen.filter(
      (m) => m.role === "user" && isStagedPromptStatus(m.queue_status),
    );
    const mapped = olderInline.map(restMessageToChatMessage);
    const conversationHeader = chatMessages.value.filter(isConversationHeaderMessage);
    const currentTranscript = chatMessages.value.filter(
      (message) => !isConversationHeaderMessage(message),
    );
    chatMessages.value = [...conversationHeader, ...mapped, ...currentTranscript];
    for (const p of olderPending) {
      if (!pendingPrompts.value.some((q) => q.id === p.id)) {
        pendingPrompts.value.unshift({
          id: p.id,
          content: wireContentToString(p.content),
          attachments: p.metadata?.attachments,
          sender: p.metadata?.sender,
          clientKey: `srv-${p.id}`,
          fromIM: p.queue_status === PROMPT_QUEUE_STATUS_IM,
        });
      }
    }
    cursors.oldestLoadedMessageId = older[0].id;
    if (older.length < HISTORY_PAGE_SIZE) {
      hasMoreHistory.value = false;
    }
    // Re-derive the workdir lock now that we've expanded what's loaded:
    // we may have just discovered a user message in older history (lock
    // stays on), or finished loading and confirmed there are none (so
    // unlock if hasMoreHistory has flipped to false).
    if (mapped.some((m) => m.role === "user")) {
      isWorkDirLocked.value = true;
    } else if (!hasMoreHistory.value && !chatMessages.value.some((m) => m.role === "user")) {
      isWorkDirLocked.value = false;
    }
    return mapped.length;
  } catch {
    return 0;
  } finally {
    isLoadingMoreHistory.value = false;
  }
}
