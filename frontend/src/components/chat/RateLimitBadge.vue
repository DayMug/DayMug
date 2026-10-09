<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { RateLimitInfo } from "@/composables/useChat";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { formatDateTime } from "@/lib/format";
import { useTimeFormat } from "@/composables/useTimeFormat";

const { t, locale } = useI18n();
const { timeHour12 } = useTimeFormat();

const props = defineProps<{
  // Map of window-type → most recently seen rate-limit frame. Empty / no
  // active window renders nothing. Picks the 5h window when both are
  // present (it's the one that actually paces typical sessions; 7d is
  // surfaced via tooltip).
  rateLimits: Partial<Record<"five_hour" | "seven_day", RateLimitInfo>>;
}>();

// Tick once per second so the countdown stays live without us having to
// hook a vuex/pinia timer. setInterval is fine — single instance, cleared
// on unmount.
const now = ref(Math.floor(Date.now() / 1000));
let timer: number | null = null;
onMounted(() => {
  timer = window.setInterval(() => {
    now.value = Math.floor(Date.now() / 1000);
  }, 1000);
});
onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer);
});

// The 5h window is the primary display — short enough to actually matter
// while you're working. If only 7d has fired this session, fall back to
// it rather than showing nothing.
const primary = computed<RateLimitInfo | null>(() => {
  return props.rateLimits.five_hour ?? props.rateLimits.seven_day ?? null;
});

// secondary is the "other" window when both are present; surfaced in the
// tooltip only so the badge itself stays a single line.
const secondary = computed<RateLimitInfo | null>(() => {
  if (props.rateLimits.five_hour && props.rateLimits.seven_day) {
    return props.rateLimits.seven_day;
  }
  return null;
});

function windowLabel(t: "five_hour" | "seven_day"): string {
  return t === "five_hour" ? "5H" : "7D";
}

// formatRemaining renders the compact "time-left" half of the badge in
// `${H}H:${M}M` form (e.g. "2H:44M"). At sub-hour intervals we drop the
// hours so the badge stays narrow, and at sub-minute it collapses to
// seconds so the final-stretch ticks are still visible.
function formatRemaining(resetsAt: number): string {
  const diff = Math.max(0, resetsAt - now.value);
  if (diff <= 0) return "RESET";
  const h = Math.floor(diff / 3600);
  const m = Math.floor((diff % 3600) / 60);
  const s = diff % 60;
  if (h > 0) return `${h}H:${m}M`;
  if (m > 0) return `${m}M`;
  return `${s}S`;
}

function formatAbsolute(resetsAt: number): string {
  const out = formatDateTime(
    new Date(resetsAt * 1000).toISOString(),
    timeHour12.value,
    locale.value,
  );
  return out || String(resetsAt);
}

// formatLongRemaining is the verbose form used in the hover tooltip
// where width isn't a concern — "2h 44m" reads better than "2H:44M"
// in a full sentence. Past a day it switches to days so the weekly window
// reads "5d 12h" rather than "132h 0m".
function formatLongRemaining(resetsAt: number): string {
  const diff = Math.max(0, resetsAt - now.value);
  if (diff <= 0) return t("rateLimit.anyMoment");
  const d = Math.floor(diff / 86400);
  const h = Math.floor(diff / 3600);
  const m = Math.floor((diff % 3600) / 60);
  const s = diff % 60;
  if (d > 0) return t("rateLimit.daysHours", { d, h: h % 24 });
  if (h > 0) return t("rateLimit.hoursMinutes", { h, m });
  if (m > 0) return t("rateLimit.minutesSeconds", { m, s });
  return t("rateLimit.seconds", { s });
}

// statusLabel translates the provider's status string. Unknown future
// values pass through verbatim rather than vanish.
const STATUS_KEYS: Record<string, string> = {
  allowed: "allowed",
  within_limit: "allowed",
  allowed_warning: "warning",
  warning: "warning",
  blocked: "blocked",
  rejected: "blocked",
};

function statusLabel(status: string): string {
  const key = STATUS_KEYS[status.toLowerCase().replace(/[\s-]+/g, "_")];
  return key ? t(`rateLimit.status.${key}`) : status;
}

// Share of the window already spent, 0–100. null when the provider sent
// no usage figure, in which case the tooltip shows no bar for that window.
function usedPercent(info: RateLimitInfo): number | null {
  if (info.utilization === undefined) return null;
  return Math.min(100, Math.max(0, info.utilization));
}

// Status-driven visual treatment. The CLI's only confirmed status today
// is "allowed"; "warning" / "blocked" / "rejected" are the documented
// transition targets. Anything else falls through to the neutral style
// so an unknown future status doesn't break the layout. Codex frames carry
// no status, so there the usage percentage decides.
const tone = computed(() => {
  const s = primary.value?.status?.toLowerCase() ?? "";
  if (s === "blocked" || s === "rejected") return "danger";
  if (s === "warning" || s === "allowed_warning") return "warning";
  const used = primary.value?.utilization;
  if (used !== undefined && used >= 100) return "danger";
  if (used !== undefined && used >= 80) return "warning";
  return "ok";
});

const toneClass = computed(() => {
  switch (tone.value) {
    case "danger":
      return "text-destructive";
    case "warning":
      return "text-amber-600 dark:text-amber-400";
    default:
      return "text-muted-foreground";
  }
});

// Format: `${remaining}/${window}` e.g. "2H:44M/5H". Status is conveyed
// through colour only so the label itself stays a clean, fixed shape that
// doesn't reflow when the CLI transitions to warning/blocked.
const label = computed(() => {
  const p = primary.value;
  if (!p) return "";
  return `${formatRemaining(p.resets_at)}/${windowLabel(p.type)}`;
});

// The tooltip shows spent quota as a bar rather than a number; the
// text beside it says when the window resets and the provider-reported
// status where there is one, e.g. "resets in 2h 44m (at 15:30) · within limit".
type WindowRow = { type: string; label: string; used: number | null; detail: string };

function windowRow(info: RateLimitInfo): WindowRow {
  let detail = t("rateLimit.resetsIn", {
    remaining: formatLongRemaining(info.resets_at),
    at: formatAbsolute(info.resets_at),
  });
  if (info.status) {
    const status = info.is_using_overage
      ? t("rateLimit.withOverage", { status: statusLabel(info.status) })
      : statusLabel(info.status);
    detail += ` · ${status}`;
  }
  return {
    type: info.type,
    label: windowLabel(info.type),
    used: usedPercent(info),
    detail,
  };
}

const rows = computed(() =>
  [primary.value, secondary.value].filter((w): w is RateLimitInfo => w !== null).map(windowRow),
);

// A reading restored from cache (or one from a turn long finished) can be
// hours old; say so rather than pass it off as live.
const updatedLine = computed(() => {
  const observed = [primary.value?.observed_at, secondary.value?.observed_at].filter(
    (t): t is number => t !== undefined,
  );
  if (observed.length === 0) return "";
  const age = now.value - Math.min(...observed);
  return age >= 60 ? t("rateLimit.updatedAgo", { age: formatLongRemaining(now.value + age) }) : "";
});

// The heading only makes sense when at least one window draws a bar.
const hasUsage = computed(() => rows.value.some((r) => r.used !== null));

function barClass(used: number): string {
  if (used >= 100) return "bg-red-400";
  if (used >= 80) return "bg-amber-400";
  return "bg-white";
}

// Screen readers can't see the bar, so the accessible label states the
// spent share in words.
const tooltip = computed(() => {
  const parts = rows.value.map((r) => {
    const used = r.used === null ? "" : t("rateLimit.percentUsed", { n: Math.round(r.used) });
    return `${r.label}: ${used}${r.detail}`;
  });
  if (updatedLine.value) parts.push(updatedLine.value);
  return parts.join(t("rateLimit.separator"));
});
</script>

<template>
  <TooltipProvider v-if="primary" :delay-duration="200">
    <Tooltip>
      <TooltipTrigger as-child>
        <span
          data-testid="rate-limit-badge"
          class="inline-flex items-center gap-1 text-[11px] font-mono tabular-nums cursor-default"
          :class="toneClass"
          :aria-label="tooltip"
        >
          <!-- Unicode "clockwise gapped circle arrow" stands in for a refresh
               glyph without dragging in a Lucide icon for one character. -->
          <span aria-hidden="true">⟳</span>
          <span>{{ label }}</span>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom">
        <div class="flex flex-col gap-1.5">
          <div v-if="hasUsage" class="font-semibold">{{ t("rateLimit.usage") }}</div>
          <div v-for="row in rows" :key="row.type" data-testid="rate-limit-row">
            <div class="flex items-center gap-2">
              <span class="font-mono">{{ row.label }}</span>
              <div
                v-if="row.used !== null"
                data-testid="rate-limit-usage-bar"
                class="h-2 w-32 overflow-hidden rounded-full bg-white/25 ring-1 ring-white/40"
                aria-hidden="true"
              >
                <div
                  class="h-full rounded-full"
                  :class="barClass(row.used)"
                  :style="{ width: `${row.used}%` }"
                />
              </div>
            </div>
            <div class="text-white/70">{{ row.detail }}</div>
          </div>
          <div v-if="updatedLine" class="text-white/50">{{ updatedLine }}</div>
        </div>
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
