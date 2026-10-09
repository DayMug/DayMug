import { describe, expect, it } from "vitest";

import { i18n } from "@/i18n";
import { noticeText } from "./notice";

const guardrail = {
  kind: "task_guardrail",
  triggers: ["model_calls", "tool_calls"],
  model_calls: 108,
  tool_calls: 40,
  recorded_cost_usd: 1.5,
};

describe("noticeText", () => {
  it("renders a guardrail reminder in English", () => {
    expect(noticeText(guardrail)).toBe(
      "The previous message's task reached the reminder threshold for model requests, tool calls: 108 model requests, 40 tool calls. This conversation has recorded $1.5000 so far. This message will still run normally; to keep later context costs down, consider starting a new conversation.",
    );
  });

  it("renders a guardrail reminder in Chinese with the server's wording", () => {
    i18n.global.locale.value = "zh";
    expect(noticeText({ ...guardrail, recorded_cost_usd: undefined })).toBe(
      "上一条消息触发的任务已达到模型请求次数、工具调用次数提醒：模型请求 108 次，工具调用 40 次。该任务尚未返回完整成本用量。本条消息仍会正常执行；如需降低后续上下文成本，建议新建对话。",
    );
  });

  it("renders the retry delay in minutes when it is whole", () => {
    expect(noticeText({ kind: "transient_retry", delay_seconds: 180, max_attempts: 3 })).toContain(
      "in 3 min (up to 3 attempts)",
    );
    i18n.global.locale.value = "zh";
    expect(noticeText({ kind: "transient_retry", delay_seconds: 90, max_attempts: 3 })).toBe(
      "上游服务过载，90 秒后自动重试（最多 3 次）。期间无需操作，取消可随时中止。",
    );
  });

  it("falls back for unknown kinds", () => {
    expect(noticeText({ kind: "future_kind" })).toBeUndefined();
    expect(noticeText(undefined)).toBeUndefined();
  });
});
