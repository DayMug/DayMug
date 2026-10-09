#!/usr/bin/env node
// Deterministic i18n locale checker. Zero LLM tokens: it just imports every
// locale under src/i18n/locales and asserts they are in full key + placeholder
// parity. Run via `pnpm i18n:check` or from the pre-commit hook.
//
// Exit codes: 0 = clean, 1 = errors (or warnings when --strict).
//
// Flags:
//   --strict   treat warnings (likely-untranslated values) as failures
//   --quiet    suppress warnings entirely

import { readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { createJiti } from "jiti";

const here = dirname(fileURLToPath(import.meta.url));
const localesDir = join(here, "..", "src", "i18n", "locales");
const jiti = createJiti(import.meta.url);
const { checkLocales } = await jiti.import("../src/i18n/parity.ts");

const argv = new Set(process.argv.slice(2));
const strict = argv.has("--strict");
const quiet = argv.has("--quiet");

async function loadLocales() {
  const entries = await readdir(localesDir);
  const locales = {};
  for (const file of entries.filter((f) => f.endsWith(".ts") && !f.endsWith(".test.ts"))) {
    const name = file.replace(/\.ts$/, "");
    const mod = await jiti.import(join(localesDir, file));
    locales[name] = mod.default;
  }
  return locales;
}

const locales = await loadLocales();
const names = Object.keys(locales).sort();
if (names.length < 2) {
  console.log(
    `i18n: only ${names.length} locale found (${names.join(", ") || "none"}), nothing to compare.`,
  );
  process.exit(0);
}

const problems = checkLocales(locales, { checkUntranslated: true });
const errors = problems.filter((p) => p.severity === "error");
const warnings = problems.filter((p) => p.severity === "warning");

const fmt = (p) => `  [${p.kind}] ${p.locale}:${p.key} — ${p.detail}`;

if (!quiet && warnings.length) {
  console.warn(`\n⚠️  ${warnings.length} i18n warning(s):`);
  for (const w of warnings) console.warn(fmt(w));
}

if (errors.length) {
  console.error(`\n❌ ${errors.length} i18n error(s) across locales [${names.join(", ")}]:`);
  for (const e of errors) console.error(fmt(e));
  console.error(
    "\nFix the locale files so every key exists in all locales with matching {placeholders}.",
  );
  process.exit(1);
}

if (strict && warnings.length) {
  console.error(`\n❌ --strict: ${warnings.length} warning(s) treated as errors.`);
  process.exit(1);
}

console.log(`✅ i18n: ${names.length} locales in parity (${names.join(", ")}), no missing keys.`);
process.exit(0);
