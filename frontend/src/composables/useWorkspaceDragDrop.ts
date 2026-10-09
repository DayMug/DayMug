import { computed, ref, type Ref } from "vue";
import type { FileEntry } from "@/composables/useFileApi";

export interface UseWorkspaceDragDropOptions {
  currentPath: Ref<string>;
  // Inline-rename guard: dragging is suppressed on the entry currently
  // being renamed so the drag doesn't tear the input out from under the
  // user mid-edit.
  renamingEntry: Ref<string | null>;
  getEntryPath: (entry: FileEntry) => string;
  getDragPaths?: (entry: FileEntry) => string[];
  performUpload: (targetPath: string, files: File[]) => Promise<void>;
  performMove: (srcPath: string, targetPath: string) => Promise<void | boolean>;
}

// useWorkspaceDragDrop owns the workspace panel's drag-and-drop state and
// handlers: OS-file drops onto the background, a file or a folder, internal
// entry drags between folders, and the top-bar "move up one level" target.
// Instance-scoped (call inside setup()).
export function useWorkspaceDragDrop({
  currentPath,
  renamingEntry,
  getEntryPath,
  getDragPaths,
  performUpload,
  performMove,
}: UseWorkspaceDragDropOptions) {
  const isDragOver = ref(false);
  const dragOverTarget = ref<string | null>(null);
  const draggedEntry = ref<{ name: string; path: string; paths: string[]; isDir: boolean } | null>(
    null,
  );
  // Highlight + hint flag for the top-bar "drop here to move up one level"
  // affordance. Mirrors the body's dragLeaveTimer pattern so quick child→child
  // transitions inside the bar don't flicker the hint off.
  const isDragOverTopBar = ref(false);
  let topBarLeaveTimer: ReturnType<typeof setTimeout> | null = null;
  let dragLeaveTimer: ReturnType<typeof setTimeout> | null = null;

  // Parent of the current listing, or null at the workspace root. Used both
  // to gate the top-bar drop target and as the move target when dropping.
  const parentPath = computed<string | null>(() => {
    const p = currentPath.value;
    if (!p || p === ".") return null;
    const idx = p.lastIndexOf("/");
    return idx === -1 ? "." : p.slice(0, idx);
  });

  // The top bar only accepts drops from an in-progress internal drag (so we
  // never repurpose an OS file drag into a no-op move) and only when there is
  // somewhere to move up to.
  const canDropToParent = computed(() => !!draggedEntry.value && parentPath.value !== null);

  function handleDragOver(event: DragEvent) {
    event.preventDefault();
    if (dragLeaveTimer) {
      clearTimeout(dragLeaveTimer);
      dragLeaveTimer = null;
    }
    isDragOver.value = true;
  }

  function handleDragLeave() {
    // Debounce to avoid flicker when moving between child elements
    dragLeaveTimer = setTimeout(() => {
      isDragOver.value = false;
      dragOverTarget.value = null;
    }, 50);
  }

  // A folder target is identified by `key` — the entry name for a folder in
  // the current listing, the full path for one nested inside the list view's
  // tree — and drops into `targetPath`, which defaults to the listed folder.
  function folderTargetPath(key: string, targetPath?: string): string {
    if (targetPath !== undefined) return targetPath;
    return currentPath.value === "." ? key : currentPath.value + "/" + key;
  }

  // Dragged entries that can actually go into `targetPath`: not the folder
  // itself, and not a folder into its own subtree.
  function movablePaths(paths: string[], targetPath: string): string[] {
    return paths.filter((path) => path !== targetPath && !targetPath.startsWith(path + "/"));
  }

  function handleFolderDragOver(event: DragEvent, key: string, targetPath?: string) {
    event.preventDefault();
    event.stopPropagation();
    const target = folderTargetPath(key, targetPath);
    // Don't highlight a folder the dragged entries can't go into.
    if (draggedEntry.value && movablePaths(draggedEntry.value.paths, target).length === 0) {
      dragOverTarget.value = null;
      return;
    }
    if (event.dataTransfer && draggedEntry.value) {
      event.dataTransfer.dropEffect = "move";
    }
    dragOverTarget.value = key;
  }

  function handleFolderDragLeave(key: string) {
    if (dragOverTarget.value === key) {
      dragOverTarget.value = null;
    }
  }

  function handleEntryDragStart(event: DragEvent, entry: FileEntry) {
    // Don't start a drag while inline-renaming this entry
    if (renamingEntry.value === entry.name) {
      event.preventDefault();
      return;
    }
    const path = getEntryPath(entry);
    const paths = getDragPaths?.(entry) ?? [path];
    draggedEntry.value = {
      name: entry.name,
      path,
      paths,
      isDir: entry.is_dir,
    };
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = "move";
      // Some browsers (Firefox) require setData to actually start the drag
      event.dataTransfer.setData("application/x-daymug-path", path);
    }
  }

  function handleEntryDragEnd() {
    draggedEntry.value = null;
    dragOverTarget.value = null;
    isDragOver.value = false;
    isDragOverTopBar.value = false;
    if (topBarLeaveTimer) {
      clearTimeout(topBarLeaveTimer);
      topBarLeaveTimer = null;
    }
  }

  async function handleDrop(event: DragEvent) {
    event.preventDefault();
    isDragOver.value = false;
    dragOverTarget.value = null;
    // Internal drag dropped on the background = no-op (item is already in current dir)
    if (draggedEntry.value) {
      draggedEntry.value = null;
      return;
    }
    const files = await processDropEvent(event);
    if (files.length === 0) return;
    await performUpload(currentPath.value, files);
  }

  async function handleFolderDrop(event: DragEvent, key: string, explicitTarget?: string) {
    event.preventDefault();
    event.stopPropagation();
    isDragOver.value = false;
    dragOverTarget.value = null;
    const targetPath = folderTargetPath(key, explicitTarget);

    // Internal drag: move the source entry into this folder
    const dragged = draggedEntry.value;
    if (dragged) {
      draggedEntry.value = null;
      const paths = movablePaths(dragged.paths, targetPath);
      if (paths.length === 0) return; // dropped only on itself
      for (const path of paths) {
        const moved = await performMove(path, targetPath);
        if (moved === false) return;
      }
      return;
    }

    // External drag: upload files into the folder
    const files = await processDropEvent(event);
    if (files.length === 0) return;
    await performUpload(targetPath, files);
  }

  // Top-bar drop = "move up one level". Only an internal drag activates it
  // (canDropToParent gates on draggedEntry being set); OS file drags onto the
  // bar fall through to the browser default and do nothing.
  function handleTopBarDragOver(event: DragEvent) {
    if (!canDropToParent.value) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    if (topBarLeaveTimer) {
      clearTimeout(topBarLeaveTimer);
      topBarLeaveTimer = null;
    }
    isDragOverTopBar.value = true;
  }

  function handleTopBarDragLeave() {
    if (topBarLeaveTimer) clearTimeout(topBarLeaveTimer);
    // Brief debounce so a child→child cursor transition inside the bar doesn't
    // flicker the hint off between dragleave and the next dragover.
    topBarLeaveTimer = setTimeout(() => {
      isDragOverTopBar.value = false;
      topBarLeaveTimer = null;
    }, 60);
  }

  async function handleTopBarDrop(event: DragEvent) {
    event.preventDefault();
    event.stopPropagation();
    if (topBarLeaveTimer) {
      clearTimeout(topBarLeaveTimer);
      topBarLeaveTimer = null;
    }
    isDragOverTopBar.value = false;
    const dragged = draggedEntry.value;
    const target = parentPath.value;
    draggedEntry.value = null;
    if (!dragged || target === null) return;
    for (const path of dragged.paths) {
      const moved = await performMove(path, target);
      if (moved === false) return;
    }
  }

  async function processDropEvent(event: DragEvent): Promise<File[]> {
    const items = event.dataTransfer?.items;
    if (!items) return Array.from(event.dataTransfer?.files ?? []);

    const entries: FileSystemEntry[] = [];
    for (let i = 0; i < items.length; i++) {
      const entry = items[i].webkitGetAsEntry?.();
      if (entry) entries.push(entry);
    }

    if (entries.length === 0) return Array.from(event.dataTransfer?.files ?? []);

    const files: File[] = [];
    for (const entry of entries) {
      await traverseEntry(entry, "", files);
    }
    return files;
  }

  async function traverseEntry(
    entry: FileSystemEntry,
    basePath: string,
    files: File[],
  ): Promise<void> {
    if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) => {
        (entry as FileSystemFileEntry).file(resolve, reject);
      });
      const relativePath = basePath ? basePath + "/" + file.name : file.name;
      // Create a new File with the relative path as name
      const fileWithPath = new File([file], relativePath, {
        type: file.type,
        lastModified: file.lastModified,
      });
      files.push(fileWithPath);
    } else if (entry.isDirectory) {
      const dirReader = (entry as FileSystemDirectoryEntry).createReader();
      const dirEntries = await new Promise<FileSystemEntry[]>((resolve, reject) => {
        dirReader.readEntries(resolve, reject);
      });
      const dirPath = basePath ? basePath + "/" + entry.name : entry.name;
      for (const child of dirEntries) {
        await traverseEntry(child, dirPath, files);
      }
    }
  }

  return {
    isDragOver,
    dragOverTarget,
    draggedEntry,
    isDragOverTopBar,
    parentPath,
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
  };
}
