import { computed, ref, type ComponentPublicInstance } from "vue";
import type { FileEntry } from "@/composables/useFileApi";
import { isPrimaryModifier } from "@/lib/primaryModifier";

export function useWorkspaceSelection(getEntries: () => FileEntry[]) {
  const selectedEntries = ref<Set<string>>(new Set());
  const selectedEntry = ref<string | null>(null);
  const bodyRef = ref<HTMLElement | null>(null);
  const marquee = ref<{
    pointerId: number;
    startX: number;
    startY: number;
    currentX: number;
    currentY: number;
    baseSelection: Set<string>;
    additive: boolean;
    moved: boolean;
  } | null>(null);

  const marqueeStyle = computed(() => {
    const m = marquee.value;
    if (!m || !m.moved) return null;
    const left = Math.min(m.startX, m.currentX);
    const top = Math.min(m.startY, m.currentY);
    return {
      left: `${left}px`,
      top: `${top}px`,
      width: `${Math.abs(m.currentX - m.startX)}px`,
      height: `${Math.abs(m.currentY - m.startY)}px`,
    };
  });

  function setBodyRef(el: Element | ComponentPublicInstance | null) {
    bodyRef.value = el instanceof HTMLElement ? el : null;
  }

  function selectOnly(name: string | null) {
    if (name === null) {
      selectedEntries.value = new Set();
      selectedEntry.value = null;
      return;
    }
    selectedEntries.value = new Set([name]);
    selectedEntry.value = name;
  }

  function toggleSelect(name: string) {
    const next = new Set(selectedEntries.value);
    if (next.has(name)) {
      next.delete(name);
      if (selectedEntry.value === name) {
        const last = Array.from(next).pop();
        selectedEntry.value = last ?? null;
      }
    } else {
      next.add(name);
      selectedEntry.value = name;
    }
    selectedEntries.value = next;
  }

  function handleSelect(entry: FileEntry, event?: MouseEvent | KeyboardEvent) {
    if (event && isPrimaryModifier(event)) {
      toggleSelect(entry.name);
    } else {
      selectOnly(entry.name);
    }
  }

  function pointerPositionInBody(event: PointerEvent) {
    const el = bodyRef.value;
    if (!el) return null;
    const rect = el.getBoundingClientRect();
    return {
      x: event.clientX - rect.left + el.scrollLeft,
      y: event.clientY - rect.top + el.scrollTop,
    };
  }

  function rectsIntersect(
    a: { left: number; top: number; right: number; bottom: number },
    b: { left: number; top: number; right: number; bottom: number },
  ): boolean {
    return a.left <= b.right && a.right >= b.left && a.top <= b.bottom && a.bottom >= b.top;
  }

  function updateMarqueeSelection() {
    const m = marquee.value;
    const body = bodyRef.value;
    if (!m || !body) return;

    const selectionRect = {
      left: Math.min(m.startX, m.currentX),
      top: Math.min(m.startY, m.currentY),
      right: Math.max(m.startX, m.currentX),
      bottom: Math.max(m.startY, m.currentY),
    };
    const bodyRect = body.getBoundingClientRect();
    const next = m.additive ? new Set(m.baseSelection) : new Set<string>();

    body.querySelectorAll<HTMLElement>("[data-entry-name]").forEach((node) => {
      const name = node.dataset.entryName;
      if (!name) return;
      const rect = node.getBoundingClientRect();
      const itemRect = {
        left: rect.left - bodyRect.left + body.scrollLeft,
        top: rect.top - bodyRect.top + body.scrollTop,
        right: rect.right - bodyRect.left + body.scrollLeft,
        bottom: rect.bottom - bodyRect.top + body.scrollTop,
      };
      if (rectsIntersect(selectionRect, itemRect)) {
        next.add(name);
      } else if (!m.additive) {
        next.delete(name);
      }
    });

    selectedEntries.value = next;
    selectedEntry.value = Array.from(next).pop() ?? null;
  }

  function startMarquee(event: PointerEvent) {
    if (event.button !== 0 || event.pointerType === "touch") return;
    const target = event.target as HTMLElement | null;
    if (target?.closest("[data-entry-name], input, textarea, button, a")) return;

    const pos = pointerPositionInBody(event);
    if (!pos) return;
    marquee.value = {
      pointerId: event.pointerId,
      startX: pos.x,
      startY: pos.y,
      currentX: pos.x,
      currentY: pos.y,
      baseSelection: new Set(selectedEntries.value),
      additive: isPrimaryModifier(event),
      moved: false,
    };
    if (!marquee.value.additive) selectOnly(null);
    bodyRef.value?.setPointerCapture?.(event.pointerId);
  }

  function moveMarquee(event: PointerEvent) {
    const m = marquee.value;
    if (!m || m.pointerId !== event.pointerId) return;
    const pos = pointerPositionInBody(event);
    if (!pos) return;
    m.currentX = pos.x;
    m.currentY = pos.y;
    if (Math.abs(m.currentX - m.startX) > 3 || Math.abs(m.currentY - m.startY) > 3) {
      m.moved = true;
    }
    if (m.moved) {
      event.preventDefault();
      updateMarqueeSelection();
    }
  }

  function endMarquee(event: PointerEvent) {
    const m = marquee.value;
    if (!m || m.pointerId !== event.pointerId) return;
    bodyRef.value?.releasePointerCapture?.(event.pointerId);
    marquee.value = null;
  }

  function getEntryByName(name: string | null): FileEntry | undefined {
    if (!name) return undefined;
    return getEntries().find((entry) => entry.name === name);
  }

  return {
    selectedEntries,
    selectedEntry,
    setBodyRef,
    marqueeStyle,
    selectOnly,
    toggleSelect,
    handleSelect,
    startMarquee,
    moveMarquee,
    endMarquee,
    getEntryByName,
  };
}
