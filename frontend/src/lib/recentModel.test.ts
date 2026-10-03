import { beforeEach, describe, expect, it } from "vitest";
import { loadRecentModel, RECENT_MODEL_STORAGE_KEY, saveRecentModel } from "./recentModel";

beforeEach(() => {
  localStorage.clear();
});

describe("recentModel", () => {
  it("persists and restores the latest provider/model pair", () => {
    saveRecentModel("codex", "gpt-5.5");

    expect(loadRecentModel()).toEqual({ provider: "codex", model: "gpt-5.5" });
  });

  it("persists the selected account when one is available", () => {
    saveRecentModel("codex", "qwen3-coder", "codex-qwen", "max");

    expect(loadRecentModel()).toEqual({
      provider: "codex",
      model: "qwen3-coder",
      account: "codex-qwen",
      think_level: "max",
    });
  });

  it("ignores malformed storage", () => {
    localStorage.setItem(RECENT_MODEL_STORAGE_KEY, "{not-json");

    expect(loadRecentModel()).toBeNull();
  });
});
