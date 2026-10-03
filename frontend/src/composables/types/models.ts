import type { ModelSpec } from "@/lib/providerPresets";

// Split out of apiTypes.ts (kept as a re-export barrel) so each domain's
// types live next to their concerns. Import via "@/composables/apiTypes".

// ProviderCapabilities mirrors backend agent.Capabilities. Boolean
// flags drive frontend feature gating (insert-while-running, the
// thinking pane, etc.) so the UI never hard-codes a provider
// name string check.
export interface ProviderCapabilities {
  supports_compaction: boolean;
  supports_thinking_stream: boolean;
  supports_rate_limit_events: boolean;
  reports_context_usage: boolean;
  reports_cost_usd: boolean;
  // True when a message sent mid-turn can be inserted into the running turn
  // (Agent SDK / app-server). CLI transports only queue it for afterwards.
  supports_steering: boolean;
}

// ProviderEntry / ModelRegistry mirror the /api/models response. The chat
// header's model picker renders directly from this shape so a new model
// added on the backend shows up without a frontend release.
export interface ProviderEntry {
  name: string;
  models: string[];
  latest: string;
  capabilities: ProviderCapabilities;
  // Admin-selected transport ("cli" / "agent-sdk" / "app-server").
  transport?: string;
}

// One entry per configured account. The model picker renders each globally
// unique account name from the user's bindings and looks up the model list
// here, so two accounts of the same provider type (e.g. a stock `codex` and a
// local `codex-qwen`) surface their own models instead of collapsing into one
// type-level list. Provider still travels with the entry for backend routing.
export interface AccountModelEntry {
  provider: string;
  account: string;
  models: string[];
  latest: string;
  // Admin-declared limits keyed by model id.
  specs: Record<string, ModelSpec>;
}

export interface ModelRegistry {
  providers: ProviderEntry[];
  accounts: AccountModelEntry[];
  default_provider: string;
}
