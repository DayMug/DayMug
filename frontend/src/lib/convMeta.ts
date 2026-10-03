// Per-conversation localStorage cache: last-seen model and context usage.
// Lets the chat header render immediately on reload before the next WS
// event arrives.
//
// This module owns the storage shape and the load/save plumbing only; the
// stateful pieces (persistConvMeta) live in useChat where they have access
// to currentModel / contextUsage / currentConversationId.

export const META_STORAGE_KEY = "daymug-conv-meta-v1";

export interface ConvMetaContextUsage {
  used: number;
  total: number;
  input_tokens?: number;
  cache_read?: number;
  cache_creation?: number;
}

export interface ConvMeta {
  model: string;
  contextUsage: ConvMetaContextUsage | null;
  // Pre-formatted "Model: X | Tools: N available" line shown at the top of
  // the chat. Persisted so a refresh re-shows it — system_init only fires
  // when a fresh claude session boots, so for an already-running session
  // the pill would otherwise vanish on reload.
  modelInfo?: string;
  // ID of the last persisted (non-activity) message at the moment the user
  // invoked clear-context. Used to re-render the "context cleared" divider
  // (and keep the inline trigger hidden) after a refresh: if this id still
  // matches the tail of the message list, the divider belongs at the end;
  // once a newer message lands, the marker is stale and we drop it.
  contextClearedAfterMessageId?: string;
}

export function loadConvMetaMap(): Record<string, ConvMeta> {
  try {
    const raw = localStorage.getItem(META_STORAGE_KEY);
    return raw ? (JSON.parse(raw) as Record<string, ConvMeta>) : {};
  } catch {
    return {};
  }
}

export function saveConvMetaMap(map: Record<string, ConvMeta>) {
  try {
    localStorage.setItem(META_STORAGE_KEY, JSON.stringify(map));
  } catch {
    // Quota / private mode: ignore.
  }
}
