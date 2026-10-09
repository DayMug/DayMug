import { ref, type Ref } from "vue";
import type { FileEntry } from "@/composables/useFileApi";
import { isPrimaryModifier } from "@/lib/primaryModifier";

// The entry the menu was opened on (null when the menu was opened on the
// panel background) plus the viewport coordinates to anchor it at.
export interface WorkspaceContextMenuTarget {
  entry: FileEntry | null;
  path: string;
  x: number;
  y: number;
}

export interface UseWorkspaceContextMenuOptions {
  currentPath: Ref<string>;
  selectedEntries: Ref<Set<string>>;
  selectedEntry: Ref<string | null>;
  selectOnly: (name: string | null) => void;
  getEntryPath: (entry: FileEntry) => string;
  // The menu's "Open" action — same routing the panel applies to a
  // double-click (folders navigate, files open a preview).
  openEntry: (entry: FileEntry, path: string) => void;
  // Resolves the set a delete should operate on: the whole multi-selection
  // when the anchor entry belongs to it, otherwise just the anchor.
  getDeleteTargets: (entry: FileEntry, path: string) => FileEntry[];
  deleteEntries: (targets: FileEntry[], path: string) => Promise<void> | void;
  // Asks the user to confirm the delete; resolving false aborts it.
  confirmDelete: (entry: FileEntry, targets: FileEntry[]) => Promise<boolean>;
  getDownloadUrl: (entry: FileEntry, path: string) => string;
  showProperties: (target: { entry: FileEntry; path: string }) => void;
  refresh: () => Promise<void> | void;
}

// useWorkspaceContextMenu owns the workspace panel's right-click /
// long-press context-menu state and the generic menu actions (open, delete,
// download, properties, refresh). Feature-specific menu handlers
// (clipboard, rename, …) stay with their feature's state and share
// `contextMenu` / `closeContextMenu`. Instance-scoped (call inside setup()).
export function useWorkspaceContextMenu({
  currentPath,
  selectedEntries,
  selectedEntry,
  selectOnly,
  getEntryPath,
  openEntry,
  getDeleteTargets,
  deleteEntries,
  confirmDelete,
  getDownloadUrl,
  showProperties,
  refresh,
}: UseWorkspaceContextMenuOptions) {
  const contextMenu = ref<WorkspaceContextMenuTarget | null>(null);

  // Mirror Finder/Explorer: opening the menu (right-click or long-press)
  // inside an existing multi-select keeps the whole set so menu actions can
  // operate on all of them; opening on an unselected entry collapses the
  // selection down to it first. Background opens (entry === null) leave the
  // selection alone.
  function openContextMenu({ entry, path, x, y }: WorkspaceContextMenuTarget) {
    const isCurrentDirectoryEntry = entry !== null && path === getEntryPath(entry);
    if (entry && !isCurrentDirectoryEntry) {
      // Expanded list rows can share a basename with a current-directory
      // entry. Keep their path-based selection separate so actions never
      // inherit an unrelated root-level multi-selection.
      selectOnly(null);
    } else if (entry && !selectedEntries.value.has(entry.name)) {
      selectOnly(entry.name);
    } else if (entry) {
      selectedEntry.value = entry.name;
    }
    contextMenu.value = { entry, path, x, y };
  }

  function handleContextMenu(event: MouseEvent, entry: FileEntry, path = getEntryPath(entry)) {
    event.preventDefault();
    openContextMenu({
      entry,
      path,
      x: event.clientX,
      y: event.clientY,
    });
  }

  function handleBackgroundContextMenu(event: MouseEvent) {
    event.preventDefault();
    openContextMenu({
      entry: null,
      path: currentPath.value,
      x: event.clientX,
      y: event.clientY,
    });
  }

  function closeContextMenu() {
    contextMenu.value = null;
  }

  function handleCtxOpen() {
    const target = contextMenu.value;
    if (!target?.entry) return;
    openEntry(target.entry, target.path);
    closeContextMenu();
  }

  async function handleCtxDelete(event: MouseEvent) {
    const target = contextMenu.value;
    if (!target?.entry) return;
    const entry = target.entry;
    const targets = getDeleteTargets(entry, target.path);
    closeContextMenu();
    // Primary-modifier-click bypasses the confirmation so power users can
    // delete rapidly; plain click routes through the confirm dialog so a stray
    // click never silently wipes a file.
    if (!isPrimaryModifier(event)) {
      const ok = await confirmDelete(entry, targets);
      if (!ok) return;
    }
    void deleteEntries(targets, target.path);
  }

  function handleCtxDownload() {
    const target = contextMenu.value;
    if (!target?.entry) return;
    const url = getDownloadUrl(target.entry, target.path);
    const a = document.createElement("a");
    a.href = url;
    a.download = "";
    a.click();
    closeContextMenu();
  }

  function handleCtxProperties() {
    if (!contextMenu.value?.entry) return;
    showProperties({
      entry: contextMenu.value.entry,
      path: contextMenu.value.path,
    });
    closeContextMenu();
  }

  function handleCtxRefresh() {
    closeContextMenu();
    void refresh();
  }

  return {
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
  };
}
