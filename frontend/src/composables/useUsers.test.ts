import { describe, it, expect, vi, beforeEach } from "vitest";

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

const mockFetchUsers = vi.fn().mockResolvedValue([]);
const mockCreateUser = vi.fn();
const mockUpdateUser = vi.fn();
const mockDeleteUser = vi.fn();
const mockReorderUsers = vi.fn();
const mockArchiveUser = vi.fn();
const mockUnarchiveUser = vi.fn();
const mockFetchArchivedAgents = vi.fn().mockResolvedValue([]);

vi.mock("./useApi", () => ({
  fetchUsers: (...args: unknown[]) => mockFetchUsers(...args),
  createUser: (...args: unknown[]) => mockCreateUser(...args),
  updateUser: (...args: unknown[]) => mockUpdateUser(...args),
  deleteUser: (...args: unknown[]) => mockDeleteUser(...args),
  reorderUsers: (...args: unknown[]) => mockReorderUsers(...args),
  archiveUser: (...args: unknown[]) => mockArchiveUser(...args),
  unarchiveUser: (...args: unknown[]) => mockUnarchiveUser(...args),
  fetchArchivedAgents: (...args: unknown[]) => mockFetchArchivedAgents(...args),
}));

vi.mock("./useChat", () => ({
  useChat: () => ({
    disconnectWs: vi.fn(),
    currentConversationId: { value: "" },
  }),
}));

vi.mock("./useConversations", () => ({
  useConversations: () => ({
    loadConversations: vi.fn(),
    // selectUser short-circuits to the in-memory list when the active
    // agent already matches; mocking the refs as empty/string forces the
    // network-fetch path the existing tests exercise.
    conversations: { value: [] },
    activeAgentId: { value: "" },
  }),
}));

vi.stubGlobal("localStorage", {
  getItem: vi.fn(),
  setItem: vi.fn(),
  removeItem: vi.fn(),
  clear: vi.fn(),
});

import { useUsers } from "./useUsers";

beforeEach(() => {
  vi.clearAllMocks();
  const { users, currentUser, archivedAgents } = useUsers();
  users.value = [];
  currentUser.value = null;
  archivedAgents.value = [];
});

type UsersValue = ReturnType<typeof useUsers>["users"]["value"];

describe("useUsers", () => {
  it("loadUsers fetches and sets users", async () => {
    const mockData = [{ id: "u1", name: "Alice" }];
    mockFetchUsers.mockResolvedValueOnce(mockData);

    const { loadUsers, users } = useUsers();
    await loadUsers();
    expect(users.value).toEqual(mockData);
  });

  it("loadUsers filters archived rows defensively", async () => {
    const mockData = [
      { id: "h1", name: "Me", username: "me" },
      { id: "a1", name: "Archived", username: "", archived: true },
    ];
    mockFetchUsers.mockResolvedValueOnce(mockData);

    const { loadUsers, users } = useUsers();
    await loadUsers();
    expect(users.value.map((u) => u.id)).toEqual(["h1"]);
  });

  it("applyUsers filters archived rows from app-state", () => {
    const { applyUsers, users } = useUsers();
    applyUsers([
      { id: "h1", name: "Me", username: "me" },
      { id: "a1", name: "Archived", username: "", archived: true },
    ] as UsersValue);

    expect(users.value.map((u) => u.id)).toEqual(["h1"]);
  });

  it("selectUser sets currentUser and stores in localStorage", async () => {
    const { users, selectUser, currentUser } = useUsers();
    users.value = [{ id: "u1", name: "Alice" }] as ReturnType<typeof useUsers>["users"]["value"];

    await selectUser("u1");
    expect(currentUser.value?.id).toBe("u1");
    expect(localStorage.setItem).toHaveBeenCalledWith("daymug-user-id", "u1");
  });

  it("selectUser does nothing for unknown user", async () => {
    const { selectUser, currentUser } = useUsers();
    await selectUser("unknown");
    expect(currentUser.value).toBeNull();
  });

  it("handleCreateUser adds user and selects it", async () => {
    const newUser = { id: "u2", name: "Bob" };
    mockCreateUser.mockResolvedValueOnce(newUser);

    const { handleCreateUser, users } = useUsers();
    await handleCreateUser({ name: "Bob", work_dir: "/tmp" });
    expect(users.value).toContainEqual(newUser);
  });

  it("handleUpdateUser updates user in list", async () => {
    const { handleUpdateUser, users, currentUser } = useUsers();
    users.value = [{ id: "u1", name: "Alice", work_dir: "/old" }] as ReturnType<
      typeof useUsers
    >["users"]["value"];
    currentUser.value = users.value[0];

    const updated = { id: "u1", name: "Alice2", work_dir: "/new" };
    mockUpdateUser.mockResolvedValueOnce(updated);

    await handleUpdateUser("u1", updated as unknown as Parameters<typeof handleUpdateUser>[1]);
    expect(users.value[0].name).toBe("Alice2");
    expect(currentUser.value?.name).toBe("Alice2");
  });

  it("handleUpdateUser reports a rejected save to the caller", async () => {
    const { handleUpdateUser, users } = useUsers();
    users.value = [{ id: "u1", name: "Alice", work_dir: "/old" }] as UsersValue;

    mockUpdateUser.mockRejectedValueOnce(new Error("work_dir outside home"));
    const res = await handleUpdateUser("u1", {} as Parameters<typeof handleUpdateUser>[1]);

    expect(res).toEqual({ ok: false, error: "work_dir outside home" });
    expect(users.value[0].work_dir).toBe("/old");
  });

  it("handleDeleteUser reports a rejected delete and keeps the row", async () => {
    const { handleDeleteUser, users } = useUsers();
    users.value = [{ id: "u1", name: "Alice" }] as UsersValue;

    mockDeleteUser.mockRejectedValueOnce(new Error("delete user: 500"));
    const res = await handleDeleteUser("u1");

    expect(res).toEqual({ ok: false, error: "delete user: 500" });
    expect(users.value.map((u) => u.id)).toEqual(["u1"]);
  });

  it("loadUsers reports a failed fetch instead of leaving the list silently empty", async () => {
    mockFetchUsers.mockRejectedValueOnce(new Error("fetch users: 503"));

    const { loadUsers } = useUsers();
    expect(await loadUsers()).toEqual({ ok: false, error: "fetch users: 503" });
  });

  it("handleArchiveUser reports a rejected archive and keeps the agent listed", async () => {
    const { handleArchiveUser, users } = useUsers();
    users.value = [{ id: "a1", name: "A1", username: "" }] as UsersValue;

    mockArchiveUser.mockRejectedValueOnce(new Error("archive agent: 409"));
    const res = await handleArchiveUser("a1");

    expect(res).toEqual({ ok: false, error: "archive agent: 409" });
    expect(users.value.map((u) => u.id)).toEqual(["a1"]);
  });

  it("handleDeleteUser removes user from list", async () => {
    const { handleDeleteUser, users, currentUser } = useUsers();
    users.value = [
      { id: "u1", name: "Alice" },
      { id: "u2", name: "Bob" },
    ] as ReturnType<typeof useUsers>["users"]["value"];
    currentUser.value = users.value[0];

    mockDeleteUser.mockResolvedValueOnce(undefined);
    await handleDeleteUser("u1");
    expect(users.value.length).toBe(1);
    expect(users.value[0].id).toBe("u2");
  });

  it("handleArchiveUser removes the agent from the list", async () => {
    const { handleArchiveUser, users } = useUsers();
    users.value = [
      { id: "h1", name: "Me", username: "me" },
      { id: "a1", name: "A1", username: "" },
      { id: "a2", name: "A2", username: "" },
    ] as UsersValue;

    mockArchiveUser.mockResolvedValueOnce(undefined);
    await handleArchiveUser("a1");

    expect(users.value.map((u) => u.id)).toEqual(["h1", "a2"]);
    expect(mockArchiveUser).toHaveBeenCalledWith("a1");
  });

  it("handleReorderUsers applies the server-returned order", async () => {
    const { handleReorderUsers, users } = useUsers();
    users.value = [
      { id: "h1", name: "Me", username: "me" },
      { id: "a1", name: "A1", username: "" },
      { id: "a2", name: "A2", username: "" },
    ] as UsersValue;

    const serverList = [
      { id: "h1", name: "Me", username: "me" },
      { id: "a2", name: "A2", username: "" },
      { id: "a1", name: "A1", username: "" },
    ];
    mockReorderUsers.mockResolvedValueOnce(serverList);

    await handleReorderUsers(["a2", "a1"]);

    expect(mockReorderUsers).toHaveBeenCalledWith(["a2", "a1"]);
    expect(users.value.map((u) => u.id)).toEqual(["h1", "a2", "a1"]);
  });

  it("handleReorderUsers rolls back on failure", async () => {
    const { handleReorderUsers, users } = useUsers();
    const original = [
      { id: "h1", name: "Me", username: "me" },
      { id: "a1", name: "A1", username: "" },
      { id: "a2", name: "A2", username: "" },
    ] as UsersValue;
    users.value = original;

    mockReorderUsers.mockRejectedValueOnce(new Error("boom"));
    await handleReorderUsers(["a2", "a1"]);

    expect(users.value.map((u) => u.id)).toEqual(["h1", "a1", "a2"]);
  });

  it("handleRestoreUser moves an agent from archived back into the list", async () => {
    const { handleRestoreUser, users, archivedAgents } = useUsers();
    users.value = [{ id: "h1", name: "Me", username: "me" }] as UsersValue;
    archivedAgents.value = [{ id: "a1", name: "A1", username: "" }] as UsersValue;

    mockUnarchiveUser.mockResolvedValueOnce({ id: "a1", name: "A1", username: "" });
    await handleRestoreUser("a1");

    expect(archivedAgents.value.length).toBe(0);
    expect(users.value.map((u) => u.id)).toContain("a1");
    expect(mockUnarchiveUser).toHaveBeenCalledWith("a1");
  });
});
