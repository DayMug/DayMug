import { describe, it, expect } from "vitest";
import { apiBase } from "./apiBase";

describe("apiBase", () => {
  it("returns window.location.origin", () => {
    expect(apiBase()).toBe(window.location.origin);
  });
});
