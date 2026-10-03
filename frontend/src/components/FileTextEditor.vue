<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch, watchEffect } from "vue";
import { useI18n } from "vue-i18n";
import { useFileApi, WriteConflictError } from "@/composables/useFileApi";
import { Button } from "@/components/ui/button";
import { useConfirm } from "@/composables/useConfirm";

import { EditorView, type ViewUpdate } from "@codemirror/view";
import type { EditorState, StateEffect, Text } from "@codemirror/state";
import {
  compartments,
  createBufferState,
  createEditorView,
  createEmptyState,
  detectLanguage,
  fontSizeExtension,
  formatJSON,
  themeExtension,
  wrapExtension,
  type EditorTheme,
} from "@/lib/codeEditor";
import { errorMessage } from "@/lib/errorMessage";

const props = defineProps<{
  userId: string;
  path: string;
  // 1-based line to scroll to and put the cursor on once the buffer is up.
  // 0 means "no target" — the editor opens at the top as usual. Set when the
  // file was reached from a content-search hit.
  revealLine?: number;
}>();
const emit = defineEmits<{
  preview: [];
  // Lets the tab bar mark which tabs hold unsaved work. Emitted rather than
  // exposed because the parent renders it, and a template ref into a child's
  // computed is the fiddlier half of that contract.
  dirtyPaths: [paths: string[]];
}>();

const { readFile, writeFile } = useFileApi();
const { confirm } = useConfirm();
const { t } = useI18n();

const filePath = computed(() => props.path);
const fileName = computed(() => filePath.value.split("/").pop() ?? "");

// ── Open buffers ───────────────────────────────────────────────────
// One CodeMirror view, one EditorState per open file. Switching tabs is
// view.setState() plus a scroll restore; the state carries the document,
// selection and undo history, so all of it survives a round-trip through
// another tab — none of that outlives the mount/unmount cycle a per-tab
// editor would go through.
//
// Deliberately a plain Map, not a ref: EditorState is an immutable graph that
// Vue must not wrap in a reactive proxy. Anything the template needs to see
// lives in `fileState` instead.
interface Buffer {
  // Authoritative only while the buffer is in the background; the mounted
  // buffer's live state is view.state, stashed back here on switch-away.
  state: EditorState;
  etag: string;
  // The document at the last load or save. Comparing against content rather
  // than setting a flag per keystroke is what makes "dirty" clear again when
  // the user undoes all the way back to the saved text.
  savedDoc: Text;
  scroll: StateEffect<unknown> | null;
}
const buffers = new Map<string, Buffer>();

interface FileState {
  dirty: boolean;
  // Terminal for this file: the buffer never arrived, so there is nothing
  // trustworthy to write back and saving would truncate it.
  loadError: string;
  // Transient: the buffer is intact and the edits are still in it, so a
  // 413/500/offline blip must not cost the user their work.
  saveError: string;
  // Neither of the above: the request was fine but the file moved underneath
  // us. Needs a decision (keep mine / take theirs), not a retry.
  conflict: boolean;
  savedAt: number | null;
}
const fileState = ref<Record<string, FileState>>({});

function blankState(): FileState {
  return { dirty: false, loadError: "", saveError: "", conflict: false, savedAt: null };
}

function ensureState(path: string): FileState {
  const existing = fileState.value[path];
  if (existing) return existing;
  fileState.value[path] = blankState();
  // Read it back out: the assignment stored a raw object but the record is a
  // reactive proxy, and mutating the raw one would update nothing on screen.
  return fileState.value[path];
}

const EMPTY_STATE = blankState();
const active = computed<FileState>(() => fileState.value[props.path] ?? EMPTY_STATE);
const dirty = computed(() => active.value.dirty);
const conflict = computed(() => active.value.conflict);
const savedAt = computed(() => active.value.savedAt);
// Failing to bring the editor up is component-wide rather than per-file: there is
// no editor at all, so no buffer is trustworthy.
const editorError = ref("");
const error = computed(() => active.value.saveError || active.value.loadError || editorError.value);

const loading = ref(true);
const saving = ref(false);
const cursor = ref({ line: 1, column: 1 });

const containerRef = ref<HTMLElement | null>(null);
// shallowRef so Vue doesn't try to deeply reactify the view — it's a
// non-plain object with internal cycles.
const editor = shallowRef<EditorView | null>(null);
// Mirrors the active buffer's text purely so the footer can show a byte count
// without reaching into the view on every render.
const currentValue = ref("");
// Which path's state is currently set on the view. Not a ref — it is only
// ever read by imperative code, and it can legitimately lag `props.path`
// while a fetch is in flight.
let mountedPath = "";
// Discards a fetch whose tab the user already switched away from.
let openRun = 0;

const dirtyPaths = computed(() =>
  Object.keys(fileState.value).filter((p) => fileState.value[p]?.dirty),
);
watch(dirtyPaths, (paths) => emit("dirtyPaths", paths), { deep: false });

// ── User preferences ───────────────────────────────────────────────
// Stored under a single key so a future schema change is one parse
// instead of a fan-out of localStorage reads/writes.
const PREFS_KEY = "daymug.editor.prefs";
const themePref = ref<"auto" | "light" | "dark">("auto");
const wordWrap = ref(true);
const fontSize = ref(13);

function loadPrefs() {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) return;
    const p = JSON.parse(raw) as Partial<{
      theme: "auto" | "light" | "dark";
      wordWrap: boolean;
      fontSize: number;
    }>;
    if (p.theme === "auto" || p.theme === "light" || p.theme === "dark") themePref.value = p.theme;
    if (typeof p.wordWrap === "boolean") wordWrap.value = p.wordWrap;
    // Clamp on read too — a tampered value otherwise locks the user into
    // an unusable font size with no in-app way to recover.
    if (typeof p.fontSize === "number" && p.fontSize >= 10 && p.fontSize <= 24) {
      fontSize.value = p.fontSize;
    }
  } catch {
    // Corrupt or unavailable storage: stick with defaults.
  }
}

function savePrefs() {
  try {
    localStorage.setItem(
      PREFS_KEY,
      JSON.stringify({
        theme: themePref.value,
        wordWrap: wordWrap.value,
        fontSize: fontSize.value,
      }),
    );
  } catch {
    // Storage disabled or full — non-fatal.
  }
}

const resolvedTheme = computed<EditorTheme>(() => {
  if (themePref.value === "auto") {
    const dark =
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-color-scheme: dark)").matches;
    return dark ? "dark" : "light";
  }
  return themePref.value;
});

watchEffect(() => {
  document.title = fileName.value ? `Edit · ${fileName.value}` : "Editor";
});

const language = computed(() => detectLanguage(filePath.value).label);
// Only whole-document JSON: a .jsonl file is one value per line and
// re-indenting it would turn it into something else.
const canFormat = computed(() => filePath.value.toLowerCase().endsWith(".json"));

const fileSize = computed(() => {
  // Reach TextEncoder via globalThis to bypass the project's ESLint
  // no-undef rule, which doesn't include modern web globals in its env
  // list. Falls back to char count when the global is somehow missing.
  const G = globalThis as unknown as { TextEncoder?: new () => { encode(s: string): Uint8Array } };
  if (!G.TextEncoder) return currentValue.value.length;
  return new G.TextEncoder().encode(currentValue.value).length;
});

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// Every buffer shares this one listener; it reports against whichever path is
// mounted, which is the only buffer the view can be editing.
function onViewUpdate(update: ViewUpdate) {
  if (update.docChanged) {
    const buf = buffers.get(mountedPath);
    if (buf) {
      currentValue.value = update.state.doc.toString();
      syncDirty(mountedPath);
    }
  }
  if (update.docChanged || update.selectionSet) syncCursor(update.state);
}

function syncCursor(state: EditorState) {
  const head = state.selection.main.head;
  const line = state.doc.lineAt(head);
  cursor.value = { line: line.number, column: head - line.from + 1 };
}

function newBufferState(doc: string): EditorState {
  return createBufferState({
    doc,
    theme: resolvedTheme.value,
    wordWrap: wordWrap.value,
    fontSize: fontSize.value,
    onSave: () => void save(),
    onUpdate: EditorView.updateListener.of(onViewUpdate),
  });
}

// A background buffer keeps whatever prefs it was created or last shown with,
// so bring it in line on the way back into the view.
function prefEffects() {
  return [
    compartments.theme.reconfigure(themeExtension(resolvedTheme.value)),
    compartments.wrap.reconfigure(wrapExtension(wordWrap.value)),
    compartments.fontSize.reconfigure(fontSizeExtension(fontSize.value)),
  ];
}

// Grammars download after the buffer is already editable — plain text first,
// colours when they arrive — so a slow chunk never shows up as a blank pane.
// A grammar that fails to load just leaves the file uncoloured.
async function attachLanguage(path: string) {
  const load = detectLanguage(path).load;
  if (!load) return;
  let ext;
  try {
    ext = await load();
  } catch {
    return;
  }
  const buf = buffers.get(path);
  if (!buf) return;
  const effects = compartments.language.reconfigure(ext);
  if (path === mountedPath && editor.value) editor.value.dispatch({ effects });
  else buf.state = buf.state.update({ effects }).state;
}

// Brings the single view up. Runs once; individual files are loaded into it
// afterwards by openPath().
async function bootstrap() {
  // Bringing the editor up is its own failure domain, and an unguarded one
  // used to reject into `void bootstrap()`: an unhandled rejection plus a
  // panel stuck on "Loading…" with no error to explain it.
  try {
    // The host might unmount before we get here (e.g. the user navigated
    // away mid-load). Bail out so we don't mount into a detached node.
    if (!containerRef.value) return;
    editor.value = createEditorView(containerRef.value);
  } catch (e) {
    const detail = errorMessage(e);
    editorError.value = `${t("editor.editorLoadFailed")} ${detail}`;
    loading.value = false;
    return;
  }
  await openPath(props.path, props.revealLine ?? 0);
}

function liveState(path: string): EditorState | null {
  const buf = buffers.get(path);
  if (!buf) return null;
  return path === mountedPath && editor.value ? editor.value.state : buf.state;
}

function syncDirty(path: string) {
  const buf = buffers.get(path);
  const st = fileState.value[path];
  const state = liveState(path);
  if (!buf || !st || !state) return;
  st.dirty = !state.doc.eq(buf.savedDoc);
}

// Makes `path` the visible buffer, fetching it first if this is its first
// appearance. Already-open files switch synchronously — that immediacy is the
// whole point of holding the states rather than remounting per file.
async function openPath(path: string, revealLine = 0) {
  const view = editor.value;
  if (!view) return;
  if (!path) {
    stashActive();
    mountedPath = "";
    view.setState(createEmptyState());
    loading.value = false;
    return;
  }

  const run = ++openRun;
  const st = ensureState(path);
  let buf = buffers.get(path);

  if (!buf) {
    loading.value = true;
    try {
      const res = await readFile(props.userId, path);
      const etag = res.headers.get("ETag") ?? "";
      const text = await res.text();
      // The user may have switched tabs while this was in flight; adopting
      // the result now would yank the editor back to a file they left.
      if (run !== openRun) return;
      const state = newBufferState(text);
      buf = { state, etag, savedDoc: state.doc, scroll: null };
      buffers.set(path, buf);
      st.loadError = "";
      void attachLanguage(path);
    } catch (e) {
      if (run !== openRun) return;
      st.loadError = errorMessage(e);
      loading.value = false;
      return;
    } finally {
      if (run === openRun) loading.value = false;
    }
  }

  if (run !== openRun) return;
  stashActive();
  view.setState(buf.state);
  mountedPath = path;
  view.dispatch({ effects: [...prefEffects(), ...(buf.scroll ? [buf.scroll] : [])] });
  currentValue.value = view.state.doc.toString();
  if (revealLine > 0) {
    const line = view.state.doc.line(Math.min(revealLine, view.state.doc.lines));
    view.dispatch({
      selection: { anchor: line.from },
      effects: EditorView.scrollIntoView(line.from, { y: "center" }),
    });
  }
  syncCursor(view.state);
  view.focus();
  syncDirty(path);
}

// The live state and scroll position belong to the outgoing tab, not the
// view, so they have to be captured before the next state is swapped in.
function stashActive() {
  const view = editor.value;
  const buf = buffers.get(mountedPath);
  if (!view || !buf) return;
  buf.state = view.state;
  buf.scroll = view.scrollSnapshot();
}

// `force` drops the If-Match header, which is the user answering "keep mine"
// to a conflict. Everything else goes through the compare-and-swap so an
// agent's concurrent rewrite can never be lost silently.
async function save(force = false) {
  const path = props.path;
  const buf = buffers.get(path);
  const st = fileState.value[path];
  const state = liveState(path);
  // Only a failed *load* blocks saving — retrying after a failed save is the
  // whole point, and the previous attempt's message must not latch the button.
  if (saving.value || !path || !buf || !st || !state || st.loadError) return;
  saving.value = true;
  st.saveError = "";
  try {
    const result = await writeFile(
      props.userId,
      path,
      state.doc.toString(),
      force ? undefined : buf.etag,
    );
    // Adopt the post-write validator so the next save compares against what
    // we just wrote rather than the version we originally read.
    buf.etag = result.etag ?? "";
    // What was written, not what's in the view now: keystrokes that landed
    // while the request was in flight are still unsaved.
    buf.savedDoc = state.doc;
    st.conflict = false;
    st.savedAt = Date.now();
    syncDirty(path);
  } catch (e) {
    if (e instanceof WriteConflictError) {
      st.conflict = true;
      // Remember where the file actually is now, so "overwrite" is a single
      // deliberate click rather than a race the user has to win twice.
      if (e.etag) buf.etag = e.etag;
    } else {
      st.saveError = errorMessage(e);
    }
  } finally {
    saving.value = false;
  }
}

async function revert() {
  const path = props.path;
  if (!path) return;
  const st = ensureState(path);
  if (
    st.dirty &&
    !(await confirm({
      message: t("editor.discardChangesConfirm"),
      variant: "destructive",
    }))
  )
    return;
  await reloadBuffer(path);
}

// Re-read a buffer from disk, discarding whatever it holds. Unconditional by
// design: the guard about losing unsaved work belongs to the caller, because
// the two callers answer it differently — revert() asks the user, while the
// workbench's disk-change sync only calls this once it knows the buffer is
// clean or the user has chosen to reload.
async function reloadBuffer(path: string) {
  if (!path) return;
  const st = ensureState(path);
  // Re-reading the file clears every channel: it replaces the buffer, so a
  // stale save failure no longer describes anything on screen.
  st.loadError = "";
  st.saveError = "";
  st.conflict = false;
  const buf = buffers.get(path);
  if (!buf) {
    await openPath(path);
    return;
  }
  try {
    const res = await readFile(props.userId, path);
    const text = await res.text();
    buf.etag = res.headers.get("ETag") ?? "";
    // A fresh state, not an edit: undoing past a reload into content that
    // is no longer on disk would only manufacture a conflict.
    const state = newBufferState(text);
    buf.savedDoc = state.doc;
    const view = editor.value;
    if (path === mountedPath && view) {
      const top = view.scrollDOM.scrollTop;
      view.setState(state);
      view.scrollDOM.scrollTop = top;
      syncCursor(view.state);
    } else {
      buf.state = state;
      // The old snapshot points into a document that no longer exists.
      buf.scroll = null;
    }
    void attachLanguage(path);
    if (path === props.path) currentValue.value = text;
    syncDirty(path);
  } catch (e) {
    st.loadError = errorMessage(e);
  }
}

// Whether a specific buffer holds unsaved edits. The workbench asks before
// letting a disk change overwrite anything.
function isDirty(path: string): boolean {
  return fileState.value[path]?.dirty ?? false;
}

// Called by the tab bar when a tab is closed. Dropping the entry is what frees
// the buffer and its undo stack; the view is cleared first if it is showing
// it, or it would keep the whole state alive.
function closeFile(path: string) {
  if (buffers.has(path)) {
    if (mountedPath === path) {
      mountedPath = "";
      editor.value?.setState(createEmptyState());
    }
    buffers.delete(path);
  }
  delete fileState.value[path];
}

function formatDocument() {
  const view = editor.value;
  if (!view || !canFormat.value) return;
  const formatted = formatJSON(view.state.doc.toString());
  if (formatted === null) return;
  // One transaction, so a single undo restores the original layout.
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: formatted } });
}

function adjustFont(delta: number) {
  fontSize.value = Math.max(10, Math.min(24, fontSize.value + delta));
}

function cycleTheme() {
  themePref.value =
    themePref.value === "auto" ? "light" : themePref.value === "light" ? "dark" : "auto";
}

async function goToPreview() {
  if (dirty.value || !filePath.value) return;
  emit("preview");
}

// The view outlives a file switch, so the active buffer is driven by the prop
// rather than by a remount.
watch(
  () => props.path,
  (path) => {
    void openPath(path, props.revealLine ?? 0);
  },
);

// ── Reactive wiring of prefs into the editor ──────────────────────
// Only the mounted buffer is reconfigured here; background buffers catch up
// in openPath() when they are shown again.
watch(resolvedTheme, (theme) => {
  editor.value?.dispatch({ effects: compartments.theme.reconfigure(themeExtension(theme)) });
  savePrefs();
});
watch(wordWrap, (v) => {
  editor.value?.dispatch({ effects: compartments.wrap.reconfigure(wrapExtension(v)) });
  savePrefs();
});
watch(fontSize, (v) => {
  editor.value?.dispatch({ effects: compartments.fontSize.reconfigure(fontSizeExtension(v)) });
  savePrefs();
});
// themePref drives resolvedTheme; persist explicitly so toggling to/from
// "auto" without crossing a light/dark boundary still writes back.
watch(themePref, savePrefs);

// Surface the standard browser confirmation when the user tries to close
// the tab with unsaved changes. We only attach the listener once mounted
// so SSR/test environments without `window` stay untouched. Typed as the
// base `Event` because the ESLint config in this project doesn't expose
// the `BeforeUnloadEvent` global; `returnValue` is read off a structural
// cast instead of pulling the named DOM type in.
//
// Every open buffer counts, not just the visible one — the unsaved work in a
// background tab is exactly as easy to lose.
function beforeUnload(e: Event) {
  if (dirtyPaths.value.length === 0) return;
  e.preventDefault();
  // BeforeUnloadEvent.returnValue is typed `boolean | string` in lib.dom
  // and the global type itself isn't exposed by the project's ESLint
  // config, so the assignment goes through a structural cast via unknown.
  (e as unknown as { returnValue: string }).returnValue = "";
}

onMounted(() => {
  loadPrefs();
  window.addEventListener("beforeunload", beforeUnload);
  void bootstrap();
});

onBeforeUnmount(() => {
  window.removeEventListener("beforeunload", beforeUnload);
  buffers.clear();
  editor.value?.destroy();
  editor.value = null;
});

defineExpose({ closeFile, reloadFile: reloadBuffer, isDirty });

const savedLabel = computed(() => {
  if (!savedAt.value) return "";
  const d = new Date(savedAt.value);
  return t("editor.savedAt", { time: d.toLocaleTimeString() });
});

const themeName = computed(() => {
  if (themePref.value === "auto") return t("editor.themeAuto");
  return themePref.value === "dark" ? t("editor.themeDark") : t("editor.themeLight");
});
</script>

<template>
  <main class="flex min-w-0 flex-1 flex-col">
    <header
      class="flex items-center justify-between gap-3 px-4 py-2 border-b border-border bg-muted shrink-0"
    >
      <div class="flex items-center gap-2 min-w-0">
        <span class="text-sm font-semibold text-foreground truncate">{{ fileName }}</span>
        <span v-if="dirty" class="text-[11px] text-muted-foreground"
          >•&nbsp;{{ t("editor.unsaved") }}</span
        >
        <span v-else-if="savedLabel" class="text-[11px] text-muted-foreground">{{
          savedLabel
        }}</span>
      </div>
      <div class="flex items-center gap-1.5 shrink-0">
        <span
          v-if="error"
          class="text-[11px] text-destructive max-w-[40ch] truncate mr-2"
          :title="error"
          >{{ error }}</span
        >
        <Button
          v-if="canFormat"
          variant="ghost"
          size="sm"
          class="h-7 px-2 text-xs"
          :title="t('editor.formatTitle')"
          :disabled="loading"
          @click="formatDocument"
          >{{ t("editor.format") }}</Button
        >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2 text-xs"
          :title="t('editor.themeTitle', { name: themeName })"
          @click="cycleTheme"
          >{{ themeName }}</Button
        >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2 text-xs"
          :title="wordWrap ? t('editor.wrapOnTitle') : t('editor.wrapOffTitle')"
          @click="wordWrap = !wordWrap"
          >{{
            t("editor.wrapLabel", { state: wordWrap ? t("editor.on") : t("editor.off") })
          }}</Button
        >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 w-7 p-0 text-xs"
          :title="t('editor.fontDecrease')"
          :disabled="fontSize <= 10"
          @click="adjustFont(-1)"
          >A−</Button
        >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 w-7 p-0 text-xs"
          :title="t('editor.fontIncrease')"
          :disabled="fontSize >= 24"
          @click="adjustFont(1)"
          >A＋</Button
        >
        <Button
          variant="ghost"
          size="sm"
          class="h-7 px-2 text-xs"
          :title="t('editor.revertTitle')"
          :disabled="loading"
          @click="revert"
          >{{ t("editor.revert") }}</Button
        >
        <Button
          variant="outline"
          size="sm"
          class="h-7 px-3 text-xs"
          :title="dirty ? t('editor.previewSaveFirst') : t('editor.previewOpen')"
          :disabled="dirty || loading || !filePath"
          @click="goToPreview"
          >{{ t("editor.preview") }}</Button
        >
        <Button
          size="sm"
          class="h-7 px-3 text-xs"
          :disabled="!dirty || saving || loading"
          @click="save()"
          >{{ saving ? t("common.saving") : t("common.save") }}</Button
        >
      </div>
    </header>
    <div
      v-if="conflict"
      class="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2 border-b border-destructive/40 bg-destructive/10 shrink-0"
    >
      <div class="min-w-0 flex-1">
        <p class="text-xs font-semibold text-destructive">{{ t("editor.conflictTitle") }}</p>
        <p class="text-[11px] text-muted-foreground">{{ t("editor.conflictBody") }}</p>
      </div>
      <div class="flex items-center gap-1.5 shrink-0">
        <Button variant="outline" size="sm" class="h-7 px-2 text-xs" @click="revert">{{
          t("editor.conflictReload")
        }}</Button>
        <Button
          variant="destructive"
          size="sm"
          class="h-7 px-2 text-xs"
          :disabled="saving"
          @click="save(true)"
          >{{ t("editor.conflictOverwrite") }}</Button
        >
      </div>
    </div>
    <div
      v-if="loading"
      class="flex-1 flex items-center justify-center text-muted-foreground text-sm"
    >
      {{ t("common.loading") }}
    </div>
    <div v-show="!loading" ref="containerRef" class="flex-1 min-h-0 overflow-hidden" />
    <footer
      v-show="!loading && !error"
      class="flex items-center justify-between gap-3 px-4 h-6 border-t border-border bg-muted shrink-0 text-[11px] font-mono text-muted-foreground"
    >
      <div class="flex items-center gap-3">
        <span>{{ language }}</span>
        <span>{{ formatSize(fileSize) }}</span>
        <span v-if="savedAt && !dirty">{{ savedLabel }}</span>
        <span v-else-if="dirty" class="text-destructive">{{ t("editor.unsaved") }}</span>
      </div>
      <div>{{ t("editor.cursor", { line: cursor.line, column: cursor.column }) }}</div>
    </footer>
  </main>
</template>
