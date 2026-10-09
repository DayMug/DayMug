import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  loadDraft,
  saveDraft,
  clearDraft,
  DRAFTS_STORAGE_KEY,
  MAX_ENTRIES,
  MAX_TEXT_LEN,
} from "./chatDrafts";

beforeEach(() => {
  localStorage.clear();
});

describe("chatDrafts", () => {
  it("round-trips text through save/load", () => {
    saveDraft("c1", "hello world");
    expect(loadDraft("c1")).toBe("hello world");
  });

  it("returns '' for an unknown conversation id", () => {
    expect(loadDraft("never-saved")).toBe("");
  });

  it("returns '' when conversationId is empty (defensive — composer can mount before id resolves)", () => {
    saveDraft("", "should-not-store");
    expect(loadDraft("")).toBe("");
    // Confirm nothing leaked under the empty key either.
    expect(localStorage.getItem(DRAFTS_STORAGE_KEY)).toBeNull();
  });

  it("saving '' removes the entry so empty drafts never accumulate", () => {
    saveDraft("c1", "draft text");
    saveDraft("c1", "");
    expect(loadDraft("c1")).toBe("");
    const raw = localStorage.getItem(DRAFTS_STORAGE_KEY);
    const map = raw ? JSON.parse(raw) : {};
    expect(map).not.toHaveProperty("c1");
  });

  it("clearDraft removes the entry", () => {
    saveDraft("c1", "draft text");
    clearDraft("c1");
    expect(loadDraft("c1")).toBe("");
  });

  it("clearDraft on an absent id is a no-op", () => {
    expect(() => clearDraft("missing")).not.toThrow();
  });

  it("truncates a single draft to MAX_TEXT_LEN so a runaway paste can't blow the quota", () => {
    const huge = "a".repeat(MAX_TEXT_LEN + 5000);
    saveDraft("c1", huge);
    expect(loadDraft("c1").length).toBe(MAX_TEXT_LEN);
  });

  it("LRU-caps at MAX_ENTRIES, evicting the oldest by updatedAt", () => {
    // Walk the clock forward one ms per save so updatedAt sorts deterministically.
    let now = 1_000_000_000;
    const nowSpy = vi.spyOn(Date, "now").mockImplementation(() => now);
    for (let i = 0; i < MAX_ENTRIES; i++) {
      now += 1;
      saveDraft(`c${i}`, `draft-${i}`);
    }
    // One more push evicts c0 (the oldest).
    now += 1;
    saveDraft("c-new", "draft-new");
    expect(loadDraft("c0")).toBe("");
    expect(loadDraft("c-new")).toBe("draft-new");
    // A middle entry survives.
    expect(loadDraft(`c${MAX_ENTRIES - 1}`)).toBe(`draft-${MAX_ENTRIES - 1}`);
    nowSpy.mockRestore();
  });

  it("survives a malformed payload in storage", () => {
    localStorage.setItem(DRAFTS_STORAGE_KEY, "{not-json");
    expect(loadDraft("c1")).toBe("");
    // And a subsequent save recovers a clean map.
    saveDraft("c1", "fresh");
    expect(loadDraft("c1")).toBe("fresh");
  });
});
