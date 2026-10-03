// Shared i18n parity logic, used by both the Vitest suite (`parity.test.ts`)
// and the standalone CLI checker (`scripts/check-i18n.mjs`) so the same rules
// run in CI and in the pre-commit hook. No vue-i18n / Vite imports here on
// purpose: this module must load under a bare `node` process.

export type LocaleTree = { [key: string]: string | LocaleTree };

export type Problem = {
  severity: "error" | "warning";
  kind:
    | "missing-key"
    | "extra-key"
    | "shape-mismatch"
    | "empty-value"
    | "placeholder-mismatch"
    | "unescaped-syntax"
    | "untranslated";
  locale: string;
  key: string;
  detail: string;
};

// Flatten a nested locale object into dot-path -> string-value pairs. Object
// nodes are kept too (mapped to the sentinel below) so we can detect a key
// that is an object in one locale but a string in another.
const OBJECT_NODE = Symbol("object-node");

export function flatten(tree: LocaleTree, prefix = ""): Map<string, string | typeof OBJECT_NODE> {
  const out = new Map<string, string | typeof OBJECT_NODE>();
  for (const [k, v] of Object.entries(tree)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === "object") {
      out.set(key, OBJECT_NODE);
      for (const [ck, cv] of flatten(v as LocaleTree, key)) out.set(ck, cv);
    } else {
      out.set(key, v as string);
    }
  }
  return out;
}

// `{'@'}` and friends are literal escapes, not interpolation: they render the
// quoted character verbatim. A locale needs them wherever the plain character
// would be message syntax, and the other locale often does not — so they must
// be stripped before any parity comparison.
const LITERAL_ESCAPE = /\{'[^']*'\}/g;

// Interpolation tokens vue-i18n understands: {name} and {0}. We compare the
// multiset of tokens so a translation can reorder but not drop/invent them.
export function placeholders(value: unknown): string[] {
  if (typeof value !== "string") return [];
  return (value.replace(LITERAL_ESCAPE, "").match(/\{[^}]+\}/g) ?? []).sort();
}

// vue-i18n compiles every message at render time, and `@` (linked message) and
// `|` (plural branches) are syntax there. A bare one throws a CompileError from
// inside the render function, which unmounts the whole app — the user sees a
// blank page, not a broken string. Neither form is used anywhere in DayMug, so
// any unescaped occurrence is a bug; write it as `{'@'}` / `{'|'}`.
export function messageSyntaxHazards(value: string): string[] {
  const bare = value.replace(LITERAL_ESCAPE, "");
  return [...new Set([...bare].filter((ch) => ch === "@" || ch === "|"))];
}

// Values that are legitimately identical across locales (brand names, acronyms,
// protocol tokens). Compared case-insensitively. Keeps the "untranslated"
// heuristic from firing on things that simply have no translation.
const SAME_ACROSS_LOCALES = new Set(
  [
    "DayMug",
    "Claude",
    "Codex",
    "OpenAI",
    "Anthropic",
    "Bark",
    "GitHub",
    "API",
    "URL",
    "HTTP",
    "HTTPS",
    "SSE",
    "JSON",
    "YAML",
    "CLI",
    "ID",
    "UUID",
    "Token",
    "Webhook",
    "Markdown",
    "Git",
    "SSH",
    "npm",
    "pnpm",
    "OK",
    "Logo",
    "PIN",
    "MCP",
    "TTY",
    "PushDeer",
    "CLI (-p)",
    "Slack",
    "App ID",
    "App Secret",
  ].map((s) => s.toLowerCase()),
);

function looksUntranslated(key: string, value: string): boolean {
  const trimmed = value.trim();
  if (trimmed.length < 4) return false;
  // Must contain Latin letters to be a candidate; pure punctuation/digits or
  // values that are only interpolation tokens are never "untranslated".
  if (!/[A-Za-z]/.test(trimmed)) return false;
  // URLs and example/placeholder strings are intentionally locale-agnostic.
  if (/:\/\//.test(trimmed) || /…$/.test(trimmed)) return false;
  // Language endonyms (the "English"/"中文" switcher labels) are meant to be
  // shown in their own language, so identical en/zh values are correct.
  if (/^language\./.test(key)) return false;
  const withoutTokens = trimmed.replace(/\{[^}]+\}/g, "").trim();
  if (!withoutTokens) return false;
  return !SAME_ACROSS_LOCALES.has(trimmed.toLowerCase());
}

export type CheckOptions = {
  reference?: string; // locale whose values define "the English source"
  checkUntranslated?: boolean;
};

// Compare every locale against the union of all keys. The reference locale
// (default "en") is additionally used for the untranslated heuristic.
export function checkLocales(
  locales: Record<string, LocaleTree>,
  opts: CheckOptions = {},
): Problem[] {
  const reference = opts.reference ?? "en";
  const names = Object.keys(locales);
  const flat: Record<string, Map<string, string | typeof OBJECT_NODE>> = {};
  for (const name of names) flat[name] = flatten(locales[name]);

  const allKeys = new Set<string>();
  for (const name of names) for (const k of flat[name].keys()) allKeys.add(k);

  const problems: Problem[] = [];
  const refFlat = flat[reference];

  for (const key of [...allKeys].sort()) {
    for (const name of names) {
      const map = flat[name];
      if (!map.has(key)) {
        problems.push({
          severity: "error",
          kind: "missing-key",
          locale: name,
          key,
          detail: `key present in other locale(s) but missing from "${name}"`,
        });
        continue;
      }
      const value = map.get(key);
      // Shape mismatch: object here, string elsewhere.
      const isObjectHere = value === OBJECT_NODE;
      const isObjectInRef = refFlat?.get(key) === OBJECT_NODE;
      if (refFlat?.has(key) && isObjectHere !== isObjectInRef) {
        problems.push({
          severity: "error",
          kind: "shape-mismatch",
          locale: name,
          key,
          detail: `is ${isObjectHere ? "an object" : "a string"} here but ${isObjectInRef ? "an object" : "a string"} in "${reference}"`,
        });
        continue;
      }
      if (isObjectHere) continue;
      const str = value as string;
      if (str.trim() === "") {
        problems.push({
          severity: "error",
          kind: "empty-value",
          locale: name,
          key,
          detail: "value is empty",
        });
        continue;
      }
      for (const ch of messageSyntaxHazards(str)) {
        problems.push({
          severity: "error",
          kind: "unescaped-syntax",
          locale: name,
          key,
          detail: `contains a bare "${ch}" — vue-i18n reads it as message syntax and throws while rendering; write it as {'${ch}'}`,
        });
      }
      // Placeholder parity against the reference locale.
      if (refFlat?.has(key) && refFlat.get(key) !== OBJECT_NODE) {
        const refPh = placeholders(refFlat.get(key));
        const myPh = placeholders(str);
        if (refPh.join(",") !== myPh.join(",")) {
          problems.push({
            severity: "error",
            kind: "placeholder-mismatch",
            locale: name,
            key,
            detail: `placeholders [${myPh.join(", ")}] != [${refPh.join(", ")}] in "${reference}"`,
          });
        }
      }
      // Soft untranslated heuristic for non-reference locales.
      if (
        opts.checkUntranslated &&
        name !== reference &&
        refFlat?.has(key) &&
        refFlat.get(key) !== OBJECT_NODE &&
        str === refFlat.get(key) &&
        looksUntranslated(key, str)
      ) {
        problems.push({
          severity: "warning",
          kind: "untranslated",
          locale: name,
          key,
          detail: `value identical to "${reference}" — may be untranslated: ${JSON.stringify(str)}`,
        });
      }
    }
  }
  return problems;
}
