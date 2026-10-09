import { computed, ref, type Ref } from "vue";
import type { FileEntry } from "@/composables/useFileApi";
import { errorMessage } from "@/lib/errorMessage";

export type WorkspaceTreeRow = {
  entry: FileEntry;
  path: string;
  depth: number;
};

interface UseWorkspaceTreeOptions {
  userId: Ref<string>;
  currentPath: Ref<string>;
  entries: Ref<FileEntry[]>;
  showHiddenFiles: Ref<boolean>;
  listDir: (userId: string, path: string) => Promise<{ entries: FileEntry[] }>;
}

/**
 * Lazy directory tree backing the workspace list view.
 *
 * Child listings stay cached while a branch is collapsed so exploring siblings
 * doesn't repeatedly hit the API; the owner calls `reset()` on every root
 * refresh/navigation to avoid showing stale files. The state deliberately
 * lives here rather than inside the renderer component so that toggling
 * between grid and list view doesn't discard an expanded tree.
 */
export function useWorkspaceTree({
  userId,
  currentPath,
  entries,
  showHiddenFiles,
  listDir,
}: UseWorkspaceTreeOptions) {
  const expandedTreePaths = ref<Set<string>>(new Set());
  const treeChildren = ref<Map<string, FileEntry[]>>(new Map());
  const loadingTreePaths = ref<Set<string>>(new Set());
  const treeLoadErrors = ref<Map<string, string>>(new Map());

  // Generation token bumped by reset() and compared after every await. Without
  // it, a branch fetch that was in flight during a root refresh re-inserted the
  // pre-refresh children — and since `treeChildren` is a permanent cache
  // (toggleTreeDirectory returns early when the key exists), those stale
  // children were never re-fetched.
  let treeRun = 0;

  function isStale(run: number, requestedUserId: string): boolean {
    return run !== treeRun || requestedUserId !== userId.value;
  }

  function reset() {
    treeRun++;
    expandedTreePaths.value = new Set();
    treeChildren.value = new Map();
    loadingTreePaths.value = new Set();
    treeLoadErrors.value = new Map();
  }

  function visibleTreeEntries(source: FileEntry[]): FileEntry[] {
    return source
      .filter((entry) => showHiddenFiles.value || !entry.name.startsWith("."))
      .slice()
      .sort((a, b) => {
        if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
        return a.name.localeCompare(b.name);
      });
  }

  const treeRows = computed<WorkspaceTreeRow[]>(() => {
    const rows: WorkspaceTreeRow[] = [];

    function append(source: FileEntry[], parentPath: string, depth: number) {
      for (const entry of visibleTreeEntries(source)) {
        const path = parentPath === "." ? entry.name : `${parentPath}/${entry.name}`;
        rows.push({ entry, path, depth });
        const children = treeChildren.value.get(path);
        if (entry.is_dir && expandedTreePaths.value.has(path) && children) {
          append(children, path, depth + 1);
        }
      }
    }

    append(entries.value, currentPath.value, 0);
    return rows;
  });

  async function toggleTreeDirectory(path: string) {
    if (loadingTreePaths.value.has(path)) return;
    if (expandedTreePaths.value.has(path)) {
      const next = new Set(expandedTreePaths.value);
      next.delete(path);
      expandedTreePaths.value = next;
      return;
    }

    expandedTreePaths.value = new Set(expandedTreePaths.value).add(path);
    if (treeChildren.value.has(path)) return;

    const run = treeRun;
    const requestedUserId = userId.value;
    loadingTreePaths.value = new Set(loadingTreePaths.value).add(path);
    const errors = new Map(treeLoadErrors.value);
    errors.delete(path);
    treeLoadErrors.value = errors;
    try {
      const result = await listDir(requestedUserId, path);
      if (isStale(run, requestedUserId)) return;
      treeChildren.value = new Map(treeChildren.value).set(path, result.entries);
    } catch (e) {
      if (isStale(run, requestedUserId)) return;
      const nextExpanded = new Set(expandedTreePaths.value);
      nextExpanded.delete(path);
      expandedTreePaths.value = nextExpanded;
      treeLoadErrors.value = new Map(treeLoadErrors.value).set(path, errorMessage(e));
    } finally {
      if (!isStale(run, requestedUserId)) {
        const nextLoading = new Set(loadingTreePaths.value);
        nextLoading.delete(path);
        loadingTreePaths.value = nextLoading;
      }
    }
  }

  return {
    expandedTreePaths,
    loadingTreePaths,
    treeLoadErrors,
    treeRows,
    toggleTreeDirectory,
    reset,
  };
}

export type WorkspaceTree = ReturnType<typeof useWorkspaceTree>;
