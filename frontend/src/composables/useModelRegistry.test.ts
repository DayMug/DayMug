import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchModels } from "./apiModels";
import { invalidateModelRegistry, loadModelRegistry, modelAcceptsImages } from "./useModelRegistry";

vi.mock("./apiModels", () => ({ fetchModels: vi.fn() }));

const mockedFetchModels = vi.mocked(fetchModels);

describe("useModelRegistry", () => {
  beforeEach(() => {
    invalidateModelRegistry();
    mockedFetchModels.mockReset();
  });

  it("refetches after an admin invalidates the registry", async () => {
    mockedFetchModels
      .mockResolvedValueOnce({ providers: [], accounts: [], default_provider: "" })
      .mockResolvedValueOnce({
        providers: [
          {
            name: "claude",
            models: ["new-model"],
            latest: "new-model",
            capabilities: {
              supports_compaction: true,
              supports_thinking_stream: true,
              supports_rate_limit_events: true,
              reports_context_usage: true,
              reports_cost_usd: true,
              supports_steering: true,
            },
          },
        ],
        accounts: [],
        default_provider: "claude",
      });

    await loadModelRegistry();
    await loadModelRegistry();
    expect(mockedFetchModels).toHaveBeenCalledTimes(1);

    invalidateModelRegistry();
    const refreshed = await loadModelRegistry();
    expect(mockedFetchModels).toHaveBeenCalledTimes(2);
    expect(refreshed.providers[0]?.latest).toBe("new-model");
  });
});

describe("modelAcceptsImages", () => {
  const registry = {
    providers: [],
    default_provider: "claude",
    accounts: [
      {
        provider: "claude-compatible",
        account: "text-only",
        models: ["m"],
        latest: "m",
        specs: { m: { no_image_input: true } },
      },
    ],
  };

  it("follows the admin's text-only flag", () => {
    expect(modelAcceptsImages(registry, "claude-compatible", "text-only", "m")).toBe(false);
  });

  it("assumes images work when nothing is declared", () => {
    expect(modelAcceptsImages(registry, "claude-compatible", "text-only", "other")).toBe(true);
    expect(modelAcceptsImages(registry, "claude", "missing", "m")).toBe(true);
    expect(modelAcceptsImages(null, "claude", "missing", "m")).toBe(true);
  });
});
