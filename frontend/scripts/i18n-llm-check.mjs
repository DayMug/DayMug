#!/usr/bin/env node
// Semantic i18n guard: scans the *staged* frontend diff for user-facing display
// strings that were hardcoded instead of routed through vue-i18n's t(). The
// deterministic check (check-i18n.mjs) only catches key/placeholder drift; it
// cannot see a brand-new English literal sitting in a .vue template. That
// judgement call is delegated to a Claude sub-agent.
//
// Cost control: we run the cheap `sonnet` model and feed it ONLY the added
// lines of the staged diff (not whole files), so a typical commit costs a few
// hundred tokens. The expensive part of this feature (writing it) used opus;
// the recurring per-commit check uses sonnet.
//
// Opt-in: the scan sends the staged diff to Anthropic and bills whoever is
// logged into the local `claude` CLI, so it only runs with DAYMUG_I18N_LLM=1.
// Skips silently if the `claude` CLI is not installed.

import { execFileSync } from "node:child_process";

if (process.env.DAYMUG_I18N_LLM !== "1") process.exit(0);

// Resolve the repo root and run every git command there, so repo-relative
// pathspecs work regardless of where the hook invokes us from (the pre-commit
// hook cd's into frontend/ first).
const repoRoot = execFileSync("git", ["rev-parse", "--show-toplevel"], {
  encoding: "utf8",
}).trim();

function git(args) {
  return execFileSync("git", args, { encoding: "utf8", cwd: repoRoot }).trim();
}

function has(cmd) {
  try {
    execFileSync("sh", ["-c", `command -v ${cmd}`], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

if (!has("claude")) {
  console.warn("i18n-llm-check: `claude` CLI not found, skipping semantic scan.");
  process.exit(0);
}

// Staged source files worth scanning: components/pages/composables, excluding
// the locale files themselves, tests, and generated/declaration files.
const staged = git(["diff", "--cached", "--name-only", "--diff-filter=ACM"])
  .split("\n")
  .filter(Boolean)
  .filter((f) => /^frontend\/src\/.*\.(vue|ts)$/.test(f))
  .filter((f) => !/i18n\/locales\//.test(f))
  .filter((f) => !/\.(test|spec)\.ts$/.test(f))
  .filter((f) => !f.endsWith(".d.ts"));

if (staged.length === 0) process.exit(0);

// Only the added lines, with file headers — keeps the payload small.
let diff = git(["diff", "--cached", "--unified=0", "--", ...staged]);
const MAX = 60_000;
if (diff.length > MAX) diff = diff.slice(0, MAX) + "\n…(diff truncated)…";

const PROMPT = `You are an i18n linter for a Vue 3 + vue-i18n codebase. The user-facing UI must be fully localized: every string the user can SEE must come from the t() function, never a hardcoded literal.

Below is a staged git diff (added lines only, prefixed with '+'). Examine ONLY added '+' lines. Report any added user-facing display text that is hardcoded instead of using t('...').

User-facing display text includes: button/menu labels, headings, titles, tooltip/placeholder/aria-label text shown to users, toast/error/confirmation messages rendered in the UI, and option labels.

DO NOT report (these are fine as literals): code comments, console.* logs, thrown Error messages used only for developers/logging, CSS classes, element ids, data-* attributes, route paths, import/module paths, object keys/enum values, type or constant identifiers, test fixtures, URLs, icon names, and strings already passed to t() or $t().

Respond with ONLY a JSON object, no prose, no markdown fences:
{"issues":[{"file":"path","line":"the offending added line trimmed","reason":"why it is user-facing","suggestion":"a t('some.key') call"}]}
If everything is fine, respond exactly: {"issues":[]}`;

let raw;
try {
  raw = execFileSync("claude", ["-p", PROMPT, "--model", "sonnet"], {
    input: diff,
    encoding: "utf8",
    timeout: 120_000,
    maxBuffer: 8 * 1024 * 1024,
  });
} catch (err) {
  // Never block a commit on a flaky/offline model call — warn and pass.
  console.warn(`i18n-llm-check: sub-agent call failed (${err.message?.split("\n")[0]}), skipping.`);
  process.exit(0);
}

// The model is told to emit raw JSON; be defensive and grab the first object.
const match = raw.match(/\{[\s\S]*\}/);
if (!match) {
  console.warn("i18n-llm-check: could not parse sub-agent output, skipping.");
  process.exit(0);
}

let issues = [];
try {
  issues = JSON.parse(match[0]).issues ?? [];
} catch {
  console.warn("i18n-llm-check: invalid JSON from sub-agent, skipping.");
  process.exit(0);
}

if (issues.length === 0) {
  console.log("✅ i18n: no hardcoded user-facing strings in staged changes.");
  process.exit(0);
}

console.error(`\n❌ i18n: ${issues.length} possibly-untranslated string(s) in staged changes:\n`);
for (const i of issues) {
  console.error(`  ${i.file}`);
  console.error(`    ${i.line}`);
  if (i.reason) console.error(`    why: ${i.reason}`);
  if (i.suggestion) console.error(`    use: ${i.suggestion}`);
  console.error("");
}
console.error(
  "Wrap these in t('...') and add the key to every locale, or bypass by unsetting DAYMUG_I18N_LLM / git commit --no-verify.",
);
process.exit(1);
