import { describe, it, expect } from "vitest";
import { createBackoff, RECONNECT_BASE_MS, RECONNECT_MAX_MS } from "./backoff";

describe("createBackoff", () => {
  it("doubles from the base up to the cap", () => {
    const b = createBackoff();
    const delays = Array.from({ length: 8 }, () => b.next());
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000]);
    expect(delays[0]).toBe(RECONNECT_BASE_MS);
    expect(Math.max(...delays)).toBe(RECONNECT_MAX_MS);
  });

  it("starts over from the base after reset", () => {
    const b = createBackoff();
    b.next();
    b.next();
    b.reset();
    expect(b.next()).toBe(1000);
  });

  it("stays flat when base equals the cap", () => {
    const b = createBackoff(3000, 3000);
    expect([b.next(), b.next(), b.next()]).toEqual([3000, 3000, 3000]);
  });
});
