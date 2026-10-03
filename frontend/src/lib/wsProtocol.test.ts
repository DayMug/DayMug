import { afterEach, describe, expect, it, vi } from "vitest";
import {
  isContextUsage,
  isRateLimitInfo,
  isSystemInitPayload,
  isToolUseStartPayload,
  isUserQuestionRequest,
  isWSFrame,
  parseWirePayload,
} from "./wsProtocol";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("isWSFrame", () => {
  it("accepts any object with a string type, known or not", () => {
    expect(isWSFrame({ type: "delta", content: "x" })).toBe(true);
    expect(isWSFrame({ type: "from_a_newer_backend" })).toBe(true);
  });

  it.each([[null], [[]], ["delta"], [{}], [{ type: 3 }]])("rejects %j", (v) => {
    expect(isWSFrame(v)).toBe(false);
  });
});

describe("payload guards", () => {
  it("isContextUsage requires numeric used/total and numeric extras", () => {
    expect(isContextUsage({ used: 1, total: 2 })).toBe(true);
    expect(isContextUsage({ used: 1, total: 2, cache_read: 3 })).toBe(true);
    expect(isContextUsage({ used: 1 })).toBe(false);
    expect(isContextUsage({ used: 1, total: 2, cache_read: "3" })).toBe(false);
  });

  it("isRateLimitInfo checks structure, not which window", () => {
    expect(isRateLimitInfo({ type: "five_hour", status: "ok", resets_at: 1 })).toBe(true);
    expect(isRateLimitInfo({ type: "seven_day_opus", resets_at: 1 })).toBe(true);
    expect(isRateLimitInfo({ type: "five_hour", resets_at: "soon" })).toBe(false);
  });

  it("isSystemInitPayload allows omitted fields but not wrong types", () => {
    expect(isSystemInitPayload({})).toBe(true);
    expect(isSystemInitPayload({ model: "m", tools: ["a"] })).toBe(true);
    expect(isSystemInitPayload({ tools: "a" })).toBe(false);
    expect(isSystemInitPayload({ model: 1 })).toBe(false);
  });

  it("isToolUseStartPayload allows any command shape", () => {
    expect(isToolUseStartPayload({ name: "Bash", command: ["ls"] })).toBe(true);
    expect(isToolUseStartPayload({ name: 1 })).toBe(false);
  });

  it("isUserQuestionRequest needs an id and at least one question object", () => {
    expect(isUserQuestionRequest({ request_id: "r", questions: [{ id: "q" }] })).toBe(true);
    expect(isUserQuestionRequest({ request_id: "", questions: [{ id: "q" }] })).toBe(false);
    expect(isUserQuestionRequest({ request_id: "r", questions: [] })).toBe(false);
    expect(isUserQuestionRequest({ request_id: "r", questions: [null] })).toBe(false);
  });
});

describe("parseWirePayload", () => {
  it("returns the decoded payload when it passes the guard", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(parseWirePayload("context_usage", '{"used":1,"total":2}', isContextUsage)).toEqual({
      used: 1,
      total: 2,
    });
    expect(spy).not.toHaveBeenCalled();
  });

  it("logs and returns null for invalid JSON", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(parseWirePayload("context_usage", "{", isContextUsage)).toBeNull();
    expect(spy).toHaveBeenCalledOnce();
    expect(spy.mock.calls[0][0]).toContain('"context_usage"');
  });

  it("logs and returns null for a shape mismatch", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    expect(parseWirePayload("context_usage", "null", isContextUsage)).toBeNull();
    expect(spy).toHaveBeenCalledOnce();
  });
});
