import { beforeEach, describe, expect, it } from "vitest";

import {
  RATE_LIMIT_CACHE_TTL_SECONDS,
  loadCachedRateLimits,
  saveCachedRateLimits,
} from "./rateLimitCache";

const NOW = 1_700_000_000;
const HOUR = 3600;

describe("rateLimitCache", () => {
  beforeEach(() => localStorage.clear());

  it("restores the last reading for the same provider and account", () => {
    const windows = {
      five_hour: { type: "five_hour" as const, resets_at: NOW + 2 * HOUR, utilization: 40 },
      seven_day: { type: "seven_day" as const, resets_at: NOW + 72 * HOUR, utilization: 9 },
    };
    saveCachedRateLimits("claude", "work", windows, NOW);
    expect(loadCachedRateLimits("claude", "work", NOW + HOUR)).toEqual(windows);
  });

  it("keeps accounts apart", () => {
    saveCachedRateLimits(
      "claude",
      "work",
      { five_hour: { type: "five_hour", resets_at: NOW + HOUR } },
      NOW,
    );
    expect(loadCachedRateLimits("claude", "personal", NOW)).toEqual({});
    expect(loadCachedRateLimits("codex", "work", NOW)).toEqual({});
  });

  it("drops a reading five hours old even if its weekly window is still open", () => {
    saveCachedRateLimits(
      "codex",
      "",
      { seven_day: { type: "seven_day", resets_at: NOW + 72 * HOUR } },
      NOW,
    );
    expect(loadCachedRateLimits("codex", "", NOW + RATE_LIMIT_CACHE_TTL_SECONDS - 1)).not.toEqual(
      {},
    );
    expect(loadCachedRateLimits("codex", "", NOW + RATE_LIMIT_CACHE_TTL_SECONDS)).toEqual({});
  });

  it("drops the whole reading once the 5h window it recorded has reset", () => {
    saveCachedRateLimits(
      "claude",
      "work",
      {
        five_hour: { type: "five_hour", resets_at: NOW + HOUR },
        seven_day: { type: "seven_day", resets_at: NOW + 72 * HOUR },
      },
      NOW,
    );
    expect(loadCachedRateLimits("claude", "work", NOW + HOUR)).toEqual({});
  });
});
