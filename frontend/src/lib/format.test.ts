import { afterEach, describe, it, expect } from "vitest";
import { i18n } from "@/i18n";
import zh from "@/i18n/locales/zh";
import { formatSentTime, formatDateTime, relativeTime } from "./format";

describe("formatSentTime", () => {
  it("returns empty for an unparseable timestamp so the caller can hide the hint", () => {
    expect(formatSentTime("not-a-date")).toBe("");
  });

  it("formats messages newer than 12h as a time-of-day so recent chat stays compact", () => {
    const now = new Date("2026-05-17T15:30:00Z").getTime();
    const tenMinutesAgo = new Date(now - 10 * 60 * 1000).toISOString();
    const out = formatSentTime(tenMinutesAgo, now);
    // Locale-dependent rendering — we don't assert exact characters
    // because Intl output varies between Node ICU builds and the
    // user's runtime. We only assert the *shape*: a single time stamp
    // with no year/date noise. A date would contain "2026" or a
    // slash/dash separator that the hh:mm formatter never emits.
    expect(out).not.toContain("2026");
    expect(out).not.toMatch(/\d{4}/);
    expect(out).toMatch(/\d/);
  });

  it("falls back to a full date for messages older than 12h so back-scroll isn't ambiguous", () => {
    const now = new Date("2026-05-17T15:30:00Z").getTime();
    const oneDayAgo = new Date(now - 24 * 60 * 60 * 1000).toISOString();
    const out = formatSentTime(oneDayAgo, now);
    // The full formatter is configured with year:numeric, so 2026
    // must appear somewhere in the output regardless of locale order.
    expect(out).toContain("2026");
  });

  it("falls back to the full date when the supplied timestamp is in the future, so a clock-skewed input never silently collapses to hh:mm", () => {
    const now = new Date("2026-05-17T15:30:00Z").getTime();
    const future = new Date(now + 60 * 1000).toISOString();
    const out = formatSentTime(future, now);
    // diffMs is negative, the recent branch requires >=0, so we hit
    // the full formatter and the year shows up.
    expect(out).toContain("2026");
  });

  it("forces 24-hour clock when hour12=false, so users who flipped the macOS toggle don't see AM/PM through their en-US locale", () => {
    const now = new Date("2026-05-17T15:30:00Z").getTime();
    const tenMinutesAgo = new Date(now - 10 * 60 * 1000).toISOString();
    const out = formatSentTime(tenMinutesAgo, now, false);
    expect(out).not.toMatch(/AM|PM/i);
  });

  it("forces 12-hour clock when hour12=true, so users on a 24h locale can still opt back into AM/PM", () => {
    const now = new Date("2026-05-17T15:30:00Z").getTime();
    const tenMinutesAgo = new Date(now - 10 * 60 * 1000).toISOString();
    const out = formatSentTime(tenMinutesAgo, now, true);
    expect(out).toMatch(/AM|PM/i);
  });
});

describe("formatDateTime", () => {
  it("returns empty for an unparseable timestamp so callers can pick their own placeholder", () => {
    expect(formatDateTime("not-a-date")).toBe("");
  });

  it("renders a full date+time including the year so the surface (file properties, admin) is unambiguous", () => {
    const out = formatDateTime("2026-05-17T15:30:00Z");
    expect(out).toContain("2026");
  });

  it("forces 24-hour clock when hour12=false so the Appearance setting reaches every datetime surface", () => {
    const out = formatDateTime("2026-05-17T15:30:00Z", false);
    expect(out).not.toMatch(/AM|PM/i);
  });

  it("forces 12-hour clock when hour12=true", () => {
    const out = formatDateTime("2026-05-17T15:30:00Z", true);
    expect(out).toMatch(/AM|PM/i);
  });
});

describe("relativeTime", () => {
  i18n.global.setLocaleMessage("zh", zh);
  afterEach(() => {
    i18n.global.locale.value = "en";
  });

  const ago = (sec: number) => new Date(Date.now() - sec * 1000).toISOString();

  it("follows the active locale", () => {
    expect(relativeTime(ago(5 * 60))).toBe("5m ago");
    expect(relativeTime(ago(3 * 3600), "compact")).toBe("3h");
    i18n.global.locale.value = "zh";
    expect(relativeTime(ago(5 * 60))).toBe("5 分钟前");
    expect(relativeTime(ago(30 * 3600))).toBe("昨天");
  });
});
