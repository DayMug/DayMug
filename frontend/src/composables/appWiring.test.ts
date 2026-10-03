import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, ref } from "vue";

import type { Conversation } from "@/composables/apiTypes";
import {
  applyAttention,
  conversationAttention,
  resetConversationAttentionStore,
} from "@/stores/conversationAttentionStore";

const state = vi.hoisted(() => ({
  refreshConversations: vi.fn(async () => {}),
  applyTitleUpdate: vi.fn(),
  applyRemoteAdded: vi.fn(),
  applyRemoteUpdated: vi.fn(),
  applyRemoteRemoved: vi.fn(),
  selectConversation: vi.fn(async () => {}),
}));

const currentUser = ref<{ id: string } | null>(null);
const chatUserId = ref<string | undefined>(undefined);
const resultVersion = ref(0);
const titleUpdate = ref<{ id: string; title: string; version: number } | null>(null);
const conversationListEvent = ref<{
  kind: "added" | "removed" | "updated";
  id: string;
  conversation?: Conversation;
  version: number;
} | null>(null);
const conversationListResync = ref(0);
const currentConversationId = ref("");
const conversations = ref<Conversation[]>([]);

vi.mock("./useUsers", () => ({ useUsers: () => ({ currentUser }) }));
vi.mock("./useChat", () => ({
  useChat: () => ({
    chatUserId,
    resultVersion,
    titleUpdate,
    conversationListEvent,
    conversationListResync,
    currentConversationId,
  }),
}));
vi.mock("./useConversations", () => ({
  useConversations: () => ({
    conversations,
    refreshConversations: state.refreshConversations,
    applyTitleUpdate: state.applyTitleUpdate,
    applyRemoteAdded: state.applyRemoteAdded,
    applyRemoteUpdated: state.applyRemoteUpdated,
    applyRemoteRemoved: state.applyRemoteRemoved,
    selectConversation: state.selectConversation,
  }),
}));

import { installAppWiring, uninstallAppWiring } from "./appWiring";

function row(id: string): Conversation {
  return {
    id,
    user_id: "u1",
    title: id,
    provider: "claude",
    model: "",
    work_dir: "/srv",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    created_at: "",
    updated_at: "",
  };
}

let version = 0;
function publish(kind: "added" | "removed" | "updated", id: string, conversation?: Conversation) {
  conversationListEvent.value = { kind, id, conversation, version: ++version };
}

beforeEach(() => {
  vi.clearAllMocks();
  currentUser.value = null;
  chatUserId.value = undefined;
  resultVersion.value = 0;
  titleUpdate.value = null;
  conversationListEvent.value = null;
  conversationListResync.value = 0;
  currentConversationId.value = "";
  conversations.value = [];
  resetConversationAttentionStore();
});

afterEach(() => {
  uninstallAppWiring();
});

describe("installAppWiring", () => {
  it("does nothing until it is installed", async () => {
    currentUser.value = { id: "u1" };
    resultVersion.value++;
    await nextTick();

    expect(chatUserId.value).toBeUndefined();
    expect(state.refreshConversations).not.toHaveBeenCalled();
  });

  it("mirrors the current user into the WS identity immediately and on change", async () => {
    currentUser.value = { id: "u1" };
    installAppWiring();
    expect(chatUserId.value).toBe("u1");

    currentUser.value = { id: "u2" };
    await nextTick();
    expect(chatUserId.value).toBe("u2");
  });

  it("is idempotent, so a second install cannot double every refresh", async () => {
    currentUser.value = { id: "u1" };
    currentConversationId.value = "c1";
    installAppWiring();
    installAppWiring();

    resultVersion.value++;
    await nextTick();
    expect(state.refreshConversations).toHaveBeenCalledTimes(1);
    expect(state.refreshConversations).toHaveBeenCalledWith("u1");
  });

  it("skips the result-time refresh without an active conversation", async () => {
    currentUser.value = { id: "u1" };
    installAppWiring();

    resultVersion.value++;
    await nextTick();
    expect(state.refreshConversations).not.toHaveBeenCalled();
  });

  it("applies pushed titles in place", async () => {
    installAppWiring();
    titleUpdate.value = { id: "c1", title: "Named", version: 1 };
    await nextTick();
    expect(state.applyTitleUpdate).toHaveBeenCalledWith("c1", "Named");
  });

  it("forwards added and updated list events as broadcasts", async () => {
    installAppWiring();
    publish("added", "c1", row("c1"));
    await nextTick();
    expect(state.applyRemoteAdded).toHaveBeenCalledWith(row("c1"), { broadcast: true });

    publish("updated", "c1", row("c1"));
    await nextTick();
    expect(state.applyRemoteUpdated).toHaveBeenCalledWith(row("c1"), { broadcast: true });
  });

  it("pivots to the top remaining conversation when the active one is removed elsewhere", async () => {
    applyAttention(
      [{ conversation_id: "c1", agent_id: "u1", title: "", state: "done", at: "" }],
      {},
    );
    currentConversationId.value = "c1";
    conversations.value = [row("c2")];
    installAppWiring();

    publish("removed", "c1");
    await nextTick();
    expect(conversationAttention.value).toEqual({});
    expect(state.applyRemoteRemoved).toHaveBeenCalledWith("c1", { broadcast: true });
    expect(state.selectConversation).toHaveBeenCalledWith("c2");
  });

  it("stays on the focused conversation when a different one is removed", async () => {
    currentConversationId.value = "c2";
    conversations.value = [row("c2")];
    installAppWiring();

    publish("removed", "c1");
    await nextTick();
    expect(state.applyRemoteRemoved).toHaveBeenCalledWith("c1", { broadcast: true });
    expect(state.selectConversation).not.toHaveBeenCalled();
  });

  it("refetches the list on a resync signal but ignores the reset value", async () => {
    currentUser.value = { id: "u1" };
    conversationListResync.value = 3;
    installAppWiring();

    conversationListResync.value = 0;
    await nextTick();
    expect(state.refreshConversations).not.toHaveBeenCalled();

    conversationListResync.value = 1;
    await nextTick();
    expect(state.refreshConversations).toHaveBeenCalledWith("u1");
  });

  it("stops reacting once uninstalled", async () => {
    currentUser.value = { id: "u1" };
    const uninstall = installAppWiring();
    uninstall();

    currentUser.value = { id: "u2" };
    await nextTick();
    expect(chatUserId.value).toBe("u1");
  });
});
