import type { FormattedUsage } from "@/lib/chatFormat";

interface TranscriptRow {
  role: string;
  activityType?: string;
  subagent?: boolean;
  usage?: FormattedUsage;
}

// hasModelReplied mirrors the backend's Store.HasModelReply: the model has
// really taken part once it has answered or called a tool. A reply whose usage
// reports zero input and zero output tokens is the CLI relaying a failure (bad
// credentials, an unsupported model), not an answer. A reply without usage
// counts, so an unknown turn errs on the side of "started".
export function hasModelReplied(rows: readonly TranscriptRow[]): boolean {
  return rows.some((row) => {
    if (row.role === "activity") return row.activityType === "tool" && !row.subagent;
    if (row.role !== "assistant") return false;
    const usage = row.usage;
    return !(usage && (usage.input ?? 0) === 0 && (usage.output ?? 0) === 0);
  });
}
