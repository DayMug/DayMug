import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";

import ChatMobileHeader from "./ChatMobileHeader.vue";

function mountHeader(props: Partial<InstanceType<typeof ChatMobileHeader>["$props"]> = {}) {
  return mount(ChatMobileHeader, {
    props: {
      backLabel: "Ada",
      title: "Refactor the parser",
      notificationsEnabled: true,
      ...props,
    },
  });
}

describe("ChatMobileHeader", () => {
  it("labels the conversation-list button with the account it returns to", () => {
    expect(
      mountHeader().find('[data-testid="mobile-back-button"]').attributes("aria-label"),
    ).toContain("Ada");
  });

  it("emits back when the back button is tapped", async () => {
    const wrapper = mountHeader();
    await wrapper.find('[data-testid="mobile-back-button"]').trigger("click");
    expect(wrapper.emitted("back")).toHaveLength(1);
  });

  it("gives every control in the bar a thumb-sized target", () => {
    // 44px is the floor both Apple and Google put on a touch target, and this
    // bar is the worst place to be under it: it sits against the browser's own
    // chrome, so a miss scrolls the address bar instead of hitting the control.
    // Every button spans the bar's full 54px height and is at least 40px wide;
    // the visible chip inside stays at the reference size. Expressed as
    // classes because happy-dom never lays the bar out.
    const wrapper = mountHeader();
    const buttons = wrapper.find(".mobile-global-bar").findAll("button");
    expect(buttons.length).toBeGreaterThanOrEqual(4);
    for (const button of buttons) {
      expect(button.classes(), button.attributes("data-testid") ?? button.text()).toContain(
        "h-[54px]",
      );
    }
  });

  it("shows the conversation title", () => {
    expect(mountHeader().text()).toContain("Refactor the parser");
  });

  it("opens account settings from the avatar", async () => {
    const wrapper = mountHeader();
    await wrapper.get('button[aria-label="Ada"]').trigger("click");
    expect(wrapper.emitted("open-account")).toHaveLength(1);
  });

  it("only offers the bell while notifications are silenced", async () => {
    const enabled = mountHeader({ notificationsEnabled: true });
    await enabled.get('[data-testid="mobile-activity-button"]').trigger("click");
    expect(enabled.find('[data-testid="mobile-enable-notifications"]').exists()).toBe(false);

    const silenced = mountHeader({ notificationsEnabled: false });
    await silenced.get('[data-testid="mobile-activity-button"]').trigger("click");
    await silenced.get('[data-testid="mobile-enable-notifications"]').trigger("click");
    expect(silenced.emitted("toggle-notifications")).toHaveLength(1);
    expect(silenced.find('[data-testid="mobile-activity-popover"]').exists()).toBe(false);
  });

  it("reloads the page from the task feed's refresh action", async () => {
    const reload = vi.fn();
    const original = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...original, reload },
    });

    const wrapper = mountHeader();
    await wrapper.get('[data-testid="mobile-activity-button"]').trigger("click");
    await wrapper.get('[data-testid="mobile-refresh-page"]').trigger("click");
    expect(reload).toHaveBeenCalledOnce();

    Object.defineProperty(window, "location", { configurable: true, value: original });
  });
});
