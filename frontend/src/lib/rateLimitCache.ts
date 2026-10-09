// Account-scoped localStorage cache of the last rate-limit frames, so a
// conversation that hasn't run a turn yet (a new one, or one just switched
// to) can show the account's quota right away instead of an empty badge.
//
// Keyed by provider + account: the windows belong to the login, not the
// conversation, and two accounts of one provider have unrelated quotas.
import type { RateLimitInfo, RateLimitWindow } from "@/stores/chatContextStore";

export const RATE_LIMIT_STORAGE_KEY = "daymug-rate-limits-v1";

// A cached reading is only trusted for one short window: after five hours
// the 5h quota has certainly rolled over, so the remembered percentage would
// describe a period that no longer exists.
export const RATE_LIMIT_CACHE_TTL_SECONDS = 5 * 60 * 60;

type Windows = Partial<Record<RateLimitWindow, RateLimitInfo>>;
type CacheEntry = { observed_at: number; windows: Windows };

function cacheKey(provider: string, account: string): string {
  return `${provider}\u0000${account}`;
}

function loadAll(): Record<string, CacheEntry> {
  try {
    const raw = localStorage.getItem(RATE_LIMIT_STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === "object" ? (parsed as Record<string, CacheEntry>) : {};
  } catch {
    return {};
  }
}

function isFresh(entry: CacheEntry, now: number): boolean {
  if (typeof entry?.observed_at !== "number") return false;
  if (now >= entry.observed_at + RATE_LIMIT_CACHE_TTL_SECONDS) return false;
  // Once the 5h window it recorded has reset, its percentage is wrong even
  // if the reading itself is recent.
  const fiveHour = entry.windows?.five_hour;
  return !fiveHour || now < fiveHour.resets_at;
}

export function saveCachedRateLimits(
  provider: string,
  account: string,
  windows: Windows,
  now = Math.floor(Date.now() / 1000),
) {
  if (!provider) return;
  const all = loadAll();
  for (const [key, entry] of Object.entries(all)) {
    if (!isFresh(entry, now)) delete all[key];
  }
  all[cacheKey(provider, account)] = { observed_at: now, windows };
  try {
    localStorage.setItem(RATE_LIMIT_STORAGE_KEY, JSON.stringify(all));
  } catch {
    // Quota exceeded / private mode: the badge just starts empty next time.
  }
}

export function loadCachedRateLimits(
  provider: string,
  account: string,
  now = Math.floor(Date.now() / 1000),
): Windows {
  if (!provider) return {};
  const entry = loadAll()[cacheKey(provider, account)];
  if (!entry || !isFresh(entry, now)) return {};
  const out: Windows = {};
  for (const type of ["five_hour", "seven_day"] as const) {
    const info = entry.windows?.[type];
    if (info && now < info.resets_at) out[type] = info;
  }
  return out;
}
