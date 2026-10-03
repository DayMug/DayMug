<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import type { RateLimitInfo } from "@/composables/useChat";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { formatDateTime } from "@/lib/format";
import { useTimeFormat } from "@/composables/useTimeFormat";

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
  const out = formatDateTime(new Date(resetsAt * 1000).toISOString(), timeHour12.value);
  return out || String(resetsAt);
}

// formatLongRemaining is the verbose form used in the hover tooltip
// where width isn't a concern — "2h 44m" reads better than "2H:44M"
// in a full sentence.
function formatLongRemaining(resetsAt: number): string {
  const diff = Math.max(0, resetsAt - now.value);
  if (diff <= 0) return "any moment";
  const h = Math.floor(diff / 3600);
  const m = Math.floor((diff % 3600) / 60);
  const s = diff % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

// Status-driven visual treatment. The CLI's only confirmed status today
// is "allowed"; "warning" / "blocked" / "rejected" are the documented
// transition targets. Anything else falls through to the neutral style
// so an unknown future status doesn't break the layout.
const tone = computed(() => {
  const s = primary.value?.status?.toLowerCase() ?? "";
  if (s === "blocked" || s === "rejected") return "danger";
  if (s === "warning") return "warning";
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

// Single-sentence tooltip that spells out which limit window we're
// counting down, when it resets in human terms, and the CLI-reported
// status. Secondary window (when both 5h and 7d are active) tags on at
// the end as a short clause rather than a second sentence — keeps the
// hover scannable.
const tooltip = computed(() => {
  const p = primary.value;
  if (!p) return "";
  const win = windowLabel(p.type);
  const remaining = formatLongRemaining(p.resets_at);
  const absolute = formatAbsolute(p.resets_at);
  const status = p.status || "unknown";
  const overage = p.is_using_overage ? ", using overage" : "";
  let line = `${win} limit resets in ${remaining} (at ${absolute}, status: ${status}${overage})`;
  const s = secondary.value;
  if (s) {
    line += `; ${windowLabel(s.type)} resets in ${formatLongRemaining(s.resets_at)}`;
  }
  return line + ".";
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
      <TooltipContent side="bottom">{{ tooltip }}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
