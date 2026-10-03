import { describe, it, expect } from "vitest";

import { detectLanguage, formatJSON } from "./codeEditor";

describe("detectLanguage", () => {
  it.each([
    ["/src/App.vue", "html"],
    ["/src/main.ts", "typescript"],
    ["/x/Component.tsx", "typescript"],
    ["/x/server.go", "go"],
    ["/x/README.MD", "markdown"],
    ["/x/config.yml", "yaml"],
    ["/x/Dockerfile", "dockerfile"],
    ["/x/.env", "shell"],
    ["/x/Cargo.toml", "toml"],
    ["/x/notes.txt", "plaintext"],
    ["/x/Makefile", "plaintext"],
    ["/x/unknown.zzz", "plaintext"],
  ])("%s → %s", (path, label) => {
    expect(detectLanguage(path).label).toBe(label);
  });

  it("gives plain text no grammar to download", () => {
    expect(detectLanguage("/x/notes.txt").load).toBeUndefined();
  });

  it("resolves a grammar for a known language", async () => {
    await expect(detectLanguage("/x/a.json").load?.()).resolves.toBeDefined();
  });
});

describe("formatJSON", () => {
  it("pretty-prints with two-space indent and keeps a trailing newline", () => {
    expect(formatJSON('{"a":[1,2]}\n')).toBe('{\n  "a": [\n    1,\n    2\n  ]\n}\n');
  });

  it("returns null for invalid JSON so the buffer is left alone", () => {
    expect(formatJSON("{ not json")).toBeNull();
  });
});
