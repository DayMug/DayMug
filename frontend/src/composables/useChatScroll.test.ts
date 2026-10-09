/* eslint-disable vue/one-component-per-file -- each test mounts its own tiny harness component */
// Covers the viewport-auto-fill behaviour. The history pagination is
// scroll-event-driven, so a heavily-collapsed first page that doesn't
// fill the panel used to leave the user stranded without a scrollbar.
// useChatScroll now drains older pages on the conversation-switch path
// until the panel becomes scrollable (or history is exhausted).
import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { defineComponent, h, nextTick, ref } from "vue";
import { useChatScroll } from "./useChatScroll";

type FakePanel = {
  clientHeight: number;
  scrollHeight: number;
  scrollTop: number;
};

interface HarnessOptions {
  clientHeight: number;
  initialScrollHeight: number;
  loadMoreHistory: () => Promise<number>;
  hasMore?: boolean;
}

function harness(opts: HarnessOptions) {
  const panel: FakePanel = {
    clientHeight: opts.clientHeight,
    scrollHeight: opts.initialScrollHeight,
    scrollTop: 0,
  };
  // Bound to undefined first; we "bind" it after mount to match the real
  // template-ref lifecycle (and to actually fire the chatPanelRef watcher,
  // which is non-immediate).
  const chatPanelRef = ref<HTMLDivElement | undefined>(undefined);
  const hasMoreHistory = ref(opts.hasMore ?? true);
  const isLoadingMoreHistory = ref(false);

  const Comp = defineComponent({
    setup() {
      useChatScroll({
        chatPanelRef,
        hasMoreHistory,
        isLoadingMoreHistory,
        loadMoreHistory: opts.loadMoreHistory,
        setChatScrollFn: () => {
          // The harness-driven tests don't exercise the registered fn path —
          // tests 6/7 below build a bespoke component for that.
        },
      });
      return () => h("div");
    },
  });

  const wrapper = mount(Comp);
  // Bind the panel after mount → fires the watch(chatPanelRef) inside the
  // composable, which is the production path (template ref binds post-mount).
  chatPanelRef.value = panel as unknown as HTMLDivElement;

  return { panel, chatPanelRef, hasMoreHistory, isLoadingMoreHistory, wrapper };
}

// The composable's auto-fill loop alternates `await loadMoreHistory()` and
// `await nextTick()`, so we have to pump microtasks several times to let
// the full chain unwind. 50 ticks is plenty given the 20-iteration cap.
async function flush(n = 50) {
  for (let i = 0; i < n; i++) {
    await nextTick();
  }
}

describe("useChatScroll — ensureViewportFilled", () => {
  it("auto-fills older pages on mount until the panel becomes scrollable", async () => {
    const loadMore = vi.fn(async () => {
      h.panel.scrollHeight += 200;
      return 25;
    });

    const h = harness({
      clientHeight: 600,
      initialScrollHeight: 100,
      loadMoreHistory: loadMore,
    });

    await flush();

    // Loop should stop once scrollHeight > clientHeight + AT_BOTTOM_THRESHOLD (60).
    // Each call adds 200; starting from 100 we need 3 calls (100 → 700) to
    // cross the 660 threshold.
    expect(loadMore).toHaveBeenCalledTimes(3);
    expect(h.panel.scrollHeight).toBeGreaterThan(h.panel.clientHeight + 60);
    // Pinned to the new tail so the latest reply stays in view.
    expect(h.panel.scrollTop).toBe(h.panel.scrollHeight);
  });

  it("bails out as soon as hasMoreHistory flips off", async () => {
    let call = 0;
    const loadMore = vi.fn(async () => {
      call++;
      h.panel.scrollHeight += 50;
      if (call === 2) h.hasMoreHistory.value = false;
      return 10;
    });

    const h = harness({
      clientHeight: 800,
      initialScrollHeight: 50,
      loadMoreHistory: loadMore,
    });

    await flush();

    // 1st call: scrollHeight 50→100, still underfilled, hasMore still true → loops
    // 2nd call: scrollHeight 100→150, hasMore flips to false → loop exits next iter
    expect(loadMore).toHaveBeenCalledTimes(2);
    expect(h.panel.scrollHeight).toBe(150);
  });

  it("stops when loadMoreHistory returns 0 (server exhausted)", async () => {
    const loadMore = vi.fn(async () => 0);

    const h = harness({
      clientHeight: 1000,
      initialScrollHeight: 100,
      loadMoreHistory: loadMore,
    });

    await flush();

    expect(loadMore).toHaveBeenCalledTimes(1);
    expect(h.panel.scrollHeight).toBe(100);
  });

  it("does nothing when the panel already fills the viewport", async () => {
    const loadMore = vi.fn(async () => 25);

    harness({
      clientHeight: 600,
      initialScrollHeight: 5000, // way past threshold
      loadMoreHistory: loadMore,
    });

    await flush();

    expect(loadMore).not.toHaveBeenCalled();
  });

  it("does nothing when there is no older history", async () => {
    const loadMore = vi.fn(async () => 25);

    harness({
      clientHeight: 600,
      initialScrollHeight: 100,
      loadMoreHistory: loadMore,
      hasMore: false,
    });

    await flush();

    expect(loadMore).not.toHaveBeenCalled();
  });

  it("the registered immediate=true scroll fn drives auto-fill independently", async () => {
    // Start scrollable so the watch(chatPanelRef) path exits without
    // calling loadMore. Then shrink the panel and invoke the registered
    // scroll fn to prove it can trigger the loop on its own.
    const panel: FakePanel = { clientHeight: 600, scrollHeight: 5000, scrollTop: 0 };
    const chatPanelRef = ref<HTMLDivElement | undefined>(undefined);
    const hasMoreHistory = ref(true);
    const isLoadingMoreHistory = ref(false);
    // Boxed in an object so TS doesn't narrow the closure-assigned `let`
    // to `never` once it sees the initial null.
    const reg: { fn: ((immediate?: boolean) => void) | null } = { fn: null };
    const loadMore = vi.fn(async () => {
      panel.scrollHeight += 300;
      return 25;
    });

    const Comp = defineComponent({
      setup() {
        useChatScroll({
          chatPanelRef,
          hasMoreHistory,
          isLoadingMoreHistory,
          loadMoreHistory: loadMore,
          setChatScrollFn: (fn) => {
            reg.fn = fn;
          },
        });
        return () => h("div");
      },
    });

    mount(Comp);
    chatPanelRef.value = panel as unknown as HTMLDivElement;
    await flush();
    expect(loadMore).not.toHaveBeenCalled();

    // Conversation switched into a heavily-collapsed thread: rendered
    // height shrinks below the viewport. reg.fn(true) is the
    // switchToConversation → scrollChat(true) entry point.
    panel.scrollHeight = 100;
    reg.fn?.(true);
    await flush();

    // 100 → 400 (still < 660) → 700 (over). Two calls.
    expect(loadMore).toHaveBeenCalledTimes(2);
    expect(panel.scrollTop).toBe(panel.scrollHeight);
  });

  it("the non-immediate scroll fn never triggers auto-fill", async () => {
    // Streaming-token tail path. Even if the panel is underfilled, this
    // must not back-fill — otherwise every streamed chunk would burn a
    // round-trip pulling older history the user didn't ask for.
    const panel: FakePanel = { clientHeight: 600, scrollHeight: 5000, scrollTop: 0 };
    const chatPanelRef = ref<HTMLDivElement | undefined>(undefined);
    const hasMoreHistory = ref(true);
    const isLoadingMoreHistory = ref(false);
    const reg: { fn: ((immediate?: boolean) => void) | null } = { fn: null };
    const loadMore = vi.fn(async () => 25);

    const Comp = defineComponent({
      setup() {
        useChatScroll({
          chatPanelRef,
          hasMoreHistory,
          isLoadingMoreHistory,
          loadMoreHistory: loadMore,
          setChatScrollFn: (fn) => {
            reg.fn = fn;
          },
        });
        return () => h("div");
      },
    });

    mount(Comp);
    chatPanelRef.value = panel as unknown as HTMLDivElement;
    await flush();
    expect(loadMore).not.toHaveBeenCalled();

    panel.scrollHeight = 100;
    reg.fn?.(false);
    await flush();

    expect(loadMore).not.toHaveBeenCalled();
  });
});
