<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Dialog, DialogContent } from "@/components/ui/dialog";
import { DialogDescription, DialogTitle } from "reka-ui";
import { useFileApi, type SearchMode, type SearchResult } from "@/composables/useFileApi";

const props = defineProps<{
  userId: string;
  open: boolean;
  // Which mode the palette opens in. Cmd+P wants file names; Cmd+Shift+F
  // wants contents. Re-applied on every open so the two shortcuts stay
  // distinct even after the user toggled the mode by hand last time.
  initialMode?: SearchMode;
}>();

const emit = defineEmits<{
  "update:open": [value: boolean];
  // `line` is 0 for name-mode hits, where there is nothing to jump to.
  select: [payload: { path: string; line: number }];
}>();

const { t } = useI18n();
const { searchFiles } = useFileApi();

const query = ref("");
const mode = ref<SearchMode>("name");
const results = ref<SearchResult[]>([]);
const truncated = ref(false);
const loading = ref(false);
const failed = ref(false);
const activeIndex = ref(0);
const inputRef = ref<HTMLInputElement | null>(null);
const listRef = ref<HTMLElement | null>(null);

// Every keystroke would otherwise walk the whole workspace. The debounce plus
// an abort of the in-flight request is what keeps a fast typist from queueing
// a dozen tree walks the results of which are all discarded but the last.
let debounceTimer: ReturnType<typeof setTimeout> | null = null;
let inFlight: AbortController | null = null;
// Guards against an out-of-order response overwriting a newer one — aborting
// is best-effort and a response can already be in the microtask queue.
let run = 0;

const DEBOUNCE_MS = 150;

// Content search has to open every candidate file, so a one-character query
// is both useless and the most expensive thing the endpoint can be asked to
// do. Name search is a cheap path-string compare, so one character is fine.
const minQueryLength = computed(() => (mode.value === "content" ? 2 : 1));

function close() {
  emit("update:open", false);
}

function cancelPending() {
  if (debounceTimer) {
    clearTimeout(debounceTimer);
    debounceTimer = null;
  }
  inFlight?.abort();
  inFlight = null;
}

async function runSearch() {
  const q = query.value.trim();
  const mine = ++run;
  if (q.length < minQueryLength.value) {
    results.value = [];
    truncated.value = false;
    loading.value = false;
    failed.value = false;
    return;
  }
  inFlight?.abort();
  const controller = new AbortController();
  inFlight = controller;
  loading.value = true;
  failed.value = false;
  try {
    const resp = await searchFiles(
      props.userId,
      { query: q, mode: mode.value, limit: 50 },
      controller.signal,
    );
    if (mine !== run) return;
    results.value = resp.results;
    truncated.value = resp.truncated;
    activeIndex.value = 0;
  } catch (e) {
    if (mine !== run) return;
    // An abort is this component cancelling itself, not a failure the user
    // should see — the newer query's result is already on its way.
    if (e instanceof DOMException && e.name === "AbortError") return;
    results.value = [];
    failed.value = true;
  } finally {
    if (mine === run) loading.value = false;
  }
}

function scheduleSearch() {
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    void runSearch();
  }, DEBOUNCE_MS);
}

watch(query, scheduleSearch);
watch(mode, () => {
  // Switching modes re-runs immediately: the user made an explicit choice and
  // waiting out a debounce reads as the toggle not having worked.
  cancelPending();
  void runSearch();
});

watch(
  () => props.open,
  async (open) => {
    if (!open) {
      cancelPending();
      return;
    }
    mode.value = props.initialMode ?? "name";
    query.value = "";
    results.value = [];
    truncated.value = false;
    failed.value = false;
    activeIndex.value = 0;
    await nextTick();
    inputRef.value?.focus();
  },
  // immediate, because the palette can be mounted already-open (a parent that
  // v-ifs it into existence rather than toggling a prop). Without this the
  // requested mode would never be applied on that path.
  { immediate: true },
);

function move(delta: number) {
  if (results.value.length === 0) return;
  const next = activeIndex.value + delta;
  // Wrap, so holding ↓ at the bottom returns to the top rather than sticking.
  activeIndex.value = (next + results.value.length) % results.value.length;
  void nextTick(() => {
    listRef.value?.querySelector('[data-active="true"]')?.scrollIntoView({ block: "nearest" });
  });
}

function choose(index: number) {
  const hit = results.value[index];
  if (!hit) return;
  emit("select", { path: hit.path, line: hit.matches[0]?.line ?? 0 });
  close();
}

function onKeydown(e: KeyboardEvent) {
  switch (e.key) {
    case "ArrowDown":
      e.preventDefault();
      move(1);
      break;
    case "ArrowUp":
      e.preventDefault();
      move(-1);
      break;
    case "Enter":
      e.preventDefault();
      choose(activeIndex.value);
      break;
    case "Tab":
      // Tab toggles the mode rather than moving focus: there is nothing else
      // in the palette worth tabbing to, and it saves a reach for the mouse.
      e.preventDefault();
      mode.value = mode.value === "name" ? "content" : "name";
      break;
  }
}

const placeholder = computed(() =>
  mode.value === "content" ? t("quickOpen.placeholderContent") : t("quickOpen.placeholderName"),
);

const showEmpty = computed(
  () =>
    !loading.value &&
    !failed.value &&
    results.value.length === 0 &&
    query.value.trim().length >= minQueryLength.value,
);

function dirOf(path: string): string {
  const slash = path.lastIndexOf("/");
  return slash >= 0 ? path.slice(0, slash) : "";
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent
      :show-close-button="false"
      class="top-[12%] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-2xl"
      data-testid="quick-open"
    >
      <DialogTitle class="sr-only">{{ t("quickOpen.title") }}</DialogTitle>
      <DialogDescription class="sr-only">{{ t("quickOpen.hint") }}</DialogDescription>

      <div class="flex items-center gap-2 border-b border-border px-3">
        <input
          ref="inputRef"
          v-model="query"
          type="text"
          class="h-11 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
          :placeholder="placeholder"
          data-testid="quick-open-input"
          @keydown="onKeydown"
        />
        <button
          type="button"
          class="shrink-0 rounded border border-border px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
          :title="t('quickOpen.toggleHint')"
          data-testid="quick-open-mode"
          @click="mode = mode === 'name' ? 'content' : 'name'"
        >
          {{ mode === "content" ? t("quickOpen.modeContent") : t("quickOpen.modeName") }}
        </button>
      </div>

      <div ref="listRef" class="max-h-[50vh] overflow-y-auto">
        <p v-if="loading" class="px-3 py-3 text-xs text-muted-foreground">
          {{ t("quickOpen.searching") }}
        </p>
        <p v-else-if="failed" class="px-3 py-3 text-xs text-destructive">
          {{ t("quickOpen.failed") }}
        </p>
        <p v-else-if="showEmpty" class="px-3 py-3 text-xs text-muted-foreground">
          {{ t("quickOpen.noResults") }}
        </p>
        <ul v-else>
          <li v-for="(hit, i) in results" :key="hit.path">
            <button
              type="button"
              class="flex w-full flex-col items-start gap-0.5 px-3 py-1.5 text-left transition-colors"
              :class="i === activeIndex ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/50'"
              :data-active="i === activeIndex"
              data-testid="quick-open-result"
              @click="choose(i)"
              @mouseenter="activeIndex = i"
            >
              <span class="flex w-full min-w-0 items-baseline gap-2">
                <span class="truncate text-sm">{{ hit.name }}</span>
                <span class="truncate text-[11px] text-muted-foreground">{{
                  dirOf(hit.path)
                }}</span>
              </span>
              <span
                v-if="hit.matches.length > 0"
                class="w-full truncate font-mono text-[11px] text-muted-foreground"
                >{{ hit.matches[0].line }}: {{ hit.matches[0].text.trim() }}</span
              >
            </button>
          </li>
        </ul>
      </div>

      <div
        class="flex items-center justify-between border-t border-border px-3 py-1.5 text-[11px] text-muted-foreground"
      >
        <span>{{ t("quickOpen.hint") }}</span>
        <span v-if="truncated">{{ t("quickOpen.truncated") }}</span>
      </div>
    </DialogContent>
  </Dialog>
</template>
