import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";

import ScheduledPromptsArea from "./ScheduledPromptsArea.vue";

describe("ScheduledPromptsArea", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2030, 4, 1, 14, 0, 0));
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders nothing when no prompt is scheduled", () => {
    const wrapper = mount(ScheduledPromptsArea, { props: { prompts: [] } });
    expect(wrapper.find("[data-testid='scheduled-prompts-area']").exists()).toBe(false);
  });

  it("shows when each prompt sends and how long is left", () => {
    const wrapper = mount(ScheduledPromptsArea, {
      props: {
        prompts: [
          {
            id: "s1",
            content: "status report",
            attachments: [{ name: "log.txt", mime: "text/plain", path: "p/log.txt", url: "/u" }],
            sendAt: new Date(2030, 4, 1, 14, 30).toISOString(),
          },
        ],
      },
    });
    expect(wrapper.find("[data-testid='scheduled-prompt-content']").text()).toBe("status report");
    expect(wrapper.find("[data-testid='scheduled-prompt-file']").text()).toContain("log.txt");
    expect(wrapper.find("[data-testid='scheduled-prompt-time']").text()).toContain("in 30 min.");
  });

  it("emits cancel with the prompt id", async () => {
    const wrapper = mount(ScheduledPromptsArea, {
      props: { prompts: [{ id: "s9", content: "x", sendAt: new Date(2030, 4, 2).toISOString() }] },
    });
    await wrapper.find("[data-testid='scheduled-prompt-cancel']").trigger("click");
    expect(wrapper.emitted("cancel")?.[0]).toEqual(["s9"]);
  });
});
