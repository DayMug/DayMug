import { describe, it, expect } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const srcDir = dirname(fileURLToPath(import.meta.url));
const indexHtml = readFileSync(join(srcDir, "..", "index.html"), "utf8");

// Read the tag's own content attribute, not the whole file — the comment above
// the tag names `viewport-fit=cover` to explain why it is absent, and a naive
// scan of the file would match that and always report it as present.
const viewportContent = /<meta\s+name="viewport"[\s\S]*?content="([^"]*)"/.exec(indexHtml)?.[1];

const SAFE_AREA_INSET = ["env(safe-area", "inset-"].join("-");

function usesSafeAreaInsets(dir: string): boolean {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (usesSafeAreaInsets(full)) return true;
      continue;
    }
    // Skip tests: this file mentions the very token it is looking for, and a
    // test asserting on the absence of a thing must not be able to satisfy
    // itself.
    if (/\.test\.ts$/.test(entry)) continue;
    if (!/\.(vue|css|ts)$/.test(entry)) continue;
    if (readFileSync(full, "utf8").includes(SAFE_AREA_INSET)) return true;
  }
  return false;
}

describe("viewport meta", () => {
  it("is present and parseable", () => {
    expect(viewportContent).toBeTypeOf("string");
  });

  it("does not claim the system's reserved edges without insetting content from them", () => {
    // `viewport-fit=cover` is a promise to handle the display's reserved edges
    // yourself. Making that promise and never applying the inset env() vars put
    // the rail toggle and the model dropdowns inside the top band of an iPad,
    // where touches are consumed by the system and the page sees no event.
    const claimsFullDisplay = /viewport-fit\s*=\s*cover/.test(viewportContent ?? "");
    if (claimsFullDisplay) {
      expect(
        usesSafeAreaInsets(srcDir),
        "viewport-fit=cover requires safe-area inset padding somewhere in src/",
      ).toBe(true);
    } else {
      expect(claimsFullDisplay).toBe(false);
    }
  });

  it("still resizes content for the on-screen keyboard", () => {
    // Unrelated to the safe area, but it shares the one meta tag — a careless
    // edit to remove viewport-fit could take this with it.
    expect(viewportContent).toContain("interactive-widget=resizes-content");
  });
});
