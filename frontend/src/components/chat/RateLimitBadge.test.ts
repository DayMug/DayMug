import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { mount } from "@vue/test-utils";
import RateLimitBadge from "./RateLimitBadge.vue";
import { i18n } from "@/i18n";
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

  it("tooltip spells out the window's reset time and status", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour() } },
    });
    const label = wrapper.find('[data-testid="rate-limit-badge"]').attributes("aria-label") || "";
    expect(label).toMatch(/^5H: resets in 1h 30m \(at .+\) · within limit$/);
  });

  it("puts the provider status into words and passes unknown codes through", () => {
    const cases: [string, string][] = [
      ["allowed_warning", "near limit"],
      ["rejected", "limit reached"],
      ["throttled", "throttled"],
    ];
    for (const [status, text] of cases) {
      const wrapper = mount(RateLimitBadge, {
        props: { rateLimits: { five_hour: fiveHour({ status }) } },
      });
      const label = wrapper.find('[data-testid="rate-limit-badge"]').attributes("aria-label") || "";
      expect(label).toMatch(new RegExp(`· ${text}$`));
    }
  });

  it("speaks the active UI language", () => {
    i18n.global.locale.value = "zh";
    const passthrough = { template: "<div><slot /></div>" };
    const wrapper = mount(RateLimitBadge, {
      props: {
        rateLimits: {
          five_hour: fiveHour({ utilization: 38, observed_at: NOW_SECONDS - 25 * 60 }),
          seven_day: sevenDay({ status: undefined }),
        },
      },
      global: {
        stubs: {
          TooltipProvider: passthrough,
          Tooltip: passthrough,
          TooltipTrigger: passthrough,
          TooltipContent: passthrough,
        },
      },
    });
    const text = wrapper.text();
    expect(text).toContain("用量");
    expect(text).toMatch(/1 小时 30 分后重置（2023\/11\/1\d.+） · 未超限/);
    expect(text).toContain("3 天 0 小时后重置");
    expect(text).toContain("25 分 0 秒前更新");
    expect(text).not.toMatch(/resets in|Updated|Usage/);
  });

  it("describes spent quota in words for screen readers", () => {
    const wrapper = mount(RateLimitBadge, {
      props: {
        rateLimits: {
          five_hour: fiveHour({ utilization: 38 }),
          seven_day: sevenDay({ utilization: 4.5, status: undefined }),
        },
      },
    });
    const label = wrapper.find('[data-testid="rate-limit-badge"]').attributes("aria-label") || "";
    expect(label).toContain("5H: 38% used, resets in 1h 30m");
    expect(label).toContain("7D: 5% used, resets in 3d 0h");
  });

  it("tooltip draws spent quota as a bar under a Usage heading without printing the number", () => {
    const passthrough = { template: "<div><slot /></div>" };
    const wrapper = mount(RateLimitBadge, {
      props: {
        rateLimits: {
          five_hour: fiveHour({ utilization: 85 }),
          seven_day: sevenDay({ status: undefined }),
        },
      },
      global: {
        stubs: {
          TooltipProvider: passthrough,
          Tooltip: passthrough,
          TooltipTrigger: passthrough,
          TooltipContent: passthrough,
        },
      },
    });
    const rows = wrapper.findAll('[data-testid="rate-limit-row"]');
    expect(rows).toHaveLength(2);
    const fill = rows[0].find('[data-testid="rate-limit-usage-bar"] > div');
    expect(fill.attributes("style")).toContain("width: 85%");
    // Nearly spent: the bar turns amber.
    expect(fill.classes()).toContain("bg-amber-400");
    expect(rows[0].text()).not.toMatch(/\d+(\.\d+)?%/);
    expect(wrapper.text()).toContain("Usage");
    // No usage figure for this window → no bar, just the reset time.
    expect(rows[1].find('[data-testid="rate-limit-usage-bar"]').exists()).toBe(false);
    expect(rows[1].text()).toContain("resets in 3d 0h");
  });

  it("says how old a cached reading is", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour({ observed_at: NOW_SECONDS - 25 * 60 }) } },
    });
    const label = wrapper.find('[data-testid="rate-limit-badge"]').attributes("aria-label") || "";
    expect(label).toContain("Updated 25m 0s ago");
  });

  it("warns from the usage percentage when there is no status", () => {
    const wrapper = mount(RateLimitBadge, {
      props: { rateLimits: { five_hour: fiveHour({ status: undefined, utilization: 85 }) } },
    });
    expect(wrapper.html()).toMatch(/text-amber/);
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
