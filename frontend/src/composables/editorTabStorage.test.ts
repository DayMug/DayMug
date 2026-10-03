import { beforeEach, describe, expect, it } from "vitest";

import {
  TABS_LRU_LIMIT,
  TABS_MAX_AGE_MS,
  TABS_STORAGE_KEY,
  loadTabs,
  saveTabs,
} from "./editorTabStorage";

function storedKeys(): string[] {
  const raw = sessionStorage.getItem(TABS_STORAGE_KEY);
  if (!raw) return [];
  return JSON.parse(raw).entries.map((e: { k: string }) => e.k);
}

describe("editorTabStorage", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it("round-trips a tab set", () => {
    saveTabs("preview", "u1", ["a.md", "b.md"]);
    expect(loadTabs("preview", "u1")).toEqual(["a.md", "b.md"]);
  });

  it("keeps scopes apart so two mounts don't share tabs", () => {
    saveTabs("preview", "u1", ["a.md"]);
    saveTabs("chat", "u1", ["b.md"]);
    expect(loadTabs("preview", "u1")).toEqual(["a.md"]);
    expect(loadTabs("chat", "u1")).toEqual(["b.md"]);
  });

  it("keeps users apart", () => {
    saveTabs("chat", "u1", ["a.md"]);
    expect(loadTabs("chat", "u2")).toEqual([]);
  });

  it("stores everything under a single key", () => {
    saveTabs("preview", "u1", ["a.md"]);
    saveTabs("chat", "u2", ["b.md"]);
    const daymugKeys = Object.keys(sessionStorage).filter((k) => k.startsWith("daymug."));
    expect(daymugKeys).toEqual([TABS_STORAGE_KEY]);
  });

  it("removes the entry when the last tab closes", () => {
    saveTabs("chat", "u1", ["a.md"]);
    saveTabs("chat", "u1", []);
    expect(storedKeys()).toEqual([]);
  });

  it("keeps at most TABS_LRU_LIMIT entries, dropping the oldest", () => {
    for (let i = 0; i < TABS_LRU_LIMIT + 3; i++) {
      saveTabs("chat", `u${i}`, [`f${i}.md`]);
    }
    const keys = storedKeys();
    expect(keys).toHaveLength(TABS_LRU_LIMIT);
    expect(keys).not.toContain("chat:u0");
    expect(keys).toContain(`chat:u${TABS_LRU_LIMIT + 2}`);
  });

  it("drops entries older than TABS_MAX_AGE_MS on write", () => {
    sessionStorage.setItem(
      TABS_STORAGE_KEY,
      JSON.stringify({
        v: 1,
        entries: [{ k: "chat:stale", paths: ["old.md"], ts: Date.now() - TABS_MAX_AGE_MS - 1 }],
      }),
    );
    saveTabs("chat", "u1", ["new.md"]);
    expect(storedKeys()).not.toContain("chat:stale");
  });

  it("discards a corrupt blob instead of half-trusting it", () => {
    sessionStorage.setItem(TABS_STORAGE_KEY, "{not json");
    expect(loadTabs("chat", "u1")).toEqual([]);
  });

  it("discards a blob written by a different schema version", () => {
    sessionStorage.setItem(
      TABS_STORAGE_KEY,
      JSON.stringify({ v: 99, entries: [{ k: "chat:u1", paths: ["x.md"], ts: Date.now() }] }),
    );
    expect(loadTabs("chat", "u1")).toEqual([]);
  });
});
