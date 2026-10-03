import type { Directive } from "vue";

// Lives apart from useMarkdown so main.ts can register the directive without
// pulling the markdown pipeline (markdown-it, highlight.js) into the entry
// module's own import list. Mermaid itself is loaded on first use.

let currentTheme: "default" | "dark" | null = null;
let idCounter = 0;

async function ensureMermaid(theme: "default" | "dark") {
  const mermaid = (await import("mermaid")).default;
  if (currentTheme !== theme) {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme });
    currentTheme = theme;
  }
  return mermaid;
}

// Render every not-yet-rendered .mermaid-diagram inside `root`. Safe to call
// repeatedly (e.g. on streaming updates): rendered nodes are skipped, and a
// node whose source is still incomplete throws and is simply retried next call.
export async function renderMermaidDiagrams(root: HTMLElement): Promise<void> {
  const nodes = Array.from(
    root.querySelectorAll<HTMLElement>(".mermaid-diagram:not([data-rendered])"),
  );
  if (nodes.length === 0) return;

  const theme = document.querySelector(".dark") ? "dark" : "default";
  const mermaid = await ensureMermaid(theme);

  for (const node of nodes) {
    const source = node.getAttribute("data-mermaid");
    if (!source) continue;
    try {
      const { svg } = await mermaid.render(`mermaid-${idCounter++}`, source);
      node.innerHTML = svg;
      node.setAttribute("data-rendered", "true");
    } catch {
      // Incomplete/invalid diagram — keep the source fallback and retry later.
    }
  }
}

// Apply alongside `v-html` on the element receiving rendered markdown; it turns
// the mermaid placeholders into diagrams once the HTML lands and on each update.
export const vMermaid: Directive<HTMLElement> = {
  mounted(el) {
    void renderMermaidDiagrams(el);
  },
  updated(el) {
    void renderMermaidDiagrams(el);
  },
};
