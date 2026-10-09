import { describe, it, expect } from "vitest";
import { splitFileName } from "./fileName";

describe("splitFileName", () => {
  it("splits the extension off the name", () => {
    expect(splitFileName("useChat.ts")).toEqual({ head: "useChat", tail: ".ts" });
  });

  it("splits on the last dot so the extension is what survives", () => {
    expect(splitFileName("WorkspacePanel.test.ts")).toEqual({
      head: "WorkspacePanel.test",
      tail: ".ts",
    });
  });

  it("rejoins to the original name", () => {
    for (const name of ["a.ts", ".gitignore", "LICENSE", "bundle.tar.gz", "v1.2.3-alpha"]) {
      const { head, tail } = splitFileName(name);
      expect(head + tail).toBe(name);
    }
  });

  it("treats a dotfile as a whole name, not an extension", () => {
    expect(splitFileName(".gitignore")).toEqual({ head: ".gitignore", tail: "" });
    expect(splitFileName(".env")).toEqual({ head: ".env", tail: "" });
  });

  it("leaves extensionless names alone", () => {
    expect(splitFileName("Dockerfile")).toEqual({ head: "Dockerfile", tail: "" });
    expect(splitFileName("LICENSE")).toEqual({ head: "LICENSE", tail: "" });
  });

  it("keeps compound archive extensions together", () => {
    expect(splitFileName("bundle.tar.gz")).toEqual({ head: "bundle", tail: ".tar.gz" });
    expect(splitFileName("backup.tar.zst")).toEqual({ head: "backup", tail: ".tar.zst" });
  });

  it("keeps .d.ts together", () => {
    expect(splitFileName("global.d.ts")).toEqual({ head: "global", tail: ".d.ts" });
  });

  it("does not leave an empty head when the name is only a compound extension", () => {
    expect(splitFileName(".tar.gz")).toEqual({ head: ".tar", tail: ".gz" });
  });

  it("ignores a trailing dot segment too long to be an extension", () => {
    // Pinning `.experimental` would leave no room for the prefix it is meant
    // to protect.
    expect(splitFileName("release.experimental")).toEqual({
      head: "release.experimental",
      tail: "",
    });
  });

  it("handles a trailing dot", () => {
    expect(splitFileName("weird.")).toEqual({ head: "weird.", tail: "" });
  });

  it("handles an empty name", () => {
    expect(splitFileName("")).toEqual({ head: "", tail: "" });
  });

  it("matches the extension case-insensitively", () => {
    expect(splitFileName("PHOTO.PNG")).toEqual({ head: "PHOTO", tail: ".PNG" });
    expect(splitFileName("BUNDLE.TAR.GZ")).toEqual({ head: "BUNDLE", tail: ".TAR.GZ" });
  });
});
