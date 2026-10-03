import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import UsageCompositionChart from "./UsageCompositionChart.vue";

describe("UsageCompositionChart", () => {
  it("shows exact, compact, percentage, and cost contribution in its tooltip", async () => {
    const wrapper = mount(UsageCompositionChart, {
      props: {
        points: [
          {
            key: "day",
            label: "2026-09-24",
            input_tokens: 1_250_000,
            cache_read_input_tokens: 2_500_000,
            cache_creation_input_tokens: 250_000,
            output_tokens: 1_000_000,
            reasoning_output_tokens: 100_000,
            cost_usd: 10,
          },
        ],
      },
    });
    await wrapper.find("svg g[tabindex='0']").trigger("mouseenter");
    const tooltip = wrapper.get('[data-testid="usage-tooltip"]').text();
    expect(tooltip).toContain("1,250,000");
    expect(tooltip).toContain("1.3M");
    expect(tooltip).toContain("25.0%");
    expect(tooltip).toContain("$2.50");
    expect(tooltip).toContain("Reasoning output");
  });
});
