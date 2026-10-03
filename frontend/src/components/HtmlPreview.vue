<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";

import { useFileWatch } from "@/composables/useFileWatch";
import { useFileApi, workspacePathFromPreviewUrl } from "@/composables/useFileApi";

// Renders an HTML file as the page it is, not as source.
//
// The frame is pointed at /files/preview/<path>, whose URL *is* the workspace
// path, so every relative reference inside the page — scripts, styles, wasm,
// fonts, files in subdirectories — resolves to the file sitting next to it on
// disk and is served by the same endpoint.
const props = withDefaults(
  defineProps<{
    userId: string;
    path: string;
    // Bumped by the host when it learns the file changed (its own watch, or a
    // manual refresh). Distinct from this component's asset watch below: the
    // host knows about the .html, we know about what the page loaded.
    reloadToken?: number;
  }>(),
  { reloadToken: 0 },
);

const { previewFileUrl } = useFileApi();

// allow-same-origin is deliberate and load-bearing: without it the page gets
// an opaque origin, which costs it localStorage, IndexedDB and every
// fetch()-based load (including WebAssembly.instantiateStreaming) — the bulk
// of what a generated single-page app does. What the sandbox still withholds
// is top-level navigation, so a previewed page cannot yank the workbench out
// from under the user.
const SANDBOX =
  "allow-scripts allow-same-origin allow-forms allow-modals allow-popups allow-downloads allow-popups-to-escape-sandbox";

// Matches filewatch.MaxPathsPerSubscription on the server. A page with more
// resources than this keeps the ones discovered first — the document and its
// early scripts and styles, which is what an edit usually touches.
const MAX_WATCHED_PATHS = 16;

// One reload for a burst: a rebuild rewrites the html and half a dozen assets,
// and each arrives as its own change event.
const RELOAD_DEBOUNCE_MS = 120;

const frame = ref<HTMLIFrameElement | null>(null);
const src = computed(() => previewFileUrl(props.userId, props.path));

// The document is always watched; the rest is whatever the page turned out to
// load, which is the only honest definition of "the files this preview
// depends on".
const watchedPaths = ref<string[]>([props.path]);

// The slice of PerformanceObserver this component uses, taken from the
// frame's own realm (constructing it from ours would observe our window).
interface FrameObserver {
  observe(options: { type: string; buffered: boolean }): void;
  disconnect(): void;
}
let observer: FrameObserver | null = null;

function disconnectObserver() {
  try {
    observer?.disconnect();
  } catch {
    // The frame navigated away and took its realm with it.
  }
  observer = null;
}

// Reads back what the framed page actually fetched and keeps the paths that
// belong to this workspace. Everything here needs same-origin access to the
// frame; a page that navigated itself to another site simply stops
// contributing, and we keep watching what we already know about.
function collectLoadedAssets() {
  const win = frame.value?.contentWindow;
  if (!win) return;
  let names: string[];
  try {
    names = win.performance.getEntriesByType("resource").map((entry) => entry.name);
  } catch {
    return;
  }
  const paths = [props.path];
  for (const name of names) {
    const path = workspacePathFromPreviewUrl(props.userId, name);
    if (path && !paths.includes(path)) paths.push(path);
    if (paths.length >= MAX_WATCHED_PATHS) break;
  }
  // Replacing the array unconditionally would re-subscribe on every observer
  // callback, and a re-subscribe costs a socket round trip.
  const current = watchedPaths.value;
  if (paths.length === current.length && paths.every((p, i) => p === current[i])) return;
  watchedPaths.value = paths;
}

// Lazily loaded resources (a wasm module fetched after boot, a chunk pulled in
// on the first click) arrive long after `load` fires, so watch the frame's
// resource timeline rather than sampling it once.
function observeFrameResources() {
  disconnectObserver();
  const win = frame.value?.contentWindow as
    | { PerformanceObserver?: new (cb: () => void) => FrameObserver }
    | null
    | undefined;
  if (!win?.PerformanceObserver) return;
  try {
    const next = new win.PerformanceObserver(() => collectLoadedAssets());
    next.observe({ type: "resource", buffered: true });
    observer = next;
  } catch {
    // Not fatal: the sample taken on `load` still covers the initial page.
  }
}

// A link inside a previewed page navigates the frame itself, replacing the
// report the user was reading with no way back to it (the sandbox already
// stops it from taking the whole workbench). Default the page's links to a new
// tab instead — allow-popups is in SANDBOX for exactly this. A page that
// states its own base target keeps it.
function defaultLinksToNewTab() {
  try {
    const doc = frame.value?.contentDocument;
    const head = doc?.head;
    if (!doc || !head || head.querySelector("base[target]")) return;
    const base = doc.createElement("base");
    base.target = "_blank";
    head.prepend(base);
  } catch {
    // The page navigated cross-origin and took its document along.
  }
}

function onFrameLoad() {
  defaultLinksToNewTab();
  collectLoadedAssets();
  observeFrameResources();
}

let reloadTimer: ReturnType<typeof setTimeout> | null = null;

function clearReloadTimer() {
  if (reloadTimer !== null) {
    clearTimeout(reloadTimer);
    reloadTimer = null;
  }
}

function reloadNow() {
  reloadTimer = null;
  const el = frame.value;
  if (!el) return;
  try {
    // Reload in place rather than re-assigning src: it preserves wherever the
    // user navigated to inside the preview, and leaves no history entry.
    el.contentWindow?.location.reload();
    return;
  } catch {
    // The page navigated somewhere cross-origin; sending the frame back to
    // the file is the only move left, and the right one.
  }
  el.src = src.value;
}

function scheduleReload() {
  clearReloadTimer();
  reloadTimer = setTimeout(reloadNow, RELOAD_DEBOUNCE_MS);
}

useFileWatch({
  userId: computed(() => props.userId),
  paths: watchedPaths,
  onChanged: scheduleReload,
  // A deleted asset changes what the page renders just as much as an edited
  // one — reload and let the page show the gap.
  onRemoved: scheduleReload,
});

watch(
  () => props.path,
  (path) => {
    disconnectObserver();
    watchedPaths.value = [path];
  },
);

watch(
  () => props.reloadToken,
  () => scheduleReload(),
);

onBeforeUnmount(() => {
  clearReloadTimer();
  disconnectObserver();
});

defineExpose({ reload: scheduleReload });
</script>

<template>
  <iframe
    ref="frame"
    :src="src"
    :sandbox="SANDBOX"
    class="w-full h-full border-none bg-white"
    data-testid="html-preview-frame"
    @load="onFrameLoad"
  ></iframe>
</template>
