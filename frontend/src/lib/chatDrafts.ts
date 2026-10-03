// Per-conversation unsent-draft cache: persists whatever the user has typed
// into the composer but hasn't sent yet, keyed by conversation id. Restored
// when the user switches back to the conversation (page reload or tab
// switch).
//
// Bounded by three independent rules so the store can't grow unbounded:
//   1. Empty drafts are deleted, not stored — sending or clearing the
//      composer removes the entry entirely.
//   2. At most MAX_ENTRIES drafts are kept; on overflow the oldest by
//      `updatedAt` are dropped (LRU).
//   3. Individual drafts are truncated to MAX_TEXT_LEN before being saved
//      so a single runaway paste can't blow the localStorage quota.
//
// Storage shape is a single JSON object under STORAGE_KEY: a map from
// conversation id to { text, updatedAt }. Matches the convMeta.ts pattern.

export const DRAFTS_STORAGE_KEY = "daymug-chat-drafts-v1";
export const MAX_ENTRIES = 50;
export const MAX_TEXT_LEN = 32 * 1024;

export interface DraftEntry {
  text: string;
  updatedAt: number;
}

export type DraftMap = Record<string, DraftEntry>;

function readAll(): DraftMap {
  try {
    const raw = localStorage.getItem(DRAFTS_STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object") return parsed as DraftMap;
  } catch {
    // Malformed payload or unavailable storage — start fresh.
  }
  return {};
}

function writeAll(map: DraftMap): void {
  try {
    localStorage.setItem(DRAFTS_STORAGE_KEY, JSON.stringify(map));
  } catch {
    // Quota / private mode: ignore (best-effort persistence).
  }
}

export function loadDraft(conversationId: string): string {
  if (!conversationId) return "";
  const map = readAll();
  return map[conversationId]?.text ?? "";
}

export function saveDraft(conversationId: string, text: string): void {
  if (!conversationId) return;
  const map = readAll();
  if (!text) {
    if (!(conversationId in map)) return;
    delete map[conversationId];
    writeAll(map);
    return;
  }
  const trimmed = text.length > MAX_TEXT_LEN ? text.slice(0, MAX_TEXT_LEN) : text;
  map[conversationId] = { text: trimmed, updatedAt: Date.now() };
  const entries = Object.entries(map);
  if (entries.length > MAX_ENTRIES) {
    entries.sort((a, b) => b[1].updatedAt - a[1].updatedAt);
    const kept: DraftMap = {};
    for (const [id, v] of entries.slice(0, MAX_ENTRIES)) kept[id] = v;
    writeAll(kept);
    return;
  }
  writeAll(map);
}

export function clearDraft(conversationId: string): void {
  if (!conversationId) return;
  const map = readAll();
  if (!(conversationId in map)) return;
  delete map[conversationId];
  writeAll(map);
}
