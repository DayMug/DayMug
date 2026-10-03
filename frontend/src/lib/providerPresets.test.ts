import { describe, expect, it } from "vitest";
import { formatContextWindow, parseContextWindow, presetsFor } from "./providerPresets";
import { accessModeOf, frameworkOf, providerTypeFor } from "./providerTypes";

describe("parseContextWindow", () => {
  it.each([
    ["", undefined],
    ["  ", undefined],
    ["128k", 128_000],
    ["1M", 1_000_000],
    ["1.5m", 1_500_000],
    ["131,072", 131_072],
    ["0", null],
    ["-5", null],
    ["lots", null],
  ])("%s → %s", (input, want) => {
    expect(parseContextWindow(input)).toBe(want);
  });
});

describe("formatContextWindow", () => {
  it("prints the unit vendors publish in", () => {
    expect(formatContextWindow(undefined)).toBe("");
    expect(formatContextWindow(1_000_000)).toBe("1M");
    expect(formatContextWindow(128_000)).toBe("128K");
    expect(formatContextWindow(131_072)).toBe("131072");
  });
});

describe("framework × access mode", () => {
  it("round-trips every provider type", () => {
    for (const type of ["claude", "claude-compatible", "codex", "openai-compatible"]) {
      expect(providerTypeFor(frameworkOf(type), accessModeOf(type))).toBe(type);
    }
  });

  it("offers only presets reachable through the framework", () => {
    expect(presetsFor("claude-code").map((p) => p.id)).toEqual(["anthropic", "deepseek", "custom"]);
    expect(presetsFor("codex").map((p) => p.id)).toEqual(["openai", "custom"]);
  });
});
