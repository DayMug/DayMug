import { describe, expect, it } from "vitest";

import { displayModelName } from "./modelName";

describe("displayModelName", () => {
  it.each([
    ["claude-opus-5-5[1m]", "Opus 5.5 · 1M"],
    ["claude-opus-5-5", "Opus 5.5"],
    ["claude-sonnet-4-5-20250929", "Sonnet 4.5"],
    ["claude-haiku-5-5", "Haiku 5.5"],
    ["claude-fable-5-1", "Fable 5.1"],
    ["claude-opus-4-20250514", "Opus 4"],
  ])("shortens %s to %s", (id, expected) => {
    expect(displayModelName(id)).toBe(expected);
  });

  it.each(["gpt-5-codex", "claude-3-5-sonnet-20241022", "my-local-model", ""])(
    "leaves %s unchanged",
    (id) => {
      expect(displayModelName(id)).toBe(id);
    },
  );
});
