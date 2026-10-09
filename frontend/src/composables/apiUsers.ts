import { jsonRequestInit, request } from "./apiClient";
import {
  type BrowseDirEntry,
  type BrowseDirResult,
  type CreateUserData,
  type UpdateUserData,
  type User,
  type AgentBot,
  type BotConnectionTestResult,
  type BotPermissionRequirements,
  type AgentBotInput,
  type AgentBotTestInput,
  type WeChatPairingStart,
  type WeChatPairingStatus,
} from "./apiTypes";

export async function fetchUsers(): Promise<User[]> {
  return request<User[]>(`/api/users`, undefined, { label: "fetch users" });
}

export async function createUser(data: CreateUserData): Promise<User> {
  return request<User>(
    `/api/users`,
    jsonRequestInit("POST", {
      name: data.name,
      work_dir: data.work_dir,
      avatar: data.avatar ?? "",
      role_definition: data.role_definition ?? "",
      mcp_config: data.mcp_config ?? "",
      claude_md_content: data.claude_md_content ?? "",
      manage_claude_md: data.manage_claude_md ?? false,
    }),
    { label: "create user" },
  );
}

export async function fetchAgentBotStatuses(userId: string) {
  return request<{ bots: import("./apiTypes").AgentBotStatus[] }>(
    `/api/users/${encodeURIComponent(userId)}/integrations/status`,
    undefined,
    { label: "fetch agent integration status" },
  );
}

export async function fetchAgentBots(userId: string): Promise<AgentBot[]> {
  return request<AgentBot[]>(`/api/users/${encodeURIComponent(userId)}/bots`, undefined, {
    label: "fetch agent bots",
  });
}

export async function fetchAgentBotRequirements(
  userId: string,
): Promise<BotPermissionRequirements> {
  return request<BotPermissionRequirements>(
    `/api/users/${encodeURIComponent(userId)}/bots/requirements`,
    undefined,
    { label: "fetch bot permission requirements" },
  );
}

export async function testAgentBotConnection(
  userId: string,
  data: AgentBotTestInput,
): Promise<BotConnectionTestResult> {
  return request<BotConnectionTestResult>(
    `/api/users/${encodeURIComponent(userId)}/bots/test-connection`,
    jsonRequestInit("POST", data),
    { label: "test bot connection", errorBody: "message" },
  );
}

export async function startWeChatPairing(userId: string): Promise<WeChatPairingStart> {
  return request<WeChatPairingStart>(
    `/api/users/${encodeURIComponent(userId)}/bots/wechat-pairing`,
    jsonRequestInit("POST", {}),
    { label: "start WeChat pairing", errorBody: "message" },
  );
}

// One round trip per call: the caller owns the retry loop so closing the dialog
// ends the pairing instead of leaving a request parked on the server.
export async function pollWeChatPairing(
  userId: string,
  challenge: string,
): Promise<WeChatPairingStatus> {
  return request<WeChatPairingStatus>(
    `/api/users/${encodeURIComponent(userId)}/bots/wechat-pairing?challenge=${encodeURIComponent(challenge)}`,
    undefined,
    { label: "poll WeChat pairing", errorBody: "message" },
  );
}

export async function createAgentBot(userId: string, data: AgentBotInput): Promise<AgentBot> {
  return request<AgentBot>(
    `/api/users/${encodeURIComponent(userId)}/bots`,
    jsonRequestInit("POST", data),
    { label: "create agent bot" },
  );
}

export async function updateAgentBot(
  userId: string,
  botId: string,
  data: AgentBotInput,
): Promise<AgentBot> {
  return request<AgentBot>(
    `/api/users/${encodeURIComponent(userId)}/bots/${encodeURIComponent(botId)}`,
    jsonRequestInit("PUT", data),
    { label: "update agent bot" },
  );
}

export async function deleteAgentBot(userId: string, botId: string): Promise<void> {
  await request<void>(
    `/api/users/${encodeURIComponent(userId)}/bots/${encodeURIComponent(botId)}`,
    { method: "DELETE" },
    { label: "delete agent bot", expect: "none" },
  );
}

export async function duplicateUser(id: string): Promise<User> {
  return request<User>(
    `/api/users/${encodeURIComponent(id)}/duplicate`,
    { method: "POST" },
    { label: "duplicate user" },
  );
}

export async function deleteUser(id: string): Promise<void> {
  await request<void>(
    `/api/users/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    { label: "delete user", errorBody: "message", expect: "none" },
  );
}

// reorderUsers persists the manual sidebar order of the caller's rows from a
// drag-to-reorder gesture. `ids` is the full ordered id list — the human owner
// and its agents, freely interleaved; the backend scopes the update to the
// caller's own rows and returns the refreshed list.
export async function reorderUsers(ids: string[]): Promise<User[]> {
  return request<User[]>(`/api/user-order`, jsonRequestInit("PUT", { ids }), {
    label: "reorder users",
  });
}

// archiveUser hides an agent from the sidebar. Restorable via unarchiveUser.
export async function archiveUser(id: string): Promise<void> {
  await request<void>(
    `/api/users/${encodeURIComponent(id)}/archive`,
    { method: "POST" },
    { label: "archive agent", errorBody: "message", expect: "none" },
  );
}

// unarchiveUser restores a previously archived agent and returns the row.
export async function unarchiveUser(id: string): Promise<User> {
  return request<User>(
    `/api/users/${encodeURIComponent(id)}/unarchive`,
    { method: "POST" },
    { label: "restore agent" },
  );
}

// fetchArchivedAgents lists the caller's archived agents for the settings
// restore view.
export async function fetchArchivedAgents(): Promise<User[]> {
  return request<User[]>(`/api/archived-agents`, undefined, { label: "fetch archived agents" });
}

// The mutations below use the *WithBodyError helpers rather than the plain
// ones: their failures are now rendered verbatim to the user, and the backend
// rejection reason (work_dir outside the jail, malformed MCP JSON) only lives
// in the response body — a bare "update user: 400" tells nobody what to fix.
export async function updateUser(id: string, data: UpdateUserData): Promise<User> {
  return request<User>(`/api/users/${encodeURIComponent(id)}`, jsonRequestInit("PUT", data), {
    label: "update user",
    errorBody: "message",
  });
}

export async function browseDirs(path?: string): Promise<BrowseDirResult> {
  const params = new URLSearchParams();
  if (path) params.set("path", path);
  const qs = params.toString();
  return request<BrowseDirResult>(`/api/browse-dirs${qs ? `?${qs}` : ""}`, undefined, {
    label: "browse dirs",
  });
}

export async function mkdirBrowseDir(path: string, name: string): Promise<BrowseDirEntry> {
  return request<BrowseDirEntry>(
    `/api/browse-dirs/mkdir`,
    jsonRequestInit("POST", { path, name }),
    { label: "mkdir", errorBody: "message" },
  );
}
