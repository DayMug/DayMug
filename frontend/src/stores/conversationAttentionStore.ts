// Conversations that want their owner back: the latest turn finished or
// failed, or it stopped on a question, and the user has not opened it since.
// The server owns the flag (it survives a locked phone or a closed tab); this
// store mirrors GET /api/conversation-attention. Blue = finished, yellow =
// waiting on the user's answer, red = failed.
import { computed, ref } from "vue";
import type { ConversationAttentionRow } from "@/composables/types/chat";

export type ConversationAttention = "done" | "waiting" | "error";

export const attentionRows = ref<ConversationAttentionRow[]>([]);
// Titles of the user's conversations that are running right now. The global
// activity snapshot carries no titles, so the task feed names live work from
// here.
export const liveConversationTitles = ref<Record<string, string>>({});
// Bumped by the server's attention_changed frame (another tab opened a
// conversation) so the app shell refetches.
export const attentionVersion = ref(0);

export function applyAttention(rows: ConversationAttentionRow[], titles: Record<string, string>) {
  attentionRows.value = rows;
  liveConversationTitles.value = titles;
}

// Drops the flag locally ahead of the server round-trip, so opening a
// conversation clears its badge immediately.
export function dropAttention(conversationId: string) {
  if (!attentionRows.value.some((row) => row.conversation_id === conversationId)) return;
  attentionRows.value = attentionRows.value.filter((row) => row.conversation_id !== conversationId);
}

export function bumpAttentionVersion() {
  attentionVersion.value += 1;
}

export function resetConversationAttentionStore() {
  attentionRows.value = [];
  liveConversationTitles.value = {};
  attentionVersion.value = 0;
}

// An agent shows its most urgent conversation: any failure outranks a pending
// question, which outranks plain finished work.
const URGENCY: Record<ConversationAttention, number> = { done: 0, waiting: 1, error: 2 };

export const conversationAttention = computed(() => {
  const out: Record<string, ConversationAttention> = {};
  for (const row of attentionRows.value) out[row.conversation_id] = row.state;
  return out;
});

export const agentAttention = computed(() => {
  const out: Record<string, ConversationAttention> = {};
  for (const row of attentionRows.value) {
    const current = out[row.agent_id];
    if (!current || URGENCY[row.state] > URGENCY[current]) out[row.agent_id] = row.state;
  }
  return out;
});
