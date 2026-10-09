import { describe, it, expect } from "vitest";
import { pathRelative } from "./relPath";

describe("pathRelative", () => {
  it("returns '.' when paths are identical", () => {
    expect(pathRelative("/a/b", "/a/b")).toBe(".");
    expect(pathRelative("/a/b/", "/a/b")).toBe(".");
  });

  it("returns the descendant tail when `to` lives under `from`", () => {
    expect(pathRelative("/a", "/a/b/c")).toBe("b/c");
    expect(pathRelative("/home/alice/proj", "/home/alice/proj/src/foo.go")).toBe("src/foo.go");
  });

  it("inserts `..` segments when `to` lives above `from`", () => {
    expect(pathRelative("/a/b/c", "/a")).toBe("../..");
    expect(pathRelative("/home/alice/proj/sub", "/home/alice/proj")).toBe("..");
  });

  it("mixes `..` traversal and descent for siblings", () => {
    expect(pathRelative("/a/b", "/a/c/d")).toBe("../c/d");
    expect(pathRelative("/home/alice/proj", "/home/alice/other/file")).toBe("../other/file");
  });
});
