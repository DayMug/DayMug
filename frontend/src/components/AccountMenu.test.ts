import { afterEach, describe, expect, it } from "vitest";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import AccountMenu from "./AccountMenu.vue";

let wrapper: VueWrapper | null = null;

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  document.body.innerHTML = "";
});

async function openMenu(props: Record<string, unknown> = {}) {
  wrapper = mount(AccountMenu, {
    props: { name: "Lin Che", username: "linche", ...props },
    attachTo: document.body,
  });
  // reka opens on pointerdown or Enter, never on click; happy-dom does not
  // synthesise the pointer path faithfully, so drive the keyboard one.
  await wrapper.get('[data-testid="account-menu-trigger"]').trigger("keydown", { key: "Enter" });
  await flushPromises();
  return wrapper;
}

function menuItem(testId: string): HTMLElement | null {
  return document.body.querySelector(`[data-testid="${testId}"]`);
}

describe("AccountMenu", () => {
  it("shows the signed-in identity above the utilities", async () => {
    await openMenu();

    const menu = document.body.querySelector('[data-testid="account-menu"]');
    expect(menu?.querySelector("strong")?.textContent).toBe("Lin Che");
    expect(menu?.textContent).toContain("@linche");
    expect(menuItem("account-menu-marketplace")).not.toBeNull();
    expect(menuItem("account-menu-settings")).not.toBeNull();
  });

  it("offers help only when an admin has published a help document", async () => {
    await openMenu({ helpAvailable: false });
    expect(menuItem("account-menu-help")).toBeNull();
    wrapper?.unmount();

    await openMenu({ helpAvailable: true });
    expect(menuItem("account-menu-help")).not.toBeNull();
  });

  it.each([
    ["account-menu-marketplace", "open-marketplace"],
    ["account-menu-help", "open-help"],
    ["account-menu-settings", "open-settings"],
  ])("emits %s's action when selected", async (testId, event) => {
    const menu = await openMenu({ helpAvailable: true });

    menuItem(testId)?.click();
    await flushPromises();

    expect(menu.emitted(event)).toHaveLength(1);
  });

  it("flags a pending upgrade on the trigger so it is visible while closed", () => {
    wrapper = mount(AccountMenu, { props: { name: "Lin Che", upgradeAvailable: true } });

    const trigger = wrapper.get('[data-testid="account-menu-trigger"]');
    expect(trigger.find('[data-testid="account-menu-upgrade-badge"]').exists()).toBe(true);
    expect(trigger.attributes("aria-label")).toBe("A new version of DayMug is available");
  });

  it("offers admin settings only to admins, below settings", async () => {
    await openMenu({ isAdmin: false });
    expect(menuItem("account-menu-admin-settings")).toBeNull();
    wrapper?.unmount();

    const menu = await openMenu({ isAdmin: true });
    const admin = menuItem("account-menu-admin-settings");
    const settings = menuItem("account-menu-settings");
    expect(admin).not.toBeNull();
    expect(
      settings!.compareDocumentPosition(admin!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();

    admin!.click();
    await flushPromises();
    expect(menu.emitted("open-admin-settings")).toHaveLength(1);
  });

  it("shows a pending upgrade only on the admin settings entry", async () => {
    await openMenu({ isAdmin: true, upgradeAvailable: true });

    expect(menuItem("account-menu-settings")?.querySelector(".bg-red-500")).toBeNull();
    expect(menuItem("account-menu-admin-upgrade-badge")).not.toBeNull();
  });

  it("lists running tasks directly above settings with a live count", async () => {
    await openMenu({
      runningConversations: [
        { conversation_id: "c1", agent_id: "a1", agent_name: "Bot", started_at: "" },
      ],
      queuedConversations: [
        { conversation_id: "c2", agent_id: "a1", agent_name: "Bot", started_at: "" },
      ],
    });

    const activity = menuItem("account-menu-activity");
    expect(activity?.nextElementSibling).toBe(menuItem("account-menu-settings"));
    expect(menuItem("account-menu-activity-count")?.textContent).toBe("2");

    activity!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    await flushPromises();
    const popup = document.body.querySelector('[data-testid="running-conversations-popup"]');
    expect(popup?.textContent).toContain("1 running · 1 queued");
    expect(popup?.querySelectorAll('[data-testid="running-conversation-row"]')).toHaveLength(1);
    expect(popup?.querySelectorAll('[data-testid="queued-conversation-row"]')).toHaveLength(1);
  });

  it("hides the count when nothing is running", async () => {
    await openMenu();
    expect(menuItem("account-menu-activity")).not.toBeNull();
    expect(menuItem("account-menu-activity-count")).toBeNull();
  });
});
