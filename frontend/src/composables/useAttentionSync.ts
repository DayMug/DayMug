import { computed, onScopeDispose, ref, watch, type Ref } from "vue";
import { fetchConversationAttention, markConversationRead } from "./apiConversations";
import { activityVersion } from "@/stores/agentActivityStore";
import {
  applyAttention,
  attentionVersion,
  conversationAttention,
  dropAttention,
} from "@/stores/conversationAttentionStore";

// Coalesces the burst of activity frames one turn produces (queued → running
// → finished) into a single fetch.
const REFRESH_DEBOUNCE_MS = 250;

// useAttentionSync keeps the attention store in step with the server and
// clears a conversation's flag once the user is actually looking at it.
//
// shownConversationId is the conversation on screen according to the
// router ("" on the mobile list, settings, …). A hidden page shows nothing:
// a turn that finishes behind a locked phone must still be waiting for the
// user when they come back.
export function useAttentionSync(shownConversationId: Ref<string>, enabled: Ref<boolean>) {
  const pageVisible = ref(document.visibilityState !== "hidden");
  const viewedConversationId = computed(() => (pageVisible.value ? shownConversationId.value : ""));

  function markRead(id: string) {
    dropAttention(id);
    markConversationRead(id).catch(() => undefined);
  }

  let timer = 0;
  let inFlight = false;
  let queued = false;

  async function refresh() {
    if (!enabled.value) return;
    if (inFlight) {
      queued = true;
      return;
    }
    inFlight = true;
    try {
      const { items, titles } = await fetchConversationAttention();
      // The conversation on screen never shows as unread: a turn that ends
      // while the user watches it is acknowledged on arrival.
      const viewed = viewedConversationId.value;
      const viewedFlagged = !!viewed && items.some((row) => row.conversation_id === viewed);
      applyAttention(
        viewedFlagged ? items.filter((row) => row.conversation_id !== viewed) : items,
        titles ?? {},
      );
      if (viewedFlagged) markRead(viewed);
    } catch {
      // A missed refresh only leaves the badge stale until the next frame.
    } finally {
      inFlight = false;
      if (queued) {
        queued = false;
        schedule();
      }
    }
  }

  function schedule() {
    window.clearTimeout(timer);
    timer = window.setTimeout(() => void refresh(), REFRESH_DEBOUNCE_MS);
  }

  function onVisibilityChange() {
    pageVisible.value = document.visibilityState !== "hidden";
    if (pageVisible.value) schedule();
  }
  document.addEventListener("visibilitychange", onVisibilityChange);

  watch([activityVersion, attentionVersion], schedule);
  watch(enabled, (on) => on && void refresh(), { immediate: true });
  watch(viewedConversationId, (id) => {
    if (id && conversationAttention.value[id]) markRead(id);
  });

  onScopeDispose(() => {
    window.clearTimeout(timer);
    document.removeEventListener("visibilitychange", onVisibilityChange);
  });

  return { refresh };
}
