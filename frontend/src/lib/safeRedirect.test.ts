import { describe, it, expect } from "vitest";
import { safeRedirect } from "./safeRedirect";

const ORIGIN = "https://daymug.example";

describe("safeRedirect", () => {
  describe("rejects off-origin targets", () => {
    const hostile: [string, string][] = [
      ["protocol-relative", "//evil.com"],
      ["protocol-relative with path", "//evil.com/steal"],
      ["triple slash", "///evil.com"],
      ["backslash authority", "/\\evil.com"],
      ["mixed slash authority", "/\\/evil.com"],
      ["leading backslashes", "\\\\evil.com"],
      ["absolute https", "https://evil.com"],
      ["absolute http", "http://evil.com/path"],
      ["scheme-relative userinfo", "//user:pass@evil.com"],
      ["javascript scheme", "javascript:alert(1)"],
      ["data scheme", "data:text/html,<script>alert(1)</script>"],
      ["leading whitespace hides authority", "  //evil.com"],
      ["tab smuggles authority", "/\t/evil.com"],
      ["newline smuggles authority", "/\n/evil.com"],
      ["carriage return smuggles authority", "/\r//evil.com"],
      ["null byte", `/${String.fromCharCode(0)}//evil.com`],
      ["absolute with encoded host", "https://evil.com/%2e%2e"],
      ["other origin same scheme", "https://daymug.example.evil.com/x"],
      // Only relative paths are accepted. main.ts only ever stores a
      // router fullPath, so an absolute form — even a same-origin one — is
      // a sign of tampering and is refused rather than normalised.
      ["absolute same-origin URL", "https://daymug.example/settings"],
    ];

    it.each(hostile)("%s -> /", (_label, input) => {
      expect(safeRedirect(input, ORIGIN)).toBe("/");
    });
  });

  describe("allows same-origin in-app paths", () => {
    const benign: [string, string, string][] = [
      ["plain path", "/settings", "/settings"],
      ["nested path", "/chat/agent-1/conv-2", "/chat/agent-1/conv-2"],
      ["path with query", "/settings/users?tab=agents", "/settings/users?tab=agents"],
      ["path with hash", "/docs#section", "/docs#section"],
      ["query and hash", "/file?path=a%2Fb#L10", "/file?path=a%2Fb#L10"],
      ["root", "/", "/"],
      // Percent-encoded slashes stay inside the path segment — the browser
      // never treats them as an authority delimiter, so this is same-origin.
      ["encoded slashes stay a path", "/%2f%2fevil.com", "/%2f%2fevil.com"],
    ];

    it.each(benign)("%s", (_label, input, expected) => {
      expect(safeRedirect(input, ORIGIN)).toBe(expected);
    });
  });

  it("falls back for a redirect back into the login flow", () => {
    expect(safeRedirect("/login", ORIGIN)).toBe("/");
    expect(safeRedirect("/login?redirect=%2F%2Fevil.com", ORIGIN)).toBe("/");
  });

  it("falls back for empty and non-string input", () => {
    expect(safeRedirect("", ORIGIN)).toBe("/");
    expect(safeRedirect(undefined, ORIGIN)).toBe("/");
    expect(safeRedirect(null, ORIGIN)).toBe("/");
    // vue-router hands back an array when the query key repeats.
    expect(safeRedirect(["/settings", "/chat"], ORIGIN)).toBe("/");
  });

  it("defaults to the current window origin", () => {
    expect(safeRedirect("/settings")).toBe("/settings");
    expect(safeRedirect("//evil.com")).toBe("/");
  });
});
