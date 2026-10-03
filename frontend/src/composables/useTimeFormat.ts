import { computed, ref, watch } from "vue";

// User preference for how chat-bubble timestamps are rendered.
// "system" = let Intl.DateTimeFormat pick based on the browser locale,
// which is the historical default. The explicit options exist because
// macOS's "24-hour time" toggle in System Settings is NOT exposed to
// browsers — they only see navigator.language — so a user on en-US who
// has flipped that toggle still gets AM/PM unless we override here.
export type TimeFormat = "system" | "12h" | "24h";

const STORAGE_KEY = "daymug.timeFormat";
const VALID: readonly TimeFormat[] = ["system", "12h", "24h"];

function readInitial(): TimeFormat {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v && (VALID as readonly string[]).includes(v)) return v as TimeFormat;
  } catch {
    // localStorage can throw in private browsing / sandboxed iframes
  }
  return "system";
}

const timeFormat = ref<TimeFormat>(readInitial());

watch(timeFormat, (next) => {
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    // best-effort persistence
  }
});

// Derived hour12 flag for Intl.DateTimeFormat:
//   "system" → undefined (defer to locale default)
//   "12h"    → true
//   "24h"    → false
const timeHour12 = computed<boolean | undefined>(() => {
  if (timeFormat.value === "12h") return true;
  if (timeFormat.value === "24h") return false;
  return undefined;
});

function setTimeFormat(next: TimeFormat) {
  if (!(VALID as readonly string[]).includes(next)) return;
  timeFormat.value = next;
}

function cycleTimeFormat() {
  const order: TimeFormat[] = ["system", "12h", "24h"];
  const i = order.indexOf(timeFormat.value);
  timeFormat.value = order[(i + 1) % order.length];
}

export function useTimeFormat() {
  return { timeFormat, timeHour12, setTimeFormat, cycleTimeFormat };
}
