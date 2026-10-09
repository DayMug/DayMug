import { computed } from "vue";
import { useIsNarrow } from "@/composables/useIsMobile";
import { useConversations } from "@/composables/useConversations";

// The rail's conversation-list button drives two different things depending on
// the viewport: an inline column when there is room for one, a modal drawer
// when there isn't. Routing here keeps the persisted column preference from
// being rewritten every time a narrow-viewport user dismisses the drawer.
export function useConversationListLayout() {
  const { isNarrow } = useIsNarrow();
  const {
    isConversationPanelOpen,
    isConversationDrawerOpen,
    toggleConversationPanel,
    toggleConversationDrawer,
  } = useConversations();

  const conversationListVisible = computed(() =>
    isNarrow.value ? isConversationDrawerOpen.value : isConversationPanelOpen.value,
  );

  function toggleConversationList() {
    if (isNarrow.value) toggleConversationDrawer();
    else toggleConversationPanel();
  }

  return { isNarrow, conversationListVisible, toggleConversationList };
}
