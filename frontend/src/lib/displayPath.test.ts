import { describe, it, expect } from "vitest";
import { displayPath } from "./displayPath";

describe("displayPath", () => {
  it("returns absolute paths verbatim", () => {
    expect(displayPath("/etc/passwd")).toBe("/etc/passwd");
    expect(displayPath("/home/alice/proj/src/")).toBe("/home/alice/proj/src/");
  });

  it("falls back to root for an empty path", () => {
    expect(displayPath("")).toBe("/");
  });
});
