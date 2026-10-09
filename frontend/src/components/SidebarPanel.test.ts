import { afterEach, describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import SidebarPanel from "./SidebarPanel.vue";

const userDefaults = {
  username: "",
  email: "",
  is_admin: false,
  disabled: false,
  role_definition: "",
  mcp_config: "",
  claude_md_content: "",
  manage_claude_md: false,
  case_mode: false,
  sandbox_mode: "jailed" as const,
  sort_order: 0,
  archived: false,
  bark_url: "",
  pushdeer_key: "",
  notification_channel: "",
  created_at: "",
};

const users = [
  { id: "u1", name: "Alice", work_dir: "/home/alice", avatar: "A", ...userDefaults },
  { id: "u2", name: "Bob", work_dir: "/home/bob", avatar: "", ...userDefaults },
];

const currentUser = users[0];

const baseProps = {
  users,
  currentUser,
  usersLoaded: true,
};

function setRect(el: Element, top: number, height: number) {
  (el as HTMLElement).getBoundingClientRect = () =>
    ({
      top,
      height,
      bottom: top + height,
      left: 0,
      right: 0,
      width: 0,
      x: 0,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
}

function dispatchPointer(type: string, clientY: number) {
  const ev = new Event(type) as Event & { clientX: number; clientY: number };
  ev.clientX = 5;
  ev.clientY = clientY;
  window.dispatchEvent(ev);
}

afterEach(() => {
  vi.useRealTimers();
});

describe("SidebarPanel", () => {
  it("renders slim sidebar with avatars", () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    // The sidebar root div has fixed w-14 (56px) width classes
    const sidebar = wrapper.find("div");
    expect(sidebar.exists()).toBe(true);
    // Two avatar trigger divs rendered (one per user)
    const avatarTriggers = wrapper.findAll("[data-slot='avatar-fallback']");
    expect(avatarTriggers.length).toBe(2);
  });

  it("shows attached platforms without relabeling chat subjects as bots", () => {
    const passthrough = { template: "<div><slot /></div>" };
    const guard = {
      ...users[0],
      id: "guard",
      name: "Guard",
      bot_platforms: ["slack", "feishu"],
    };
    const ada = { ...users[1], id: "ada", name: "Ada" };
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, users: [guard, ada], currentUser: guard },
      global: {
        stubs: {
          Tooltip: passthrough,
          TooltipContent: passthrough,
          TooltipProvider: passthrough,
          TooltipTrigger: passthrough,
        },
      },
    });

    const guardRow = wrapper.get('[data-agent-id="guard"]');
    expect(guardRow.text()).toContain("Guard");
    // Platform names are translated, so the English locale reads "Feishu (Lark)"
    // rather than the Chinese brand name it used to hardcode.
    expect(guardRow.text()).toContain("Slack · Feishu (Lark)");
    expect(guardRow.text()).not.toContain("Bot");
    expect(guardRow.text()).not.toContain("Agent");

    const adaRow = wrapper.get('[data-agent-id="ada"]');
    expect(adaRow.text()).toContain("Ada");
    expect(adaRow.text()).not.toContain("Slack");
    expect(adaRow.text()).not.toContain("飞书");
    expect(adaRow.text()).not.toContain("Bot");
    expect(adaRow.text()).not.toContain("Agent");
  });

  it("vertically centers agent names and platform badges in the tooltip", () => {
    const passthrough = { template: "<div><slot /></div>" };
    const guard = {
      ...users[0],
      id: "guard",
      name: "Guard",
      bot_platforms: ["slack", "feishu"],
    };
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, users: [guard], currentUser: guard },
      global: {
        stubs: {
          Tooltip: passthrough,
          TooltipContent: passthrough,
          TooltipProvider: passthrough,
          TooltipTrigger: passthrough,
        },
      },
    });

    const guardRow = wrapper.get('[data-agent-id="guard"]');
    const name = guardRow.findAll("span").find((element) => element.text() === "Guard");
    const platforms = guardRow.get('[data-slot="badge"]');

    expect(name?.classes()).toEqual(expect.arrayContaining(["inline-flex", "h-5", "items-center"]));
    expect(platforms.classes()).toEqual(expect.arrayContaining(["h-5", "py-0", "leading-none"]));
  });

  it("prefixes an agent tooltip with its owner's name only when the names differ", () => {
    const passthrough = { template: "<div><slot /></div>" };
    const owner = {
      ...users[0],
      id: "owner",
      name: "Alice Chen",
      username: "alice",
      owner_id: "owner",
    };
    const distinctAgent = { ...users[1], id: "writer", name: "Writer", owner_id: "owner" };
    const sameNameAgent = { ...users[1], id: "alice-agent", name: "alice", owner_id: "owner" };
    const wrapper = mount(SidebarPanel, {
      props: {
        ...baseProps,
        users: [owner, distinctAgent, sameNameAgent],
        currentUser: distinctAgent,
      },
      global: {
        stubs: {
          Tooltip: passthrough,
          TooltipContent: passthrough,
          TooltipProvider: passthrough,
          TooltipTrigger: passthrough,
        },
      },
    });

    expect(wrapper.get('[data-agent-id="owner"]').text()).toContain("Alice Chen");
    expect(wrapper.get('[data-agent-id="writer"]').text()).toContain("alice/Writer");
    const sameNameRow = wrapper.get('[data-agent-id="alice-agent"]');
    expect(sameNameRow.text()).toContain("alice");
    expect(sameNameRow.text()).not.toContain("alice/alice");
  });

  it("highlights current user with a hairline on the avatar, not a ring on the logo", () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const items = wrapper.findAll("[data-agent-id]");

    expect(items[0].find(".agent-rail-avatar--active").exists()).toBe(true);
    expect(items[1].find(".agent-rail-avatar--active").exists()).toBe(false);
    for (const avatar of wrapper.findAll("[data-slot='avatar']")) {
      expect(avatar.classes().join(" ")).not.toContain("ring-");
    }
  });

  it("holds back the add-agent control until the roster has loaded", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, users: [], usersLoaded: false },
    });
    expect(wrapper.find('[data-testid="agent-rail-add"]').exists()).toBe(false);
  });

  it("draws the add-agent control as a labelled icon tile the size of an avatar", async () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const add = wrapper.get('[data-testid="agent-rail-add"]');

    expect(add.attributes("aria-label")).toBe("Add user");
    expect(add.find("svg").exists()).toBe(true);
    expect(add.text()).toBe("");
    expect(add.classes()).toEqual(expect.arrayContaining(["size-6", "rounded-[7px]"]));

    await add.trigger("click");
    expect(wrapper.emitted("add-user")).toHaveLength(1);
  });

  it("carries the connected tab over to the mobile bar", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, mobile: true },
    });
    const tabs = wrapper.findAll('[data-testid="agent-rail-tab"]');

    expect(tabs).toHaveLength(1);
    expect(tabs[0].classes()).toContain("agent-bar-tab");
  });

  it("marks desktop and mobile agent cells as stable pointer surfaces", () => {
    const desktop = mount(SidebarPanel, { props: baseProps });
    const mobile = mount(SidebarPanel, { props: { ...baseProps, mobile: true } });

    for (const item of desktop.findAll(".agent-rail-item")) {
      expect(item.attributes("data-cursor-surface")).toBe("pointer");
    }
    for (const item of mobile.findAll(".agent-bar-item")) {
      expect(item.attributes("data-cursor-surface")).toBe("pointer");
    }
  });

  it("keeps avatar hit areas free of background hover effects", () => {
    const desktop = mount(SidebarPanel, { props: baseProps });
    const desktopItems = desktop.findAll(".agent-rail-item");

    expect(desktopItems).toHaveLength(users.length);
    for (const item of desktopItems) {
      expect(item.classes()).not.toContain("hover:bg-sidebar-accent");
      expect(item.classes()).not.toContain("transition-colors");
    }

    const mobile = mount(SidebarPanel, {
      props: { ...baseProps, mobile: true },
    });
    const mobileItems = mobile
      .findAll("[data-slot='avatar']")
      .map((avatar) => avatar.element.closest(".cursor-pointer"));

    expect(mobileItems).toHaveLength(users.length);
    for (const item of mobileItems) {
      expect(item?.classList.contains("hover:bg-sidebar-accent")).toBe(false);
      expect(item?.classList.contains("transition-colors")).toBe(false);
    }
  });

  it("shows the running border only on running agents that are not selected", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, runningAgentIds: ["u1", "u2"] },
    });
    const items = wrapper.findAll("[data-agent-id]");

    expect(items[0].find(".agent-rail-avatar--active").exists()).toBe(true);
    expect(items[0].find('[data-testid="running-avatar-border"]').exists()).toBe(false);
    expect(items[1].find(".agent-rail-avatar--active").exists()).toBe(false);
    expect(items[1].find('[data-testid="running-avatar-border"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="agent-running-slider"]').exists()).toBe(false);
  });

  it("keeps the running border attached to its own avatar", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, runningAgentIds: ["u2"] },
    });
    const border = wrapper.get('[data-testid="running-avatar-border"]');

    expect(border.classes()).toContain("agent-running-border");
    expect(border.element.closest("[data-agent-id]")?.getAttribute("data-agent-id")).toBe("u2");
  });

  it("shows the same running border on the mobile agent bar", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, mobile: true, runningAgentIds: ["u2"] },
    });

    expect(wrapper.findAll('[data-testid="running-avatar-border"]')).toHaveLength(1);
  });

  it("keeps every rail cell the same size so avatar centres never move", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, runningAgentIds: ["u2"] },
    });

    for (const item of wrapper.findAll("[data-agent-id]")) {
      expect(item.classes()).toContain("size-6");
    }
  });

  it("emits select-user on avatar click", async () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    // Click the wrapper div around the second avatar
    const clickTargets = wrapper.findAll("[data-slot='avatar']");
    await clickTargets[1].element
      .closest("div[class*='cursor-pointer']")!
      .dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(wrapper.emitted("select-user")).toEqual([["u2"]]);
  });

  it("emits context-menu on right click", async () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const clickTargets = wrapper.findAll("[data-slot='avatar']");
    await clickTargets[0].element
      .closest("div[class*='cursor-pointer']")!
      .dispatchEvent(new MouseEvent("contextmenu", { bubbles: true }));
    expect(wrapper.emitted("context-menu")).toBeTruthy();
    const args = wrapper.emitted("context-menu")![0];
    expect(args[1]).toEqual(users[0]);
  });

  it("replaces the connection dot with an app marketplace button", async () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    expect(wrapper.find(".status-dot").exists()).toBe(false);
    await wrapper.get("[data-testid='marketplace-trigger']").trigger("click");
    expect(wrapper.emitted("open-marketplace")).toEqual([[]]);
  });

  it("emits open-settings on gear click", async () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    // The settings button is the last button in the sidebar footer area
    const buttons = wrapper.findAll("button");
    // buttons: one add-user + one settings; settings is last
    await buttons[buttons.length - 1].trigger("click");
    expect(wrapper.emitted("open-settings")).toBeTruthy();
  });

  it("sidebar has fixed 48px rail width", () => {
    // The rail is the sidebar's 48px left gutter: 12px padding, a 24px
    // avatar, 12px padding. The conversation panel bleeds its brand bar and
    // controls back over it, so this width is what aligns the two. The mobile
    // bottom bar branch is exercised via the `mobile` prop in other tests.
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const root = wrapper.find("div");
    expect(root.classes()).toContain("w-12");
    expect(root.classes()).toContain("min-w-12");
  });

  it("renders conversation toggle button", () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const toggle = wrapper.find(".conversations-toggle");
    expect(toggle.exists()).toBe(true);
  });

  it("emits toggle-conversations on toggle button click", async () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const toggle = wrapper.find(".conversations-toggle");
    await toggle.trigger("click");
    expect(wrapper.emitted("toggle-conversations")).toBeTruthy();
  });

  it("hands the collapse action to the expanded conversation panel", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, conversationPanelOpen: true },
    });
    expect(wrapper.find(".conversations-toggle").exists()).toBe(false);
  });

  it("hands the running-conversations trigger to the account menu while the panel is docked", () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, conversationPanelOpen: true, helpAvailable: true },
    });
    expect(wrapper.find("[data-testid='running-conversations-trigger']").exists()).toBe(false);
    expect(wrapper.find("[data-testid='marketplace-trigger']").exists()).toBe(false);
  });

  it("joins the tab to the conversation panel only while that panel is docked", () => {
    const open = mount(SidebarPanel, {
      props: { ...baseProps, conversationPanelOpen: true },
    });
    expect(open.find(".agent-rail-shell").classes()).toContain("agent-rail-shell--joined");

    const closed = mount(SidebarPanel, { props: baseProps });
    expect(closed.find(".agent-rail-shell").classes()).not.toContain("agent-rail-shell--joined");
  });

  it("hides help button when no help doc is configured", () => {
    const wrapper = mount(SidebarPanel, { props: baseProps });
    expect(wrapper.find("button[aria-label='Help']").exists()).toBe(false);
  });

  it("renders help button when helpAvailable is true and emits open-help", async () => {
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, helpAvailable: true },
    });
    const btn = wrapper.find("button[aria-label='Help']");
    expect(btn.exists()).toBe(true);
    await btn.trigger("click");
    expect(wrapper.emitted("open-help")).toBeTruthy();
  });

  it("shows aligned running conversation details with relative times captured on open", async () => {
    vi.useFakeTimers();
    vi.setSystemTime("2026-07-22T09:31:05Z");
    const passthrough = { template: "<div><slot /></div>" };
    const wrapper = mount(SidebarPanel, {
      props: {
        ...baseProps,
        helpAvailable: true,
        runningConversations: [
          {
            conversation_id: "conv-1",
            agent_id: "other-agent",
            agent_name: "Remote Agent",
            account_name: "alice-claude",
            model: "claude-sonnet-4-5",
            started_at: "2026-07-22T09:31:02Z",
          },
          {
            conversation_id: "conv-2",
            agent_id: "other-agent-2",
            agent_name: "Remote Agent 2",
            account_name: "bob-codex",
            model: "gpt-5.2-codex",
            started_at: "2026-07-22T09:30:00Z",
          },
        ],
      },
      global: {
        stubs: {
          Tooltip: passthrough,
          TooltipContent: passthrough,
          TooltipProvider: passthrough,
          TooltipTrigger: passthrough,
        },
      },
    });

    expect(
      wrapper.get("[data-testid='running-conversations-trigger']").attributes("aria-label"),
    ).toBe("2 running · 0 queued");
    const popup = wrapper.get("[data-testid='running-conversations-popup']");
    expect(popup.classes()).toContain("w-[440px]");
    expect(wrapper.get("[data-testid='running-conversations-table']").classes()).toContain(
      "text-left",
    );
    const rows = wrapper.findAll("[data-testid='running-conversation-row']");
    expect(rows[0].element.tagName).toBe("TR");
    expect(rows[0].findAll("td")[0].classes()).toContain("truncate");
    expect(rows[0].findAll("td")[0].attributes("title")).toBe("alice-claude");
    expect(rows[0].findAll("td")[1].classes()).toContain("truncate");
    expect(rows[0].findAll("td")[1].attributes("title")).toBe("claude-sonnet-4-5");
    expect(rows[0].findAll("td")[2].classes()).toContain("truncate");
    expect(rows[0].findAll("td")[2].attributes("title")).toBe("Remote Agent");
    expect(wrapper.get("[data-testid='running-time-header']").classes()).toContain("text-right");
    expect(wrapper.get("[data-testid='running-time-cell']").classes()).toContain("text-right");
    expect(rows[0].text()).toContain("alice-claude");
    expect(rows[0].text()).toContain("claude-sonnet-4-5");
    expect(rows[0].text()).toContain("Remote Agent");
    expect(rows[0].text()).toContain("3s ago");
    expect(rows[1].text()).toContain("1min ago");

    vi.setSystemTime("2026-07-22T09:36:05Z");
    await wrapper.setProps({ helpAvailable: true });
    expect(rows[0].text()).toContain("3s ago");
    expect(rows[1].text()).toContain("1min ago");
  });

  it("shows queued conversations in a separate status section", () => {
    vi.useFakeTimers();
    vi.setSystemTime("2026-07-22T09:31:05Z");
    const passthrough = { template: "<div><slot /></div>" };
    const wrapper = mount(SidebarPanel, {
      props: {
        ...baseProps,
        runningConversations: [
          {
            conversation_id: "conv-running",
            agent_id: "u1",
            agent_name: "Alice",
            account_name: "claude-main",
            model: "claude-sonnet-4-5",
            started_at: "2026-07-22T09:30:00Z",
          },
        ],
        queuedConversations: [
          {
            conversation_id: "conv-queued",
            agent_id: "u2",
            agent_name: "Bob",
            account_name: "codex-work",
            model: "gpt-5.6-sol",
            started_at: "2026-07-22T09:28:00Z",
          },
        ],
      },
      global: {
        stubs: {
          Tooltip: passthrough,
          TooltipContent: passthrough,
          TooltipProvider: passthrough,
          TooltipTrigger: passthrough,
        },
      },
    });

    expect(
      wrapper.get("[data-testid='running-conversations-trigger']").attributes("aria-label"),
    ).toBe("1 running · 1 queued");
    expect(wrapper.get("[data-testid='running-conversations-section-label']").text()).toBe(
      "Running1",
    );
    expect(wrapper.get("[data-testid='queued-conversations-section-label']").text()).toBe(
      "Queued1",
    );
    const queued = wrapper.get("[data-testid='queued-conversation-row']");
    expect(wrapper.get("[data-testid='queued-conversations-table']").classes()).toContain(
      "text-left",
    );
    expect(wrapper.get("[data-testid='queued-time-header']").classes()).toContain("text-right");
    expect(wrapper.get("[data-testid='queued-time-cell']").classes()).toContain("text-right");
    expect(queued.text()).toContain("codex-work");
    expect(queued.text()).toContain("gpt-5.6-sol");
    expect(queued.text()).toContain("Bob");
    expect(queued.text()).toContain("3min ago");
  });

  it("shows owner display name and agent name in the activity Agent column", () => {
    const passthrough = { template: "<div><slot /></div>" };
    const writer = { ...users[1], id: "writer", name: "Writer", owner_id: "owner" };
    const sameName = { ...users[1], id: "alice-agent", name: "alice", owner_id: "owner" };
    const activity = {
      account_name: "codex-main",
      model: "gpt-5.6-sol",
      started_at: "2026-07-22T09:30:00Z",
    };
    const wrapper = mount(SidebarPanel, {
      props: {
        ...baseProps,
        // The real /api/users response contains only owned agents, not the
        // signed-in human owner row. owner_username must therefore come from
        // the activity snapshot rather than a local owner lookup. The visible
        // user name wins over the login username, matching the rest of the UI.
        users: [writer, sameName],
        currentUser: writer,
        runningConversations: [
          {
            ...activity,
            conversation_id: "conv-running",
            agent_id: "writer",
            agent_name: "Writer",
            owner_name: "Alice User",
            owner_username: "alice",
          },
        ],
        queuedConversations: [
          {
            ...activity,
            conversation_id: "conv-queued",
            agent_id: "alice-agent",
            agent_name: "alice",
            owner_name: "alice",
            owner_username: "alice-login",
          },
        ],
      },
      global: {
        stubs: {
          Tooltip: passthrough,
          TooltipContent: passthrough,
          TooltipProvider: passthrough,
          TooltipTrigger: passthrough,
        },
      },
    });

    const runningAgentCell = wrapper.get("[data-testid='running-agent-cell']");
    const queuedAgentCell = wrapper.get("[data-testid='queued-agent-cell']");
    expect(runningAgentCell.text()).toBe("Alice User/Writer");
    expect(runningAgentCell.attributes("title")).toBe("Alice User/Writer");
    expect(queuedAgentCell.text()).toBe("alice");
    expect(queuedAgentCell.attributes("title")).toBe("alice");
  });

  it("opens on hover, is not closed by a click, and closes when the icon is left", async () => {
    const wrapper = mount(SidebarPanel, {
      attachTo: document.body,
      props: baseProps,
    });
    const trigger = wrapper.get("[data-testid='running-conversations-trigger']");

    expect(trigger.element.tagName).toBe("BUTTON");
    expect(trigger.text()).toBe("");
    await trigger.trigger("pointermove", { pointerType: "mouse" });
    await vi.waitFor(() => {
      expect(
        document.body.querySelector('[data-slot="tooltip-content"]:not([data-state="closed"])'),
      ).not.toBeNull();
    });

    await trigger.trigger("click");
    expect(
      document.body.querySelector('[data-slot="tooltip-content"]:not([data-state="closed"])'),
    ).not.toBeNull();
    expect(
      document.body.querySelector('[data-slot="tooltip-content"][data-inverted-surface]'),
    ).not.toBeNull();

    await trigger.trigger("pointerleave", { pointerType: "mouse" });
    await vi.waitFor(() => {
      expect(
        document.body.querySelector('[data-slot="tooltip-content"]:not([data-state="closed"])'),
      ).toBeNull();
    });
    wrapper.unmount();
  });

  it("opens the activity snapshot from a tap, with no hover involved", async () => {
    // reka-ui drops the tooltip's pointermove path for `pointerType: "touch"`,
    // so on a tablet — touch input, but a viewport wide enough for this rail —
    // hover can never happen and the panel had no way to open at all.
    const wrapper = mount(SidebarPanel, {
      attachTo: document.body,
      props: baseProps,
    });
    const trigger = wrapper.get("[data-testid='running-conversations-trigger']");

    await trigger.trigger("pointermove", { pointerType: "touch" });
    expect(
      document.body.querySelector('[data-slot="tooltip-content"]:not([data-state="closed"])'),
    ).toBeNull();

    await trigger.trigger("click");
    await vi.waitFor(() => {
      expect(
        document.body.querySelector('[data-slot="tooltip-content"]:not([data-state="closed"])'),
      ).not.toBeNull();
    });
    expect(trigger.attributes("aria-expanded")).toBe("true");
    wrapper.unmount();
  });

  it("renders the human owner alongside agents as a draggable row", () => {
    // /api/users returns the signed-in human owner (non-empty username)
    // alongside the caller's agents. The rail shows both, and every row —
    // owner included — is draggable, so the owner carries a data-agent-id.
    const owner = {
      id: "owner",
      name: "Owner",
      work_dir: "/home/owner",
      avatar: "O",
      ...userDefaults,
      username: "owner",
    };
    const wrapper = mount(SidebarPanel, {
      props: { ...baseProps, users: [owner, ...users] },
    });
    // Owner + two agents all render avatars and reorder hooks.
    expect(wrapper.findAll("[data-slot='avatar-fallback']").length).toBe(3);
    expect(wrapper.find('[data-agent-id="owner"]').exists()).toBe(true);
    expect(wrapper.find('[data-agent-id="u1"]').exists()).toBe(true);
  });

  it("emits reorder-agents after a long-press avatar drag", async () => {
    vi.useFakeTimers();
    const wrapper = mount(SidebarPanel, { props: baseProps });
    const first = wrapper.get('[data-agent-id="u1"]');
    const second = wrapper.get('[data-agent-id="u2"]');
    setRect(first.element, 0, 30);
    setRect(second.element, 30, 30);

    await first.trigger("pointerdown", {
      button: 0,
      pointerId: 1,
      clientX: 5,
      clientY: 10,
    });
    vi.advanceTimersByTime(350);
    dispatchPointer("pointermove", 50);
    expect(wrapper.emitted("reorder-agents")).toBeUndefined();
    dispatchPointer("pointerup", 50);

    expect(wrapper.emitted("reorder-agents")).toEqual([[["u2", "u1"]]]);
  });

  it("marks the held row with the active hairline instead of an outline", async () => {
    vi.useFakeTimers();
    const wrapper = mount(SidebarPanel, { props: baseProps });
    // u2 is not the current user, so any marker on it comes from the drag alone.
    const held = wrapper.get('[data-agent-id="u2"]');

    expect(held.find(".agent-rail-avatar--active").exists()).toBe(false);

    await held.trigger("pointerdown", { button: 0, pointerId: 1, clientX: 5, clientY: 40 });
    vi.advanceTimersByTime(350);
    await wrapper.vm.$nextTick();

    expect(held.find(".agent-rail-avatar--active").exists()).toBe(true);
    expect(held.classes().join(" ")).not.toContain("ring-");

    dispatchPointer("pointerup", 40);
  });
});
