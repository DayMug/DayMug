import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";

import AdminGlobalPauseTasks from "./AdminGlobalPauseTasks.vue";

const mockFetchPause = vi.fn();
const mockSetPause = vi.fn();

vi.mock("@/composables/useApi", () => ({
  adminFetchPause: (...args: unknown[]) => mockFetchPause(...args),
  adminSetPause: (...args: unknown[]) => mockSetPause(...args),
}));

beforeEach(() => {
  vi.clearAllMocks();
  mockFetchPause.mockResolvedValue({ paused: false });
});

async function mountPanel() {
  const wrapper = mount(AdminGlobalPauseTasks);
  await flushPromises();
  return wrapper;
}

function toggle(wrapper: Awaited<ReturnType<typeof mountPanel>>) {
  return wrapper.find('[data-testid="pause-new-tasks-switch"]');
}

describe("AdminGlobalPauseTasks", () => {
  it("reflects the server state fetched on mount", async () => {
    mockFetchPause.mockResolvedValueOnce({ paused: true });
    const wrapper = await mountPanel();
    expect(wrapper.text()).toContain("Paused");
  });

  it("starts from running when the server reports no pause", async () => {
    const wrapper = await mountPanel();
    expect(wrapper.text()).toContain("Running");
  });

  it("sends the requested state and adopts what the server confirms", async () => {
    mockSetPause.mockResolvedValueOnce({ paused: true });
    const wrapper = await mountPanel();

    await toggle(wrapper).trigger("click");
    await flushPromises();

    expect(mockSetPause).toHaveBeenCalledWith(true);
    expect(wrapper.text()).toContain("Paused");
  });

  // The switch must not drift from the server: a rejected PUT leaves the panel
  // showing what is actually in effect, not what the operator asked for.
  it("keeps the previous state when the toggle fails", async () => {
    mockSetPause.mockRejectedValueOnce(new Error("set pause: 500"));
    const wrapper = await mountPanel();

    await toggle(wrapper).trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("Running");
    expect(wrapper.text()).toContain("set pause: 500");
  });

  it("reports how many conversations restarted on resume", async () => {
    mockFetchPause.mockResolvedValueOnce({ paused: true });
    mockSetPause.mockResolvedValueOnce({ paused: false, resumed_conversations: 3 });
    const wrapper = await mountPanel();

    await toggle(wrapper).trigger("click");
    await flushPromises();

    expect(mockSetPause).toHaveBeenCalledWith(false);
    expect(wrapper.text()).toContain("Resumed 3");
  });

  it("stays silent about resumed work when nothing was queued", async () => {
    mockFetchPause.mockResolvedValueOnce({ paused: true });
    mockSetPause.mockResolvedValueOnce({ paused: false, resumed_conversations: 0 });
    const wrapper = await mountPanel();

    await toggle(wrapper).trigger("click");
    await flushPromises();

    expect(wrapper.text()).not.toContain("Resumed");
  });

  // A panel that cannot read the state must not offer to write it.
  it("disables the switch when the initial fetch fails", async () => {
    mockFetchPause.mockRejectedValueOnce(new Error("pause state: 500"));
    const wrapper = await mountPanel();

    await toggle(wrapper).trigger("click");
    await flushPromises();

    expect(mockSetPause).not.toHaveBeenCalled();
  });
});
