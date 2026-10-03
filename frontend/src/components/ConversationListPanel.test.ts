import { describe, it, expect, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import ConversationListPanel from "./ConversationListPanel.vue";

vi.mock("@/components/ui/context-menu", () => ({
  ContextMenu: { template: "<div><slot /></div>" },
  ContextMenuTrigger: { template: "<div><slot /></div>" },
  ContextMenuContent: { template: "<div><slot /></div>" },
  ContextMenuItem: {
    props: ["variant"],
    emits: ["select"],
    template: '<button data-slot="context-menu-item" @click="$emit(\'select\')"><slot /></button>',
  },
}));

const convDefaults = {
  user_id: "u1",
  work_dir: "/tmp",
  session_id: "",
  notifications_enabled: true,
  pinned: false,
  pin_order: 0,
  account_name: "",
  provider: "",
  model: "",
  created_at: "2026-04-06T10:00:00Z",
};

const conversations = [
  { id: "c1", title: "First chat", updated_at: new Date().toISOString(), ...convDefaults },
  { id: "c2", title: "", updated_at: "2026-04-05T10:00:00Z", ...convDefaults },
];

const baseProps = {
  conversations,
  currentConversationId: "c1",
  userId: "u1",
};

describe("ConversationListPanel", () => {
  it("marks a conversation's attention state in the row's trailing status slot", () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, conversationAttention: { c2: "error" } },
    });

    const rows = wrapper.findAll(".conv-row");
    expect(rows[0].find('[data-testid="unread-completion-conversation-dot"]').exists()).toBe(false);
    const dot = rows[1].get('[data-testid="unread-completion-conversation-dot"]');
    expect(dot.classes()).toEqual(expect.arrayContaining(["attention-dot", "rounded-full"]));
    expect(dot.attributes("data-attention")).toBe("error");
  });

  it("shows a spinner instead of the unread dot while a conversation is running", () => {
    const wrapper = mount(ConversationListPanel, {
      props: {
        ...baseProps,
        runningConversationIds: ["c2"],
        conversationAttention: { c2: "done" },
      },
    });

    const row = wrapper.findAll(".conv-row")[1];
    expect(row.find('[data-testid="conversation-running-spinner"]').exists()).toBe(true);
    expect(row.find('[data-testid="unread-completion-conversation-dot"]').exists()).toBe(false);
  });

  it("keeps the delete affordance hidden until the row is hovered", () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, currentConversationId: "c1" },
    });

    for (const btn of wrapper.findAll(".delete-btn")) {
      expect(btn.classes()).toEqual(
        expect.arrayContaining(["opacity-0", "group-hover:opacity-100"]),
      );
    }
  });

  it("starts with the unified DayMug workspace header", () => {
    const wrapper = mount(ConversationListPanel, {
      props: baseProps,
      attrs: {
        "user-name": "Alice",
        "user-avatar": "/alice.png",
        "user-work-dir": "/home/alice/project",
      },
    });

    expect(wrapper.text()).not.toContain("Alice");
    expect(wrapper.text()).not.toContain("/home/alice/project");
    expect(wrapper.findComponent({ name: "AgentAvatar" }).exists()).toBe(false);
    expect(wrapper.find(".conversation-panel").element.firstElementChild?.textContent).toContain(
      "DayMug",
    );
    expect(wrapper.text()).toContain("New conversation");
  });

  it("collapses the panel from its header", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });

    await wrapper.get('[data-testid="conversation-panel-collapse"]').trigger("click");

    expect(wrapper.emitted("toggle-conversations")).toHaveLength(1);
  });

  it("keeps the account name a label and routes the footer menu's actions upward", async () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, accountName: "Alice", accountUsername: "alice", helpAvailable: true },
    });

    // Clicking the identity must not navigate; only the menu does.
    await wrapper.get(".workspace-account-footer strong").trigger("click");
    expect(wrapper.emitted("open-settings")).toBeUndefined();

    const menu = wrapper.getComponent({ name: "AccountMenu" });
    expect(menu.props()).toMatchObject({
      name: "Alice",
      username: "alice",
      helpAvailable: true,
    });
    menu.vm.$emit("open-marketplace");
    menu.vm.$emit("open-help");
    menu.vm.$emit("open-settings");

    expect(wrapper.emitted("open-marketplace")).toHaveLength(1);
    expect(wrapper.emitted("open-help")).toHaveLength(1);
    expect(wrapper.emitted("open-settings")).toHaveLength(1);
  });

  it("renders conversation titles", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    expect(wrapper.text()).toContain("First chat");
    // Empty title should show fallback
    expect(wrapper.text()).toContain("New conversation");
  });

  it("filters the current agent's conversations by title", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify(conversations), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    const wrapper = mount(ConversationListPanel, { props: baseProps });

    await wrapper.get('[data-testid="conversation-search-toggle"]').trigger("click");
    const input = wrapper.get('[data-testid="conversation-search-input"]');
    await input.setValue("FIRST");
    await flushPromises();

    const rows = wrapper.findAll(".conv-row");
    expect(rows).toHaveLength(1);
    expect(rows[0].text()).toContain("First chat");
  });

  it("clears and closes conversation search with Escape", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify(conversations), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    const wrapper = mount(ConversationListPanel, { props: baseProps });

    await wrapper.get('[data-testid="conversation-search-toggle"]').trigger("click");
    const input = wrapper.get('[data-testid="conversation-search-input"]');
    await input.setValue("missing");
    await flushPromises();
    expect(wrapper.text()).toContain("No matching conversations");
    await input.trigger("keydown", { key: "Escape" });

    expect(wrapper.find('[data-testid="conversation-search-input"]').exists()).toBe(false);
    expect(wrapper.findAll(".conv-row")).toHaveLength(2);
  });

  it("marks every conversation row as one stable pointer surface", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    for (const row of wrapper.findAll(".conv-row")) {
      expect(row.attributes("data-cursor-surface")).toBe("pointer");
    }
  });

  it("scopes the stable pointer cursor to the panel while dragging", async () => {
    vi.useFakeTimers();
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    try {
      await wrapper.find(".conv-row").trigger("mousedown", { button: 0 });
      await vi.advanceTimersByTimeAsync(400);
      expect(wrapper.find(".conversation-panel").classes()).toContain("conversation-dragging");

      window.dispatchEvent(new MouseEvent("mouseup"));
      await wrapper.vm.$nextTick();
      expect(wrapper.find(".conversation-panel").classes()).not.toContain("conversation-dragging");
    } finally {
      wrapper.unmount();
      vi.useRealTimers();
    }
  });

  it("highlights active conversation", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const rows = wrapper.findAll(".conv-row");

    expect(rows[0].classes()).toEqual(expect.arrayContaining(["bg-white", "hover:bg-white"]));
    expect(rows[0].classes()).not.toContain("bg-transparent");
    expect(rows[1].classes()).toContain("bg-transparent");
  });

  it("marks the active conversation by surface alone, keeping every title at one weight", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const titles = wrapper.findAll(".conversation-title > div");

    for (const title of titles) {
      expect(title.classes()).not.toContain("font-medium");
    }
  });

  it("highlights the active conversation without a right-edge stripe", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const activeRow = wrapper.findAll(".conv-row")[0];

    expect(activeRow.classes()).toContain("bg-white");
    expect(activeRow.find('[data-testid="conv-active-stripe"]').exists()).toBe(false);
  });

  it("does not draw a bottom progress bar under running conversations", () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, runningConversationIds: ["c1"] },
    });

    expect(wrapper.find('[data-slot="avatar"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="running-progress"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="conversation-running-spinner"]').exists()).toBe(true);
  });

  it("emits select-session on click", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const items = wrapper.findAll(".group");
    await items[1].trigger("click");
    expect(wrapper.emitted("select-conversation")).toEqual([["c2"]]);
  });

  it("emits new-session on button click", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const newBtn = wrapper.find("button.w-full");
    await newBtn.trigger("click");
    expect(wrapper.emitted("new-conversation")).toBeTruthy();
  });

  it("disables the new conversation button for one second after clicking", async () => {
    vi.useFakeTimers();
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    try {
      const newBtn = wrapper.find("button.w-full");
      await newBtn.trigger("click");
      expect(wrapper.emitted("new-conversation")).toHaveLength(1);
      expect(newBtn.attributes("disabled")).toBeDefined();

      await newBtn.trigger("click");
      expect(wrapper.emitted("new-conversation")).toHaveLength(1);

      await vi.advanceTimersByTimeAsync(1000);
      await wrapper.vm.$nextTick();
      expect(newBtn.attributes("disabled")).toBeUndefined();

      await newBtn.trigger("click");
      expect(wrapper.emitted("new-conversation")).toHaveLength(2);
    } finally {
      wrapper.unmount();
      vi.useRealTimers();
    }
  });

  it("emits delete-conversation with skipConfirm=false on a plain trash click", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const deleteBtn = wrapper.find(".delete-btn");
    await deleteBtn.trigger("click");
    // App.vue inspects the skipConfirm arg to decide whether to confirm —
    // the panel itself stays "dumb" and just forwards the modifier state.
    expect(wrapper.emitted("delete-conversation")).toEqual([["c1", false]]);
  });

  it("vertically centers the trash button in the conversation row", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const slot = wrapper.find(".delete-btn").element.parentElement;

    expect(slot?.className).toContain("self-center");
    expect(wrapper.find(".delete-btn").classes()).not.toContain("mt-0.5");
  });

  it("emits delete-conversation with skipConfirm=true when the trash is primary-modifier-clicked", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const deleteBtn = wrapper.find(".delete-btn");
    await deleteBtn.trigger("click", { ctrlKey: true });
    expect(wrapper.emitted("delete-conversation")).toEqual([["c1", true]]);
  });

  it("enters edit mode on double-click and emits rename-session on Enter", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const titleArea = wrapper.findAll(".conversation-title")[0];
    await titleArea.trigger("dblclick");
    const input = wrapper.find(".rename-input");
    expect(input.exists()).toBe(true);
    await input.setValue("Renamed chat");
    await input.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("rename-conversation")).toEqual([["c1", "Renamed chat"]]);
  });

  it("cancels edit on Escape", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const titleArea = wrapper.findAll(".conversation-title")[0];
    await titleArea.trigger("dblclick");
    const input = wrapper.find(".rename-input");
    expect(input.exists()).toBe(true);
    await input.trigger("keydown", { key: "Escape" });
    expect(wrapper.find(".rename-input").exists()).toBe(false);
    expect(wrapper.emitted("rename-conversation")).toBeUndefined();
  });

  it("ignores Enter pressed mid-IME-composition (CJK input)", async () => {
    // IMEs use Enter to commit a candidate. Without the composition guard,
    // that Enter would close the rename and drop the in-flight characters.
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const titleArea = wrapper.findAll(".conversation-title")[0];
    await titleArea.trigger("dblclick");
    const input = wrapper.find(".rename-input");
    await input.trigger("compositionstart");
    await input.setValue("中文");
    // Enter while composing — should NOT commit.
    await input.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("rename-conversation")).toBeUndefined();
    expect(wrapper.find(".rename-input").exists()).toBe(true);
    // After composition ends, a real Enter commits.
    await input.trigger("compositionend");
    await input.trigger("keydown", { key: "Enter" });
    expect(wrapper.emitted("rename-conversation")).toEqual([["c1", "中文"]]);
  });

  it("ignores Enter when KeyboardEvent.isComposing is true", async () => {
    // Some browsers don't fire compositionend before the synthesized
    // keydown.enter that accepts a candidate, but they do set isComposing.
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const titleArea = wrapper.findAll(".conversation-title")[0];
    await titleArea.trigger("dblclick");
    const input = wrapper.find(".rename-input");
    await input.setValue("partial");
    await input.trigger("keydown", { key: "Enter", isComposing: true });
    expect(wrapper.emitted("rename-conversation")).toBeUndefined();
  });

  it("shows empty state when no conversations", () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, conversations: [] },
    });
    expect(wrapper.text()).toContain("No conversations yet");
  });

  it("removes the inline pin button from conversation rows", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    expect(wrapper.find(".pin-btn").exists()).toBe(false);
  });

  it("renders the conversation row context menu actions", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    expect(wrapper.text()).toContain("Pin to top");
    expect(wrapper.text()).toContain("Share");
    expect(wrapper.text()).toContain("Delete");
  });

  it("uses the same labeled swipe action tray as the mobile agent list", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });

    expect(wrapper.get(".swipe-share").text()).toBe("Share");
    expect(wrapper.get(".swipe-delete").text()).toBe("Delete");
    expect(wrapper.findAll(".swipe-action-icon")).toHaveLength(4);
  });

  it("renders unpin for an already-pinned conversation context action", () => {
    const wrapper = mount(ConversationListPanel, {
      props: {
        ...baseProps,
        conversations: [
          {
            id: "c1",
            title: "Pinned",
            updated_at: new Date().toISOString(),
            ...convDefaults,
            pinned: true,
          },
        ],
      },
    });
    expect(wrapper.text()).toContain("Unpin");
  });

  it("emits share-conversation from the context menu", async () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const shareItem = wrapper.findAll('[data-slot="context-menu-item"]').find((item) => {
      return item.text().includes("Share");
    });
    expect(shareItem?.exists()).toBe(true);
    await shareItem?.trigger("click");
    expect(wrapper.emitted("share-conversation")).toEqual([["c1"]]);
  });

  it("shows shared status beside the conversation time and emits unshare from the context menu", async () => {
    const wrapper = mount(ConversationListPanel, {
      props: {
        ...baseProps,
        conversations: [
          {
            id: "c1",
            title: "Shared",
            updated_at: new Date().toISOString(),
            ...convDefaults,
            shared: true,
          },
        ],
      },
    });
    const status = wrapper.find(".conversation-shared-status");
    expect(status.exists()).toBe(true);
    expect(status.attributes("aria-label")).toBe("Shared");
    const unshareItem = wrapper.findAll('[data-slot="context-menu-item"]').find((item) => {
      return item.text().includes("Unshare");
    });
    expect(unshareItem?.exists()).toBe(true);
    await unshareItem?.trigger("click");
    expect(wrapper.emitted("unshare-conversation")).toEqual([["c1"]]);
  });

  it("adds a copy share link action for already-shared conversations", async () => {
    const wrapper = mount(ConversationListPanel, {
      props: {
        ...baseProps,
        conversations: [
          {
            id: "c1",
            title: "Shared",
            updated_at: new Date().toISOString(),
            ...convDefaults,
            shared: true,
          },
        ],
      },
    });
    const copyItem = wrapper.findAll('[data-slot="context-menu-item"]').find((item) => {
      return item.text().includes("Copy share link");
    });
    expect(copyItem?.exists()).toBe(true);
    await copyItem?.trigger("click");
    expect(wrapper.emitted("copy-share-link")).toEqual([["c1"]]);
  });

  // The bundle carries the account-level config dir, which on codex enumerates
  // every project path on the host. Non-admins must not even see the entry.
  it("hides the diagnostics export from non-admins", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    const exportItem = wrapper.findAll('[data-slot="context-menu-item"]').find((item) => {
      return item.text().includes("diagnostics");
    });
    expect(exportItem).toBeUndefined();
  });

  it("emits export-bundle from the context menu for admins", async () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, isAdmin: true },
    });
    const exportItem = wrapper.findAll('[data-slot="context-menu-item"]').find((item) => {
      return item.text().includes("diagnostics");
    });
    expect(exportItem?.exists()).toBe(true);
    await exportItem?.trigger("click");
    expect(wrapper.emitted("export-bundle")).toEqual([["c1"]]);
  });

  it("shows pinned status beside the conversation time", () => {
    const wrapper = mount(ConversationListPanel, {
      props: {
        ...baseProps,
        conversations: [
          {
            id: "c1",
            title: "Pinned",
            updated_at: new Date().toISOString(),
            ...convDefaults,
            pinned: true,
          },
        ],
      },
    });
    const status = wrapper.find(".conversation-status");
    expect(status.exists()).toBe(true);
    expect(status.attributes("aria-label")).toBe("Pinned");
  });

  it("shows relative time for recent items", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    // First conversation has current time, so should show "just now" or similar
    const text = wrapper.text();
    // The second conversation is from yesterday-ish
    expect(text).toMatch(/ago|just now|yesterday|\d+d ago/);
  });

  it("hides the load-more button when there is no further page", () => {
    const wrapper = mount(ConversationListPanel, { props: baseProps });
    expect(wrapper.find('[data-testid="load-more-conversations"]').exists()).toBe(false);
  });

  it("emits load-more when there are more pages", async () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, hasMore: true },
    });
    const button = wrapper.find('[data-testid="load-more-conversations"]');
    expect(button.exists()).toBe(true);
    await button.trigger("click");
    expect(wrapper.emitted("load-more")).toHaveLength(1);
  });

  it("disables the load-more button while a page is in flight", () => {
    const wrapper = mount(ConversationListPanel, {
      props: { ...baseProps, hasMore: true, loadingMore: true },
    });
    const button = wrapper.find('[data-testid="load-more-conversations"]');
    expect(button.attributes("disabled")).toBeDefined();
  });
});
