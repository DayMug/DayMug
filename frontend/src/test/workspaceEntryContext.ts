import { ref } from "vue";
import { vi } from "vitest";
import type { FileEntry } from "@/composables/useFileApi";
import {
  workspaceEntryContextKey,
  type WorkspaceEntryContext,
} from "@/components/workspace/workspaceEntryContext";

export function makeEntry(name: string, isDir = false): FileEntry {
  return { name, is_dir: isDir, size: 0, modified: "" };
}

/**
 * Test double for the context WorkspacePanel provides to its entry renderers.
 * Every handler is a spy so a renderer test can assert "this DOM event reaches
 * that panel action" without standing up the whole panel.
 */
export function makeEntryContext(overrides: Partial<WorkspaceEntryContext> = {}) {
  const ctx: WorkspaceEntryContext = {
    selectedEntries: ref(new Set<string>()),
    cutNames: ref(new Set<string>()),
    dragOverTarget: ref(null),
    draggedEntry: ref(null),
    selectedTreePath: ref(null),
    renamingEntry: ref(null),
    renameValue: ref(""),
    renameComposing: ref(false),
    getEntryPath: (entry) => entry.name,
    onEntryClick: vi.fn(),
    onEntryDblClick: vi.fn(),
    onEntryContextMenu: vi.fn(),
    startLongPress: vi.fn(),
    moveLongPress: vi.fn(),
    endLongPress: vi.fn(),
    onEntryDragStart: vi.fn(),
    onEntryDragEnd: vi.fn(),
    onFolderDragOver: vi.fn(),
    onFolderDragLeave: vi.fn(),
    onFolderDrop: vi.fn(),
    onRenameEnter: vi.fn(),
    cancelRename: vi.fn(),
    confirmRename: vi.fn(),
    ...overrides,
  };
  return {
    ctx,
    provide: { [workspaceEntryContextKey as symbol]: ctx },
  };
}
