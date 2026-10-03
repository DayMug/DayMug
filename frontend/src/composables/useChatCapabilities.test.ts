import { describe, it, expect, vi, beforeEach } from "vitest";
import { ref } from "vue";

import type { ModelRegistry } from "@/composables/apiTypes";
import type { RateLimitInfo, RateLimitWindow } from "@/stores/chatContextStore";

const mockRegistry = ref<ModelRegistry | null>(null);
const mockEnsureLoaded = vi.fn(async () => mockRegistry.value as ModelRegistry);

vi.mock("@/composables/useModelRegistry", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/composables/useModelRegistry")>();
  return {
    ...actual,
    useModelRegistry: () => ({ registry: mockRegistry, ensureLoaded: mockEnsureLoaded }),
  };
});

import { useChatCapabilities } from "./useChatCapabilities";

// Keep the capability configurable in this unit test even though production
// Codex always uses app-server; the composable must remain provider-neutral.
function makeRegistry(
  supportsRateLimitEvents: boolean,
  codexReportsContextUsage = false,
): ModelRegistry {
  return {
    default_provider: "claude",
    accounts: [],
    providers: [
      {
        name: "claude",
        models: ["claude-sonnet-4"],
        latest: "claude-sonnet-4",
        capabilities: {
          supports_compaction: true,
          supports_thinking_stream: true,
          supports_rate_limit_events: supportsRateLimitEvents,
          reports_context_usage: true,
          reports_cost_usd: true,
          supports_steering: true,
        },
      },
      {
        name: "codex",
        models: ["gpt-5.6"],
        latest: "gpt-5.6",
        capabilities: {
          supports_compaction: true,
          supports_thinking_stream: true,
          supports_rate_limit_events: false,
          reports_context_usage: codexReportsContextUsage,
          reports_cost_usd: false,
          supports_steering: true,
        },
      },
    ],
  };
}

function fiveHourLimit(): Partial<Record<RateLimitWindow, RateLimitInfo>> {
  return {
    five_hour: { type: "five_hour", status: "allowed", resets_at: 1234567890 },
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockRegistry.value = null;
});

describe("useChatCapabilities", () => {
  it("kicks off the registry load on setup", () => {
    useChatCapabilities({ conversationProvider: ref("claude"), rateLimits: ref({}) });
    expect(mockEnsureLoaded).toHaveBeenCalled();
  });

  it("reports no rate-limit support while the registry is unloaded", () => {
    const { supportsRateLimitEvents, hasRateLimitInfo } = useChatCapabilities({
      conversationProvider: ref("claude"),
      rateLimits: ref(fiveHourLimit()),
    });
    expect(supportsRateLimitEvents.value).toBe(false);
    expect(hasRateLimitInfo.value).toBe(false);
  });

  it("reports no rate-limit support when no provider is set", () => {
    mockRegistry.value = makeRegistry(true);
    const { supportsRateLimitEvents } = useChatCapabilities({
      conversationProvider: ref(""),
      rateLimits: ref(fiveHourLimit()),
    });
    expect(supportsRateLimitEvents.value).toBe(false);
  });

  it("enables the badge only when the provider supports rate-limit events", () => {
    mockRegistry.value = makeRegistry(true);
    const { supportsRateLimitEvents, hasRateLimitInfo } = useChatCapabilities({
      conversationProvider: ref("claude"),
      rateLimits: ref(fiveHourLimit()),
    });
    expect(supportsRateLimitEvents.value).toBe(true);
    expect(hasRateLimitInfo.value).toBe(true);
  });

  it("keeps hasRateLimitInfo off until a rate-limit frame has arrived", () => {
    mockRegistry.value = makeRegistry(true);
    const rateLimits = ref<Partial<Record<RateLimitWindow, RateLimitInfo>>>({});
    const { supportsRateLimitEvents, hasRateLimitInfo } = useChatCapabilities({
      conversationProvider: ref("claude"),
      rateLimits,
    });
    expect(supportsRateLimitEvents.value).toBe(true);
    expect(hasRateLimitInfo.value).toBe(false);

    rateLimits.value = fiveHourLimit();
    expect(hasRateLimitInfo.value).toBe(true);
  });

  it("stays hidden for providers without the capability even with stale frames in state", () => {
    mockRegistry.value = makeRegistry(false);
    const { supportsRateLimitEvents, hasRateLimitInfo } = useChatCapabilities({
      conversationProvider: ref("claude"),
      rateLimits: ref(fiveHourLimit()),
    });
    expect(supportsRateLimitEvents.value).toBe(false);
    expect(hasRateLimitInfo.value).toBe(false);
  });

  it("reacts to the provider switching on the same conversation state", () => {
    mockRegistry.value = makeRegistry(true);
    const conversationProvider = ref("codex");
    const { supportsRateLimitEvents } = useChatCapabilities({
      conversationProvider,
      rateLimits: ref(fiveHourLimit()),
    });
    expect(supportsRateLimitEvents.value).toBe(false);

    conversationProvider.value = "claude";
    expect(supportsRateLimitEvents.value).toBe(true);
  });

  it("reports no context usage while the registry is unloaded", () => {
    const { reportsContextUsage } = useChatCapabilities({
      conversationProvider: ref("claude"),
      rateLimits: ref({}),
    });
    expect(reportsContextUsage.value).toBe(false);
  });

  it("hides context usage for Codex on the CLI transport", () => {
    mockRegistry.value = makeRegistry(false, false);
    const { reportsContextUsage } = useChatCapabilities({
      conversationProvider: ref("codex"),
      rateLimits: ref({}),
    });
    expect(reportsContextUsage.value).toBe(false);
  });

  // Same provider name, different transport — the capability is the only thing
  // that tells them apart, which is the whole reason it's plumbed through.
  it("shows context usage for Codex on the app-server transport", () => {
    mockRegistry.value = makeRegistry(false, true);
    const { reportsContextUsage } = useChatCapabilities({
      conversationProvider: ref("codex"),
      rateLimits: ref({}),
    });
    expect(reportsContextUsage.value).toBe(true);
  });
});
