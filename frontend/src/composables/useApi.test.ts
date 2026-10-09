import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  fetchUsers,
  createUser,
  updateUser,
  deleteUser,
  fetchConversations,
  createConversation,
  deleteConversation,
  renameConversation,
  fetchMessages,
} from "./useApi";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("useApi - users", () => {
  it("fetchUsers returns list", async () => {
    const data = [{ id: "u1", name: "Alice", work_dir: "/tmp", avatar: "", created_at: "" }];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(data) }),
    );
    const result = await fetchUsers();
    expect(result).toEqual(data);
  });

  it("createUser posts and returns", async () => {
    const user = { id: "u2", name: "Bob", work_dir: "/home/bob", avatar: "" };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(user) }),
    );
    const result = await createUser({ name: "Bob", work_dir: "/home/bob" });
    expect(result).toEqual(user);
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/users"),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("updateUser sends PUT and returns", async () => {
    const user = { id: "u1", name: "Updated", work_dir: "/new", avatar: "X", created_at: "" };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(user) }),
    );
    const result = await updateUser("u1", {
      name: "Updated",
      work_dir: "/new",
      avatar: "X",
      role_definition: "",
      mcp_config: "",
      claude_md_content: "",
      manage_claude_md: false,
    });
    expect(result).toEqual(user);
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/users/u1"),
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("deleteUser sends DELETE", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true }));
    await deleteUser("u1");
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/users/u1"),
      expect.objectContaining({ method: "DELETE" }),
    );
  });
});

describe("useApi - conversations", () => {
  it("fetchConversations returns list", async () => {
    const data = [{ id: "c1", user_id: "u1", title: "Test", created_at: "", updated_at: "" }];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(data) }),
    );
    const result = await fetchConversations("u1");
    expect(result).toEqual(data);
    expect(fetch).toHaveBeenCalledWith(expect.stringContaining("user_id=u1"), undefined);
  });

  it("createConversation posts and returns", async () => {
    const conv = { id: "c2", user_id: "u1", title: "New" };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(conv) }),
    );
    const result = await createConversation("u1", "New");
    expect(result).toEqual(conv);
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/conversations"),
      expect.objectContaining({ method: "POST" }),
    );
  });

  it("deleteConversation sends DELETE", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true }));
    await deleteConversation("c1");
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/conversations/c1"),
      expect.objectContaining({ method: "DELETE" }),
    );
  });

  it("renameConversation sends PUT and returns", async () => {
    const conv = { id: "c1", user_id: "u1", title: "Renamed" };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(conv) }),
    );
    const result = await renameConversation("c1", "Renamed");
    expect(result).toEqual(conv);
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/api/conversations/c1/title"),
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("fetchMessages returns messages", async () => {
    const msgs = [{ id: "m1", role: "user", content: "hi" }];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(msgs) }),
    );
    const result = await fetchMessages("c1");
    expect(result).toEqual(msgs);
    expect(fetch).toHaveBeenCalledWith(
      expect.stringMatching(/\/api\/conversations\/c1\/messages$/),
      expect.anything(),
    );
  });

  it("fetchMessages sends the reverse-paging cursor even when empty", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve([]) }),
    );
    await fetchMessages("c1", { beforeId: "", limit: 50 });
    expect(fetch).toHaveBeenCalledWith(
      expect.stringMatching(/\/api\/conversations\/c1\/messages\?limit=50&before_id=$/),
      expect.anything(),
    );
  });

  it("fetchConversations throws on error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 500 }));
    await expect(fetchConversations("u1")).rejects.toThrow("500");
  });
});
