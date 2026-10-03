import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";

import AdminGlobalPricing from "./AdminGlobalPricing.vue";

const mockFetchPricing = vi.fn();
const mockSavePricing = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminFetchPricing: (...args: unknown[]) => mockFetchPricing(...args),
  adminSavePricing: (...args: unknown[]) => mockSavePricing(...args),
}));

const codexPricing = {
  provider: "codex",
  models: ["gpt-5"],
  rates: { "gpt-5": { input: 1.25, output: 10 } },
  defaults: { "gpt-5": { input: 1.25, output: 10 } },
};

const claudeCompatPricing = {
  provider: "claude-compatible",
  models: ["glm-5"],
  rates: { "glm-5": { input: 3, output: 15 } },
  defaults: { "glm-5": { input: 3, output: 15 } },
};

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchPricing.mockResolvedValue(codexPricing);
});

async function mountPricing() {
  const wrapper = mount(AdminGlobalPricing);
  await flushPromises();
  return wrapper;
}

function findButton(wrapper: Awaited<ReturnType<typeof mountPricing>>, text: string) {
  return wrapper.findAll("button").find((b) => b.text().includes(text));
}

describe("AdminGlobalPricing", () => {
  it("loads the codex table on mount", async () => {
    const wrapper = await mountPricing();
    expect(mockFetchPricing).toHaveBeenCalledWith("codex");
    expect(wrapper.text()).toContain("gpt-5");
  });

  it("refetches when the admin switches provider tab", async () => {
    const wrapper = await mountPricing();
    mockFetchPricing.mockResolvedValueOnce(claudeCompatPricing);

    await findButton(wrapper, "claude-compatible")?.trigger("click");
    await flushPromises();

    expect(mockFetchPricing).toHaveBeenLastCalledWith("claude-compatible");
    expect(wrapper.text()).toContain("glm-5");
  });

  it("hides the cache-write columns for codex and shows them for claude-compatible", async () => {
    const wrapper = await mountPricing();
    // codex: model + input + cached input + output
    expect(wrapper.findAll("thead th")).toHaveLength(4);

    mockFetchPricing.mockResolvedValueOnce(claudeCompatPricing);
    await findButton(wrapper, "claude-compatible")?.trigger("click");
    await flushPromises();

    expect(wrapper.findAll("thead th")).toHaveLength(6);
  });

  it("keeps Save disabled until a cell diverges from the server value", async () => {
    const wrapper = await mountPricing();
    expect(findButton(wrapper, "Save")?.attributes("disabled")).toBeDefined();

    await wrapper.findAll('input[type="number"]')[0].setValue("2.5");
    expect(findButton(wrapper, "Save")?.attributes("disabled")).toBeUndefined();
  });

  it("treats a blank optional cell as 'no override' rather than a change", async () => {
    mockFetchPricing.mockResolvedValueOnce({
      ...codexPricing,
      rates: { "gpt-5": { input: 1.25, output: 10, cached_input: 0 } },
    });
    const wrapper = await mountPricing();
    expect(findButton(wrapper, "Save")?.attributes("disabled")).toBeDefined();
  });

  it("omits zeroed optional fields from the saved payload", async () => {
    const wrapper = await mountPricing();
    mockSavePricing.mockResolvedValueOnce(codexPricing);

    await wrapper.findAll('input[type="number"]')[0].setValue("2");
    await findButton(wrapper, "Save")?.trigger("click");
    await flushPromises();

    expect(mockSavePricing).toHaveBeenCalledWith("codex", {
      "gpt-5": { input: 2, output: 10 },
    });
  });

  it("rejects a negative rate before hitting the server", async () => {
    const wrapper = await mountPricing();

    await wrapper.findAll('input[type="number"]')[0].setValue("-1");
    await findButton(wrapper, "Save")?.trigger("click");
    await flushPromises();

    expect(mockSavePricing).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("Rates must be non-negative.");
  });

  it("sends an empty override map when resetting to defaults", async () => {
    const wrapper = await mountPricing();
    mockSavePricing.mockResolvedValueOnce(codexPricing);

    await findButton(wrapper, "Reset to defaults")?.trigger("click");
    await flushPromises();

    expect(mockSavePricing).toHaveBeenCalledWith("codex", {});
    expect(wrapper.text()).toContain("Reverted to compile-time defaults.");
  });

  // Tab clicks are not serialised, so two loads can be in flight at once.
  // Whichever response lands last used to win the table while the tab
  // highlight showed the last *clicked* provider — and Save then wrote the
  // visible model keys into the other provider's rate table.
  it("keeps the table and the save target on the last-clicked provider", async () => {
    const wrapper = await mountPricing();

    // claude-compatible's fetch is held open so codex's (queued after it) resolves first.
    let releaseCompat: (v: unknown) => void = () => {};
    mockFetchPricing.mockImplementationOnce(
      () => new Promise((resolve) => (releaseCompat = resolve)),
    );
    await findButton(wrapper, "claude-compatible")?.trigger("click");

    mockFetchPricing.mockResolvedValueOnce(codexPricing);
    await findButton(wrapper, "codex")?.trigger("click");
    await flushPromises();

    releaseCompat(claudeCompatPricing);
    await flushPromises();

    // The superseded claude-compatible response must not repopulate the table.
    expect(wrapper.text()).toContain("gpt-5");
    expect(wrapper.text()).not.toContain("glm-5");

    mockSavePricing.mockResolvedValueOnce(codexPricing);
    await wrapper.findAll('input[type="number"]')[0].setValue("2");
    await findButton(wrapper, "Save")?.trigger("click");
    await flushPromises();

    expect(mockSavePricing).toHaveBeenCalledWith("codex", {
      "gpt-5": { input: 2, output: 10 },
    });
  });

  // Even a single in-flight switch is dangerous: until it lands, the tab
  // says claude-compatible while the table still lists codex's models.
  it("blanks the table while a provider switch is in flight", async () => {
    const wrapper = await mountPricing();
    expect(wrapper.text()).toContain("gpt-5");

    let releaseCompat: (v: unknown) => void = () => {};
    mockFetchPricing.mockImplementationOnce(
      () => new Promise((resolve) => (releaseCompat = resolve)),
    );
    await findButton(wrapper, "claude-compatible")?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).not.toContain("gpt-5");
    expect(wrapper.findAll('input[type="number"]')).toHaveLength(0);

    releaseCompat(claudeCompatPricing);
    await flushPromises();
    expect(wrapper.text()).toContain("glm-5");
  });

  it("surfaces a failed load without rendering a half-populated table", async () => {
    mockFetchPricing.mockRejectedValueOnce(new Error("pricing unavailable"));
    const wrapper = await mountPricing();

    expect(wrapper.text()).toContain("pricing unavailable");
    expect(wrapper.findAll('input[type="number"]')).toHaveLength(0);
  });
});
