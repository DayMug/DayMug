import { describe, it, expect, vi } from "vitest";

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

import { useTheme } from "./useTheme";

describe("useTheme", () => {
  it("returns isDark ref and toggleTheme function", () => {
    const { isDark, toggleTheme } = useTheme();
    expect(isDark.value).toBe(false);
    toggleTheme();
    expect(isDark.value).toBe(true);
    toggleTheme();
    expect(isDark.value).toBe(false);
  });
});
