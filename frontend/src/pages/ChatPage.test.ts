import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import { ref, computed } from "vue";
import { createRouter, createMemoryHistory, type Router } from "vue-router";
import ChatPage from "./ChatPage.vue";
import ChatFooterControls from "@/components/chat/ChatFooterControls.vue";
import type { ModelRegistry } from "@/composables/apiTypes";
import { requestBrowserNotificationPermission } from "@/composables/useBrowserNotifications";

enableAutoUnmount(afterEach);

vi.mock("@/composables/useBrowserNotifications", () => ({
  requestBrowserNotificationPermission: vi.fn(),
}));

const requestNotificationPermission = vi.mocked(requestBrowserNotificationPermission);

const sharedRouter: Router = createRouter({
  history: createMemoryHistory("/"),
  routes: [
    { path: "/", name: "home", component: { template: "<div />" } },
    {
      path: "/conversations",
      name: "conversations",
      component: { template: "<div />" },
    },
    { path: "/file/:userId", name: "file", component: { template: "<div />" } },
  ],
});

function mountChatPage() {
  return mount(ChatPage, {
    global: {
      plugins: [sharedRouter],
      directives: { mermaid: {} },
      stubs: { ChatFooterControls: true },
    },
  });
}

// Focus assertions need the tree in the real document — a detached element can
// never become document.activeElement.
function mountChatPageAttached() {
  return mount(ChatPage, {
    attachTo: document.body,
    global: {
      plugins: [sharedRouter],
      directives: { mermaid: {} },
      stubs: { ChatFooterControls: true },
    },
  });
}

function stubMobileViewport() {
  return vi.spyOn(window, "matchMedia").mockImplementation(
    (query: string) =>
      ({
        matches: query === "(max-width: 767px)",
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      }) as unknown as MediaQueryList,
  );
}

type Msg = {
  role: string;
  content: string;
  activityType?: string;
  usage?: { short: string; detail: string };
};

type TestConversation = {
  id: string;
  title?: string;
  notifications_enabled?: boolean;
  pinned?: boolean;
  provider?: string;
  model?: string;
};

const chatMessages = ref<Msg[]>([]);
const isThinking = ref(false);
const isTurnStatusKnown = ref(true);
const isConnected = ref(true);
const contextUsage = ref<{ used: number; total: number } | null>(null);
const rateLimits = ref<Record<string, unknown>>({});
const mockRegistry = ref<ModelRegistry | null>(null);

// reportsContextUsage is the only capability these tests exercise; the rest are
// filled with the shipped defaults so the shape stays valid.
function registryWith(provider: string, reportsContextUsage: boolean): ModelRegistry {
  return {
    default_provider: provider,
    accounts: [],
    providers: [
      {
        name: provider,
        models: ["m"],
        latest: "m",
        capabilities: {
          supports_compaction: true,
          supports_thinking_stream: true,
          supports_rate_limit_events: false,
          reports_context_usage: reportsContextUsage,
          reports_cost_usd: false,
          supports_steering: true,
        },
      },
    ],
  };
}
const currentUser = ref<{ id: string; name: string; work_dir?: string } | null>({
  id: "u1",
  name: "Test",
});
const authMe = ref<{
  id: string;
  username: string;
  name: string;
  is_admin: boolean;
} | null>(null);
const resultVersion = ref(0);

const conversationWorkDir = ref("");
const isWorkDirLocked = ref(false);
const currentConversationId = ref("");
const queuePosition = ref<number | null>(null);
const pendingPrompts = ref<{ id?: string; content: string; clientKey: string }[]>([]);
const recallText = ref("");
const recallAttachments = ref([]);
const conversations = ref<TestConversation[]>([]);
const currentModel = ref("");
const conversationProvider = ref("");
const hasMoreHistory = ref(false);
const isLoadingMoreHistory = ref(false);

const mockState = {
  chatMessages,
  currentUser,
  authMe,
  isThinking,
  isTurnStatusKnown,
  isConnected,
  contextUsage,
  rateLimits,
  resultVersion,
  conversationWorkDir,
  isWorkDirLocked,
  recallText,
  recallAttachments,
  currentConversationId,
  queuePosition,
  pendingPrompts,
  conversations,
  currentModel,
  conversationProvider,
  hasMoreHistory,
  isLoadingMoreHistory,
  contextPercent: computed(() => 0),
  startNewConversation: vi.fn(),
  sendMessage: vi.fn(),
  cancelMessage: vi.fn(),
  cancelPendingPrompt: vi.fn(),
  clearContext: vi.fn().mockResolvedValue(true),
  changeWorkDir: vi.fn(),
  setChatScrollFn: vi.fn(),
  toggleConversationNotifications: vi.fn(),
  loadMoreHistory: vi.fn().mockResolvedValue(0),
};

// ChatPage reads its app state from four composables; each hands back the same
// fake so a test can set any ref without caring which composable owns it.
vi.mock("@/composables/useAuth", () => ({ useAuth: () => mockState }));
vi.mock("@/composables/useUsers", () => ({ useUsers: () => mockState }));
vi.mock("@/composables/useConversations", () => ({ useConversations: () => mockState }));
vi.mock("@/composables/useChat", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/composables/useChat")>();
  return { ...actual, useChat: () => mockState };
});

vi.mock("@/composables/useMarkdown", () => ({
  renderMarkdown: (s: string) => `<p>${s}</p>`,
}));

// The context bar is gated on the live backend's reports_context_usage, so the
// registry has to be seeded for any test that asserts on the bar.
vi.mock("@/composables/useModelRegistry", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/composables/useModelRegistry")>();
  return {
    ...actual,
    useModelRegistry: () => ({
      registry: mockRegistry,
      ensureLoaded: async () => mockRegistry.value as ModelRegistry,
    }),
  };
});

vi.mock("@/composables/useTheme", async () => {
  const { ref } = await import("vue");
  return { useTheme: () => ({ isDark: ref(false), toggleTheme: () => {} }) };
});

const mockListDir = vi.fn().mockResolvedValue({ path: ".", entries: [] });

vi.mock("@/composables/useFileApi", () => ({
  // Re-exported alongside useFileApi; the preview surface and the open-file
  // routing import it directly, so the mock has to carry it too.
  isHtmlFile: (name: string) => /\.html?$/i.test(name),
  useFileApi: () => ({
    listDir: mockListDir,
    readFile: vi.fn(),
    downloadFileUrl: vi.fn().mockReturnValue(""),
    downloadZipUrl: vi.fn().mockReturnValue(""),
    uploadFiles: vi.fn(),
    deleteFile: vi.fn(),
    renameFile: vi.fn(),
    mkdir: vi.fn(),
    moveFile: vi.fn(),
    copyFile: vi.fn(),
  }),
}));

beforeEach(() => {
  vi.clearAllMocks();
  chatMessages.value = [];
  isThinking.value = false;
  isTurnStatusKnown.value = true;
  isConnected.value = true;
  contextUsage.value = null;
  rateLimits.value = {};
  mockRegistry.value = null;
  currentUser.value = { id: "u1", name: "Test" };
  authMe.value = null;
  resultVersion.value = 0;
  conversationWorkDir.value = "";
  isWorkDirLocked.value = false;
  currentConversationId.value = "";
  queuePosition.value = null;
  recallText.value = "";
  recallAttachments.value = [];
  conversations.value = [];
  currentModel.value = "";
  conversationProvider.value = "";
  hasMoreHistory.value = false;
  isLoadingMoreHistory.value = false;
  pendingPrompts.value = [];
});

describe("ChatPage", () => {
  it("renders chat and workspace panels", () => {
    const wrapper = mountChatPage();
    expect(wrapper.find(".chat-panel").exists()).toBe(true);
    expect(wrapper.find(".workspace-panel").exists()).toBe(true);
  });

  it("shows the conversation title in the desktop chat header", () => {
    currentConversationId.value = "c1";
    conversations.value = [
      {
        id: "c1",
        title: "Ship the release",
        notifications_enabled: true,
        pinned: false,
      },
    ];
    const wrapper = mountChatPage();
    const headers = wrapper.findAll(".panel-header");
    expect(headers.length).toBe(1);
    expect(headers[0].find("[data-testid='chat-title']").text()).toBe("Ship the release");
  });

  it("has input area with textarea and send button", () => {
    const wrapper = mountChatPage();
    expect(wrapper.find("textarea").exists()).toBe(true);
    expect(wrapper.find("[data-testid='send-btn']").exists()).toBe(true);
  });

  it("does not render the legacy New button in the desktop chat header", () => {
    // The New conversation entry point is the ConversationListPanel button; the chat
    // header was decluttered to keep just the title + context bar.
    const wrapper = mountChatPage();
    expect(wrapper.find(".new-btn").exists()).toBe(false);
  });

  it("renders model and context controls in the composer footer instead of the desktop header", async () => {
    currentConversationId.value = "c1";
    conversations.value = [
      {
        id: "c1",
        title: "Footer controls",
        provider: "claude",
        model: "claude-sonnet-4-5",
      },
    ];
    mockRegistry.value = registryWith("claude", true);
    conversationProvider.value = "claude";
    contextUsage.value = { used: 100_000, total: 200_000 };

    const wrapper = mountChatPage();
    await wrapper.vm.$nextTick();

    const header = wrapper.get(".panel-header");
    expect(header.findComponent(ChatFooterControls).exists()).toBe(false);

    const composer = wrapper.get('[data-testid="composer-stack"]');
    const footer = wrapper.getComponent(ChatFooterControls);
    expect(composer.element.contains(footer.element)).toBe(true);
    expect(footer.props("conversation")).toMatchObject({ id: "c1" });
    expect(footer.props("showContextUsage")).toBe(true);
  });

  // Codex over the CLI emits no context_usage, so the only value that can
  // reach the UI is a row replayed from an older estimating build — pinned at
  // used == total, i.e. a permanently full bar.
  it("hides stale context usage for Codex on a backend that doesn't report it", async () => {
    mockRegistry.value = registryWith("codex", false);
    contextUsage.value = { used: 400_000, total: 400_000 };
    conversationProvider.value = "codex";
    const wrapper = mountChatPage();
    await wrapper.vm.$nextTick();

    expect(wrapper.getComponent(ChatFooterControls).props("showContextUsage")).toBe(false);
  });

  it("shows context usage for Codex once the backend reports it", async () => {
    mockRegistry.value = registryWith("codex", true);
    contextUsage.value = { used: 94_489, total: 258_400 };
    conversationProvider.value = "codex";
    const wrapper = mountChatPage();
    await wrapper.vm.$nextTick();

    expect(wrapper.getComponent(ChatFooterControls).props("showContextUsage")).toBe(true);
  });

  it("keeps context usage visible for Claude conversations", async () => {
    mockRegistry.value = registryWith("claude", true);
    contextUsage.value = { used: 100_000, total: 200_000 };
    conversationProvider.value = "claude";
    const wrapper = mountChatPage();
    await wrapper.vm.$nextTick();

    expect(wrapper.getComponent(ChatFooterControls).props("showContextUsage")).toBe(true);
  });

  // The registry is fetched async; until it lands nothing is known about the
  // backend, and a bar that pops in wrong is worse than one that appears late.
  it("hides context usage until the model registry has loaded", async () => {
    mockRegistry.value = null;
    contextUsage.value = { used: 100_000, total: 200_000 };
    conversationProvider.value = "claude";
    const wrapper = mountChatPage();
    await wrapper.vm.$nextTick();

    expect(wrapper.getComponent(ChatFooterControls).props("showContextUsage")).toBe(false);
  });

  it("opens the mobile task feed and refreshes the page from Refresh", async () => {
    const matchMediaSpy = stubMobileViewport();
    const reloadSpy = vi.spyOn(window.location, "reload").mockImplementation(() => {});

    try {
      const wrapper = mountChatPage();
      await wrapper.vm.$nextTick();
      const activityButton = wrapper.find("[data-testid='mobile-activity-button']");

      expect(activityButton.exists()).toBe(true);
      await activityButton.trigger("click");

      expect(wrapper.find("[data-testid='mobile-activity-popover']").exists()).toBe(true);
      expect(mockState.startNewConversation).not.toHaveBeenCalled();

      await wrapper.find("[data-testid='mobile-refresh-page']").trigger("click");
      expect(reloadSpy).toHaveBeenCalledTimes(1);
    } finally {
      reloadSpy.mockRestore();
      matchMediaSpy.mockRestore();
    }
  });

  it("routes the mobile back button to the conversation list", async () => {
    const matchMediaSpy = stubMobileViewport();

    try {
      await sharedRouter.push("/");
      const wrapper = mountChatPage();
      await wrapper.vm.$nextTick();
      await wrapper.find("[data-testid='mobile-back-button']").trigger("click");
      await flushPromises();

      expect(sharedRouter.currentRoute.value.name).toBe("conversations");
    } finally {
      matchMediaSpy.mockRestore();
    }
  });

  it("uses persistent bottom navigation to switch between chat and artifacts", async () => {
    const matchMediaSpy = stubMobileViewport();
    try {
      await sharedRouter.push("/");
      const wrapper = mountChatPage();
      await wrapper.vm.$nextTick();

      expect(wrapper.find("[data-testid='mobile-project-header']").exists()).toBe(true);
      expect(wrapper.find("[data-testid='mobile-chat-tab']").classes()).toContain("bg-[#f3f2ff]");
      expect(wrapper.findComponent({ name: "WorkspacePanel" }).exists()).toBe(false);

      await wrapper.find("[data-testid='mobile-artifacts-tab']").trigger("click");
      await flushPromises();
      expect(wrapper.findComponent({ name: "WorkspacePanel" }).exists()).toBe(true);
      expect(wrapper.find("[data-testid='mobile-artifacts-tab']").classes()).toContain(
        "bg-[#f3f2ff]",
      );
      expect(sharedRouter.currentRoute.value.query.view).toBe("artifacts");

      await wrapper.find("[data-testid='mobile-chat-tab']").trigger("click");
      await flushPromises();
      expect(wrapper.find("textarea.composer-input").exists()).toBe(true);
      expect(sharedRouter.currentRoute.value.query.view).toBeUndefined();
    } finally {
      matchMediaSpy.mockRestore();
    }
  });

  it("pops history instead of pushing when the conversation list is the entry behind", async () => {
    // Opening a conversation is a push, so backing out has to be a pop. Pushing
    // the list again left two entries per visit behind, turning the phone's own
    // back gesture into a walk back through every conversation ever opened.
    const matchMediaSpy = stubMobileViewport();
    const backSpy = vi.spyOn(sharedRouter, "back").mockImplementation(() => {});
    const historyState = { back: "/conversations", current: "/chat/u1/c1" };
    const stateSpy = vi.spyOn(window.history, "state", "get").mockReturnValue(historyState);

    try {
      await sharedRouter.push("/");
      const wrapper = mountChatPage();
      await wrapper.vm.$nextTick();
      await wrapper.find("[data-testid='mobile-back-button']").trigger("click");
      await flushPromises();

      expect(backSpy).toHaveBeenCalledTimes(1);
      expect(sharedRouter.currentRoute.value.name).not.toBe("conversations");
    } finally {
      stateSpy.mockRestore();
      backSpy.mockRestore();
      matchMediaSpy.mockRestore();
    }
  });

  it("still pushes the list when nothing is behind the conversation", async () => {
    // A conversation reached from a deep link or a push notification has no
    // list underneath it; popping there would leave the app entirely.
    const matchMediaSpy = stubMobileViewport();
    const backSpy = vi.spyOn(sharedRouter, "back").mockImplementation(() => {});
    const stateSpy = vi.spyOn(window.history, "state", "get").mockReturnValue(null);

    try {
      await sharedRouter.push("/");
      const wrapper = mountChatPage();
      await wrapper.vm.$nextTick();
      await wrapper.find("[data-testid='mobile-back-button']").trigger("click");
      await flushPromises();

      expect(backSpy).not.toHaveBeenCalled();
      expect(sharedRouter.currentRoute.value.name).toBe("conversations");
    } finally {
      stateSpy.mockRestore();
      backSpy.mockRestore();
      matchMediaSpy.mockRestore();
    }
  });

  it("renders the model selector in the mobile composer footer", async () => {
    const matchMediaSpy = stubMobileViewport();
    currentConversationId.value = "c1";
    conversations.value = [
      {
        id: "c1",
        title: "Mobile model chat",
        notifications_enabled: true,
        pinned: false,
        provider: "claude",
        model: "claude-sonnet-4-5",
      },
    ];
    chatMessages.value = [{ role: "user", content: "hi" }];

    try {
      const wrapper = mountChatPage();
      await wrapper.vm.$nextTick();

      expect(wrapper.find("[data-testid='mobile-model-selector-row']").exists()).toBe(false);
      const footer = wrapper.getComponent(ChatFooterControls);
      expect(wrapper.get('[data-testid="composer-stack"]').element.contains(footer.element)).toBe(
        true,
      );
      expect(footer.props("conversation")).toMatchObject({
        id: "c1",
        provider: "claude",
        model: "claude-sonnet-4-5",
      });
      expect(footer.props("hasMessages")).toBe(true);
    } finally {
      matchMediaSpy.mockRestore();
    }
  });

  it("does not send message when Enter is pressed (Send button only)", async () => {
    const wrapper = mountChatPage();
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await textarea.trigger("keydown", { key: "Enter" });
    expect(mockState.sendMessage).not.toHaveBeenCalled();
    expect(requestNotificationPermission).not.toHaveBeenCalled();
  });

  it("sends message when Send button is clicked", async () => {
    const wrapper = mountChatPage();
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await wrapper.find("[data-testid='send-btn']").trigger("click");
    expect(mockState.sendMessage).toHaveBeenCalledWith("hello");
    expect(requestNotificationPermission).toHaveBeenCalledOnce();
  });

  it("inserts into the running task when Send is clicked mid-turn", async () => {
    isThinking.value = true;
    const wrapper = mountChatPage();
    await wrapper.find("textarea").setValue("change direction");
    await wrapper.find("[data-testid='send-btn']").trigger("click");

    expect(mockState.sendMessage).toHaveBeenCalledWith("change direction", undefined, true);
  });

  it("queues after the active task from the send options menu", async () => {
    isThinking.value = true;
    const wrapper = mountChatPage();
    await wrapper.find("textarea").setValue("change direction");
    await wrapper.find("[data-testid='send-options-btn']").trigger("click");
    await wrapper.find("[data-testid='queue-after-current-btn']").trigger("click");

    expect(mockState.sendMessage).toHaveBeenCalledWith("change direction");
  });

  it("shows thinking indicator when isThinking is true", async () => {
    isThinking.value = true;
    const wrapper = mountChatPage();
    expect(wrapper.find(".thinking-indicator").exists()).toBe(true);
  });

  it("labels the assistant side with the agent's name and the user side with a fixed 'You'", () => {
    // The assistant label resolves to the active agent's display name
    // so the transcript reads as a conversation with *that* agent. The
    // user side, however, is intentionally pinned to the localized
    // "You" — surfacing the human's own name on their side of their
    // own transcript adds noise, and gets actively confusing when the
    // human and the agent share a display name (which is common when
    // both rows are seeded from the same email handle).
    currentUser.value = { id: "u1", name: "Researcher" };
    authMe.value = { id: "h1", name: "Alice", username: "alice", is_admin: false };
    chatMessages.value = [
      { role: "user", content: "hi" },
      { role: "assistant", content: "hello" },
    ];
    const wrapper = mountChatPage();
    const msgs = wrapper.findAll(".msg-label");
    expect(msgs.length).toBe(2);
    expect(msgs[0].text()).toBe("You");
    expect(msgs[1].text()).toBe("Researcher");
  });

  it("falls back to 'You' / 'Claude' labels when the agent record is missing", () => {
    currentUser.value = null;
    authMe.value = null;
    chatMessages.value = [
      { role: "user", content: "hi" },
      { role: "assistant", content: "hello" },
    ];
    const wrapper = mountChatPage();
    const msgs = wrapper.findAll(".msg-label");
    expect(msgs[0].text()).toBe("You");
    expect(msgs[1].text()).toBe("Claude");
  });

  it("groups tool activity into a foldable card, collapsed by default", () => {
    // Tool calls used to render as one card per tool (always expanded). They
    // dominated the chat scrollback during long Claude turns, so we now wrap
    // each consecutive run into a single foldable card. Default state is
    // collapsed: only the summary header is visible, per-tool details stay
    // hidden until the user expands the group.
    chatMessages.value = [
      { role: "activity", content: "[Read]\n  file_path: /tmp", activityType: "tool" },
    ];
    const wrapper = mountChatPage();
    expect(wrapper.find(".tool-group-header").exists()).toBe(true);
    expect(wrapper.find(".tool-group-header").text()).toContain("Used Read");
    expect(wrapper.find(".tool-group-header").text()).toContain("Read");
    // The per-tool body / chip is hidden until the group is expanded.
    expect(wrapper.find(".tool-header").exists()).toBe(false);
  });

  it("expanding a tool group reveals its per-tool details", async () => {
    chatMessages.value = [
      { role: "activity", content: "[Read]\n  file_path: /tmp", activityType: "tool" },
      { role: "activity", content: "[Bash]\n  cmd: ls", activityType: "tool" },
    ];
    const wrapper = mountChatPage();
    // Both tools are inside a single group (consecutive), so there should be
    // exactly one group header but two tool headers once expanded.
    expect(wrapper.findAll(".tool-group-header").length).toBe(1);
    await wrapper.find(".tool-group-header").trigger("click");
    const tools = wrapper.findAll(".tool-header").map((n) => n.text());
    expect(tools).toEqual(["[Read]", "[Bash]"]);
  });

  it("displays info activity entries inline in chat", () => {
    chatMessages.value = [
      {
        role: "activity",
        content: "Model: claude-opus-4-6 | Tools: 5 available",
        activityType: "info",
      },
    ];
    const wrapper = mountChatPage();
    expect(wrapper.find(".info-entry").exists()).toBe(true);
    expect(wrapper.find(".info-entry").text()).toContain("claude-opus-4-6");
  });

  it("displays thinking activity entries inline in chat", () => {
    chatMessages.value = [
      { role: "activity", content: "Let me think about this...", activityType: "thinking" },
    ];
    const wrapper = mountChatPage();
    expect(wrapper.find(".thinking-entry").exists()).toBe(true);
    expect(wrapper.find(".thinking-text").text()).toContain("Let me think");
  });

  it("calls cancelMessage when cancel button is clicked", async () => {
    isThinking.value = true;
    const wrapper = mountChatPage();
    await wrapper.find("[data-testid='cancel-btn']").trigger("click");
    expect(mockState.cancelMessage).toHaveBeenCalled();
  });

  it("renders every activity row and the per-turn token chip, with no density gate", async () => {
    // There is exactly one transcript density: tool groups, model banners,
    // thinking rows and the end-of-turn token chip always render. The chip
    // used to be hidden alongside the rest of the chrome in "simple" mode,
    // which is how a finished turn could end up showing no token cost at
    // all. The assistant row carries its own `usage` so this covers the
    // per-turn chip, not just the model banner — both share `.usage-entry`.
    currentModel.value = "claude-opus-4-8[1m]";
    chatMessages.value = [
      {
        role: "activity",
        content: "Model: claude-opus-4-8[1m] | Tools: 59 available",
        activityType: "model",
      },
      { role: "user", content: "hi" },
      { role: "activity", content: "[Read]\n  file: /tmp", activityType: "tool" },
      { role: "activity", content: "thinking...", activityType: "thinking" },
      {
        role: "assistant",
        content: "hello",
        usage: { short: "1.2k in / 340 out · $0.02", detail: "" },
      },
    ];

    // Wipe the default seeded "Test" agent so the labels in this case
    // fall back to the historical "You / Claude" framing — keeps the
    // assertion focused on which rows render, not on the dynamic label
    // resolution which has its own test above.
    currentUser.value = null;
    authMe.value = null;

    const wrapper = mountChatPage();
    await flushPromises();

    expect(wrapper.find(".tool-group-header").exists()).toBe(true);
    expect(wrapper.find(".thinking-entry").exists()).toBe(true);
    // Two `.usage-entry` chips: the leading `model` banner activity row and
    // the per-turn token-cost chip under the assistant bubble.
    expect(wrapper.findAll(".usage-entry").length).toBe(2);
    expect(wrapper.findAll(".msg-label").map((n) => n.text())).toEqual(["You", "Claude"]);
  });

  it("keeps the live stream activity visible so an in-flight reply types itself out", () => {
    // The stream row is the assistant's reply *before* the result event
    // promotes it to a regular assistant message.
    chatMessages.value = [
      { role: "user", content: "hi" },
      { role: "activity", content: "streaming reply…", activityType: "stream" },
    ];
    const wrapper = mountChatPage();
    expect(wrapper.find(".stream-text").exists()).toBe(true);
  });

  it("restores a collapsed workspace pane from the chat-header expand button", async () => {
    // Collapsing is persisted per browser, so the only way back is the
    // header affordance — it has to surface whenever the pane is zeroed.
    localStorage.setItem("daymug-workspace-collapsed", "true");

    const wrapper = mountChatPage();
    expect(wrapper.find(".workspace-panel").attributes("style") ?? "").toContain("width: 0px");
    expect(wrapper.find("[data-testid='workspace-expand']").exists()).toBe(true);

    await wrapper.find("[data-testid='workspace-expand']").trigger("click");
    await flushPromises();
    expect(wrapper.find(".workspace-panel").attributes("style") ?? "").not.toContain("width: 0px");

    localStorage.removeItem("daymug-workspace-collapsed");
  });

  it("refreshes workspace panel when resultVersion increments", async () => {
    mountChatPage();
    await flushPromises();

    mockListDir.mockClear();
    resultVersion.value++;
    await flushPromises();

    expect(mockListDir).toHaveBeenCalled();
  });

  it("refreshes the workspace panel as soon as the user sends a message", async () => {
    // The result-version watch covers post-claude refresh, but we also
    // want the file pane to reload at the start of a turn — any files
    // the user just uploaded (or that a previous turn produced into a
    // different folder than the panel is currently viewing) should
    // show up without the user having to click refresh themselves.
    const wrapper = mountChatPage();
    await flushPromises();

    mockListDir.mockClear();
    const textarea = wrapper.find("textarea");
    await textarea.setValue("hello");
    await wrapper.find("[data-testid='send-btn']").trigger("click");
    await flushPromises();

    expect(mockState.sendMessage).toHaveBeenCalledWith("hello");
    expect(mockListDir).toHaveBeenCalled();
  });

  it("applies width styles to chat and workspace panels", () => {
    const wrapper = mountChatPage();
    const chatStyle = wrapper.find(".chat-panel").attributes("style");
    const workspaceStyle = wrapper.find(".workspace-panel").attributes("style");
    expect(chatStyle).toContain("width:");
    expect(workspaceStyle).toContain("width:");
  });

  it("renders the resize handle while the workspace is open and hides it while collapsed", async () => {
    // The drag handle is a "between two panes" affordance — it's only
    // meaningful when both panes exist. Once collapsed, the chat-header
    // expand button is the sole entry point back, so the handle must
    // not linger as a thin strip in the collapsed state.
    localStorage.removeItem("daymug-workspace-collapsed");

    const wrapper = mountChatPage();
    expect(wrapper.find("[data-testid='workspace-resize-handle']").exists()).toBe(true);

    await wrapper.find("[data-testid='workspace-collapse']").trigger("click");
    await flushPromises();
    expect(wrapper.find("[data-testid='workspace-resize-handle']").exists()).toBe(false);

    localStorage.removeItem("daymug-workspace-collapsed");
  });

  it("renders the workspace divider as a one-pixel rule", () => {
    const wrapper = mountChatPage();
    const handle = wrapper.find("[data-testid='workspace-resize-handle']");

    expect(handle.classes()).toContain("w-px");
    expect(handle.classes()).not.toContain("w-1.5");
    expect(wrapper.find(".chat-panel").classes()).not.toContain("border-r");
  });

  it("keeps the resting workspace divider as faint as the panel borders", () => {
    // The hover colour is the only emphasis the handle gets; a darker resting
    // rule reads as permanently hovered next to the #eeeeee header border.
    const wrapper = mountChatPage();
    const handle = wrapper.find("[data-testid='workspace-resize-handle']");

    expect(handle.classes()).toContain("bg-[#eeeeee]");
    expect(handle.classes()).not.toContain("bg-border");
    expect(handle.classes()).toContain("hover:bg-primary/30");
  });

  it("gives the one-pixel divider a wider raised grab band", () => {
    // The band overflows the 1px handle, so without z-10 the workspace
    // column (a later sibling) paints over its right half and half the
    // grab area silently disappears.
    const wrapper = mountChatPage();
    const band = wrapper.find("[data-testid='workspace-resize-handle'] span");

    expect(band.exists()).toBe(true);
    expect(band.classes()).toContain("w-3");
    expect(band.classes()).toContain("z-10");
    expect(band.classes()).toContain("touch-none");
  });

  it("starts touch drag from the resize handle", async () => {
    const wrapper = mountChatPage();
    const handle = wrapper.find("[data-testid='workspace-resize-handle']");
    expect(handle.exists()).toBe(true);

    await handle.trigger("touchstart", { touches: [{ clientX: 500 }] });
    expect(handle.classes()).toContain("bg-primary/30");

    // Cleanup so listeners don't leak between tests.
    document.dispatchEvent(new Event("touchend"));
  });

  it("disables native touch gestures on the resize handle", () => {
    const wrapper = mountChatPage();
    const style = wrapper.find("[data-testid='workspace-resize-handle']").attributes("style") ?? "";
    expect(style.replace(/\s/g, "")).toContain("touch-action:none");
  });

  it("chat-header expand button is hidden while the workspace is open, and restores width when clicked after collapse", async () => {
    // The chat header carries the sole expand affordance now; this test
    // pins both halves of the contract — invisible while open, visible +
    // functional while collapsed.
    localStorage.removeItem("daymug-workspace-collapsed");

    const wrapper = mountChatPage();
    // Open state: no expand button in the chat header.
    expect(wrapper.find("[data-testid='workspace-expand']").exists()).toBe(false);

    // Collapse via the workspace top-bar button.
    await wrapper.find("[data-testid='workspace-collapse']").trigger("click");
    await flushPromises();

    // Collapsed state: header button appears, pane is hidden.
    const expandBtn = wrapper.find("[data-testid='workspace-expand']");
    expect(expandBtn.exists()).toBe(true);
    const hidden = wrapper.find(".workspace-panel").attributes("style") ?? "";
    expect(hidden.replace(/\s/g, "")).toContain("width:0px");

    // Clicking it restores a non-zero width and removes the button.
    await expandBtn.trigger("click");
    await flushPromises();
    const restored = wrapper.find(".workspace-panel").attributes("style") ?? "";
    expect(restored.replace(/\s/g, "")).toMatch(/width:\d+px/);
    expect(restored).not.toContain("width: 0px");
    expect(wrapper.find("[data-testid='workspace-expand']").exists()).toBe(false);

    localStorage.removeItem("daymug-workspace-collapsed");
  });

  it("opens the workspace panel at the conversation's work_dir on mount", async () => {
    // Refreshing the page or opening a new conversation should always land
    // the file pane on the agent's work_dir, regardless of whatever folder
    // the user was last clicking through.
    currentUser.value = { id: "u1", name: "Test", work_dir: "/home/alice/Workspace/Work" };
    conversationWorkDir.value = "/home/alice/Workspace/Work/daymug";

    mountChatPage();
    await flushPromises();

    expect(mockListDir).toHaveBeenCalledWith("u1", "daymug");
    expect(mockListDir).not.toHaveBeenCalledWith("u1", ".");
  });

  it("normalizes the workspace initial path when the user work_dir has a trailing slash", async () => {
    currentUser.value = { id: "u1", name: "Test", work_dir: "/home/alice/workspace/" };
    conversationWorkDir.value = "/home/alice/workspace/company";

    mountChatPage();
    await flushPromises();

    expect(mockListDir).toHaveBeenCalledWith("u1", "company");
    expect(mockListDir).not.toHaveBeenCalledWith("u1", "");
  });

  it("normalizes workspace navigation paths when the user work_dir has a trailing slash", async () => {
    currentUser.value = { id: "u1", name: "Test", work_dir: "/home/alice/workspace/" };
    conversationWorkDir.value = "/home/alice/workspace/";

    const wrapper = mountChatPage();
    await flushPromises();

    wrapper.findComponent({ name: "WorkspacePanel" }).vm.$emit("directory-changed", "company");
    await flushPromises();

    expect(mockState.changeWorkDir).toHaveBeenCalledWith("/home/alice/workspace/company");
    expect(mockState.changeWorkDir).not.toHaveBeenCalledWith("/home/alice/workspace//company");
  });

  it("scrolls the chat panel to the bottom on mount so existing history starts at the latest message", async () => {
    // Repro of the reported UX: opening an existing conversation used
    // to land the reader at the top of the message list, forcing them
    // to scroll down to find what's new. happy-dom doesn't run layout,
    // so we stub scrollHeight on the panel after mount and verify the
    // ref-watch jumped scrollTop to it.
    chatMessages.value = [
      { role: "user", content: "first" },
      { role: "assistant", content: "second" },
    ];
    const wrapper = mountChatPage();
    const panel = wrapper.find(".chat-panel .wy-scroll").element as HTMLDivElement;
    expect(panel).toBeTruthy();
    Object.defineProperty(panel, "scrollHeight", { configurable: true, value: 999 });
    panel.scrollTop = 0;
    // Force the watch on chatPanelRef to re-run by toggling its binding —
    // the production trigger is the panel mounting/remounting; in the test
    // we already mounted so we just emulate the post-mount nextTick.
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(panel.scrollTop).toBe(999);
  });

  it("registers a setChatScrollFn that accepts an immediate flag and forces scroll past the at-bottom guard", async () => {
    // The streaming auto-scroll is gated on the reader already being at
    // the tail. The immediate variant (used when entering / switching
    // conversations) must skip that guard so the user always lands at
    // the latest message even if the previous conversation had them scrolled
    // up. We exercise the registered fn directly to lock in the contract.
    chatMessages.value = [{ role: "user", content: "x" }];
    const wrapper = mountChatPage();
    await flushPromises();

    const panel = wrapper.find(".chat-panel .wy-scroll").element as HTMLDivElement;
    Object.defineProperty(panel, "scrollHeight", { configurable: true, value: 800 });
    Object.defineProperty(panel, "clientHeight", { configurable: true, value: 200 });
    // Park the reader 400px above the tail — well past AT_BOTTOM_THRESHOLD.
    panel.scrollTop = 200;

    const registered = mockState.setChatScrollFn.mock.calls.at(-1)?.[0] as (
      immediate?: boolean,
    ) => void;
    expect(registered).toBeInstanceOf(Function);

    // Gated path leaves the reader where they are.
    registered(false);
    await wrapper.vm.$nextTick();
    expect(panel.scrollTop).toBe(200);

    // Immediate path forces the jump.
    registered(true);
    await wrapper.vm.$nextTick();
    expect(panel.scrollTop).toBe(800);
  });

  it("re-snaps to the bottom when the tab returns to visible if the reader was tailing before", async () => {
    // Reproduces the reported bug: while the tab is hidden, browsers
    // suspend layout, so streamed chunks land with a stale scrollHeight.
    // The user comes back, sees their notification, but the assistant's
    // reply is parked just below the visible area. We snapshot at-tail
    // state on hide and re-snap on visible so the reader actually lands
    // on the message that triggered their notification.
    chatMessages.value = [
      { role: "user", content: "task" },
      { role: "assistant", content: "reply" },
    ];
    const wrapper = mountChatPage();
    await flushPromises();

    const panel = wrapper.find(".chat-panel .wy-scroll").element as HTMLDivElement;
    Object.defineProperty(panel, "clientHeight", { configurable: true, value: 200 });
    // Reader is at the tail when going hidden.
    Object.defineProperty(panel, "scrollHeight", { configurable: true, value: 1000 });
    panel.scrollTop = 800;

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));

    // Background streaming grew the panel — but the in-tab scroll never
    // caught up because layout was suspended.
    Object.defineProperty(panel, "scrollHeight", { configurable: true, value: 1500 });

    const rafSpy = vi
      .spyOn(window, "requestAnimationFrame")
      .mockImplementation((cb: FrameRequestCallback) => {
        cb(0);
        return 0;
      });

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));

    expect(panel.scrollTop).toBe(1500);
    rafSpy.mockRestore();
  });

  it("preserves scroll position on visibility return when the reader had scrolled up before leaving", async () => {
    // Counter-case: a reader who was deliberately reading older content
    // when they tabbed away should not be yanked to the bottom on return.
    chatMessages.value = [{ role: "user", content: "x" }];
    const wrapper = mountChatPage();
    await flushPromises();

    const panel = wrapper.find(".chat-panel .wy-scroll").element as HTMLDivElement;
    Object.defineProperty(panel, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(panel, "scrollHeight", { configurable: true, value: 1000 });
    // Far above the at-bottom threshold.
    panel.scrollTop = 100;

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));

    Object.defineProperty(panel, "scrollHeight", { configurable: true, value: 1500 });

    const rafSpy = vi
      .spyOn(window, "requestAnimationFrame")
      .mockImplementation((cb: FrameRequestCallback) => {
        cb(0);
        return 0;
      });

    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));

    expect(panel.scrollTop).toBe(100);
    rafSpy.mockRestore();
  });

  describe("inline clear-context trigger", () => {
    // Production code defines the divider in useChat.ts; we reuse that exact
    // string here so the test breaks loudly if the constant ever changes
    // (the gate keys off content equality, not a substring).
    const DIVIDER = "— context cleared, future messages start fresh —";

    function setReadyConversation() {
      currentConversationId.value = "c1";
      chatMessages.value = [
        { role: "user", content: "hi" },
        { role: "assistant", content: "hello" },
      ];
    }

    it("renders the clear-context button when the conversation is idle with history", () => {
      setReadyConversation();
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(true);
    });

    it("hides the button while a turn is in flight (isThinking)", () => {
      setReadyConversation();
      isThinking.value = true;
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(false);
    });

    it("hides the button while waiting in the cross-account queue", () => {
      setReadyConversation();
      queuePosition.value = 2;
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(false);
    });

    it("hides the button right after a fresh clear (last activity is the cleared divider)", () => {
      currentConversationId.value = "c1";
      chatMessages.value = [
        { role: "user", content: "hi" },
        { role: "assistant", content: "hello" },
        { role: "activity", content: DIVIDER, activityType: "info" },
      ];
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(false);
    });

    it("still renders the button when the tail is an unrelated info activity", () => {
      // The gate must key off the exact divider, not any info entry —
      // otherwise routine "Model: …" / "Failed to clear context: …" infos
      // would silently lock the user out of trying again.
      currentConversationId.value = "c1";
      chatMessages.value = [
        { role: "user", content: "hi" },
        { role: "assistant", content: "hello" },
        {
          role: "activity",
          content: "Failed to clear context: boom",
          activityType: "info",
        },
      ];
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(true);
    });

    it("hides the button when there's no current conversation", () => {
      chatMessages.value = [{ role: "user", content: "hi" }];
      currentConversationId.value = "";
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(false);
    });

    it("hides the button when the conversation has no messages yet", () => {
      currentConversationId.value = "c1";
      chatMessages.value = [];
      const wrapper = mountChatPage();
      expect(wrapper.find("[data-testid='clear-context-btn']").exists()).toBe(false);
    });
  });

  // The composer ref is attached through the same function ref the height
  // observer uses. A refactor once replaced the static `ref` with that hook and
  // dropped focus without a single test failing — these two pin the behaviour
  // for both layouts.
  describe("composer autofocus", () => {
    it("focuses the composer input on mount in the desktop layout", async () => {
      const wrapper = mountChatPageAttached();
      try {
        await flushPromises();
        expect(document.activeElement).toBe(wrapper.find("textarea.composer-input").element);
      } finally {
        wrapper.unmount();
      }
    });

    it("focuses the composer input on mount in the mobile layout", async () => {
      const matchMediaSpy = stubMobileViewport();
      const wrapper = mountChatPageAttached();
      try {
        await flushPromises();
        expect(document.activeElement).toBe(wrapper.find("textarea.composer-input").element);
      } finally {
        wrapper.unmount();
        matchMediaSpy.mockRestore();
      }
    });
  });

  describe("artifact workspace", () => {
    it("keeps file previews inside the artifact panel", () => {
      const wrapper = mountChatPage();
      expect(wrapper.findComponent({ name: "WorkspacePanel" }).props("fileOpenTarget")).toBe(
        "panel",
      );
      expect(wrapper.find(".chat-panel").classes()).toContain("relative");
    });

    it("expands only the artifact panel for fullscreen preview", async () => {
      const wrapper = mountChatPage();
      wrapper.findComponent({ name: "WorkspacePanel" }).vm.$emit("fullscreen-change", true);
      await wrapper.vm.$nextTick();

      const panel = wrapper.get('[data-testid="desktop-artifact-panel"]');
      expect(panel.classes()).toEqual(expect.arrayContaining(["absolute", "inset-0", "z-30"]));
      expect(wrapper.find(".chat-panel").classes()).toContain("relative");
    });
  });
});
