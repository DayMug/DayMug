#!/usr/bin/env node
// Deterministic frontend/backend API contract checker. Zero LLM tokens, no new
// dependencies: it reads the golden snapshot the Go test writes
// (backend/internal/store/contract.golden.json) and compares it against the
// hand-written TypeScript interfaces using the TypeScript compiler API.
//
// Why this and not codegen: ~95% of handler responses are anonymous gin.H
// maps, so there is almost nothing to generate from, and generated types would
// flatten the literal unions (`platform: "slack" | "feishu"`), Pick<>
// derivations, and deliberately-widened optionality the frontend hand-writes.
// This gets the anti-drift benefit without any of that.
//
// It compares FIELD NAMES and OPTIONALITY only, never types. Comparing types
// would fight the narrowing the frontend does on purpose.
//
// Optionality is checked asymmetrically, because the two directions differ in
// how dangerous they are:
//
//   Go required + TS optional  -> ALLOWED. A widening. Several interfaces do
//                                 this so pre-existing test fixtures without
//                                 the field stay valid. Reading it just means
//                                 handling a `| undefined` that never happens.
//   Go optional + TS required  -> ERROR. The backend omits the field (it is
//                                 `omitempty`) but the frontend's types promise
//                                 it is always there, so consumers dereference
//                                 something that can legitimately be missing.
//
// Run via `pnpm api-contract:check`, from the pre-commit hook, or in CI.
// Exit codes: 0 = clean, 1 = drift (or a stale exemption).

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import ts from "typescript";

const here = dirname(fileURLToPath(import.meta.url));
const frontendRoot = resolve(here, "..");
const goldenPath = resolve(
  frontendRoot,
  "..",
  "backend",
  "internal",
  "store",
  "contract.golden.json",
);

// Fields the frontend knowingly does not model. Each entry is a decision, not
// an oversight — that is the whole point of listing them here rather than
// letting the check quietly pass. Keyed "<GoType>.<json_field>".
const EXEMPTIONS = {
  // The backend persists the raw context_usage blob on the conversation row so
  // a cold browser can render the token bar before the next reply arrives. The
  // SPA reads that value off the live `context_usage` WebSocket frame instead
  // and never touches the REST column, so modelling it would be dead surface.
  "store.Conversation.last_context_usage":
    "SPA reads context usage from the live WS frame, not the REST row",
};

const argv = new Set(process.argv.slice(2));
const quiet = argv.has("--quiet");

// ---------------------------------------------------------------------------
// Golden snapshot (produced by: go test ./internal/store -run
// TestAPIContractSnapshot -update-contract)
// ---------------------------------------------------------------------------

let golden;
try {
  golden = JSON.parse(readFileSync(goldenPath, "utf8"));
} catch (err) {
  console.error(`❌ cannot read the Go contract snapshot at ${goldenPath}: ${err.message}`);
  console.error(
    "Generate it with: cd backend && go test ./internal/store -run TestAPIContractSnapshot -update-contract",
  );
  process.exit(1);
}

// ---------------------------------------------------------------------------
// TypeScript program
// ---------------------------------------------------------------------------

const configPath = join(frontendRoot, "tsconfig.app.json");
const configFile = ts.readConfigFile(configPath, ts.sys.readFile);
if (configFile.error) {
  console.error(
    `❌ cannot read ${configPath}: ${ts.flattenDiagnosticMessageText(configFile.error.messageText, "\n")}`,
  );
  process.exit(1);
}
const parsed = ts.parseJsonConfigFileContent(configFile.config, ts.sys, frontendRoot);

// Only the files the contract actually names need to be roots; the compiler
// pulls in whatever they import. Keeps this well under a second.
const rootFiles = [
  ...new Set(Object.values(golden.types).map((t) => resolve(frontendRoot, t.ts_file))),
];
const program = ts.createProgram(rootFiles, { ...parsed.options, noEmit: true });
const checker = program.getTypeChecker();

/** Find an exported interface / type alias by name in a specific source file. */
function findExportedType(file, name) {
  const source = program.getSourceFile(resolve(frontendRoot, file));
  if (!source) return { error: `source file not found: ${file}` };
  const moduleSymbol = checker.getSymbolAtLocation(source);
  if (!moduleSymbol) return { error: `${file} is not a module` };
  const exported = checker.getExportsOfModule(moduleSymbol).find((s) => s.getName() === name);
  if (!exported) return { error: `${file} does not export \`${name}\`` };
  return { type: checker.getDeclaredTypeOfSymbol(exported) };
}

/**
 * Collect the member names of a type, with optionality.
 *
 * A discriminated union (WSMessage: 20 frame interfaces + a forward-compat
 * fallback) is flattened to the UNION of its members' fields, not the
 * intersection. That is the right comparison here: the Go side is one flat
 * envelope struct where every field but `type` is omitempty, and the TS union
 * is a refinement of exactly that envelope. The union of its members is
 * therefore precisely the set of keys the backend can put on the wire.
 * Comparing per-member would be meaningless, since each frame deliberately
 * carries only its own subset.
 *
 * A field is treated as required only when it is present AND non-optional in
 * every union member — anything else can be absent on some frame.
 */
function collectMembers(type) {
  const members = type.isUnion() ? type.types : [type];
  const seenIn = new Map(); // name -> { count, optionalSomewhere }

  for (const member of members) {
    for (const prop of checker.getPropertiesOfType(member)) {
      const name = prop.getName();
      const entry = seenIn.get(name) ?? { count: 0, optionalSomewhere: false };
      entry.count += 1;
      if ((prop.flags & ts.SymbolFlags.Optional) !== 0) entry.optionalSomewhere = true;
      seenIn.set(name, entry);
    }
  }

  const required = new Set();
  const optional = new Set();
  for (const [name, entry] of seenIn) {
    if (entry.count === members.length && !entry.optionalSomewhere) required.add(name);
    else optional.add(name);
  }
  return { required, optional, all: new Set(seenIn.keys()) };
}

// ---------------------------------------------------------------------------
// Compare
// ---------------------------------------------------------------------------

const problems = [];
const usedExemptions = new Set();

for (const [goName, spec] of Object.entries(golden.types)) {
  const found = findExportedType(spec.ts_file, spec.ts_interface);
  if (found.error) {
    problems.push({ type: goName, detail: found.error });
    continue;
  }

  const tsm = collectMembers(found.type);
  const goRequired = new Set(spec.required_fields ?? []);
  const goOptional = new Set(spec.optional_fields ?? []);
  const goAll = new Set([...goRequired, ...goOptional]);

  // 1. Backend sends it, frontend does not model it.
  for (const field of [...goAll].sort()) {
    if (tsm.all.has(field)) continue;
    const key = `${goName}.${field}`;
    if (key in EXEMPTIONS) {
      usedExemptions.add(key);
      continue;
    }
    problems.push({
      type: goName,
      detail: `backend sends \`${field}\` but ${spec.ts_interface} does not declare it`,
      hint: `add \`${field}\` to ${spec.ts_interface} in ${spec.ts_file}, or add an entry to EXEMPTIONS in this script`,
    });
  }

  // 2. Frontend models a field the backend never sends.
  for (const field of [...tsm.all].sort()) {
    if (goAll.has(field)) continue;
    problems.push({
      type: goName,
      detail: `${spec.ts_interface} declares \`${field}\` but the backend never sends it`,
      hint: "the Go field was probably renamed or removed; drop it from the interface",
    });
  }

  // 3. Backend may omit it, but the frontend promises it is always present.
  for (const field of [...goOptional].sort()) {
    if (tsm.required.has(field)) {
      problems.push({
        type: goName,
        detail: `backend omits \`${field}\` when empty, but ${spec.ts_interface} declares it required`,
        hint: `mark it \`${field}?:\` in ${spec.ts_file}`,
      });
    }
  }
}

// A stale exemption is drift too: it means someone fixed the field but left
// the waiver behind, and the waiver would hide the next real regression.
for (const key of Object.keys(EXEMPTIONS)) {
  if (!usedExemptions.has(key)) {
    problems.push({
      type: key.split(".").slice(0, 2).join("."),
      detail: `exemption \`${key}\` no longer applies`,
      hint: "remove it from EXEMPTIONS in this script",
    });
  }
}

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------

if (problems.length) {
  console.error(`\n❌ ${problems.length} API contract problem(s):`);
  for (const p of problems) {
    console.error(`  [${p.type}] ${p.detail}`);
    if (p.hint) console.error(`      → ${p.hint}`);
  }
  console.error(
    "\nThe Go snapshot is backend/internal/store/contract.golden.json; regenerate it with\n" +
      "  cd backend && go test ./internal/store -run TestAPIContractSnapshot -update-contract",
  );
  process.exit(1);
}

if (!quiet) {
  const count = Object.keys(golden.types).length;
  const waived = Object.keys(EXEMPTIONS).length;
  console.log(
    `✅ api-contract: ${count} type(s) in sync between Go and TypeScript` +
      (waived ? ` (${waived} exempted field(s))` : ""),
  );
}
process.exit(0);
