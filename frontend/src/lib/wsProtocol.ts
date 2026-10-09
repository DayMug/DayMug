// Wire protocol of the chat WebSocket (/api/terminal): every frame the Go
// backend emits, plus the runtime guards that decide whether a decoded frame
// or JSON-in-a-string payload is safe to hand to the dispatcher. Type-only
// imports keep this module free of runtime dependencies.
import type { Conversation } from "@/composables/apiTypes";
import type { RunningConversationActivity } from "@/stores/agentActivityStore";
import type { ContextUsage, RateLimitInfo } from "@/stores/chatContextStore";

// WireContent is deliberately `unknown` — it models the frames whose
// `content` really is polymorphic. store.Message.MarshalJSON emits a raw
// JSON value for tool rows (ToolContentForWire) and a plain string for
// everything else, so persisted rows and live `tool_result` frames can be
// either. Frames whose backend field is a Go `string` declare
// `content?: string` directly instead of reaching for this alias; use
// `wireContentToString` / `parseToolResultPayload` (messageState.ts) as the
// type guards at the consumption points.
export type WireContent = unknown;

// Fields the transport layer stamps onto frames rather than the emitter.
// `service.Broadcaster.scopeFrame` fills conversation_id/subscription_id on
// room broadcasts and the per-socket writeJSON fills them on direct
// replies — but only once a conversation is bound, and `service.UserHub`
// frames (conversation_* / conversation_activity) are never stamped at all.
// Hence both stay optional on every member.
interface WSFrameBase {
  conversation_id?: string;
  subscription_id?: string;
  // Per-room monotonic counter stamped by `service.Broadcaster.Broadcast`.
  // The room fanout is non-blocking by design — a slow client must never
  // stall the stream — so frames are silently dropped when our socket's send
  // buffer fills. A jump in `seq` is the only evidence that happened; see
  // `noteFrameSeq` in chat/messageDispatcher.ts.
  //
  // Absent on everything the broadcaster didn't stamp: direct per-socket
  // replies (init_ok, history_backfill, input_ack), UserHub fanout
  // (conversation_* / title_updated), and the cached context_usage tail
  // pushed on join. Those must be ignored for gap detection.
  seq?: number;
}

// Mixed into every frame routed through Broadcaster.Broadcast, i.e. every
// frame that can be resent from the in-flight replay buffer when a socket
// joins an already-busy conversation. Text handlers merge these with any
// locally-rendered prefix instead of blindly appending them.
interface WSReplayable {
  replay?: boolean;
}

// Mixed into the stream-event family, the only frames the backend tags with
// a sub-agent (Task / Agent tool) origin. The chat surface renders these on
// a separate, indented track and skips dedup logic that only applies to the
// parent's own stream.
interface WSSubagentScoped {
  subagent?: boolean;
}

// A persisted DB row exactly as store.Message.MarshalJSON emits it. Shared
// by history_backfill (`messages`) and cancel_ack (`cancelled_prompts`),
// which both carry []store.Message — the same shape the REST /messages
// endpoint returns, so consumers reuse their REST mapping logic.
export interface WSPersistedMessage {
  id: string;
  conversation_id: string;
  role: string;
  content: WireContent;
  created_at: string;
  // Only non-empty for user prompts still in the dispatcher pipeline:
  // "pending" (queued) or "processing" (a worker is running it).
  queue_status?: string;
  metadata?: AssistantMetadata;
}

// Incremental text chunks. `content` is a Go string on the wire; an empty
// chunk is dropped by omitempty, hence optional.
export interface WSTextStreamFrame extends WSFrameBase, WSReplayable, WSSubagentScoped {
  type: "delta" | "thinking_delta" | "tool_input_delta";
  content?: string;
}

// Structured stream events. `content` is a Go string that *contains* JSON —
// the backend does not embed it as a nested object — so consumers must
// JSON.parse it after stringifying.
export interface WSJSONStreamFrame extends WSFrameBase, WSReplayable, WSSubagentScoped {
  type: "system_init" | "tool_use_start" | "context_usage" | "rate_limit";
  content?: string;
  // Present only on tool_use_start. The backend timestamp lets a replayed
  // in-flight call resume from its original start instead of restarting at 0s.
  tool_started_at?: number;
}

export interface WSToolResultFrame extends WSFrameBase, WSReplayable, WSSubagentScoped {
  type: "tool_result";
  // Raw JSON when the compacted tool payload parses as JSON, a plain
  // string otherwise (store.ToolContentForWire).
  content?: WireContent;
  // Absent for sub-agent tool results (never persisted) and when the tool
  // row failed to save — the backend emits `persist_failed` in that case.
  message_id?: string;
  tool_duration_ms?: number;
}

export interface WSResultFrame extends WSFrameBase, WSReplayable, WSSubagentScoped {
  type: "result";
  // Absent for a silent turn: empty assistant text is dropped by omitempty.
  content?: string;
  // Absent when no assistant row was persisted — empty text, a failed
  // save, or a sub-agent frame (which skips the persistence enrichment).
  message_id?: string;
  // The persisted id of the thinking row (if any) that goes alongside the
  // assistant reply. Lets the client claim its already-rendered in-flight
  // thinking activity as the canonical row so a later REST sync / backfill
  // won't re-add it. Absent when the turn produced no thinking block.
  thinking_message_id?: string;
  // Set on live non-subagent results and on the IM bridge paths; absent on
  // /compact results and on sub-agent frames.
  metadata?: AssistantMetadata;
}

export interface UserQuestionOption {
  label: string;
  description?: string;
}

export interface UserQuestion {
  id: string;
  header: string;
  question: string;
  multi_select?: boolean;
  allow_other?: boolean;
  secret?: boolean;
  options?: UserQuestionOption[];
}

export interface UserQuestionRequest {
  request_id: string;
  questions: UserQuestion[];
  auto_resolution_ms?: number;
}

export interface WSUserQuestionFrame extends WSFrameBase, WSReplayable {
  type: "user_question";
  content?: string;
}

export interface WSUserQuestionResolvedFrame extends WSFrameBase, WSReplayable {
  type: "user_question_resolved";
  request_id: string;
}

export interface WSUserMessageFrame extends WSFrameBase, WSReplayable {
  type: "user_message";
  content?: string;
  // Always present: the echo is only emitted after the row is persisted.
  message_id: string;
  // Web prompts carry it only with upload attachments; IM-mirrored prompts
  // always carry the sender identity.
  metadata?: AssistantMetadata;
  // Mirrors the persisted row so a live client stages the message exactly as
  // it would after reloading it over REST. Only the IM bridge sets it
  // ("pool_queued"): an IM prompt is echoed the moment it is stored, which is
  // before it has an account slot.
  queue_status?: string;
}

export interface WSInputAckFrame extends WSFrameBase {
  type: "input_ack";
  // Always present: only emitted after SavePrompt succeeded.
  message_id: string;
}

export interface WSPromptStartedFrame extends WSFrameBase, WSReplayable {
  type: "prompt_started";
  // Always present: the id of the prompt row the worker just claimed.
  message_id: string;
}

export interface WSQueueStatusFrame extends WSFrameBase, WSReplayable {
  type: "queue_status";
  status: "queued";
  // 1-based position among tasks waiting for a per-account slot; active
  // tasks are excluded so the value is always >= 1.
  queue_position?: number;
  // Complete queue breakdown supplied alongside queue_position. Backend
  // sends these as *int, so a genuine 0 survives the wire and both are
  // always present on this frame.
  queue_ahead: number;
  queue_running: number;
}

export interface WSStatusFrame extends WSFrameBase, WSReplayable {
  type: "status";
  status: "ready" | "thinking" | "shutdown";
}

export interface WSInitStatusFrame extends WSFrameBase {
  type: "init_status";
  // Authoritative snapshot right after init_ok; never "shutdown".
  status: "ready" | "thinking";
  conversation_id: string;
}

// The global job snapshot. All arrays are omitempty on the Go side, so an
// empty state arrives as absent fields rather than explicit empty arrays.
interface WSActivitySnapshot {
  running_agent_ids?: string[];
  running_conversation_ids?: string[];
  running_conversations?: RunningConversationActivity[];
  queued_conversations?: RunningConversationActivity[];
  waiting_conversations?: RunningConversationActivity[];
  failed_conversation_ids?: string[];
}

export interface WSInitOkFrame extends WSFrameBase, WSActivitySnapshot {
  type: "init_ok";
}

export interface WSConversationActivityFrame extends WSFrameBase, WSActivitySnapshot {
  type: "conversation_activity";
}

export interface WSHistoryBackfillFrame extends WSFrameBase {
  type: "history_backfill";
  conversation_id: string;
  // Persisted messages that landed while the client was offline. The
  // backend skips the frame entirely when there is nothing to send, so the
  // array is always present and non-empty.
  messages: WSPersistedMessage[];
}

export interface WSCancelAckFrame extends WSFrameBase {
  type: "cancel_ack";
  conversation_id: string;
  // The prompts the dispatcher dropped from its pending queue, in FIFO
  // order. Absent when the cancel matched nothing or errored.
  cancelled_prompts?: WSPersistedMessage[];
}

export interface WSErrorFrame extends WSFrameBase, WSReplayable {
  type: "error";
  // Always non-empty on every emitter.
  message: string;
  // Only set when the error row was persisted to the DB. Handler-level
  // rejections ("init required before input", …) arrive without one.
  message_id?: string;
  // Carries `notice` when the server wrote this row as a structured notice.
  metadata?: AssistantMetadata;
}

export interface WSSessionInfoFrame extends WSFrameBase, WSReplayable {
  type: "session_info";
  content?: string;
}

export interface WSSessionWarningFrame extends WSFrameBase, WSReplayable {
  type: "session_warning";
  message: string;
  message_id?: string;
  metadata?: AssistantMetadata;
}

export interface WSPersistFailedFrame extends WSFrameBase, WSReplayable {
  type: "persist_failed";
  conversation_id: string;
  message: string;
}

export interface WSTitleUpdatedFrame extends WSFrameBase {
  type: "title_updated";
  conversation_id: string;
  title: string;
}

// DELETE /conversations/:id/messages wiped the history. Sent to every client
// in the conversation room (including the one that asked) so peer tabs drop
// the body they are still showing. Deliberately not replayable: buffering it
// would let a stale wipe replay over messages sent after the clear.
export interface WSMessagesClearedFrame extends WSFrameBase {
  type: "messages_cleared";
  conversation_id: string;
}

// The backend marshals the full store.Conversation row (minus the share
// token) so peer tabs can splice it into their session list without an
// extra REST round-trip — reuse the REST row type rather than declaring a
// subset that silently drops fields like pinned/shared/account_name.
//
// `conversation_id` is inherited as optional on purpose: the cron
// scheduler's conversation_added frame omits it entirely and only carries
// the nested row, so consumers must read the id off `conversation.id`.
export interface WSConversationAddedFrame extends WSFrameBase {
  type: "conversation_added";
  conversation: Conversation;
}

export interface WSConversationUpdatedFrame extends WSFrameBase {
  type: "conversation_updated";
  conversation: Conversation;
}

export interface WSConversationRemovedFrame extends WSFrameBase {
  type: "conversation_removed";
  conversation_id: string;
}

// Another tab of the same user opened a conversation and cleared its
// attention flag.
export interface WSAttentionChangedFrame extends WSFrameBase {
  type: "attention_changed";
  conversation_id?: string;
}

// Every frame type the Go backend emits today. Keep in lockstep with
// service.ServerMessage's emitters.
export type WSKnownMessage =
  | WSTextStreamFrame
  | WSJSONStreamFrame
  | WSToolResultFrame
  | WSResultFrame
  | WSUserQuestionFrame
  | WSUserQuestionResolvedFrame
  | WSUserMessageFrame
  | WSInputAckFrame
  | WSPromptStartedFrame
  | WSQueueStatusFrame
  | WSStatusFrame
  | WSInitStatusFrame
  | WSInitOkFrame
  | WSConversationActivityFrame
  | WSAttentionChangedFrame
  | WSHistoryBackfillFrame
  | WSCancelAckFrame
  | WSErrorFrame
  | WSSessionInfoFrame
  | WSSessionWarningFrame
  | WSPersistFailedFrame
  | WSTitleUpdatedFrame
  | WSMessagesClearedFrame
  | WSConversationAddedFrame
  | WSConversationUpdatedFrame
  | WSConversationRemovedFrame;

export type WSMessageType = WSKnownMessage["type"];

// Forward compatibility: a newer backend may broadcast a frame type this
// build has never heard of. Modelling it explicitly (rather than widening
// `type` to `string` on every member) keeps the discriminated union sound
// while preserving the historical behaviour — unknown types fall through
// the dispatcher untouched.
export interface WSUnknownFrame extends WSFrameBase {
  type: string;
}

export type WSMessage = WSKnownMessage | WSUnknownFrame;

export interface AssistantMetadata {
  model?: string;
  // Set when the provider started this turn on its own — today only
  // "background_wakeup", a turn produced once background work a previous
  // turn armed finally settled. Absent on an ordinary prompted reply.
  origin?: string;
  // The raw usage object claude reports — same shape that formatUsage
  // already knows how to consume. Kept generic so a future field
  // addition is wire-compatible without touching this type.
  usage?: Record<string, unknown>;
  // Uploaded chat images and inbound IM images are persisted under the
  // serving agent's work_dir and exposed through the authenticated file-read
  // endpoint. The same shape travels over REST history and live events.
  attachments?: MessageAttachment[];
  // Present on messages mirrored from an attached IM bot. Keeping the
  // platform identity structured lets the web transcript distinguish
  // multiple humans in one Slack / Feishu thread instead of labelling every
  // role=user row as the signed-in web user.
  sender?: MessageSender;
  // Present on persisted tool rows. REST/history frames use one metadata
  // field for every message role, despite this interface's legacy name.
  duration_ms?: number;
  // Non-fatal transcript notices are stored in error-compatible DB rows,
  // but this marker lets history restore their warning presentation.
  notice_type?: "session_warning";
  // Structured form of a server-authored notice; rendered in the viewer's
  // locale instead of the row's content.
  notice?: TranscriptNotice;
}

export interface TranscriptNotice {
  kind: "task_guardrail" | "transient_retry" | (string & {});
  // task_guardrail
  triggers?: string[];
  model_calls?: number;
  tool_calls?: number;
  recorded_cost_usd?: number;
  // transient_retry
  delay_seconds?: number;
  max_attempts?: number;
}

export interface MessageSender {
  platform: string;
  id?: string;
  name?: string;
}

export interface MessageAttachment {
  name: string;
  mime: string;
  path: string;
  url: string;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isOptional(v: unknown, type: "string" | "number" | "boolean"): boolean {
  return v === undefined || typeof v === type;
}

// isWSFrame is the gate in front of the dispatcher: anything that decoded but
// isn't an object with a string `type` can't be routed, and letting it through
// used to crash inside the handler instead.
export function isWSFrame(v: unknown): v is WSMessage {
  return isRecord(v) && typeof v.type === "string";
}

export function isContextUsage(v: unknown): v is ContextUsage {
  return (
    isRecord(v) &&
    typeof v.used === "number" &&
    typeof v.total === "number" &&
    isOptional(v.input_tokens, "number") &&
    isOptional(v.cache_read, "number") &&
    isOptional(v.cache_creation, "number")
  );
}

// Structural check only. Which windows the badge shows is the dispatcher's
// call: Anthropic can report windows this build doesn't render, and those are
// skipped quietly rather than treated as malformed.
export function isRateLimitInfo(v: unknown): v is RateLimitInfo {
  return isRecord(v) && typeof v.type === "string" && typeof v.resets_at === "number";
}

// Wire shape of a system_init frame's JSON content. The CLIs report more
// (cwd, permission mode, …) but the UI only surfaces model + tool count.
export interface SystemInitPayload {
  model?: string;
  tools?: string[];
}

export function isSystemInitPayload(v: unknown): v is SystemInitPayload {
  return (
    isRecord(v) &&
    isOptional(v.model, "string") &&
    (v.tools === undefined || Array.isArray(v.tools))
  );
}

// Wire shape of a tool_use_start frame's JSON content. Claude streams its
// input separately, while Codex includes the shell command on the start frame.
export interface ToolUseStartPayload {
  id?: string;
  name?: string;
  command?: unknown;
}

export function isToolUseStartPayload(v: unknown): v is ToolUseStartPayload {
  return isRecord(v) && isOptional(v.id, "string") && isOptional(v.name, "string");
}

export function isUserQuestionRequest(v: unknown): v is UserQuestionRequest {
  return (
    isRecord(v) &&
    typeof v.request_id === "string" &&
    v.request_id !== "" &&
    Array.isArray(v.questions) &&
    v.questions.length > 0 &&
    v.questions.every(isRecord)
  );
}

// parseWirePayload decodes the JSON string several stream frames carry in
// `content` and checks its shape. A payload that fails either step is logged
// and dropped — one bad frame from a provider must not take the stream down,
// but it shouldn't vanish without a trace either.
export function parseWirePayload<T>(
  frameType: string,
  raw: string,
  guard: (v: unknown) => v is T,
): T | null {
  let decoded: unknown;
  try {
    decoded = JSON.parse(raw);
  } catch (err) {
    console.error(`[ws] "${frameType}" payload is not JSON; dropped`, err);
    return null;
  }
  if (!guard(decoded)) {
    console.error(`[ws] "${frameType}" payload has an unexpected shape; dropped`, decoded);
    return null;
  }
  return decoded;
}
