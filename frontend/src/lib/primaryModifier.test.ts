import { describe, expect, it } from "vitest";
import { isMacLike, isPrimaryModifier, primaryModifierLabel } from "./primaryModifier";

function nav(platform: string): Navigator {
  return { platform, userAgent: "" } as Navigator;
}

describe("primaryModifier", () => {
  it("uses Ctrl on Windows/Linux", () => {
    const linux = nav("Linux x86_64");
    expect(isMacLike(linux)).toBe(false);
    expect(primaryModifierLabel(linux)).toBe("Ctrl");
    expect(isPrimaryModifier({ ctrlKey: true, metaKey: false }, linux)).toBe(true);
    expect(isPrimaryModifier({ ctrlKey: false, metaKey: true }, linux)).toBe(false);
  });

  it("uses Command on Mac-like platforms", () => {
    const mac = nav("MacIntel");
    expect(isMacLike(mac)).toBe(true);
    expect(primaryModifierLabel(mac)).toBe("⌘");
    expect(isPrimaryModifier({ ctrlKey: true, metaKey: false }, mac)).toBe(false);
    expect(isPrimaryModifier({ ctrlKey: false, metaKey: true }, mac)).toBe(true);
  });
});
