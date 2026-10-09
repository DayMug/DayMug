import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import SettingsLayout from "./SettingsLayout.vue";

// SettingsLayout is now a thin shell — it provides a scrollable, pull-to-
// refresh container for the nested /settings/* routes. The hub-and-spoke
// nav lives in SettingsHub, and detail pages render their own
// SettingsDetailHeader. A pull remounts the active route (re-running its
// onMounted fetches), so we test both rendering and the remount.

// happy-dom doesn't synthesise touch lists, so we hand-roll the bits the
// gesture reads.
function makeTouchEvent(type: string, clientY: number): TouchEvent {
  const event = new Event(type, { bubbles: true, cancelable: true }) as TouchEvent;
  const touch = { clientY } as Touch;
  Object.defineProperty(event, "touches", { value: [touch] });
  Object.defineProperty(event, "changedTouches", { value: [touch] });
  return event;
}

function makeRouter(child: Record<string, unknown>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/settings",
        component: SettingsLayout,
        children: [{ path: "", name: "settings", component: child }],
      },
    ],
  });
  router.push("/settings");
  return router;
}

describe("SettingsLayout", () => {
  it("renders the nested settings child route inside the shell", async () => {
    const router = makeRouter({ template: '<div class="hub-stub">hub</div>' });
    await router.isReady();
    const wrapper = mount(SettingsLayout, { global: { plugins: [router] } });
    expect(wrapper.find(".hub-stub").exists()).toBe(true);
  });

  it("remounts the active route on a pull past the trigger distance", async () => {
    const mountedSpy = vi.fn();
    const router = makeRouter({
      template: '<div class="child-stub">child</div>',
      mounted: mountedSpy,
    });
    await router.isReady();
    const wrapper = mount(SettingsLayout, { global: { plugins: [router] } });
    expect(mountedSpy).toHaveBeenCalledTimes(1);

    const shell = wrapper.find(".pull-to-refresh");
    Object.defineProperty(shell.element, "scrollTop", { value: 0, configurable: true });
    shell.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    shell.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    shell.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();
    await wrapper.vm.$nextTick();

    // Key bump remounted the child, so its onMounted ran a second time.
    expect(mountedSpy).toHaveBeenCalledTimes(2);
    expect(wrapper.find(".child-stub").exists()).toBe(true);
  });
});
