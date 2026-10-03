import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";
import MobileAgentsScreen from "./MobileAgentsScreen.vue";
import type { Conversation, User } from "@/composables/useApi";

type ScreenProps = {
  users: User[];
  currentUser: User | null;
  conversationsByAgent: Record<string, Conversation[]>;
  loadingByAgent?: Record<string, boolean>;
  currentConversationId: string;
  refreshing?: boolean;
};

// Build a synthetic TouchEvent payload that satisfies the bits the
// component reads. happy-dom doesn't synthesise touch lists for us, so
// we hand-roll one and trigger via dispatchEvent.
function makeTouchEvent(type: string, clientY: number): TouchEvent {
  const event = new Event(type, { bubbles: true, cancelable: true }) as TouchEvent;
  const touch = { clientY } as Touch;
  Object.defineProperty(event, "touches", { value: [touch] });
  Object.defineProperty(event, "changedTouches", { value: [touch] });
  return event;
}

// Pin a deterministic vertical rect on a row so the reorder drop-index maths
// (which reads getBoundingClientRect) is testable under happy-dom.
function setRect(el: HTMLElement, top: number, height: number) {
  el.getBoundingClientRect = () =>
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
  work_dir: "",
  session_id: "",
  notifications_enabled: true,
  pinned: false,
  pin_order: 0,
  account_name: "",
  provider: "",
  model: "",
  created_at: "2026-05-01T00:00:00Z",
  updated_at: "2026-05-02T00:00:00Z",
  ...over,
});

function mountScreen(propsOverride: Partial<ScreenProps> = {}) {
  const props: ScreenProps = {
    users: [baseUser()],
    currentUser: null,
    conversationsByAgent: {},
    currentConversationId: "",
    ...propsOverride,
  };
  return mount(MobileAgentsScreen, { props });
}

describe("MobileAgentsScreen", () => {
  it("marks agent rows as stable pointer surfaces", () => {
    const wrapper = mountScreen({ users: [baseUser(), baseUser({ id: "u2", name: "Bob" })] });
    for (const row of wrapper.findAll(".agent-row")) {
      expect(row.attributes("data-cursor-surface")).toBe("pointer");
    }
  });

  it("does not render the DayMug title or logo in the header", () => {
    const wrapper = mountScreen();
    // The brand strip used to host a Mark logo + 'DayMug' wordmark; both are
    // gone now so the header reads as a clean 'Agents' screen.
    expect(wrapper.text()).not.toContain("DayMug");
    expect(wrapper.find('[data-testid="brand-mark"]').exists()).toBe(false);
  });

  it("keeps the settings affordance reachable from the header", async () => {
    const wrapper = mountScreen();
    const btn = wrapper.find('button[title="Settings"]');
    expect(btn.exists()).toBe(true);
    await btn.trigger("click");
    expect(wrapper.emitted("open-settings")).toBeTruthy();
  });

  it("emits select-agent when expanding a different agent row", async () => {
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapper = mountScreen({
      users: [a, b],
      currentUser: a,
      conversationsByAgent: { u1: [baseConversation()] },
      currentConversationId: "",
    });
    // Bob's row is collapsed; tapping it should ask the parent to load
    // his conversations into the per-agent cache (cheap no-op if the
    // cache is already populated).
    await wrapper.find('.agent-row[data-agent-id="u2"]').trigger("click");
    const events = wrapper.emitted("select-agent");
    expect(events).toBeTruthy();
    const last = events![events!.length - 1];
    expect(last[0]).toEqual(b);
  });

  it("does not surface a conversation-count line on agent rows", () => {
    // The count line used to appear/disappear on expand, jiggling the
    // avatar against the text block. The row must read as exactly two
    // lines (name + work_dir) regardless of cache state.
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapper = mountScreen({
      users: [a, b],
      currentUser: a,
      conversationsByAgent: {
        u1: [baseConversation({ id: "c1" }), baseConversation({ id: "c2" })],
      },
      currentConversationId: "c1",
    });
    expect(wrapper.text()).not.toMatch(/\d+ conversation\b/);
  });

  it("renders a loading placeholder when an expanded agent is mid-fetch", () => {
    const a = baseUser({ id: "u1", name: "Alice" });
    const wrapper = mountScreen({
      users: [a],
      currentUser: a,
      // Cache cleared by the parent (force-fresh path) — empty list +
      // loading flag means the inline panel must show "Loading…", not
      // the misleading "No conversations yet." placeholder.
      conversationsByAgent: { u1: [] },
      loadingByAgent: { u1: true },
      currentConversationId: "",
    });
    const list = wrapper.find(".agent-conversations");
    expect(list.exists()).toBe(true);
    expect(list.text()).toContain("Loading conversations");
    expect(list.text()).not.toContain("No conversations yet.");
  });

  it("expands the current user's row by default and renders its conversations inline", () => {
    const u = baseUser();
    const conv = baseConversation();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [conv] },
      currentConversationId: conv.id,
    });
    const list = wrapper.find(".agent-conversations");
    expect(list.exists()).toBe(true);
    expect(list.text()).toContain("Convo one");
    expect(wrapper.find('.agent-row[data-agent-id="u1"]').attributes("aria-expanded")).toBe("true");
  });

  it("collapses the inline conversation list when the already-expanded row is tapped again", async () => {
    const u = baseUser();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [baseConversation()] },
      currentConversationId: "c1",
    });
    expect(wrapper.find(".agent-conversations").exists()).toBe(true);

    await wrapper.find(".agent-row").trigger("click");
    expect(wrapper.find(".agent-conversations").exists()).toBe(false);
    // The auto-expand mount fires one initial select-agent for the
    // current user; collapsing the row must not fire another one.
    const events = wrapper.emitted("select-agent");
    expect(events?.length ?? 0).toBeLessThanOrEqual(1);
  });

  it("emits select-conversation with the owning agent when an inline conversation is tapped", async () => {
    const u = baseUser();
    const c = baseConversation();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [c] },
      currentConversationId: "",
    });
    await wrapper.find(".m-conversation-card").trigger("click");
    const events = wrapper.emitted("select-conversation");
    expect(events).toBeTruthy();
    expect(events![0][0]).toEqual(u);
    expect(events![0][1]).toBe(c.id);
  });

  it("emits new-conversation from the inline +New conversation button", async () => {
    const u = baseUser();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [] },
      currentConversationId: "",
    });
    const btns = wrapper
      .findAll(".agent-conversations button")
      .filter((b) => b.text().includes("New conversation"));
    expect(btns.length).toBe(1);
    await btns[0].trigger("click");
    const events = wrapper.emitted("new-conversation");
    expect(events).toBeTruthy();
    expect(events![0][0]).toEqual(u);
  });

  it("emits refresh with the expanded agent id after a pull past the trigger distance", async () => {
    const u = baseUser();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [baseConversation()] },
      currentConversationId: "c1",
    });
    const scroll = wrapper.find('[data-testid="agents-scroll"]');
    Object.defineProperty(scroll.element, "scrollTop", { value: 0, configurable: true });

    // Pull well past the 64px trigger (rubber-band halves it, so 200px
    // of finger travel yields ~96px of indicator — comfortably above).
    scroll.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    scroll.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    scroll.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    const events = wrapper.emitted("refresh");
    expect(events).toBeTruthy();
    expect(events![0][0]).toBe(u.id);
  });

  it("does not emit refresh when the pull is below the trigger distance", async () => {
    const u = baseUser();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [baseConversation()] },
      currentConversationId: "c1",
    });
    const scroll = wrapper.find('[data-testid="agents-scroll"]');
    Object.defineProperty(scroll.element, "scrollTop", { value: 0, configurable: true });

    // 40px of finger ≈ 20px of indicator (rubber-band) — under the 64px
    // commit threshold, so the release must be discarded.
    scroll.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    scroll.element.dispatchEvent(makeTouchEvent("touchmove", 140));
    scroll.element.dispatchEvent(makeTouchEvent("touchend", 140));
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("refresh")).toBeFalsy();
  });

  it("does not emit refresh while a refresh is already in flight", async () => {
    const u = baseUser();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [baseConversation()] },
      currentConversationId: "c1",
      refreshing: true,
    });
    const scroll = wrapper.find('[data-testid="agents-scroll"]');
    Object.defineProperty(scroll.element, "scrollTop", { value: 0, configurable: true });

    scroll.element.dispatchEvent(makeTouchEvent("touchstart", 100));
    scroll.element.dispatchEvent(makeTouchEvent("touchmove", 300));
    scroll.element.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("refresh")).toBeFalsy();
  });

  it("keeps the spinner pinned while refreshing is true", () => {
    const u = baseUser();
    const wrapper = mountScreen({
      users: [u],
      currentUser: u,
      conversationsByAgent: { u1: [baseConversation()] },
      currentConversationId: "c1",
      refreshing: true,
    });
    const indicator = wrapper.find('[data-testid="pull-indicator"]');
    expect(indicator.exists()).toBe(true);
    expect(indicator.text()).toContain("Refreshing");
    // The indicator should be visible (non-zero height) even without an
    // active touch — the parent owns the lifecycle until the refetch
    // resolves.
    const styleHeight = indicator.attributes("style") ?? "";
    expect(styleHeight).toMatch(/height:\s*\d+px/);
    expect(styleHeight).not.toMatch(/height:\s*0px/);
  });

  it("renders the human owner alongside agents, all draggable", () => {
    // /api/users returns the human owner alongside the caller's agents. This
    // screen shows both, and every row — owner included — exposes the drag
    // affordance so the owner can be reordered freely among the agents.
    const owner = baseUser({ id: "human", name: "Me", username: "owner" });
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapper = mountScreen({ users: [owner, a, b], currentUser: null });

    expect(wrapper.find('.agent-row[data-agent-id="human"]').exists()).toBe(true);
    const rows = wrapper.findAll(".agent-row-wrap");
    expect(rows.length).toBe(3);
    rows.forEach((r) => expect(r.attributes("data-agent-wrap")).toBeTruthy());
  });

  it("lets the human owner be dragged to a new slot among the agents", async () => {
    // Owner + agents all participate in reorder; dragging the owner row emits
    // the full interleaved order.
    const owner = baseUser({ id: "human", name: "Me", username: "owner" });
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const c = baseUser({ id: "u3", name: "Cara" });
    const wrapper = mountScreen({ users: [owner, a, b, c], currentUser: null });

    const rowWraps = wrapper.findAll(".agent-row-wrap");
    rowWraps.forEach((wrap, i) => setRect(wrap.element as HTMLElement, i * 30, 30));
    const ownerRow = rowWraps[0].element; // the human owner, top row

    vi.useFakeTimers();
    try {
      ownerRow.dispatchEvent(makeTouchEvent("touchstart", 10));
      vi.advanceTimersByTime(350);
      ownerRow.dispatchEvent(makeTouchEvent("touchmove", 80));
      ownerRow.dispatchEvent(makeTouchEvent("touchend", 80));
    } finally {
      vi.useRealTimers();
    }
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("reorder-agents")?.[0][0]).toEqual(["u1", "u2", "human", "u3"]);
  });

  it("reorders agents via a long-press drag and emits reorder-agents", async () => {
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const c = baseUser({ id: "u3", name: "Cara" });
    // No current user so no row auto-expands — keeps the rows uniform height.
    const wrapper = mountScreen({ users: [a, b, c], currentUser: null });

    // The gesture measures the row wrappers (which track the finger), so stub
    // deterministic rects on those.
    const rowWraps = wrapper.findAll(".agent-row-wrap");
    rowWraps.forEach((wrap, i) => setRect(wrap.element as HTMLElement, i * 30, 30));
    const first = rowWraps[0].element;

    vi.useFakeTimers();
    try {
      first.dispatchEvent(makeTouchEvent("touchstart", 10));
      // Hold past the long-press threshold to enter drag mode.
      vi.advanceTimersByTime(350);
      // Drag below the last row's midpoint, then release.
      first.dispatchEvent(makeTouchEvent("touchmove", 80));
      first.dispatchEvent(makeTouchEvent("touchend", 80));
    } finally {
      vi.useRealTimers();
    }
    await wrapper.vm.$nextTick();

    const events = wrapper.emitted("reorder-agents");
    expect(events).toBeTruthy();
    expect(events![0][0]).toEqual(["u2", "u3", "u1"]);
  });

  it("a long-press on the first row never arms pull-to-refresh, so it stays draggable", async () => {
    // Regression: the first agent sits at scrollTop 0, the only place pull-to-
    // refresh arms. A tiny downward drift during the long-press used to engage
    // the pull, which grew the indicator and pushed the whole rail down — the
    // reorder then captured offset geometry and the row felt immovable. The pull
    // must stay dormant while a reorder is even pending.
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const c = baseUser({ id: "u3", name: "Cara" });
    const wrapper = mountScreen({ users: [a, b, c], currentUser: null });

    const scroll = wrapper.find('[data-testid="agents-scroll"]');
    Object.defineProperty(scroll.element, "scrollTop", { value: 0, configurable: true });
    const rowWraps = wrapper.findAll(".agent-row-wrap");
    rowWraps.forEach((wrap, i) => setRect(wrap.element as HTMLElement, i * 30, 30));
    const first = rowWraps[0].element;

    vi.useFakeTimers();
    try {
      first.dispatchEvent(makeTouchEvent("touchstart", 10));
      // Small downward drift (< the 8px cancel tolerance) while holding: this is
      // what used to leak into the pull. The indicator must stay at zero height.
      first.dispatchEvent(makeTouchEvent("touchmove", 13));
      await wrapper.vm.$nextTick();
      const indicator = wrapper.find('[data-testid="pull-indicator"]');
      expect(indicator.attributes("style") ?? "").toMatch(/height:\s*0px/);

      // The long-press still matures into a clean drag of the first row.
      vi.advanceTimersByTime(350);
      first.dispatchEvent(makeTouchEvent("touchmove", 80));
      first.dispatchEvent(makeTouchEvent("touchend", 80));
    } finally {
      vi.useRealTimers();
    }
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("reorder-agents")?.[0][0]).toEqual(["u2", "u3", "u1"]);
    expect(wrapper.emitted("refresh")).toBeFalsy();
  });

  it("still pulls-to-refresh when a row is swiped down without holding", async () => {
    // The other side of the disambiguation: a quick downward swipe on a row
    // (movement past the cancel tolerance before the long-press fires) makes the
    // reorder bow out, and the pull resumes with the full finger travel intact.
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapper = mountScreen({ users: [a, b], currentUser: null });

    const scroll = wrapper.find('[data-testid="agents-scroll"]');
    Object.defineProperty(scroll.element, "scrollTop", { value: 0, configurable: true });
    const first = wrapper.findAll(".agent-row-wrap")[0].element;

    first.dispatchEvent(makeTouchEvent("touchstart", 100));
    first.dispatchEvent(makeTouchEvent("touchmove", 130)); // 30px > tolerance -> reorder bows out
    first.dispatchEvent(makeTouchEvent("touchmove", 300)); // pull travels past the trigger
    first.dispatchEvent(makeTouchEvent("touchend", 300));
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted("refresh")).toBeTruthy();
    expect(wrapper.emitted("reorder-agents")).toBeFalsy();
  });

  it("does not emit reorder-agents for a quick tap (expands instead)", async () => {
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapper = mountScreen({ users: [a, b], currentUser: null });

    await wrapper.find('.agent-row[data-agent-id="u2"]').trigger("click");

    expect(wrapper.emitted("reorder-agents")).toBeFalsy();
    expect(wrapper.emitted("select-agent")).toBeTruthy();
  });

  it("renders each agent's own conversation list when multiple rows are expanded over time", async () => {
    const a = baseUser({ id: "u1", name: "Alice" });
    const b = baseUser({ id: "u2", name: "Bob" });
    const wrapper = mountScreen({
      users: [a, b],
      currentUser: a,
      conversationsByAgent: {
        u1: [baseConversation({ id: "c-a", title: "Alice convo", user_id: "u1" })],
        u2: [baseConversation({ id: "c-b", title: "Bob convo", user_id: "u2" })],
      },
      currentConversationId: "c-a",
    });

    // Alice (current) is auto-expanded with her own list.
    let conversationBlocks = wrapper.findAll(".agent-conversations");
    expect(conversationBlocks.length).toBe(1);
    expect(conversationBlocks[0].text()).toContain("Alice convo");
    expect(conversationBlocks[0].text()).not.toContain("Bob convo");

    // Expanding Bob swaps the inline list (accordion behaviour) and
    // shows Bob's own conversations sourced from the per-agent map.
    await wrapper.find('.agent-row[data-agent-id="u2"]').trigger("click");
    conversationBlocks = wrapper.findAll(".agent-conversations");
    expect(conversationBlocks.length).toBe(1);
    expect(conversationBlocks[0].text()).toContain("Bob convo");
    expect(conversationBlocks[0].text()).not.toContain("Alice convo");
  });
});
