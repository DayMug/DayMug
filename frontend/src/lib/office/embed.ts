/**
 * Addresses and pinned versions for the two CasualOffice editor embeds.
 *
 * The versions live here rather than being read from `package.json` because
 * they go into the URL and the running app cannot see build-time dependency
 * versions. `scripts/sync-casual-office.mjs` reads this file back and compares
 * it against what is actually installed, failing the build on a mismatch — so
 * this is the single source of truth and the script keeps it from drifting.
 */

/** Must match the installed `@casualoffice/sheets`. */
export const SHEETS_VERSION = "0.20.0";

/** Must match the installed `@casualoffice/docs`. */
export const DOCS_VERSION = "1.4.2";

/** App locales that have a baked embed. Mirrors `LOCALES` in the sync script. */
const EMBED_LOCALES = ["en", "zh"] as const;

export type EmbedLocale = (typeof EMBED_LOCALES)[number];

/**
 * Map a vue-i18n locale onto an embed build.
 *
 * The UI language is baked into the bundle at build time (upstream ships no
 * usable dictionary for sheets), so an unrecognised locale falls back to
 * English rather than requesting a bundle that was never emitted.
 */
export function embedLocale(locale: string): EmbedLocale {
  const base = locale.toLowerCase().split("-")[0];
  return (EMBED_LOCALES as readonly string[]).includes(base) ? (base as EmbedLocale) : "en";
}

/** `en` keeps upstream's bare filenames; other locales get an infix. */
function localized(file: string, locale: EmbedLocale): string {
  if (locale === "en") return file;
  const dot = file.indexOf(".");
  return `${file.slice(0, dot)}.${locale}${file.slice(dot)}`;
}

/**
 * The spreadsheet embed URL.
 *
 * **`viewMode` must be passed explicitly.** `mountEmbedded` defaults to
 * `preview`, and preview does two things:
 *
 * 1. adds a read-only permission point over the whole workbook, so every edit
 *    is rejected with "The range is protected, and you do not have edit
 *    permission.";
 * 2. drops `chrome` to `none`, so the menu bar, the toolbars and the **sheet
 *    tab strip** are not rendered — a multi-sheet xlsx then looks like it has
 *    one sheet.
 *
 * That is exactly what view-only should do, which is why `.xlsm` stays on
 * preview. It is also more reliable than the `set.readonly` command: the
 * embed's `mountEmbedded` only registers handlers for theme, features and
 * viewMode — `set.readonly` and `set.locale` reach nobody.
 *
 * The version is part of the path because the runtime resolves its workers
 * with `new URL('./parser.worker.js', …)`, so those filenames are fixed and
 * cannot carry a content hash. A version directory means an upgrade produces a
 * fresh set of URLs and no stale strong-cached copy survives it.
 */
export function sheetsEmbedUrl(options: { readOnly: boolean; locale: EmbedLocale }): string {
  const query = new URLSearchParams({
    app: "sheet",
    viewMode: options.readOnly ? "preview" : "editor",
  });
  const file = localized("embed.html", options.locale);
  return `/casual-office/sheets/${SHEETS_VERSION}/${file}?${query.toString()}`;
}

/**
 * The document embed URL. docs takes all of its configuration from
 * `window.location.search` (`app` / `docId` / `viewMode` / `cspNonce`); there
 * is no other entry point.
 *
 * @param docId the file path. The editor treats it as an opaque identifier and
 *   hands it straight back to the host.
 */
export function docsEmbedUrl(docId: string, locale: EmbedLocale): string {
  const query = new URLSearchParams({ app: "docs", docId, viewMode: "editor" });
  const file = localized("embed.html", locale);
  return `/casual-office/docs/${DOCS_VERSION}/${file}?${query.toString()}`;
}
