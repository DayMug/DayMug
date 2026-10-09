import { describe, expect, it } from "vitest";

import { hasModelReplied } from "./conversationStarted";

const failedUsage = { short: "", detail: "", input: 0, output: 0 };
const realUsage = { short: "", detail: "", input: 12, output: 40 };

describe("hasModelReplied", () => {
  it("is false for prompts and errors alone", () => {
    expect(hasModelReplied([])).toBe(false);
    expect(hasModelReplied([{ role: "user" }, { role: "error" }])).toBe(false);
  });

  it("ignores the CLI's zero-usage failure text", () => {
    expect(
      hasModelReplied([
        { role: "user" },
        { role: "assistant", usage: failedUsage },
        { role: "error" },
      ]),
    ).toBe(false);
  });

  it("is true once the model answers", () => {
    expect(hasModelReplied([{ role: "user" }, { role: "assistant", usage: realUsage }])).toBe(true);
  });

  it("treats a reply without usage as an answer", () => {
    expect(hasModelReplied([{ role: "user" }, { role: "assistant" }])).toBe(true);
  });

  it("counts the main agent's tool calls but not other activity", () => {
    expect(hasModelReplied([{ role: "activity", activityType: "tool" }])).toBe(true);
    expect(hasModelReplied([{ role: "activity", activityType: "tool", subagent: true }])).toBe(
      false,
    );
    expect(hasModelReplied([{ role: "activity", activityType: "info" }])).toBe(false);
  });
});
