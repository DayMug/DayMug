import { i18n } from "@/i18n";

// formatToolInput renders a stream-json tool input payload as a readable
// "  key: value" listing for the activity feed. Long values are truncated.
// Falls back to the raw string when the payload isn't valid JSON.
export function formatToolInput(jsonStr: string): string {
  try {
    const obj = JSON.parse(jsonStr);
    const parts: string[] = [];
    for (const [key, value] of Object.entries(obj)) {
      const v = typeof value === "string" ? value : JSON.stringify(value);
      const display = v.length > 80 ? v.slice(0, 80) + "..." : v;
      parts.push(`  ${key}: ${display}`);
    }
    return parts.join("\n");
  } catch {
    return jsonStr.length > 200 ? jsonStr.slice(0, 200) + "..." : jsonStr;
  }
}

// formatUsage renders a stream-json usage payload as a tight one-line
// summary for the activity feed. Abbreviations keep the strip readable
// in the chat reading column even on narrow viewports:
//   I/O = input / output  CR = cache read  CW = cache write
// The short string is what shows on the chip; `detail` is a multi-line
// breakdown the renderer drops onto the element's `title` attribute so
// hovering surfaces the un-abbreviated names plus the 5m / 1h cache
// TTL split (1h writes price ~2x the 5m rate, so the breakdown is the
// single most useful hint when a turn's cost looks "off"). Callers that
// only want the chip text can read `.short`.
export interface FormattedUsage {
  short: string;
  detail: string;
  // Raw counts pulled off the same payload so callers that need numbers
  // (the `/cost` slash command's "Last turn — input / output" summary)
  // can read them without a second JSON.parse. Undefined when the
  // payload was malformed (formatUsage returns the raw text as `short`
  // in that case) or genuinely omitted those fields.
  input?: number;
  output?: number;
}

// compact shortens raw token counts to a chip-friendly K/M form so the
// strip stays scannable even on long-running turns: 9_125_693 → "9.1M",
// 15_272 → "15.3K", 7_000 → "7K", 999 → "999". Trailing ".0" is trimmed
// so round figures don't pick up a noisy decimal. The full number is
// always available in the hover tooltip via the `detail` line.
function compact(n: number): string {
  if (n < 1000) return String(n);
  const trim = (s: string) => s.replace(/\.0$/, "");
  if (n < 1_000_000) return trim((n / 1000).toFixed(1)) + "K";
  return trim((n / 1_000_000).toFixed(1)) + "M";
}

const t = i18n.global.t;
const usd = (n: number) => `$${n.toFixed(4)}`;

export function formatUsage(content: string): FormattedUsage {
  try {
    const u = JSON.parse(content);
    const parts: string[] = [];
    if (u.input_tokens != null || u.output_tokens != null) {
      parts.push(
        t("usageChip.io", {
          input: compact(u.input_tokens ?? 0),
          output: compact(u.output_tokens ?? 0),
        }),
      );
    }
    if (u.cache_read_input_tokens) {
      parts.push(t("usageChip.cacheRead", { n: compact(u.cache_read_input_tokens) }));
    }
    if (u.cache_creation_input_tokens) {
      parts.push(t("usageChip.cacheWrite", { n: compact(u.cache_creation_input_tokens) }));
    }
    // Rows written before the backend stamped turn_cost_usd only carry the
    // provider's own figure, which for Claude is the session's running total.
    if (u.turn_cost_usd != null) {
      parts.push(t("usageChip.cost", { cost: usd(u.turn_cost_usd) }));
      if (u.session_cost_usd != null) {
        parts.push(t("usageChip.total", { cost: usd(u.session_cost_usd) }));
      }
    } else if (u.total_cost_usd != null) {
      parts.push(t("usageChip.cost", { cost: usd(u.total_cost_usd) }));
    }
    if (u.num_turns) parts.push(t("usageChip.turns", { n: u.num_turns }));
    const short = parts.join(" | ");

    // Detail tooltip: expand abbreviations and surface the cache TTL
    // split when the CLI provided it. Numbers go through toLocaleString
    // so 165418 reads as "165,418" — much easier to scan in the
    // hover than the raw digits we keep in the chip.
    const fmt = (n: number) => n.toLocaleString("en-US");
    const lines: string[] = [];
    if (u.input_tokens != null) lines.push(t("usageChip.inputTokens", { n: fmt(u.input_tokens) }));
    if (u.output_tokens != null) {
      lines.push(t("usageChip.outputTokens", { n: fmt(u.output_tokens) }));
    }
    if (u.cache_read_input_tokens != null) {
      lines.push(t("usageChip.cacheReadDetail", { n: fmt(u.cache_read_input_tokens) }));
    }
    if (u.cache_creation_input_tokens != null) {
      lines.push(t("usageChip.cacheWriteDetail", { n: fmt(u.cache_creation_input_tokens) }));
      const cc = u.cache_creation;
      if (cc) {
        if (cc.ephemeral_5m_input_tokens != null) {
          lines.push(`  • ${t("usageChip.ttl5m", { n: fmt(cc.ephemeral_5m_input_tokens) })}`);
        }
        if (cc.ephemeral_1h_input_tokens != null) {
          lines.push(`  • ${t("usageChip.ttl1h", { n: fmt(cc.ephemeral_1h_input_tokens) })}`);
        }
      }
    }
    if (u.turn_cost_usd != null)
      lines.push(t("usageChip.thisTurn", { cost: usd(u.turn_cost_usd) }));
    if (u.session_cost_usd != null) {
      lines.push(t("usageChip.conversationTotal", { cost: usd(u.session_cost_usd) }));
    }
    if (u.total_cost_usd != null) {
      lines.push(t("usageChip.cliCost", { cost: usd(u.total_cost_usd) }));
    }
    if (u.num_turns) lines.push(t("usageChip.internalTurns", { n: u.num_turns }));
    return {
      short,
      detail: lines.join("\n"),
      input: typeof u.input_tokens === "number" ? u.input_tokens : undefined,
      output: typeof u.output_tokens === "number" ? u.output_tokens : undefined,
    };
  } catch {
    return { short: content, detail: "" };
  }
}
