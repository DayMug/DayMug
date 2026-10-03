import { ref, type Ref } from "vue";

// The slice of FileWorkbench the artifact panel drives: both guards resolve
// false when the user keeps an unsaved buffer open.
export interface ArtifactWorkbenchHandle {
  requestNavigateAway: () => Promise<boolean>;
  requestClosePath: (path: string) => Promise<boolean>;
}

export interface UseArtifactPanelOptions {
  // Template ref of the panel's FileWorkbench; null until it mounts.
  workbench: Ref<ArtifactWorkbenchHandle | null>;
  onFullscreenChange: (fullscreen: boolean) => void;
  onCollapse: () => void;
}

// useArtifactPanel owns the "panel" file-open target of the workspace: the
// lightweight preview tabs, which of them is active, and whether the folder
// browser or the workbench is showing. FileWorkbench keeps the actual editor
// buffers alive behind the directory view, so opening the folder browser never
// discards unsaved text. Instance-scoped (call inside setup()).
export function useArtifactPanel({
  workbench,
  onFullscreenChange,
  onCollapse,
}: UseArtifactPanelOptions) {
  const paths = ref<string[]>([]);
  const activePath = ref<string | null>(null);
  const directoryOpen = ref(true);
  const fullscreen = ref(false);
  const editMode = ref(false);

  async function canLeave(): Promise<boolean> {
    if (!activePath.value || !workbench.value) return true;
    return workbench.value.requestNavigateAway();
  }

  async function open(path: string) {
    if (path !== activePath.value && !(await canLeave())) return;
    if (!paths.value.includes(path)) paths.value.push(path);
    activePath.value = path;
    directoryOpen.value = false;
    editMode.value = false;
  }

  async function activate(path: string) {
    if (path !== activePath.value && !(await canLeave())) return;
    activePath.value = path;
    directoryOpen.value = false;
    editMode.value = false;
  }

  function finishClose(path: string) {
    const index = paths.value.indexOf(path);
    if (index < 0) return;
    paths.value.splice(index, 1);
    if (activePath.value !== path) return;
    activePath.value = paths.value[index - 1] ?? paths.value[index] ?? null;
    directoryOpen.value = activePath.value === null;
    editMode.value = false;
  }

  async function close(path: string) {
    if (workbench.value && !(await workbench.value.requestClosePath(path))) return;
    finishClose(path);
  }

  function showDirectory() {
    // The workbench remains mounted behind the directory, so this does not
    // replace its path or discard an editor buffer.
    directoryOpen.value = true;
  }

  function toggleFullscreen() {
    fullscreen.value = !fullscreen.value;
    onFullscreenChange(fullscreen.value);
  }

  function collapse() {
    if (fullscreen.value) {
      fullscreen.value = false;
      onFullscreenChange(false);
    }
    onCollapse();
  }

  function reset() {
    paths.value = [];
    activePath.value = null;
    directoryOpen.value = true;
    editMode.value = false;
    if (fullscreen.value) {
      fullscreen.value = false;
      onFullscreenChange(false);
    }
  }

  return {
    paths,
    activePath,
    directoryOpen,
    fullscreen,
    editMode,
    open,
    activate,
    close,
    showDirectory,
    toggleFullscreen,
    collapse,
    reset,
  };
}
