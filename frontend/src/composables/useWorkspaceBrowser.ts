import { computed, ref, type Ref } from "vue";
import { useFileApi, type FileEntry } from "@/composables/useFileApi";
import { errorMessage } from "@/lib/errorMessage";

export interface UseWorkspaceBrowserOptions {
  userId: Ref<string>;
  // showHiddenFiles flips the dot-file filter applied client-side after the
  // backend returns the full listing. The composable re-runs the filter via
  // a watch so toggling it doesn't require a refetch.
  showHiddenFiles: Ref<boolean>;
  // onBeforeLoad fires at the start of every loadDir (including refresh).
  // The component uses it to clear transient UI state — selection, inline
  // rename, context menu — that should not survive a relisting. Mirrors the
  // pre-extraction inline behavior of the previous loadDir, which always
  // wiped those refs even on a refresh.
  onBeforeLoad?: () => void;
}

// useWorkspaceBrowser owns the workspace panel's path / listing / breadcrumb
// state. The component above keeps UI concerns (selection, context menu,
// clipboard, drag-drop, conflict dialogs) since those are tightly coupled to
// template bindings. File mutation ops likewise stay in the component
// because they orchestrate UI feedback (re-show context menu, open dialogs)
// alongside the actual API call.
export function useWorkspaceBrowser({
  userId,
  showHiddenFiles,
  onBeforeLoad,
}: UseWorkspaceBrowserOptions) {
  const fileApi = useFileApi();

  const currentPath = ref(".");
  // The raw, unfiltered listing returned by the backend. The visible
  // `entries` derives from this with the dot-file filter applied in-memory
  // so toggling `showHiddenFiles` doesn't trigger a re-fetch (which used to
  // flash a Loading… placeholder over the grid before the new list landed).
  const rawEntries = ref<FileEntry[]>([]);
  const loading = ref(false);
  const error = ref("");
  const breadcrumbs = ref<{ label: string; path: string }[]>([]);

  const entries = computed<FileEntry[]>(() => {
    return rawEntries.value
      .filter((e) => showHiddenFiles.value || !e.name.startsWith("."))
      .slice()
      .sort((a, b) => {
        if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
        return a.name.localeCompare(b.name);
      });
  });

  function buildBreadcrumbs(path: string) {
    const parts = path === "." ? [] : path.split("/").filter(Boolean);
    const crumbs: { label: string; path: string }[] = [{ label: "Home", path: "." }];
    let current = "";
    for (const part of parts) {
      current = current ? current + "/" + part : part;
      crumbs.push({ label: part, path: current });
    }
    breadcrumbs.value = crumbs;
  }

  // Generation token for in-flight listings. Only the newest load may write
  // shared state: double-clicking a slow folder A then a fast folder B used to
  // let A's late response overwrite B's listing.
  let loadRun = 0;

  // A load is stale once a newer one started or once the panel was rebound to
  // a different agent. The userId check matters on its own: the panel can swap
  // agents without issuing a new listing (the parent holds off while
  // `initialPath` is still empty), and A's entries rendered under B's userId
  // would send every delete/rename/download to the wrong workspace.
  function isStale(run: number, requestedUserId: string): boolean {
    return run !== loadRun || requestedUserId !== userId.value;
  }

  async function loadDir(path: string) {
    const run = ++loadRun;
    const requestedUserId = userId.value;
    // Only flip to the Loading… placeholder when we have nothing to
    // render yet. A refresh (same path, list already on screen) and a
    // navigation between populated folders both keep the existing grid
    // visible until new data arrives — avoids the flash of "Loading…"
    // for sub-second fetches that the user explicitly triggered (right-
    // click Refresh, hidden-files toggle aside-already-handled, etc.).
    if (rawEntries.value.length === 0) {
      loading.value = true;
    }
    error.value = "";
    if (onBeforeLoad) onBeforeLoad();
    try {
      const result = await fileApi.listDir(requestedUserId, path);
      if (isStale(run, requestedUserId)) return;
      currentPath.value = path;
      rawEntries.value = result.entries;
      buildBreadcrumbs(path);
    } catch (e) {
      if (isStale(run, requestedUserId)) return;
      const message = errorMessage(e);
      if (path !== ".") {
        // A directory the agent deleted under us shouldn't strand the panel,
        // so we still drop back to the workspace root — but the redirect used
        // to be silent, which read as "the double-click did nothing". Restore
        // the failure afterwards (the nested load clears `error` on entry),
        // unless yet another navigation has since taken over.
        loading.value = false;
        await loadDir(".");
        if (loadRun === run + 1) error.value = message;
        return;
      }
      error.value = message;
    } finally {
      // A superseded load must not clear the placeholder the newer one owns.
      if (run === loadRun) loading.value = false;
    }
  }

  function navigateTo(path: string) {
    return loadDir(path);
  }

  function refresh() {
    return loadDir(currentPath.value);
  }

  function getEntryPath(entry: FileEntry): string {
    return currentPath.value === "." ? entry.name : currentPath.value + "/" + entry.name;
  }

  return {
    currentPath,
    entries,
    loading,
    error,
    breadcrumbs,
    loadDir,
    navigateTo,
    refresh,
    getEntryPath,
    fileApi,
  };
}
