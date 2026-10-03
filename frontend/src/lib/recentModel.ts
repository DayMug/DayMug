import type { ThinkLevel } from "@/composables/apiTypes";

export const RECENT_MODEL_STORAGE_KEY = "daymug-recent-model-v1";

export interface RecentModel {
  provider: string;
  model: string;
  account?: string;
  think_level?: ThinkLevel;
}

function isThinkLevel(value: unknown): value is ThinkLevel {
  return (
    value === "" || value === "low" || value === "medium" || value === "high" || value === "max"
  );
}

function isValidPair(value: unknown): value is RecentModel {
  if (!value || typeof value !== "object") return false;
  const v = value as Partial<RecentModel>;
  return (
    typeof v.provider === "string" &&
    v.provider !== "" &&
    typeof v.model === "string" &&
    v.model !== "" &&
    (v.think_level === undefined || isThinkLevel(v.think_level))
  );
}

export function loadRecentModel(): RecentModel | null {
  try {
    const raw = localStorage.getItem(RECENT_MODEL_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as unknown;
    return isValidPair(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

export function saveRecentModel(
  provider: string,
  model: string,
  account?: string,
  thinkLevel?: ThinkLevel,
) {
  const trimmedAccount = account?.trim();
  const value = {
    provider: provider.trim(),
    model: model.trim(),
    ...(trimmedAccount ? { account: trimmedAccount } : {}),
    ...(thinkLevel !== undefined ? { think_level: thinkLevel } : {}),
  };
  if (!isValidPair(value)) return;
  try {
    localStorage.setItem(RECENT_MODEL_STORAGE_KEY, JSON.stringify(value));
  } catch {
    // Storage can be unavailable in private windows; model persistence is best-effort.
  }
}
