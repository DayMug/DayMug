import { describe, it, expect } from "vitest";
import { avatarHue, avatarColors, avatarInitials } from "./avatar";

describe("avatarHue", () => {
  it("returns a stable value for the same name", () => {
    expect(avatarHue("Backend Go")).toBe(avatarHue("Backend Go"));
  });

  it("uses the full name instead of only the first letter", () => {
    const names = ["Alice", "Andrew", "Avery", "Alicia", "Aaron"];
    const hues = new Set(names.map(avatarHue));
    expect(hues.size).toBe(names.length);
  });

  it("distinguishes names that only differ near the end", () => {
    expect(avatarHue("Agent Alpha")).not.toBe(avatarHue("Agent Alpine"));
  });

  it("returns a fallback hue for empty / null / whitespace", () => {
    expect(avatarHue("")).toBe(220);
    expect(avatarHue("   ")).toBe(220);
    expect(avatarHue(null)).toBe(220);
    expect(avatarHue(undefined)).toBe(220);
  });

  it("stays inside [0, 360)", () => {
    for (const n of ["a", "abc", "Backend Go", "测试 用户", "x".repeat(200)]) {
      const h = avatarHue(n);
      expect(h).toBeGreaterThanOrEqual(0);
      expect(h).toBeLessThan(360);
    }
  });
});

describe("avatarInitials", () => {
  it("takes the first letter of up to two words", () => {
    expect(avatarInitials("Backend Go")).toBe("BG");
    expect(avatarInitials("Web Vue")).toBe("WV");
    expect(avatarInitials("Sandbox")).toBe("S");
  });

  it("uppercases and trims", () => {
    expect(avatarInitials("  alice  bob  ")).toBe("AB");
    expect(avatarInitials("alice bob carol")).toBe("AB");
  });

  it("returns ? for blank inputs", () => {
    expect(avatarInitials("")).toBe("?");
    expect(avatarInitials("   ")).toBe("?");
    expect(avatarInitials(null)).toBe("?");
    expect(avatarInitials(undefined)).toBe("?");
  });
});

describe("avatarColors", () => {
  it("includes the hue in both bg and fg expressions", () => {
    const { bg, fg } = avatarColors("Backend Go");
    const hue = avatarHue("Backend Go");
    expect(bg).toContain(`${hue}`);
    expect(fg).toContain(`${hue}`);
    expect(bg).toMatch(/^oklch\(/);
    expect(fg).toMatch(/^oklch\(/);
  });
});
