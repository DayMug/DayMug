import { describe, it, expect } from "vitest";
import {
  countFilesPerTopFolder,
  checkFolderFileLimit,
  formatBytes,
  formatSpeed,
  FOLDER_FILE_LIMIT,
} from "./uploadHelpers";

function f(rel: string, name?: string) {
  return { webkitRelativePath: rel, name: name ?? rel.split("/").pop() ?? "" };
}

describe("countFilesPerTopFolder", () => {
  it("groups by the first path segment", () => {
    const files = [f("docs/a.md"), f("docs/sub/b.md"), f("src/main.ts")];
    const counts = countFilesPerTopFolder(files);
    expect(counts.get("docs")).toBe(2);
    expect(counts.get("src")).toBe(1);
  });

  it("skips loose single files with no folder segment", () => {
    const files = [{ name: "loose.txt", webkitRelativePath: "" }, f("docs/a.md")];
    const counts = countFilesPerTopFolder(files);
    expect(counts.size).toBe(1);
    expect(counts.get("docs")).toBe(1);
  });

  it("falls back to name when webkitRelativePath is missing (drag-drop traversal)", () => {
    const files = [{ name: "imgs/1.png" }, { name: "imgs/2.png" }];
    const counts = countFilesPerTopFolder(files);
    expect(counts.get("imgs")).toBe(2);
  });
});

describe("checkFolderFileLimit", () => {
  it("returns null when every folder is under the limit", () => {
    const files = Array.from({ length: 5 }, (_, i) => f(`docs/${i}.md`));
    expect(checkFolderFileLimit(files)).toBeNull();
  });

  it("flags folders that exceed the limit and mentions zip in the message", () => {
    const files = Array.from({ length: FOLDER_FILE_LIMIT + 1 }, (_, i) => f(`big/${i}.md`));
    const msg = checkFolderFileLimit(files);
    expect(msg).not.toBeNull();
    expect(msg).toContain("big");
    expect(msg).toContain(String(FOLDER_FILE_LIMIT + 1));
    expect(msg).toContain("zip");
  });

  it("flags any folder that exceeds the limit even if others are fine", () => {
    const files = [
      ...Array.from({ length: 3 }, (_, i) => f(`small/${i}.md`)),
      ...Array.from({ length: 11 }, (_, i) => f(`big/${i}.md`)),
    ];
    const msg = checkFolderFileLimit(files);
    expect(msg).toContain("big");
  });

  it("does not flag loose files even when there are many", () => {
    const files = Array.from({ length: 50 }, (_, i) => ({
      name: `file-${i}.txt`,
      webkitRelativePath: "",
    }));
    expect(checkFolderFileLimit(files)).toBeNull();
  });

  it("respects a custom limit", () => {
    const files = Array.from({ length: 3 }, (_, i) => f(`a/${i}.md`));
    expect(checkFolderFileLimit(files, 5)).toBeNull();
    expect(checkFolderFileLimit(files, 2)).not.toBeNull();
  });
});

describe("formatBytes", () => {
  it("formats values across units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(formatBytes(2 * 1024 * 1024 * 1024)).toBe("2.00 GB");
  });

  it("handles invalid input safely", () => {
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(NaN)).toBe("0 B");
  });
});

describe("formatSpeed", () => {
  it("appends /s to the byte format", () => {
    expect(formatSpeed(1024)).toBe("1.0 KB/s");
    expect(formatSpeed(0)).toBe("0 B/s");
  });
});
