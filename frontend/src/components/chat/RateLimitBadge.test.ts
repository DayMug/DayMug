import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import RateLimitBadge from "./RateLimitBadge.vue";
import type { RateLimitInfo } from "@/composables/useChat";

// A fixed wall-clock anchor so the "resets_at - now" math is deterministic.
const NOW_SECONDS = 1_700_000_000;

function fiveHour(overrides: Partial<RateLimitInfo> = {}): RateLimitInfo {
  return {
    type: "five_hour",
    status: "allowed",
    resets_at: NOW_SECONDS + 90 * 60, // 1h 30m from now
    ...overrides,
  };
}

function sevenDay(overrides: Partial<RateLimitInfo> = {}): RateLimitInfo {
  return {
    type: "seven_day",
    status: "allowed",
    resets_at: NOW_SECONDS + 3 * 24 * 3600,
    ...overrides,
  };
}

describe("RateLimitBadge", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(NOW_SECONDS * 1000));
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders nothing when no rate-limit info is available", () => {
    const wrapper = mount(RateLimitBadge, { props: { rateLimits: {} } });
    expect(wrapper.find('[data-testid="rate-limit-badge"]').exists()).toBe(false);
  });

  it("renders the H:M/window countdown when only five_hour is present", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour() } },
    });
    expect(wrapper.text()).toContain("1H:30M/5H");
  });

  it("falls back to 7d window when only seven_day has fired", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { seven_day: sevenDay() } },
    });
    // 3 full days = 72 hours, 0 minutes
    expect(wrapper.text()).toContain("72H:0M/7D");
  });

  it("uses the warning tone when status is warning", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour({ status: "warning" }) } },
    });
    // Status no longer leaks into the visible label — colour is the only signal.
    expect(wrapper.text()).toContain("1H:30M/5H");
    expect(wrapper.html()).toMatch(/text-amber/);
  });

  it("uses the danger tone when status is blocked", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour({ status: "blocked" }) } },
    });
    expect(wrapper.text()).toContain("1H:30M/5H");
    expect(wrapper.html()).toContain("text-destructive");
  });

  it("renders a one-sentence tooltip with reset time and status", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour() } },
    });
    const label = wrapper.find('[data-testid="rate-limit-badge"]').attributes("aria-label") || "";
    expect(label).toMatch(/^5H limit resets in 1h 30m \(at .+, status: allowed\)\.$/);
  });

  it("tacks the secondary window onto the same tooltip sentence", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour(), seven_day: sevenDay() } },
    });
    const label = wrapper.find('[data-testid="rate-limit-badge"]').attributes("aria-label") || "";
    expect(label).toContain("5H limit");
    expect(label).toContain("7D resets in");
    // Single sentence — no embedded newline.
    expect(label.includes("\n")).toBe(false);
  });

  it("renders 'RESET' when the reset moment has passed", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour({ resets_at: NOW_SECONDS - 5 }) } },
    });
    expect(wrapper.text()).toContain("RESET/5H");
  });

  it("ticks the countdown as time passes", async () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour({ resets_at: NOW_SECONDS + 75 }) } },
    });
    // Sub-hour: drop the H part so the badge stays narrow.
    expect(wrapper.text()).toContain("1M/5H");
    // advanceTimersByTimeAsync moves both the mocked clock AND fires the
    // interval, so 20s here = 20 ticks of the badge's per-second updater.
    await vi.advanceTimersByTimeAsync(20_000);
    // 75 - 20 = 55s remaining → sub-minute collapses to seconds.
    expect(wrapper.text()).toContain("55S/5H");
  });
});
