<script setup lang="ts">
import { computed } from "vue";
import { Progress } from "@/components/ui/progress";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

const props = defineProps<{
  used: number;
  total: number;
  // Optional breakdown — populated when the backend's context_usage event
  // carries the new fields (most modern claude CLI versions). Older / partial
  // events leave these undefined; we then render just "Context X%".
  inputTokens?: number;
  cacheRead?: number;
  cacheCreation?: number;
  // Optional pricing threshold. Codex input above 272K uses a higher price
  // tier; callers opt in because this rule is provider/model-specific.
  priceBreakpoint?: number;
  // Pre-formatted per-turn usage summary (e.g.
  // "I/O: 9/2.4K | CR: 249K | CW: 6K | Cost: $0.2234 | Turns: 4") from the
  // most recent assistant turn. When present, the bar surfaces it as the
  // headline of the hover tooltip — same numbers as the per-message chip
  // but reachable without scrolling to the relevant bubble.
  usageSummary?: string;
}>();

const percent = computed(() => {
  if (!props.total) return 0;
  // Defensive cap: the backend already excludes output tokens from `used`,
  // but cap here too so a stray API change can't render a >100% bar.
  return Math.min(100, Math.max(0, Math.round((props.used / props.total) * 100)));
});

const priceBreakpointPercent = computed(() => {
  if (!props.priceBreakpoint || !props.total) return null;
  if (props.priceBreakpoint <= 0 || props.priceBreakpoint >= props.total) return null;
  return (props.priceBreakpoint / props.total) * 100;
});

function fmtNum(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return (n / 1000).toFixed(n < 10_000 ? 1 : 0) + "k";
  return (n / 1_000_000).toFixed(1) + "M";
}

// Tooltip is the only place the per-turn breakdown shows — the compact
// editorial bar stays as-is. When a usageSummary from the most recent
// assistant turn is available, it leads (I/O, cache, cost, turns); the
// cumulative context line follows so both bits of info live in one hover.
const tooltip = computed(() => {
  const lines: string[] = [];
  if (props.usageSummary) lines.push(props.usageSummary);
  lines.push(
    `${t("chat.context.context")} ${percent.value}% (${fmtNum(props.used)} / ${fmtNum(props.total)})`,
  );
  if (props.inputTokens != null)
    lines.push(`${t("chat.context.input")}: ${fmtNum(props.inputTokens)}`);
  if (props.cacheRead != null && props.cacheRead > 0) {
    lines.push(`${t("chat.context.cacheRead")}: ${fmtNum(props.cacheRead)}`);
  }
  if (props.cacheCreation != null && props.cacheCreation > 0) {
    lines.push(`${t("chat.context.cacheWrite")}: ${fmtNum(props.cacheCreation)}`);
  }
  if (priceBreakpointPercent.value != null) {
    lines.push(
      `${t("chat.context.priceBreakpoint")}: ${fmtNum(props.priceBreakpoint!)} (${t("chat.context.priceBreakpointHint")})`,
    );
  }
  return lines.join("\n");
});
</script>

<template>
  <!-- Compact editorial style: 80×4 indigo progress + mono percent. The
       full "context" label is exposed via aria-label for screen readers
       and surfaced visually via the shadcn Tooltip on hover.
       In a narrow composer the 80px track is dropped and only the percentage
       survives: the bar is a redundant rendering of a number that is already
       on screen, and those 88px are exactly what lets the controls and the
       send button share one line on a phone. The query needs the composer
       pill's `@container`, so anywhere else the bar always shows. -->
  <TooltipProvider :delay-duration="200">
    <Tooltip>
      <TooltipTrigger as-child>
        <div class="flex items-center gap-2 cursor-default" :aria-label="tooltip">
          <div class="relative w-20 h-1 @max-sm:hidden">
            <Progress :model-value="percent" class="w-full h-1" />
            <span
              v-if="priceBreakpointPercent != null"
              class="absolute top-[-2px] z-10 h-2 w-px bg-amber-500"
              :style="{ left: `${priceBreakpointPercent}%` }"
              :title="`${t('chat.context.priceBreakpoint')}: ${fmtNum(priceBreakpoint!)}`"
              aria-hidden="true"
            />
          </div>
          <span
            class="text-[11px] font-mono text-muted-foreground tabular-nums min-w-[2.4rem] text-right @max-sm:min-w-0"
            >{{ percent }}%</span
          >
        </div>
      </TooltipTrigger>
      <TooltipContent side="bottom">{{ tooltip }}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
