import { beforeEach, describe, expect, it } from "vitest";
import {
  AGENT_PREFS_STORAGE_KEY,
  loadAgentPrefs,
  saveAgentLocation,
  saveAgentModel,
} from "./agentPrefs";

beforeEach(() => {
  localStorage.clear();
});

describe("agentPrefs", () => {
  it("keeps a model per agent", () => {
    saveAgentModel("a1", { provider: "claude", model: "claude-sonnet-5", think_level: "high" });
    saveAgentModel("a2", { provider: "codex", model: "gpt-5.5", account: "codex-main" });

    expect(loadAgentPrefs("a1").model).toEqual({
      provider: "claude",
      model: "claude-sonnet-5",
      think_level: "high",
    });
    expect(loadAgentPrefs("a2").model).toEqual({
      provider: "codex",
      model: "gpt-5.5",
      account: "codex-main",
    });
    expect(loadAgentPrefs("a3")).toEqual({});
  });

  it("keeps the run location next to the model without clobbering it", () => {
    saveAgentModel("a1", { provider: "claude", model: "claude-sonnet-5" });
    saveAgentLocation("a1", { kind: "device", device_id: "d1", work_dir: "/Users/alice/proj" });

    expect(loadAgentPrefs("a1")).toEqual({
      model: { provider: "claude", model: "claude-sonnet-5" },
      location: { kind: "device", device_id: "d1", work_dir: "/Users/alice/proj" },
    });

    saveAgentLocation("a1", { kind: "server" });
    expect(loadAgentPrefs("a1").location).toEqual({ kind: "server" });
    expect(loadAgentPrefs("a1").model).toEqual({ provider: "claude", model: "claude-sonnet-5" });
  });

  it("drops malformed entries but keeps the valid ones", () => {
    localStorage.setItem(
      AGENT_PREFS_STORAGE_KEY,
      JSON.stringify({
        good: { model: { provider: "claude", model: "m" }, location: { kind: "server" } },
        badModel: { model: { provider: "", model: "m" } },
        badLocation: { location: { kind: "device", device_id: "d1" } },
        junk: 42,
      }),
    );

    expect(loadAgentPrefs("good")).toEqual({
      model: { provider: "claude", model: "m" },
      location: { kind: "server" },
    });
    expect(loadAgentPrefs("badModel")).toEqual({});
    expect(loadAgentPrefs("badLocation")).toEqual({});
    expect(loadAgentPrefs("junk")).toEqual({});
  });

  it("ignores unparseable storage", () => {
    localStorage.setItem(AGENT_PREFS_STORAGE_KEY, "{not-json");
    expect(loadAgentPrefs("a1")).toEqual({});
    saveAgentLocation("a1", { kind: "server" });
    expect(loadAgentPrefs("a1").location).toEqual({ kind: "server" });
  });

  it("forgets the least recently touched agents past the cap", () => {
    for (let i = 0; i < 50; i++) saveAgentLocation(`a${i}`, { kind: "server" });
    saveAgentModel("a0", { provider: "claude", model: "m" }); // touch a0 → newest
    saveAgentLocation("a50", { kind: "server" }); // evicts a1, the oldest untouched

    expect(loadAgentPrefs("a0").model).toEqual({ provider: "claude", model: "m" });
    expect(loadAgentPrefs("a1")).toEqual({});
    expect(loadAgentPrefs("a50").location).toEqual({ kind: "server" });
  });
});
