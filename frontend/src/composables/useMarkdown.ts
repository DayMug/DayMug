import MarkdownIt from "markdown-it";
import cjkFriendly from "markdown-it-cjk-friendly";
import { ref } from "vue";
import hljs from "@/lib/highlight";

type Katex = (typeof import("katex"))["default"];

// Per-render scratch state markdown-it threads through every rule.
interface RenderEnv {
  mathPending?: boolean;
  breaks?: boolean;
}

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
  highlight(str: string, lang: string): string {
    if (lang && hljs.getLanguage(lang)) {
      try {
        return `<pre class="hljs"><code>${hljs.highlight(str, { language: lang }).value}</code></pre>`;
      } catch {
        // fall through
      }
    }
    return `<pre class="hljs"><code>${md.utils.escapeHtml(str)}</code></pre>`;
  },
});

// CommonMark only closes `**` when the delimiter is right-flanking, so
// `**结论。**后文` — full-width punctuation before the closer, a CJK letter after
// it — stays literal asterisks. Agents write exactly that shape in almost every
// Chinese summary; the plugin relaxes the flanking rule for CJK neighbours.
md.use(cjkFriendly);

// A filesystem path is not a URL. `[the report](/home/u/proj/out.html)` and
// `![chart](out.png)` both render as targets the browser resolves against
// DayMug's own origin, so the user clicks through to a 404 — in the chat's
// case navigating the tab away from a turn that is still running. Files are
// reachable only through the authenticated /api/users/:id/files endpoints,
// which markdown never has enough context to address, so anything that isn't
// an openable web URL loses its href/src and stays readable as text. The path
// itself remains visible so users can copy the exact filesystem location.
const OPENABLE_URL = /^(?:https?:\/\/|mailto:)/i;

const defaultLinkOpen =
  md.renderer.rules.link_open ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));

md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  const token = tokens[idx];
  const href = token.attrGet("href") ?? "";
  if (OPENABLE_URL.test(href)) {
    // A chat tab holds a running turn; following a link in place abandons it.
    token.attrSet("target", "_blank");
    token.attrSet("rel", "noopener noreferrer");
  } else if (!href.startsWith("#")) {
    token.attrs = (token.attrs ?? []).filter(([name]) => name !== "href");
  }
  return defaultLinkOpen(tokens, idx, options, env, self);
};

const defaultImage =
  md.renderer.rules.image ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));

md.renderer.rules.image = (tokens, idx, options, env, self) => {
  const token = tokens[idx];
  const src = token.attrGet("src") ?? "";
  if (!OPENABLE_URL.test(src) && !src.startsWith("data:image/")) {
    // Dropping src renders the alt text, which beats a broken-image icon.
    token.attrs = (token.attrs ?? []).filter(([name]) => name !== "src");
  }
  return defaultImage(tokens, idx, options, env, self);
};

// Intercept ```mermaid fences and emit a placeholder carrying the raw source
// in a data attribute. The inner <pre> is a no-JS / parse-error fallback that
// the async renderer replaces with an <svg> once mermaid resolves the diagram.
const defaultFence =
  md.renderer.rules.fence ??
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));

function isEscaped(src: string, pos: number): boolean {
  let slashCount = 0;
  for (let i = pos - 1; i >= 0 && src[i] === "\\"; i--) {
    slashCount++;
  }
  return slashCount % 2 === 1;
}

// KaTeX (~260 KB plus its stylesheet and fonts) is only needed by the few
// messages that contain math, so it loads on the first render that meets a
// math token. Until then the source renders as escaped text; `katexVersion`
// bumps once the module lands, and every render that produced such a
// placeholder read it, so exactly those components re-render with real math.
let katex: Katex | null = null;
let katexLoading: Promise<void> | null = null;
export const katexVersion = ref(0);

export function loadKatex(): Promise<void> {
  if (katex) return Promise.resolve();
  katexLoading ??= Promise.all([import("katex"), import("katex/dist/katex.min.css")])
    .then(([mod]) => {
      katex = mod.default;
      katexVersion.value++;
    })
    .catch((err: unknown) => {
      // Leave the placeholders up; the next math render retries the import.
      katexLoading = null;
      console.error("[markdown] failed to load KaTeX", err);
    });
  return katexLoading;
}

function renderMath(source: string, displayMode: boolean, env: RenderEnv): string {
  if (!katex) {
    env.mathPending = true;
    void loadKatex();
    const escaped = md.utils.escapeHtml(source);
    return displayMode
      ? `<div class="math-pending">${escaped}</div>`
      : `<span class="math-pending">${escaped}</span>`;
  }
  return katex.renderToString(source, {
    displayMode,
    throwOnError: false,
    strict: "warn",
    trust: false,
  });
}

md.inline.ruler.before("escape", "math_inline", (state, silent) => {
  const start = state.pos;
  const src = state.src;

  if (src.startsWith("\\(", start) && !isEscaped(src, start)) {
    for (let end = start + 2; end < src.length - 1; end++) {
      if (!src.startsWith("\\)", end) || isEscaped(src, end)) continue;
      const content = src.slice(start + 2, end);
      if (!content.trim()) return false;
      if (!silent) {
        const token = state.push("math_inline", "math", 0);
        token.markup = "\\(\\)";
        token.content = content;
      }
      state.pos = end + 2;
      return true;
    }
    return false;
  }

  if (src[start] !== "$" || src[start + 1] === "$" || isEscaped(src, start)) {
    return false;
  }

  for (let end = start + 1; end < src.length; end++) {
    if (src[end] !== "$" || isEscaped(src, end)) continue;
    const content = src.slice(start + 1, end);
    if (!content.trim()) return false;
    if (!silent) {
      const token = state.push("math_inline", "math", 0);
      token.markup = "$";
      token.content = content;
    }
    state.pos = end + 1;
    return true;
  }

  return false;
});

md.block.ruler.before("fence", "math_block", (state, startLine, endLine, silent) => {
  const start = state.bMarks[startLine] + state.tShift[startLine];
  const max = state.eMarks[startLine];
  const firstLine = state.src.slice(start, max);

  const delimiter = firstLine.startsWith("$$")
    ? { open: "$$", close: "$$" }
    : firstLine.startsWith("\\[")
      ? { open: "\\[", close: "\\]" }
      : null;

  if (!delimiter) return false;
  if (silent) return true;

  const lines: string[] = [];
  const firstContent = firstLine.slice(delimiter.open.length);
  const sameLineEnd = firstContent.indexOf(delimiter.close);
  let nextLine = startLine;

  if (sameLineEnd >= 0) {
    lines.push(firstContent.slice(0, sameLineEnd));
  } else {
    if (firstContent) lines.push(firstContent);
    for (nextLine = startLine + 1; nextLine < endLine; nextLine++) {
      const lineStart = state.bMarks[nextLine] + state.tShift[nextLine];
      const lineMax = state.eMarks[nextLine];
      const line = state.src.slice(lineStart, lineMax);
      const end = line.indexOf(delimiter.close);
      if (end >= 0) {
        lines.push(line.slice(0, end));
        break;
      }
      lines.push(line);
    }
  }

  const token = state.push("math_block", "math", 0);
  token.block = true;
  token.markup = `${delimiter.open}${delimiter.close}`;
  token.content = lines.join("\n").trim();
  state.line = nextLine + 1;
  return true;
});

md.renderer.rules.math_inline = (tokens, idx, _options, env: RenderEnv) =>
  renderMath(tokens[idx].content, false, env);
md.renderer.rules.math_block = (tokens, idx, _options, env: RenderEnv) =>
  `${renderMath(tokens[idx].content, true, env)}\n`;

md.renderer.rules.fence = (tokens, idx, options, env, self) => {
  const token = tokens[idx];
  const lang = token.info ? token.info.trim().split(/\s+/g)[0] : "";
  if (lang === "mermaid") {
    const escaped = md.utils.escapeHtml(token.content);
    return `<div class="mermaid-diagram" data-mermaid="${escaped}"><pre class="hljs"><code>${escaped}</code></pre></div>`;
  }
  return defaultFence(tokens, idx, options, env, self);
};

// Typed text has no "Markdown or plain?" signal worth guessing from, and in a
// typed message a single Enter means a new line: CommonMark would join
// "第一行\n第二行" into one line. `breaks` renders each such newline as <br>
// while every other Markdown construct still works. Off for agent output and
// files, which are written as Markdown and hard-wrap their source.
md.renderer.rules.softbreak = (_tokens, _idx, options, env: RenderEnv) =>
  env.breaks || options.breaks ? "<br>\n" : "\n";

// A chat re-renders rows whose text hasn't changed (remounts on conversation
// switch, density toggles, list reshuffles), and markdown-it + highlight.js
// reparse from scratch each time. Keyed by the exact text; Map order doubles
// as recency, so the first key is always the eviction candidate.
const RENDER_CACHE_LIMIT = 200;
const renderCache = new Map<string, { html: string; mathPending: boolean }>();

export interface RenderMarkdownOptions {
  // Off for text that changes on every frame (a streaming reply): each
  // intermediate snapshot is rendered exactly once, and caching them would
  // only evict the settled messages the cache exists for.
  cache?: boolean;
  // Keep single newlines as line breaks (user-typed messages).
  breaks?: boolean;
}

export function renderMarkdown(text: string, options: RenderMarkdownOptions = {}): string {
  const useCache = options.cache !== false;
  const breaks = options.breaks === true;
  const key = breaks ? `\0breaks\0${text}` : text;
  const hit = useCache ? renderCache.get(key) : undefined;
  if (hit && !(hit.mathPending && katex)) {
    renderCache.delete(key);
    renderCache.set(key, hit);
    // Keep the caller subscribed until the math can actually render.
    if (hit.mathPending) void katexVersion.value;
    return hit.html;
  }

  const env: RenderEnv = { breaks };
  const html = md.render(text, env);
  const mathPending = !!env.mathPending;
  if (mathPending) void katexVersion.value;

  if (useCache) {
    renderCache.delete(key);
    renderCache.set(key, { html, mathPending });
    if (renderCache.size > RENDER_CACHE_LIMIT) {
      const oldest = renderCache.keys().next().value;
      if (oldest !== undefined) renderCache.delete(oldest);
    }
  }
  return html;
}

// Test hook: the cache is module state shared across a test file.
export function clearMarkdownCache() {
  renderCache.clear();
}
