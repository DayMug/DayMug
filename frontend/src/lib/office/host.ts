/**
 * The **host side** of the two CasualOffice editors: it wires the editor
 * running inside the iframe up to DayMug's file API.
 *
 * The editor stores nothing itself — it asks the host for bytes when it
 * starts and pushes bytes back when it saves, leaving the host to decide
 * where they go. So this file does three things:
 *
 *   1. **Read** — `casual.load.request` → the files read endpoint. csv/tsv are
 *      converted to xlsx first (see `csvbridge`).
 *   2. **Write** — bytes out of the editor → a multipart upload that
 *      overwrites the path in place, matching how every other binary write in
 *      the app behaves. Where those bytes come from differs per editor; see
 *      below.
 *   3. **Confirm a lossy save** — csv holds one sheet, no formatting, and only
 *      the computed value of a formula. Ask before writing, once per file, the
 *      way desktop Excel does.
 *
 * ## The two editors are not equally capable
 *
 * They speak the same protocol strings, but **each registers handlers for only
 * part of it**. Counted off against their respective `mountEmbedded`:
 *
 * | | sheets | docs |
 * |---|---|---|
 * | `set.theme` | yes | **no** (cannot follow dark mode) |
 * | `set.locale` / `set.readonly` | **no** (in the protocol, nobody listens) | no |
 * | sends `save.request` with bytes | **no** (see below) | yes, on its own autosave |
 *
 * So neither read-only nor UI language can be driven by a command: read-only
 * goes through the URL's `viewMode=preview` (see `embed.ts`) and the language
 * is baked into the bundle at build time (see `scripts/sync-casual-office.mjs`).
 *
 * ## The spreadsheet cannot save over the protocol at all
 *
 * `@casualoffice/sheets@0.20.0`'s embed defines `requestSave` on its transport
 * and then **never calls it** — verified by reading the shipped bundle, and
 * confirmed by driving the real editor: a `casual.command.save` produces only
 * a `save.notify` carrying a Univer snapshot, never the xlsx bytes a host
 * would need. Waiting for `onSaveRequest` from the spreadsheet is waiting
 * forever.
 *
 * What the bundle does offer is an escape hatch it publishes deliberately:
 * `mountEmbedded` assigns its editor API to `globalThis.__casualEmbedApi`
 * inside the iframe, and that object exposes `exportXlsx()`. The embed is
 * served from our own origin, so the host can reach in and call it. That is
 * what `save()` does. It is a private-ish surface, so a missing API is
 * reported as an error rather than silently doing nothing — an upgrade that
 * removes it must be loud.
 *
 * docs needs none of this: its `mountEmbedded` wires save straight to
 * `requestSave`, so its autosave delivers docx bytes to `onSaveRequest` and
 * the host just writes them.
 *
 * ## Why "dirty" is inferred from input events
 *
 * The v1 iframe protocol emits no change events, and the spreadsheet embed's
 * own `dirtyChange` turned out to be unreadable from outside (see
 * `watchInteraction`). So the host watches for input inside the same-origin
 * iframe instead, and uses it only to decide whether closing should ask —
 * a spreadsheet is never written without the user asking for it.
 *
 * ## Why there is no reload()
 *
 * Neither the transport nor the embed API offers "load again". The only way to
 * make the editor re-read a file is to remount the iframe (change its `:key`),
 * after which it issues a fresh `casual.load.request` on startup.
 */
import { EmbedHostTransport, type SaveResponseData } from "@casualoffice/sheets/embed";

import { csvToXlsx, xlsxToCsv, type CsvDialect } from "@/lib/office/csvbridge";
import { extOf, isLegacySheet, isReadOnly, legacyToXlsx, type OfficeApp } from "@/lib/office/files";

const XLSX_CONTENT_TYPE = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
const DOCX_CONTENT_TYPE = "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
const CSV_CONTENT_TYPE = "text/csv;charset=utf-8";

/** Extensions that go through the csv bridge. xlsx/xlsm/docx pass through. */
const DELIMITED_EXTS = ["csv", "tsv"];

/**
 * The slice of the spreadsheet embed's API that the host uses.
 *
 * `mountEmbedded` hangs this off the iframe window's `globalThis`; see the
 * file header for why saving has to go through it.
 */
interface CasualEmbedApi {
  exportXlsx(): Promise<Blob>;
}

/** Events inside the iframe that mean the user is working in the editor. */
const INTERACTION_EVENTS = ["keydown", "pointerdown", "paste"] as const;

/**
 * Copy an iframe-produced Blob into an ArrayBuffer belonging to *this* realm.
 *
 * The export comes from the embed's own window, so its Blob — and the
 * ArrayBuffer that Blob yields — are built from the iframe realm's
 * constructors. Libraries that branch on `x instanceof ArrayBuffer` then take
 * the wrong branch: JSZip, reached through exceljs on the csv path, rejects it
 * with "Can't read the data of 'the loaded zip file'". Copying the bytes costs
 * one pass over a few hundred KB and makes the value ordinary.
 */
async function sameRealmBytes(blob: Blob): Promise<ArrayBuffer> {
  const source = new Uint8Array(await blob.arrayBuffer());
  const bytes = new ArrayBuffer(source.byteLength);
  new Uint8Array(bytes).set(source);
  return bytes;
}

/** What a lossy save is about to discard, so the caller can say so. */
export interface LossyWarning {
  /** Sheets in the workbook; above 1, everything after the first is dropped. */
  sheetCount: number;
  /** The original was not UTF-8 — the browser can only write UTF-8 back. */
  reencodesTo: "utf-8" | null;
}

/** Byte-level file access, injected so tests need no network. */
export interface OfficeFileGateway {
  read(path: string): Promise<ArrayBuffer>;
  write(path: string, bytes: ArrayBuffer, contentType: string): Promise<void>;
}

export interface OfficeHostOptions {
  app: OfficeApp;
  path: string;
  files: OfficeFileGateway;
  /** The iframe's contentWindow. The caller guarantees it exists. */
  iframeWindow: Window;
  /** The iframe's origin — `window.location.origin` for a same-origin deploy. */
  origin: string;
  /** The window that receives messages. Only tests pass this. */
  hostWindow?: Pick<Window, "addEventListener" | "removeEventListener">;
  /** Ask before a lossy save. Returning false abandons it. */
  confirmLossy: (warning: LossyWarning) => Promise<boolean>;
  onReady: () => void;
  onSaved: () => void;
  onError: (error: unknown) => void;
}

export class OfficeHost {
  private readonly transport: EmbedHostTransport;
  private readonly opts: OfficeHostOptions;
  /** Encoding/delimiter/BOM detected on read; restored when writing back. */
  private dialect: CsvDialect | null = null;
  /** Warn once per file, then remember the answer. */
  private lossyConfirmed = false;
  private destroyed = false;
  /** The user has worked in the editor since the last save; see `watchInteraction`. */
  private touched = false;
  private detachInteraction: (() => void) | null = null;
  /** The handshake has been answered; see `onEditorReady`. */
  private greeted = false;

  constructor(opts: OfficeHostOptions) {
    this.opts = opts;
    this.transport = new EmbedHostTransport({
      app: opts.app,
      iframeWindow: opts.iframeWindow,
      embedOrigin: opts.origin,
      ...(opts.hostWindow ? { hostWindow: opts.hostWindow } : {}),
    });
    this.transport.on({
      onEditorReady: () => {
        // **Answer the handshake exactly once.** The embed replies to
        // `casual.hello` by sending `casual.ready` again, so greeting it back
        // every time turns the handshake into an endless postMessage
        // ping-pong — and re-runs everything below on every lap, which among
        // other things kept resetting the dirty flag to false.
        if (this.greeted) return;
        this.greeted = true;

        this.transport.sendHostHello({ capabilities: ["load", "save"] });
        // No set.locale / set.readonly here: **neither editor listens** (the
        // sheets mountEmbedded registers only theme / features / viewMode).
        // Read-only rides on the URL's viewMode=preview and the language is
        // baked into the bundle — see embed.ts.
        // A file the editor will not let anyone change can never be dirty.
        if (opts.app === "sheet" && !isReadOnly(opts.path)) this.watchInteraction();
        this.opts.onReady();
      },
      onLoadRequest: () => this.load(),
      onSaveRequest: (data) => this.write(data.bytes),
      onError: (data) => this.opts.onError(new Error(data.message ?? data.code)),
    });
  }

  /**
   * Write the editor's current content, reporting whether it reached disk.
   *
   * For a spreadsheet this pulls the bytes out of the in-iframe editor API
   * (see the file header — the protocol has no working path for it). For a
   * document there is nothing to do: it autosaves, and its bytes have already
   * arrived through `onSaveRequest`.
   */
  async save(): Promise<boolean> {
    if (this.opts.app !== "sheet") return true;
    if (this.destroyed) return false;

    const api = this.embedApi();
    if (!api) {
      this.opts.onError(new Error("The spreadsheet editor exposed no export API"));
      return false;
    }

    try {
      const blob = await api.exportXlsx();
      const result = await this.write(await sameRealmBytes(blob));
      if (result.ok) this.touched = false;
      return result.ok;
    } catch (error) {
      this.opts.onError(error);
      return false;
    }
  }

  /** Whether the editor may hold unsaved changes. Always false for docs, which autosaves. */
  isDirty(): boolean {
    return this.touched;
  }

  /**
   * Notice that the user has worked in the editor, so closing an unsaved
   * spreadsheet can ask first.
   *
   * This deliberately does **not** use the embed's own `dirtyChange` event.
   * Measured against the real editor, that signal cannot be read from outside:
   * applying the loaded workbook reports dirty just like an edit does, editing
   * one cell emits a long `true, false, true, false…` run that frequently ends
   * on `false` while the edit is unsaved, and calling `markDirty(false)` to
   * establish a baseline perturbs the very sequence being measured. Any rule
   * built on it was wrong in one direction or the other — and being wrong
   * towards "clean" loses the user's work.
   *
   * Input events in the iframe are a signal we own: the embed is same-origin,
   * so the host can listen directly. It over-reports (clicking a cell counts)
   * and that is the right way to be wrong — the cost is one confirmation
   * prompt, not a lost edit. It is used only to decide whether to *ask*;
   * nothing is ever written without the user saying so.
   */
  private watchInteraction(): void {
    const target = this.opts.iframeWindow;
    const mark = () => (this.touched = true);
    for (const event of INTERACTION_EVENTS) target.addEventListener(event, mark, true);
    this.detachInteraction = () => {
      for (const event of INTERACTION_EVENTS) target.removeEventListener(event, mark, true);
    };
  }

  /** The editor API the embed publishes inside its (same-origin) iframe. */
  private embedApi(): CasualEmbedApi | null {
    const api = (this.opts.iframeWindow as unknown as { __casualEmbedApi?: CasualEmbedApi })
      .__casualEmbedApi;
    return typeof api?.exportXlsx === "function" ? api : null;
  }

  /** **sheets only**; docs has no `set.theme`. */
  setTheme(theme: "light" | "dark" | "system"): void {
    if (this.opts.app !== "sheet") return;
    this.transport.sendSetTheme({ theme });
  }

  destroy(): void {
    this.destroyed = true;
    this.detachInteraction?.();
    this.detachInteraction = null;
    this.transport.destroy();
  }

  private async load() {
    try {
      let bytes = await this.opts.files.read(this.opts.path);
      if (this.isDelimited()) {
        const converted = await csvToXlsx(bytes, this.opts.path);
        this.dialect = converted.dialect;
        bytes = converted.bytes;
      } else if (isLegacySheet(this.opts.path)) {
        bytes = await legacyToXlsx(bytes);
      }

      return {
        ok: true as const,
        bytes,
        fileName: this.opts.path.slice(this.opts.path.lastIndexOf("/") + 1),
      };
    } catch (error) {
      this.opts.onError(error);
      return { ok: false as const, code: "load_failed", message: "This file could not be opened" };
    }
  }

  private isDelimited(): boolean {
    return this.opts.app === "sheet" && DELIMITED_EXTS.includes(extOf(this.opts.path));
  }

  /**
   * sheets always hands over xlsx bytes and docs always docx bytes. csv has to
   * be converted back, and what that discards has to be stated first.
   */
  private async write(bytes: ArrayBuffer): Promise<SaveResponseData> {
    if (this.destroyed) return { ok: false, code: "cancelled" };

    try {
      const delimited = this.isDelimited();
      let payload = bytes;
      let contentType = this.opts.app === "docs" ? DOCX_CONTENT_TYPE : XLSX_CONTENT_TYPE;
      let sheetCount = 1;

      if (delimited) {
        const dialect = this.dialect ?? { encoding: "utf-8" as const, delimiter: ",", bom: false };
        const csv = await xlsxToCsv(bytes, dialect);
        payload = csv.bytes;
        contentType = CSV_CONTENT_TYPE;
        sheetCount = csv.sheetCount;
      }

      if (!(await this.confirmIfLossy({ delimited, sheetCount }))) {
        return { ok: false, code: "cancelled", message: "Save cancelled" };
      }

      await this.opts.files.write(this.opts.path, payload, contentType);
      this.opts.onSaved();
      // The protocol wants a version token back, which the editor would echo
      // as `baseEtag` on its next save. This host does no optimistic locking —
      // it overwrites, like every other binary write in the app — so there is
      // no token to give, and an invented one would imply a guarantee that
      // isn't there.
      return { ok: true, etag: "" };
    } catch (error) {
      this.opts.onError(error);
      return { ok: false, code: "internal", message: "Save failed" };
    }
  }

  /** Only csv/tsv lose anything; xlsx/docx round-trip and xlsm cannot be edited. */
  private async confirmIfLossy(info: { delimited: boolean; sheetCount: number }): Promise<boolean> {
    if (!info.delimited || this.lossyConfirmed) return true;

    const ok = await this.opts.confirmLossy({
      sheetCount: info.sheetCount,
      reencodesTo: this.dialect && this.dialect.encoding !== "utf-8" ? "utf-8" : null,
    });
    if (ok) this.lossyConfirmed = true;
    return ok;
  }
}
