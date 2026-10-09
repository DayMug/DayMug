import { i18n } from "@/i18n";

const t = i18n.global.t;

// relativeTime renders a timestamp as a human-readable age. The two styles
// match what each surface used to emit before the helper was extracted:
//   - "long":    "just now" / "5m ago" / "2h ago" / "yesterday" / "3d ago"
//   - "compact": "now"      / "5m"     / "2h"     / "yesterday" / "3d"
// Falls back to a localized date string after 30 days, or "" for an unparseable input.
export function relativeTime(dateStr: string, style: "long" | "compact" = "long"): string {
  const then = new Date(dateStr).getTime();
  if (isNaN(then)) return "";
  const diffSec = Math.floor((Date.now() - then) / 1000);

  const compact = style === "compact";
  if (diffSec < 60) return t(compact ? "relativeTime.now" : "relativeTime.justNow");
  if (diffSec < 3600) {
    const m = Math.floor(diffSec / 60);
    return t(compact ? "relativeTime.minutesShort" : "relativeTime.minutesAgo", { n: m });
  }
  if (diffSec < 86400) {
    const h = Math.floor(diffSec / 3600);
    return t(compact ? "relativeTime.hoursShort" : "relativeTime.hoursAgo", { n: h });
  }
  if (diffSec < 86400 * 2) return t("relativeTime.yesterday");
  if (diffSec < 86400 * 30) {
    const d = Math.floor(diffSec / 86400);
    return t(compact ? "relativeTime.daysShort" : "relativeTime.daysAgo", { n: d });
  }
  return new Date(dateStr).toLocaleDateString();
}

// formatSentTime renders a message timestamp for the small "sent at" hint
// that sits next to the username in chat. Defers to the browser's default
// locale (Intl.DateTimeFormat with locale=undefined) so date ordering and
// separators follow the user's locale. The optional `hour12` argument
// overrides the locale's clock — macOS's "24-hour time" toggle is not
// exposed to browsers, so without an override an en-US user always sees
// AM/PM regardless of that OS setting. Recent messages (<12h) collapse to
// hh:mm:ss so the chat header stays compact; older messages get a full date
// so scrolling back through history doesn't show a wall of "14:23:05" that
// could be from any day. Seconds are always shown: several turns often land
// within the same minute, and the header is what tells them apart.
const recentTimeFormatterDefault = new Intl.DateTimeFormat(undefined, {
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
});
const fullTimeFormatterDefault = new Intl.DateTimeFormat(undefined, {
  year: "numeric",
  month: "numeric",
  day: "numeric",
  hour: "numeric",
  minute: "2-digit",
  second: "2-digit",
});

// formatDateTime renders a full date + time string for any surface that
// previously called `Date.toLocaleString()` directly — file properties,
// admin "released at", rate-limit reset etc. Date order and separators
// follow the browser locale; the optional `hour12` argument overrides the
// locale's clock so the user's Appearance → Time format preference wins, and
// the optional `locale` pins the date order to the UI language instead.
// Returns "" when the input doesn't parse, matching formatSentTime's
// "empty string when there's nothing to show" convention.
export function formatDateTime(dateStr: string, hour12?: boolean, locale?: string): string {
  const d = new Date(dateStr);
  if (isNaN(d.getTime())) return "";
  if (hour12 === undefined) return d.toLocaleString(locale);
  return d.toLocaleString(locale, { hour12 });
}

export function formatSentTime(
  dateStr: string,
  now: number = Date.now(),
  hour12?: boolean,
): string {
  const then = new Date(dateStr).getTime();
  if (isNaN(then)) return "";
  const diffMs = now - then;
  const recent = diffMs < 12 * 60 * 60 * 1000 && diffMs >= 0;
  if (hour12 === undefined) {
    return (recent ? recentTimeFormatterDefault : fullTimeFormatterDefault).format(then);
  }
  // Custom hour12 override: build a one-off formatter. The cost is tiny
  // (one DateTimeFormat per render) and the override is rare enough that
  // caching by hour12 value isn't worth the bookkeeping.
  const opts: Intl.DateTimeFormatOptions = recent
    ? { hour: "numeric", minute: "2-digit", second: "2-digit", hour12 }
    : {
        year: "numeric",
        month: "numeric",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
        second: "2-digit",
        hour12,
      };
  return new Intl.DateTimeFormat(undefined, opts).format(then);
}
