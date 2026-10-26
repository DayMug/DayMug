import { describe, expect, it } from "vitest";

import {
  MAX_SCHEDULE_AHEAD_MS,
  defaultAbsoluteSendAt,
  formatSendAt,
  formatUntil,
  isValidSendAt,
  parseLocalDateTime,
  relativeSendAt,
  toLocalDateTimeInput,
} from "./scheduleTime";

const now = new Date(2030, 4, 1, 14, 20, 30);

describe("scheduleTime", () => {
  it("adds a relative delay in the chosen unit", () => {
    expect(relativeSendAt(15, "minutes", now)?.getTime()).toBe(now.getTime() + 15 * 60_000);
    expect(relativeSendAt(2, "hours", now)?.getTime()).toBe(now.getTime() + 2 * 3_600_000);
    expect(relativeSendAt(1, "days", now)?.getTime()).toBe(now.getTime() + 86_400_000);
    expect(relativeSendAt(0, "minutes", now)).toBeNull();
    expect(relativeSendAt(Number.NaN, "hours", now)).toBeNull();
  });

  it("accepts only future times inside the server window", () => {
    expect(isValidSendAt(new Date(now.getTime() + 1000), now)).toBe(true);
    expect(isValidSendAt(now, now)).toBe(false);
    expect(isValidSendAt(new Date(now.getTime() - 1000), now)).toBe(false);
    expect(isValidSendAt(new Date(now.getTime() + MAX_SCHEDULE_AHEAD_MS + 1), now)).toBe(false);
    expect(isValidSendAt(null, now)).toBe(false);
    expect(isValidSendAt(new Date(Number.NaN), now)).toBe(false);
  });

  it("round-trips datetime-local values in local time", () => {
    const value = toLocalDateTimeInput(new Date(2030, 0, 2, 3, 4));
    expect(value).toBe("2030-01-02T03:04");
    expect(parseLocalDateTime(value)?.getTime()).toBe(new Date(2030, 0, 2, 3, 4).getTime());
    expect(parseLocalDateTime("")).toBeNull();
    expect(parseLocalDateTime("tomorrow")).toBeNull();
  });

  it("suggests the next whole hour at least half an hour out", () => {
    expect(defaultAbsoluteSendAt(now)).toEqual(new Date(2030, 4, 1, 15, 0));
    expect(defaultAbsoluteSendAt(new Date(2030, 4, 1, 14, 45))).toEqual(
      new Date(2030, 4, 1, 16, 0),
    );
  });

  it("shows the date only when the send is not today", () => {
    const today = formatSendAt(new Date(2030, 4, 1, 16, 5), now, "en-US", false);
    expect(today).toBe("16:05");
    const tomorrow = formatSendAt(new Date(2030, 4, 2, 9, 0), now, "en-US", false);
    expect(tomorrow).toContain("May 2");
  });

  it("phrases the wait in the largest sensible unit, never zero", () => {
    expect(formatUntil(new Date(now.getTime() + 10_000), now, "en")).toBe("in 1 min.");
    expect(formatUntil(new Date(now.getTime() + 12 * 60_000), now, "en")).toBe("in 12 min.");
    expect(formatUntil(new Date(now.getTime() + 3 * 3_600_000), now, "en")).toBe("in 3 hr.");
    expect(formatUntil(new Date(now.getTime() + 5 * 86_400_000), now, "en")).toBe("in 5 days");
  });
});
