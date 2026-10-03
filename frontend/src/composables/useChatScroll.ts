import { nextTick, onMounted, onUnmounted, watch, type Ref } from "vue";

// Distance (in px) from the bottom we still consider "at bottom". Covers
// partial-row visibility, sub-pixel rounding, and the small offset added by
// the ChatInput's typing indicator.
const AT_BOTTOM_THRESHOLD = 60;

// Trigger lazy-load when the user is within this many pixels of the top.
// Generous so the older page is already in flight by the time the
// scrollbar reaches the very top — avoids a visible "stall at zero"
// pause while waiting on the request.
const LOAD_MORE_THRESHOLD_PX = 200;

export interface UseChatScrollOptions {
  chatPanelRef: Ref<HTMLDivElement | undefined>;
  hasMoreHistory: Ref<boolean>;
  isLoadingMoreHistory: Ref<boolean>;
  loadMoreHistory: () => Promise<number>;
  // setChatScrollFn registers a "scroll to bottom on demand" callback with
  // useChat so it can drive the panel from the conversation switch path.
  setChatScrollFn: (fn: (immediate?: boolean) => void) => void;
}

// useChatScroll owns the chat-panel scroll behaviour: auto-scroll-on-tail,
// lazy-load-on-scroll-top with anchor preservation, and the
// visibility-change "snap to tail when the tab returns" recovery.
export function useChatScroll({
  chatPanelRef,
  hasMoreHistory,
  isLoadingMoreHistory,
  loadMoreHistory,
  setChatScrollFn,
}: UseChatScrollOptions) {
  // Auto-scroll only when the reader is following the live tail. If the
  // reader has scrolled up to look at older content, leave them where they
  // are even when new messages append below — that's the explicit ask:
  // keep their scroll position stable so they can keep reading. The check
  // happens BEFORE the DOM applies the pending message append, so
  // el.scrollHeight here still reflects the pre-append height; nextTick
  // then runs after Vue patches and el.scrollHeight reflects the new total.
  //
  // `immediate=true` skips the at-bottom guard and unconditionally jumps to
  // the tail — used when the user has just opened or re-entered the chat
  // surface (mount, conversation switch, mobile tab toggle).
  function scrollToBottom(immediate = false) {
    const el = chatPanelRef.value;
    if (!el) return;
    if (!immediate) {
      const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
      if (distanceFromBottom >= AT_BOTTOM_THRESHOLD) return;
    }
    nextTick(() => {
      const cur = chatPanelRef.value;
      if (!cur) return;
      cur.scrollTop = cur.scrollHeight;
    });
  }

  async function maybeLoadMoreHistory() {
    const el = chatPanelRef.value;
    if (!el) return;
    if (!hasMoreHistory.value || isLoadingMoreHistory.value) return;
    if (el.scrollTop > LOAD_MORE_THRESHOLD_PX) return;

    // Snapshot the document height *before* prepending so we can restore
    // the same content under the user's viewport after Vue patches the
    // DOM. Without this, scrolling to the top gets yanked back to zero
    // every time a page lands (the scroll bar position survives but its
    // anchor — the top message — is now far below).
    const oldHeight = el.scrollHeight;
    const oldScrollTop = el.scrollTop;

    const added = await loadMoreHistory();
    if (added === 0) return;

    await nextTick();
    const cur = chatPanelRef.value;
    if (!cur) return;
    const delta = cur.scrollHeight - oldHeight;
    cur.scrollTop = oldScrollTop + delta;
  }

  // First page from switchToConversation is capped at HISTORY_PAGE_SIZE,
  // but heavy collapse (consecutive tool turns fold into a single card,
  // thinking blocks hide their bodies) can render it shorter than the
  // viewport. With no scrollbar, the @scroll-driven maybeLoadMoreHistory
  // never fires and the user looks stuck on a handful of recent messages
  // until they manually expand a card. Drain older pages until the panel
  // is scrollable (or we run out of history) so pagination self-bootstraps.
  async function ensureViewportFilled() {
    // Safety cap guards against pathological backends returning the same
    // oldest id repeatedly — without it `added > 0 && still underfilled`
    // would spin forever.
    for (let i = 0; i < 20; i++) {
      const el = chatPanelRef.value;
      if (!el) return;
      if (!hasMoreHistory.value || isLoadingMoreHistory.value) return;
      if (el.scrollHeight > el.clientHeight + AT_BOTTOM_THRESHOLD) return;

      const added = await loadMoreHistory();
      if (added === 0) return;

      await nextTick();
      const cur = chatPanelRef.value;
      if (!cur) return;
      // An underfilled panel has scrollTop=0 sitting at the bottom too,
      // so repin to the new tail; otherwise prepending older pages would
      // visually scroll the latest reply out of view.
      cur.scrollTop = cur.scrollHeight;
    }
  }

  function onChatScroll() {
    void maybeLoadMoreHistory();
  }

  // When the tab is hidden, browsers (Chrome especially) suspend layout in
  // background tabs. Each scrollChat() during streaming still assigns
  // scrollTop=scrollHeight, but it reads a stale scrollHeight, so once
  // layout catches up on visibility return the panel is pinned just shy
  // of the new assistant reply — the message is in the DOM, just off-screen
  // below. We snapshot the at-tail state when the tab goes hidden and
  // re-snap once we're back, so a user who was following the conversation
  // actually sees the message that triggered their notification.
  let wasFollowingTailWhenHidden = true;

  function onVisibilityChange() {
    const el = chatPanelRef.value;
    if (!el) return;
    if (document.visibilityState === "hidden") {
      const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
      wasFollowingTailWhenHidden = distanceFromBottom < AT_BOTTOM_THRESHOLD;
    } else if (document.visibilityState === "visible" && wasFollowingTailWhenHidden) {
      // rAF defers until the browser has run layout for the now-visible tab,
      // so scrollHeight reflects the streamed content that arrived while we
      // were backgrounded.
      window.requestAnimationFrame(() => {
        const cur = chatPanelRef.value;
        if (cur) cur.scrollTop = cur.scrollHeight;
      });
    }
  }

  onMounted(() => {
    setChatScrollFn((immediate?: boolean) => {
      scrollToBottom(immediate);
      // The immediate=true path is the "I just opened / switched into
      // this conversation, snap to tail" call from switchToConversation.
      // Tag along an auto-fill so a heavily-collapsed first page doesn't
      // strand the user without a scrollbar.
      if (immediate) void ensureViewportFilled();
    });
    document.addEventListener("visibilitychange", onVisibilityChange);
  });

  onUnmounted(() => {
    document.removeEventListener("visibilitychange", onVisibilityChange);
  });

  // Force scroll to the latest message whenever the chat panel becomes
  // visible. Covers two cases that were missing before:
  //   1. Initial route mount: the chatScrollFn is registered in onMounted
  //      AFTER conversation messages may already be loaded, so the
  //      switchToConversation scroll call could fire against a null fn.
  //   2. Mobile tab toggle (Files <-> Chat): the chat panel is unmounted by
  //      v-if when the user is on Files, then remounted with scrollTop=0.
  // Watching the ref binding handles both — it fires when the element is
  // (re)mounted but stays inert during plain content updates.
  watch(chatPanelRef, (el) => {
    if (!el) return;
    nextTick(() => {
      el.scrollTop = el.scrollHeight;
      // Covers the case where messages are already loaded by the time
      // the panel mounts (route remount, Files↔Chat tab toggle) — the
      // setChatScrollFn path above won't fire again in that window.
      void ensureViewportFilled();
    });
  });

  return {
    scrollToBottom,
    onChatScroll,
  };
}
