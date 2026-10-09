import { describe, expect, it } from "vitest";

import { csvToXlsx, xlsxToCsv } from "./csvbridge";

function utf8(text: string): ArrayBuffer {
  const bytes = new TextEncoder().encode(text);
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
}

async function excel() {
  return (await import("exceljs/dist/exceljs.min.js")).default;
}

/** Full circle: csv bytes → xlsx → csv text, keeping the original dialect. */
async function roundTrip(input: ArrayBuffer, filename = "a.csv"): Promise<string> {
  const { bytes, dialect } = await csvToXlsx(input, filename);
  const out = await xlsxToCsv(bytes, dialect);
  return new TextDecoder("utf-8").decode(out.bytes);
}

describe("csvToXlsx / xlsxToCsv round trip", () => {
  it("returns a plain csv unchanged", async () => {
    expect(await roundTrip(utf8("姓名,年龄\n张三,30\n李四,28\n"))).toBe(
      "姓名,年龄\r\n张三,30\r\n李四,28\r\n",
    );
  });

  it("reads GBK Chinese correctly and writes it back as UTF-8", async () => {
    // GBK bytes for `姓名,部门`. Exports from Chinese tooling are commonly
    // GBK, and getting the decode wrong ruins the whole file.
    const gbk = new Uint8Array([
      0xd0, 0xd5, 0xc3, 0xfb, 0x2c, 0xb2, 0xbf, 0xc3, 0xc5, 0x0a, 0xd5, 0xc5, 0xc8, 0xfd, 0x2c,
      0xbc, 0xbc, 0xca, 0xf5,
    ]);
    const input = gbk.buffer.slice(gbk.byteOffset, gbk.byteOffset + gbk.byteLength) as ArrayBuffer;

    const { dialect } = await csvToXlsx(input, "a.csv");
    expect(dialect.encoding).toBe("gbk");
    expect(await roundTrip(input)).toBe("姓名,部门\r\n张三,技术\r\n");
  });

  it("preserves a semicolon or tab delimiter", async () => {
    expect(await roundTrip(utf8("a;b\n1;2\n"))).toBe("a;b\r\n1;2\r\n");
    expect(await roundTrip(utf8("a\tb\n1\t2\n"), "a.tsv")).toBe("a\tb\r\n1\t2\r\n");
  });

  it("re-quotes fields per RFC 4180", async () => {
    expect(await roundTrip(utf8('note\n"has,comma"\n"has""quote"\n"has\nnewline"\n'))).toBe(
      'note\r\n"has,comma"\r\n"has""quote"\r\n"has\nnewline"\r\n',
    );
  });

  it("keeps a leading zero as text while numbers stay numeric", async () => {
    const { bytes } = await csvToXlsx(utf8("id,qty\n007,12\n"));
    const workbook = new (await excel()).Workbook();
    await workbook.xlsx.load(bytes);
    const sheet = workbook.worksheets[0]!;

    expect(sheet.getCell(2, 1).value).toBe("007");
    expect(sheet.getCell(2, 2).value).toBe(12);
  });

  it("keeps a BOM if there was one and adds none if there wasn't", async () => {
    const withBom = await csvToXlsx(utf8("﻿a,b\n1,2\n"));
    expect(withBom.dialect.bom).toBe(true);
    const out = await xlsxToCsv(withBom.bytes, withBom.dialect);
    expect(new Uint8Array(out.bytes).slice(0, 3)).toEqual(new Uint8Array([0xef, 0xbb, 0xbf]));

    const plain = await csvToXlsx(utf8("a,b\n1,2\n"));
    expect(plain.dialect.bom).toBe(false);
    const plainOut = await xlsxToCsv(plain.bytes, plain.dialect);
    expect(new Uint8Array(plainOut.bytes)[0]).not.toBe(0xef);
  });
});

describe("what xlsxToCsv loses", () => {
  it("writes only the first worksheet and reports how many there were", async () => {
    const workbook = new (await excel()).Workbook();
    workbook.addWorksheet("one").addRow(["first"]);
    workbook.addWorksheet("two").addRow(["second"]);
    const buffer = await workbook.xlsx.writeBuffer();
    const view = new Uint8Array(buffer);
    const bytes = view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength);

    const out = await xlsxToCsv(bytes as ArrayBuffer, { delimiter: ",", bom: false });
    // sheetCount is what the caller's "the other sheets will be dropped"
    // warning is built from, so it must be reported.
    expect(out.sheetCount).toBe(2);
    expect(new TextDecoder().decode(out.bytes)).toBe("first\r\n");
  });

  it("writes a formula's computed value rather than its text", async () => {
    const workbook = new (await excel()).Workbook();
    const sheet = workbook.addWorksheet("Sheet1");
    sheet.addRow([1]);
    sheet.addRow([2]);
    sheet.getCell(3, 1).value = { formula: "SUM(A1:A2)", result: 3 };
    const buffer = await workbook.xlsx.writeBuffer();
    const view = new Uint8Array(buffer);
    const bytes = view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength);

    const out = await xlsxToCsv(bytes as ArrayBuffer, { delimiter: ",", bom: false });
    expect(new TextDecoder().decode(out.bytes)).toBe("1\r\n2\r\n3\r\n");
  });
});
