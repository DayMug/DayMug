import { isRecentModel, type RecentModel } from "./recentModel";

// Per-agent memory of how the user last ran that agent: the model profile and
// whether the conversation ran on the server or in a folder on this computer.
// A new conversation for the same agent starts from these, so switching
// between agents that live on different models (or different computers) does
// not mean re-picking both every time. Browser-local on purpose: it describes
// this browser's habits, not the agent, and the folder is only meaningful on
// the computer that has it.
export const AGENT_PREFS_STORAGE_KEY = "daymug-agent-prefs-v1";

// Bounded so a long-lived browser that has seen many agents does not grow the
// entry forever; the least recently touched agents drop off first.
const MAX_AGENTS = 50;

export type AgentRunLocation =
  | { kind: "server" }
  | { kind: "device"; device_id: string; work_dir: string };

export interface AgentPrefs {
  model?: RecentModel;
  location?: AgentRunLocation;
}

type Store = Record<string, AgentPrefs>;

function isLocation(value: unknown): value is AgentRunLocation {
  if (!value || typeof value !== "object") return false;
  const v = value as Partial<{ kind: string; device_id: string; work_dir: string }>;
  if (v.kind === "server") return true;
  return (
    v.kind === "device" &&
    typeof v.device_id === "string" &&
    v.device_id !== "" &&
    typeof v.work_dir === "string" &&
    v.work_dir !== ""
  );
}

function sanitize(value: unknown): AgentPrefs | null {
  if (!value || typeof value !== "object") return null;
  const v = value as Partial<AgentPrefs>;
  const prefs: AgentPrefs = {};
  if (isRecentModel(v.model)) prefs.model = v.model;
  if (isLocation(v.location)) prefs.location = v.location;
  return prefs;
}

function readStore(): Store {
  try {
    const raw = localStorage.getItem(AGENT_PREFS_STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Store = {};
    for (const [agentId, entry] of Object.entries(parsed as Record<string, unknown>)) {
      const prefs = sanitize(entry);
      if (prefs) out[agentId] = prefs;
    }
    return out;
  } catch {
    return {};
  }
}

function writeStore(store: Store) {
  try {
    localStorage.setItem(AGENT_PREFS_STORAGE_KEY, JSON.stringify(store));
  } catch {
    // Storage can be unavailable in private windows; the memory is best-effort.
  }
}

export function loadAgentPrefs(agentId: string): AgentPrefs {
  if (!agentId) return {};
  return readStore()[agentId] ?? {};
}

// Re-inserting the agent moves it to the end, which is the LRU order the cap
// below trims from the front.
function update(agentId: string, patch: AgentPrefs) {
  if (!agentId) return;
  const store = readStore();
  const next: AgentPrefs = { ...store[agentId], ...patch };
  delete store[agentId];
  store[agentId] = next;
  const ids = Object.keys(store);
  for (const stale of ids.slice(0, Math.max(0, ids.length - MAX_AGENTS))) delete store[stale];
  writeStore(store);
}

export function saveAgentModel(agentId: string, model: RecentModel) {
  if (!isRecentModel(model)) return;
  update(agentId, { model });
}

export function saveAgentLocation(agentId: string, location: AgentRunLocation) {
  if (!isLocation(location)) return;
  update(agentId, { location });
}
