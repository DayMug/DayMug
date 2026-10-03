import { describe, it, expect, vi, beforeEach } from "vitest";
import { updateUser, deleteUser, archiveUser } from "./apiUsers";
import type { UpdateUserData } from "./types/user";

const stubUpdate: UpdateUserData = {
  name: "agent",
  work_dir: "/tmp/agent",
  avatar: "",
  role_definition: "",
  mcp_config: "",
  claude_md_content: "",
  manage_claude_md: false,
};

beforeEach(() => {
  vi.restoreAllMocks();
});

// The rejection reason (work_dir outside the jail, malformed MCP JSON) only
// exists in the response body. These mutations render their failure to the
// user now, so dropping the body would leave them staring at a bare status.
function rejectWith(reason: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      json: () => Promise.resolve({ error: reason }),
    }),
  );
}

describe("apiUsers surfaces backend rejection reasons", () => {
  it("updateUser rejects with the body message, not the status code", async () => {
    rejectWith("work_dir must stay inside /home/agents");
    await expect(updateUser("u1", stubUpdate)).rejects.toThrow(
      "work_dir must stay inside /home/agents",
    );
  });

  it("deleteUser rejects with the body message", async () => {
    rejectWith("agent still has running jobs");
    await expect(deleteUser("u1")).rejects.toThrow("agent still has running jobs");
  });

  it("archiveUser rejects with the body message", async () => {
    rejectWith("cannot archive the last agent");
    await expect(archiveUser("u1")).rejects.toThrow("cannot archive the last agent");
  });

  it("falls back to the labelled status when the body carries no reason", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 500,
        json: () => Promise.reject(new Error("not json")),
      }),
    );
    await expect(updateUser("u1", stubUpdate)).rejects.toThrow("update user: 500");
  });
});
