import { computed } from "vue";
import type { Ref } from "vue";

import type { RateLimitInfo, RateLimitWindow } from "@/stores/chatContextStore";
import { providerCapabilities, useModelRegistry } from "./useModelRegistry";

// Rate-limit badge gating. Only claude's CLI emits rate_limit_event
// frames today (capabilities.supports_rate_limit_events); codex never
// does. Without this gate the badge would render whatever stale claude
// frame was sitting in module-level state when the user switched onto
// a codex conversation — and the reset countdown shown would have
// nothing to do with the active provider.
export function useChatCapabilities(deps: {
  conversationProvider: Ref<string>;
  rateLimits: Ref<Partial<Record<RateLimitWindow, RateLimitInfo>>>;
}) {
  const { conversationProvider, rateLimits } = deps;

  const { registry: modelRegistry, ensureLoaded: ensureModelRegistry } = useModelRegistry();
  void ensureModelRegistry().catch(() => {
    // Registry load failure leaves capabilities null below; the badge
    // simply stays hidden until the registry becomes available, which
    // is the same conservative fallback used elsewhere.
  });

  const supportsRateLimitEvents = computed(() => {
    const provider = conversationProvider.value;
    if (!provider) return false;
    const caps = providerCapabilities(modelRegistry.value, provider);
    return caps?.supports_rate_limit_events ?? false;
  });

  const hasRateLimitInfo = computed(
    () =>
      supportsRateLimitEvents.value &&
      (Boolean(rateLimits.value.five_hour) || Boolean(rateLimits.value.seven_day)),
  );

  // Context-bar gating stays capability-driven: app-server reports a real
  // modelContextWindow, while future providers may not.
  const reportsContextUsage = computed(() => {
    const provider = conversationProvider.value;
    if (!provider) return false;
    const caps = providerCapabilities(modelRegistry.value, provider);
    return caps?.reports_context_usage ?? false;
  });

  return { supportsRateLimitEvents, hasRateLimitInfo, reportsContextUsage };
}
