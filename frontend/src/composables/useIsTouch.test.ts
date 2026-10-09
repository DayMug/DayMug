import { describe, it, expect, vi, afterEach } from "vitest";
import { mount } from "@vue/test-utils";
import { defineComponent } from "vue";
import { useIsTouch } from "./useIsTouch";

afterEach(() => {
  vi.unstubAllGlobals();
});

// Captured inside setup(), so a value that only lands in onMounted reads as
// null here — which is the whole point: touch-only affordances that arrive one
// frame late visibly pop in.
let seenDuringSetup: boolean | null = null;

const probe = defineComponent({
  setup() {
    const { isTouch } = useIsTouch();
    seenDuringSetup = isTouch.value;
    return { isTouch };
  },
  template: "<div />",
});

function stubPointer(coarse: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn().mockImplementation((query: string) => ({
      matches: coarse && query === "(pointer: coarse)",
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  );
}

describe("useIsTouch", () => {
  it("knows it is a touch device during setup", () => {
    seenDuringSetup = null;
    stubPointer(true);
    const wrapper = mount(probe);
    expect(seenDuringSetup).toBe(true);
    expect(wrapper.vm.isTouch).toBe(true);
  });

  it("reports a pointer device during setup", () => {
    seenDuringSetup = null;
    stubPointer(false);
    const wrapper = mount(probe);
    expect(seenDuringSetup).toBe(false);
    expect(wrapper.vm.isTouch).toBe(false);
  });
});
