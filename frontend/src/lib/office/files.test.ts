import { describe, expect, it } from "vitest";

import { isLegacySheet, isReadOnly, officeAppFor } from "./files";

describe("officeAppFor", () => {
  it.each([
    ["book.xlsx", "sheet"],
    ["macros.xlsm", "sheet"],
    ["data.csv", "sheet"],
    ["data.tsv", "sheet"],
    ["old.xls", "sheet"],
    ["open.ods", "sheet"],
    ["letter.docx", "docs"],
  ])("routes %s to the %s editor", (path, app) => {
    expect(officeAppFor(path)).toBe(app);
  });

  it.each(["notes.txt", "deck.pptx", "legacy.doc", "archive.zip", "README"])(
    "leaves %s to the other preview surfaces",
    (path) => {
      expect(officeAppFor(path)).toBeNull();
    },
  );

  it("ignores case and directory components", () => {
    expect(officeAppFor("/a/b/Report.XLSX")).toBe("sheet");
  });
});

describe("isReadOnly", () => {
  it("refuses to edit a macro workbook", () => {
    // The export path emits no vbaProject, so saving would silently strip the
    // macros from a file the user believes is unchanged.
    expect(isReadOnly("build.xlsm")).toBe(true);
  });

  it.each(["old.xls", "open.ods"])("refuses to edit %s, which has no writer", (path) => {
    expect(isReadOnly(path)).toBe(true);
    expect(isLegacySheet(path)).toBe(true);
  });

  it.each(["book.xlsx", "data.csv", "letter.docx"])("lets %s be edited", (path) => {
    expect(isReadOnly(path)).toBe(false);
  });
});
