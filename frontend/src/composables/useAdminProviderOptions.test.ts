import { describe, it, expect } from "vitest";
import { ref } from "vue";
import { mount } from "@vue/test-utils";

import { useAdminProviderOptions } from "./useAdminProviderOptions";
import type { AdminPublicConfig } from "./useApi";

const baseConfig = {
  default_home_root: "/srv/users",
  providers: [],
  sandbox: { enabled: false, type: "noop" },
  upgrade: { enabled: true },
  current_version: "v1.0.0",
  backend: "sqlite",
} as AdminPublicConfig;

// The composable calls useI18n(), which needs an active component instance.
function withConfig(cfg: AdminPublicConfig | null) {
  let api: ReturnType<typeof useAdminProviderOptions>;
  mount({
    setup() {
      api = useAdminProviderOptions(ref(cfg));
      return () => null;
    },
  });
  return api!;
}

describe("useAdminProviderOptions", () => {
  it("groups typed providers by CLI type", () => {
    const { optionsByType } = withConfig({
      ...baseConfig,
      providers: [
        { name: "default", type: "claude", max_concurrent: 1 },
        { name: "alt", type: "claude", max_concurrent: 1 },
        { name: "default-codex", type: "codex", max_concurrent: 1 },
      ],
    });
    expect(optionsByType.value).toEqual({
      claude: ["default", "alt"],
      codex: ["default-codex"],
    });
  });

  it("offers no pickers before the config has loaded", () => {
    const { optionsByType } = withConfig(null);
    expect(optionsByType.value).toEqual({});
  });

  it("treats an untyped provider row as claude", () => {
    const { optionsByType } = withConfig({
      ...baseConfig,
      providers: [{ name: "legacy", type: "", max_concurrent: 1 }],
    });
    expect(optionsByType.value).toEqual({ claude: ["legacy"] });
  });

  it("sorts claude first and the rest alphabetically", () => {
    const { types } = withConfig({
      ...baseConfig,
      providers: [
        { name: "z", type: "zeta", max_concurrent: 1 },
        { name: "c", type: "codex", max_concurrent: 1 },
        { name: "a", type: "claude", max_concurrent: 1 },
      ],
    });
    expect(types.value).toEqual(["claude", "codex", "zeta"]);
  });

  it("yields nothing before the config resolves", () => {
    const { optionsByType, types } = withConfig(null);
    expect(optionsByType.value).toEqual({});
    expect(types.value).toEqual([]);
  });

  it("titlecases an unknown CLI type for its label", () => {
    const { labelForType } = withConfig(baseConfig);
    expect(labelForType("claude")).toBe("Claude account");
    expect(labelForType("gemini")).toContain("Gemini");
  });
});
