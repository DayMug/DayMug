/**
 * Regression tests for `sync-casual-office.mjs`.
 *
 * The script actually runs and the real output is asserted — nothing is
 * mocked, because the whole value of this script is "did it copy and rewrite
 * the right bytes", and a mocked filesystem would test none of that.
 *
 * It writes to the canonical `public/casual-office/` rather than a temp
 * directory so it shares the script's stamp cache with `dev` and `build`;
 * compressing ~28MB at level 9 once per locale takes the better part of a
 * minute and there is no reason to pay it twice. The directory is gitignored
 * build output that both of those commands produce anyway.
 *
 * The three assertions that matter most:
 *
 * 1. **Font interception** (sheets): the runtime injects
 *    `fonts.googleapis.com` links into `<head>` unless an element with the
 *    same id is already there. The ids come from upstream, so they are read
 *    back **out of the bundle** rather than hardcoded — a rename must fail
 *    here. Offline, a missing icon font renders as literal text.
 * 2. **i18n injection** (docs): the script rewrites one place in an upstream
 *    artifact. The anchor must match exactly once, or the build should fail
 *    rather than silently ship an English UI.
 * 3. **Localisation** (both): the menu bar strings are hardcoded in JSX and
 *    can only be replaced by exact `label:"…"` substitution. A word-list entry
 *    that matches nothing must fail the build — a half-translated UI is harder
 *    to notice than an untranslated one.
 */
import { existsSync, readFileSync, statSync } from "node:fs";
import { gunzipSync } from "node:zlib";
import { join, resolve } from "node:path";
import { beforeAll, describe, expect, it } from "vitest";

const SCRIPT = resolve(process.cwd(), "scripts", "sync-casual-office.mjs");
const modules = (...parts: string[]) => resolve(process.cwd(), "node_modules", ...parts);
const SHEETS_RUNTIME = modules("@casualoffice/sheets/dist/embed/embed-runtime.js");
const DOCS_RUNTIME = modules("@casualoffice/docs/dist/embed/embed-runtime.mjs");

// The script is .mjs with no type declarations; import it by runtime URL so
// tsc does not try to resolve it.
type SyncModule = {
  sync: (opts?: { outRoot?: string; force?: boolean }) => {
    sheets: string;
    docs: string;
    root: string;
  };
  patchLabels: (source: string, labels: Record<string, string>, pkg: string) => string;
  patchDocsI18n: (source: string) => string;
};

let mod: SyncModule;
let sheetsDir: string;
let docsDir: string;
let sheetsHtml: string;
let sheetsHtmlZh: string;
let docsHtml: string;
let docsHtmlZh: string;

beforeAll(async () => {
  mod = (await import(new URL(SCRIPT, "file://").href)) as SyncModule;
  const out = mod.sync();
  sheetsDir = join(out.root, "sheets", out.sheets);
  docsDir = join(out.root, "docs", out.docs);
  sheetsHtml = readFileSync(join(sheetsDir, "embed.html"), "utf8");
  sheetsHtmlZh = readFileSync(join(sheetsDir, "embed.zh.html"), "utf8");
  docsHtml = readFileSync(join(docsDir, "embed.html"), "utf8");
  docsHtmlZh = readFileSync(join(docsDir, "embed.zh.html"), "utf8");
  // Compressing the bundles twice over is slow on a cold cache.
}, 180_000);

function gunzipText(path: string): string {
  return gunzipSync(readFileSync(path)).toString("utf8");
}

describe("spreadsheet embed", () => {
  it("puts the runtime and all three workers, gzipped, in one directory", () => {
    // The runtime starts its workers with
    // new URL('./parser.worker.js', import.meta.url), so a subdirectory would
    // break them. Only .gz is written; the Go handler and the Vite dev
    // middleware both pick a representation from Accept-Encoding.
    for (const file of [
      "embed-runtime.js",
      "embed-runtime.zh.js",
      "parser.worker.js",
      "exporter.worker.js",
      "formula.worker.js",
    ]) {
      expect(statSync(join(sheetsDir, `${file}.gz`)).size).toBeGreaterThan(0);
      expect(existsSync(join(sheetsDir, file))).toBe(false);
    }
  });

  it("shares the workers across locales instead of copying them", () => {
    // The workers carry no UI text and are 2.6MB compressed. A per-locale copy
    // would be pure waste inside the release binary.
    expect(existsSync(join(sheetsDir, "parser.worker.zh.js.gz"))).toBe(false);
  });

  it("compresses the runtime to under a third of its size", () => {
    // Guards against this silently degrading into "copy the raw file and
    // rename it", which would quietly restore the full download.
    const gz = statSync(join(sheetsDir, "embed-runtime.js.gz")).size;
    expect(gz).toBeLessThan(statSync(SHEETS_RUNTIME).size / 3);
  });

  it("pre-declares exactly the link ids the bundle looks for", () => {
    const ids = [...readFileSync(SHEETS_RUNTIME, "utf8").matchAll(/id:"(cs-font-[a-z-]+)"/g)].map(
      (m) => m[1],
    );
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) expect(sheetsHtml).toContain(`id="${id}"`);
  });

  it("ships the pre-instanced icon font, not the 3.8MB variable one", () => {
    const font = statSync(join(sheetsDir, "fonts/material-symbols-outlined.woff2")).size;
    expect(font).toBeLessThan(1_000_000);
  });

  it("keeps the ligature settings in the icon CSS", () => {
    // Without liga, Material Symbols draws icon names as literal text.
    const css = readFileSync(join(sheetsDir, "fonts/cs-font-material-symbols.css"), "utf8");
    expect(css).toContain("font-feature-settings");
    expect(css).toContain("liga");
  });

  it("references no external CDN", () => {
    for (const text of [
      sheetsHtml,
      readFileSync(join(sheetsDir, "fonts/cs-font-inter.css"), "utf8"),
      readFileSync(join(sheetsDir, "fonts/cs-font-material-symbols.css"), "utf8"),
    ]) {
      expect(text).not.toMatch(/https?:\/\//);
    }
  });
});

describe("baked-in language", () => {
  it("leaves the English build byte-identical to upstream", () => {
    // en is upstream's own language. Rewriting it would be pure risk for no
    // gain, and this is what makes the rewrite machinery opt-in per locale.
    expect(gunzipText(join(sheetsDir, "embed-runtime.js.gz"))).toBe(
      readFileSync(SHEETS_RUNTIME, "utf8"),
    );
  });

  it("translates the spreadsheet's hardcoded menu bar in the zh build", () => {
    // This bundle contains no Chinese at all and only packages the EN_US
    // locale, so build-time substitution is the only route.
    const runtime = gunzipText(join(sheetsDir, "embed-runtime.zh.js.gz"));
    for (const zh of ['label:"编辑"', 'label:"插入"', 'label:"数据"', 'label:"格式"']) {
      expect(runtime).toContain(zh);
    }
    for (const en of ['label:"Edit"', 'label:"Insert"', 'label:"Data"', 'label:"Format"']) {
      expect(runtime).not.toContain(en);
    }
  });

  it("translates the document's hardcoded menu bar in the zh build", () => {
    // Upstream missed these: Edit / View are written into the JSX, so the
    // dictionary cannot reach them.
    const runtime = gunzipText(join(docsDir, "embed-runtime.zh.mjs.gz"));
    expect(runtime).toContain('label:"编辑"');
    expect(runtime).toContain('label:"视图"');
    expect(runtime).not.toContain('label:"Edit"');
    expect(runtime).not.toContain('label:"View"');
  });

  it("marks each embed.html with its own language", () => {
    expect(sheetsHtml).toContain('<html lang="en">');
    expect(sheetsHtmlZh).toContain('<html lang="zh-CN">');
    expect(docsHtml).toContain('<html lang="en">');
    expect(docsHtmlZh).toContain('<html lang="zh-CN">');
  });

  it("fails the build when a word list entry matches nothing", () => {
    // Word lists track upstream's wording. If upstream rewords and we do not
    // follow, the result is a half-translated UI — harder to spot than an
    // untranslated one, so it has to fail at build time.
    expect(() => mod.patchLabels('label:"Edit"', { Nope: "没有" }, "pkg")).toThrow(
      /never matched the bundle/,
    );
    expect(mod.patchLabels('label:"Edit"', { Edit: "编辑" }, "pkg")).toBe('label:"编辑"');
  });
});

describe("document embed", () => {
  it("finds the i18n injection anchor exactly once upstream", () => {
    // This rewrites an upstream artifact. Zero matches means upstream
    // restructured; more than one means the anchor is too loose. Both have to
    // fail the build rather than ship an English UI.
    const source = readFileSync(DOCS_RUNTIME, "utf8");
    expect(source.split("docxEditorProps:{readOnly:").length - 1).toBe(1);
    expect(() => mod.patchDocsI18n("nothing to anchor on")).toThrow(/matched 0 times/);
  });

  it("carries the injected lookup into the compressed zh runtime", () => {
    const runtime = gunzipText(join(docsDir, "embed-runtime.zh.mjs.gz"));
    expect(runtime).toContain("docxEditorProps:{i18n:globalThis.__CASUAL_I18N__,readOnly:");
  });

  it("loads the dictionary as its own file, before the runtime", () => {
    const dict = gunzipText(join(docsDir, "i18n.zh.js.gz"));
    expect(dict).toContain("globalThis.__CASUAL_I18N__=");
    // Upstream's zh-CN.json has hundreds of entries; far fewer means the wrong
    // file was read.
    const parsed = JSON.parse(dict.replace(/^[^=]+=/, "").trim()) as Record<string, unknown>;
    expect(Object.keys(parsed).length).toBeGreaterThan(20);

    // Ordering is load-bearing: mountEmbedded reads __CASUAL_I18N__ on entry.
    expect(docsHtmlZh.indexOf("i18n.zh.js")).toBeLessThan(docsHtmlZh.indexOf("boot.zh.mjs"));
  });

  it("fills in the keys upstream left untranslated", () => {
    // Upstream's zh-CN.json leaves 247 keys null, which fall back to English.
    const dict = gunzipText(join(docsDir, "i18n.zh.js.gz"));
    const parsed = JSON.parse(dict.replace(/^[^=]+=/, "").trim()) as Record<string, unknown>;
    const toolbar = parsed.toolbar as Record<string, unknown>;
    expect(toolbar.tools).toBe("工具");
    expect(toolbar.wordCount).toBe("字数统计");
  });

  it("ships no dictionary for the English build", () => {
    expect(existsSync(join(docsDir, "i18n.js.gz"))).toBe(false);
    expect(docsHtml).not.toContain("i18n.js");
  });

  it("blocks outbound fetches via CSP while allowing fonts", () => {
    // connect-src covers the Wikipedia search and the web-llm model download.
    expect(docsHtml).toContain("connect-src 'self'");
    expect(docsHtml).toContain("https://fonts.googleapis.com");
  });

  it("keeps both scripts external so script-src needs no unsafe-inline", () => {
    expect(docsHtml).toContain("script-src 'self'");
    expect(docsHtml).not.toMatch(/script-src[^;]*unsafe-inline/);
    expect(existsSync(join(docsDir, "boot.mjs"))).toBe(true);
  });

  it("writes the runtime and CSS as .gz only", () => {
    for (const file of ["embed-runtime.mjs", "embed-runtime.zh.mjs", "embed-runtime.css"]) {
      expect(statSync(join(docsDir, `${file}.gz`)).size).toBeGreaterThan(0);
      expect(existsSync(join(docsDir, file))).toBe(false);
    }
  });
});

describe("theme", () => {
  it("pulls both embeds onto DayMug's accent", () => {
    // The editors run in an iframe and cannot inherit the host's design
    // tokens, but both accents are CSS variables, so redeclaring them in the
    // generated embed.html is enough. DayMug's palette is achromatic.
    for (const html of [sheetsHtml, docsHtml]) {
      expect(html).toContain("--color-accent: #070707;");
      expect(html).toContain("--color-accent-hover: #222222;");
    }
  });

  it("marks the Univer variables !important", () => {
    // Univer injects its theme as a <style> appended to <head> at mount time,
    // i.e. after this block; without !important the override does nothing.
    expect(sheetsHtml).toContain("--univer-primary-600: #161616 !important;");
    expect(sheetsHtml).toContain(".univer-dark {");
  });

  it("wraps the document toolbar instead of scrolling it sideways", () => {
    // Upstream utilities are `.ep-root .rounded-full` etc.; an unprefixed
    // override loses on specificity and the wrapped bar stays a clipped pill.
    expect(docsHtml).toContain(".ep-root [data-testid='formatting-bar'] {");
    expect(docsHtml).toContain("flex-wrap: wrap;");
  });

  it("ships the preflight the docs runtime assumes", () => {
    // Without it every toolbar <button> keeps the browser's outset border.
    expect(docsHtml).toContain(":where(*, ::before, ::after) {");
    expect(docsHtml).toContain(":where(button, input, select, textarea) {");
    expect(docsHtml).toContain("border: 0 solid;");
  });
});
