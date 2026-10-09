// Global singleton store: the staging area of prompts the user has sent but
// the dispatcher hasn't started yet, plus the account-pool queue metrics
// rendered next to them. See src/stores/index.ts for the conventions every
// store in this directory follows.
import { ref } from "vue";

import type { MessageAttachment, MessageSender } from "@/composables/useWebSocket";

// PendingPrompt represents a user message the dispatcher has accepted but
// not yet started running. The staging-area UI renders these (NOT in
// chatMessages) so a user can see what they sent, watch its position in
// the queue, and click to recall it before the worker picks it up. The
// `prompt_started` event from the dispatcher worker is the cue to move a
// pending entry into chatMessages as a regular user turn.
export interface PendingPrompt {
  // Persisted DB id once input_ack lands. Empty for the brief window
  // between optimistic push and ack, identified locally via clientKey.
  id?: string;
  content: string;
  attachments?: MessageAttachment[];
  sender?: MessageSender;
  // Stable client-side handle so the input_ack handler can find this
  // optimistic entry to stamp the canonical id on. Random per send so
  // races between sibling tabs don't collide.
  clientKey: string;
  // Pool position when the head-of-queue is parked behind another
  // account-mate. Only meaningful for the entry at index 0 (the one
  // the dispatcher will claim next); siblings derive their order from
  // their array index. Null = not pool-throttled.
  poolPosition?: number | null;
  // Complete account-pool breakdown for the status label. poolAhead counts
  // active jobs plus earlier waiters; poolRunning is the active subset.
  poolAhead?: number | null;
  poolRunning?: number | null;
  // True for a prompt that arrived over an attached IM bot and is waiting for
  // an account slot. It is NOT recallable: the text was typed in Slack or
  // WeChat, so there is nothing to pull back into this composer, and the row
  // is invisible to the web dispatcher's pending queue (the backend marks it
  // "pool_queued", never "pending") so the per-message recall endpoint would
  // not find it either. Its × cancels the run instead.
  fromIM?: boolean;
}

// PROMPT_QUEUE_STATUS_IM is the persisted marker for an IM prompt waiting on
// an account slot — store.QueueStatusPoolQueued on the Go side.
export const PROMPT_QUEUE_STATUS_IM = "pool_queued";

// isStagedPromptStatus reports whether a persisted user row belongs in the
// staging area rather than the transcript. Two different queues land here:
// the web dispatcher's own FIFO ("pending") and an IM turn waiting for an
// account slot. "processing" is deliberately excluded — that prompt is running.
export function isStagedPromptStatus(status?: string): boolean {
  return status === "pending" || status === PROMPT_QUEUE_STATUS_IM;
}

// Shared by every useChat() caller.
export const pendingPrompts = ref<PendingPrompt[]>([]);

let pendingPromptSeq = 0;
// nextPendingPromptSeq increments and returns the client-key sequence.
// Wrapped in a function (rather than an exported `let`) because ESM
// bindings can't be assigned from importing modules.
export function nextPendingPromptSeq(): number {
  return ++pendingPromptSeq;
}

// Ids of prompts whose `prompt_started` event arrived before the matching
// peer-tab `user_message` echo. The two events come from different
// goroutines (input handler vs dispatcher worker) so although they are
// serialized through the broadcaster room mutex, the worker can win the
// race when the pool slot is granted instantly. When that happens the
// `user_message` handler must push straight to chatMessages instead of
// stranding the entry in the staging area waiting for a `prompt_started`
// that already came and went.
export const startedPromptIds = new Set<string>();

// Queue position for the current conversation. Non-null while the server
// has the in-flight prompt queued behind another active job on the same
// Claude account (cross-account FIFO via service.Pool). The integer is
// its one-based position among jobs waiting for an account slot. Active
// jobs are excluded ("1" = first task beyond the concurrency limit).
// Cleared on status=thinking once the
// pool grants this conversation a slot.
export const queuePosition = ref<number | null>(null);
export const queueAhead = ref<number | null>(null);
export const queueRunning = ref<number | null>(null);

export function clearQueueMetrics() {
  queuePosition.value = null;
  queueAhead.value = null;
  queueRunning.value = null;
}

export function resetChatQueueStore() {
  pendingPrompts.value = [];
  startedPromptIds.clear();
  pendingPromptSeq = 0;
  clearQueueMetrics();
}
