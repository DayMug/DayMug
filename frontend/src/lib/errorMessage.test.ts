import { describe, it, expect } from "vitest";
import { errorMessage } from "./errorMessage";

describe("errorMessage", () => {
  it("returns an Error's message", () => {
    expect(errorMessage(new Error("boom"))).toBe("boom");
  });

  it("keeps an Error's empty message rather than using the fallback", () => {
    expect(errorMessage(new Error(""), "fallback")).toBe("");
  });

  it("stringifies non-Error values", () => {
    expect(errorMessage("plain")).toBe("plain");
    expect(errorMessage(42)).toBe("42");
    expect(errorMessage(undefined)).toBe("undefined");
    expect(errorMessage({ status: 500 })).toBe("[object Object]");
  });

  it("uses the fallback for non-Error values when given", () => {
    expect(errorMessage("plain", "localized")).toBe("localized");
  });
});
