import { describe, it, expect, vi, beforeEach } from "vitest";
import { ref } from "vue";
import type { Router } from "vue-router";

import type { User } from "@/composables/useApi";

const mockConfirm = vi.hoisted(() => vi.fn(async () => false));
const mockDeleteStaleConversations = vi.hoisted(() => vi.fn());
vi.mock("@/composables/useConfirm", () => ({
  useConfirm: () => ({ confirm: mockConfirm }),
}));
vi.mock("@/composables/apiConversations", () => ({
  deleteStaleConversations: mockDeleteStaleConversations,
}));

import { useAppShell } from "./useAppShell";
import type { AppShellDeps } from "./useAppShell";

function makeUser(id: string, extra: Partial<User> = {}): User {
  return { id, name: `Agent ${id}`, ...extra } as User;
}

function makeDeps(overrides: Partial<AppShellDeps> = {}) {
  const router = { push: vi.fn(), replace: vi.fn() } as unknown as Router;
  const deps: AppShellDeps = {
    router,
    t: (key: string) => key,
    currentUser: ref<User | null>(null),
    currentConversationId: ref<string | null>("conv-1"),
    conversations: ref([]),
    selectUser: vi.fn(async () => {}),
    selectConversation: vi.fn(async () => {}),
    startNewConversation: vi.fn(async () => {}),
    deleteConversation: vi.fn(async () => {}),
    handleArchiveUser: vi.fn(async () => {}),
    loadUsers: vi.fn(async () => {}),
    loadConversationsForAgent: vi.fn(async () => {}),
    refreshConversationsForAgent: vi.fn(async () => {}),
    applyRemoteRemoved: vi.fn(() => true),
    ...overrides,
  };
  return deps;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("useAppShell", () => {
  describe("handleDeleteConversation", () => {
    it("deletes without prompting when skipConfirm is true", async () => {
      mockConfirm.mockResolvedValue(false);
      const deps = makeDeps();
      const shell = useAppShell(deps);

      await shell.handleDeleteConversation("conv-9", true);

      expect(mockConfirm).not.toHaveBeenCalled();
      expect(deps.deleteConversation).toHaveBeenCalledWith("conv-9");
    });

    it("aborts when the confirmation is declined", async () => {
      mockConfirm.mockResolvedValue(false);
      const deps = makeDeps();
      const shell = useAppShell(deps);

      await shell.handleDeleteConversation("conv-9", false);

      expect(deps.deleteConversation).not.toHaveBeenCalled();
    });

    it("deletes when the confirmation is accepted", async () => {
      mockConfirm.mockResolvedValue(true);
      const deps = makeDeps();
      const shell = useAppShell(deps);

      await shell.handleDeleteConversation("conv-9", false);

      expect(deps.deleteConversation).toHaveBeenCalledWith("conv-9");
    });
  });

  describe("handleSelectConversation", () => {
    it("selects the conversation and reflects it in the URL", async () => {
      const deps = makeDeps({ currentUser: ref<User | null>(makeUser("u1")) });
      const shell = useAppShell(deps);

      await shell.handleSelectConversation("conv-2");

      expect(deps.selectConversation).toHaveBeenCalledWith("conv-2");
      expect(deps.router.replace).toHaveBeenCalledWith({
        name: "chat",
        params: { userId: "u1", conversationId: "conv-2" },
      });
    });

    it("skips the URL update when no user is active", async () => {
      const deps = makeDeps();
      const shell = useAppShell(deps);

      await shell.handleSelectConversation("conv-2");

      expect(deps.router.replace).not.toHaveBeenCalled();
    });
  });

  describe("handleSelectUser", () => {
    it("pushes the new user + current conversation in one navigation", async () => {
      const deps = makeDeps({ currentConversationId: ref<string | null>("conv-7") });
      const shell = useAppShell(deps);

      await shell.handleSelectUser("u2");

      expect(deps.selectUser).toHaveBeenCalledWith("u2");
      expect(deps.router.push).toHaveBeenCalledWith({
        name: "chat",
        params: { userId: "u2", conversationId: "conv-7" },
      });
    });

    it("falls back to an empty conversation segment when none is active", async () => {
      const deps = makeDeps({ currentConversationId: ref<string | null>(null) });
      const shell = useAppShell(deps);

      await shell.handleSelectUser("u2");

      expect(deps.router.push).toHaveBeenCalledWith({
        name: "chat",
        params: { userId: "u2", conversationId: "" },
      });
    });
  });

  describe("context menu", () => {
    it("opens at the click position and closes on edit", () => {
      const deps = makeDeps();
      const shell = useAppShell(deps);
      const user = makeUser("u1");

      shell.handleUserContextMenu({ clientX: 11, clientY: 22 } as MouseEvent, user);
      expect(shell.contextMenuUser.value).toEqual(user);
      expect(shell.contextMenuPos.value).toEqual({ x: 11, y: 22 });

      shell.handleEditUser(user);
      expect(shell.contextMenuUser.value).toBeNull();
      expect(deps.router.push).toHaveBeenCalledWith({
        name: "settings-users-edit",
        params: { id: "u1" },
      });
    });
  });

  describe("handleArchiveAgent", () => {
    it("archives and closes the menu once the confirmation is accepted", async () => {
      mockConfirm.mockResolvedValue(true);
      const deps = makeDeps();
      const shell = useAppShell(deps);
      const user = makeUser("u1");
      shell.contextMenuUser.value = user;

      await shell.handleArchiveAgent(user);

      expect(shell.contextMenuUser.value).toBeNull();
      expect(deps.handleArchiveUser).toHaveBeenCalledWith("u1");
    });

    it("leaves the agent alone when the confirmation is declined", async () => {
      mockConfirm.mockResolvedValue(false);
      const deps = makeDeps();
      const shell = useAppShell(deps);

      await shell.handleArchiveAgent(makeUser("u1"));

      expect(deps.handleArchiveUser).not.toHaveBeenCalled();
    });
  });

  describe("handleCleanupStaleConversations", () => {
    it("removes returned ids and selects the next conversation", async () => {
      mockConfirm.mockResolvedValue(true);
      mockDeleteStaleConversations.mockResolvedValue({
        deleted_ids: ["conv-1", "conv-old"],
        deleted_count: 2,
      });
      const conversations = ref([{ id: "conv-next" }] as never[]);
      const deps = makeDeps({
        currentUser: ref<User | null>(makeUser("u1")),
        conversations,
      });
      const shell = useAppShell(deps);

      await shell.handleCleanupStaleConversations(makeUser("u1"));

      expect(mockDeleteStaleConversations).toHaveBeenCalledWith("u1");
      expect(deps.applyRemoteRemoved).toHaveBeenCalledTimes(2);
      expect(deps.selectConversation).toHaveBeenCalledWith("conv-next");
    });

    it("creates a replacement when cleanup removes the agent's last active conversation", async () => {
      mockConfirm.mockResolvedValue(true);
      mockDeleteStaleConversations.mockResolvedValue({ deleted_ids: ["conv-1"], deleted_count: 1 });
      const deps = makeDeps({ currentUser: ref<User | null>(makeUser("u1")) });
      const shell = useAppShell(deps);

      await shell.handleCleanupStaleConversations(makeUser("u1"));

      expect(deps.startNewConversation).toHaveBeenCalledWith("u1");
    });
  });

  describe("mobile IA", () => {
    it("handleMobileSelectConversation switches agents before drilling in", async () => {
      const deps = makeDeps({ currentUser: ref<User | null>(makeUser("u1")) });
      const shell = useAppShell(deps);

      await shell.handleMobileSelectConversation(makeUser("u2"), "conv-3");

      expect(deps.selectUser).toHaveBeenCalledWith("u2", "conv-3");
      expect(deps.selectConversation).not.toHaveBeenCalled();
      expect(deps.router.push).toHaveBeenCalledWith({
        name: "chat",
        params: { userId: "u2", conversationId: "conv-3" },
      });
    });

    it("handleMobileSelectConversation reuses the active agent's selection path", async () => {
      const deps = makeDeps({ currentUser: ref<User | null>(makeUser("u1")) });
      const shell = useAppShell(deps);

      await shell.handleMobileSelectConversation(makeUser("u1"), "conv-3");

      expect(deps.selectUser).not.toHaveBeenCalled();
      expect(deps.selectConversation).toHaveBeenCalledWith("conv-3");
      expect(deps.router.push).toHaveBeenCalledWith({
        name: "chat",
        params: { userId: "u1", conversationId: "conv-3" },
      });
    });

    it("handleMobileRefresh refreshes users plus the expanded agent and clears the spinner", async () => {
      const deps = makeDeps();
      const shell = useAppShell(deps);

      const p = shell.handleMobileRefresh("u9");
      expect(shell.mobileRefreshing.value).toBe(true);
      await p;

      expect(deps.loadUsers).toHaveBeenCalled();
      expect(deps.refreshConversationsForAgent).toHaveBeenCalledWith("u9");
      expect(shell.mobileRefreshing.value).toBe(false);
    });

    it("handleMobileRefresh skips the per-agent refresh when nothing is expanded", async () => {
      const deps = makeDeps();
      const shell = useAppShell(deps);

      await shell.handleMobileRefresh(null);

      expect(deps.refreshConversationsForAgent).not.toHaveBeenCalled();
    });

    it("handleMobileNewConversation switches agent, starts a conversation, and navigates", async () => {
      const deps = makeDeps({ currentUser: ref<User | null>(makeUser("u1")) });
      const shell = useAppShell(deps);

      shell.handleMobileNewConversation(makeUser("u2"));
      await new Promise((r) => setTimeout(r, 0));

      expect(deps.selectUser).toHaveBeenCalledWith("u2");
      expect(deps.startNewConversation).toHaveBeenCalledWith("u2");
      expect(deps.router.push).toHaveBeenCalledWith({
        name: "chat",
        params: { userId: "u2", conversationId: "conv-1" },
      });
    });
  });
});
