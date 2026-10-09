import { afterEach, describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import ChatToolGroup from "./ChatToolGroup.vue";

interface TestToolMsg {
  role: string;
  content: string;
  activityType: string;
  subagent: boolean;
  toolStartedAt?: number;
  toolDurationMs?: number;
  toolCompleted?: boolean;
}

function makeTool(
  key: string,
  content: string,
  subagent = false,
  timing: Pick<TestToolMsg, "toolStartedAt" | "toolDurationMs" | "toolCompleted"> = {},
): { key: string; msg: TestToolMsg } {
  return {
    key,
    msg: { role: "activity", content, activityType: "tool", subagent, ...timing },
  };
}

describe("ChatToolGroup", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("collapsed: shows tool count and a name summary, hides per-tool detail", () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Read]\n  file: /tmp/a"), makeTool("m2", "[Bash]\n  cmd: ls")],
        expanded: false,
        subagent: false,
      },
    });
    const header = wrapper.find(".tool-group-header");
    expect(header.text()).toContain("Used 2 tools");
    expect(wrapper.find(".tool-group-summary").text()).toContain("Read");
    expect(wrapper.find(".tool-group-summary").text()).toContain("Bash");
    // Body (per-tool details) only renders when expanded.
    expect(wrapper.find(".tool-group-body").exists()).toBe(false);
    expect(wrapper.find(".tool-detail").exists()).toBe(false);
    expect(wrapper.find(".tool-group-surface").classes()).not.toContain("border");
  });

  it("expanded: renders each tool's header and detail", () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Read]\n  file: /tmp/a"), makeTool("m2", "[Bash]\n  cmd: ls")],
        expanded: true,
        subagent: false,
      },
    });
    expect(wrapper.find(".tool-group-body").exists()).toBe(true);
    const headers = wrapper.findAll(".tool-header").map((n) => n.text());
    expect(headers).toEqual(["[Read]", "[Bash]"]);
    const details = wrapper.findAll(".tool-detail").map((n) => n.text());
    expect(details[0]).toContain("file: /tmp/a");
    expect(details[1]).toContain("cmd: ls");
    expect(wrapper.find(".tool-group-summary").text()).toContain("Used 2 tools");
  });

  it("increments a running tool every second and freezes the completed duration", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-24T10:00:00.000Z"));
    const startedAt = Date.now();
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Bash] calling...", false, { toolStartedAt: startedAt })],
        expanded: true,
        subagent: false,
      },
    });

    expect(wrapper.find(".tool-duration").text()).toBe("Elapsed 0s");
    await vi.advanceTimersByTimeAsync(2000);
    expect(wrapper.find(".tool-duration").text()).toBe("Elapsed 2s");

    await wrapper.setProps({
      tools: [
        makeTool("m1", "[Bash]\n  command: sleep 2", false, {
          toolStartedAt: startedAt,
          toolDurationMs: 2400,
          toolCompleted: true,
        }),
      ],
    });
    expect(wrapper.find(".tool-duration").text()).toBe("Took 2s");
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("shows elapsed time in the collapsed header every second and stops on completion", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-30T10:00:00.000Z"));
    const startedAt = Date.now();
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Bash]", false, { toolStartedAt: startedAt })],
        expanded: false,
        subagent: false,
      },
    });

    expect(wrapper.find(".tool-group-body").exists()).toBe(false);
    expect(wrapper.find(".tool-group-duration").text()).toBe("Elapsed 0s");
    await vi.advanceTimersByTimeAsync(1000);
    expect(wrapper.find(".tool-group-duration").text()).toBe("Elapsed 1s");
    await vi.advanceTimersByTimeAsync(1000);
    expect(wrapper.find(".tool-group-duration").text()).toBe("Elapsed 2s");

    await wrapper.setProps({
      tools: [
        makeTool("m1", "[Bash]", false, {
          toolStartedAt: startedAt,
          toolDurationMs: 2400,
          toolCompleted: true,
        }),
      ],
    });
    expect(wrapper.find(".tool-group-duration").text()).toBe("Took 2s");
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(3000);
    expect(wrapper.find(".tool-group-duration").text()).toBe("Took 2s");
    wrapper.unmount();
  });

  it("shows total tool duration in the summary and individual durations when expanded", async () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [
          makeTool("m1", "[Read]", false, { toolDurationMs: 1200, toolCompleted: true }),
          makeTool("m2", "[Bash]", false, { toolDurationMs: 61000, toolCompleted: true }),
        ],
        expanded: false,
        subagent: false,
      },
    });
    expect(wrapper.find(".tool-group-duration").text()).toBe("Took 1m 02s");
    await wrapper.setProps({ expanded: true });
    expect(wrapper.findAll(".tool-duration").map((node) => node.text())).toEqual([
      "Took 1s",
      "Took 1m 01s",
    ]);
    wrapper.unmount();
  });

  it("counts the gap between sequential tools in the summary span", () => {
    const t0 = Date.parse("2026-09-30T10:00:00.000Z");
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [
          makeTool("m1", "[Read]", false, {
            toolStartedAt: t0,
            toolDurationMs: 1000,
            toolCompleted: true,
          }),
          makeTool("m2", "[Bash]", false, {
            toolStartedAt: t0 + 8000,
            toolDurationMs: 2000,
            toolCompleted: true,
          }),
        ],
        expanded: false,
        subagent: false,
      },
    });
    expect(wrapper.find(".tool-group-duration").text()).toBe("Took 10s");
    wrapper.unmount();
  });

  it("does not double-count parallel tools in the summary span", () => {
    const t0 = Date.parse("2026-09-30T10:00:00.000Z");
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [
          makeTool("m1", "[Bash]", false, {
            toolStartedAt: t0,
            toolDurationMs: 5000,
            toolCompleted: true,
          }),
          makeTool("m2", "[Bash]", false, {
            toolStartedAt: t0 + 100,
            toolDurationMs: 4000,
            toolCompleted: true,
          }),
        ],
        expanded: false,
        subagent: false,
      },
    });
    expect(wrapper.find(".tool-group-duration").text()).toBe("Took 5s");
    wrapper.unmount();
  });

  it("measures a running group from the first tool's start", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-09-30T10:00:10.000Z"));
    const t0 = Date.parse("2026-09-30T10:00:00.000Z");
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [
          makeTool("m1", "[Read]", false, {
            toolStartedAt: t0,
            toolDurationMs: 1000,
            toolCompleted: true,
          }),
          makeTool("m2", "[Bash]", false, { toolStartedAt: t0 + 9000 }),
        ],
        expanded: false,
        subagent: false,
      },
    });
    expect(wrapper.find(".tool-group-duration").text()).toBe("Elapsed 10s");
    wrapper.unmount();
  });

  it("omits summary duration for older tools without timing metadata", () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Bash]", false, { toolCompleted: true })],
        expanded: false,
        subagent: false,
      },
    });
    expect(wrapper.find(".tool-group-duration").exists()).toBe(false);
    wrapper.unmount();
  });

  it("cleans up the running timer when the tool group is unmounted", () => {
    vi.useFakeTimers();
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Bash]", false, { toolStartedAt: Date.now() })],
        expanded: false,
        subagent: false,
      },
    });
    expect(vi.getTimerCount()).toBe(1);
    wrapper.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("toggle button click emits 'toggle'", async () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Read]\n  file: /tmp/a")],
        expanded: false,
        subagent: false,
      },
    });
    await wrapper.find(".tool-group-header").trigger("click");
    expect(wrapper.emitted("toggle")).toBeTruthy();
    expect(wrapper.emitted("toggle")?.length).toBe(1);
  });

  it("dedupes repeated tool names in the collapsed summary", () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [
          makeTool("m1", "[Read]\n  file: /a"),
          makeTool("m2", "[Read]\n  file: /b"),
          makeTool("m3", "[Read]\n  file: /c"),
        ],
        expanded: false,
        subagent: false,
      },
    });
    // The summary should mention Read once but the count should still be 3.
    const summary = wrapper.find(".tool-group-summary").text();
    expect(summary.match(/Read/g)?.length).toBe(1);
    expect(wrapper.find(".tool-group-header").text()).toContain("Used 3 tools");
  });

  it("subagent groups render on the indented track", () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Read]\n  file: /a", true)],
        expanded: false,
        subagent: true,
      },
    });
    expect(wrapper.find(".subagent-track").exists()).toBe(true);
    expect(wrapper.find('[data-subagent="true"]').exists()).toBe(true);
  });

  it("non-subagent groups do not get the indented track", () => {
    const wrapper = mount(ChatToolGroup, {
      props: {
        tools: [makeTool("m1", "[Read]\n  file: /a")],
        expanded: false,
        subagent: false,
      },
    });
    expect(wrapper.find(".subagent-track").exists()).toBe(false);
    expect(wrapper.find('[data-subagent="true"]').exists()).toBe(false);
  });
});
