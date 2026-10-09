import { nextTick, ref, type Ref } from "vue";
import type { FileEntry } from "@/composables/useFileApi";
import type { WorkspaceContextMenuTarget } from "@/composables/useWorkspaceContextMenu";
import { errorMessage } from "@/lib/errorMessage";

export interface UseWorkspaceInlineRenameOptions {
  userId: Ref<string>;
  currentPath: Ref<string>;
  // The browser's error surface — a failed rename renders in the listing slot.
  error: Ref<string>;
  contextMenu: Ref<WorkspaceContextMenuTarget | null>;
  closeContextMenu: () => void;
  getEntryPath: (entry: FileEntry) => string;
  loadDir: (path: string) => Promise<void> | void;
  renameFile: (userId: string, oldPath: string, newPath: string) => Promise<void>;
  // The rename input lives inside whichever entry renderer is mounted.
  focusInput: () => void;
}

// useWorkspaceInlineRename owns the workspace panel's in-place rename: which
// entry of the current directory is being renamed, the edit buffer, and the
// IME guard. Unlike the conversation lists' useInlineRename, the edit stays
// open until the server has renamed the file and the listing has reloaded.
// Instance-scoped (call inside setup()).
export function useWorkspaceInlineRename({
  userId,
  currentPath,
  error,
  contextMenu,
  closeContextMenu,
  getEntryPath,
  loadDir,
  renameFile,
  focusInput,
}: UseWorkspaceInlineRenameOptions) {
  const renamingEntry = ref<string | null>(null);
  const renameValue = ref("");
  // Suppress Enter-to-commit while the IME is mid-composition: pressing Enter
  // to accept a Chinese/Japanese/Korean candidate would otherwise close the
  // rename without keeping the typed characters.
  const renameComposing = ref(false);

  // Opens the rename input on an entry of the current listing.
  function beginRename(name: string) {
    renamingEntry.value = name;
    renameValue.value = name;
    nextTick(focusInput);
  }

  // Renames `entry`, or the context menu's entry when called from the menu. A
  // nested list-view row is renamed from its own directory, so the panel
  // navigates there first.
  async function startRename(entry?: FileEntry) {
    const menuTarget = contextMenu.value;
    const target = entry ?? menuTarget?.entry;
    if (!target) return;
    const targetPath = entry ? getEntryPath(entry) : menuTarget?.path;
    closeContextMenu();
    if (targetPath && targetPath !== getEntryPath(target)) {
      const parentPath = targetPath.slice(0, targetPath.lastIndexOf("/")) || ".";
      await loadDir(parentPath);
    }
    beginRename(target.name);
  }

  async function confirmRename() {
    if (!renamingEntry.value || !renameValue.value.trim()) {
      cancelRename();
      return;
    }
    const newName = renameValue.value.trim();
    if (newName === renamingEntry.value) {
      cancelRename();
      return;
    }
    const oldPath =
      currentPath.value === "."
        ? renamingEntry.value
        : currentPath.value + "/" + renamingEntry.value;
    const newPath = currentPath.value === "." ? newName : currentPath.value + "/" + newName;
    try {
      await renameFile(userId.value, oldPath, newPath);
      await loadDir(currentPath.value);
    } catch (e) {
      error.value = errorMessage(e);
    }
    renamingEntry.value = null;
  }

  function cancelRename() {
    renamingEntry.value = null;
  }

  function onRenameEnter(e: KeyboardEvent) {
    if (renameComposing.value || e.isComposing) return;
    void confirmRename();
  }

  return {
    renamingEntry,
    renameValue,
    renameComposing,
    beginRename,
    startRename,
    confirmRename,
    cancelRename,
    onRenameEnter,
  };
}
