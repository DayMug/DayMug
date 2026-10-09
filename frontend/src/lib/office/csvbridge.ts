/**
 * csv/tsv ↔ xlsx, in both directions.
 *
 * The spreadsheet editor only handles xlsx: `.csv` / `.tsv` / `.ods` never
 * appear anywhere in its embed bundle and saving is hardwired to
 * `exportXlsx`. Giving CSV files the full formula-and-formatting editor
 * therefore means converting on both ends, here in the host.
 *
 * ## The round trip is lossy, and that has to be said out loud
 *
 * CSV cannot hold a second worksheet, cannot hold formatting, and turns
 * formulas into their computed values. That is the file format, not the
 * editor — Excel behaves the same way, warning once and then writing only the
 * active sheet. The conversion lives here; **the warning is raised by the
 * caller**, which is why `xlsxToCsv` reports `sheetCount` for it to word.
 */
import { decodeTableBytes, parseDelimited, sniffDelimiter } from "@/lib/office/csv";

/** What reading a CSV detected. Saving reuses the delimiter and BOM. */
export interface CsvDialect {
  encoding: "utf-8" | "gbk" | "utf-16le" | "utf-16be";
  delimiter: string;
  /** The original had a BOM. Keep it as it was, either way. */
  bom: boolean;
}

export interface CsvToXlsxResult {
  bytes: ArrayBuffer;
  dialect: CsvDialect;
}

export interface XlsxToCsvResult {
  bytes: ArrayBuffer;
  /** Worksheets in the workbook. >1 means this save drops the rest. */
  sheetCount: number;
}

/** Must be the browser bundle; the package root pulls in fs/stream. */
async function excel(): Promise<typeof import("exceljs")> {
  const mod = await import("exceljs/dist/exceljs.min.js");
  return (mod as { default: typeof import("exceljs") }).default;
}

function hasBom(input: ArrayBuffer): boolean {
  const b = new Uint8Array(input);
  if (b.length >= 3 && b[0] === 0xef && b[1] === 0xbb && b[2] === 0xbf) return true;
  if (b.length >= 2 && b[0] === 0xff && b[1] === 0xfe) return true;
  return b.length >= 2 && b[0] === 0xfe && b[1] === 0xff;
}

/**
 * Number or text?
 *
 * Storing everything as text would break formulas and SUM, which defeats the
 * conversion; but `007` (an id) and `+8613…` (a phone number) must stay text.
 * The rule: looks numeric and has no leading zero.
 *
 * Dates are deliberately **not** detected. Turning `2024-01-01` into a Date
 * drags in time zones, and shifting every date by a day is a far worse error
 * than a date rendered as text. A user who wants a date format can set one in
 * the grid.
 */
function cellValue(raw: string): string | number {
  if (raw === "") return "";
  if (!/^[+-]?(?:\d+\.?\d*|\.\d+)(?:e[+-]?\d+)?$/i.test(raw)) return raw;
  if (/^[+-]?0\d/.test(raw)) return raw;
  const value = Number(raw);
  return Number.isFinite(value) ? value : raw;
}

/** csv/tsv bytes → single-sheet xlsx bytes. */
export async function csvToXlsx(input: ArrayBuffer, filename = ""): Promise<CsvToXlsxResult> {
  const { text, encoding } = decodeTableBytes(input);
  const delimiter = sniffDelimiter(text, filename);
  const grid = parseDelimited(text, delimiter);

  const ExcelJS = await excel();
  const workbook = new ExcelJS.Workbook();
  const sheet = workbook.addWorksheet("Sheet1");
  for (const row of grid) sheet.addRow(row.map(cellValue));

  const output = await workbook.xlsx.writeBuffer();
  const view = new Uint8Array(output);
  return {
    bytes: view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength) as ArrayBuffer,
    dialect: { encoding, delimiter, bom: hasBom(input) },
  };
}

/**
 * A cell as the string that goes into the CSV.
 *
 * Formulas contribute their **computed result**, not the formula: writing
 * `=SUM(A1:A3)` into a CSV only hands the next reader a string (and invites
 * Excel's formula-injection behaviour). Rich text is flattened, a hyperlink
 * contributes its display text, and an errored cell writes its error code —
 * all matching what is on screen.
 */
function cellText(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (value instanceof Date) return formatDate(value);
  if (typeof value === "object") {
    const cell = value as Record<string, unknown>;
    if ("result" in cell) return cellText(cell.result);
    if ("richText" in cell && Array.isArray(cell.richText)) {
      return cell.richText.map((part) => String((part as { text?: string }).text ?? "")).join("");
    }
    if ("text" in cell) return cellText(cell.text);
    if ("error" in cell) return String(cell.error);
    if ("hyperlink" in cell) return String(cell.hyperlink);
    return "";
  }
  if (typeof value === "boolean") return value ? "TRUE" : "FALSE";
  return String(value);
}

function formatDate(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  const date = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  if (d.getHours() === 0 && d.getMinutes() === 0 && d.getSeconds() === 0) return date;
  return `${date} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** RFC 4180: quote a field containing the delimiter, a quote or a newline. */
function escapeField(text: string, delimiter: string): string {
  if (!text.includes(delimiter) && !/["\r\n]/.test(text)) return text;
  return `"${text.replaceAll('"', '""')}"`;
}

/**
 * xlsx bytes → csv bytes. **Only the first worksheet**; the rest are dropped
 * because the format cannot hold them (see the file header).
 *
 * The output is always UTF-8: the browser's `TextEncoder` only produces UTF-8
 * and there is no built-in GBK encoder, so a GBK file changes encoding on the
 * way back. The caller states that plainly in its confirmation. The BOM
 * follows the original — present if it was present (Excel relies on it for
 * Chinese), absent otherwise.
 */
export async function xlsxToCsv(
  input: ArrayBuffer,
  dialect: Pick<CsvDialect, "delimiter" | "bom">,
): Promise<XlsxToCsvResult> {
  const ExcelJS = await excel();
  const workbook = new ExcelJS.Workbook();
  await workbook.xlsx.load(input);

  const sheet = workbook.worksheets[0];
  const lines: string[] = [];
  if (sheet) {
    const width = sheet.columnCount;
    sheet.eachRow({ includeEmpty: true }, (row) => {
      const cells: string[] = [];
      for (let c = 1; c <= width; c++) {
        cells.push(escapeField(cellText(row.getCell(c).value), dialect.delimiter));
      }
      // Trim trailing empty columns: a run of commas only makes the file
      // dirtier, and the width is set by the cells that have content.
      while (cells.length > 0 && cells[cells.length - 1] === "") cells.pop();
      lines.push(cells.join(dialect.delimiter));
    });
  }

  const text = `${dialect.bom ? "﻿" : ""}${lines.join("\r\n")}${lines.length > 0 ? "\r\n" : ""}`;
  const bytes = new TextEncoder().encode(text);
  return {
    bytes: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer,
    sheetCount: workbook.worksheets.length,
  };
}
