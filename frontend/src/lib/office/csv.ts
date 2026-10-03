/**
 * CSV/TSV bytes → a two-dimensional grid.
 *
 * The spreadsheet editor only speaks xlsx, so a `.csv` has to be decoded here
 * and converted before it can be opened (`csvbridge.ts`). Encoding and
 * delimiter detection are the part the editor does nothing about.
 *
 * Three things happen in order, and none of them may make the whole file
 * unreadable:
 *
 *   1. **Decode** — BOM first; without one, try strict UTF-8 and fall back to
 *      GBK (a large share of spreadsheets exported in China are GBK).
 *   2. **Sniff the delimiter** — pick from `, ; \t |` by *consistency* across
 *      lines rather than raw frequency: prose contains plenty of commas but at
 *      erratic counts per line, while a delimiter appears exactly
 *      columns-minus-one times on every line.
 *   3. **Parse** — RFC 4180 quoting rules, plus tolerance for damaged files
 *      (an unterminated quote swallows the rest as content instead of
 *      throwing). Being able to look at a file that is slightly broken is
 *      most of the point.
 *
 * Hand-rolled rather than papaparse: the job here is one in-memory string to
 * `string[][]`, and papaparse earns its keep on streaming, workers and type
 * inference — none of which apply. A single scan is faster (no intermediate
 * objects) and avoids a runtime dependency. The cost is that the quote
 * boundaries have to be exactly right, so `csv.test.ts` covers each RFC 4180
 * rule individually.
 */

/** The decoded text plus which encoding produced it — the caller reports it. */
export interface DecodedText {
  text: string;
  encoding: "utf-8" | "gbk" | "utf-16le" | "utf-16be";
}

/** Candidate delimiters. Order breaks ties (comma is by far the commonest). */
const DELIMITERS = [",", "\t", ";", "|"];

/** Lines to read when sniffing. Twenty is plenty; scanning 100k is waste. */
const SNIFF_LINES = 20;

function bytesOf(input: ArrayBuffer | Uint8Array): Uint8Array {
  return input instanceof Uint8Array ? input : new Uint8Array(input);
}

/**
 * Decode bytes to text.
 *
 * With no BOM the rule is "if strict UTF-8 decodes it, it is UTF-8": a GBK
 * double-byte character is very unlikely to also form a valid UTF-8 sequence,
 * which makes this reliable for Chinese text — and for pure ASCII both
 * encodings give the same result, so guessing wrong costs nothing.
 */
export function decodeTableBytes(input: ArrayBuffer | Uint8Array): DecodedText {
  const bytes = bytesOf(input);

  if (bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) {
    return { text: decodeWith("utf-8", bytes.subarray(3)), encoding: "utf-8" };
  }
  if (bytes.length >= 2 && bytes[0] === 0xff && bytes[1] === 0xfe) {
    return { text: decodeWith("utf-16le", bytes.subarray(2)), encoding: "utf-16le" };
  }
  if (bytes.length >= 2 && bytes[0] === 0xfe && bytes[1] === 0xff) {
    return { text: decodeWith("utf-16be", bytes.subarray(2)), encoding: "utf-16be" };
  }

  try {
    return { text: new TextDecoder("utf-8", { fatal: true }).decode(bytes), encoding: "utf-8" };
  } catch {
    // Not valid UTF-8. If the GBK decoder is missing (very old runtime), a
    // lenient UTF-8 pass full of replacement characters still beats refusing
    // to open the file at all.
    try {
      return { text: new TextDecoder("gbk").decode(bytes), encoding: "gbk" };
    } catch {
      return { text: decodeWith("utf-8", bytes), encoding: "utf-8" };
    }
  }
}

function decodeWith(label: string, bytes: Uint8Array): string {
  try {
    return new TextDecoder(label).decode(bytes);
  } catch {
    return new TextDecoder("utf-8").decode(bytes);
  }
}

/**
 * Guess the delimiter.
 *
 * The test is "the same non-zero count on every line": a delimiter occurs
 * exactly columns-minus-one times per row, while punctuation in the content
 * varies. Among consistent candidates the highest count wins; if none is
 * consistent, fall back to a single column (a one-column CSV is still valid).
 */
export function sniffDelimiter(text: string, filename = ""): string {
  if (/\.tsv$/i.test(filename)) return "\t";

  const lines: string[] = [];
  for (const line of text.split(/\r\n|\n|\r/)) {
    if (line !== "") lines.push(line);
    if (lines.length >= SNIFF_LINES) break;
  }
  if (lines.length === 0) return ",";

  let best = ",";
  let bestCount = 0;
  for (const delimiter of DELIMITERS) {
    const counts = lines.map((line) => countOutsideQuotes(line, delimiter));
    const first = counts[0] ?? 0;
    if (first === 0) continue;
    if (!counts.every((c) => c === first)) continue;
    if (first > bestCount) {
      best = delimiter;
      bestCount = first;
    }
  }
  return best;
}

/** Count delimiters outside quotes — a comma inside quotes is content. */
function countOutsideQuotes(line: string, delimiter: string): number {
  let count = 0;
  let quoted = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (ch === '"') {
      quoted = !quoted;
      continue;
    }
    if (!quoted && ch === delimiter) count++;
  }
  return count;
}

/**
 * RFC 4180 scanner: text → grid.
 *
 * Fields may be wrapped in `"`, `""` inside them is one literal quote, and a
 * wrapped field may contain delimiters and newlines. `\r\n`, `\n` and `\r` all
 * end a row.
 *
 * Damaged input never throws: an unterminated quote takes the rest of the file
 * as that field's content, because someone opening a broken file is usually
 * trying to find out how it is broken.
 *
 * A row consisting of one empty field (i.e. a blank line) is skipped, so the
 * trailing newline and any blank lines an exporter left behind do not become
 * rows.
 */
export function parseDelimited(text: string, delimiter: string): string[][] {
  const rows: string[][] = [];
  const n = text.length;
  const dc = delimiter.charCodeAt(0);
  let row: string[] = [];
  let i = 0;

  while (i < n) {
    const field = scanField(text, i, dc);
    i = field.next;
    row.push(field.value);

    if (i >= n) break;
    const c = text.charCodeAt(i);
    if (c === dc) {
      i++;
      // A trailing delimiter at end-of-file still means one more (empty)
      // column; without this the last column would silently disappear.
      if (i >= n) {
        row.push("");
        break;
      }
      continue;
    }
    i += c === 13 && text.charCodeAt(i + 1) === 10 ? 2 : 1;
    pushRow(rows, row);
    row = [];
  }

  pushRow(rows, row);
  return rows;
}

/** One field scan: the value plus where scanning stopped. */
interface FieldScan {
  value: string;
  next: number;
}

function scanField(text: string, i: number, dc: number): FieldScan {
  if (text.charCodeAt(i) === 34 /* " */) return scanQuotedField(text, i + 1, dc);
  const end = scanBareEnd(text, i, dc);
  return { value: text.slice(i, end), next: end };
}

/** End of unquoted content: a delimiter, a newline, or end of file. */
function scanBareEnd(text: string, i: number, dc: number): number {
  const n = text.length;
  while (i < n) {
    const c = text.charCodeAt(i);
    if (c === dc || c === 10 || c === 13) break;
    i++;
  }
  return i;
}

/** Scan a quoted field; `i` is the first character after the opening quote. */
function scanQuotedField(text: string, i: number, dc: number): FieldScan {
  const n = text.length;
  let chunkStart = i;
  let acc = "";
  let value: string;
  for (;;) {
    const q = text.indexOf('"', i);
    if (q < 0) {
      // Unterminated quote (truncated or malformed): take the rest as content.
      return { value: acc + text.slice(chunkStart), next: n };
    }
    if (text.charCodeAt(q + 1) === 34) {
      // `""` is one literal quote; keep it with the preceding chunk.
      acc += text.slice(chunkStart, q + 1);
      i = q + 2;
      chunkStart = i;
      continue;
    }
    value = acc + text.slice(chunkStart, q);
    i = q + 1;
    break;
  }
  // Content after the closing quote (`"a"b,c`) is treated as a continuation of
  // the same field rather than the end of a row — otherwise one stray quote
  // would throw off the row count for the whole file.
  if (i < n) {
    const end = scanBareEnd(text, i, dc);
    value += text.slice(i, end);
    i = end;
  }
  return { value, next: i };
}

function pushRow(rows: string[][], row: string[]): void {
  if (row.length === 0) return;
  // A single empty field is a blank line.
  if (row.length === 1 && row[0] === "") return;
  rows.push(row);
}
