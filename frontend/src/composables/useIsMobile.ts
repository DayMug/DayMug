import { ref, computed, onMounted, onUnmounted } from "vue";
import { useLocalStorage, useWindowSize } from "@vueuse/core";
import {
  WORKSPACE_COLLAPSED_KEY,
  WORKSPACE_DEFAULT_WIDTH,
  WORKSPACE_WIDTH_KEY,
} from "./useResizablePanels";

const MOBILE_BREAKPOINT = 768;

// Widths the desktop shell needs before an inline conversation list is worth
// having. The 48px rail plus 232px list match the reference workspace's 280px
// left column; the chat floor mirrors the sizes the panels themselves enforce.
const AGENT_RAIL_PX = 48;
const CONVERSATION_LIST_PX = 232;
const RESPONSIVE_WORKSPACE_BREAKPOINT = 1080;
const MIN_CHAT_PX = 360;

function useMaxWidth(maxWidthPx: number) {
  // Resolve the viewport during setup, not in onMounted. Vue runs onMounted
  // after a component's first render, so a deferred read meant every phone
  // painted the full desktop tree once — the agent rail, the conversation
  // column, the resizable panes and the workspace panel — and then threw all of
  // it away a frame later when the flag flipped. The router already answers
  // "is this a phone?" with a synchronous matchMedia call when it redirects `/`
  // to the conversation list, so the deferred read also left router and shell
  // disagreeing for exactly that frame.
  //
  // The MediaQueryList is created once and reused by the listener: querying
  // twice would leave the listener watching a different object than the one the
  // initial value came from.
  const mql = supportsMatchMedia() ? window.matchMedia(`(max-width: ${maxWidthPx}px)`) : null;
  const matches = ref(mql?.matches ?? false);

  function update(e: MediaQueryListEvent | MediaQueryList) {
    matches.value = e.matches;
  }

  onMounted(() => {
    if (!mql) return;
    // Re-sync: a rotation between setup and mount would otherwise be missed,
    // since the listener only reports changes from here on.
    matches.value = mql.matches;
    mql.addEventListener("change", update);
  });

  onUnmounted(() => {
    mql?.removeEventListener("change", update);
  });

  return matches;
}

function supportsMatchMedia(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function";
}

/**
 * Reactive composable that tracks whether the viewport is mobile-sized (< 768px).
 * Uses window.matchMedia for efficient change detection.
 */
export function useIsMobile() {
  const isMobile = useMaxWidth(MOBILE_BREAKPOINT - 1);
  return { isMobile };
}

/**
 * Whether the shell has to fall back to showing the conversation list as a
 * modal drawer instead of an inline column.
 *
 * This asks about *available* width, not viewport width. A fixed 1024px
 * breakpoint got tablet portrait wrong in both directions: it forced a
 * full-screen modal at 834px even when the workspace pane was collapsed and
 * ~500px of chat was free, and it would have allowed an inline column at
 * 1030px with the workspace open, leaving the chat squeezed to nothing. The
 * modal only exists because the columns do not fit, so measure that.
 */
export function useIsNarrow() {
  const { width } = useWindowSize();
  // Same localStorage entries useResizablePanels owns; vueuse keeps the two
  // reads in sync within the tab, so collapsing the workspace re-evaluates
  // this immediately rather than at the next reload.
  const workspaceCollapsed = useLocalStorage(WORKSPACE_COLLAPSED_KEY, false);
  const workspaceWidth = useLocalStorage(WORKSPACE_WIDTH_KEY, WORKSPACE_DEFAULT_WIDTH);

  const isNarrow = computed(() => {
    const workspace =
      workspaceCollapsed.value || width.value <= RESPONSIVE_WORKSPACE_BREAKPOINT
        ? 0
        : workspaceWidth.value;
    return width.value < AGENT_RAIL_PX + CONVERSATION_LIST_PX + workspace + MIN_CHAT_PX;
  });

  return { isNarrow };
}
