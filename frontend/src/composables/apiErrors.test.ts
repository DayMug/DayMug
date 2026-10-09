import { describe, it, expect, vi, afterEach } from "vitest";
import { setUnauthorizedHandler, UnauthorizedError } from "./apiClient";
import { fetchUsers, deleteAgentBot, updateUser, archiveUser } from "./apiUsers";
import { deleteConversation, updateConversationModel } from "./apiConversations";
import { createCronJob } from "./apiCron";
import { deleteMarketplaceApp } from "./apiMarketplace";
import { login, changeMyPassword } from "./apiAuth";
import {
  adminListUsers,
  adminFetchPublicConfig,
  adminCreateUser,
  adminDeleteUser,
  adminSetDisabled,
  adminFetchPricing,
  adminDatabaseSize,
} from "./apiAdmin";
import { useFileApi, WriteConflictError } from "./useFileApi";
import type { UpdateUserData } from "./types/user";
import type { CronJobInput } from "./types/cron";

// These tests pin the exact error text every API path throws: the UI renders
// those messages verbatim, so the shared request helper must reproduce each
// historical format byte for byte.

type Body = { kind: "json"; value: unknown } | { kind: "invalid" };

function respondWith(status: number, body: Body) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: () =>
        body.kind === "json" ? Promise.resolve(body.value) : Promise.reject(new Error("not json")),
    }),
  );
}

const withReason = (reason: unknown): Body => ({ kind: "json", value: { error: reason } });
const invalid: Body = { kind: "invalid" };

const stubUser: UpdateUserData = {
  name: "a",
  work_dir: "/tmp/a",
  avatar: "",
  role_definition: "",
  mcp_config: "",
  claude_md_content: "",
  manage_claude_md: false,
};
const stubCron = {} as CronJobInput;

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(null);
});

async function caught(p: Promise<unknown>): Promise<Error & { status?: number }> {
  try {
    await p;
  } catch (e) {
    return e as Error & { status?: number };
  }
  throw new Error("expected rejection");
}

describe("label + status errors ignore the body", () => {
  const cases: [string, () => Promise<unknown>, string][] = [
    ["fetchUsers", () => fetchUsers(), "fetch users: 500"],
    ["deleteConversation", () => deleteConversation("c"), "delete conversation: 500"],
    ["deleteAgentBot", () => deleteAgentBot("u", "b"), "delete agent bot: 500"],
    ["adminListUsers", () => adminListUsers(), "admin list users: 500"],
    ["adminFetchPublicConfig", () => adminFetchPublicConfig(), "admin public config: 500"],
    ["readFile", () => useFileApi().readFile("u", "a.txt"), "read file: 500"],
  ];

  it.each(cases)("%s", async (_name, call, want) => {
    respondWith(500, withReason("server said no"));
    const err = await caught(call());
    expect(err.message).toBe(want);
  });
});

describe("body error replaces the labelled status", () => {
  const cases: [string, () => Promise<unknown>, string][] = [
    ["updateUser", () => updateUser("u", stubUser), "update user: 500"],
    ["archiveUser", () => archiveUser("u"), "archive agent: 500"],
    ["createCronJob", () => createCronJob(stubCron), "create scheduled task: 500"],
    ["deleteMarketplaceApp", () => deleteMarketplaceApp("m"), "delete app: 500"],
    ["changeMyPassword", () => changeMyPassword("a", "b"), "change password: 500"],
    ["adminCreateUser", () => adminCreateUser({} as never), "admin create user: 500"],
    ["adminDeleteUser", () => adminDeleteUser("u"), "admin delete user: 500"],
    ["adminSetDisabled(true)", () => adminSetDisabled("u", true), "admin disable: 500"],
    ["adminSetDisabled(false)", () => adminSetDisabled("u", false), "admin enable: 500"],
    ["adminFetchPricing", () => adminFetchPricing("codex"), "codex pricing: 500"],
    ["adminDatabaseSize", () => adminDatabaseSize(), "db size: 500"],
  ];

  it.each(cases)("%s uses the body reason", async (_name, call) => {
    respondWith(500, withReason("server said no"));
    expect((await caught(call())).message).toBe("server said no");
  });

  it.each(cases)("%s falls back when the body is not JSON", async (_name, call, fallback) => {
    respondWith(500, invalid);
    expect((await caught(call())).message).toBe(fallback);
  });

  it.each(cases)("%s falls back on an empty reason", async (_name, call, fallback) => {
    respondWith(500, withReason(""));
    expect((await caught(call())).message).toBe(fallback);
  });

  it.each(cases)("%s falls back on a null body", async (_name, call, fallback) => {
    respondWith(500, { kind: "json", value: null });
    expect((await caught(call())).message).toBe(fallback);
  });
});

describe("body error falls back to the bare status", () => {
  const cases: [string, () => Promise<unknown>][] = [
    ["updateConversationModel", () => updateConversationModel("c", "claude", "m")],
  ];

  it.each(cases)("%s uses the body reason", async (_name, call) => {
    respondWith(409, withReason("disabled in this deploy"));
    expect((await caught(call())).message).toBe("disabled in this deploy");
  });

  it.each(cases)("%s falls back to the status alone", async (_name, call) => {
    respondWith(409, invalid);
    expect((await caught(call())).message).toBe("409");
  });
});

describe("body error appended to the labelled status", () => {
  const api = useFileApi();
  const cases: [string, () => Promise<unknown>, string][] = [
    ["writeFile", () => api.writeFile("u", "a.txt", "x"), "write file: 400"],
    ["extractArchive", () => api.extractArchive("u", "a.zip"), "extract: 400"],
    ["compressToZip", () => api.compressToZip("u", ["a"], ""), "compress: 400"],
  ];

  it.each(cases)("%s appends the body reason", async (_name, call, prefix) => {
    respondWith(400, withReason("bad path"));
    expect((await caught(call())).message).toBe(`${prefix}: bad path`);
  });

  it.each(cases)("%s leaves the bare labelled status", async (_name, call, prefix) => {
    respondWith(400, invalid);
    expect((await caught(call())).message).toBe(prefix);
  });

  it("writeFile turns 412 into a WriteConflictError with the body etag", async () => {
    respondWith(412, { kind: "json", value: { error: "stale", etag: "v2" } });
    const err = await caught(api.writeFile("u", "a.txt", "x", "v1"));
    expect(err).toBeInstanceOf(WriteConflictError);
    expect((err as WriteConflictError).etag).toBe("v2");
  });
});

describe("errors carry the HTTP status where callers branch on it", () => {
  it("login keeps the body reason and the status", async () => {
    respondWith(401, withReason("invalid username or password"));
    const err = await caught(login("a", "b"));
    expect(err.message).toBe("invalid username or password");
    expect(err.status).toBe(401);
  });

  it("login falls back to its labelled status", async () => {
    respondWith(429, invalid);
    const err = await caught(login("a", "b"));
    expect(err.message).toBe("login: 429");
    expect(err.status).toBe(429);
  });

  it("moveFile exposes 409 so the UI can offer overwrite / rename", async () => {
    respondWith(409, withReason("exists"));
    const err = await caught(useFileApi().moveFile("u", "a", "b"));
    expect(err.message).toBe("move: 409");
    expect(err.status).toBe(409);
  });
});

describe("401 outside the login endpoint", () => {
  it("fires the unauthorized handler and rejects with UnauthorizedError", async () => {
    const handler = vi.fn();
    setUnauthorizedHandler(handler);
    respondWith(401, withReason("session expired"));
    const err = await caught(adminCreateUser({} as never));
    expect(err).toBeInstanceOf(UnauthorizedError);
    expect(err.message).toBe("unauthorized");
    expect(handler).toHaveBeenCalledOnce();
  });
});

describe("successful responses", () => {
  it("unwraps envelope bodies", async () => {
    respondWith(200, { kind: "json", value: { bytes: 42 } });
    await expect(adminDatabaseSize()).resolves.toBe(42);
  });

  it("void endpoints resolve to undefined without reading the body", async () => {
    const json = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 204, json }));
    await expect(deleteConversation("c")).resolves.toBeUndefined();
    await expect(adminDeleteUser("u")).resolves.toBeUndefined();
    expect(json).not.toHaveBeenCalled();
  });
});
