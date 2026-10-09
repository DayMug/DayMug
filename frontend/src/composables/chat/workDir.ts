// workDirHint returns the activity-info text shown at the top of the chat. An
// Agent's working directory is fixed, so the hint only names it.
export const WORK_DIR_HINT_PREFIX = "Your current working directory is ";
export function workDirHint(workDir: string): string {
  return `${WORK_DIR_HINT_PREFIX}${workDir}.`;
}
