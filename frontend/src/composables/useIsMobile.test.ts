/* eslint-disable vue/one-component-per-file -- multiple test-only components share this file */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { mount } from "@vue/test-utils";
import { defineComponent, nextTick } from "vue";
import { useIsMobile, useIsNarrow } from "./useIsMobile";

// Records what the flag read during setup — i.e. before Vue has rendered the
// component even once. Anything that only arrives in onMounted is invisible here.
let seenDuringSetup: boolean | null = null;

function createMobileComponent() {
  return defineComponent({
    setup() {
      const { isMobile } = useIsMobile();
      seenDuringSetup = isMobile.value;
      return { isMobile };
    },
    template: "<div>{{ isMobile }}</div>",
  });
}

function createNarrowComponent() {
  return defineComponent({
    setup() {
      const { isNarrow } = useIsNarrow();
      return { isNarrow };
    },
    template: "<div>{{ isNarrow }}</div>",
  });
}

type MockMql = {
  query: string;
  matches: boolean;
  listeners: Array<(e: MediaQueryListEvent) => void>;
};

let mqls: MockMql[] = [];

function setupMatchMedia(matcher: (query: string) => boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockImplementation((query: string) => {
      const entry: MockMql = {
        query,
        matches: matcher(query),
        listeners: [],
      };
      mqls.push(entry);
      return {
        get matches() {
          return entry.matches;
        },
        addEventListener: (_event: string, handler: (e: MediaQueryListEvent) => void) => {
          entry.listeners.push(handler);
        },
        removeEventListener: (_event: string, handler: (e: MediaQueryListEvent) => void) => {
          const idx = entry.listeners.indexOf(handler);
          if (idx >= 0) entry.listeners.splice(idx, 1);
        },
      };
    }),
  );
}

beforeEach(() => {
  mqls = [];
  seenDuringSetup = null;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useIsMobile", () => {
  it("returns false for desktop viewport", () => {
    setupMatchMedia(() => false);
    const wrapper = mount(createMobileComponent());
    expect(wrapper.vm.isMobile).toBe(false);
  });

  it("returns true for mobile viewport", () => {
    setupMatchMedia(() => true);
    const wrapper = mount(createMobileComponent());
    expect(wrapper.vm.isMobile).toBe(true);
  });

  it("knows the viewport during setup, before the first render", () => {
    // The shell picks its whole component tree off this flag. A value that only
    // lands in onMounted made every phone render the desktop tree once — rail,
    // conversation column, resizable panes, workspace panel — and discard it a
    // frame later, and left the router (which reads matchMedia synchronously to
    // redirect `/`) disagreeing with the shell for that frame.
    setupMatchMedia(() => true);
    mount(createMobileComponent());
    expect(seenDuringSetup).toBe(true);
  });

  it("listens on the same MediaQueryList it read the initial value from", () => {
    setupMatchMedia(() => true);
    mount(createMobileComponent());
    const mobile = mqls.filter((m) => m.query === "(max-width: 767px)");
    expect(mobile).toHaveLength(1);
    expect(mobile[0].listeners).toHaveLength(1);
  });

  it("uses the 767px max-width media query", () => {
    setupMatchMedia(() => false);
    mount(createMobileComponent());
    expect(mqls.some((m) => m.query === "(max-width: 767px)")).toBe(true);
  });

  it("reacts to viewport changes", async () => {
    setupMatchMedia(() => false);
    const wrapper = mount(createMobileComponent());
    expect(wrapper.vm.isMobile).toBe(false);

    for (const m of mqls) {
      for (const handler of m.listeners) {
        handler({ matches: true } as MediaQueryListEvent);
      }
    }
    await nextTick();
    expect(wrapper.vm.isMobile).toBe(true);

    for (const m of mqls) {
      for (const handler of m.listeners) {
        handler({ matches: false } as MediaQueryListEvent);
      }
    }
    await nextTick();
    expect(wrapper.vm.isMobile).toBe(false);
  });

  it("cleans up listener on unmount", () => {
    setupMatchMedia(() => false);
    const wrapper = mount(createMobileComponent());
    expect(mqls[0].listeners.length).toBe(1);
    wrapper.unmount();
    expect(mqls[0].listeners.length).toBe(0);
  });
});

describe("useIsNarrow", () => {
  // Room needed for an inline column = rail 48 + list 232 + workspace + chat 360.
  function setViewportWidth(px: number) {
    Object.defineProperty(window, "innerWidth", { value: px, configurable: true });
    window.dispatchEvent(new Event("resize"));
  }

  beforeEach(() => {
    setupMatchMedia(() => false);
    localStorage.clear();
  });

  it("keeps the sidebar inline when the responsive layout hides the workspace", async () => {
    localStorage.setItem("daymug-workspace-collapsed", "false");
    setViewportWidth(834);
    const wrapper = mount(createNarrowComponent());
    await nextTick();
    expect(wrapper.vm.isNarrow).toBe(false);
  });

  it("allows an inline column at the same width once the workspace is collapsed", async () => {
    // 834 >= 48 + 232 + 0 + 360 = 640, so ~550px of
    // chat is free and there is no reason to cover the app with a modal.
    localStorage.setItem("daymug-workspace-collapsed", "true");
    setViewportWidth(834);
    const wrapper = mount(createNarrowComponent());
    await nextTick();
    expect(wrapper.vm.isNarrow).toBe(false);
  });

  it("goes inline at tablet landscape with the workspace open", async () => {
    localStorage.setItem("daymug-workspace-collapsed", "false");
    setViewportWidth(1194);
    const wrapper = mount(createNarrowComponent());
    await nextTick();
    expect(wrapper.vm.isNarrow).toBe(false);
  });

  it("accounts for a workspace the user has widened", async () => {
    // 1194 is wide enough at the default 400, but not at 720: the old fixed
    // breakpoint could not see this at all.
    localStorage.setItem("daymug-workspace-collapsed", "false");
    localStorage.setItem("daymug-workspace-px", "720");
    setViewportWidth(1194);
    const wrapper = mount(createNarrowComponent());
    await nextTick();
    expect(wrapper.vm.isNarrow).toBe(true);
  });

  it("reacts to the viewport being resized", async () => {
    localStorage.setItem("daymug-workspace-collapsed", "false");
    setViewportWidth(1400);
    const wrapper = mount(createNarrowComponent());
    await nextTick();
    expect(wrapper.vm.isNarrow).toBe(false);

    setViewportWidth(620);
    await nextTick();
    expect(wrapper.vm.isNarrow).toBe(true);
  });
});
