import { ref } from "vue";
import { fetchServerInfo, type ServerInfo } from "./apiServerInfo";

// Module-level singleton: server-info changes per-deploy, not per-session,
// so caching it for the lifetime of the page load is fine and saves every
// composable that needs `sandbox_enabled` from triggering a redundant fetch.
const info = ref<ServerInfo | null>(null);
const loading = ref(false);
let inflight: Promise<ServerInfo> | null = null;

// sessionStorage acts as a soft cache across hard-reloads — server-info
// is per-deploy state, not per-session, so handing the cached payload back
// instantly on remount avoids one /api/server-info round-trip on every
// refresh. A new deploy bumps `version`, so we still re-fetch in the
// background and overwrite the cache when it differs.
const STORAGE_KEY = "daymug-server-info";

function readCached(): ServerInfo | null {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as ServerInfo;
    if (typeof parsed?.version !== "string") return null;
    return parsed;
  } catch {
    return null;
  }
}

function writeCached(v: ServerInfo) {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(v));
  } catch {
    // Quota exceeded / disabled — non-fatal, the in-memory ref already
    // satisfies callers for the rest of the page lifetime.
  }
}

// load forces (or shares) a single fetch. Concurrent callers see the same
// in-flight promise so a multi-component mount doesn't hammer the endpoint.
async function load(): Promise<ServerInfo> {
  if (info.value) return info.value;
  if (inflight) return inflight;
  const cached = readCached();
  if (cached) {
    info.value = cached;
    return cached;
  }
  loading.value = true;
  inflight = fetchServerInfo()
    .then((v) => {
      info.value = v;
      writeCached(v);
      return v;
    })
    .finally(() => {
      loading.value = false;
      inflight = null;
    });
  return inflight;
}

// applyServerInfo lets the cold-start aggregate (/api/app-state) prime the
// singleton without a separate /api/server-info round-trip. Also persists
// to sessionStorage so subsequent hard-reloads short-circuit before any
// network call. Safe to call repeatedly — newer payloads overwrite older.
function applyServerInfo(v: ServerInfo) {
  info.value = v;
  writeCached(v);
}

export function useServerInfo() {
  return { serverInfo: info, loading, loadServerInfo: load, applyServerInfo };
}
