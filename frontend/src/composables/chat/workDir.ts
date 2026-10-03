import {
  conversationWorkDir,
  currentConversationId,
  isWorkDirLocked,
} from "@/stores/activeConversationStore";
import { chatMessages } from "@/stores/chatMessageStore";

import { updateConversationWorkDir } from "../useApi";

// workDirHint returns the activity-info text shown at the top of the
// chat. While the workdir is unlocked we include the call-to-action
// pointing the user at the workspace panel; once locked (the user has
// at least one message in this conversation) the instruction is dead
// weight — claude is already running and the panel won't accept a new
// path — so the hint shrinks to just the path itself.
export const WORK_DIR_HINT_PREFIX = "Your current working directory is ";
export function workDirHint(workDir: string, locked: boolean): string {
  if (locked) {
    return `${WORK_DIR_HINT_PREFIX}${workDir}.`;
  }
  return `${WORK_DIR_HINT_PREFIX}${workDir}. To switch, navigate in the workspace panel on the right.`;
}

// downgradeWorkDirHint rewrites the existing top-of-chat working-directory
// hint to its shortened form. Called from sendMessage so the hint stays
// in sync with the workdir-lock that flips on the first user message.
export function downgradeWorkDirHint() {
  if (!conversationWorkDir.value) return;
  const idx = chatMessages.value.findIndex(
    (m) =>
      m.role === "activity" &&
      m.activityType === "info" &&
      m.content.startsWith(WORK_DIR_HINT_PREFIX),
  );
  if (idx >= 0) {
    chatMessages.value[idx].content = workDirHint(conversationWorkDir.value, true);
  }
}

// lockWorkDirAfterUpload is the hook the chat composer fires when a file
// finishes uploading. The backend has already persisted conv.work_dir to
// the cwd that file landed under, so we mirror that on the front-end:
// flip isWorkDirLocked so the workspace panel's directory-changed events
// no longer try to repoint the conversation, and rewrite the top-of-chat
// hint into its locked form so the call-to-action stops misleading the
// user. Idempotent — safe to call on every successful upload.
export function lockWorkDirAfterUpload() {
  if (isWorkDirLocked.value) return;
  isWorkDirLocked.value = true;
  downgradeWorkDirHint();
}

export async function changeWorkDir(newAbsPath: string) {
  const convId = currentConversationId.value;
  if (!convId || isWorkDirLocked.value) return;
  try {
    const conv = await updateConversationWorkDir(convId, newAbsPath);
    // The user may have switched conversations while the PATCH was in flight.
    // `conversationWorkDir` and the hint message both describe whatever is on
    // screen now, so writing this response into them would show the old
    // conversation's directory under the new one.
    if (currentConversationId.value !== convId) return;
    conversationWorkDir.value = conv.work_dir;

    // Update the info message with the new absolute path.
    const infoIdx = chatMessages.value.findIndex(
      (m) =>
        m.role === "activity" &&
        m.activityType === "info" &&
        m.content.startsWith(WORK_DIR_HINT_PREFIX),
    );
    if (infoIdx >= 0) {
      chatMessages.value[infoIdx].content = workDirHint(conv.work_dir, isWorkDirLocked.value);
    }
  } catch {
    // ignore — may be locked server-side
  }
}
