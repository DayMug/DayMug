import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import ContextUsageBar from "./ContextUsageBar.vue";

describe("ContextUsageBar", () => {
  it("renders percentage based on used/total", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 500, total: 1000 } });
    expect(wrapper.text()).toBe("50%");
  });

  it("renders 0% when total is 0", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 0, total: 0 } });
    expect(wrapper.text()).toBe("0%");
  });

  it("renders progress bar with correct value", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 750, total: 1000 } });
    // The shadcn Progress component uses a translateX style on the indicator
    const indicator = wrapper.find("[data-slot='progress-indicator']");
    expect(indicator.exists()).toBe(true);
    expect(indicator.attributes("style")).toContain("translateX(-25%)");
  });

  it("rounds percentage", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 333, total: 1000 } });
    expect(wrapper.text()).toBe("33%");
  });

  it("exposes a 'Context X%' tooltip on the wrapper", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 250, total: 1000 } });
    // The accessible label moved from inline text to aria-label when we
    // collapsed the bar to its compact editorial form.
    expect(wrapper.html()).toContain("Context 25% (250 / 1.0k)");
  });

  it("includes cache breakdown in tooltip when provided", () => {
    const wrapper = mount(ContextUsageBar, {
      props: {
        used: 50000,
        total: 200000,
        inputTokens: 300,
        cacheRead: 49000,
        cacheCreation: 700,
      },
    });
    const title = wrapper.find("div[aria-label]").attributes("aria-label") || "";
    expect(title).toContain("Context 25%");
    expect(title).toContain("Input: 300");
    expect(title).toContain("Cache read: 49k");
    expect(title).toContain("Cache write: 700");
  });

  it("omits cache lines when those props are absent or zero", () => {
    const wrapper = mount(ContextUsageBar, {
      props: { used: 100, total: 1000, cacheRead: 0, cacheCreation: 0 },
    });
    const title = wrapper.find("div[aria-label]").attributes("aria-label") || "";
    expect(title).not.toContain("Cache read");
    expect(title).not.toContain("Cache write");
  });

  it("leads the tooltip with the per-turn usage summary when present", () => {
    const wrapper = mount(ContextUsageBar, {
      props: {
        used: 50000,
        total: 200000,
        usageSummary: "I/O: 9/2.4K | CR: 249K | CW: 6K | Cost: $0.2234 | Turns: 4",
      },
    });
    const title = wrapper.find("div[aria-label]").attributes("aria-label") || "";
    expect(title.startsWith("I/O: 9/2.4K | CR: 249K | CW: 6K | Cost: $0.2234 | Turns: 4")).toBe(
      true,
    );
    expect(title).toContain("Context 25%");
  });

  it("marks the pricing breakpoint on the progress bar", () => {
    const wrapper = mount(ContextUsageBar, {
      props: { used: 280000, total: 400000, priceBreakpoint: 272000 },
    });
    const marker = wrapper.find("span[aria-hidden='true']");
    expect(marker.exists()).toBe(true);
    expect(marker.attributes("style")).toContain("left: 68%");
    expect(wrapper.find("div[aria-label]").attributes("aria-label")).toContain("272k");
  });

  it("does not render a pricing breakpoint without an opt-in threshold", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 280000, total: 400000 } });
    expect(wrapper.find("span[aria-hidden='true']").exists()).toBe(false);
  });

  // The percentage carries the whole message; the 80px track is decoration a
  // phone-width composer cannot afford if the controls and the send button are
  // to share one line.
  it("drops the progress track in a narrow container but keeps the percentage", () => {
    const wrapper = mount(ContextUsageBar, { props: { used: 50000, total: 200000 } });
    const track = wrapper.find("div.w-20");
    expect(track.classes()).toContain("@max-sm:hidden");
    expect(wrapper.text()).toContain("25%");
  });
});
