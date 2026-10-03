import { beforeAll, describe, it, expect, vi } from "vitest";
import { effect, stop } from "vue";
import { clearMarkdownCache, katexVersion, loadKatex, renderMarkdown } from "./useMarkdown";

describe("renderMarkdown", () => {
  // KaTeX is lazy in production; these cases describe the settled output.
  beforeAll(() => loadKatex());
  it("renders headings", () => {
    const html = renderMarkdown("# Hello");
    expect(html).toContain("<h1>");
    expect(html).toContain("Hello");
  });

  it("closes bold that ends in full-width punctuation before CJK text", () => {
    const html = renderMarkdown("**增长主要来自华东大客户续约。**销售额同比增长 18.4%。");
    expect(html).toContain("<strong>增长主要来自华东大客户续约。</strong>销售额");
    expect(html).not.toContain("**");
  });

  it("renders inline code", () => {
    const html = renderMarkdown("Use `const x = 1` here");
    expect(html).toContain("<code>");
    expect(html).toContain("const x = 1");
  });

  it("renders code blocks with language", () => {
    const html = renderMarkdown("```js\nconsole.log('hi')\n```");
    expect(html).toContain("<pre");
    expect(html).toContain("hljs");
  });

  it("renders lists", () => {
    const html = renderMarkdown("- one\n- two\n- three");
    expect(html).toContain("<ul>");
    expect(html).toContain("<li>");
  });

  it("escapes raw HTML", () => {
    const html = renderMarkdown("<script>alert('xss')</script>");
    expect(html).not.toContain("<script>");
  });

  it("renders bold and italic", () => {
    const html = renderMarkdown("**bold** and *italic*");
    expect(html).toContain("<strong>bold</strong>");
    expect(html).toContain("<em>italic</em>");
  });

  it("renders inline math with KaTeX", () => {
    const html = renderMarkdown("其中 $C_{a,h} = \\sum_{n=1}^N \\omega_{n,h}^2$ 为组合集中度。");
    expect(html).toContain('class="katex"');
    expect(html).toContain("C");
    expect(html).not.toContain("$C_{a,h}");
  });

  it("renders display math with KaTeX", () => {
    const html = renderMarkdown(
      "$$\\sigma_{i,h}^2 = \\sigma_{a,h}^2 \\left[C_{a,h} + (1 - C_{a,h})\\rho_{a,h}\\right], \\tag{4.2}$$",
    );
    expect(html).toContain('class="katex-display"');
    expect(html).toContain("4.2");
    expect(html).not.toContain("$$");
  });

  it("renders LaTeX bracket display math with KaTeX", () => {
    const html = renderMarkdown(
      "核心关系通常是：\n\n\\[ \\text{输入 Token}+\\text{推理/输出所占 Token}\\leq\\text{上下文窗口} \\]",
    );
    expect(html).toContain('class="katex-display"');
    expect(html).toContain("输入 Token");
    expect(html).not.toContain("\\[");
  });

  it("renders LaTeX parenthesis inline math with KaTeX", () => {
    const html = renderMarkdown("其中 \\(x^2 + y^2\\) 是行内公式。");
    expect(html).toContain('class="katex"');
    expect(html).not.toContain('class="katex-display"');
    expect(html).not.toContain("\\(");
  });

  it("leaves unmatched dollar text alone", () => {
    const html = renderMarkdown("The total is $5 today.");
    expect(html).toContain("$5 today");
    expect(html).not.toContain('class="katex"');
  });

  it("turns a mermaid fence into a placeholder carrying the escaped source", () => {
    const html = renderMarkdown("```mermaid\ngraph TD; A-->B\n```");
    expect(html).toContain('class="mermaid-diagram"');
    expect(html).toContain('data-mermaid="graph TD; A--&gt;B');
    // A non-mermaid fence is still highlighted as a normal code block.
    expect(renderMarkdown("```js\n1\n```")).not.toContain("mermaid-diagram");
  });

  // A filesystem path resolves against DayMug's own origin, so a clickable
  // one is always a 404 — and in the chat it navigates away from a running
  // turn. See the OPENABLE_URL note in useMarkdown.ts.
  it("strips the href from a link that points at a filesystem path", () => {
    expect(renderMarkdown("[report](/home/alice/proj/out.html)")).not.toContain("href");
    expect(renderMarkdown("[report](out/report.html)")).not.toContain("href");
    expect(renderMarkdown("[x](javascript:alert(1))")).not.toContain("href");
    // The link text still has to read normally.
    expect(renderMarkdown("[report](out/report.html)")).toContain("report");
  });

  it("keeps web links clickable and opens them in a new tab", () => {
    const html = renderMarkdown("[docs](https://example.com/a)");
    expect(html).toContain('href="https://example.com/a"');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener noreferrer"');
  });

  it("opens a bare URL in a new tab too", () => {
    // Agents write links as plain text far more often than as [text](url);
    // linkify turns those into anchors, which must carry the same target.
    const html = renderMarkdown("see https://example.com/report for details");
    expect(html).toContain('href="https://example.com/report"');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener noreferrer"');
  });

  it("keeps in-document anchors without making them new tabs", () => {
    const html = renderMarkdown("[top](#heading)");
    expect(html).toContain('href="#heading"');
    expect(html).not.toContain("_blank");
  });

  it("drops the src of an image that points at a filesystem path", () => {
    const html = renderMarkdown("![chart](/home/alice/proj/chart.png)");
    expect(html).not.toContain("src");
    expect(html).toContain('alt="chart"');
    expect(renderMarkdown("![c](https://example.com/c.png)")).toContain(
      'src="https://example.com/c.png"',
    );
  });

  it("highlights the curated languages and their aliases", () => {
    const samples: Record<string, string> = {
      ts: "const x: number = 1;",
      python: "def f(): return 1",
      go: "func main() {}",
      rust: "fn main() {}",
      sh: 'echo "hi"',
      yml: "key: value",
      toml: "[section]",
      html: "<div></div>",
      dockerfile: "FROM alpine",
    };
    for (const [lang, code] of Object.entries(samples)) {
      expect(renderMarkdown("```" + lang + "\n" + code + "\n```"), lang).toContain("hljs-");
    }
  });

  it("renders a fence in an unregistered language as escaped plain code", () => {
    const html = renderMarkdown("```brainfuck\n<+>\n```");
    expect(html).toContain("&lt;+&gt;");
    expect(html).not.toContain("hljs-");
  });
});

describe("renderMarkdown cache", () => {
  it("returns the same html for repeated text without reparsing", () => {
    clearMarkdownCache();
    const text = "# cached heading " + Math.random();
    const first = renderMarkdown(text);
    const second = renderMarkdown(text);
    expect(second).toBe(first);
  });

  it("serves a hit from the cache instead of calling markdown-it again", async () => {
    vi.resetModules();
    const mod = await import("./useMarkdown");
    const MarkdownIt = (await import("markdown-it")).default;
    const render = vi.spyOn(MarkdownIt.prototype, "render");
    try {
      mod.renderMarkdown("**memo**");
      mod.renderMarkdown("**memo**");
      mod.renderMarkdown("**memo**", { cache: false });
      expect(render).toHaveBeenCalledTimes(2);
    } finally {
      render.mockRestore();
    }
  });

  it("evicts the least recently used entry beyond 200", async () => {
    vi.resetModules();
    const mod = await import("./useMarkdown");
    const MarkdownIt = (await import("markdown-it")).default;
    const render = vi.spyOn(MarkdownIt.prototype, "render");
    try {
      mod.renderMarkdown("keep");
      for (let i = 0; i < 199; i++) mod.renderMarkdown(`filler ${i}`);
      mod.renderMarkdown("keep"); // refresh: now most recent
      mod.renderMarkdown("one more"); // evicts "filler 0", not "keep"
      render.mockClear();

      mod.renderMarkdown("keep");
      expect(render).not.toHaveBeenCalled();
      mod.renderMarkdown("filler 0");
      expect(render).toHaveBeenCalledTimes(1);
    } finally {
      render.mockRestore();
    }
  });
});

describe("lazy KaTeX", () => {
  it("shows the source until KaTeX loads, then re-renders subscribers", async () => {
    vi.resetModules();
    const mod = await import("./useMarkdown");
    const text = "area $a^2$ here";

    let html = "";
    let runs = 0;
    const runner = effect(() => {
      runs++;
      html = mod.renderMarkdown(text);
    });
    try {
      expect(html).toContain('class="math-pending"');
      expect(html).toContain("a^2");
      expect(html).not.toContain('class="katex"');

      await mod.loadKatex();

      expect(runs).toBe(2);
      expect(html).toContain('class="katex"');
    } finally {
      stop(runner);
    }
  });

  it("does not subscribe math-free renders to the KaTeX load", () => {
    let runs = 0;
    const runner = effect(() => {
      runs++;
      renderMarkdown("no math at all");
    });
    try {
      katexVersion.value++;
      expect(runs).toBe(1);
    } finally {
      stop(runner);
    }
  });
});
