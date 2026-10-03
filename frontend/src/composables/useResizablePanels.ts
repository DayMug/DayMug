import { computed, onUnmounted, ref, type Ref } from "vue";
import { useLocalStorage } from "@vueuse/core";

// useResizablePanels owns the desktop chat / workspace split: it persists
// the workspace pane width to localStorage (so the choice survives reloads),
// exposes inline styles for both panes, and wires up mouse + touch drag
// handlers that update the width as the user drags the divider.
//
// The width is anchored on the *workspace* (not chat percent) because UI
// design's reference point is the workspace edge — keeping that anchor
// stable matches the editorial layout intent.
// Exported because useIsNarrow reads the same two entries to work out whether
// the conversation list can be shown inline. Two modules addressing one
// preference by hand-typed string is how they drift apart.
export const WORKSPACE_WIDTH_KEY = "daymug-workspace-px";
export const WORKSPACE_COLLAPSED_KEY = "daymug-workspace-collapsed";
export const WORKSPACE_DEFAULT_WIDTH = 400;

export function useResizablePanels(
  containerRef: Ref<HTMLElement | undefined>,
  options: {
    storageKey?: string;
    collapseStorageKey?: string;
    defaultWidth?: number;
    minWidth?: number;
    maxWidth?: number;
  } = {},
) {
  const storageKey = options.storageKey ?? WORKSPACE_WIDTH_KEY;
  const collapseStorageKey = options.collapseStorageKey ?? WORKSPACE_COLLAPSED_KEY;
  const defaultWidth = options.defaultWidth ?? WORKSPACE_DEFAULT_WIDTH;
  const minWidth = options.minWidth ?? 280;
  const maxFloor = options.maxWidth ?? 720;

  const workspaceWidthPx = useLocalStorage(storageKey, defaultWidth);
  // Collapse hides the workspace pane down to the resize handle, so a
  // user reading long chat output can claim the full chat width with a
  // single click. Persisted so the choice survives reloads — same
  // contract as the width preference next to it. The handle remains
  // visible (and becomes a single-click expander) while collapsed; the
  // collapse button inside the workspace top bar disappears with the
  // pane.
  const isCollapsed = useLocalStorage(collapseStorageKey, false);
  const isDragging = ref(false);

  const chatPanelStyle = computed(() =>
    isCollapsed.value ? { width: "100%" } : { width: `calc(100% - ${workspaceWidthPx.value}px)` },
  );
  const workspacePanelStyle = computed(() =>
    isCollapsed.value
      ? { width: "0px", overflow: "hidden" }
      : { width: `${workspaceWidthPx.value}px` },
  );

  function collapse() {
    isCollapsed.value = true;
  }
  function expand() {
    isCollapsed.value = false;
  }

  function updateChatWidth(clientX: number) {
    if (!containerRef.value) return;
    const rect = containerRef.value.getBoundingClientRect();
    // Drag handle sits on the divider; clientX maps to chat width, so the
    // workspace gets the remainder. Clamp so neither pane disappears.
    const wsWidth = rect.right - clientX;
    const max = Math.min(maxFloor, rect.width - 360);
    workspaceWidthPx.value = Math.min(max, Math.max(minWidth, wsWidth));
  }

  function onDragStart(e: MouseEvent) {
    // Drag is meaningless while the pane is collapsed — the handle's
    // click handler is the expansion path then. Bailing here avoids a
    // 1px drag from accidentally re-running updateChatWidth before the
    // expand state has settled.
    if (isCollapsed.value) return;
    e.preventDefault();
    isDragging.value = true;
    document.addEventListener("mousemove", onDragMove);
    document.addEventListener("mouseup", onDragEnd);
  }

  function onDragMove(e: MouseEvent) {
    if (!isDragging.value) return;
    updateChatWidth(e.clientX);
  }

  function onDragEnd() {
    isDragging.value = false;
    document.removeEventListener("mousemove", onDragMove);
    document.removeEventListener("mouseup", onDragEnd);
  }

  function onTouchStart(e: TouchEvent) {
    if (isCollapsed.value) return;
    if (e.touches.length !== 1) return;
    isDragging.value = true;
    document.addEventListener("touchmove", onTouchMove, { passive: false });
    document.addEventListener("touchend", onTouchEnd);
    document.addEventListener("touchcancel", onTouchEnd);
  }

  function onTouchMove(e: TouchEvent) {
    if (!isDragging.value || e.touches.length !== 1) return;
    e.preventDefault();
    updateChatWidth(e.touches[0].clientX);
  }

  function onTouchEnd() {
    isDragging.value = false;
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", onTouchEnd);
    document.removeEventListener("touchcancel", onTouchEnd);
  }

  // Belt and suspenders: if the component is destroyed mid-drag (e.g. route
  // change while the user holds the mouse), the document listeners would
  // outlive the component without these.
  onUnmounted(() => {
    document.removeEventListener("mousemove", onDragMove);
    document.removeEventListener("mouseup", onDragEnd);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", onTouchEnd);
    document.removeEventListener("touchcancel", onTouchEnd);
  });

  return {
    workspaceWidthPx,
    isCollapsed,
    isDragging,
    chatPanelStyle,
    workspacePanelStyle,
    onDragStart,
    onTouchStart,
    collapse,
    expand,
  };
}
