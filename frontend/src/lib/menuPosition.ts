// Clamp a fixed-positioned popup so it stays within the viewport.
// Returns {left, top} that should be applied to a fixed-position element.
// Falls back to (x, y) if the element is not yet rendered.
export function clampMenuPosition(
  el: HTMLElement,
  x: number,
  y: number,
  margin = 8,
): { left: number; top: number } {
  const rect = el.getBoundingClientRect();
  const vw = window.innerWidth;
  const vh = window.innerHeight;
  const w = rect.width || 0;
  const h = rect.height || 0;
  const maxLeft = Math.max(margin, vw - w - margin);
  const maxTop = Math.max(margin, vh - h - margin);
  return {
    left: Math.min(Math.max(margin, x), maxLeft),
    top: Math.min(Math.max(margin, y), maxTop),
  };
}
