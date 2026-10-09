<script setup lang="ts">
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Download, PanelRightOpen, PencilLine, X } from "lucide-vue-next";

import FilePreview from "@/components/FilePreview.vue";
import FileIcon from "@/components/FileIcon.vue";
import FileQuickOpen from "@/components/FileQuickOpen.vue";
import { Button } from "@/components/ui/button";
import { useConfirm } from "@/composables/useConfirm";
import { loadTabs, saveTabs } from "@/composables/editorTabStorage";
import { useFileWatch } from "@/composables/useFileWatch";
import {
  isEditableText,
  isHtmlFile,
  shouldInspectTextFile,
  useFileApi,
  type SearchMode,
} from "@/composables/useFileApi";
import { isReadOnly as isOfficeReadOnly, officeAppFor } from "@/lib/office/files";

// The file surface shared by the standalone preview page and the chat page's
// inline panel. Everything that decides *how* a file is shown lives here —
// edit-mode detection, the office-file special case, dirty-buffer guards — so
// the two mounts can't drift apart. The parent owns only *which* file and
// where that choice is persisted.
const props = withDefaults(
  defineProps<{
    userId: string;
    path: string;
    // "The parent would like this file opened for editing." Advisory: a
    // office file is always edit-only and a binary is never editable, so the
    // workbench resolves the real mode and reports it back.
    editMode?: boolean;
    revealLine?: number;
    // Close affordance. The preview page is the whole page and has nowhere to
    // go, so it hides it; the chat panel shows it.
    showClose?: boolean;
    // Keeps the two mounts' tab lists apart.
    storageScope?: string;
    enableQuickOpen?: boolean;
    // Extra controls the host wants in the preview header (the preview page
    // puts its file-panel expand button here).
    showPanelExpand?: boolean;
    // Artifact panels render their own persistent file tabs above this
    // workbench; standalone/editor hosts keep the built-in tab strip.
    showTabs?: boolean;
  }>(),
  {
    editMode: false,
    revealLine: 0,
    showClose: false,
    storageScope: "preview",
    enableQuickOpen: true,
    showPanelExpand: false,
    showTabs: true,
  },
);

const emit = defineEmits<{
  // Navigation inside the workbench (tab activation, quick-open). One event
  // rather than separate path/line updates so the host can apply both in a
  // single router push.
  open: [payload: { path: string; line: number }];
  "update:editMode": [on: boolean];
  close: [];
  expandPanel: [];
}>();

const { t } = useI18n();
const { downloadFileUrl, inspectFile } = useFileApi();
const { confirm } = useConfirm();

const FileTextEditor = defineAsyncComponent(() =>
  import("@/components/FileTextEditor.vue").then((mod) => mod.default),
);

const fileName = computed(() => props.path.split("/").pop() ?? "");

// ── Editor tabs ────────────────────────────────────────────────────
// The workbench owns the tab list because it owns the buffers behind them.
// Closing a tab therefore needs both — a splice here and a closeFile() on the
// editor, or the model leaks.
const openPaths = ref<string[]>([]);
const dirtyTabs = ref<string[]>([]);
const editorRef = ref<{
  closeFile: (path: string) => void;
  reloadFile: (path: string) => Promise<void>;
  isDirty: (path: string) => boolean;
} | null>(null);

// Order matters: restore before the save watcher exists and before the
// textEditMode watcher below pushes the file this workbench opened with.
// Deferring to onMounted overwrites the stored set with that single path.
openPaths.value = loadTabs(props.storageScope, props.userId);
watch(openPaths, () => saveTabs(props.storageScope, props.userId, openPaths.value), {
  deep: true,
});

function tabName(path: string): string {
  return path.split("/").pop() ?? path;
}

function activateTab(path: string) {
  if (path === props.path) return;
  // Line 0 drops any reveal target from the previous jump — it belongs to the
  // file we are leaving and would scroll the new tab somewhere random.
  emit("open", { path, line: 0 });
}

async function closeTab(path: string) {
  if (
    dirtyTabs.value.includes(path) &&
    !(await confirm({
      message: t("editor.closeDirtyConfirm", { name: tabName(path) }),
      variant: "destructive",
    }))
  )
    return;

  const index = openPaths.value.indexOf(path);
  if (index < 0) return;
  openPaths.value.splice(index, 1);
  editorRef.value?.closeFile(path);
  if (path !== props.path) return;

  // Closing the visible tab has to leave the workbench somewhere valid. Prefer
  // the tab on the left (what the user was most likely reading before), else
  // the one that slid into this slot.
  const next = openPaths.value[index - 1] ?? openPaths.value[index];
  if (next) {
    activateTab(next);
    return;
  }
  // Nothing left to show. Dropping to preview keeps the current path on screen
  // rather than stranding the user on a blank surface.
  setEditMode(false);
}

// ── Quick open ─────────────────────────────────────────────────────
const quickOpen = ref(false);
const quickOpenMode = ref<SearchMode>("name");

function openQuickOpen(mode: SearchMode) {
  quickOpenMode.value = mode;
  quickOpen.value = true;
}

function onQuickOpenSelect(payload: { path: string; line: number }) {
  emit("open", payload);
}

// ── Mode resolution ────────────────────────────────────────────────
function isOffice(path: string): boolean {
  return officeAppFor(path) !== null;
}

// Office files are edit-only: the embedded editor is the only useful view, so
// we skip the read-only preview entirely and land straight in edit mode. The
// editor itself refuses edits where they would be destructive (.xlsm, legacy
// .xls/.ods). Other files honour the parent's request; without one, preview.
function resolveEditMode(): boolean {
  return isOffice(props.path) || (props.editMode && isEditableText(props.path));
}

const resolvedEdit = ref(resolveEditMode());

function setEditMode(on: boolean) {
  if (resolvedEdit.value === on) return;
  resolvedEdit.value = on;
}

// An HTML file has three faces: the rendered page, its source, and the
// editor. The page is the default — double-clicking an .html in the file
// panel almost always means "show me what this looks like" — and the toggle
// below covers the other two.
const isHtml = computed(() => isHtmlFile(props.path));
const htmlWebView = ref(true);

const inspectedText = ref(false);
let inspectRun = 0;
const textEditable = computed(
  () => props.path !== "" && (isEditableText(props.path) || inspectedText.value),
);
const textEditMode = computed(() => resolvedEdit.value && textEditable.value);
const officeEditMode = computed(() => resolvedEdit.value && isOffice(props.path));
// docs autosaves and has no host-triggered save, and the view-only formats
// have nothing to save — in both cases a Save button would be a lie.
const officeSavable = computed(
  () => officeAppFor(props.path) === "sheet" && !isOfficeReadOnly(props.path),
);
const officeReadOnlyNote = computed(() =>
  isOfficeReadOnly(props.path) ? t("files.office.readOnlyNote") : t("files.office.autosaveNote"),
);
const editable = computed(() => props.path !== "" && (textEditable.value || isOffice(props.path)));

const filePreviewRef = ref<{
  refresh: () => void;
  save: () => Promise<boolean>;
  isDirty: () => boolean;
} | null>(null);
const saving = ref(false);
const saved = ref(false);

// Drive the embedded editor's save — the Save controls live in this header.
async function save() {
  if (saving.value) return;
  saving.value = true;
  saved.value = false;
  try {
    const ok = await filePreviewRef.value?.save();
    if (ok) saved.value = true;
  } finally {
    saving.value = false;
  }
}

// Intercept Cmd/Ctrl+S so the browser's "save page" dialog never fires while
// editing an office file — route it to the in-app save instead. Only active in
// edit mode; preview leaves the shortcut to the browser.
function onKeydown(e: KeyboardEvent) {
  // Quick-open works in preview as well as edit mode — navigating the
  // workspace is not an editing action.
  if (props.enableQuickOpen && (e.metaKey || e.ctrlKey) && !e.altKey) {
    // Both shortcuts collide with a browser default (print / the page's own
    // find), which is the accepted trade every web IDE makes.
    if (e.key === "p" || e.key === "P") {
      e.preventDefault();
      openQuickOpen("name");
      return;
    }
    if (e.shiftKey && (e.key === "f" || e.key === "F")) {
      e.preventDefault();
      openQuickOpen("content");
      return;
    }
  }
  if (!resolvedEdit.value) return;
  if ((e.metaKey || e.ctrlKey) && !e.altKey && (e.key === "s" || e.key === "S")) {
    e.preventDefault();
    void save();
  }
}

// Warn before the tab is closed (or reloaded) while the editor holds
// unsaved edits. Browsers ignore custom text here and show their own generic
// "Leave site?" prompt — setting returnValue is what triggers it. Typed as the
// base `Event` because the project's ESLint config doesn't expose the
// `BeforeUnloadEvent` global (same approach as the text editor component).
function beforeUnload(e: Event) {
  if (!filePreviewRef.value?.isDirty()) return;
  e.preventDefault();
  (e as unknown as { returnValue: string }).returnValue = "";
}

// Every exit from the inline workbench funnels through this method (the X,
// the back-to-conversations button and Escape). Checking all text buffers plus
// the office surface here prevents a host-level close from bypassing the
// unsaved-work guard.
async function requestClose(): Promise<boolean> {
  const hasUnsavedChanges = dirtyTabs.value.length > 0 || !!filePreviewRef.value?.isDirty();
  if (
    hasUnsavedChanges &&
    !(await confirm({
      message: t("files.workbench.closeDirtyConfirm"),
      variant: "destructive",
    }))
  ) {
    return false;
  }
  emit("close");
  return true;
}

// Artifact hosts need the same guard when a tab switch or directory toggle
// would replace the visible editor without closing the whole workbench.
async function requestNavigateAway(): Promise<boolean> {
  const hasUnsavedChanges =
    dirtyTabs.value.includes(props.path) || !!filePreviewRef.value?.isDirty();
  if (!hasUnsavedChanges) return true;
  return confirm({
    message: t("files.workbench.closeDirtyConfirm"),
    variant: "destructive",
  });
}

// Close one artifact tab without applying the whole-workbench dirty check to
// unrelated buffers. The artifact host owns navigation after this succeeds.
async function requestClosePath(path: string): Promise<boolean> {
  const hasUnsavedChanges =
    dirtyTabs.value.includes(path) || (path === props.path && !!filePreviewRef.value?.isDirty());
  if (
    hasUnsavedChanges &&
    !(await confirm({
      message: t("editor.closeDirtyConfirm", { name: tabName(path) }),
      variant: "destructive",
    }))
  ) {
    return false;
  }

  const index = openPaths.value.indexOf(path);
  if (index >= 0) openPaths.value.splice(index, 1);
  editorRef.value?.closeFile(path);
  return true;
}

// A file becomes a tab once it is genuinely open in the editor — not merely
// present in the URL, which is also true of images and spreadsheets.
watch(
  textEditMode,
  (on) => {
    if (!on || !props.path) return;
    if (!openPaths.value.includes(props.path)) openPaths.value.push(props.path);
  },
  { immediate: true },
);

watch(
  () => props.path,
  () => {
    if (textEditMode.value && props.path && !openPaths.value.includes(props.path)) {
      openPaths.value.push(props.path);
    }
    inspectedText.value = false;
    resolvedEdit.value = resolveEditMode();
    htmlWebView.value = true;
    saved.value = false;
  },
);

watch(
  () => props.path,
  async (path) => {
    const run = ++inspectRun;
    const wantsEdit = props.editMode;
    inspectedText.value = false;
    if (!path || !shouldInspectTextFile(path)) return;
    try {
      const info = await inspectFile(props.userId, path);
      if (run !== inspectRun) return;
      inspectedText.value = info.is_text;
      if (info.is_text && wantsEdit) {
        resolvedEdit.value = true;
      }
    } catch {
      if (run === inspectRun) inspectedText.value = false;
    }
  },
  { immediate: true },
);

// Report the resolved mode back so the host can persist it (URL on the preview
// page, the file-panel store in chat). Immediate so a plain preview normalises
// to "not editing" rather than leaving a stale request in place.
watch(resolvedEdit, (on) => emit("update:editMode", on), { immediate: true });

onMounted(() => {
  window.addEventListener("keydown", onKeydown);
  window.addEventListener("beforeunload", beforeUnload);
});
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKeydown);
  window.removeEventListener("beforeunload", beforeUnload);
});

defineExpose({ requestClose, requestNavigateAway, requestClosePath });

function goToEditor() {
  if (!editable.value) return;
  setEditMode(true);
}

function goToPreview() {
  setEditMode(false);
}

// ── Disk-change sync ───────────────────────────────────────────────
// Paths worth watching: every open editor tab, plus whatever the preview is
// showing. Nothing else — the watch budget is per-file on purpose.
const watchedPaths = computed(() => {
  const paths = new Set(openPaths.value);
  if (props.path) paths.add(props.path);
  return [...paths];
});

// Files whose on-disk copy has moved on while the buffer holds unsaved edits.
// Kept as a set rather than a boolean so switching tabs shows the right state
// for each file.
const diskConflicts = ref<string[]>([]);
const removedPaths = ref<string[]>([]);

const currentConflict = computed(() => diskConflicts.value.includes(props.path));
const currentRemoved = computed(() => removedPaths.value.includes(props.path));

function clearNotices(path: string) {
  diskConflicts.value = diskConflicts.value.filter((p) => p !== path);
  removedPaths.value = removedPaths.value.filter((p) => p !== path);
}

function onDiskChanged(path: string) {
  removedPaths.value = removedPaths.value.filter((p) => p !== path);
  if (textEditMode.value && editorRef.value?.isDirty(path)) {
    // Never silently overwrite work in progress. Reloading here would throw
    // away whatever the user is typing, and that is the one failure this
    // feature must not have.
    if (!diskConflicts.value.includes(path)) diskConflicts.value = [...diskConflicts.value, path];
    return;
  }
  if (textEditMode.value) {
    void editorRef.value?.reloadFile(path);
    return;
  }
  if (path === props.path) filePreviewRef.value?.refresh();
}

function onDiskRemoved(path: string) {
  diskConflicts.value = diskConflicts.value.filter((p) => p !== path);
  if (!removedPaths.value.includes(path)) removedPaths.value = [...removedPaths.value, path];
}

useFileWatch({
  userId: computed(() => props.userId),
  paths: watchedPaths,
  onChanged: onDiskChanged,
  onRemoved: onDiskRemoved,
});

function reloadFromDisk() {
  const path = props.path;
  clearNotices(path);
  if (textEditMode.value) void editorRef.value?.reloadFile(path);
  else filePreviewRef.value?.refresh();
}

function keepMyChanges() {
  clearNotices(props.path);
}

watch(
  () => props.path,
  (path, previous) => {
    // A notice describes one file; carrying it across a tab switch would
    // accuse the wrong buffer.
    if (previous) clearNotices(previous);
    clearNotices(path);
  },
);
</script>

<template>
  <main class="relative flex flex-1 min-w-0 min-h-0 flex-col">
    <!-- Read-only preview controls. `min-h-11` (not `py-*`) is what keeps this
         bar exactly as tall as the workspace panel's breadcrumb bar, so the two
         bottom borders meet as one line across the divider. -->
    <header
      v-if="!resolvedEdit"
      class="flex h-11 shrink-0 items-center justify-between gap-2 border-b border-[#e6e6e6] bg-card pl-3 pr-2 dark:border-border"
    >
      <span class="flex min-w-0 items-center gap-[7px] text-[14px] font-semibold text-foreground">
        <FileIcon :is-dir="false" :file-name="fileName" :size="17" />
        <span class="truncate">{{ fileName }}</span>
      </span>
      <div class="flex shrink-0 items-center gap-2.5">
        <button
          v-if="path && showPanelExpand"
          type="button"
          class="grid size-7 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-[#efeff1] hover:text-foreground dark:hover:bg-[#242424]"
          :title="t('chat.showWorkspacePanel')"
          data-testid="preview-file-panel-expand"
          @click="emit('expandPanel')"
        >
          <PanelRightOpen :size="16" />
        </button>
        <div
          v-if="isHtml"
          class="flex items-center overflow-hidden rounded-md border border-border"
          data-testid="html-view-toggle"
        >
          <button
            v-for="view in [
              { web: true, label: t('files.preview.viewPage'), testid: 'html-view-page' },
              { web: false, label: t('files.preview.viewSource'), testid: 'html-view-source' },
            ]"
            :key="view.testid"
            type="button"
            class="h-7 px-2.5 text-xs transition-colors"
            :class="
              htmlWebView === view.web
                ? 'bg-background text-foreground font-medium'
                : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground'
            "
            :data-testid="view.testid"
            :data-active="htmlWebView === view.web"
            @click="htmlWebView = view.web"
          >
            {{ view.label }}
          </button>
        </div>
        <Button
          v-if="editable"
          variant="outline"
          size="sm"
          class="h-[30px] gap-1 rounded-[7px] px-[7px] text-[12px] font-normal"
          :title="t('files.preview.openInEditor')"
          @click="goToEditor"
          ><PencilLine :size="13" />{{ t("common.edit") }}</Button
        >
        <a
          v-if="path"
          :href="downloadFileUrl(props.userId, path)"
          download
          class="grid size-7 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-[#efeff1] hover:text-foreground dark:hover:bg-[#242424]"
          :aria-label="t('common.download')"
        >
          <Download :size="16" />
        </a>
        <button
          v-if="showClose"
          type="button"
          class="grid size-7 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-[#efeff1] hover:text-foreground dark:hover:bg-[#242424]"
          :title="t('files.workbench.close')"
          data-testid="workbench-close"
          @click="requestClose"
        >
          <X :size="16" />
        </button>
      </div>
    </header>

    <!-- Office edit mode. Unlike the text editor this is a plain bar rather
         than an overlay: the embedded editor puts its own menu bar across the
         top of its frame, so anything floating over it would collide with
         File/Edit/View rather than share a row the way the old grid's
         centred ribbon tabs allowed. -->
    <header
      v-if="officeEditMode"
      class="flex h-11 shrink-0 items-center justify-between gap-2 border-b border-[#e6e6e6] bg-card pl-3 pr-2 dark:border-border"
    >
      <span class="flex min-w-0 items-center gap-[7px] text-[14px] font-semibold text-foreground">
        <FileIcon :is-dir="false" :file-name="fileName" :size="17" />
        <span class="truncate">{{ fileName }}</span>
      </span>
      <div class="flex items-center gap-2.5 shrink-0">
        <span v-if="saved" class="text-xs text-muted-foreground">{{
          t("files.spreadsheet.saved")
        }}</span>
        <button
          v-if="path && showPanelExpand"
          type="button"
          class="grid size-7 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-[#efeff1] hover:text-foreground dark:hover:bg-[#242424]"
          :title="t('chat.showWorkspacePanel')"
          data-testid="preview-file-panel-expand"
          @click="emit('expandPanel')"
        >
          <PanelRightOpen :size="16" />
        </button>
        <Button
          v-if="officeSavable"
          size="sm"
          class="h-[30px] rounded-[7px] px-[7px] text-[12px]"
          :disabled="saving"
          @click="save()"
          >{{ saving ? t("files.spreadsheet.saving") : t("files.spreadsheet.save") }}</Button
        >
        <span v-else class="text-xs text-muted-foreground">{{ officeReadOnlyNote }}</span>
        <Button
          v-if="path"
          variant="outline"
          size="sm"
          class="h-[30px] rounded-[7px] px-[7px] text-[12px]"
          as-child
        >
          <a :href="downloadFileUrl(props.userId, path)" download>{{ t("common.download") }}</a>
        </Button>
        <button
          v-if="showClose"
          type="button"
          class="grid size-7 place-items-center rounded-lg text-muted-foreground transition-colors hover:bg-[#efeff1] hover:text-foreground dark:hover:bg-[#242424]"
          :title="t('files.workbench.close')"
          data-testid="workbench-close"
          @click="requestClose"
        >
          <X :size="16" />
        </button>
      </div>
    </header>

    <div
      v-if="showTabs && textEditMode && openPaths.length > 0"
      class="flex shrink-0 items-stretch overflow-x-auto border-b border-border bg-muted"
      data-testid="editor-tabs"
    >
      <div
        v-for="p in openPaths"
        :key="p"
        class="group flex shrink-0 items-center gap-1.5 border-r border-border px-3 py-1.5 text-xs transition-colors"
        :class="
          p === path ? 'bg-background text-foreground' : 'text-muted-foreground hover:bg-accent/50'
        "
        data-testid="editor-tab"
        :data-path="p"
        :data-active="p === path"
      >
        <button
          type="button"
          class="max-w-[16ch] truncate"
          :title="p"
          data-testid="editor-tab-label"
          @click="activateTab(p)"
        >
          {{ tabName(p) }}
        </button>
        <span
          v-if="dirtyTabs.includes(p)"
          class="size-1.5 shrink-0 rounded-full bg-foreground/60"
          data-testid="editor-tab-dirty"
          :title="t('editor.unsavedChanges')"
        />
        <button
          type="button"
          class="shrink-0 rounded p-0.5 text-muted-foreground opacity-0 transition-opacity hover:bg-accent hover:text-accent-foreground focus:opacity-100 group-hover:opacity-100"
          :title="t('editor.closeTab')"
          data-testid="editor-tab-close"
          @click="closeTab(p)"
        >
          <X :size="12" />
        </button>
      </div>
      <button
        v-if="showClose"
        type="button"
        class="ml-auto shrink-0 self-center rounded px-2 text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
        :title="t('files.workbench.close')"
        data-testid="workbench-close"
        @click="requestClose"
      >
        <X :size="14" />
      </button>
    </div>

    <!-- Disk-change notices. The conflict bar is the reason the sync never
         reloads over unsaved edits: the choice belongs to whoever is typing. -->
    <div
      v-if="currentConflict"
      class="flex shrink-0 items-center gap-3 border-b border-border bg-amber-500/15 px-4 py-1.5 text-xs text-foreground"
      data-testid="workbench-disk-conflict"
    >
      <span class="flex-1 truncate">{{ t("files.workbench.diskChanged") }}</span>
      <Button
        variant="outline"
        size="sm"
        class="h-6 px-2 text-xs"
        data-testid="workbench-conflict-reload"
        @click="reloadFromDisk"
        >{{ t("files.workbench.reload") }}</Button
      >
      <Button
        variant="ghost"
        size="sm"
        class="h-6 px-2 text-xs"
        data-testid="workbench-conflict-keep"
        @click="keepMyChanges"
        >{{ t("files.workbench.keepMine") }}</Button
      >
    </div>
    <div
      v-else-if="currentRemoved"
      class="flex shrink-0 items-center gap-3 border-b border-border bg-destructive/15 px-4 py-1.5 text-xs text-foreground"
      data-testid="workbench-disk-removed"
    >
      <span class="flex-1 truncate">{{ t("files.workbench.removed") }}</span>
    </div>

    <FileTextEditor
      v-if="textEditMode"
      ref="editorRef"
      :user-id="props.userId"
      :path="path"
      :reveal-line="props.revealLine"
      @preview="goToPreview"
      @dirty-paths="dirtyTabs = $event"
    />
    <FilePreview
      v-else-if="path"
      ref="filePreviewRef"
      :user-id="props.userId"
      :path="path"
      :web-view="htmlWebView"
      class="flex-1 min-h-0"
    />
    <div v-else class="flex flex-1 items-center justify-center text-muted-foreground text-sm">
      {{ t("files.preview.noPath") }}
    </div>

    <FileQuickOpen
      v-if="props.enableQuickOpen"
      v-model:open="quickOpen"
      :user-id="props.userId"
      :initial-mode="quickOpenMode"
      @select="onQuickOpenSelect"
    />
  </main>
</template>
