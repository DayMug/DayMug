import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import PullToRefresh from "./PullToRefresh.vue";

// happy-dom doesn't synthesise touch lists, so we hand-roll the bits the
// component reads (mirrors MobileAgentsScreen.test.ts).
function makeTouchEvent(type: string, clientY: number): TouchEvent {
  const event = new Event(type, { bubbles: true, cancelable: true }) as TouchEvent;
  const touch = { clientY } as Touch;
  Object.defineProperty(event, "touches", { value: [touch] });
  Object.defineProperty(event, "changedTouches", { value: [touch] });
  return event;
}

function mountWrapper(onRefresh: () => Promise<void> | void, disabled = false) {
  return mount(PullToRefresh, {
    props: { onRefresh, disabled },
    slots: { default: '<div class="content">body</div>' },
  });
}

function atTop(el: Element) {
  Object.defineProperty(el, "scrollTop", { value: 0, configurable: true });
}

describe("PullToRefresh", () => {
  it("renders slotted content", () => {
    const wrapper = mountWrapper(() => {});
    expect(wrapper.find(".content").exists()).toBe(true);
  });

  it("invokes onRefresh after a pull past the trigger distance", async () => {
    const onRefresh = vi.fn();
    const wrapper = mountWrapper(onRefresh);
    const root = wrapper.find(".pull-to-refresh");
    atTop(root.element);

    // 300 - 100 = 200px of finger travel, rubber-banded to ~96px — well
    // above the 64px threshold.
    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    root.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("does not invoke onRefresh when the pull is below the trigger distance", async () => {
    const onRefresh = vi.fn();
    const wrapper = mountWrapper(onRefresh);
    const root = wrapper.find(".pull-to-refresh");
    atTop(root.element);

    // 40px of finger ≈ 20px indicator — under the 64px commit threshold.
    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 140));
    root.element.dispatchEvent(makeTouchEvent("touchend", 140));
    await wrapper.vm.$nextTick();

    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("does not engage the gesture unless scrolled to the top", async () => {
    const onRefresh = vi.fn();
    const wrapper = mountWrapper(onRefresh);
    const root = wrapper.find(".pull-to-refresh");
    Object.defineProperty(root.element, "scrollTop", { value: 50, configurable: true });

    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    root.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("does nothing when disabled", async () => {
    const onRefresh = vi.fn();
    const wrapper = mountWrapper(onRefresh, true);
    const root = wrapper.find(".pull-to-refresh");
    atTop(root.element);

    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    root.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("pins the spinner while an async onRefresh is in flight", async () => {
    let resolve!: () => void;
    const onRefresh = vi.fn(() => new Promise<void>((r) => (resolve = r)));
    const wrapper = mountWrapper(onRefresh);
    const root = wrapper.find(".pull-to-refresh");
    atTop(root.element);

    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    root.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    const indicator = wrapper.find('[data-testid="pull-indicator"]');
    expect(indicator.text()).toContain("Refreshing");
    const style = indicator.attributes("style") ?? "";
    expect(style).toMatch(/height:\s*\d+px/);
    expect(style).not.toMatch(/height:\s*0px/);

    // Once the refresh resolves the spinner clears.
    resolve();
    await wrapper.vm.$nextTick();
    await wrapper.vm.$nextTick();
    const after = wrapper.find('[data-testid="pull-indicator"]').attributes("style") ?? "";
    expect(after).toMatch(/height:\s*0px/);
  });

  it("ignores a second pull while a refresh is already in flight", async () => {
    const onRefresh = vi.fn(() => new Promise<void>(() => {}));
    const wrapper = mountWrapper(onRefresh);
    const root = wrapper.find(".pull-to-refresh");
    atTop(root.element);

    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    root.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    root.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    root.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    root.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    expect(onRefresh).toHaveBeenCalledTimes(1);
  });
});
