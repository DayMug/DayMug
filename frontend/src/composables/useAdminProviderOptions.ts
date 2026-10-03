import { computed, type ComputedRef, type Ref } from "vue";
import { useI18n } from "vue-i18n";
import type { AdminPublicConfig } from "./useApi";

export interface AdminProviderOptions {
  /** Provider names grouped by CLI type, e.g. `{ claude: ["default"], codex: [...] }`. */
  optionsByType: ComputedRef<Record<string, string[]>>;
  /** Stable render order for the per-type pickers. */
  types: ComputedRef<string[]>;
  /** Localized section label for one CLI type. */
  labelForType: (type: string) => string;
}

// useAdminProviderOptions derives the per-CLI-type provider pickers from the
// admin public config. Three surfaces on the users page need exactly this
// (create form, batch toolbar, inline edit row); deriving it once keeps the
// grouping from being reimplemented per surface.
export function useAdminProviderOptions(cfg: Ref<AdminPublicConfig | null>): AdminProviderOptions {
  const { t } = useI18n();

  const optionsByType = computed<Record<string, string[]>>(() => {
    const out: Record<string, string[]> = {};
    for (const p of cfg.value?.providers ?? []) {
      const type = p.type || "claude";
      if (!out[type]) out[type] = [];
      out[type].push(p.name);
    }
    return out;
  });

  // Claude sorts first — it's the historical default and the only type many
  // deployments have; the rest follow alphabetically so the order is stable
  // across config reloads.
  const types = computed<string[]>(() =>
    Object.keys(optionsByType.value).sort((a, b) => {
      if (a === "claude") return -1;
      if (b === "claude") return 1;
      return a.localeCompare(b);
    }),
  );

  // Display names for the types we ship. The generic branch title-cases an
  // unknown type, which reads badly for the compatible pair ("Openai-
  // compatible"), so those two are spelled out rather than derived.
  const TYPE_DISPLAY_NAMES: Record<string, string> = {
    "claude-compatible": "Claude-compatible",
    "openai-compatible": "OpenAI-compatible",
  };

  function labelForType(type: string): string {
    if (type === "claude") return t("settings.adminUsers.claudeAccount");
    if (type === "codex") return t("settings.adminUsers.codexAccount");
    return t("settings.adminUsers.providerAccount", {
      type: TYPE_DISPLAY_NAMES[type] ?? type.charAt(0).toUpperCase() + type.slice(1),
    });
  }

  return { optionsByType, types, labelForType };
}
