/**
 * Which files the embedded editors handle, and on what terms.
 *
 * One classifier shared by the preview surface, the workbench header and the
 * workspace overlay — they used to carry three copies of the same regex and
 * drifted apart.
 */

/** The two editors the host knows about. Same spelling as the protocol's `app`. */
export type OfficeApp = "sheet" | "docs";

export function extOf(path: string): string {
  const base = path.slice(path.lastIndexOf("/") + 1);
  const dot = base.lastIndexOf(".");
  return dot < 0 ? "" : base.slice(dot + 1).toLowerCase();
}

/** Opened by the spreadsheet editor directly (OOXML, or via the csv bridge). */
const SHEET_EXTS = ["xlsx", "xlsm", "csv", "tsv"];

/**
 * Viewable but not editable: converted to xlsx in the browser before the
 * editor sees them.
 *
 * The CasualOffice parser reads OOXML zips only — no BIFF `.xls`, no
 * OpenDocument. Those are converted by SheetJS on the way in (see
 * `legacyToXlsx`) and opened read-only, because there is no writer for either
 * format. Saving them as xlsx bytes under the original name, which is what
 * this app used to do, produces a file whose extension lies about its
 * contents.
 */
const LEGACY_SHEET_EXTS = ["xls", "ods"];

const DOC_EXTS = ["docx"];

/** The editor that owns this path, or null if neither does. */
export function officeAppFor(path: string): OfficeApp | null {
  const ext = extOf(path);
  if (SHEET_EXTS.includes(ext) || LEGACY_SHEET_EXTS.includes(ext)) return "sheet";
  if (DOC_EXTS.includes(ext)) return "docs";
  return null;
}

export function isLegacySheet(path: string): boolean {
  return LEGACY_SHEET_EXTS.includes(extOf(path));
}

/**
 * Files that open view-only.
 *
 * `.xlsm` because the export path emits no vbaProject — saving would quietly
 * strip the macros, and handing back a file the user believes is still the
 * original is worse than refusing the edit. `.xls` / `.ods` because nothing
 * here can write those formats back.
 */
export function isReadOnly(path: string): boolean {
  return extOf(path) === "xlsm" || isLegacySheet(path);
}

/**
 * `.xls` / `.ods` → xlsx bytes, so the editor can render them.
 *
 * SheetJS is imported lazily and only on this path, so the ~250KB it costs is
 * never paid by anyone opening a modern spreadsheet. Formatting is not
 * recovered — the community build cannot read cell styles — but the values,
 * formulas and sheet structure come through, which is what viewing one of
 * these files is for.
 */
export async function legacyToXlsx(bytes: ArrayBuffer): Promise<ArrayBuffer> {
  const XLSX = await import("xlsx");
  const workbook = XLSX.read(new Uint8Array(bytes), { type: "array", cellFormula: true });
  const out = XLSX.write(workbook, { type: "array", bookType: "xlsx" }) as ArrayBuffer;
  return out;
}
