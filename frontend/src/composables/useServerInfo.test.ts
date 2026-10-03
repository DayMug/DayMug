import { describe, it, expect, vi, beforeEach } from "vitest";

const mockFetchServerInfo = vi.fn();
vi.mock("./apiServerInfo", () => ({
  fetchServerInfo: () => mockFetchServerInfo(),
}));

const memSession: Record<string, string> = {};
vi.stubGlobal("sessionStorage", {
  getItem: (k: string) => memSession[k] ?? null,
  setItem: (k: string, v: string) => {
    memSession[k] = v;
  },
  removeItem: (k: string) => {
    delete memSession[k];
  },
  clear: () => {
    for (const k of Object.keys(memSession)) delete memSession[k];
  },
});

const SAMPLE = {
  version: "1.2.3",
  sandbox_enabled: false,
  sandbox_type: "",
  manifest_url: "https://example.test/manifest.json",
};

// Reset both the in-memory singleton and the sessionStorage cache between
// tests by importing the module fresh each time. vitest's vi.resetModules
// makes the next import re-execute the module body.
async function importFresh() {
  vi.resetModules();
  for (const k of Object.keys(memSession)) delete memSession[k];
  mockFetchServerInfo.mockReset();
  return import("./useServerInfo");
}

describe("useServerInfo", () => {
  beforeEach(() => {
    for (const k of Object.keys(memSession)) delete memSession[k];
    mockFetchServerInfo.mockReset();
  });

  it("loadServerInfo fetches the network on first call and caches in sessionStorage", async () => {
    const { useServerInfo } = await importFresh();
    mockFetchServerInfo.mockResolvedValueOnce(SAMPLE);
    const { loadServerInfo, serverInfo } = useServerInfo();
    const result = await loadServerInfo();
    expect(result).toEqual(SAMPLE);
    expect(serverInfo.value).toEqual(SAMPLE);
    expect(JSON.parse(memSession["daymug-server-info"])).toEqual(SAMPLE);
  });

  it("reuses the sessionStorage cache on remount without hitting the network", async () => {
    // Prime the cache as if a prior page load had already saved it.
    memSession["daymug-server-info"] = JSON.stringify(SAMPLE);
    const { useServerInfo } = await importFresh();
    // Re-seed since importFresh cleared the cache; do it after the import.
    memSession["daymug-server-info"] = JSON.stringify(SAMPLE);
    const { loadServerInfo } = useServerInfo();
    const result = await loadServerInfo();
    expect(result).toEqual(SAMPLE);
    expect(mockFetchServerInfo).not.toHaveBeenCalled();
  });

  it("applyServerInfo primes the singleton + sessionStorage without a fetch", async () => {
    const { useServerInfo } = await importFresh();
    const { applyServerInfo, serverInfo, loadServerInfo } = useServerInfo();
    applyServerInfo(SAMPLE);
    expect(serverInfo.value).toEqual(SAMPLE);
    expect(JSON.parse(memSession["daymug-server-info"])).toEqual(SAMPLE);
    // Subsequent load() resolves from cache without a network call.
    const result = await loadServerInfo();
    expect(result).toEqual(SAMPLE);
    expect(mockFetchServerInfo).not.toHaveBeenCalled();
  });

  it("dedupes concurrent loads to a single in-flight fetch", async () => {
    const { useServerInfo } = await importFresh();
    let resolveFetch: ((v: typeof SAMPLE) => void) | null = null;
    mockFetchServerInfo.mockReturnValueOnce(
      new Promise<typeof SAMPLE>((resolve) => {
        resolveFetch = resolve;
      }),
    );
    const { loadServerInfo } = useServerInfo();
    const p1 = loadServerInfo();
    const p2 = loadServerInfo();
    // Both callers should be waiting on the same in-flight promise.
    expect(mockFetchServerInfo).toHaveBeenCalledTimes(1);
    resolveFetch!(SAMPLE);
    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1).toEqual(SAMPLE);
    expect(r2).toEqual(SAMPLE);
  });
});
