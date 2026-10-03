/**
 * Stage the two CasualOffice editor embeds into `public/casual-office/`.
 *
 * ```
 * casual-office/sheets/<version>/…   @casualoffice/sheets   (.xlsx / .xlsm / .csv)
 * casual-office/docs/<version>/…     @casualoffice/docs     (.docx)
 * ```
 *
 * One shared prefix keeps the backend simple: `handler/routes.go` only has to
 * recognise `casual-office/` to apply all three rules at once (immutable
 * caching, pre-compressed bytes, content ETag). Versions are independent, so
 * upgrading one editor swaps only its own directory.
 *
 * ## Copied at build time rather than committed
 *
 * The two editors weigh ~28MB raw. `frontend/dist` is already gitignored, so
 * committing these into `public/` would make them the only build output in
 * git. They are copied out of `node_modules` instead and
 * `public/casual-office/` is gitignored too.
 *
 * ## Large JS ships only as `.gz`
 *
 * The backend has no gzip middleware — it serves embedded bytes verbatim. So
 * the big files are compressed once at build time, the compressed form goes
 * into `go:embed`, and the static handler picks the representation from
 * `Accept-Encoding`. `vite.config.ts` carries the matching dev-server rule;
 * the two must agree or a change works in `pnpm dev` and breaks in a release.
 *
 * ## Both `embed.html` files are generated, not copied
 *
 * Neither upstream copy is usable: the sheets one makes the runtime fetch
 * fonts from Google, and the docs one imports a filename it does not ship.
 * See `renderSheetsHtml` / `renderDocsHtml`.
 *
 * ## Localisation happens here, not in the editors
 *
 * Each bundle's UI text splits in two: strings resolved through an i18n
 * dictionary, and strings hardcoded in JSX. docs has a dictionary we can
 * inject (`patchDocsI18n`); sheets only ever packages EN_US. The menu bar and
 * toolbars — the most visible surfaces — are hardcoded in *both*. So a
 * non-English build has to rewrite the upstream bundle at build time; see
 * `patchLabels` and the word lists under `scripts/i18n/`. Upgrading either
 * package means re-checking those rewrites.
 *
 * Because the rewrite bakes a language into the bundle, `LOCALES` below emits
 * one runtime per language. Only the files that actually carry UI text are
 * duplicated — the three sheets workers hold none and are shared, which is why
 * the language is part of the *filename* and not a directory level: the
 * runtime resolves its workers with `new URL('./x.worker.js', import.meta.url)`
 * and would not find them one directory up.
 *
 * Usage: `pnpm sync:office` (already wired ahead of `dev` / `build`).
 * `CASUAL_OFFICE_OUT` overrides the output directory; the test uses it to
 * write into a temp dir.
 */
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = resolve(here, "..");
const i18nDir = join(here, "i18n");

/**
 * The app locales that get their own baked runtime, in `src/i18n` spelling.
 *
 * `en` is upstream's own language, so it needs no rewriting and no dictionary.
 * Each extra entry costs another copy of the language-bearing bundles
 * (~3.2MB gzipped for sheets, ~0.9MB for docs) inside the release binary, so
 * adding one is a deliberate trade, not a default.
 */
const LOCALES = ["en", "zh"];

/** Word lists per locale. `en` is absent on purpose — upstream is English. */
const SHEETS_LABELS = { zh: "casual-sheets-labels.zh-CN.json" };
const DOCS_LABELS = { zh: "casual-docs-labels.zh-CN.json" };
/** Upstream dictionary + our patch, keyed by the upstream file's name. */
const DOCS_DICTS = { zh: { upstream: "zh-CN.json", patch: "casual-docs-dict.zh-CN.json" } };

/** `lang` attribute for the generated `embed.html`. */
const HTML_LANG = { en: "en", zh: "zh-CN" };

function readJson(path) {
  return JSON.parse(readFileSync(path, "utf8"));
}

/** The sheets runtime looks these ids up; a missing one means a missing font. */
export const FONT_LINK_IDS = ["cs-font-inter", "cs-font-material-symbols"];

/**
 * The three sheets workers. They carry no UI text, so one copy serves every
 * locale — but they must sit in the same directory as the runtime, which
 * starts them via `new URL('./parser.worker.js', import.meta.url)`.
 */
const SHEETS_WORKERS = ["parser.worker.js", "exporter.worker.js", "formula.worker.js"];

/**
 * docs reads its dictionary off an anchor inside `mountEmbedded`.
 *
 * The anchor matches a **prop name** (part of the public API), never a minified
 * variable name — those change on every upstream build. `docxEditorProps` is
 * spread onto the component that takes `i18n`, so writing the dictionary into
 * that object hands it to the i18n provider.
 */
const DOCS_I18N_ANCHOR = "docxEditorProps:{readOnly:";
const DOCS_I18N_PATCH = "docxEditorProps:{i18n:globalThis.__CASUAL_I18N__,readOnly:";

/** Locate a package root by walking up from a file its `exports` map allows. */
function pkgDirVia(specifier, up) {
  let dir = dirname(require.resolve(specifier));
  for (let i = 0; i < up; i++) dir = dirname(dir);
  return dir;
}

function pkgVersion(dir) {
  return JSON.parse(readFileSync(join(dir, "package.json"), "utf8")).version;
}

function writeGz(destPath, contents) {
  writeFileSync(`${destPath}.gz`, gzipSync(contents, { level: 9 }));
}

// ------------------------------------------------------------- localisation

/**
 * Only these props are rewritten — they are unambiguously human-facing.
 *
 * `name` / `text` / `message` are excluded: in both bundles they double as
 * internal identifiers (worker names, font names, command names), so
 * translating them would change behaviour rather than wording.
 */
const LABEL_PROPS = ["label", "title", "tooltip", "placeholder", "description", "ariaLabel"];

/**
 * Translate the UI text that upstream hardcodes into its JSX.
 *
 * Two shapes are handled:
 *
 * ```js
 * label:"Bold"                  // plain literal
 * label:`${x?"✓ ":""}Bold`      // template, checked-state prefix kept intact
 * ```
 *
 * **Every entry must match at least once.** A silent miss leaves the UI half
 * translated, which is worse than leaving it in English — and it looks
 * identical to "upstream never translated this", so it would never be found.
 */
export function patchLabels(source, labels, pkg) {
  const missed = [];
  let patched = source;

  for (const [en, translated] of Object.entries(labels)) {
    const quoted = JSON.stringify(en).slice(1, -1);
    let hits = 0;

    for (const prop of LABEL_PROPS) {
      const plain = `${prop}:"${quoted}"`;
      hits += patched.split(plain).length - 1;
      patched = patched.split(plain).join(`${prop}:"${translated}"`);
    }
    const template = `}${quoted}\``;
    hits += patched.split(template).length - 1;
    patched = patched.split(template).join(`}${translated}\``);

    if (hits === 0) missed.push(en);
  }

  if (missed.length > 0) {
    throw new Error(
      `${missed.length} entries of the ${pkg} word list never matched the bundle:\n` +
        `${missed.map((m) => `  - ${m}`).join("\n")}\n` +
        `Upstream reworded these. Fix or drop them in scripts/i18n/.`,
    );
  }
  return patched;
}

// -------------------------------------------------------------------- theme

/**
 * Pull both editors' accent onto DayMug's own tokens.
 *
 * DayMug's palette is deliberately achromatic (`src/assets/index.css`:
 * `--primary: oklch(0.13 0 0)` in light, `oklch(0.985 0 0)` in dark), so the
 * editors get a grey ramp rather than a brand hue — the point is that they
 * stop looking like a third-party widget dropped into the page.
 *
 * This works because both accents are CSS variables, not baked colours:
 *
 * - CasualOffice's own chrome uses the `--color-accent` family;
 * - the Univer core inside sheets uses `--univer-primary-*`, injected by the
 *   runtime **at mount time** as a `<style>` appended to `<head>`. That lands
 *   after ours, so at equal specificity it wins — which is why those
 *   declarations carry `!important` (custom properties accept it).
 *
 * Dark mode only matters for sheets (docs has no `set.theme` and stays light),
 * but both get the rules so a future docs release needs no change here.
 */
const ACCENT = {
  light: {
    accent: "#070707",
    hover: "#222222",
    active: "#383838",
    fg: "#FAFAFA",
    soft: "#EBEBEB",
    bright: "#333333",
    selected: "rgba(7, 7, 7, 0.08)",
    selectedStrong: "rgba(7, 7, 7, 0.16)",
  },
  dark: {
    accent: "#FAFAFA",
    hover: "#DEDEDE",
    active: "#BEBEBE",
    fg: "#070707",
    soft: "rgba(250, 250, 250, 0.14)",
    bright: "#E4E4E4",
    selected: "rgba(250, 250, 250, 0.16)",
    selectedStrong: "rgba(250, 250, 250, 0.26)",
  },
};

/** Univer's primary ramp. One achromatic scale, reversed for dark. */
const UNIVER_PRIMARY = {
  light: {
    50: "#F3F3F3",
    100: "#EBEBEB",
    200: "#D1D1D1",
    300: "#B1B1B1",
    400: "#5B5B5B",
    500: "#070707",
    600: "#161616",
    700: "#222222",
    800: "#333333",
    900: "#484848",
  },
  dark: {
    50: "#161616",
    100: "#222222",
    200: "#333333",
    300: "#484848",
    400: "#9E9E9E",
    500: "#FAFAFA",
    600: "#E4E4E4",
    700: "#D1D1D1",
    800: "#B1B1B1",
    900: "#868686",
  },
};

/** Indentation matches the `<style>` block the CSS is embedded into. */
const PAD = " ".repeat(6);

function rule(selector, declarations) {
  return [`${PAD}${selector} {`, ...declarations.map((d) => `${PAD}  ${d}`), `${PAD}}`].join("\n");
}

function accentRule(selector, tone) {
  const c = ACCENT[tone];
  return rule(selector, [
    `--color-accent: ${c.accent};`,
    `--color-accent-hover: ${c.hover};`,
    `--color-accent-active: ${c.active};`,
    `--color-accent-fg: ${c.fg};`,
    `--color-accent-soft: ${c.soft};`,
    `--color-accent-bright: ${c.bright};`,
    `--accent-gradient: linear-gradient(135deg, ${c.bright}, ${c.accent});`,
    `--color-focus-ring: ${c.accent};`,
    `--color-selected: ${c.selected};`,
    `--color-selected-strong: ${c.selectedStrong};`,
  ]);
}

function univerRule(selector, tone) {
  return rule(
    selector,
    Object.entries(UNIVER_PRIMARY[tone]).map(
      ([step, hex]) => `--univer-primary-${step}: ${hex} !important;`,
    ),
  );
}

/** @param includeUniver sheets passes true (Univer core); docs has none. */
export function renderThemeCss(includeUniver) {
  const blocks = [accentRule(":root", "light"), accentRule("[data-theme='dark']", "dark")];
  if (includeUniver) {
    blocks.push(univerRule(":root", "light"), univerRule(".univer-dark", "dark"));
  }
  return blocks.join("\n");
}

// ------------------------------------------------------------------- sheets

/**
 * The sheets `embed.html`.
 *
 * Upstream's copy lets the runtime append two `fonts.googleapis.com` `<link>`s
 * to `document.head` at mount time (Inter + Material Symbols). A DayMug box
 * may well have no egress, and a missing Material Symbols renders every icon
 * as its literal name ("content_copy").
 *
 * The runtime's injection skips any id that already exists, so pre-declaring
 * the same ids pointing at self-hosted copies displaces it without touching a
 * byte of the bundle. The ids come from upstream, so an upgrade has to
 * re-check them — `sync-casual-office.test.ts` reads them back out of the
 * bundle and compares.
 */
export function renderSheetsHtml(locale, runtimeFile) {
  const links = FONT_LINK_IDS.map(
    (id) => `    <link id="${id}" rel="stylesheet" href="./fonts/${id}.css" />`,
  ).join("\n");

  return `<!doctype html>
<html lang="${HTML_LANG[locale]}">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Spreadsheet</title>
    <!-- Not decoration: seeing these ids already present stops the runtime
         from reaching out to fonts.googleapis.com. With no egress a missing
         icon font renders as literal text like "content_copy". -->
${links}
    <style>
      html,
      body {
        margin: 0;
        padding: 0;
        height: 100%;
        overflow: hidden;
      }
      #casual-embed-root {
        width: 100%;
        height: 100%;
      }

      /* Accent pulled onto DayMug's tokens. The Univer rules need !important:
         its theme variables are injected into the end of <head> at runtime,
         after this block. */
${renderThemeCss(true)}
    </style>
  </head>
  <body>
    <div id="casual-embed-root"></div>
    <script type="module">
      import { mountEmbedded } from './${runtimeFile}'
      const root = document.getElementById('casual-embed-root')
      if (root) mountEmbedded({ root })
    </script>
  </body>
</html>
`;
}

/**
 * Inter, from fontsource's variable build — one woff2 covers weights 100-900.
 *
 * The family has to be declared as `Inter` (Google's name), not fontsource's
 * `Inter Variable`: the placeholder link is displacing Google's, so the name
 * must line up.
 *
 * The 0.20.0 bundle never actually references Inter (its stacks are all
 * `ui-sans-serif, system-ui, …`) yet still emits that CDN link. A real font is
 * shipped anyway rather than an empty stub — the placeholder must not 404, and
 * an upstream release that starts using it needs no change here.
 */
function syncInter(fontsDir) {
  const file = "inter-latin-wght-normal.woff2";
  cpSync(require.resolve(`@fontsource-variable/inter/files/${file}`), join(fontsDir, file));
  writeFileSync(
    join(fontsDir, "cs-font-inter.css"),
    [
      "@font-face {",
      "  font-family: 'Inter';",
      "  font-style: normal;",
      "  font-weight: 100 900;",
      "  font-display: swap;",
      `  src: url('./${file}') format('woff2-variations');`,
      "}",
      "",
    ].join("\n"),
  );
}

/**
 * Material Symbols Outlined.
 *
 * `@material-symbols/font-400` rather than `material-symbols`: the latter
 * ships a four-axis variable font (FILL / GRAD / opsz / wght) at 3.8MB, and
 * the bundle pins every axis (`opsz 20, wght 400, FILL 0, GRAD 0`), so none of
 * the interpolation data is reachable. The former is the same upstream release
 * pre-instanced at wght=400 — 0.47MB with all 6605 glyphs. woff2 is already
 * compressed, so gzip cannot recover that 3.3MB; only the package swap can.
 *
 * The CSS is copied as-is instead of hand-written: besides `@font-face` it
 * carries the `.material-symbols-outlined` class and the `font-feature-settings`
 * line that makes the ligatures form. Without those the icons render as their
 * literal names.
 */
function syncMaterialSymbols(fontsDir) {
  const dir = pkgDirVia("@material-symbols/font-400/package.json", 0);
  cpSync(
    join(dir, "material-symbols-outlined.woff2"),
    join(fontsDir, "material-symbols-outlined.woff2"),
  );
  cpSync(join(dir, "outlined.css"), join(fontsDir, "cs-font-material-symbols.css"));
}

/** Language-bearing filenames. `en` keeps the bare name upstream would use. */
function localized(file, locale) {
  if (locale === "en") return file;
  const dot = file.indexOf(".");
  return `${file.slice(0, dot)}.${locale}${file.slice(dot)}`;
}

function syncSheets(root) {
  const embed = dirname(require.resolve("@casualoffice/sheets/embed/embed.html"));
  const version = pkgVersion(resolve(embed, "..", ".."));
  assertVersionPinned("SHEETS_VERSION", "@casualoffice/sheets", version);

  const dest = join(root, "sheets", version);
  mkdirSync(join(dest, "fonts"), { recursive: true });

  // Shared across locales — no UI text in any of them.
  for (const worker of SHEETS_WORKERS) {
    writeGz(join(dest, worker), readFileSync(join(embed, worker)));
  }

  const source = readFileSync(join(embed, "embed-runtime.js"), "utf8");
  for (const locale of LOCALES) {
    const labels = SHEETS_LABELS[locale];
    const runtime = labels
      ? patchLabels(source, readJson(join(i18nDir, labels)).labels, "@casualoffice/sheets")
      : source;
    const runtimeFile = localized("embed-runtime.js", locale);
    writeGz(join(dest, runtimeFile), Buffer.from(runtime));
    writeFileSync(
      join(dest, localized("embed.html", locale)),
      renderSheetsHtml(locale, runtimeFile),
    );
  }

  syncInter(join(dest, "fonts"));
  syncMaterialSymbols(join(dest, "fonts"));

  return version;
}

// --------------------------------------------------------------------- docs

/**
 * The docs `embed.html`. Three things differ from the sheets one:
 *
 * 1. **Dictionary.** docs has no `set.locale` command and `mountEmbedded`
 *    takes no locale argument, and the embed bundle contains no translations
 *    at all. The dictionary is attached to `globalThis.__CASUAL_I18N__`
 *    *before* the runtime is imported, and the injected prop picks it up (see
 *    `patchDocsI18n`). A missing dictionary falls back to English, not a crash.
 * 2. **CSP.** Three code paths in the bundle reach the network: a
 *    `fonts.googleapis.com` URL assembled from the document's fonts, the
 *    Wikipedia search API, and an 880MB web-llm model. `connect-src 'self'`
 *    blocks the latter two. Fonts are allowed through; with no egress those
 *    requests just fail and any non-builtin font falls back to a system one.
 *    To block them too, tighten `style-src` / `font-src` to `'self'`.
 * 3. **Filename.** Upstream's `embed.html` imports `./embed-runtime.js` while
 *    the package actually ships `.mjs` — their copy is simply broken.
 *
 * `docId` travels in the query string because `mountEmbedded` reads `app` /
 * `docId` / `viewMode` / `cspNonce` from `window.location.search` and offers
 * no other configuration entry point.
 */
export function renderDocsHtml(locale, { bootFile, dictFile }) {
  const csp = [
    "default-src 'self'",
    // Wikipedia search and the web-llm download both go through fetch.
    "connect-src 'self'",
    "img-src 'self' data: blob:",
    // The dictionary is its own file, so scripts need no 'unsafe-inline'.
    "script-src 'self'",
    // Styles must keep 'unsafe-inline': the runtime injects <style> into head
    // and components lean on inline style attributes. Tightening this strips
    // the editor down to unstyled markup.
    "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com",
    "font-src 'self' data: https://fonts.gstatic.com",
  ].join("; ");

  const dictTag = dictFile ? `\n    <script src="./${dictFile}"></script>` : "";

  return `<!doctype html>
<html lang="${HTML_LANG[locale]}">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta http-equiv="Content-Security-Policy" content="${csp}" />
    <title>Document</title>
    <link rel="stylesheet" href="./embed-runtime.css" />
    <style>
      html,
      body {
        margin: 0;
        padding: 0;
        height: 100%;
        overflow: hidden;
      }
      #casual-embed-root {
        width: 100%;
        height: 100%;
      }

      /* docs declares its tokens in embed-runtime.css's :root; this block comes
         after it, so equal specificity is enough — no !important needed. */
${renderThemeCss(false)}

      /* The slice of Tailwind's preflight the runtime assumes but does not
         ship: embed-runtime.css is built for a host page that already loaded
         Tailwind. Without it every <button> keeps the browser's grey fill and
         thick outset border, and border-r / border-b dividers draw nothing
         because border-style stays none. :where() keeps this at zero
         specificity so any upstream rule still wins over it. */
      :where(*, ::before, ::after) {
        box-sizing: border-box;
        border: 0 solid;
      }
      :where(button, input, select, textarea) {
        margin: 0;
        padding: 0;
        font: inherit;
        color: inherit;
        background-color: transparent;
        border-radius: 0;
      }
      :where(button, [role='button']) {
        cursor: pointer;
      }

      /* Let the toolbar **wrap** instead of scrolling sideways. Upstream sets
         overflow-x-auto, which in a narrow window hides half the buttons
         behind a scrollbar nobody thinks to look for. Upstream's utilities are
         scoped as \`.ep-root .rounded-full\`, so these need the same prefix to
         win; without it the wrapped bar stays a pill whose curved ends clip
         the first and last rows. */
      .ep-root [data-testid='formatting-bar'] {
        flex-wrap: wrap;
        overflow-x: visible;
        border-radius: 8px;
        min-height: 30px;
        row-gap: 2px;
      }
      .ep-root [data-testid='editor-toolbar'],
      .ep-root [data-testid='title-bar'] {
        flex-wrap: wrap;
        overflow-x: visible;
      }
    </style>
  </head>
  <body>
    <div id="casual-embed-root"></div>
    <!-- Separate files rather than inline scripts, so script-src 'self' holds.
         The dictionary has to be in place before the runtime runs
         (mountEmbedded reads __CASUAL_I18N__ immediately) — classic scripts
         run before module scripts, so this ordering is guaranteed. -->${dictTag}
    <script type="module" src="./${bootFile}"></script>
  </body>
</html>
`;
}

/**
 * Give `mountEmbedded` somewhere to read the dictionary from.
 *
 * **This rewrites an upstream artifact** and must be re-checked when
 * `@casualoffice/docs` is upgraded. It is kept as light as possible: one
 * replacement, anchored on a public prop name. Anything other than exactly one
 * match throws, so an upstream restructure turns into a **failed build** rather
 * than a silently English UI.
 */
export function patchDocsI18n(source) {
  const hits = source.split(DOCS_I18N_ANCHOR).length - 1;
  if (hits !== 1) {
    throw new Error(
      `The docs i18n anchor matched ${hits} times (expected 1).\n` +
        `@casualoffice/docs restructured mountEmbedded; re-check ${DOCS_I18N_ANCHOR}.`,
    );
  }
  return source.replace(DOCS_I18N_ANCHOR, DOCS_I18N_PATCH);
}

/**
 * Build a dictionary out of upstream's `i18n/<lang>.json` plus our patch.
 * The embed bundle never loads those files itself — they are shipped for the
 * React/library entry points.
 *
 * Upstream's `zh-CN.json` leaves **247 keys null** (about a third), which fall
 * back to the bundle's English at runtime; `casual-docs-dict.zh-CN.json` fills
 * in the ones that are actually visible.
 *
 * Every key in the patch must already exist upstream: a typo would otherwise
 * just never take effect, and on screen that is indistinguishable from
 * "upstream didn't translate it".
 */
export function docsDictionary(pkgRoot, spec) {
  const base = readJson(join(pkgRoot, "i18n", spec.upstream));
  const patch = readJson(join(i18nDir, spec.patch)).dict;
  const unknown = [];

  const merge = (target, source, path) => {
    for (const [key, value] of Object.entries(source)) {
      const at = path ? `${path}.${key}` : key;
      if (!(key in target)) {
        unknown.push(at);
        continue;
      }
      if (value !== null && typeof value === "object") merge(target[key], value, at);
      else target[key] = value;
    }
  };
  merge(base, patch, "");

  if (unknown.length > 0) {
    throw new Error(
      `${spec.patch} has ${unknown.length} keys that upstream's dictionary lacks:\n` +
        `${unknown.map((k) => `  - ${k}`).join("\n")}\n` +
        `Upstream restructured its dictionary, or the key names are misspelled.`,
    );
  }
  return base;
}

function syncDocs(root) {
  const embed = dirname(require.resolve("@casualoffice/docs/embed/embed.html"));
  const pkgRoot = resolve(embed, "..", "..");
  const version = pkgVersion(pkgRoot);
  assertVersionPinned("DOCS_VERSION", "@casualoffice/docs", version);

  const dest = join(root, "docs", version);
  mkdirSync(dest, { recursive: true });

  // 50KB of CSS, shared by every locale. Compressed anyway so it takes the
  // same serveGzipped path as the JS.
  writeGz(join(dest, "embed-runtime.css"), readFileSync(join(embed, "embed-runtime.css")));

  const source = readFileSync(join(embed, "embed-runtime.mjs"), "utf8");
  for (const locale of LOCALES) {
    const dictSpec = DOCS_DICTS[locale];
    const labels = DOCS_LABELS[locale];

    let runtime = source;
    if (dictSpec) runtime = patchDocsI18n(runtime);
    if (labels) {
      runtime = patchLabels(runtime, readJson(join(i18nDir, labels)).labels, "@casualoffice/docs");
    }

    const runtimeFile = localized("embed-runtime.mjs", locale);
    const bootFile = localized("boot.mjs", locale);
    const dictFile = dictSpec ? localized("i18n.js", locale) : null;

    writeGz(join(dest, runtimeFile), Buffer.from(runtime));
    // The boot script is a separate file (not inlined) so script-src 'self'
    // can stay closed.
    writeFileSync(
      join(dest, bootFile),
      `import { mountEmbedded } from './${runtimeFile}'\n\n` +
        `const root = document.getElementById('casual-embed-root')\n` +
        `if (root) mountEmbedded({ root })\n`,
    );
    if (dictSpec) {
      // ~26KB, ~7KB compressed. Its own file so it can be compressed, revalidate
      // on its own ETag, and avoid needing script-src 'unsafe-inline'.
      const dict = `globalThis.__CASUAL_I18N__=${JSON.stringify(docsDictionary(pkgRoot, dictSpec))}\n`;
      writeGz(join(dest, dictFile), Buffer.from(dict));
    }
    writeFileSync(
      join(dest, localized("embed.html", locale)),
      renderDocsHtml(locale, { bootFile, dictFile }),
    );
  }

  return version;
}

// -------------------------------------------------------------------- entry

/**
 * The frontend keeps the versions in `src/lib/office/embed.ts` because they go
 * into the URL and the runtime cannot read a build-time dependency version.
 * A mismatch stops the build here — otherwise the page would request a
 * directory that does not exist, far from the cause.
 */
function assertVersionPinned(constName, pkg, version) {
  const file = join(frontendDir, "src", "lib", "office", "embed.ts");
  const pinned = new RegExp(`${constName}\\s*=\\s*"([^"]+)"`).exec(readFileSync(file, "utf8"))?.[1];
  if (pinned !== version) {
    throw new Error(
      `${constName} (${pinned}) does not match the installed ${pkg} (${version}).\n` +
        `Update src/lib/office/embed.ts after upgrading the dependency.`,
    );
  }
}

/**
 * Everything that can change the output: the script itself, the two package
 * versions, the locale set, and the word lists. Hashing them lets a repeat run
 * skip the work — which matters because compressing ~28MB at level 9, once per
 * locale, costs the better part of a minute, and `dev` and `build` both run
 * this first.
 *
 * Deliberately *not* hashing the upstream bundles themselves: reading 28MB to
 * decide whether to re-read it saves little, and a package cannot change
 * without its version changing.
 */
function stampOf(versions) {
  const parts = [
    readFileSync(fileURLToPath(import.meta.url), "utf8"),
    JSON.stringify(versions),
    JSON.stringify(LOCALES),
    ...Object.values(SHEETS_LABELS).map((f) => readFileSync(join(i18nDir, f), "utf8")),
    ...Object.values(DOCS_LABELS).map((f) => readFileSync(join(i18nDir, f), "utf8")),
    ...Object.values(DOCS_DICTS).map((d) => readFileSync(join(i18nDir, d.patch), "utf8")),
  ];
  return createHash("sha256").update(parts.join("\u0000")).digest("hex");
}

/** Resolve the installed versions without touching the (large) bundles. */
function installedVersions() {
  return {
    sheets: pkgVersion(
      resolve(dirname(require.resolve("@casualoffice/sheets/embed/embed.html")), "..", ".."),
    ),
    docs: pkgVersion(
      resolve(dirname(require.resolve("@casualoffice/docs/embed/embed.html")), "..", ".."),
    ),
  };
}

export function sync({ outRoot, force = false } = {}) {
  const root = outRoot ?? join(frontendDir, "public", "casual-office");
  const versions = installedVersions();
  const stampFile = join(root, ".stamp");
  const stamp = stampOf(versions);

  if (!force && existsSync(stampFile) && readFileSync(stampFile, "utf8") === stamp) {
    return { ...versions, root, cached: true };
  }

  // Rebuild the whole prefix so stale version directories don't pile up.
  rmSync(root, { recursive: true, force: true });
  mkdirSync(root, { recursive: true });

  const result = { sheets: syncSheets(root), docs: syncDocs(root), root, cached: false };
  // Written last: a crash halfway through must not leave a stamp claiming the
  // output is complete.
  writeFileSync(stampFile, stamp);
  return result;
}

// Stay quiet when imported (by the test).
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const { sheets, docs, root, cached } = sync({ outRoot: process.env.CASUAL_OFFICE_OUT });
  const how = cached ? "up to date" : "written";
  console.log(`casual-office → ${root}  (sheets ${sheets}, docs ${docs}, ${how})`);
}
