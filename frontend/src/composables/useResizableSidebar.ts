import { computed, onUnmounted, ref, type Ref } from "vue";
import { useLocalStorage } from "@vueuse/core";

export function useResizableSidebar(
  panelRef: Ref<HTMLElement | undefined>,
  options: {
    storageKey?: string;
    defaultWidth?: number;
    minWidth?: number;
    maxWidth?: number;
  } = {},
) {
  const storageKey = options.storageKey ?? "daymug-conversation-panel-px";
  const defaultWidth = options.defaultWidth ?? 288;
  const minWidth = options.minWidth ?? 240;
  const maxWidth = options.maxWidth ?? 480;
  const clamp = (width: number) =>
    Number.isFinite(width) ? Math.min(maxWidth, Math.max(minWidth, width)) : defaultWidth;

  const widthPx = useLocalStorage(storageKey, defaultWidth);
  widthPx.value = clamp(widthPx.value);
  const isDragging = ref(false);
  const panelStyle = computed(() => ({
    width: `${widthPx.value}px`,
    minWidth: `${widthPx.value}px`,
  }));

  function updateWidth(clientX: number) {
    if (!panelRef.value) return;
    const { left } = panelRef.value.getBoundingClientRect();
    widthPx.value = clamp(clientX - left);
  }

  function onDragStart(e: MouseEvent) {
    e.preventDefault();
    isDragging.value = true;
    document.addEventListener("mousemove", onDragMove);
    document.addEventListener("mouseup", onDragEnd);
  }

  function onDragMove(e: MouseEvent) {
    if (!isDragging.value) return;
    updateWidth(e.clientX);
  }

  function onDragEnd() {
    isDragging.value = false;
    document.removeEventListener("mousemove", onDragMove);
    document.removeEventListener("mouseup", onDragEnd);
  }

  function onTouchStart(e: TouchEvent) {
    if (e.touches.length !== 1) return;
    isDragging.value = true;
    document.addEventListener("touchmove", onTouchMove, { passive: false });
    document.addEventListener("touchend", onTouchEnd);
    document.addEventListener("touchcancel", onTouchEnd);
  }

  function onTouchMove(e: TouchEvent) {
    if (!isDragging.value || e.touches.length !== 1) return;
    e.preventDefault();
    updateWidth(e.touches[0].clientX);
  }

  function onTouchEnd() {
    isDragging.value = false;
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", onTouchEnd);
    document.removeEventListener("touchcancel", onTouchEnd);
  }

  onUnmounted(() => {
    document.removeEventListener("mousemove", onDragMove);
    document.removeEventListener("mouseup", onDragEnd);
    document.removeEventListener("touchmove", onTouchMove);
    document.removeEventListener("touchend", onTouchEnd);
    document.removeEventListener("touchcancel", onTouchEnd);
  });

  return {
    widthPx,
    isDragging,
    panelStyle,
    onDragStart,
    onTouchStart,
  };
}
