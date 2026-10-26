import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";

import ChatMobileHeader from "./ChatMobileHeader.vue";

function mountHeader(props: Partial<InstanceType<typeof ChatMobileHeader>["$props"]> = {}) {
  return mount(ChatMobileHeader, {
    props: {
      accountLabel: "Ada",
      title: "Refactor the parser",
      notificationsEnabled: true,
      ...props,
    },
  });
}

describe("ChatMobileHeader", () => {
  it("emits back when the back button is tapped", async () => {
    const wrapper = mountHeader();
    await wrapper.find('[data-testid="mobile-back-button"]').trigger("click");
    expect(wrapper.emitted("back")).toHaveLength(1);
  });

  it("gives every control in the bar a thumb-sized target", () => {
    // 44px is the floor both Apple and Google put on a touch target. The
    // visible chip inside each button is smaller so the bar stays light.
    // Expressed as classes because happy-dom never lays the bar out.
    for (const variant of ["conversation", "list"] as const) {
      const buttons = mountHeader({ variant, canResume: false })
        .find(".mobile-global-bar")
        .findAll("button");
      expect(buttons.length).toBeGreaterThanOrEqual(2);
      for (const button of buttons) {
        expect(button.classes(), button.attributes("data-testid")).toContain("size-11");
      }
    }
  });

  it("draws no rule of its own, so the bar reads as part of the screen", () => {
    expect(mountHeader().find(".mobile-global-bar").classes().join(" ")).not.toMatch(/border-b/);
  });

  it("leaves the title off the conversation screen so the transcript gets the room", () => {
    const wrapper = mountHeader();
    expect(wrapper.find('[data-testid="mobile-header-title"]').exists()).toBe(false);
    expect(wrapper.text()).not.toContain("Refactor the parser");
  });

  it("takes no height when floating, but its buttons still take taps", () => {
    const bar = mountHeader({ floating: true }).get(".mobile-global-bar");
    expect(bar.classes()).toContain("absolute");
    expect(bar.classes()).toContain("pointer-events-none");
    expect(bar.classes()).toContain("[&_button]:pointer-events-auto");
  });

  it("flips between transcript and artifacts from one toggle", async () => {
    const wrapper = mountHeader();
    const toggle = wrapper.get('[data-testid="mobile-artifacts-toggle"]');
    expect(toggle.attributes("aria-pressed")).toBe("false");
    await toggle.trigger("click");
    expect(wrapper.emitted("toggle-artifacts")).toHaveLength(1);

    await wrapper.setProps({ artifactsOpen: true });
    expect(wrapper.get('[data-testid="mobile-artifacts-toggle"]').attributes("aria-pressed")).toBe(
      "true",
    );
  });

  it("offers the open conversation as a way back from the list", async () => {
    const wrapper = mountHeader({ variant: "list", canResume: true });
    expect(wrapper.find('[data-testid="mobile-back-button"]').exists()).toBe(false);
    await wrapper.get('[data-testid="mobile-header-title"]').trigger("click");
    expect(wrapper.emitted("resume")).toHaveLength(1);
  });

  it("does not make the list title tappable when there is nothing to resume", async () => {
    const wrapper = mountHeader({ variant: "list", canResume: false });
    const title = wrapper.get('[data-testid="mobile-header-title"]');
    expect(title.element.tagName).toBe("SPAN");
    await title.trigger("click");
    expect(wrapper.emitted("resume")).toBeUndefined();
  });

  it("shows the open conversation's title on the list", () => {
    expect(mountHeader({ variant: "list" }).text()).toContain("Refactor the parser");
  });

  it("opens account settings from the avatar on the list", async () => {
    const wrapper = mountHeader({ variant: "list" });
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
