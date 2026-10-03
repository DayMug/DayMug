<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { UsageCompositionPoint } from "@/composables/useApi";

const props = defineProps<{ points: UsageCompositionPoint[] }>();
const { t } = useI18n();

const components = [
  { key: "input_tokens", label: "input", color: "#2563eb" },
  { key: "cache_read_input_tokens", label: "cacheRead", color: "#14b8a6" },
  { key: "cache_creation_input_tokens", label: "cacheCreation", color: "#8b5cf6" },
  { key: "output_tokens", label: "output", color: "#f59e0b" },
  { key: "reasoning_output_tokens", label: "reasoning", color: "#ec4899" },
] as const;

type ComponentKey = (typeof components)[number]["key"];

interface ChartPoint extends UsageCompositionPoint {
  total: number;
}

const chartPoints = computed<ChartPoint[]>(() =>
  props.points.map((point) => ({
    ...point,
    // Reasoning is a provider-supplied subset of output, so it is shown as a
    // separate overlay segment but not counted twice in total-token footprint.
    total:
      point.input_tokens +
      point.cache_read_input_tokens +
      point.cache_creation_input_tokens +
      point.output_tokens,
  })),
);

const maxTotal = computed(() => Math.max(1, ...chartPoints.value.map((point) => point.total)));
const chartContainer = ref<HTMLElement | null>(null);
const containerWidth = ref(0);
const width = computed(() =>
  Math.max(720, chartPoints.value.length * 76 + 72, containerWidth.value),
);
const height = 300;
const pad = { top: 20, right: 16, bottom: 54, left: 56 };
const innerHeight = height - pad.top - pad.bottom;
const innerWidth = computed(() => width.value - pad.left - pad.right);
const slotWidth = computed(() => innerWidth.value / Math.max(1, chartPoints.value.length));
const barWidth = computed(() => Math.min(42, slotWidth.value * 0.64));

function value(point: UsageCompositionPoint, key: ComponentKey): number {
  return point[key];
}

function x(index: number): number {
  return pad.left + slotWidth.value * index + (slotWidth.value - barWidth.value) / 2;
}

function segmentHeight(point: ChartPoint, key: ComponentKey): number {
  // reasoning_output_tokens is commonly included in output_tokens. Rendering
  // it as an inset cap preserves visibility without inflating the stack.
  if (key === "reasoning_output_tokens") return 0;
  return (value(point, key) / maxTotal.value) * innerHeight;
}

function y(point: ChartPoint, componentIndex: number): number {
  let before = 0;
  for (let i = 0; i < componentIndex; i += 1) {
    const key = components[i].key;
    if (key !== "reasoning_output_tokens") before += value(point, key);
  }
  const current = components[componentIndex].key;
  return pad.top + innerHeight - ((before + value(point, current)) / maxTotal.value) * innerHeight;
}

const ticks = computed(() =>
  [0, 0.25, 0.5, 0.75, 1].map((ratio) => ({
    y: pad.top + innerHeight * (1 - ratio),
    label: formatCompact(maxTotal.value * ratio),
  })),
);

const hoverIndex = ref<number | null>(null);
let resizeObserver: ResizeObserver | null = null;

onMounted(() => {
  if (!chartContainer.value) return;
  containerWidth.value = chartContainer.value.clientWidth;
  if (typeof ResizeObserver === "undefined") return;
  resizeObserver = new ResizeObserver(([entry]) => {
    containerWidth.value = entry.contentRect.width;
  });
  resizeObserver.observe(chartContainer.value);
});

onBeforeUnmount(() => resizeObserver?.disconnect());

function formatCompact(n: number): string {
  const abs = Math.abs(n);
  if (abs >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)}B`;
  if (abs >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (abs >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return Math.round(n).toLocaleString();
}

function exact(n: number): string {
  return Math.round(n).toLocaleString("en-US");
}

function share(point: ChartPoint, key: ComponentKey): number {
  if (point.total <= 0) return 0;
  return (value(point, key) / point.total) * 100;
}

function costShare(point: ChartPoint, key: ComponentKey): string {
  return `$${((point.cost_usd * share(point, key)) / 100).toFixed(2)}`;
}

function shortLabel(label: string): string {
  return label.length > 12 ? `${label.slice(0, 11)}…` : label;
}
</script>

<template>
  <div ref="chartContainer" class="relative" data-testid="usage-composition-chart">
    <div class="overflow-x-auto pb-1">
      <svg :viewBox="`0 0 ${width} ${height}`" :width="width" :height="height" role="img">
        <g v-for="tick in ticks" :key="tick.y">
          <line
            :x1="pad.left"
            :x2="width - pad.right"
            :y1="tick.y"
            :y2="tick.y"
            class="stroke-border"
            stroke-dasharray="3 4"
          />
          <text
            :x="pad.left - 8"
            :y="tick.y + 4"
            text-anchor="end"
            class="fill-muted-foreground text-[10px]"
          >
            {{ tick.label }}
          </text>
        </g>

        <g
          v-for="(point, index) in chartPoints"
          :key="point.key"
          tabindex="0"
          class="outline-none"
          @mouseenter="hoverIndex = index"
          @mouseleave="hoverIndex = null"
          @focus="hoverIndex = index"
          @blur="hoverIndex = null"
        >
          <rect
            :x="x(index) - 8"
            :y="pad.top"
            :width="barWidth + 16"
            :height="innerHeight"
            fill="transparent"
          />
          <rect
            v-for="(component, componentIndex) in components.slice(0, 4)"
            :key="component.key"
            :x="x(index)"
            :y="y(point, componentIndex)"
            :width="barWidth"
            :height="Math.max(0, segmentHeight(point, component.key))"
            :fill="component.color"
            :opacity="hoverIndex === null || hoverIndex === index ? 1 : 0.38"
          />
          <rect
            v-if="point.reasoning_output_tokens > 0"
            :x="x(index) + barWidth - 6"
            :y="pad.top + innerHeight - (point.output_tokens / maxTotal) * innerHeight"
            width="6"
            :height="Math.max(2, (point.reasoning_output_tokens / maxTotal) * innerHeight)"
            :fill="components[4].color"
          />
          <text
            :x="x(index) + barWidth / 2"
            :y="height - 25"
            text-anchor="middle"
            class="fill-muted-foreground text-[10px]"
          >
            {{ shortLabel(point.label) }}
          </text>
        </g>
      </svg>
    </div>

    <div
      v-if="hoverIndex !== null"
      data-testid="usage-tooltip"
      class="pointer-events-none absolute right-3 top-3 z-10 w-[300px] rounded-lg border border-border bg-popover/95 p-3 text-xs shadow-xl backdrop-blur"
    >
      <div class="mb-2 flex items-center justify-between gap-3">
        <strong class="truncate">{{ chartPoints[hoverIndex].label }}</strong>
        <span class="font-mono text-muted-foreground">{{
          formatCompact(chartPoints[hoverIndex].total)
        }}</span>
      </div>
      <div class="space-y-1.5">
        <div
          v-for="component in components"
          :key="component.key"
          class="grid grid-cols-[8px_1fr_auto] items-center gap-2"
        >
          <span class="size-2 rounded-sm" :style="{ background: component.color }" />
          <span>{{ t(`settings.usage.${component.label}`) }}</span>
          <span class="text-right font-mono">
            {{ exact(value(chartPoints[hoverIndex], component.key)) }} ·
            {{ formatCompact(value(chartPoints[hoverIndex], component.key)) }} ·
            {{ share(chartPoints[hoverIndex], component.key).toFixed(1) }}% ·
            {{ costShare(chartPoints[hoverIndex], component.key) }}
          </span>
        </div>
      </div>
    </div>

    <div class="mt-3 flex flex-wrap gap-x-5 gap-y-2 text-xs text-muted-foreground">
      <span v-for="component in components" :key="component.key" class="flex items-center gap-1.5">
        <i class="size-2.5 rounded-sm" :style="{ background: component.color }" />
        {{ t(`settings.usage.${component.label}`) }}
      </span>
    </div>
  </div>
</template>
