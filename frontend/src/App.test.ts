import { describe, it, expect, vi, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import { ref } from "vue";

// Stub the app-state composables wholesale — App.vue's only goal in onMounted
// that we care about here is the post-load redirect logic, which depends on
// `users`, `currentUser`, `currentConversationId`, `loadAuthMe`, `loadUsers`,
// and `selectUser`. Everything else is presentational state we can default.
const mockUsers = ref([{ id: "user-1", name: "Alice" } as { id: string; name: string }]);
const mockCurrentUser = ref<{ id: string; name: string } | null>(null);
const mockCurrentConversationId = ref<string | null>(null);
const mockIsMobile = ref(false);
const mockIsNarrow = ref(false);
const mockIsConversationPanelOpen = ref(false);
const mockIsConversationDrawerOpen = ref(false);
const mockActiveAgentIds = ref<string[]>([]);
const mockActiveConversationIds = ref<string[]>([]);
const mockLoadAuthMe = vi.fn(async () => {});
const mockLoadUsers = vi.fn(async () => {});
const mockRefreshConversations = vi.fn(async () => {});
const mockSelectUser = vi.fn(async (uid: string) => {
  mockCurrentUser.value = { id: uid, name: "Alice" };
  // Simulate the real selectUser behaviour: as soon as it resolves the
  // user has a "current" conversation. The bug fired when this watcher
  // observed a non-null id and bounced the route to /chat.
  mockCurrentConversationId.value = "conv-1";
});

vi.mock("@/composables/useTheme", () => ({
  useTheme: () => ({ isDark: ref(false), toggleTheme: vi.fn() }),
}));

vi.mock("@/composables/useAuth", () => ({
  useAuth: () => ({
    authMe: ref(null),
    loadAuthMe: mockLoadAuthMe,
    applyAuthMe: vi.fn(),
    clearAuthMe: vi.fn(),
  }),
}));

vi.mock("@/composables/useUsers", () => ({
  useUsers: () => ({
    users: mockUsers,
    currentUser: mockCurrentUser,
    loadUsers: mockLoadUsers,
    applyUsers: vi.fn(),
    selectUser: mockSelectUser,
    handleArchiveUser: vi.fn(),
    handleReorderUsers: vi.fn(),
  }),
}));

vi.mock("@/composables/useChat", () => ({
  useChat: () => ({
    isConnected: ref(true),
    disconnectWs: vi.fn(),
    currentConversationId: mockCurrentConversationId,
    activeAgentIds: mockActiveAgentIds,
    activeConversationIds: mockActiveConversationIds,
    runningConversations: ref([]),
    queuedConversations: ref([]),
  }),
}));

vi.mock("@/composables/useConversations", () => ({
  useConversations: () => ({
    conversations: ref([]),
    conversationsByAgent: ref({}),
    conversationsHasMore: ref(false),
    isLoadingMoreConversations: ref(false),
    isCreatingConversation: ref(false),
    loadingAgentIds: ref({}),
    loadMoreConversations: vi.fn(),
    loadAllConversationsForAgent: vi.fn(async () => true),
    loadConversationsForAgent: vi.fn(),
    applyConversations: vi.fn(),
    refreshConversations: mockRefreshConversations,
    refreshConversationsForAgent: vi.fn(),
    isConversationPanelOpen: mockIsConversationPanelOpen,
    toggleConversationPanel: vi.fn(),
    isConversationDrawerOpen: mockIsConversationDrawerOpen,
    toggleConversationDrawer: vi.fn(() => {
      mockIsConversationDrawerOpen.value = !mockIsConversationDrawerOpen.value;
    }),
    closeConversationDrawer: vi.fn(() => {
      mockIsConversationDrawerOpen.value = false;
    }),
    selectConversation: vi.fn(),
    startNewConversation: vi.fn(),
    deleteConversation: vi.fn(),
    renameConversation: vi.fn(),
    toggleConversationNotifications: vi.fn(),
    toggleConversationPinned: vi.fn(),
    reorderPinned: vi.fn(),
    applyRemoteUpdated: vi.fn(),
    applyRemoteRemoved: vi.fn(),
  }),
}));

vi.mock("@/composables/useDocumentTitle", () => ({
  useDocumentTitle: () => {},
}));

vi.mock("@/composables/useIsMobile", () => ({
  useIsMobile: () => ({ isMobile: mockIsMobile }),
  useIsNarrow: () => ({ isNarrow: mockIsNarrow }),
}));

vi.mock("@/composables/useApi", () => ({
  fetchAppState: vi.fn().mockRejectedValue(new Error("app-state unavailable in unit test")),
}));

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

const memStore: Record<string, string> = {};
vi.stubGlobal("localStorage", {
  getItem: (k: string) => memStore[k] ?? null,
  setItem: (k: string, v: string) => {
    memStore[k] = v;
  },
  removeItem: (k: string) => {
    delete memStore[k];
  },
  clear: () => {
    for (const k of Object.keys(memStore)) delete memStore[k];
  },
});

import App from "./App.vue";
import { resetAppChromeStore } from "@/stores/appChromeStore";

function makeRouter(initialPath: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "home", component: { template: "<div>home</div>" } },
      {
        path: "/conversations",
        name: "conversations",
        component: { template: "<div>conversations</div>" },
      },
      {
        path: "/chat/:userId?/:conversationId?",
        name: "chat",
        component: { template: "<div>chat</div>" },
      },
      {
        path: "/login",
        name: "login",
        component: { template: "<div>login</div>" },
        meta: { layout: "bare" },
      },
      {
        path: "/settings/system",
        name: "settings-system",
        component: { template: "<div>settings-system</div>" },
      },
    ],
  });
  router.push(initialPath);
  return router;
}

beforeEach(() => {
  vi.clearAllMocks();
  mockCurrentUser.value = null;
  mockCurrentConversationId.value = null;
  mockIsMobile.value = false;
  mockIsNarrow.value = false;
  mockIsConversationPanelOpen.value = false;
  mockIsConversationDrawerOpen.value = false;
  mockActiveAgentIds.value = [];
  mockActiveConversationIds.value = [];
  resetAppChromeStore();
  localStorage.clear();
});

describe("App.vue conversation drawer", () => {
  // The drawer teleports to document.body, so it outlives an un-unmounted
  // wrapper and would leak into the next assertion.
  async function mountNarrow() {
    localStorage.setItem("daymug-user-id", "user-1");
    const router = makeRouter("/chat/user-1/conv-1");
    await router.isReady();
    const wrapper = mount(App, {
      global: {
        plugins: [router],
        stubs: { ConversationListContainer: true, SidebarPanel: true },
      },
    });
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }
    return wrapper;
  }

  it("stays closed on load even when the persisted panel preference is open", async () => {
    // Regression: the narrow-viewport drawer was driven by the same persisted
    // flag as the wide-screen inline column, which defaults to `true`. An iPad
    // in portrait therefore loaded with a modal backdrop over the whole app.
    mockIsNarrow.value = true;
    mockIsConversationPanelOpen.value = true;

    const wrapper = await mountNarrow();
    expect(document.querySelector("[data-testid='conversation-drawer']")).toBeNull();
    wrapper.unmount();
  });

  it("opens only once the drawer's own state is set", async () => {
    mockIsNarrow.value = true;
    mockIsConversationPanelOpen.value = false;

    const wrapper = await mountNarrow();
    mockIsConversationDrawerOpen.value = true;
    await wrapper.vm.$nextTick();

    expect(document.querySelector("[data-testid='conversation-drawer']")).not.toBeNull();
    wrapper.unmount();
  });
});

describe("App.vue", () => {
  it("hard-refresh on /settings/* keeps the user on the settings route", async () => {
    // Regression: previously the post-mount onMounted hook called
    // `router.replace({ name: 'chat', ... })` unconditionally once a
    // saved user / first-user fallback resolved, so refreshing any
    // /settings/* page bounced you to /chat.
    localStorage.setItem("daymug-user-id", "user-1");
    const router = makeRouter("/settings/system");
    await router.isReady();

    mount(App, { global: { plugins: [router] } });
    // Wait for onMounted's awaited chain (loadAuthMe → loadUsers →
    // selectUser) and the post-flush watcher tick.
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }

    expect(mockSelectUser).toHaveBeenCalledWith("user-1");
    expect(router.currentRoute.value.path).toBe("/settings/system");
    expect(router.currentRoute.value.name).toBe("settings-system");
  });

  it("hard-refresh on / still redirects to the saved user's chat", async () => {
    // Sanity check: the / → /chat fallback is unchanged for non-settings
    // routes, so the regression fix didn't over-correct.
    localStorage.setItem("daymug-user-id", "user-1");
    const router = makeRouter("/");
    await router.isReady();

    mount(App, { global: { plugins: [router] } });
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }

    expect(router.currentRoute.value.name).toBe("chat");
    expect(router.currentRoute.value.params.userId).toBe("user-1");
  });

  it("keeps a mobile hard-refresh deep link on the chat route", async () => {
    mockIsMobile.value = true;
    const router = makeRouter("/chat/user-1/conv-99");
    await router.isReady();

    mount(App, { global: { plugins: [router] } });
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }

    expect(mockSelectUser).toHaveBeenCalledWith("user-1", "conv-99");
    expect(router.currentRoute.value.name).toBe("chat");
  });

  it("keeps the mobile conversation list on /conversations", async () => {
    mockIsMobile.value = true;
    localStorage.setItem("daymug-user-id", "user-1");
    const router = makeRouter("/conversations");
    await router.isReady();

    const wrapper = mount(App, { global: { plugins: [router] } });
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }

    expect(router.currentRoute.value.name).toBe("conversations");
    expect(wrapper.find(".mobile-global-bar").exists()).toBe(true);
    expect(wrapper.find(".mobile-workspace-nav").exists()).toBe(true);
    expect(wrapper.get('[data-testid="mobile-conversations-tab"]').classes()).toContain(
      "bg-[#f3f2ff]",
    );
    wrapper.unmount();
  });

  it("opens the current conversation's artifact view from the mobile workspace nav", async () => {
    mockIsMobile.value = true;
    mockCurrentUser.value = { id: "user-1", name: "Alice" };
    mockCurrentConversationId.value = "conv-1";
    localStorage.setItem("daymug-user-id", "user-1");
    const router = makeRouter("/conversations");
    await router.isReady();

    const wrapper = mount(App, { global: { plugins: [router] } });
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }

    await wrapper.get('[data-testid="mobile-artifacts-tab"]').trigger("click");
    for (let i = 0; i < 3; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }

    expect(router.currentRoute.value.name).toBe("chat");
    expect(router.currentRoute.value.params).toMatchObject({
      userId: "user-1",
      conversationId: "conv-1",
    });
    expect(router.currentRoute.value.query.view).toBe("artifacts");
    wrapper.unmount();
  });

  it("refreshes conversations when Chrome restores the tab from history", async () => {
    const router = makeRouter("/chat/user-1/conv-1");
    await router.isReady();

    const wrapper = mount(App, { global: { plugins: [router] } });
    for (let i = 0; i < 5; i++) {
      await new Promise((r) => setTimeout(r, 0));
    }
    mockRefreshConversations.mockClear();

    const event = new Event("pageshow") as Event & { persisted?: boolean };
    Object.defineProperty(event, "persisted", { value: true });
    window.dispatchEvent(event);

    expect(mockRefreshConversations).toHaveBeenCalledWith("user-1");
    wrapper.unmount();
  });

  it("restores the desktop conversation panel width and exposes its resize handle", async () => {
    localStorage.setItem("daymug-conversation-panel-px", "352");
    mockCurrentUser.value = { id: "user-1", name: "Alice" };
    mockIsConversationPanelOpen.value = true;
    const router = makeRouter("/chat/user-1/conv-1");
    await router.isReady();

    const wrapper = mount(App, { global: { plugins: [router] } });
    const shell = wrapper.find("[data-testid='conversation-panel-shell']");

    expect(shell.exists()).toBe(true);
    expect(shell.attributes("style")).toContain("width: 352px");
    expect(wrapper.find("[data-testid='conversation-panel-resize-handle']").exists()).toBe(true);
    wrapper.unmount();
  });

  it("leaves the conversation panel's handle line invisible until hovered", async () => {
    // The list's own border-r is the resting rule; a second line over it made
    // the edge look permanently hovered.
    mockCurrentUser.value = { id: "user-1", name: "Alice" };
    mockIsConversationPanelOpen.value = true;
    const router = makeRouter("/chat/user-1/conv-1");
    await router.isReady();

    const wrapper = mount(App, { global: { plugins: [router] } });
    const line = wrapper.find("[data-testid='conversation-panel-resize-line']");

    expect(line.classes()).toContain("bg-transparent");
    expect(line.classes()).toContain("group-hover:bg-primary/30");
    expect(line.classes()).not.toContain("bg-sidebar-border");
    wrapper.unmount();
  });

  it("starts the desktop shell at the reference page's 280px left-column width", async () => {
    mockCurrentUser.value = { id: "user-1", name: "Alice" };
    mockIsConversationPanelOpen.value = true;
    const router = makeRouter("/chat/user-1/conv-1");
    await router.isReady();

    const wrapper = mount(App, { global: { plugins: [router] } });
    const shell = wrapper.find("[data-testid='conversation-panel-shell']");
    expect(shell.attributes("style")).toContain("width: 232px");
    wrapper.unmount();
  });
});
