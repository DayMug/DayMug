<script setup lang="ts">
import { computed, onBeforeUnmount, provide, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowLeft, Download, Laptop } from "lucide-vue-next";
import FileContextMenu from "@/components/FileContextMenu.vue";
import WorkspaceFileGrid from "./WorkspaceFileGrid.vue";
import WorkspaceFileTree from "./WorkspaceFileTree.vue";
import WorkspaceTopBar from "./WorkspaceTopBar.vue";
import { workspaceEntryContextKey } from "./workspaceEntryContext";
import { useWorkspaceTree } from "@/composables/useWorkspaceTree";
import { useWorkspaceLongPress } from "@/composables/useWorkspaceLongPress";
import {
  deviceFileDownloadUrl,
  listDeviceFiles,
  readDeviceFile,
  type ConversationDeviceBinding,
  type DeviceFileEntry,
} from "@/composables/apiDevices";
import { dirLabel } from "@/lib/desktopClient";
import { errorMessage } from "@/lib/errorMessage";
import { copyText } from "@/lib/clipboard";
import {
  loadWorkspaceUIPrefs,
  saveWorkspaceUIPrefs,
  type WorkspaceViewMode,
} from "@/lib/workspacePrefs";

// The file panel of a conversation bound to a folder on a computer: the same
// place the agent's tools work in, read through DayMug-Client. It wears the
// server workspace's chrome (top bar, grid/tree renderers, context menu) so
// switching a conversation to a computer doesn't change how files look, but
// it is read-only — editing happens in the user's own apps, or by asking the
// agent — so the mutating actions are left out.
const props = defineProps<{
  conversationId: string;
  binding: ConversationDeviceBinding;
  canCollapse?: boolean;
}>();
const emit = defineEmits<{ collapse: [] }>();

const { t } = useI18n();

// Text previews beyond this are cut; the full file stays one download away.
const TEXT_PREVIEW_CHARS = 512 * 1024;

const path = ref(".");
const entries = ref<DeviceFileEntry[]>([]);
const truncated = ref(false);
const loading = ref(false);
const error = ref("");
const prefs = loadWorkspaceUIPrefs();
const showHidden = ref(prefs.showHiddenFiles ?? false);
const viewMode = ref<WorkspaceViewMode>(prefs.viewMode ?? "list");

interface Preview {
  path: string;
  name: string;
  kind: "image" | "text" | "other";
  text?: string;
  url?: string;
  clipped?: boolean;
  error?: string;
  loading: boolean;
}
const preview = ref<Preview | null>(null);

const visible = computed(() =>
  entries.value
    .filter((e) => showHidden.value || !e.name.startsWith("."))
    .slice()
    .sort((a, b) => (a.is_dir !== b.is_dir ? (a.is_dir ? -1 : 1) : a.name.localeCompare(b.name))),
);

const rootLabel = computed(() => dirLabel(props.binding.work_dir));
// Same shape as the server panel's: the first crumb is the root (rendered as
// the heading), the rest are the folders below it.
const breadcrumbs = computed(() => {
  const parts = path.value === "." ? [] : path.value.split("/");
  return [
    { label: rootLabel.value, path: "." },
    ...parts.map((label, i) => ({ label, path: parts.slice(0, i + 1).join("/") })),
  ];
});

const tree = useWorkspaceTree({
  userId: computed(() => props.conversationId),
  currentPath: path,
  entries,
  showHiddenFiles: showHidden,
  listDir: (conversationId, dir) => listDeviceFiles(conversationId, dir),
});

function join(dir: string, name: string) {
  return dir === "." ? name : `${dir}/${name}`;
}

let listRun = 0;
let listAbort: AbortController | null = null;

async function load(target: string) {
  const run = ++listRun;
  listAbort?.abort();
  const ctrl = new AbortController();
  listAbort = ctrl;
  if (entries.value.length === 0) loading.value = true;
  error.value = "";
  try {
    const res = await listDeviceFiles(props.conversationId, target, ctrl.signal);
    if (run !== listRun) return;
    tree.reset();
    path.value = res.path || ".";
    entries.value = res.entries;
    truncated.value = res.truncated;
  } catch (e) {
    if (run !== listRun) return;
    error.value = errorMessage(e, t("deviceFiles.loadFailed"));
    if (target !== "." && target !== path.value) return;
    entries.value = [];
  } finally {
    if (run === listRun) loading.value = false;
  }
}

function revokePreview() {
  if (preview.value?.url) URL.revokeObjectURL(preview.value.url);
}

function closePreview() {
  previewAbort?.abort();
  revokePreview();
  preview.value = null;
}

let previewAbort: AbortController | null = null;

async function openFile(entry: DeviceFileEntry, filePath = join(path.value, entry.name)) {
  closePreview();
  const ctrl = new AbortController();
  previewAbort = ctrl;
  preview.value = { path: filePath, name: entry.name, kind: "other", loading: true };
  try {
    const res = await readDeviceFile(props.conversationId, filePath, ctrl.signal);
    const type = res.headers.get("Content-Type") ?? "";
    const blob = await res.blob();
    if (ctrl.signal.aborted) return;
    if (type.startsWith("image/")) {
      preview.value = {
        ...preview.value,
        kind: "image",
        url: URL.createObjectURL(blob),
        loading: false,
      };
    } else if (type.startsWith("text/")) {
      const text = await blob.text();
      if (ctrl.signal.aborted) return;
      preview.value = {
        ...preview.value,
        kind: "text",
        text: text.slice(0, TEXT_PREVIEW_CHARS),
        clipped: text.length > TEXT_PREVIEW_CHARS,
        loading: false,
      };
    } else {
      preview.value = { ...preview.value, kind: "other", loading: false };
    }
  } catch (e) {
    if (ctrl.signal.aborted) return;
    preview.value = {
      ...preview.value!,
      loading: false,
      error: errorMessage(e, t("deviceFiles.readFailed")),
    };
  }
}

// `target` is the entry's path from the bound folder; a nested tree row
// passes its own, a top-level entry defaults to the current folder.
function open(entry: DeviceFileEntry, target = join(path.value, entry.name)) {
  if (entry.is_dir) void load(target);
  else void openFile(entry, target);
}

function navigate(target: string) {
  closePreview();
  void load(target);
}

function refresh() {
  const shown = preview.value;
  if (shown) {
    const name = shown.path.slice(shown.path.lastIndexOf("/") + 1);
    void openFile({ name, is_dir: false, size: 0, modified: "" }, shown.path);
  }
  return load(path.value);
}

function setViewMode(mode: WorkspaceViewMode) {
  if (viewMode.value === mode) return;
  viewMode.value = mode;
  saveWorkspaceUIPrefs({ ...loadWorkspaceUIPrefs(), viewMode: mode });
}

function toggleHidden() {
  showHidden.value = !showHidden.value;
  saveWorkspaceUIPrefs({ ...loadWorkspaceUIPrefs(), showHiddenFiles: showHidden.value });
}

// ── Context menu ────────────────────────────────────────────────────────
interface MenuTarget {
  entry: DeviceFileEntry | null;
  path: string;
  x: number;
  y: number;
}
const contextMenu = ref<MenuTarget | null>(null);

function openContextMenu(target: MenuTarget) {
  contextMenu.value = target;
}

function closeContextMenu() {
  contextMenu.value = null;
}

const { startLongPress, moveLongPress, endLongPress, consumeLongPressClick } =
  useWorkspaceLongPress({ onLongPress: openContextMenu });

function onBackgroundContextMenu(event: MouseEvent) {
  openContextMenu({ entry: null, path: path.value, x: event.clientX, y: event.clientY });
}

function withMenuTarget(action: (target: MenuTarget & { entry: DeviceFileEntry }) => void) {
  const target = contextMenu.value;
  closeContextMenu();
  if (target?.entry) action(target as MenuTarget & { entry: DeviceFileEntry });
}

function menuOpen() {
  withMenuTarget((m) => open(m.entry, m.path));
}

function menuCopyName() {
  withMenuTarget((m) => void copyText(m.entry.name));
}

// Relative to the bound folder — the agent's working directory — matching
// what "Copy path" gives on the server panel.
function menuCopyPath() {
  withMenuTarget((m) => void copyText(m.path));
}

function menuDownload() {
  withMenuTarget((m) => {
    if (m.entry.is_dir) return;
    const a = document.createElement("a");
    a.href = deviceFileDownloadUrl(props.conversationId, m.path);
    a.download = m.entry.name;
    a.click();
  });
}

function menuRefresh() {
  closeContextMenu();
  void refresh();
}

function menuToggleHidden() {
  closeContextMenu();
  toggleHidden();
}

function menuToggleView() {
  closeContextMenu();
  setViewMode(viewMode.value === "grid" ? "list" : "grid");
}

// The grid/tree renderers are shared with the server panel and read their
// state from this context. Selection, rename and drag-and-drop don't apply to
// a read-only folder, so those parts stay inert.
const noSelection = ref<Set<string>>(new Set());
provide(workspaceEntryContextKey, {
  selectedEntries: noSelection,
  cutNames: noSelection,
  dragOverTarget: ref(null),
  draggedEntry: ref(null),
  selectedTreePath: ref(null),
  renamingEntry: ref(null),
  renameValue: ref(""),
  renameComposing: ref(false),
  getEntryPath: (entry) => join(path.value, entry.name),
  onEntryClick: (entry, _event, target) => {
    if (consumeLongPressClick()) return;
    open(entry, target);
  },
  onEntryDblClick: () => {},
  onEntryContextMenu: (event, entry, target) => {
    event.preventDefault();
    openContextMenu({
      entry,
      path: target ?? join(path.value, entry.name),
      x: event.clientX,
      y: event.clientY,
    });
  },
  startLongPress,
  moveLongPress,
  endLongPress,
  onEntryDragStart: (event) => event.preventDefault(),
  onEntryDragEnd: () => {},
  onFolderDragOver: () => {},
  onFolderDragLeave: () => {},
  onFolderDrop: () => {},
  onRenameEnter: () => {},
  cancelRename: () => {},
  confirmRename: () => {},
});

// A new conversation, or a different folder, starts over at its root.
watch(
  () => [props.conversationId, props.binding.device_id, props.binding.work_dir],
  () => {
    closePreview();
    closeContextMenu();
    tree.reset();
    entries.value = [];
    path.value = ".";
    void load(".");
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  listAbort?.abort();
  closePreview();
});

defineExpose({ refresh });
</script>

<template>
  <div class="flex h-full min-h-0 flex-col bg-background" data-testid="device-workspace-panel">
    <WorkspaceTopBar
      read-only
      :breadcrumbs="breadcrumbs"
      :home-label="rootLabel"
      :home-title="binding.work_dir"
      :is-drag-over-top-bar="false"
      :can-drop-to-parent="false"
      :can-collapse="canCollapse"
      :view-mode="viewMode"
      @navigate="navigate"
      @collapse="emit('collapse')"
      @set-view-mode="setViewMode"
      @refresh="refresh"
    >
      <template #leading>
        <span
          class="flex min-w-0 items-center gap-1.5 px-1.5 text-[12px] text-muted-foreground"
          :title="t('deviceFiles.title', { name: binding.device_name, dir: binding.work_dir })"
          data-testid="device-workspace-location"
        >
          <span class="relative shrink-0">
            <Laptop :size="15" />
            <span
              class="absolute -right-0.5 -bottom-0.5 size-1.5 rounded-full"
              :class="binding.online ? 'bg-emerald-500' : 'bg-muted-foreground'"
            />
          </span>
          <span class="max-w-[10rem] truncate">{{ binding.device_name }}</span>
        </span>
        <span class="mx-0.5 h-3.5 w-px bg-[#dddddd] dark:bg-border" aria-hidden="true" />
      </template>
    </WorkspaceTopBar>

    <!-- File preview -->
    <div v-if="preview" class="flex min-h-0 flex-1 flex-col" data-testid="device-workspace-preview">
      <div
        class="flex h-9 shrink-0 items-center gap-1 border-b border-[#eeeeee] px-2 dark:border-border"
      >
        <button
          type="button"
          class="grid size-7 place-items-center rounded-md hover:bg-accent"
          :aria-label="t('deviceFiles.back')"
          data-testid="device-workspace-back"
          @click="closePreview"
        >
          <ArrowLeft :size="15" />
        </button>
        <span class="min-w-0 flex-1 truncate text-[13px] font-medium" :title="preview.path">{{
          preview.name
        }}</span>
        <a
          class="grid size-7 place-items-center rounded-md hover:bg-accent"
          :href="deviceFileDownloadUrl(conversationId, preview.path)"
          :download="preview.name"
          :aria-label="t('deviceFiles.download')"
          data-testid="device-workspace-download"
        >
          <Download :size="15" />
        </a>
      </div>
      <div class="min-h-0 flex-1 overflow-auto">
        <p v-if="preview.loading" class="p-4 text-sm text-muted-foreground">
          {{ t("common.loading") }}
        </p>
        <p v-else-if="preview.error" class="p-4 text-sm text-destructive">{{ preview.error }}</p>
        <img
          v-else-if="preview.kind === 'image'"
          :src="preview.url"
          :alt="preview.name"
          class="mx-auto max-w-full p-4"
        />
        <template v-else-if="preview.kind === 'text'">
          <pre
            class="whitespace-pre-wrap break-words p-4 font-mono text-[12px] leading-5"
            data-testid="device-workspace-text"
            >{{ preview.text }}</pre
          >
          <p v-if="preview.clipped" class="px-4 pb-4 text-xs text-muted-foreground">
            {{ t("deviceFiles.clipped") }}
          </p>
        </template>
        <p v-else class="p-4 text-sm text-muted-foreground">{{ t("deviceFiles.noPreview") }}</p>
      </div>
    </div>

    <!-- Listing: the server panel's renderers on the same canvas. -->
    <div
      v-else
      class="ws-body wy-scroll min-h-0 flex-1 overflow-y-auto bg-[#f7f7f7] dark:bg-[#111]"
      data-testid="device-workspace-list"
      @contextmenu.prevent="onBackgroundContextMenu"
      @pointerdown="startLongPress($event, null, path)"
      @pointermove="moveLongPress"
      @pointerup="endLongPress"
      @pointercancel="endLongPress"
    >
      <div v-if="loading" class="px-5 py-10 text-center text-sm text-muted-foreground">
        {{ t("workspace.loading") }}
      </div>
      <div
        v-else-if="error"
        class="px-5 py-10 text-center text-sm text-destructive"
        data-testid="device-workspace-error"
      >
        {{ error }}
      </div>
      <div
        v-else-if="visible.length === 0"
        class="px-5 py-10 text-center text-sm text-muted-foreground"
      >
        {{ t("deviceFiles.empty") }}
      </div>
      <WorkspaceFileGrid v-else-if="viewMode === 'grid'" :entries="visible" />
      <WorkspaceFileTree v-else :tree="tree" />
      <p v-if="truncated" class="px-4 pb-4 text-xs text-muted-foreground">
        {{ t("deviceFiles.truncated") }}
      </p>
    </div>

    <FileContextMenu
      v-if="contextMenu"
      read-only
      :entry="contextMenu.entry"
      :entry-path="contextMenu.path"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :has-clipboard="false"
      :show-hidden-files="showHidden"
      :view-mode="viewMode"
      @close="closeContextMenu"
      @open="menuOpen"
      @copy-name="menuCopyName"
      @copy-path="menuCopyPath"
      @download="menuDownload"
      @refresh="menuRefresh"
      @toggle-hidden-files="menuToggleHidden"
      @toggle-view-mode="menuToggleView"
    />
  </div>
</template>

<style scoped>
/* Long-press opens our menu; keep iOS's selection callout out of the way. */
.ws-body {
  -webkit-touch-callout: none;
}
</style>
