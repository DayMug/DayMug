// Time helpers for the composer's "send later" action. Kept pure (every
// function takes `now`) so the picker and the scheduled list agree on what is
// valid and tests do not depend on the wall clock.

export type ScheduleUnit = "minutes" | "hours" | "days";

const UNIT_MS: Record<ScheduleUnit, number> = {
  minutes: 60_000,
  hours: 3_600_000,
  days: 86_400_000,
};

// The server refuses anything past 366 days; the picker stops a day short so
// clock skew between browser and server can never turn a valid pick into an
// error.
export const MAX_SCHEDULE_AHEAD_MS = 365 * UNIT_MS.days;

export function relativeSendAt(amount: number, unit: ScheduleUnit, now: Date): Date | null {
  if (!Number.isFinite(amount) || amount <= 0) return null;
  return new Date(now.getTime() + amount * UNIT_MS[unit]);
}

// isValidSendAt: strictly in the future and inside the server's window.
export function isValidSendAt(sendAt: Date | null, now: Date): sendAt is Date {
  if (!sendAt || Number.isNaN(sendAt.getTime())) return false;
  const ahead = sendAt.getTime() - now.getTime();
  return ahead > 0 && ahead <= MAX_SCHEDULE_AHEAD_MS;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

// toLocalDateTimeInput renders a Date as the local-time value an
// <input type="datetime-local"> expects ("YYYY-MM-DDTHH:mm").
export function toLocalDateTimeInput(d: Date): string {
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}`
  );
}

// parseLocalDateTime reads a datetime-local value back as a local instant.
export function parseLocalDateTime(value: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(value.trim());
  if (!m) return null;
  const [, y, mo, d, h, mi, s] = m;
  const date = new Date(
    Number(y),
    Number(mo) - 1,
    Number(d),
    Number(h),
    Number(mi),
    Number(s ?? 0),
  );
  return Number.isNaN(date.getTime()) ? null : date;
}

// defaultAbsoluteSendAt suggests the next whole hour at least 30 minutes out,
// a sensible starting point the user nudges rather than types from scratch.
export function defaultAbsoluteSendAt(now: Date): Date {
  const d = new Date(now.getTime() + 30 * UNIT_MS.minutes);
  d.setMinutes(0, 0, 0);
  d.setHours(d.getHours() + 1);
  return d;
}

function sameDay(a: Date, b: Date): boolean {
  return (
    a.getFullYear() === b.getFullYear() &&
    a.getMonth() === b.getMonth() &&
    a.getDate() === b.getDate()
  );
}

// formatSendAt shows a clock time for today and adds the date otherwise.
export function formatSendAt(sendAt: Date, now: Date, locale?: string, hour12?: boolean): string {
  const time: Intl.DateTimeFormatOptions = { hour: "numeric", minute: "2-digit", hour12 };
  const opts: Intl.DateTimeFormatOptions = sameDay(sendAt, now)
    ? time
    : {
        ...(sendAt.getFullYear() === now.getFullYear() ? {} : { year: "numeric" }),
        month: "short",
        day: "numeric",
        ...time,
      };
  return new Intl.DateTimeFormat(locale, opts).format(sendAt);
}

// formatUntil phrases the wait ("in 12 minutes"). Rounded up so a prompt never
// reads "in 0 minutes" while it is still waiting.
export function formatUntil(sendAt: Date, now: Date, locale?: string): string {
  const rtf = new Intl.RelativeTimeFormat(locale, { numeric: "always", style: "short" });
  const ms = Math.max(sendAt.getTime() - now.getTime(), 0);
  const minutes = Math.ceil(ms / UNIT_MS.minutes);
  if (minutes < 60) return rtf.format(Math.max(minutes, 1), "minute");
  const hours = Math.round(ms / UNIT_MS.hours);
  if (hours < 48) return rtf.format(hours, "hour");
  return rtf.format(Math.round(ms / UNIT_MS.days), "day");
}
