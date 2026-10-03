import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { enableAutoUnmount, flushPromises, mount } from "@vue/test-utils";
import { ref } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

import ConversationListPanel from "./ConversationListPanel.vue";
import {
  helpDialogOpen,
  marketplaceDialogOpen,
  resetAppChromeStore,
  shareNotice,
  upgradeAvailable,
} from "@/stores/appChromeStore";
import {
  applyAttention,
  resetConversationAttentionStore,
} from "@/stores/conversationAttentionStore";

enableAutoUnmount(afterEach);

const fake = vi.hoisted(() => ({
  selectConversation: vi.fn(async () => {}),
  startNewConversation: vi.fn(async () => {}),
  deleteConversation: vi.fn(async () => {}),
  renameConversation: vi.fn(),
  toggleConversationPinned: vi.fn(),
  reorderPinned: vi.fn(),
  loadMoreConversations: vi.fn(),
  closeConversationDrawer: vi.fn(),
  toggleConversationList: vi.fn(),
  applyRemoteUpdated: vi.fn(),
  confirm: vi.fn(async () => true),
  shareConversation: vi.fn(),
  unshareConversation: vi.fn(),
  copyText: vi.fn(async () => {}),
}));

const authMe = ref<{ name: string; username: string; is_admin: boolean } | null>(null);
const currentUser = ref<{ id: string } | null>({ id: "u1" });
const currentConversationId = ref("c1");
const activeConversationIds = ref<string[]>([]);
const users = ref([{ id: "u1", name: "Alice" }]);
const runningConversations = ref([{ conversation_id: "c2", agent_id: "u1" }]);
const queuedConversations = ref([{ conversation_id: "c3", agent_id: "u1" }]);
const conversations = ref([{ id: "c1" }, { id: "c2" }]);
const conversationsHasMore = ref(false);
const isLoadingMoreConversations = ref(false);
const helpMarkdown = ref("");

vi.mock("@/composables/useAuth", () => ({ useAuth: () => ({ authMe }) }));
vi.mock("@/composables/useUsers", () => ({ useUsers: () => ({ users, currentUser }) }));
vi.mock("@/composables/useChat", () => ({
  useChat: () => ({
    currentConversationId,
    activeConversationIds,
    runningConversations,
    queuedConversations,
  }),
}));
vi.mock("@/composables/useConversations", () => ({
  useConversations: () => ({
    conversations,
    conversationsHasMore,
    isLoadingMoreConversations,
    loadMoreConversations: fake.loadMoreConversations,
    selectConversation: fake.selectConversation,
    startNewConversation: fake.startNewConversation,
    deleteConversation: fake.deleteConversation,
    renameConversation: fake.renameConversation,
    toggleConversationPinned: fake.toggleConversationPinned,
    reorderPinned: fake.reorderPinned,
    closeConversationDrawer: fake.closeConversationDrawer,
    applyRemoteUpdated: fake.applyRemoteUpdated,
  }),
}));
vi.mock("@/composables/useConversationListLayout", () => ({
  useConversationListLayout: () => ({ toggleConversationList: fake.toggleConversationList }),
}));
vi.mock("@/composables/useHelpDoc", () => ({
  useHelpDoc: () => ({ markdown: helpMarkdown }),
}));
vi.mock("@/composables/useConfirm", () => ({ useConfirm: () => ({ confirm: fake.confirm }) }));
vi.mock("@/composables/apiConversations", () => ({
  shareConversation: fake.shareConversation,
  unshareConversation: fake.unshareConversation,
}));
vi.mock("@/composables/apiAdmin", () => ({
  adminSessionBundleUrl: (id: string) => `/bundle/${id}`,
}));
vi.mock("@/lib/clipboard", () => ({ copyText: fake.copyText }));

import ConversationListContainer from "./ConversationListContainer.vue";

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "home", component: { template: "<div />" } },
      { path: "/settings", name: "settings", component: { template: "<div />" } },
      { path: "/settings/admin", name: "settings-admin", component: { template: "<div />" } },
      {
        path: "/chat/:userId?/:conversationId?",
        name: "chat",
        component: { template: "<div />" },
      },
    ],
  });
}

async function mountContainer(props: { drawer?: boolean } = {}) {
  const router = makeRouter();
  await router.push("/");
  await router.isReady();
  const wrapper = mount(ConversationListContainer, {
    props,
    global: { plugins: [router], stubs: { ConversationListPanel: true } },
  });
  return { wrapper, router, panel: wrapper.findComponent(ConversationListPanel) };
}

beforeEach(() => {
  vi.clearAllMocks();
  authMe.value = { name: "Alice", username: "alice", is_admin: false };
  currentUser.value = { id: "u1" };
  currentConversationId.value = "c1";
  activeConversationIds.value = [];
  conversationsHasMore.value = false;
  isLoadingMoreConversations.value = false;
  helpMarkdown.value = "";
  resetAppChromeStore();
  resetConversationAttentionStore();
});

describe("ConversationListContainer", () => {
  it("feeds the panel from app state", async () => {
    authMe.value = { name: "Alice", username: "alice", is_admin: true };
    activeConversationIds.value = ["c2"];
    applyAttention(
      [{ conversation_id: "c1", agent_id: "u1", title: "", state: "done", at: "" }],
      {},
    );
    conversationsHasMore.value = true;
    helpMarkdown.value = "# Help";
    upgradeAvailable.value = true;

    const { panel } = await mountContainer();

    expect(panel.props()).toMatchObject({
      conversations: conversations.value,
      currentConversationId: "c1",
      userId: "u1",
      runningConversationIds: ["c2"],
      conversationAttention: { c1: "done" },
      hasMore: true,
      loadingMore: false,
      isAdmin: true,
      accountName: "Alice",
      accountUsername: "alice",
      helpAvailable: true,
      upgradeAvailable: true,
      users: users.value,
      runningConversations: runningConversations.value,
      queuedConversations: queuedConversations.value,
      standalone: false,
    });
  });

  it("renders nothing without a current user", async () => {
    currentUser.value = null;
    const { panel } = await mountContainer();
    expect(panel.exists()).toBe(false);
  });

  it("selects a conversation and reflects it in the URL", async () => {
    const { panel, router } = await mountContainer();
    panel.vm.$emit("select-conversation", "c2");
    await flushPromises();

    expect(fake.selectConversation).toHaveBeenCalledWith("c2");
    expect(router.currentRoute.value.params).toMatchObject({ userId: "u1", conversationId: "c2" });
    expect(fake.closeConversationDrawer).not.toHaveBeenCalled();
  });

  it("dismisses the drawer after a selection in drawer mode", async () => {
    const { panel } = await mountContainer({ drawer: true });
    expect(panel.props("standalone")).toBe(true);

    panel.vm.$emit("select-conversation", "c2");
    await flushPromises();
    expect(fake.selectConversation).toHaveBeenCalledWith("c2");
    expect(fake.closeConversationDrawer).toHaveBeenCalledTimes(1);
  });

  it("collapses the docked column but closes the drawer", async () => {
    const docked = await mountContainer();
    docked.panel.vm.$emit("toggle-conversations");
    expect(fake.toggleConversationList).toHaveBeenCalledTimes(1);
    expect(fake.closeConversationDrawer).not.toHaveBeenCalled();

    const drawer = await mountContainer({ drawer: true });
    drawer.panel.vm.$emit("toggle-conversations");
    expect(fake.closeConversationDrawer).toHaveBeenCalledTimes(1);
    expect(fake.toggleConversationList).toHaveBeenCalledTimes(1);
  });

  it("scopes new conversations and pin reorders to the current user", async () => {
    const { panel } = await mountContainer();
    panel.vm.$emit("new-conversation");
    panel.vm.$emit("reorder-pinned", ["c2", "c1"]);

    expect(fake.startNewConversation).toHaveBeenCalledWith("u1");
    expect(fake.reorderPinned).toHaveBeenCalledWith("u1", ["c2", "c1"]);
  });

  it("forwards row edits to the conversation store", async () => {
    const { panel } = await mountContainer();
    panel.vm.$emit("rename-conversation", "c1", "Renamed");
    panel.vm.$emit("toggle-pinned", "c1", true);
    panel.vm.$emit("load-more");

    expect(fake.renameConversation).toHaveBeenCalledWith("c1", "Renamed");
    expect(fake.toggleConversationPinned).toHaveBeenCalledWith("c1", true);
    expect(fake.loadMoreConversations).toHaveBeenCalled();
  });

  it("confirms a delete unless the row asked to skip it", async () => {
    const { panel } = await mountContainer();
    panel.vm.$emit("delete-conversation", "c1", false);
    await flushPromises();
    expect(fake.confirm).toHaveBeenCalledTimes(1);
    expect(fake.deleteConversation).toHaveBeenCalledWith("c1");

    panel.vm.$emit("delete-conversation", "c2", true);
    await flushPromises();
    expect(fake.confirm).toHaveBeenCalledTimes(1);
    expect(fake.deleteConversation).toHaveBeenCalledWith("c2");
  });

  it("shares, copies the absolute link and raises the share toast", async () => {
    const row = { id: "c1", shared: true };
    fake.shareConversation.mockResolvedValue({ conversation: row, url: "/s/abc" });
    const { panel } = await mountContainer();

    panel.vm.$emit("share-conversation", "c1");
    await flushPromises();

    expect(fake.applyRemoteUpdated).toHaveBeenCalledWith(row, { broadcast: true });
    expect(fake.copyText).toHaveBeenCalledWith(`${window.location.origin}/s/abc`);
    expect(shareNotice.value).not.toBe("");
  });

  it("unshares and patches the row in place", async () => {
    const row = { id: "c1", shared: false };
    fake.unshareConversation.mockResolvedValue(row);
    const { panel } = await mountContainer();

    panel.vm.$emit("unshare-conversation", "c1");
    await flushPromises();

    expect(fake.applyRemoteUpdated).toHaveBeenCalledWith(row, { broadcast: true });
    expect(shareNotice.value).not.toBe("");
  });

  it("opens the app-wide dialogs and settings from the account menu", async () => {
    const { panel, router } = await mountContainer();
    panel.vm.$emit("open-help");
    panel.vm.$emit("open-marketplace");
    panel.vm.$emit("open-settings");
    await flushPromises();

    expect(helpDialogOpen.value).toBe(true);
    expect(marketplaceDialogOpen.value).toBe(true);
    expect(router.currentRoute.value.name).toBe("settings");
  });

  it("routes the admin settings entry to the admin panel", async () => {
    const { panel, router } = await mountContainer();
    panel.vm.$emit("open-admin-settings");
    await flushPromises();

    expect(router.currentRoute.value.name).toBe("settings-admin");
  });
});
