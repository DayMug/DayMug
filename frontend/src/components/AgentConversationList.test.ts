import { describe, it, expect, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import AgentConversationList from "./AgentConversationList.vue";
import type { Conversation, User } from "@/composables/useApi";

const baseUser = (over: Partial<User> = {}): User => ({
  id: "u1",
  name: "Alice",
  username: "",
  email: "",
  is_admin: false,
  disabled: false,
  work_dir: "/home/alice/proj",
  avatar: "A",
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
  ...over,
});

const baseConversation = (over: Partial<Conversation> = {}): Conversation => ({
  id: "c1",
  user_id: "u1",
  title: "Convo one",
  provider: "",
  model: "",
  work_dir: "",
  session_id: "",
  notifications_enabled: true,
  pinned: false,
  pin_order: 0,
  account_name: "",
  created_at: "2026-05-01T00:00:00Z",
  updated_at: "2026-05-02T00:00:00Z",
  ...over,
});

describe("AgentConversationList", () => {
  it("shows unread completion only on the matching conversation title", () => {
    const wrapper = mount(AgentConversationList, {
      props: {
        user: baseUser(),
        conversations: [
          baseConversation({ id: "c1", title: "Alpha" }),
          baseConversation({ id: "c2", title: "Beta" }),
        ],
        currentConversationId: "c1",
        conversationAttention: { c2: "waiting" },
      },
    });

    const cards = wrapper.findAll(".m-conversation-card");
    expect(cards[0].find('[data-testid="unread-completion-conversation-dot"]').exists()).toBe(
      false,
    );
    const dot = cards[1].get('[data-testid="unread-completion-conversation-dot"]');
    expect(dot.classes()).toEqual(
      expect.arrayContaining(["absolute", "-left-1.5", "-top-0.5", "attention-dot"]),
    );
    expect(dot.attributes("data-attention")).toBe("waiting");
  });

  it("renders the supplied conversations under the agent panel", () => {
    const u = baseUser();
    const wrapper = mount(AgentConversationList, {
      props: {
        user: u,
        conversations: [
          baseConversation({ id: "c1", title: "Alpha" }),
          baseConversation({ id: "c2", title: "Beta" }),
        ],
        currentConversationId: "c1",
      },
    });
    const cards = wrapper.findAll(".m-conversation-card");
    expect(cards).toHaveLength(2);
    expect(cards[0].text()).toContain("Alpha");
    expect(cards[1].text()).toContain("Beta");
    expect(cards.every((card) => card.attributes("data-cursor-surface") === "pointer")).toBe(true);
  });

  it("filters only the expanded agent's conversations by title", async () => {
    const conversations = [
      baseConversation({ id: "c1", title: "Alpha plan" }),
      baseConversation({ id: "c2", title: "Beta notes" }),
    ];
    vi.mocked(fetch).mockResolvedValueOnce(
      new Response(JSON.stringify(conversations), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    const wrapper = mount(AgentConversationList, {
      props: {
        user: baseUser(),
        conversations,
        currentConversationId: "",
      },
    });

    await wrapper.get('[data-testid="conversation-search-toggle"]').trigger("click");
    await wrapper.get('[data-testid="conversation-search-input"]').setValue("BETA");
    await flushPromises();

    const cards = wrapper.findAll(".m-conversation-card");
    expect(cards).toHaveLength(1);
    expect(cards[0].text()).toContain("Beta notes");
    expect(wrapper.text()).not.toContain("Alpha plan");
  });

  it("emits select-conversation with the owning agent and conversation id when a card is tapped", async () => {
    const u = baseUser();
    const c = baseConversation({ id: "c-tapped" });
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [c], currentConversationId: "" },
    });
    await wrapper.find(".m-conversation-card").trigger("click");
    const events = wrapper.emitted("select-conversation");
    expect(events).toBeTruthy();
    expect(events![0][0]).toEqual(u);
    expect(events![0][1]).toBe("c-tapped");
  });

  it("emits new-conversation for the owning agent", async () => {
    const u = baseUser({ id: "u-new" });
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [], currentConversationId: "" },
    });
    const btn = wrapper.findAll("button").find((b) => b.text().includes("New conversation"));
    expect(btn).toBeTruthy();
    await btn!.trigger("click");
    const events = wrapper.emitted("new-conversation");
    expect(events).toBeTruthy();
    expect(events![0][0]).toEqual(u);
  });

  it("shows the pointer cursor on the new conversation button", () => {
    const wrapper = mount(AgentConversationList, {
      props: { user: baseUser(), conversations: [], currentConversationId: "" },
    });
    const btn = wrapper
      .findAll("button")
      .find((button) => button.text().includes("New conversation"));

    expect(btn?.classes()).toContain("cursor-pointer");
  });

  it("scopes the stable pointer cursor to the agent panel while dragging", async () => {
    vi.useFakeTimers();
    const wrapper = mount(AgentConversationList, {
      props: {
        user: baseUser(),
        conversations: [baseConversation()],
        currentConversationId: "",
      },
    });
    try {
      await wrapper.find(".m-conversation-card").trigger("mousedown", { button: 0 });
      await vi.advanceTimersByTimeAsync(400);
      expect(wrapper.find(".agent-conversations").classes()).toContain("conversation-dragging");

      window.dispatchEvent(new MouseEvent("mouseup"));
      await wrapper.vm.$nextTick();
      expect(wrapper.find(".agent-conversations").classes()).not.toContain("conversation-dragging");
    } finally {
      wrapper.unmount();
      vi.useRealTimers();
    }
  });

  it("animates the new conversation button on hover and press", () => {
    const wrapper = mount(AgentConversationList, {
      props: { user: baseUser(), conversations: [], currentConversationId: "" },
    });
    const btn = wrapper
      .findAll("button")
      .find((button) => button.text().includes("New conversation"));

    expect(btn?.classes()).toEqual(
      expect.arrayContaining([
        "transition-[transform,box-shadow]",
        "hover:-translate-y-0.5",
        "hover:shadow-md",
        "active:scale-[0.98]",
        "motion-reduce:transition-none",
      ]),
    );
  });

  it("disables the new conversation button for one second after clicking", async () => {
    vi.useFakeTimers();
    const u = baseUser({ id: "u-new" });
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [], currentConversationId: "" },
    });
    try {
      const btn = wrapper.findAll("button").find((b) => b.text().includes("New conversation"));
      expect(btn).toBeTruthy();
      await btn!.trigger("click");
      expect(wrapper.emitted("new-conversation")).toHaveLength(1);
      expect(btn!.attributes("disabled")).toBeDefined();

      await btn!.trigger("click");
      expect(wrapper.emitted("new-conversation")).toHaveLength(1);

      await vi.advanceTimersByTimeAsync(1000);
      await wrapper.vm.$nextTick();
      expect(btn!.attributes("disabled")).toBeUndefined();

      await btn!.trigger("click");
      expect(wrapper.emitted("new-conversation")).toHaveLength(2);
    } finally {
      wrapper.unmount();
      vi.useRealTimers();
    }
  });

  it("renders the empty placeholder when the agent has no conversations", () => {
    const u = baseUser();
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [], currentConversationId: "" },
    });
    expect(wrapper.text()).toContain("No conversations yet.");
  });

  it("renders a loading placeholder while a fetch is in flight", () => {
    // While the parent is fetching this agent's conversations we must
    // not show "No conversations yet." — that misleads the user into
    // thinking the agent is empty when really the data just hasn't
    // arrived yet. (Was the trigger for the perceived "previous
    // agent's conversations" flash on the mobile Agents screen.)
    const u = baseUser();
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [], currentConversationId: "", loading: true },
    });
    expect(wrapper.text()).toContain("Loading conversations");
    expect(wrapper.text()).not.toContain("No conversations yet.");
  });

  it("deletes via the swipe-revealed action button, skipping the confirm prompt", async () => {
    const u = baseUser();
    const c = baseConversation({ id: "c-del" });
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [c], currentConversationId: "" },
    });
    await wrapper.find(".swipe-delete").trigger("click");
    const events = wrapper.emitted("delete-conversation");
    expect(events).toBeTruthy();
    // The swipe + tap is the confirmation, so the second arg (skip-confirm) is
    // true — App.vue must not gate this on a window.confirm that mobile
    // in-app browsers silently suppress.
    expect(events![0]).toEqual(["c-del", true]);
  });

  it("shares via the swipe-revealed action button when the conversation is private", async () => {
    const u = baseUser();
    const c = baseConversation({ id: "c-share" });
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [c], currentConversationId: "" },
    });
    await wrapper.find(".swipe-share").trigger("click");
    expect(wrapper.emitted("share-conversation")).toEqual([["c-share"]]);
  });

  it("renders compact labeled swipe actions instead of solid color slabs", () => {
    const wrapper = mount(AgentConversationList, {
      props: {
        user: baseUser(),
        conversations: [baseConversation()],
        currentConversationId: "",
      },
    });

    expect(wrapper.get(".swipe-share").text()).toBe("Share");
    expect(wrapper.get(".swipe-delete").text()).toBe("Delete");
    expect(wrapper.findAll(".swipe-action-icon")).toHaveLength(2);
    expect(wrapper.get(".swipe-share").classes()).not.toContain("bg-primary");
  });

  it("unshares via the swipe-revealed action button when the conversation is shared", async () => {
    const u = baseUser();
    const c = baseConversation({ id: "c-share", shared: true });
    const wrapper = mount(AgentConversationList, {
      props: { user: u, conversations: [c], currentConversationId: "" },
    });
    expect(wrapper.find(".conversation-shared-status").exists()).toBe(true);
    await wrapper.find(".swipe-share").trigger("click");
    expect(wrapper.emitted("unshare-conversation")).toEqual([["c-share"]]);
  });

  it("shows pin status beside the conversation time", () => {
    const u = baseUser();
    const wrapper = mount(AgentConversationList, {
      props: {
        user: u,
        conversations: [
          baseConversation({ id: "c1", title: "Plain", pinned: false }),
          baseConversation({ id: "c2", title: "Pinned", pinned: true }),
        ],
        currentConversationId: "",
      },
    });
    const cards = wrapper.findAll(".m-conversation-card");
    expect(cards[0].find(".conversation-status").exists()).toBe(false);
    const status = cards[1].find(".conversation-status");
    expect(status.exists()).toBe(true);
    expect(status.attributes("aria-label")).toBe("Pinned");
  });

  it("scopes editing state to its own DOM tree", async () => {
    // Two sibling instances (one per agent) live alongside each other
    // when expansion is allowed for multiple rows. Editing state in
    // panel A must never reach into panel B.
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapperA = mount(AgentConversationList, {
      attachTo: document.body,
      props: {
        user: a,
        conversations: [baseConversation({ id: "ca", title: "A-orig" })],
        currentConversationId: "",
      },
    });
    const wrapperB = mount(AgentConversationList, {
      attachTo: document.body,
      props: {
        user: b,
        conversations: [baseConversation({ id: "cb", user_id: "u2", title: "B-orig" })],
        currentConversationId: "",
      },
    });

    await wrapperA.find(".m-conversation-card span.flex-1").trigger("dblclick");
    expect(wrapperA.find("input.m-rename-input").exists()).toBe(true);
    // Panel B must remain in its read-only display state — the rename
    // input belongs to A only.
    expect(wrapperB.find("input.m-rename-input").exists()).toBe(false);

    wrapperA.unmount();
    wrapperB.unmount();
  });
});
