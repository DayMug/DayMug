import { computed, ref, type Ref } from "vue";
import type { WorkspaceContextMenuTarget } from "@/composables/useWorkspaceContextMenu";
import { isPrimaryModifier } from "@/lib/primaryModifier";
import { errorMessage } from "@/lib/errorMessage";

export interface WorkspaceClipboard {
  paths: string[];
  names: string[];
  mode: "copy" | "cut";
}

// Shown when the move target already has an entry of the same name.
export interface WorkspaceMoveConflict {
  srcPath: string;
  targetPath: string;
  fileName: string;
  clearClipboardOnSuccess?: boolean;
}

type PathsAndNames = { paths: string[]; names: string[] };

export interface UseWorkspaceClipboardOptions {
  userId: Ref<string>;
  currentPath: Ref<string>;
  // The browser's error surface — failed moves / copies render there.
  error: Ref<string>;
  selectedEntry: Ref<string | null>;
  contextMenu: Ref<WorkspaceContextMenuTarget | null>;
  closeContextMenu: () => void;
  // The set an action operates on, from a current-directory anchor name or a
  // context-menu target — the whole multi-selection when the anchor is in it.
  selectionPaths: (anchorName: string) => PathsAndNames;
  contextSelectionPaths: (target: WorkspaceContextMenuTarget) => PathsAndNames;
  loadDir: (path: string) => Promise<void> | void;
  moveFile: (
    userId: string,
    srcPath: string,
    dstPath: string,
    onConflict?: "overwrite" | "rename",
  ) => Promise<void>;
  copyFile: (userId: string, srcPath: string, dstPath: string) => Promise<unknown>;
  // True while something else owns the keyboard (the fullscreen preview), so
  // the shortcuts fall through to the browser.
  shortcutsSuspended: () => boolean;
}

// useWorkspaceClipboard owns the workspace panel's cut / copy / paste: the
// clipboard itself, the Cmd/Ctrl+C/X/V shortcuts, and moves together with the
// name-conflict dialog they can raise (drag-and-drop moves share it).
// Instance-scoped (call inside setup()).
export function useWorkspaceClipboard({
  userId,
  currentPath,
  error,
  selectedEntry,
  contextMenu,
  closeContextMenu,
  selectionPaths,
  contextSelectionPaths,
  loadDir,
  moveFile,
  copyFile,
  shortcutsSuspended,
}: UseWorkspaceClipboardOptions) {
  const clipboard = ref<WorkspaceClipboard | null>(null);
  const conflictMove = ref<WorkspaceMoveConflict | null>(null);

  const cutNames = computed(() => {
    if (!clipboard.value || clipboard.value.mode !== "cut") return new Set<string>();
    return new Set(clipboard.value.names);
  });

  function setClipboard(mode: "copy" | "cut") {
    const anchor = selectedEntry.value;
    if (!anchor) return;
    const { paths, names } = selectionPaths(anchor);
    if (paths.length === 0) return;
    clipboard.value = { paths, names, mode };
  }

  function clearClipboard() {
    clipboard.value = null;
  }

  function handleCtxCut() {
    const target = contextMenu.value;
    if (!target?.entry) return;
    const { paths, names } = contextSelectionPaths(target);
    clipboard.value = { paths, names, mode: "cut" };
    closeContextMenu();
  }

  function handleCtxCopy() {
    const target = contextMenu.value;
    if (!target?.entry) return;
    const { paths, names } = contextSelectionPaths(target);
    clipboard.value = { paths, names, mode: "copy" };
    closeContextMenu();
  }

  async function handlePaste() {
    if (!clipboard.value) return;
    closeContextMenu();
    if (clipboard.value.mode === "cut") {
      const paths = clipboard.value.paths;
      if (paths.length === 0) return;
      if (paths.length === 1) {
        // Single entry: route through performMove so a 409 opens the
        // FileConflictDialog, clearing the clipboard once the move lands.
        await performMove(paths[0], currentPath.value, { clearClipboardOnSuccess: true });
        return;
      }
      // Multi-select: move every cut entry. performMove handles one conflict
      // at a time via the dialog, so if a file stops to ask we halt rather than
      // clobber that pending dialog with the next move — the clipboard stays
      // put so the user can resolve and paste the remainder. Clear only once
      // the whole set has moved cleanly.
      for (const srcPath of paths) {
        const moved = await performMove(srcPath, currentPath.value, {});
        if (!moved) return;
      }
      clipboard.value = null;
      return;
    }
    try {
      for (const srcPath of clipboard.value.paths) {
        await copyFile(userId.value, srcPath, currentPath.value);
      }
      await loadDir(currentPath.value);
    } catch (e) {
      error.value = errorMessage(e);
    }
  }

  // Returns true when the file actually moved, false when the move was
  // deferred to the conflict dialog or failed — the multi-file paste loop uses
  // this to stop on the first entry that needs the user's attention.
  async function performMove(
    srcPath: string,
    targetPath: string,
    opts: { onConflict?: "overwrite" | "rename"; clearClipboardOnSuccess?: boolean } = {},
  ): Promise<boolean> {
    try {
      await moveFile(userId.value, srcPath, targetPath, opts.onConflict);
      if (opts.clearClipboardOnSuccess) clipboard.value = null;
      await loadDir(currentPath.value);
      return true;
    } catch (e) {
      if (
        !opts.onConflict &&
        e &&
        typeof e === "object" &&
        "status" in e &&
        (e as { status: number }).status === 409
      ) {
        const fileName = srcPath.split("/").pop() ?? srcPath;
        conflictMove.value = {
          srcPath,
          targetPath,
          fileName,
          clearClipboardOnSuccess: opts.clearClipboardOnSuccess,
        };
        return false;
      }
      error.value = errorMessage(e);
      return false;
    }
  }

  function handleConflictCancel() {
    conflictMove.value = null;
  }

  async function retryConflict(onConflict: "overwrite" | "rename") {
    const c = conflictMove.value;
    if (!c) return;
    conflictMove.value = null;
    await performMove(c.srcPath, c.targetPath, {
      onConflict,
      clearClipboardOnSuccess: c.clearClipboardOnSuccess,
    });
  }

  async function handleConflictRename() {
    await retryConflict("rename");
  }

  async function handleConflictOverwrite() {
    await retryConflict("overwrite");
  }

  function handleKeydown(e: KeyboardEvent) {
    // Don't intercept when typing in inputs
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;

    // While the file-preview modal is open the user's focus is the preview
    // surface, not the file panel — Cmd+C/X/V there must fall through to the
    // browser so they can copy text out of the markdown / code / plain-text
    // views (and aren't surprised by a stale workspace paste).
    if (shortcutsSuspended()) return;

    const mod = isPrimaryModifier(e);
    if (!mod) return;

    if (e.key === "c" || e.key === "x") {
      // If the user has an active text selection (chat message, anywhere
      // else in the page), let the browser run its native copy/cut so the
      // highlighted text lands on the clipboard instead of the selected
      // file path.
      const hasTextSelection = (window.getSelection()?.toString() ?? "").length > 0;
      if (hasTextSelection) return;
      if (selectedEntry.value) {
        e.preventDefault();
        setClipboard(e.key === "c" ? "copy" : "cut");
      }
    } else if (e.key === "v") {
      if (clipboard.value) {
        e.preventDefault();
        handlePaste();
      }
    }
  }

  return {
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
  };
}
