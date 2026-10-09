<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import {
  ChevronRight,
  FilePen,
  FileText,
  Globe,
  ListTodo,
  RefreshCw,
  Search,
  SquareTerminal,
  Users,
  Wrench,
} from "lucide-vue-next";
import { useI18n } from "vue-i18n";

const { t } = useI18n();

interface ToolMsg {
  role: string;
  content: string;
  activityType?: string;
  subagent?: boolean;
  toolStartedAt?: number;
  toolDurationMs?: number;
  toolCompleted?: boolean;
}

const props = defineProps<{
  tools: { key: string; msg: ToolMsg }[];
  expanded: boolean;
  subagent: boolean;
}>();

const emit = defineEmits<{ toggle: [] }>();

const now = ref(Date.now());
let timer: ReturnType<typeof globalThis.setInterval> | undefined;

const hasRunningTool = computed(() =>
  props.tools.some((tool) => tool.msg.toolStartedAt !== undefined && !tool.msg.toolCompleted),
);

watch(
  hasRunningTool,
  (running) => {
    if (running && timer === undefined) {
      now.value = Date.now();
      timer = globalThis.setInterval(() => {
        now.value = Date.now();
      }, 1000);
    } else if (!running && timer !== undefined) {
      globalThis.clearInterval(timer);
      timer = undefined;
    }
  },
  { immediate: true },
);

onUnmounted(() => {
  if (timer !== undefined) globalThis.clearInterval(timer);
});

function formatDuration(milliseconds: number, completed: boolean): string {
  const seconds = completed
    ? Math.max(1, Math.round(milliseconds / 1000))
    : Math.max(0, Math.floor(milliseconds / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
}

function durationLabel(msg: ToolMsg): string {
  if (msg.toolCompleted) {
    if (msg.toolDurationMs === undefined) return "";
    return t("chat.toolDuration", { duration: formatDuration(msg.toolDurationMs, true) });
  }
  if (msg.toolStartedAt === undefined) return "";
  return t("chat.toolElapsed", {
    duration: formatDuration(now.value - msg.toolStartedAt, false),
  });
}

// Wall-clock span from the first tool's start to the last tool's end, so the
// model's thinking between calls counts and parallel calls aren't summed.
// Old history rows without duration_ms carry no start, so fall back to the sum.
function groupSpanMs(): number | undefined {
  let start = Infinity;
  let end = -Infinity;
  for (const { msg } of props.tools) {
    if (msg.toolStartedAt === undefined) return undefined;
    const toolEnd = msg.toolCompleted
      ? msg.toolDurationMs === undefined
        ? undefined
        : msg.toolStartedAt + msg.toolDurationMs
      : now.value;
    if (toolEnd === undefined) return undefined;
    start = Math.min(start, msg.toolStartedAt);
    end = Math.max(end, toolEnd);
  }
  return props.tools.length ? Math.max(0, end - start) : undefined;
}

const groupDurationLabel = computed(() => {
  let total = groupSpanMs();
  if (total === undefined) {
    const durations = props.tools.flatMap(({ msg }) => {
      if (msg.toolCompleted) return msg.toolDurationMs === undefined ? [] : [msg.toolDurationMs];
      return msg.toolStartedAt === undefined ? [] : [Math.max(0, now.value - msg.toolStartedAt)];
    });
    if (!durations.length) return "";
    total = durations.reduce((sum, duration) => sum + duration, 0);
  }
  return t(hasRunningTool.value ? "chat.toolElapsed" : "chat.toolDuration", {
    duration: formatDuration(total, !hasRunningTool.value),
  });
});

function head(content: string): string {
  return content.split("\n", 1)[0] ?? "";
}

function detail(content: string): string {
  const idx = content.indexOf("\n");
  return idx >= 0 ? content.slice(idx + 1).trim() : "";
}

// Tool messages arrive as "[ToolName]\n  arg: value" — strip the brackets so
// the collapsed summary reads as a comma-joined list of names rather than a
// pile of square brackets.
function toolName(content: string): string {
  const h = head(content).trim();
  const m = h.match(/^\[(.+)\]$/);
  return m ? m[1] : h;
}

const summary = computed(() => {
  const names = props.tools.map((t) => toolName(t.msg.content));
  const seen: string[] = [];
  for (const n of names) {
    if (!seen.includes(n)) seen.push(n);
    if (seen.length >= 4) break;
  }
  return seen.join(", ") + (names.length > seen.length ? ", …" : "");
});

// One glyph per tool family, so a run of calls can be told apart at a glance
// the way the reference marks its table-reading step with a sheet icon.
// Codex names its tools differently from Claude; both spellings map here.
const TOOL_ICONS: Record<string, typeof Wrench> = {
  Read: FileText,
  NotebookRead: FileText,
  Write: FilePen,
  Edit: FilePen,
  MultiEdit: FilePen,
  NotebookEdit: FilePen,
  apply_patch: FilePen,
  Bash: SquareTerminal,
  BashOutput: SquareTerminal,
  shell: SquareTerminal,
  exec_command: SquareTerminal,
  Grep: Search,
  Glob: Search,
  WebFetch: Globe,
  WebSearch: Globe,
  web_search: Globe,
  TodoWrite: ListTodo,
  update_plan: ListTodo,
  Task: Users,
  Agent: Users,
};

const groupIcon = computed(() => {
  if (hasRunningTool.value) return RefreshCw;
  const first = props.tools[0];
  return (first && TOOL_ICONS[toolName(first.msg.content)]) || Wrench;
});

const collapsedLabel = computed(() =>
  props.tools.length === 1
    ? t("chat.usedTool", { name: summary.value })
    : t("chat.usedTools", { count: props.tools.length, names: summary.value }),
);
</script>

<template>
  <!-- Iris inline tool line: a quiet 13px "已使用 …" with its glyph, sitting in
       the prose rhythm rather than in a card. The chevron only surfaces on
       hover (or once open) — the whole line is the toggle. -->
  <div
    class="tool-group -mt-[3px] mb-[21px] text-[13px]"
    :class="subagent ? 'subagent-track pl-4 border-l-2 border-[var(--edge-soft)] opacity-80' : ''"
    :data-subagent="subagent ? 'true' : undefined"
  >
    <div class="tool-group-surface overflow-hidden">
      <button
        type="button"
        class="tool-group-header group/tool flex min-h-9 w-full cursor-pointer items-center gap-[9px] rounded-md px-0 text-left text-[13px] text-[#85868b] transition-colors hover:text-foreground dark:text-muted-foreground"
        :aria-expanded="expanded"
        @click="emit('toggle')"
      >
        <component
          :is="groupIcon"
          class="size-[18px] shrink-0"
          :class="
            hasRunningTool
              ? 'animate-spin text-[#6963db] [animation-duration:2.6s]'
              : 'text-[#8c8d92] dark:text-muted-foreground'
          "
          :stroke-width="1.6"
          aria-hidden="true"
        />
        <span class="tool-group-summary min-w-0 truncate">{{ collapsedLabel }}</span>
        <span
          v-if="groupDurationLabel"
          class="tool-group-duration shrink-0 text-[11px] tabular-nums text-muted-foreground"
        >
          {{ groupDurationLabel }}
        </span>
        <ChevronRight
          class="size-3.5 shrink-0 transition-[transform,opacity]"
          :class="expanded ? 'rotate-90 opacity-100' : 'opacity-0 group-hover/tool:opacity-100'"
          aria-hidden="true"
        />
      </button>
      <div
        v-if="expanded"
        class="tool-group-body ml-[9px] border-l border-[var(--edge-soft)] pl-[18px]"
      >
        <div v-for="tool in tools" :key="tool.key" class="py-2 first:pt-1">
          <div class="tool-header flex items-center gap-2 text-xs">
            <span class="font-semibold text-foreground">{{ head(tool.msg.content) }}</span>
            <span class="tool-duration text-[11px] tabular-nums text-muted-foreground">
              {{ durationLabel(tool.msg) }}
            </span>
          </div>
          <pre
            v-if="detail(tool.msg.content)"
            class="tool-detail mt-1 mb-0 text-[11px] leading-[1.45] text-[var(--ink-3)] whitespace-pre-wrap break-words"
            >{{ detail(tool.msg.content) }}</pre
          >
        </div>
      </div>
    </div>
  </div>
</template>
