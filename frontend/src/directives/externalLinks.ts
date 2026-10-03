import type { Directive } from "vue";

// Rendered markdown lands inside rows that own the click themselves — a
// thinking block collapses when its body is clicked. Following a link there
// folded the transcript away at the same moment the new tab opened, so a
// click that lands on an anchor stops at the anchor and leaves the
// conversation exactly as it was.
//
// Opening in a new tab is normally markdown-it's doing (useMarkdown puts
// target/rel on every openable href). The handler re-asserts it rather than
// trusting the HTML, because a chat surface can receive an anchor that never
// passed through that renderer, and a chat tab holds a running turn: the one
// thing a link must never do is navigate it away.
function onClick(event: MouseEvent) {
  const clicked = event.target;
  if (!(clicked instanceof Element)) return;
  const anchor = clicked.closest("a[href]");
  if (!anchor) return;
  // An in-document anchor has to stay in this tab — that's how it scrolls.
  if ((anchor.getAttribute("href") ?? "").startsWith("#")) return;
  anchor.setAttribute("target", "_blank");
  anchor.setAttribute("rel", "noopener noreferrer");
  event.stopPropagation();
}

export const vExternalLinks: Directive<HTMLElement> = {
  mounted(el) {
    el.addEventListener("click", onClick);
  },
  unmounted(el) {
    el.removeEventListener("click", onClick);
  },
};
