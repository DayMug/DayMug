// Global singleton store: app-wide chrome that several shell surfaces toggle —
// the agent rail, the docked conversation list, the narrow-viewport drawer and
// the mobile agents screen all open the same help and marketplace dialogs and
// show the same share toast. See src/stores/index.ts for the conventions every
// store in this directory follows.
import { ref } from "vue";

export const helpDialogOpen = ref(false);
export const marketplaceDialogOpen = ref(false);

// Set by the app shell's one-shot admin upgrade check; drives the red dot on
// the account menu's admin settings entry.
export const upgradeAvailable = ref(false);

const SHARE_NOTICE_MS = 2200;

export const shareNotice = ref("");
let shareNoticeTimer: ReturnType<typeof setTimeout> | null = null;

// showShareNotice keeps the toast's auto-dismiss in step with its text: a new
// notice restarts the countdown instead of inheriting the previous one's.
export function showShareNotice(message: string) {
  shareNotice.value = message;
  if (shareNoticeTimer) clearTimeout(shareNoticeTimer);
  shareNoticeTimer = setTimeout(() => {
    shareNotice.value = "";
    shareNoticeTimer = null;
  }, SHARE_NOTICE_MS);
}

export function cancelShareNoticeTimer() {
  if (shareNoticeTimer !== null) {
    clearTimeout(shareNoticeTimer);
    shareNoticeTimer = null;
  }
}

export function resetAppChromeStore() {
  cancelShareNoticeTimer();
  helpDialogOpen.value = false;
  marketplaceDialogOpen.value = false;
  upgradeAvailable.value = false;
  shareNotice.value = "";
}
