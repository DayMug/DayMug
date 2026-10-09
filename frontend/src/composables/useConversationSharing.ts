import { useI18n } from "vue-i18n";
import { shareConversation, unshareConversation } from "@/composables/apiConversations";
import { adminSessionBundleUrl } from "@/composables/apiAdmin";
import { useConversations } from "@/composables/useConversations";
import { copyText } from "@/lib/clipboard";
import { showShareNotice } from "@/stores/appChromeStore";

// Share / unshare / export actions for a conversation row, shared by every
// list surface. Each action patches the local list with the server's row and
// confirms through the app-wide share toast.
export function useConversationSharing() {
  const { t } = useI18n();
  const { applyRemoteUpdated } = useConversations();

  // "Copy link" on an already-shared row goes through the same share request
  // as the first share; the list surfaces only differ in the menu label.
  async function handleShareConversation(id: string) {
    const result = await shareConversation(id);
    applyRemoteUpdated(result.conversation, { broadcast: true });
    const url = new URL(result.url, window.location.origin).toString();
    await copyText(url);
    showShareNotice(t("conversations.shareCopied"));
  }

  async function handleUnshareConversation(id: string) {
    const updated = await unshareConversation(id);
    applyRemoteUpdated(updated, { broadcast: true });
    showShareNotice(t("conversations.shareRemoved"));
  }

  function handleExportBundle(id: string) {
    window.open(adminSessionBundleUrl(id), "_blank");
  }

  return {
    handleShareConversation,
    handleCopyShareLink: handleShareConversation,
    handleUnshareConversation,
    handleExportBundle,
  };
}
