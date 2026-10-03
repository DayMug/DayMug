import { describe, it, expect } from "vitest";
import { formatUsage } from "./chatFormat";

describe("formatUsage", () => {
  it("returns the abbreviated short line with K/M-compacted counts for the chip itself", () => {
    // The chip stays scannable even when token counts run into the
    // millions; the precise integer is always available in the hover
    // tooltip via the `detail` field.
    const { short } = formatUsage(
      JSON.stringify({
        input_tokens: 6,
        output_tokens: 1486,
        cache_read_input_tokens: 9125693,
        cache_creation_input_tokens: 15272,
        total_cost_usd: 0.1814,
        num_turns: 1,
      }),
    );
    expect(short).toBe("I/O: 6/1.5K | CR: 9.1M | CW: 15.3K | Cost: $0.1814 | Turns: 1");
  });

  it("shows the turn's billed cost and the conversation total instead of the provider's running figure", () => {
    const { short, detail } = formatUsage(
      JSON.stringify({
        input_tokens: 4,
        output_tokens: 104,
        total_cost_usd: 1.3747,
        turn_cost_usd: 0.0259,
        session_cost_usd: 1.3747,
      }),
    );
    expect(short).toBe("I/O: 4/104 | Cost: $0.0259 | Total: $1.3747");
    expect(detail).toContain("This turn: $0.0259");
    expect(detail).toContain("Conversation total: $1.3747");
  });

  it("trims trailing .0 from round K/M figures so 7000 reads as '7K' not '7.0K'", () => {
    const { short } = formatUsage(
      JSON.stringify({ cache_read_input_tokens: 7000, cache_creation_input_tokens: 1000000 }),
    );
    expect(short).toBe("CR: 7K | CW: 1M");
  });

  it("keeps sub-1000 counts as raw integers — abbreviation would lose precision for free", () => {
    const { short } = formatUsage(JSON.stringify({ input_tokens: 6, output_tokens: 273 }));
    expect(short).toBe("I/O: 6/273");
  });

  it("expands the abbreviations into a multi-line detail block for the hover tooltip", () => {
    // Hover sees the un-abbreviated names plus thousands separators so
    // 165418 reads as "165,418" — easier to scan than the raw digits we
    // keep in the chip.
    const { detail } = formatUsage(
      JSON.stringify({
        input_tokens: 7,
        output_tokens: 273,
        cache_read_input_tokens: 198056,
        cache_creation_input_tokens: 165418,
        total_cost_usd: 1.1398,
        num_turns: 2,
      }),
    );
    expect(detail).toContain("Input tokens: 7");
    expect(detail).toContain("Output tokens: 273");
    expect(detail).toContain("Cache read: 198,056");
    expect(detail).toContain("Cache write: 165,418");
    expect(detail).toContain("Cost (CLI-reported): $1.1398");
    expect(detail).toContain("Internal API turns: 2");
  });

  it("includes the 5m / 1h cache TTL split in the tooltip when CLI provides it", () => {
    // Single most useful hint when a turn's cost looks mismatched against
    // the raw token count: 1h writes price ~2x the 5m rate, so 165k
    // nearly all 5m costs much less than 165k mostly 1h.
    const { short, detail } = formatUsage(
      JSON.stringify({
        cache_creation_input_tokens: 165418,
        cache_creation: {
          ephemeral_5m_input_tokens: 165000,
          ephemeral_1h_input_tokens: 418,
        },
      }),
    );
    expect(short).toBe("CW: 165.4K");
    expect(detail).toContain("Cache write: 165,418");
    expect(detail).toContain("5min TTL: 165,000");
    expect(detail).toContain("1h TTL:");
    expect(detail).toContain("418");
  });

  it("falls back to the raw payload string in `short` when JSON is malformed", () => {
    expect(formatUsage("not-json")).toEqual({ short: "not-json", detail: "" });
  });
});
