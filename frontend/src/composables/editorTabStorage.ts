// Persistence for the workbench's open-editor-tab list.
//
// sessionStorage, not localStorage: a restored tab set belongs to the browser
// tab it was opened in, and surviving a browser restart would mostly mean
// resurrecting paths the agent has since moved.
//
// One key for every scope and user, not one key per user. The per-user form
// this replaced never reclaimed anything: delete an agent and its key sat in
// storage forever, because nothing was left that knew to remove it.

export const TABS_STORAGE_KEY = "daymug.editor.tabs.v1";
export const TABS_LRU_LIMIT = 20;
export const TABS_MAX_AGE_MS = 14 * 24 * 60 * 60 * 1000;
const SCHEMA_VERSION = 1;

interface TabEntry {
  k: string;
  paths: string[];
  ts: number;
}

// The project's ESLint env list covers localStorage but not sessionStorage, so
// it is reached through globalThis the same way TextEncoder is elsewhere.
// Structurally typed rather than as `Storage` for the same reason — the DOM
// type's name is not in the env list either.
interface TabStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

function sessionStore(): TabStore | null {
  return (globalThis as unknown as { sessionStorage?: TabStore }).sessionStorage ?? null;
}

export function tabScopeKey(scope: string, userId: string): string {
  return `${scope}:${userId}`;
}

function isTabEntry(value: unknown): value is TabEntry {
  if (typeof value !== "object" || value === null) return false;
  const e = value as Record<string, unknown>;
  return (
    typeof e.k === "string" &&
    Array.isArray(e.paths) &&
    e.paths.every((p) => typeof p === "string") &&
    typeof e.ts === "number"
  );
}

function readAll(): TabEntry[] {
  try {
    const raw = sessionStore()?.getItem(TABS_STORAGE_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return [];
    const blob = parsed as { v?: unknown; entries?: unknown };
    if (blob.v !== SCHEMA_VERSION || !Array.isArray(blob.entries)) return [];
    return blob.entries.filter(isTabEntry);
  } catch {
    // Corrupt or unavailable storage: start with no tabs.
    return [];
  }
}

export function loadTabs(scope: string, userId: string): string[] {
  return readAll().find((e) => e.k === tabScopeKey(scope, userId))?.paths ?? [];
}

export function saveTabs(scope: string, userId: string, paths: string[]) {
  const k = tabScopeKey(scope, userId);
  const entries = readAll().filter((e) => e.k !== k);
  if (paths.length > 0) entries.push({ k, paths: [...paths], ts: Date.now() });

  // Recency is the array's own order — the touched key is re-appended above.
  // A sort on `ts` would tie for entries written in the same millisecond and
  // could evict the one just written. `ts` only drives the age cutoff.
  const cutoff = Date.now() - TABS_MAX_AGE_MS;
  const pruned = entries.filter((e) => e.ts > cutoff).slice(-TABS_LRU_LIMIT);
  try {
    sessionStore()?.setItem(
      TABS_STORAGE_KEY,
      JSON.stringify({ v: SCHEMA_VERSION, entries: pruned }),
    );
  } catch {
    // Storage disabled or full — non-fatal.
  }
}
