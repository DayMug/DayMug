import { describe, it, expect } from "vitest";
import { PROVIDER_TYPES, frameworkOf, isCodexFamily } from "./providerTypes";

describe("providerTypes", () => {
  it("places each compatible type with the transport it reuses", () => {
    expect(PROVIDER_TYPES.filter(isCodexFamily)).toEqual(["codex", "openai-compatible"]);
    expect(frameworkOf("claude-compatible")).toBe("claude-code");
    expect(frameworkOf("openai-compatible")).toBe("codex");
  });

  // The retired `mimo` type and any unknown string must not be claimed by the
  // codex family: config-dir placeholders and CLI choices keyed off these
  // helpers would otherwise silently point a stale row at the wrong CLI.
  it("claims no codex family for unknown or retired types", () => {
    for (const type of ["mimo", "", "anthropic"]) {
      expect(isCodexFamily(type)).toBe(false);
    }
  });
});
