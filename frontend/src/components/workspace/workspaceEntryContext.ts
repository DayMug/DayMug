import { inject, type InjectionKey, type Ref } from "vue";
import type { FileEntry } from "@/composables/useFileApi";

/**
 * Wiring shared between `WorkspacePanel` and the components that render its
 * entries (`WorkspaceFileGrid`, `WorkspaceFileTree`).
 *
 * Selection, drag-and-drop, inline rename and the long-press gesture are all
 * owned by the panel — the renderers only read that state and forward DOM
 * events back. Injecting one context keeps the renderers from needing ~20
 * pass-through props each.
 */
export interface WorkspaceEntryContext {
  selectedEntries: Ref<Set<string>>;
  cutNames: Ref<Set<string>>;
  dragOverTarget: Ref<string | null>;
  draggedEntry: Ref<{ name: string; path: string; paths: string[]; isDir: boolean } | null>;
  /** Highlighted nested row in list view; null while the highlight is a root entry. */
  selectedTreePath: Ref<string | null>;
  renamingEntry: Ref<string | null>;
  renameValue: Ref<string>;
  renameComposing: Ref<boolean>;
  getEntryPath: (entry: FileEntry) => string;
  onEntryClick: (entry: FileEntry, event: MouseEvent, path?: string) => void;
  onEntryDblClick: (entry: FileEntry, event: MouseEvent, path?: string) => void;
  onEntryContextMenu: (event: MouseEvent, entry: FileEntry, path?: string) => void;
  startLongPress: (event: PointerEvent, entry: FileEntry | null, path: string) => void;
  moveLongPress: (event: PointerEvent) => void;
  endLongPress: () => void;
  onEntryDragStart: (event: DragEvent, entry: FileEntry) => void;
  onEntryDragEnd: () => void;
  onFolderDragOver: (event: DragEvent, key: string, targetPath?: string) => void;
  onFolderDragLeave: (key: string) => void;
  onFolderDrop: (event: DragEvent, key: string, targetPath?: string) => void;
  onRenameEnter: (event: KeyboardEvent) => void;
  cancelRename: () => void;
  confirmRename: () => void;
}

export const workspaceEntryContextKey: InjectionKey<WorkspaceEntryContext> =
  Symbol("workspaceEntryContext");

export function useWorkspaceEntryContext(): WorkspaceEntryContext {
  const ctx = inject(workspaceEntryContextKey);
  if (!ctx) {
    throw new Error("useWorkspaceEntryContext() must be used inside a WorkspacePanel");
  }
  return ctx;
}
