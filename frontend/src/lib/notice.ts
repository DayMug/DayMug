import { i18n } from "@/i18n";
import type { TranscriptNotice } from "@/lib/wsProtocol";

const t = i18n.global.t;

const GUARDRAIL_TRIGGER_KEYS: Record<string, string> = {
  model_calls: "notices.triggers.modelCalls",
  tool_calls: "notices.triggers.toolCalls",
  cost: "notices.triggers.cost",
};

function formatDelay(seconds: number): string {
  if (seconds >= 60 && seconds % 60 === 0) return t("notices.minutes", { n: seconds / 60 });
  return t("notices.seconds", { n: seconds });
}

// noticeText renders a server-authored notice in the active locale. Returns
// undefined for an unknown kind so the caller falls back to the row's content
// (a ready-made sentence the server keeps for IM and older clients).
export function noticeText(notice: TranscriptNotice | undefined): string | undefined {
  switch (notice?.kind) {
    case "task_guardrail": {
      const triggers = (notice.triggers ?? [])
        .map((code) => GUARDRAIL_TRIGGER_KEYS[code])
        .filter((key): key is string => !!key)
        .map((key) => t(key))
        .join(t("notices.triggerSeparator"));
      const cost = notice.recorded_cost_usd
        ? t("notices.guardrailCost", { cost: `$${notice.recorded_cost_usd.toFixed(4)}` })
        : t("notices.guardrailCostUnknown");
      return t("notices.guardrail", {
        triggers,
        modelCalls: notice.model_calls ?? 0,
        toolCalls: notice.tool_calls ?? 0,
        cost,
      });
    }
    case "transient_retry":
      return t("notices.transientRetry", {
        delay: formatDelay(notice.delay_seconds ?? 0),
        attempts: notice.max_attempts ?? 0,
      });
    default:
      return undefined;
  }
}
