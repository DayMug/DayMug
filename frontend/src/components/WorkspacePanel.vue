<script setup lang="ts">
import { ref, watch, onMounted, onUnmounted, computed, provide, toRef } from "vue";
import { useRoute, useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import type { FileEntry } from "@/composables/useFileApi";
import { useWorkspaceBrowser } from "@/composables/useWorkspaceBrowser";
import { useWorkspaceSelection } from "@/composables/useWorkspaceSelection";
import { useWorkspaceUpload } from "@/composables/useWorkspaceUpload";
import { useWorkspaceDragDrop } from "@/composables/useWorkspaceDragDrop";
import { useWorkspaceLongPress } from "@/composables/useWorkspaceLongPress";
import {
  useWorkspaceContextMenu,
  type WorkspaceContextMenuTarget,
} from "@/composables/useWorkspaceContextMenu";
import { useWorkspacePreview } from "@/composables/useWorkspacePreview";
import { useWorkspaceTree } from "@/composables/useWorkspaceTree";
import { useWorkspaceClipboard } from "@/composables/useWorkspaceClipboard";
import { useWorkspaceInlineRename } from "@/composables/useWorkspaceInlineRename";
import { useArtifactPanel } from "@/composables/useArtifactPanel";
import FileContextMenu from "./FileContextMenu.vue";
import FilePropertiesDialog from "./FilePropertiesDialog.vue";
import FileConflictDialog from "./FileConflictDialog.vue";
import WorkspacePreview from "./workspace/WorkspacePreview.vue";
import WorkspaceTopBar from "./workspace/WorkspaceTopBar.vue";
import WorkspaceFileGrid from "./workspace/WorkspaceFileGrid.vue";
import WorkspaceFileTree from "./workspace/WorkspaceFileTree.vue";
import WorkspaceUploadProgress from "./workspace/WorkspaceUploadProgress.vue";
import WorkspaceArtifactBar from "./workspace/WorkspaceArtifactBar.vue";
import FileWorkbench from "./FileWorkbench.vue";
import { workspaceEntryContextKey } from "./workspace/workspaceEntryContext";
import { pathRelative } from "@/lib/relPath";
import { copyText as copyToClipboard } from "@/lib/clipboard";
import { isPrimaryModifier } from "@/lib/primaryModifier";
import {
  loadWorkspaceUIPrefs,
  saveWorkspaceUIPrefs,
  type WorkspaceViewMode,
} from "@/lib/workspacePrefs";
import { useConfirm } from "@/composables/useConfirm";
import { errorMessage } from "@/lib/errorMessage";

const router = useRouter();
const route = useRoute();
const { t } = useI18n();
const { confirm } = useConfirm();

const props = defineProps<{
  userId: string;
  initialPath?: string;
  // Conversation id — used purely as a "reset trigger" for the panel.
  // When it changes the panel snaps back to `initialPath`, so a user who
  // navigated into a subfolder during the previous conversation doesn't
  // keep seeing that subfolder when a new conversation rooted at the
  // same `initialPath` value loads (where the prop didn't change and
  // the existing initialPath watcher would never fire).
  conversationId?: string;
  // Absolute work_dir of the agent the panel is browsing — paths in the
  // panel are relative to this. Optional because legacy mounts (tests, etc.)
  // never wired it; when absent, "Copy path" falls back to the panel-relative
  // form.
  userWorkDir?: string;
  // Absolute work_dir the conversation's claude process is rooted at. When
  // it differs from userWorkDir, "Copy path" rewrites the result so the
  // pasted path is what the agent would resolve from its CWD.
  conversationWorkDir?: string;
  // When true, a collapse button shows in the top bar that emits
  // `collapse` so the parent layout can hide the panel down to its
  // resize-handle. Set on desktop only — mobile uses a tab toggle and
  // doesn't need a separate collapse affordance.
  canCollapse?: boolean;
  // File double-click / context-menu Open target.
  //   "new-tab"  — default; opens the standalone preview page in a new tab.
  //   "same-tab" — the preview page reusing this panel to switch files.
  //   "inline"   — hand the path to the parent via `open-file` and let it
  //                decide; the chat page turns its chat column into a
  //                workbench rather than leaving the page.
  //   "panel"    — reference-workspace behaviour: keep chat in place and
  //                open files as tabs inside this artifact panel.
  fileOpenTarget?: "new-tab" | "same-tab" | "inline" | "panel";
}>();

const emit = defineEmits<{
  "directory-changed": [path: string];
  collapse: [];
  // Only emitted when fileOpenTarget is "inline".
  "open-file": [path: string];
  "fullscreen-change": [fullscreen: boolean];
}>();

// Reactive toggle backed by UI prefs. False (the default) keeps dot-files
// out of the listing the way Finder / Explorer do; the toggle gives
// developers a quick peek at .git, .env, etc when they need it.
const showHiddenFiles = ref<boolean>(loadWorkspaceUIPrefs().showHiddenFiles ?? false);
// Workspace view mode — "grid" matches the Finder-style cards (default)
// and "list" stacks entries as rows so the user can scan many files at
// once. Persisted alongside other UI prefs.
const viewMode = ref<WorkspaceViewMode>(
  loadWorkspaceUIPrefs().viewMode ?? (props.fileOpenTarget === "panel" ? "list" : "grid"),
);
// Highlighted nested row in list view. Root rows use `selectedEntries`
// instead — only descendants need their own highlight, since every workspace
// action still operates on the current directory's listing.
const selectedTreePath = ref<string | null>(null);

const {
  selectedEntries,
  selectedEntry,
  setBodyRef,
  marqueeStyle,
  selectOnly,
  handleSelect,
  startMarquee,
  moveMarquee,
  endMarquee,
} = useWorkspaceSelection(() => entries.value);

const {
  currentPath,
  entries,
  loading,
  error,
  breadcrumbs,
  loadDir,
  refresh: refreshDir,
  getEntryPath,
  fileApi: {
    listDir,
    downloadFileUrl,
    downloadZipUrl,
    uploadFiles,
    deleteFile,
    renameFile,
    writeFile,
    mkdir,
    moveFile,
    copyFile,
    extractArchive,
    compressToZip,
  },
} = useWorkspaceBrowser({
  userId: toRef(props, "userId"),
  showHiddenFiles,
  onDirectoryChanged: (path) => emit("directory-changed", path),
  // Mirror pre-extraction behavior: every list reload clears transient UI
  // state (selection, inline rename, context menu) so a stale selection from
  // a previous fetch can't bleed into the new listing — refresh paths
  // included, not just real navigations.
  onBeforeLoad: () => {
    tree.reset();
    selectedTreePath.value = null;
    selectOnly(null);
    renamingEntry.value = null;
    closeContextMenu();
  },
});

// List mode doubles as a lazy file tree. The state lives here rather than in
// WorkspaceFileTree so switching to grid view and back doesn't collapse
// everything the user expanded.
const tree = useWorkspaceTree({
  userId: toRef(props, "userId"),
  currentPath,
  entries,
  showHiddenFiles,
  listDir,
});

function toggleHiddenFiles() {
  showHiddenFiles.value = !showHiddenFiles.value;
  saveWorkspaceUIPrefs({
    ...loadWorkspaceUIPrefs(),
    showHiddenFiles: showHiddenFiles.value,
  });
  // The composable's watch on showHiddenFiles re-runs the listing.
}

function setViewMode(mode: WorkspaceViewMode) {
  if (viewMode.value === mode) return;
  viewMode.value = mode;
  saveWorkspaceUIPrefs({ ...loadWorkspaceUIPrefs(), viewMode: mode });
}

function toggleViewMode() {
  setViewMode(viewMode.value === "grid" ? "list" : "grid");
}

const isInlineTarget = computed(() => props.fileOpenTarget === "inline");
const isPanelTarget = computed(() => props.fileOpenTarget === "panel");
const isEmbeddedTarget = computed(() => isInlineTarget.value || isPanelTarget.value);

// The artifact panel ("panel" open target) owns lightweight preview tabs.
const artifactWorkbench = ref<InstanceType<typeof FileWorkbench> | null>(null);
const {
  paths: artifactPaths,
  activePath: activeArtifactPath,
  directoryOpen: artifactDirectoryOpen,
  fullscreen: artifactFullscreen,
  editMode: artifactEditMode,
  open: openArtifact,
  activate: activateArtifact,
  close: closeArtifact,
  showDirectory: showArtifactDirectory,
  toggleFullscreen: toggleArtifactFullscreen,
  collapse: collapseArtifactPanel,
  reset: resetArtifactState,
} = useArtifactPanel({
  workbench: artifactWorkbench,
  onFullscreenChange: (fullscreen) => emit("fullscreen-change", fullscreen),
  onCollapse: () => emit("collapse"),
});

// Preview / open routing — the route-backed fullscreen preview overlay
// (`?preview=<path>`) plus new-tab preview/editor windows.
const {
  fullscreenPreview,
  openPreview,
  openPreviewInNewTab,
  openEditorInNewTab,
  handleGlobalKeydown,
} = useWorkspacePreview({ userId: toRef(props, "userId"), router, route });
const uploadInput = ref<HTMLInputElement>();
const folderUploadInput = ref<HTMLInputElement>();

// Upload pipeline — progress/abort/conflict state lives in the composable;
// the template binds its refs for the bottom progress bar and the
// upload-conflict dialog.
const {
  uploadProgress,
  uploadCancelled,
  conflictUpload,
  settleUploadConflict,
  performUpload,
  cancelUpload,
  abortActiveUpload,
  handleUploadInput,
} = useWorkspaceUpload({
  userId: toRef(props, "userId"),
  currentPath,
  error,
  uploadFiles,
  loadDir,
});

// Context menu — state and generic actions live in the composable; the
// feature-specific handlers below (clipboard, rename, …) share its
// `contextMenu` / `closeContextMenu`. The callbacks reference hoisted
// function declarations defined further down.
const {
  contextMenu,
  openContextMenu,
  handleContextMenu,
  handleBackgroundContextMenu,
  closeContextMenu,
  handleCtxOpen,
  handleCtxDelete,
  handleCtxDownload,
  handleCtxProperties,
  handleCtxRefresh,
} = useWorkspaceContextMenu({
  currentPath,
  selectedEntries,
  selectedEntry,
  selectOnly,
  getEntryPath,
  openEntry: (entry, path) => handleEntryClick(entry, path),
  getDeleteTargets: (entry, path) => getDeleteTargets(entry, path),
  deleteEntries: (targets, path) => handleDelete(targets, path),
  confirmDelete: async (entry, targets) => {
    const message =
      targets.length > 1
        ? t("workspace.confirmDeleteFiles", { count: targets.length })
        : t("workspace.confirmDeleteFile", { name: entry.name });
    return await confirm({ message, variant: "destructive" });
  },
  getDownloadUrl: (entry, path) => getDownloadUrl(entry, path),
  showProperties: (target) => {
    propertiesEntry.value = target;
  },
  refresh: () => refreshDir(),
});

// The rename input lives inside whichever renderer is mounted; only one of
// the two ever is, so calling both is enough to focus it.
const fileGrid = ref<InstanceType<typeof WorkspaceFileGrid> | null>(null);
const fileTree = ref<InstanceType<typeof WorkspaceFileTree> | null>(null);

function focusRenameInput() {
  fileGrid.value?.focusRenameInput();
  fileTree.value?.focusRenameInput();
}

const {
  renamingEntry,
  renameValue,
  renameComposing,
  beginRename,
  startRename,
  confirmRename,
  cancelRename,
  onRenameEnter,
} = useWorkspaceInlineRename({
  userId: toRef(props, "userId"),
  currentPath,
  error,
  contextMenu,
  closeContextMenu,
  getEntryPath,
  loadDir,
  renameFile,
  focusInput: focusRenameInput,
});

// Cut / copy / paste, plus moves and the name-conflict dialog they share with
// drag-and-drop below.
const {
  clipboard,
  cutNames,
  conflictMove,
  clearClipboard,
  handleCtxCut,
  handleCtxCopy,
  handlePaste,
  performMove,
  handleConflictCancel,
  handleConflictRename,
  handleConflictOverwrite,
  handleKeydown,
} = useWorkspaceClipboard({
  userId: toRef(props, "userId"),
  currentPath,
  error,
  selectedEntry,
  contextMenu,
  closeContextMenu,
  selectionPaths: (anchorName) => selectionPathsAndNames(anchorName),
  contextSelectionPaths: (target) => contextSelectionPathsAndNames(target),
  loadDir,
  moveFile,
  copyFile,
  shortcutsSuspended: () => Boolean(fullscreenPreview.value),
});

// Drag-and-drop — internal entry moves, OS-file drops, and the top-bar
// "move up one level" target.
const {
  isDragOver,
  dragOverTarget,
  draggedEntry,
  isDragOverTopBar,
  canDropToParent,
  handleDragOver,
  handleDragLeave,
  handleFolderDragOver,
  handleFolderDragLeave,
  handleEntryDragStart,
  handleEntryDragEnd,
  handleDrop,
  handleFolderDrop,
  handleTopBarDragOver,
  handleTopBarDragLeave,
  handleTopBarDrop,
} = useWorkspaceDragDrop({
  currentPath,
  renamingEntry,
  getEntryPath,
  getDragPaths: (entry) => selectionPathsAndNames(entry.name).paths,
  performUpload,
  performMove,
});

// Properties dialog
const propertiesEntry = ref<{ entry: FileEntry; path: string } | null>(null);

function navigateTo(path: string) {
  loadDir(path);
}

function handleEntryClick(
  entry: FileEntry,
  path = getEntryPath(entry),
  target = props.fileOpenTarget ?? "new-tab",
) {
  if (entry.is_dir) {
    loadDir(path);
  } else if (target === "panel") {
    openArtifact(path);
  } else if (target === "inline") {
    emit("open-file", path);
  } else {
    openPreview(path, target);
  }
}

function handleBodyPointerDown(event: PointerEvent) {
  startLongPress(event, null, currentPath.value);
  startMarquee(event);
}

function handleBodyPointerMove(event: PointerEvent) {
  moveLongPress(event);
  moveMarquee(event);
}

function handleBodyPointerEnd(event: PointerEvent) {
  endLongPress();
  endMarquee(event);
}

function getDownloadUrl(entry: FileEntry, path = getEntryPath(entry)): string {
  if (path !== getEntryPath(entry)) {
    return entry.is_dir ? downloadZipUrl(props.userId, path) : downloadFileUrl(props.userId, path);
  }
  const { paths } = selectionPathsAndNames(entry.name);
  if (paths.length > 1) return downloadZipUrl(props.userId, paths);

  return entry.is_dir ? downloadZipUrl(props.userId, path) : downloadFileUrl(props.userId, path);
}

function getDeleteTargets(entry: FileEntry, path = getEntryPath(entry)): FileEntry[] {
  if (path !== getEntryPath(entry)) return [entry];
  if (!selectedEntries.value.has(entry.name)) return [entry];

  const byName = new Map(entries.value.map((item) => [item.name, item]));
  const targets = Array.from(selectedEntries.value)
    .map((name) => byName.get(name))
    .filter((item): item is FileEntry => item !== undefined);

  return targets.length > 0 ? targets : [entry];
}

async function handleDelete(targets: FileEntry | FileEntry[], targetPath?: string) {
  const entriesToDelete = Array.isArray(targets) ? targets : [targets];
  try {
    if (
      targetPath &&
      entriesToDelete.length === 1 &&
      targetPath !== getEntryPath(entriesToDelete[0])
    ) {
      await deleteFile(props.userId, targetPath);
    } else {
      for (const entry of entriesToDelete) {
        await deleteFile(props.userId, getEntryPath(entry));
      }
    }
    await loadDir(currentPath.value);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

// Decompresses an archive (.zip / .tar / .tar.gz / .tar.bz2) into a sibling
// directory the backend names after the archive stem. We reload the listing
// on success so the new folder appears, and select it so the user can
// double-click straight in.
async function handleCtxExtract() {
  const target = contextMenu.value;
  if (!target?.entry) return;
  const archivePath = target.path;
  closeContextMenu();
  try {
    const { path: extractedPath } = await extractArchive(props.userId, archivePath);
    await loadDir(currentPath.value);
    const justName = extractedPath.split("/").pop();
    if (justName) selectOnly(justName);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

// Compresses the current multi-selection (or the right-clicked entry, if the
// menu opened on something outside the set) into a single zip in the current
// folder. The backend names it after the source basename when only one item
// was selected and "Archive.zip" otherwise, deduping if either already
// exists. On success we refresh and highlight the new archive.
async function handleCtxCompress() {
  const target = contextMenu.value;
  if (!target?.entry) return;
  // Pack the multi-selection when the right-clicked entry belongs to it,
  // otherwise just that entry — same rule cut/copy/delete apply.
  const { paths } = contextSelectionPathsAndNames(target);
  const destinationPath =
    target.path === getEntryPath(target.entry)
      ? currentPath.value
      : target.path.slice(0, target.path.lastIndexOf("/")) || ".";
  closeContextMenu();
  try {
    const { name } = await compressToZip(props.userId, paths, destinationPath);
    await loadDir(currentPath.value);
    selectOnly(name);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

// iOS long-press gesture — the timer state machine lives in the composable;
// when it fires we mirror the desktop right-click selection rule and open
// the same FileContextMenu.
const { startLongPress, moveLongPress, endLongPress, consumeLongPressClick } =
  useWorkspaceLongPress({
    onLongPress: ({ entry, path, x, y }) => {
      openContextMenu({ entry, path, x, y });
    },
  });

function handleEntryClickGuarded(entry: FileEntry, event: MouseEvent, path?: string) {
  if (consumeLongPressClick()) return;
  // The artifact panel follows the reference workspace's document-browser
  // interaction: one click opens an item. Primary-modifier clicks retain the
  // existing selection behaviour for multi-file actions.
  if (isPanelTarget.value && !isPrimaryModifier(event)) {
    selectedTreePath.value = null;
    handleEntryClick(entry, path ?? getEntryPath(entry), "panel");
    return;
  }
  if (path && path !== getEntryPath(entry)) {
    selectOnly(null);
    selectedTreePath.value = path;
    return;
  }
  selectedTreePath.value = null;
  handleSelect(entry, event);
}

function handleEntryDblClickGuarded(entry: FileEntry, event: MouseEvent, path?: string) {
  if (consumeLongPressClick()) return;
  // Cmd/Ctrl escapes the panel's in-place open the same way it escapes a
  // same-tab link: on the preview page, where files normally replace the
  // current one, the modifier sends this file to its own tab instead.
  const target = isPrimaryModifier(event) ? "new-tab" : (props.fileOpenTarget ?? "new-tab");
  handleEntryClick(entry, path ?? getEntryPath(entry), target);
}

function handleCtxOpenInNewTab() {
  const target = contextMenu.value;
  if (!target?.entry || target.entry.is_dir) return;
  closeContextMenu();
  openPreviewInNewTab(target.path);
}

function handleCtxToggleHidden() {
  closeContextMenu();
  toggleHiddenFiles();
}

function handleCtxToggleView() {
  closeContextMenu();
  toggleViewMode();
}

// Resolve the set an action should operate on from a single anchor name:
// when the anchor is part of the active multi-selection we take the whole
// selection, otherwise just the anchor. Mirrors the right-click rule applied
// at menu-open time (and reused by Compress/Delete) so cut/copy act on every
// selected entry, not only the one under the cursor.
function selectionPathsAndNames(anchorName: string): { paths: string[]; names: string[] } {
  const names = selectedEntries.value.has(anchorName)
    ? Array.from(selectedEntries.value)
    : [anchorName];
  const paths = names.map((n) => (currentPath.value === "." ? n : `${currentPath.value}/${n}`));
  return { paths, names };
}

function contextSelectionPathsAndNames(target: WorkspaceContextMenuTarget): {
  paths: string[];
  names: string[];
} {
  if (!target.entry) return { paths: [], names: [] };
  if (target.path !== getEntryPath(target.entry)) {
    return { paths: [target.path], names: [target.entry.name] };
  }
  return selectionPathsAndNames(target.entry.name);
}

function handleCtxCopyName() {
  if (!contextMenu.value?.entry) return;
  void copyToClipboard(contextMenu.value.entry.name);
  closeContextMenu();
}

function handleCtxCopyPath() {
  // The path the user wants to paste should be what the agent's claude
  // process resolves from its CWD — i.e. relative to the conversation's
  // work_dir, not the agent account's work_dir (which is what the panel
  // browses against). When both work_dirs are wired, rebase the path; when
  // either is missing, fall back to the panel-relative form.
  if (!contextMenu.value?.entry) return;
  const panelPath = contextMenu.value.path;
  const agentRoot = props.userWorkDir ?? "";
  const convRoot = props.conversationWorkDir ?? "";
  let copyText = panelPath;
  if (agentRoot && convRoot) {
    const abs = panelPath === "." ? agentRoot : `${agentRoot}/${panelPath}`;
    copyText = pathRelative(convRoot, abs);
  }
  void copyToClipboard(copyText);
  closeContextMenu();
}

function nextAvailableName(baseName: string): string {
  const existingNames = new Set(entries.value.map((entry) => entry.name));
  if (!existingNames.has(baseName)) return baseName;

  let index = 2;
  while (existingNames.has(`${baseName} (${index})`)) index++;
  return `${baseName} (${index})`;
}

// New file / folder
async function handleNewFile() {
  closeContextMenu();
  const fileName = nextAvailableName("New File");
  const filePath = currentPath.value === "." ? fileName : `${currentPath.value}/${fileName}`;
  try {
    await writeFile(props.userId, filePath, "");
    await loadDir(currentPath.value);
    beginRename(fileName);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

async function handleNewFolder() {
  closeContextMenu();
  const folderName = nextAvailableName("New Folder");

  const folderPath = currentPath.value === "." ? folderName : currentPath.value + "/" + folderName;
  try {
    await mkdir(props.userId, folderPath);
    await loadDir(currentPath.value);
    // Start inline rename on the new folder
    beginRename(folderName);
  } catch (e) {
    error.value = errorMessage(e);
  }
}

function handleCtxUpload() {
  closeContextMenu();
  uploadInput.value?.click();
}

function handleCtxUploadFolder() {
  closeContextMenu();
  folderUploadInput.value?.click();
}

onMounted(() => {
  document.addEventListener("keydown", handleKeydown);
  // handleGlobalKeydown only closes the overlay this panel renders. In inline
  // mode there is no overlay, and Escape belongs to whoever owns the inline
  // workbench — claiming it here would swallow their close.
  if (!isEmbeddedTarget.value) document.addEventListener("keydown", handleGlobalKeydown);
});

onUnmounted(() => {
  document.removeEventListener("keydown", handleKeydown);
  document.removeEventListener("keydown", handleGlobalKeydown);
});

// Everything the grid / tree renderers need. Provided instead of threaded
// through props because both renderers touch nearly the whole interaction
// surface of the panel.
provide(workspaceEntryContextKey, {
  selectedEntries,
  cutNames,
  dragOverTarget,
  draggedEntry,
  selectedTreePath,
  renamingEntry,
  renameValue,
  renameComposing,
  getEntryPath,
  onEntryClick: handleEntryClickGuarded,
  onEntryDblClick: handleEntryDblClickGuarded,
  onEntryContextMenu: handleContextMenu,
  startLongPress,
  moveLongPress,
  endLongPress,
  onEntryDragStart: handleEntryDragStart,
  onEntryDragEnd: handleEntryDragEnd,
  onFolderDragOver: handleFolderDragOver,
  onFolderDragLeave: handleFolderDragLeave,
  onFolderDrop: handleFolderDrop,
  onRenameEnter,
  cancelRename,
  confirmRename,
});

defineExpose({ refresh: refreshDir });

// Tracks the userId we last actually issued a load against. Compared
// against `props.userId` inside the watcher instead of Vue's `oldVals`,
// because the "initialPath === ''" early return below would otherwise
// consume the agent-switch tick: by the time initialPath becomes
// non-empty again, `oldVals[0]` already shows the new userId and the
// userIdChanged branch would fail to fire — leaving the panel showing
// the previous agent's files when both agents' work_dirs resolve the
// same target string (typically "." at root).
let lastLoadedUserId: string | undefined;
// Tracked separately from `lastLoadedUserId` because the invalidation below
// must run even on the ticks where we skip the listing (initialPath === "").
let lastBoundUserId: string | undefined;
let lastConversationId: string | undefined;

// State that is bound to a specific workspace root and therefore cannot
// survive an agent switch. The clipboard deliberately survives a *directory*
// change (cut here, paste there is the whole point) and a conversation switch
// (same agent, same work_dir) — only a different agent invalidates it, because
// the stored paths would then resolve against a work_dir the user never
// browsed: paste either clobbers a same-named file or 404s.
function resetAgentScopedState() {
  clearClipboard();
  abortActiveUpload();
}

// Modals pinned to one entry in one directory. Both go stale as soon as the
// panel is rebound — a conversation switch snaps the listing back to
// `initialPath`, leaving a dialog describing a file the user can no longer
// see. Not cleared on a plain reload: an upload finishing in the background
// would otherwise close the properties dialog the user is reading.
function resetPanelScopedDialogs() {
  propertiesEntry.value = null;
  conflictMove.value = null;
}

watch(
  [() => props.userId, () => props.conversationId, () => props.initialPath],
  ([newUserId, newConversationId]) => {
    // Invalidate before the early return below: ChatPage emits initialPath=""
    // for the whole agent-switch transition, and a clipboard or dialog from
    // the previous agent must not survive that window.
    if (newUserId !== lastBoundUserId) {
      if (lastBoundUserId !== undefined) resetArtifactState();
      lastBoundUserId = newUserId;
      resetAgentScopedState();
      resetPanelScopedDialogs();
    } else if (newConversationId !== lastConversationId) {
      resetPanelScopedDialogs();
      if (lastConversationId !== undefined) resetArtifactState();
    }
    lastConversationId = newConversationId;

    // Empty string is the parent's "data not ready, hold off" signal —
    // ChatPage emits "" on hard refresh while the conversation fetch is
    // still in flight, and during the agent-switch transition while
    // conversationWorkDir hasn't been rebound yet. Skipping here avoids
    // the extra round-trip where we would otherwise list the user's
    // work_dir root and then immediately re-list the conversation's
    // work_dir as soon as it lands. Undefined (legacy / test mounts
    // that never wire the prop) still falls back to ".", matching the
    // previous behaviour.
    if (props.initialPath === "") return;
    const target = props.initialPath ?? ".";
    const userIdChanged = newUserId !== lastLoadedUserId;
    // Initial mount and agent (userId) switches are inherent "fresh
    // state" transitions — always fetch. For a conversation switch
    // within the same agent, only re-fetch when the panel isn't
    // already at the new conversation's initialPath. This catches the
    // case where the user navigated the panel into a subfolder during
    // the previous conversation and the new conversation happens to
    // resolve to the same `initialPath` value (so the prop didn't
    // change and the previous initialPath-only watcher missed it).
    if (userIdChanged) {
      lastLoadedUserId = newUserId;
      loadDir(target);
      return;
    }
    if (target !== currentPath.value) {
      loadDir(target);
    }
  },
  { immediate: true },
);
</script>

<template>
  <div
    class="workspace-panel flex flex-col h-full min-h-0"
    :class="{ 'workspace-internal-drag': draggedEntry }"
  >
    <WorkspaceArtifactBar
      v-if="isPanelTarget"
      :paths="artifactPaths"
      :active-path="activeArtifactPath"
      :directory-open="artifactDirectoryOpen"
      :fullscreen="artifactFullscreen"
      :can-collapse="canCollapse"
      @activate="activateArtifact"
      @close="closeArtifact"
      @directory="showArtifactDirectory"
      @toggle-fullscreen="toggleArtifactFullscreen"
      @collapse="collapseArtifactPanel"
    />
    <WorkspaceTopBar
      v-if="!isPanelTarget || artifactDirectoryOpen || !activeArtifactPath"
      :breadcrumbs="breadcrumbs"
      :is-drag-over-top-bar="isDragOverTopBar"
      :can-drop-to-parent="canDropToParent"
      :can-collapse="canCollapse && !isPanelTarget"
      :view-mode="viewMode"
      @dragover="handleTopBarDragOver"
      @dragleave="handleTopBarDragLeave"
      @drop="handleTopBarDrop"
      @navigate="navigateTo"
      @collapse="emit('collapse')"
      @set-view-mode="setViewMode"
      @new-folder="handleNewFolder"
      @new-file="handleNewFile"
      @upload="handleCtxUpload"
      @refresh="refreshDir"
    />
    <input ref="uploadInput" type="file" multiple class="hidden" @change="handleUploadInput" />
    <input
      ref="folderUploadInput"
      type="file"
      multiple
      webkitdirectory
      class="hidden"
      @change="handleUploadInput"
    />

    <!-- File listing (preview is rendered as a modal overlay below, not inline) -->
    <div
      v-if="!isPanelTarget || artifactDirectoryOpen || !activeArtifactPath"
      :ref="setBodyRef"
      class="ws-body wy-scroll flex-1 overflow-y-auto bg-[#f7f7f7] transition-colors dark:bg-[#111]"
      :class="{
        'ring-2 ring-primary ring-inset bg-primary/5': isDragOver && !dragOverTarget,
        relative: true,
      }"
      @contextmenu.prevent="handleBackgroundContextMenu"
      @pointerdown="handleBodyPointerDown"
      @pointermove="handleBodyPointerMove"
      @pointerup="handleBodyPointerEnd"
      @pointercancel="handleBodyPointerEnd"
      @dragover.prevent="handleDragOver"
      @dragleave="handleDragLeave"
      @drop.prevent="handleDrop"
    >
      <div
        v-if="marqueeStyle"
        class="pointer-events-none absolute z-20 rounded-sm border border-primary bg-primary/10"
        :style="marqueeStyle"
      />
      <div v-if="loading" class="py-10 px-5 text-center text-muted-foreground text-sm">
        {{ t("workspace.loading") }}
      </div>
      <div v-else-if="error" class="ws-error py-10 px-5 text-center text-destructive text-sm">
        {{ error }}
      </div>
      <div
        v-else-if="entries.length === 0"
        class="py-10 px-5 text-center text-muted-foreground text-sm"
      >
        {{ t("workspace.emptyDirectory") }}
      </div>

      <!-- Grid view — UI design's editorial Finder grid -->
      <WorkspaceFileGrid v-else-if="viewMode === 'grid'" ref="fileGrid" :entries="entries" />

      <!-- List view — a lazy tree for scanning without losing the current path -->
      <WorkspaceFileTree v-else ref="fileTree" :tree="tree" />
    </div>

    <FileWorkbench
      v-if="isPanelTarget && activeArtifactPath"
      v-show="!artifactDirectoryOpen"
      ref="artifactWorkbench"
      :user-id="props.userId"
      :path="activeArtifactPath"
      :edit-mode="artifactEditMode"
      :enable-quick-open="false"
      :show-tabs="false"
      storage-scope="artifacts"
      @open="({ path }) => void activateArtifact(path)"
      @update:edit-mode="artifactEditMode = $event"
    />

    <WorkspaceUploadProgress
      v-if="uploadProgress"
      :progress="uploadProgress"
      :cancelled="uploadCancelled"
      @cancel="cancelUpload"
    />

    <!-- Context menu -->
    <FileContextMenu
      v-if="contextMenu"
      :entry="contextMenu.entry"
      :entry-path="contextMenu.path"
      :x="contextMenu.x"
      :y="contextMenu.y"
      :has-clipboard="clipboard !== null"
      :show-hidden-files="showHiddenFiles"
      :view-mode="viewMode"
      :show-open-in-new-tab="(props.fileOpenTarget ?? 'new-tab') !== 'new-tab'"
      :selection-count="
        contextMenu?.entry && selectedEntries.has(contextMenu.entry.name) ? selectedEntries.size : 1
      "
      @close="closeContextMenu"
      @open-in-new-tab="handleCtxOpenInNewTab"
      @open="handleCtxOpen"
      @rename="startRename()"
      @delete="handleCtxDelete"
      @download="handleCtxDownload"
      @properties="handleCtxProperties"
      @cut="handleCtxCut"
      @copy="handleCtxCopy"
      @copy-name="handleCtxCopyName"
      @copy-path="handleCtxCopyPath"
      @paste="handlePaste"
      @new-file="handleNewFile"
      @new-folder="handleNewFolder"
      @upload="handleCtxUpload"
      @upload-folder="handleCtxUploadFolder"
      @refresh="handleCtxRefresh"
      @toggle-hidden-files="handleCtxToggleHidden"
      @toggle-view-mode="handleCtxToggleView"
      @extract="handleCtxExtract"
      @compress-zip="handleCtxCompress"
    />

    <!-- Properties dialog -->
    <FilePropertiesDialog
      v-if="propertiesEntry"
      :entry="propertiesEntry.entry"
      :entry-path="propertiesEntry.path"
      @close="propertiesEntry = null"
    />

    <!-- Move conflict dialog -->
    <FileConflictDialog
      v-if="conflictMove"
      :file-name="conflictMove.fileName"
      :target-folder="conflictMove.targetPath"
      @cancel="handleConflictCancel"
      @rename="handleConflictRename"
      @overwrite="handleConflictOverwrite"
    />

    <!-- Upload conflict dialog -->
    <FileConflictDialog
      v-if="conflictUpload"
      :file-name="conflictUpload.fileName"
      :target-folder="conflictUpload.targetFolder"
      :action="t('files.conflict.uploadAction')"
      @cancel="settleUploadConflict('cancel')"
      @rename="settleUploadConflict('rename')"
      @overwrite="settleUploadConflict('overwrite')"
    />

    <!-- File preview — modal-centered overlay. Open/close state is the
         route-backed `fullscreenPreview`; Escape is handled by the global
         keydown listener above. Suppressed in inline mode: the chat page
         already shows the file in its own column, and a modal on top of it
         would fight the panel it duplicates. -->
    <WorkspacePreview
      v-if="fullscreenPreview && !isEmbeddedTarget"
      :user-id="props.userId"
      :path="fullscreenPreview"
      @close="fullscreenPreview = null"
      @edit="openEditorInNewTab"
    />
  </div>
</template>

<style scoped>
/* Native drag events can replace their source before dragend fires. Scope the
   pointer cursor to this panel's reactive drag state so it disappears with the
   state or component and can never remain stuck on the page root. */
.workspace-internal-drag {
  cursor: pointer;
}
</style>

<style scoped>
/* iPad/iOS Safari fires `contextmenu` on long-press, but it ALSO opens the
   system selection callout (copy / share / look up) on top of our menu.
   Suppress the callout on the surfaces that own a contextmenu handler so
   the long-press gesture lands cleanly on FileContextMenu instead. */
.ws-body {
  -webkit-touch-callout: none;
}
</style>
