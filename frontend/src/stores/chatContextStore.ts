// Global singleton store: the context-window gauge and the account-wide
// rate-limit badge. The actions that mutate it against the network
// (clearContext) and the localStorage mirror (persistConvMeta) live in
// composables/chat/contextTracking.ts. See src/stores/index.ts for the
// conventions every store in this directory follows.
import { computed, ref } from "vue";

import type { FormattedUsage } from "@/lib/chatFormat";

import { chatMessages } from "./chatMessageStore";

// `used` and `total` drive the bar; the optional fields surface in the
// tooltip so users can see which slice of context is fresh input vs cache
// reuse. They're omitted from older messages (and from intra-turn live
// updates that fall back to a previous total), so always treat them as
// possibly-undefined.
export type ContextUsage = {
  used: number;
  total: number;
  input_tokens?: number;
  cache_read?: number;
  cache_creation?: number;
};
export const contextUsage = ref<ContextUsage | null>(null);

// ID of the last persisted message at the moment clear-context was invoked.
// Persisted via convMeta so the "context cleared" divider survives a page
// refresh — without it, the chip would only live in the in-memory
// chatMessages array and the inline "clear context" button would
// immediately reappear at the bottom of the thread (since the divider tail
// check no longer matches). Empty string when no pending divider applies.
export const contextClearedAfterMessageId = ref<string>("");

// Rate-limit state for the active conversation's account. Claude emits a
// rate_limit_event only at process start and status transitions (allowed →
// warning → blocked); codex's app-server pushes account/rateLimits/updated
// while a turn runs. Frames are sparse and per-window, so we hold the most
// recently seen frame for each window in a single map. lib/rateLimitCache
// mirrors it per provider+account so a conversation with no turn yet can
// show the account's last reading.
export type RateLimitWindow = "five_hour" | "seven_day";
export type RateLimitInfo = {
  type: RateLimitWindow;
  // Only claude's headline window carries one; codex frames and claude's
  // secondary windows leave it out.
  status?: string;
  resets_at: number; // Unix epoch seconds
  utilization?: number; // percent of the window spent, 0–100
  overage_status?: string;
  is_using_overage?: boolean;
  // Unix epoch seconds the reading was observed: stamped client-side for live
  // frames, sent by the server when it replays its remembered reading on join.
  observed_at?: number;
};
export const rateLimits = ref<Partial<Record<RateLimitWindow, RateLimitInfo>>>({});

export const contextPercent = computed(() => {
  if (!contextUsage.value || !contextUsage.value.total) return 0;
  return Math.min(
    100,
    Math.max(0, Math.round((contextUsage.value.used / contextUsage.value.total) * 100)),
  );
});

// Derived live from the latest assistant row that carries a parsed
// usage payload. Drives the composer's usage summary, and stays correct across refreshes because the
// numbers ride along on store.Message.Metadata (no localStorage shadow,
// no separate ref to forget to clear). Returns null when no assistant
// turn has reported usage yet for the active conversation.
// Returns the full FormattedUsage of the most recent assistant turn that
// reported one. Exposes the whole shape (not just input/output) so the
// header's context bar can surface the same "I/O … | CR … | CW … | Cost
// … | Turns …" summary on hover that the per-message chip already shows.
export const lastUsage = computed<FormattedUsage | null>(() => {
  for (let i = chatMessages.value.length - 1; i >= 0; i--) {
    const m = chatMessages.value[i];
    if (m.role !== "assistant" || !m.usage) continue;
    if (m.usage.input == null || m.usage.output == null) continue;
    return m.usage;
  }
  return null;
});

export function resetChatContextStore() {
  contextUsage.value = null;
  contextClearedAfterMessageId.value = "";
  rateLimits.value = {};
}
