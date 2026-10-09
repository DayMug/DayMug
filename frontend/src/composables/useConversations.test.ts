import { describe, it, expect, vi, beforeEach } from "vitest";

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

vi.stubGlobal("localStorage", {
  getItem: vi.fn().mockReturnValue(null),
  setItem: vi.fn(),
  removeItem: vi.fn(),
  clear: vi.fn(),
});

const broadcastChannels: FakeBroadcastChannel[] = [];
class FakeBroadcastChannel {
  name: string;
  onmessage: ((event: MessageEvent) => void) | null = null;
  postMessage = vi.fn((message: unknown) => structuredClone(message));

  constructor(name: string) {
    this.name = name;
    broadcastChannels.push(this);
  }
}

vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);

const mockFetchConversations = vi.fn().mockResolvedValue([]);
// Two cases below call mockCreateConversation.mockReset() to install a
// deferred implementation, which also drops the default resolution. Keep the
// default in a const so beforeEach can put it back and the suite stays
// order-independent.
const NEW_CONVERSATION_ROW = {
  id: "conv-new",
  title: "",
  user_id: "u1",
  work_dir: "/tmp",
  session_id: "",
  notifications_enabled: false,
  pinned: false,
  pin_order: 0,
  account_name: "",
  provider: "",
  model: "",
  created_at: "",
  updated_at: "",
};
const mockCreateConversation = vi.fn().mockResolvedValue(NEW_CONVERSATION_ROW);
const mockDeleteConversation = vi.fn().mockResolvedValue(undefined);
const mockRenameConversation = vi.fn().mockResolvedValue({
  id: "c1",
  title: "Renamed",
  user_id: "u1",
  work_dir: "/tmp",
  session_id: "",
  notifications_enabled: false,
  pinned: false,
  pin_order: 0,
  account_name: "",
  provider: "",
  model: "",
  created_at: "",
  updated_at: "",
});
const mockUpdateConversationNotifications = vi.fn().mockResolvedValue({
  id: "c1",
  title: "First",
  user_id: "u1",
  work_dir: "/tmp",
  session_id: "",
  notifications_enabled: true,
  pinned: false,
  pin_order: 0,
  account_name: "",
  provider: "",
  model: "",
  created_at: "",
  updated_at: "",
});

vi.mock("./useApi", () => ({
  fetchConversations: (...args: unknown[]) => mockFetchConversations(...args),
  // Sidebar load paths ask for a page; the suite predates paging and drives
  // everything through mockFetchConversations, so both land on the same mock.
  // Head loads call it with just the user id, "load more" adds the offset.
  fetchConversationsPage: (...args: unknown[]) => mockFetchConversations(...args),
  // Two-row pages keep the paging cases readable; the composable only uses
  // this to decide "was that window full?".
  CONVERSATION_PAGE_SIZE: 2,
  createConversation: (...args: unknown[]) => mockCreateConversation(...args),
  deleteConversation: (...args: unknown[]) => mockDeleteConversation(...args),
  renameConversation: (...args: unknown[]) => mockRenameConversation(...args),
  updateConversationNotifications: (...args: unknown[]) =>
    mockUpdateConversationNotifications(...args),
  fetchMessages: vi.fn().mockResolvedValue([]),
  fetchConversation: vi.fn().mockResolvedValue({
    id: "c1",
    title: "",
    user_id: "u1",
    work_dir: "/tmp",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    provider: "",
    model: "",
    created_at: "",
    updated_at: "",
  }),
  updateConversationWorkDir: vi.fn().mockResolvedValue({}),
}));

vi.mock("./useWebSocket", () => ({
  useWebSocket: () => ({
    isConnected: { value: true },
    reconnectAttempts: { value: 0 },
    connect: vi.fn(),
    disconnect: vi.fn(),
    send: vi.fn(),
    onMessage: vi.fn(),
    onReconnect: vi.fn(),
  }),
}));

import { useConversations } from "./useConversations";
import { resetActiveConversationStore } from "@/stores/activeConversationStore";
import { resetConversationListStore } from "@/stores/conversationListStore";
import { RECENT_MODEL_STORAGE_KEY } from "@/lib/recentModel";

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(localStorage.getItem).mockReturnValue(null);
  broadcastChannels.forEach((channel) => channel.postMessage.mockClear());
  mockCreateConversation.mockReset().mockResolvedValue(NEW_CONVERSATION_ROW);
  // Both layers are global stores, so every case starts from the store's
  // post-boot values rather than hand-picking refs — the in-flight
  // create-guard used to survive into the next test and silently swallow
  // its POST.
  resetConversationListStore();
  resetActiveConversationStore();
});

describe("useConversations", () => {
  it("loadConversations fetches and auto-selects first", async () => {
    const convs = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
      {
        id: "c2",
        title: "Second",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockFetchConversations.mockResolvedValueOnce(convs);

    const { loadConversations, conversations } = useConversations();
    await loadConversations("u1");
    expect(conversations.value).toEqual(convs);
    expect(mockFetchConversations).toHaveBeenCalledWith("u1");
  });

  it("loadConversations creates new session when empty", async () => {
    mockFetchConversations.mockResolvedValueOnce([]);

    const { loadConversations, conversations } = useConversations();
    await loadConversations("u1");
    expect(mockCreateConversation).toHaveBeenCalledWith("u1");
    expect(conversations.value.length).toBe(1);
  });

  it("loadConversations flips the per-agent loading flag for the duration of the fetch", async () => {
    // Without this, MobileAgentsScreen briefly renders the inline list
    // with `loading=false` and `conversations=[]` on fresh page load —
    // the AgentConversationList empty-state then shows "No conversations
    // yet." even when the user actually has conversations and we're just
    // waiting on the GET.
    let resolveFetch: (value: unknown[]) => void = () => {};
    mockFetchConversations.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFetch = resolve as (value: unknown[]) => void;
        }),
    );

    const { loadConversations, loadingAgentIds } = useConversations();
    const inFlight = loadConversations("u1");

    // Flag must be on while the request is outstanding so the
    // AgentConversationList renders "Loading conversations…" instead of
    // the empty-state placeholder.
    expect(loadingAgentIds.value["u1"]).toBe(true);

    resolveFetch([
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ]);
    await inFlight;

    // Cleared once the list lands — conversations.length > 0 hides the
    // placeholder anyway, but leaving the flag dangling would mislead
    // any future reader of the cache state.
    expect(loadingAgentIds.value["u1"]).toBeUndefined();
  });

  it("loadConversations clears the loading flag even when fetchConversations throws", async () => {
    mockFetchConversations.mockRejectedValueOnce(new Error("network"));

    const { loadConversations, loadingAgentIds } = useConversations();
    await loadConversations("u1");

    expect(loadingAgentIds.value["u1"]).toBeUndefined();
  });

  it("createNewConversation prepends to list", async () => {
    const { createNewConversation, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Old",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockCreateConversation.mockResolvedValueOnce({
      id: "c2",
      title: "",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      created_at: "",
      updated_at: "",
    });

    await createNewConversation("u1");
    expect(conversations.value[0].id).toBe("c2");
    expect(conversations.value.length).toBe(2);
  });

  it("deleteConversation removes from list", async () => {
    const { deleteConversation, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
      {
        id: "c2",
        title: "Second",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    await deleteConversation("c1");
    expect(conversations.value.length).toBe(1);
    expect(conversations.value[0].id).toBe("c2");
  });

  it("deleteConversation debounces concurrent calls on the same id", async () => {
    // The trash icon lives on every list (sidebar / agents / mobile) and
    // a fast double-click currently fires two DELETEs. The composable
    // tracks in-flight ids so the second call short-circuits.
    const { deleteConversation, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    let resolveDelete: (value?: unknown) => void = () => {};
    mockDeleteConversation.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveDelete = resolve;
        }),
    );

    const first = deleteConversation("c1");
    const second = deleteConversation("c1");
    const third = deleteConversation("c1");

    // The second + third calls must short-circuit synchronously without
    // queueing another DELETE behind the in-flight one.
    expect(mockDeleteConversation).toHaveBeenCalledTimes(1);

    resolveDelete();
    await Promise.all([first, second, third]);

    // Once the first delete settles the id is freed and a fresh attempt
    // would go through again — confirming we're tracking in-flight, not
    // permanently blacklisting the id.
    mockDeleteConversation.mockResolvedValueOnce(undefined);
    await deleteConversation("c1");
    expect(mockDeleteConversation).toHaveBeenCalledTimes(2);
  });

  it("toggleConversationPanel toggles state and persists", () => {
    const { isConversationPanelOpen, toggleConversationPanel } = useConversations();
    const initial = isConversationPanelOpen.value;
    toggleConversationPanel();
    expect(isConversationPanelOpen.value).toBe(!initial);
    expect(localStorage.setItem).toHaveBeenCalledWith(
      "daymug-conversation-panel",
      String(!initial),
    );
  });

  it("renameConversation updates conversation in list", async () => {
    const { renameConversation, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Old",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockRenameConversation.mockResolvedValueOnce({
      id: "c1",
      title: "Renamed",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      created_at: "",
      updated_at: "",
    });

    await renameConversation("c1", "Renamed");
    expect(mockRenameConversation).toHaveBeenCalledWith("c1", "Renamed");
    expect(conversations.value[0].title).toBe("Renamed");
  });

  it("startNewConversation prepends a new conversation rooted at the agent's default work_dir, leaving the old one in the list", async () => {
    const { useChat } = await import("./useChat");
    const { currentConversationId, conversationWorkDir } = useChat();
    currentConversationId.value = "c1";
    // The previous conversation drifted into a subdir, but a fresh
    // conversation must NOT inherit it — it should reset to the agent's
    // root (signalled by passing no work_dir; the backend defaults to
    // the user's configured work_dir).
    conversationWorkDir.value = "/home/alice/project/deep/subdir";

    const { startNewConversation, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Old",
        user_id: "u1",
        work_dir: "/home/alice/project/deep/subdir",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockCreateConversation.mockResolvedValueOnce({
      id: "c2",
      title: "",
      user_id: "u1",
      work_dir: "/home/alice",
      session_id: "",
      created_at: "",
      updated_at: "",
    });

    await startNewConversation("u1");

    // Frontend passes no work_dir override — the backend's Create handler
    // defaults to the owning user's configured work_dir.
    expect(mockCreateConversation).toHaveBeenCalledWith("u1");
    // The previous session stays in the list (no archiving anymore).
    expect(conversations.value.length).toBe(2);
    expect(conversations.value[0].id).toBe("c2");
    expect(conversations.value[1].id).toBe("c1");
  });

  it("toggleConversationNotifications optimistically flips and persists via the API", async () => {
    const { toggleConversationNotifications, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockUpdateConversationNotifications.mockResolvedValueOnce({
      id: "c1",
      title: "First",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: true,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    await toggleConversationNotifications("c1", true);

    expect(mockUpdateConversationNotifications).toHaveBeenCalledWith("c1", true);
    expect(conversations.value[0].notifications_enabled).toBe(true);
  });

  it("toggleConversationNotifications reverts when the API call fails", async () => {
    const { toggleConversationNotifications, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockUpdateConversationNotifications.mockRejectedValueOnce(new Error("boom"));

    await toggleConversationNotifications("c1", true);

    expect(conversations.value[0].notifications_enabled).toBe(false);
  });

  // The index captured before the await goes stale whenever the list is
  // re-sorted or spliced while the request is in flight — the reply would then
  // overwrite whichever conversation happens to sit at that slot.
  it("toggleConversationNotifications writes back by id after the list re-orders", async () => {
    const row = (id: string, title: string) => ({
      id,
      title,
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });
    const { toggleConversationNotifications, conversations } = useConversations();
    conversations.value = [row("c1", "First"), row("c2", "Second")];

    let resolveUpdate: (v: unknown) => void = () => {};
    mockUpdateConversationNotifications.mockImplementationOnce(
      () => new Promise((resolve) => (resolveUpdate = resolve)),
    );
    const pending = toggleConversationNotifications("c1", true);

    // Something re-orders the sidebar mid-flight (peer-tab update, pin, …).
    conversations.value = [conversations.value[1], conversations.value[0]];
    resolveUpdate({ ...row("c1", "First"), notifications_enabled: true });
    await pending;

    expect(conversations.value.find((c) => c.id === "c1")?.notifications_enabled).toBe(true);
    expect(conversations.value.find((c) => c.id === "c2")?.notifications_enabled).toBe(false);
  });

  // Slow fetch for agent A must not repaint the sidebar after the user has
  // already switched to agent B.
  it("loadConversations ignores a response superseded by a newer load", async () => {
    const row = (id: string, userId: string) => ({
      id,
      title: id,
      user_id: userId,
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });
    let resolveFirst: (v: unknown[]) => void = () => {};
    mockFetchConversations
      .mockImplementationOnce(() => new Promise((resolve) => (resolveFirst = resolve)))
      .mockResolvedValueOnce([row("b1", "u2")]);

    const { loadConversations, conversations, activeAgentId } = useConversations();
    const slow = loadConversations("u1");
    await loadConversations("u2");

    resolveFirst([row("a1", "u1")]);
    await slow;

    expect(conversations.value.map((c) => c.id)).toEqual(["b1"]);
    expect(activeAgentId.value).toBe("u2");
  });

  it("applyTitleUpdate updates a matching conversation in place", () => {
    const { applyTitleUpdate, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
      {
        id: "c2",
        title: "Other",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    applyTitleUpdate("c1", "Auto-Generated");

    expect(conversations.value[0].title).toBe("Auto-Generated");
    expect(conversations.value[1].title).toBe("Other");
  });

  it("applyTitleUpdate is a no-op for unknown ids", () => {
    const { applyTitleUpdate, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Keep",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    applyTitleUpdate("missing", "should not apply");

    expect(conversations.value[0].title).toBe("Keep");
  });

  it("refreshConversations re-fetches without side effects", async () => {
    const convs = [
      {
        id: "c1",
        title: "Updated",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockFetchConversations.mockResolvedValueOnce(convs);

    const { refreshConversations, conversations } = useConversations();
    await refreshConversations("u1");
    expect(conversations.value).toEqual(convs);
  });

  it("refreshConversationsForAgent always re-fetches and updates the per-agent cache", async () => {
    // Pull-to-refresh on the mobile Agents screen must hit the network
    // even when the agent's list is already cached — otherwise the
    // user's explicit refresh gesture would be a no-op.
    const { conversationsByAgent, refreshConversationsForAgent } = useConversations();
    conversationsByAgent.value = {
      u1: [
        {
          id: "stale",
          title: "Stale",
          user_id: "u1",
          work_dir: "/tmp",
          session_id: "",
          notifications_enabled: false,
          pinned: false,
          pin_order: 0,
          account_name: "",
          provider: "",
          model: "",
          created_at: "",
          updated_at: "",
        },
      ],
    };
    const fresh = [
      {
        id: "fresh",
        title: "Fresh",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockFetchConversations.mockResolvedValueOnce(fresh);

    await refreshConversationsForAgent("u1");

    expect(mockFetchConversations).toHaveBeenCalledWith("u1");
    expect(conversationsByAgent.value.u1).toEqual(fresh);
  });

  it("refreshConversationsForAgent rebinds the active list when refreshing the active agent", async () => {
    // When the agent being refreshed is the same one whose list is
    // currently materialised in conversations.value, the active list
    // must also reflect the fresh data — otherwise the chat sidebar
    // would still show stale rows after the pull-to-refresh.
    const { activeAgentId, conversations, refreshConversationsForAgent } = useConversations();
    activeAgentId.value = "u1";
    conversations.value = [
      {
        id: "stale",
        title: "Stale",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    const fresh = [
      {
        id: "fresh",
        title: "Fresh",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    mockFetchConversations.mockResolvedValueOnce(fresh);

    await refreshConversationsForAgent("u1");
    expect(conversations.value).toEqual(fresh);
  });

  it("refreshConversationsForAgent flips the per-agent loading flag for the duration of the fetch", async () => {
    let resolveFetch: (value: unknown[]) => void = () => {};
    mockFetchConversations.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFetch = resolve as (value: unknown[]) => void;
        }),
    );

    const { refreshConversationsForAgent, loadingAgentIds } = useConversations();
    const inFlight = refreshConversationsForAgent("u1");
    expect(loadingAgentIds.value["u1"]).toBe(true);

    resolveFetch([]);
    await inFlight;
    expect(loadingAgentIds.value["u1"]).toBeUndefined();
  });

  it("applyRemoteAdded prepends a new row received from a peer tab", () => {
    const { applyRemoteAdded, conversations, conversationsByAgent } = useConversations();
    conversations.value = [
      {
        id: "existing",
        title: "Already here",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    conversationsByAgent.value = { u1: [...conversations.value] };

    applyRemoteAdded({
      id: "fresh",
      title: "From peer",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    expect(conversations.value.length).toBe(2);
    expect(conversations.value[0].id).toBe("fresh");
    expect(conversationsByAgent.value.u1?.[0]?.id).toBe("fresh");
  });

  it("applyRemoteAdded merges in place when the id already exists locally", () => {
    const { applyRemoteAdded, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Old",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    applyRemoteAdded({
      id: "c1",
      title: "Renamed",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: true,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    expect(conversations.value.length).toBe(1);
    expect(conversations.value[0].title).toBe("Renamed");
    expect(conversations.value[0].notifications_enabled).toBe(true);
  });

  it("applyRemoteUpdated patches the matching row when present", () => {
    const { applyRemoteUpdated, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Old",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    applyRemoteUpdated({
      id: "c1",
      title: "Updated",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: true,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    expect(conversations.value[0].title).toBe("Updated");
    expect(conversations.value[0].notifications_enabled).toBe(true);
  });

  it("createNewConversation is idempotent when the user-hub broadcast already inserted the row", async () => {
    // Race scenario: the originator is subscribed to its own user-hub
    // room, so a `conversation_added` broadcast may land before the POST
    // response and `applyRemoteAdded` will have already inserted the row.
    // The optimistic insert in `createNewConversation` must not duplicate it.
    const { createNewConversation, applyRemoteAdded, conversations } = useConversations();
    conversations.value = [];

    const newConv = {
      id: "c2",
      title: "",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "2026-05-05T10:00:00Z",
    };
    mockCreateConversation.mockResolvedValueOnce(newConv);

    // Simulate the racing WS event landing first.
    applyRemoteAdded(newConv);
    expect(conversations.value.length).toBe(1);

    // Then the POST response arrives — should NOT duplicate.
    await createNewConversation("u1");
    expect(conversations.value.length).toBe(1);
    expect(conversations.value[0].id).toBe("c2");
  });

  it("startNewConversation uses the remembered provider/model when available", async () => {
    vi.mocked(localStorage.getItem).mockImplementation((key: string) =>
      key === RECENT_MODEL_STORAGE_KEY
        ? JSON.stringify({ provider: "codex", model: "gpt-5.5", think_level: "low" })
        : null,
    );
    mockCreateConversation.mockResolvedValueOnce({
      id: "c-recent",
      title: "",
      user_id: "u1",
      work_dir: "/home/alice",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "codex",
      model: "gpt-5.5",
      created_at: "",
      updated_at: "",
    });

    const { startNewConversation } = useConversations();
    await startNewConversation("u1");

    expect(mockCreateConversation).toHaveBeenCalledWith(
      "u1",
      undefined,
      "codex",
      "gpt-5.5",
      undefined,
      "low",
    );
  });

  it("startNewConversation preserves the remembered account for account-scoped models", async () => {
    vi.mocked(localStorage.getItem).mockImplementation((key: string) =>
      key === RECENT_MODEL_STORAGE_KEY
        ? JSON.stringify({ provider: "codex", model: "qwen3-coder", account: "codex-qwen" })
        : null,
    );
    mockCreateConversation.mockResolvedValueOnce({
      id: "c-recent-account",
      title: "",
      user_id: "u1",
      work_dir: "/home/alice",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      provider: "codex",
      model: "qwen3-coder",
      account_name: "codex-qwen",
      created_at: "",
      updated_at: "",
    });

    const { startNewConversation } = useConversations();
    await startNewConversation("u1");

    expect(mockCreateConversation).toHaveBeenCalledWith(
      "u1",
      undefined,
      "codex",
      "qwen3-coder",
      "codex-qwen",
      undefined,
    );
  });

  it("startNewConversation falls back to server defaults when the remembered model is rejected", async () => {
    vi.mocked(localStorage.getItem).mockImplementation((key: string) =>
      key === RECENT_MODEL_STORAGE_KEY
        ? JSON.stringify({ provider: "codex", model: "retired-model" })
        : null,
    );
    mockCreateConversation
      .mockRejectedValueOnce(new Error("create conversation: 400"))
      .mockResolvedValueOnce({
        id: "c-default",
        title: "",
        user_id: "u1",
        work_dir: "/home/alice",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "claude",
        model: "claude-opus-4-8",
        created_at: "",
        updated_at: "",
      });

    const { startNewConversation } = useConversations();
    await startNewConversation("u1");

    expect(mockCreateConversation).toHaveBeenNthCalledWith(
      1,
      "u1",
      undefined,
      "codex",
      "retired-model",
      undefined,
      undefined,
    );
    expect(mockCreateConversation).toHaveBeenNthCalledWith(2, "u1");
  });

  it("createNewConversation for one agent does not leak into a different agent's sidebar mid-switch", async () => {
    // Reported repro: while a "+ new" POST for agentA is in flight the
    // user switches to agentB. loadConversations replaces conversations.value
    // with agentB's list; then the POST response lands. Without per-agent
    // gating the new row would splice into agentB's sidebar until the next
    // hard refresh wiped it.
    const { loadConversations, createNewConversation, conversations, conversationsByAgent } =
      useConversations();
    const agentBRow = {
      id: "b1",
      title: "B's only",
      user_id: "agentB",
      work_dir: "/tmp/b",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "2026-05-05T09:00:00Z",
    };

    let resolveCreate: (v: unknown) => void = () => {};
    mockCreateConversation.mockReset();
    mockCreateConversation.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveCreate = resolve;
        }),
    );

    // 1. POST for agentA is dispatched but pending.
    const creating = createNewConversation("agentA");

    // 2. User switches to agentB before the POST resolves; conversations.value
    //    now reflects agentB's list and activeAgentId tracks agentB.
    mockFetchConversations.mockResolvedValueOnce([agentBRow]);
    await loadConversations("agentB");
    expect(conversations.value.map((c) => c.id)).toEqual(["b1"]);

    // 3. POST response for agentA arrives.
    resolveCreate({
      id: "a-new",
      title: "",
      user_id: "agentA",
      work_dir: "/tmp/a",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "2026-05-05T10:00:00Z",
    });
    await creating;

    // agentB's sidebar stays untouched — no ghost agentA row.
    expect(conversations.value.map((c) => c.id)).toEqual(["b1"]);
    // The agentB bucket also stays untouched.
    expect(conversationsByAgent.value["agentB"].map((c) => c.id)).toEqual(["b1"]);
    // And we definitely didn't pollute agentB's bucket with agentA's row.
    expect(conversationsByAgent.value["agentB"].some((c) => c.user_id === "agentA")).toBe(false);
  });

  it("createNewConversation ignores concurrent calls while one is in flight", async () => {
    // Reproduces the "double-click on + New Conversation makes two rows"
    // bug. Two callers fire before the POST resolves; only the first
    // should hit the API and only one row should land in the list.
    const { createNewConversation, conversations, isCreatingConversation } = useConversations();
    conversations.value = [];
    mockCreateConversation.mockReset();

    let resolveCreate: (v: unknown) => void = () => {};
    mockCreateConversation.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveCreate = resolve;
        }),
    );

    const first = createNewConversation("u1");
    expect(isCreatingConversation.value).toBe(true);
    // Second call lands while the first is still pending — should no-op.
    const second = createNewConversation("u1");

    resolveCreate({
      id: "c-once",
      title: "",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    });

    await Promise.all([first, second]);
    expect(mockCreateConversation).toHaveBeenCalledTimes(1);
    expect(conversations.value.length).toBe(1);
    expect(isCreatingConversation.value).toBe(false);
  });

  it("applyRemoteUpdated re-sorts the list by updated_at DESC so a freshly-bumped row jumps to the top", () => {
    // Models the common "peer tab finished a turn" event: the active
    // conversation in another tab just had its `updated_at` bumped by
    // SaveMessage, and the broadcast carries the new timestamp. The
    // sidebar must reorder so the row sits at the top — without this,
    // peer tabs only catch up on the next REST refresh.
    const { applyRemoteUpdated, conversations, conversationsByAgent } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Recent",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "2026-05-05T11:00:00Z",
      },
      {
        id: "c2",
        title: "Older",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "2026-05-05T10:00:00Z",
      },
    ];
    conversationsByAgent.value = { u1: [...conversations.value] };

    // c2 just had its updated_at bumped past c1's.
    applyRemoteUpdated({
      id: "c2",
      title: "Older",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "2026-05-05T12:00:00Z",
    });

    expect(conversations.value[0].id).toBe("c2");
    expect(conversations.value[1].id).toBe("c1");
    expect(conversationsByAgent.value.u1?.[0]?.id).toBe("c2");
  });

  it("applyRemoteAdded re-sorts so a freshly inserted row lands at its canonical position", () => {
    // A peer-tab `conversation_added` (or the originator's racing copy)
    // must end up sorted by updated_at DESC, not blindly at the head —
    // otherwise an older session created elsewhere could displace a
    // newer one on top.
    const { applyRemoteAdded, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Recent",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "2026-05-05T12:00:00Z",
      },
    ];

    applyRemoteAdded({
      id: "c2",
      title: "Older peer-created",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "2026-05-05T10:00:00Z",
    });

    expect(conversations.value.length).toBe(2);
    expect(conversations.value[0].id).toBe("c1");
    expect(conversations.value[1].id).toBe("c2");
  });

  it("applyRemoteAdded breaks an equal updated_at tie by created_at so the newer row wins", () => {
    // Two conversations whose updated_at coincide (second-resolution
    // server-side) must order by created_at DESC, so the more recently
    // created one stays on top instead of landing in SQLite's undefined order.
    const { applyRemoteAdded, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Created earlier",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "2026-05-05T12:00:00Z",
        updated_at: "2026-05-05T12:00:05Z",
      },
    ];

    applyRemoteAdded({
      id: "c2",
      title: "Created later",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "2026-05-05T12:00:02Z",
      updated_at: "2026-05-05T12:00:05Z",
    });

    expect(conversations.value.map((c) => c.id)).toEqual(["c2", "c1"]);
  });

  it("applyRemoteRemoved drops the row and reports whether it was present", () => {
    const { applyRemoteRemoved, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
      {
        id: "c2",
        title: "Second",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    expect(applyRemoteRemoved("c1")).toBe(true);
    expect(conversations.value.length).toBe(1);
    expect(conversations.value[0].id).toBe("c2");
    expect(applyRemoteRemoved("never-existed")).toBe(false);
  });

  it("broadcasts confirmed conversation updates to peer browser tabs", () => {
    const { applyRemoteUpdated, conversations } = useConversations();
    const updated = {
      id: "c1",
      title: "Updated",
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: "",
      updated_at: "",
    };
    conversations.value = [{ ...updated, title: "Old" }];

    applyRemoteUpdated(updated, { broadcast: true });

    expect(broadcastChannels[0].postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "updated",
        id: "c1",
        conversation: updated,
      }),
    );
  });

  it("broadcasts conversation rows read from reactive state as cloneable data", () => {
    const { applyRemoteUpdated, conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Updated",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    expect(() => applyRemoteUpdated(conversations.value[0], { broadcast: true })).not.toThrow();
  });

  it("applies conversation updates received from a peer browser tab", () => {
    const { conversations } = useConversations();
    conversations.value = [
      {
        id: "c1",
        title: "Old",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];

    broadcastChannels[0].onmessage?.({
      data: {
        source: "peer-tab",
        kind: "updated",
        id: "c1",
        conversation: {
          id: "c1",
          title: "From peer",
          user_id: "u1",
          work_dir: "/tmp",
          session_id: "",
          notifications_enabled: true,
          pinned: false,
          pin_order: 0,
          account_name: "",
          provider: "",
          model: "",
          created_at: "",
          updated_at: "",
        },
      },
    } as MessageEvent);

    expect(conversations.value[0].title).toBe("From peer");
    expect(conversations.value[0].notifications_enabled).toBe(true);
  });

  it("applyConversations seeds the list + active agent without hitting fetchConversations", async () => {
    // The /api/app-state aggregate carries the user's conversation list
    // inline, so the cold-start path can prime useConversations via this
    // entry point and skip the per-user GET /api/conversations round-trip.
    mockFetchConversations.mockClear();
    const list = [
      {
        id: "c1",
        title: "First",
        user_id: "u1",
        work_dir: "/tmp",
        session_id: "",
        notifications_enabled: false,
        pinned: false,
        pin_order: 0,
        account_name: "",
        provider: "",
        model: "",
        created_at: "",
        updated_at: "",
      },
    ];
    const { applyConversations, conversations, activeAgentId, conversationsByAgent } =
      useConversations();
    await applyConversations("u1", list);
    expect(mockFetchConversations).not.toHaveBeenCalled();
    expect(conversations.value).toEqual(list);
    expect(activeAgentId.value).toBe("u1");
    expect(conversationsByAgent.value["u1"]).toEqual(list);
  });

  // --- Paging -------------------------------------------------------------

  function convRow(id: string, updatedAt = "2026-01-01 00:00:00") {
    return {
      id,
      title: id,
      user_id: "u1",
      work_dir: "/tmp",
      session_id: "",
      notifications_enabled: false,
      pinned: false,
      pin_order: 0,
      account_name: "",
      provider: "",
      model: "",
      created_at: updatedAt,
      updated_at: updatedAt,
    };
  }

  it("loads the active agent's complete conversation list for search", async () => {
    const completeList = [convRow("c1"), convRow("c2"), convRow("c3")];
    mockFetchConversations.mockResolvedValueOnce(completeList);
    const {
      activeAgentId,
      conversations,
      conversationsByAgent,
      conversationsHasMore,
      loadAllConversationsForAgent,
    } = useConversations();
    activeAgentId.value = "u1";
    conversations.value = [completeList[0]];
    conversationsHasMore.value = true;

    await expect(loadAllConversationsForAgent("u1")).resolves.toBe(true);

    expect(mockFetchConversations).toHaveBeenCalledWith("u1");
    expect(conversations.value).toEqual(completeList);
    expect(conversationsByAgent.value.u1).toEqual(completeList);
    expect(conversationsHasMore.value).toBe(false);
  });

  it("loads a non-active agent's complete list without replacing the sidebar", async () => {
    const activeList = [convRow("active")];
    const otherList = [{ ...convRow("other"), user_id: "u2" }];
    mockFetchConversations.mockResolvedValueOnce(otherList);
    const { activeAgentId, conversations, conversationsByAgent, loadAllConversationsForAgent } =
      useConversations();
    activeAgentId.value = "u1";
    conversations.value = activeList;

    await expect(loadAllConversationsForAgent("u2")).resolves.toBe(true);

    expect(conversations.value).toEqual(activeList);
    expect(conversationsByAgent.value.u2).toEqual(otherList);
  });

  it("loadMoreConversations appends the next page instead of replacing the list", async () => {
    mockFetchConversations.mockResolvedValueOnce([
      convRow("c1", "2026-01-03 00:00:00"),
      convRow("c2", "2026-01-02 00:00:00"),
    ]);
    const { loadConversations, loadMoreConversations, conversations, conversationsHasMore } =
      useConversations();
    await loadConversations("u1");
    // A full window means the server had more rows to give.
    expect(conversationsHasMore.value).toBe(true);

    mockFetchConversations.mockResolvedValueOnce([convRow("c3", "2026-01-01 00:00:00")]);
    await loadMoreConversations();

    expect(conversations.value.map((c) => c.id)).toEqual(["c1", "c2", "c3"]);
    // A short window is proof the list is exhausted.
    expect(conversationsHasMore.value).toBe(false);
    // Offset is the rendered row count, not a stored cursor.
    expect(mockFetchConversations).toHaveBeenLastCalledWith("u1", 2);
  });

  it("loadMoreConversations drops rows already rendered instead of duplicating them", async () => {
    // updated_at keeps moving, so a row from window one can drift upward and
    // come back inside window two. Offset paging can't prevent that; the
    // append has to be idempotent.
    mockFetchConversations.mockResolvedValueOnce([convRow("c1"), convRow("c2")]);
    const { loadConversations, loadMoreConversations, conversations } = useConversations();
    await loadConversations("u1");

    mockFetchConversations.mockResolvedValueOnce([convRow("c2"), convRow("c3")]);
    await loadMoreConversations();

    expect(conversations.value.map((c) => c.id)).toEqual(["c1", "c2", "c3"]);
  });

  it("loadMoreConversations is a no-op when the last page was short", async () => {
    mockFetchConversations.mockResolvedValueOnce([convRow("c1")]);
    const { loadConversations, loadMoreConversations } = useConversations();
    await loadConversations("u1");
    mockFetchConversations.mockClear();

    await loadMoreConversations();
    expect(mockFetchConversations).not.toHaveBeenCalled();
  });

  it("loadMoreConversations ignores a second click while a page is in flight", async () => {
    mockFetchConversations.mockResolvedValueOnce([convRow("c1"), convRow("c2")]);
    const { loadConversations, loadMoreConversations, conversations } = useConversations();
    await loadConversations("u1");
    mockFetchConversations.mockClear();

    let resolveFetch: (value: unknown[]) => void = () => {};
    mockFetchConversations.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFetch = resolve as (value: unknown[]) => void;
        }),
    );
    const first = loadMoreConversations();
    const second = loadMoreConversations();
    expect(mockFetchConversations).toHaveBeenCalledTimes(1);

    resolveFetch([convRow("c3")]);
    await Promise.all([first, second]);
    expect(conversations.value.map((c) => c.id)).toEqual(["c1", "c2", "c3"]);
  });

  it("refreshConversations keeps already-paged rows instead of snapping back to page one", async () => {
    mockFetchConversations.mockResolvedValueOnce([
      convRow("c1", "2026-01-04 00:00:00"),
      convRow("c2", "2026-01-03 00:00:00"),
    ]);
    const {
      loadConversations,
      loadMoreConversations,
      refreshConversations,
      conversations,
      conversationsHasMore,
    } = useConversations();
    await loadConversations("u1");

    mockFetchConversations.mockResolvedValueOnce([
      convRow("c3", "2026-01-02 00:00:00"),
      convRow("c4", "2026-01-01 00:00:00"),
    ]);
    await loadMoreConversations();
    expect(conversations.value.map((c) => c.id)).toEqual(["c1", "c2", "c3", "c4"]);

    // An assistant result fires refreshConversations; it re-fetches window one.
    mockFetchConversations.mockResolvedValueOnce([
      convRow("c1", "2026-01-04 00:00:00"),
      convRow("c2", "2026-01-03 00:00:00"),
    ]);
    await refreshConversations("u1");

    expect(conversations.value.map((c) => c.id)).toEqual(["c1", "c2", "c3", "c4"]);
    expect(conversationsHasMore.value).toBe(true);
  });
});
