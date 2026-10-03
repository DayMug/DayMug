<script setup lang="ts">
// Binds ConversationListPanel to the app's conversation state. The shell mounts
// the list in two places — the docked column and the narrow-viewport drawer —
// and both need the same data and the same row actions; only the drawer's
// dismissal differs. ConversationListPanel stays a pure view over its props.
import { computed } from "vue";
import { useRouter } from "vue-router";
import { useI18n } from "vue-i18n";
import ConversationListPanel from "@/components/ConversationListPanel.vue";
import { useAuth } from "@/composables/useAuth";
import { useUsers } from "@/composables/useUsers";
import { useChat } from "@/composables/useChat";
import { useConversations } from "@/composables/useConversations";
import { useConversationActions } from "@/composables/useAppShell";
import { useConversationSharing } from "@/composables/useConversationSharing";
import { useConversationListLayout } from "@/composables/useConversationListLayout";
import { useHelpDoc } from "@/composables/useHelpDoc";
import { conversationAttention } from "@/stores/conversationAttentionStore";
import { helpDialogOpen, marketplaceDialogOpen, upgradeAvailable } from "@/stores/appChromeStore";

const props = defineProps<{
  // Drawer mode: the list floats over the page, so picking a conversation or
  // hitting the collapse button dismisses it instead of toggling the column.
  drawer?: boolean;
}>();

const router = useRouter();
const { t } = useI18n();
const { authMe } = useAuth();
const { users, currentUser } = useUsers();
const { currentConversationId, activeConversationIds, runningConversations, queuedConversations } =
  useChat();
const {
  conversations,
  conversationsHasMore,
  isLoadingMoreConversations,
  loadMoreConversations,
  selectConversation,
  startNewConversation,
  deleteConversation,
  renameConversation,
  toggleConversationPinned,
  reorderPinned,
  closeConversationDrawer,
} = useConversations();
const { handleDeleteConversation, handleSelectConversation } = useConversationActions({
  router,
  t,
  currentUser,
  selectConversation,
  deleteConversation,
});
const {
  handleShareConversation,
  handleUnshareConversation,
  handleCopyShareLink,
  handleExportBundle,
} = useConversationSharing();
const { toggleConversationList } = useConversationListLayout();
const { markdown: helpDocMarkdown } = useHelpDoc();

const isAdmin = computed(() => authMe.value?.is_admin === true);
const helpAvailable = computed(() => helpDocMarkdown.value.trim().length > 0);

function onSelect(id: string) {
  void handleSelectConversation(id);
  if (props.drawer) closeConversationDrawer();
}

function onNewConversation() {
  if (currentUser.value) void startNewConversation(currentUser.value.id);
}

function onReorderPinned(ids: string[]) {
  if (currentUser.value) void reorderPinned(currentUser.value.id, ids);
}

function openMarketplace() {
  marketplaceDialogOpen.value = true;
}

function openHelp() {
  helpDialogOpen.value = true;
}

function onToggleConversations() {
  if (props.drawer) closeConversationDrawer();
  else toggleConversationList();
}
</script>

<template>
  <ConversationListPanel
    v-if="currentUser"
    :conversations="conversations"
    :current-conversation-id="currentConversationId"
    :user-id="currentUser.id"
    :running-conversation-ids="activeConversationIds"
    :conversation-attention="conversationAttention"
    :has-more="conversationsHasMore"
    :loading-more="isLoadingMoreConversations"
    :is-admin="isAdmin"
    :account-name="authMe?.name"
    :account-username="authMe?.username"
    :help-available="helpAvailable"
    :upgrade-available="upgradeAvailable"
    :users="users"
    :running-conversations="runningConversations"
    :queued-conversations="queuedConversations"
    :standalone="drawer"
    @select-conversation="onSelect"
    @new-conversation="onNewConversation"
    @delete-conversation="handleDeleteConversation"
    @rename-conversation="renameConversation"
    @toggle-pinned="toggleConversationPinned"
    @share-conversation="handleShareConversation"
    @unshare-conversation="handleUnshareConversation"
    @copy-share-link="handleCopyShareLink"
    @export-bundle="handleExportBundle"
    @reorder-pinned="onReorderPinned"
    @load-more="loadMoreConversations"
    @toggle-conversations="onToggleConversations"
    @open-settings="router.push({ name: 'settings' })"
    @open-admin-settings="router.push({ name: 'settings-admin' })"
    @open-marketplace="openMarketplace"
    @open-help="openHelp"
  />
</template>
