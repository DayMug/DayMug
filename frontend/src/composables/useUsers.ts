import type { User, CreateUserData, UpdateUserData } from "./useApi";
import { archivedAgents, currentUser, users, usersLoaded } from "@/stores/userStore";
import {
  fetchUsers as apiFetchUsers,
  createUser as apiCreateUser,
  updateUser as apiUpdateUser,
  deleteUser as apiDeleteUser,
  duplicateUser as apiDuplicateUser,
  reorderUsers as apiReorderUsers,
  archiveUser as apiArchiveUser,
  unarchiveUser as apiUnarchiveUser,
  fetchArchivedAgents as apiFetchArchivedAgents,
} from "./useApi";
import { useChat } from "./useChat";
import { useConversations } from "./useConversations";
import { errorMessage } from "@/lib/errorMessage";

// MutationResult is how every roster operation reports the outcome of its
// round-trip. These used to swallow rejections, which made a save the backend
// refused (work_dir outside the jail, malformed MCP JSON, a dropped
// connection) look exactly like a successful one to the caller — and to the
// user, who got a navigation back to the settings hub either way. Returning
// instead of throwing keeps the fire-and-forget call sites (App.vue,
// useAppShell) free of unhandled rejections.
export interface MutationResult {
  ok: boolean;
  /** Failure reason from the API layer. Empty when `ok`. */
  error: string;
}

function mutationOk(): MutationResult {
  return { ok: true, error: "" };
}

function mutationFailed(e: unknown): MutationResult {
  return { ok: false, error: errorMessage(e) };
}

function visibleUsers(list: User[]): User[] {
  return list.filter((u) => !u.archived);
}

async function loadUsers(): Promise<MutationResult> {
  try {
    users.value = visibleUsers(await apiFetchUsers());
    usersLoaded.value = true;
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

// applyUsers seeds the users list from a payload the caller already
// fetched (typically the /api/app-state aggregate). Bypasses the dedicated
// /api/users round-trip on cold start.
function applyUsers(list: User[]) {
  users.value = visibleUsers(list);
  usersLoaded.value = true;
}

async function selectUser(userId: string, preferredSessionId?: string) {
  const user = users.value.find((u) => u.id === userId);
  if (!user) return;
  currentUser.value = user;
  localStorage.setItem("daymug-user-id", userId);
  const { conversations, activeAgentId, loadConversations } = useConversations();
  // Short-circuit when the cold-start /api/app-state payload already
  // primed this agent's conversation list (applyConversations sets
  // activeAgentId). Saves one round-trip on the page-load critical path —
  // any later selectUser call against a *different* agent still falls
  // through to the network fetch since activeAgentId won't match.
  if (activeAgentId.value === userId && conversations.value.length > 0) {
    const { switchToConversation } = useChat();
    const target =
      preferredSessionId && conversations.value.some((c) => c.id === preferredSessionId)
        ? preferredSessionId
        : conversations.value[0].id;
    await switchToConversation(target);
    return;
  }
  await loadConversations(userId, preferredSessionId);
}

async function handleCreateUser(data: CreateUserData): Promise<MutationResult> {
  try {
    const user = await apiCreateUser(data);
    users.value.push(user);
    await selectUser(user.id);
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

async function handleUpdateUser(id: string, data: UpdateUserData): Promise<MutationResult> {
  try {
    const updated = await apiUpdateUser(id, data);
    const idx = users.value.findIndex((u) => u.id === updated.id);
    if (idx >= 0) users.value[idx] = updated;
    if (currentUser.value?.id === updated.id) currentUser.value = updated;
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

async function handleDuplicateUser(userId: string): Promise<User | null> {
  try {
    const dup = await apiDuplicateUser(userId);
    users.value.push(dup);
    return dup;
  } catch {
    return null;
  }
}

async function handleDeleteUser(userId: string): Promise<MutationResult> {
  try {
    await apiDeleteUser(userId);
    users.value = users.value.filter((u) => u.id !== userId);
    if (currentUser.value?.id === userId) {
      if (users.value.length > 0) {
        await selectUser(users.value[0].id);
      } else {
        currentUser.value = null;
        const { disconnectWs, currentConversationId } = useChat();
        currentConversationId.value = "";
        disconnectWs();
      }
    }
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

// handleReorderUsers persists a new manual order of the caller's rows from a
// sidebar drag. `ids` is the full ordered id list — the human owner and its
// agents, freely interleaved. The local list is reordered optimistically for an
// instant response, then replaced by the authoritative server list — or rolled
// back if the request fails.
async function handleReorderUsers(ids: string[]) {
  const prev = users.value;
  const byId = new Map(prev.map((u) => [u.id, u] as const));
  const ordered: User[] = [];
  for (const id of ids) {
    const u = byId.get(id);
    if (u) {
      ordered.push(u);
      byId.delete(id);
    }
  }
  // Any row missing from `ids` keeps its original relative position at the end.
  for (const u of prev) {
    if (byId.has(u.id)) ordered.push(u);
  }
  users.value = ordered;
  try {
    users.value = visibleUsers(await apiReorderUsers(ids));
  } catch {
    users.value = prev;
  }
}

// handleArchiveUser hides an agent from the sidebar. Mirrors handleDeleteUser's
// active-agent handling so archiving the current agent doesn't leave the chat
// surface pointing at a now-hidden row.
async function handleArchiveUser(userId: string): Promise<MutationResult> {
  try {
    await apiArchiveUser(userId);
    users.value = users.value.filter((u) => u.id !== userId);
    if (currentUser.value?.id === userId) {
      if (users.value.length > 0) {
        await selectUser(users.value[0].id);
      } else {
        currentUser.value = null;
        const { disconnectWs, currentConversationId } = useChat();
        currentConversationId.value = "";
        disconnectWs();
      }
    }
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

async function loadArchivedAgents(): Promise<MutationResult> {
  try {
    archivedAgents.value = await apiFetchArchivedAgents();
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

// handleRestoreUser brings an archived agent back into the sidebar list.
async function handleRestoreUser(userId: string): Promise<MutationResult> {
  try {
    const restored = await apiUnarchiveUser(userId);
    archivedAgents.value = archivedAgents.value.filter((u) => u.id !== userId);
    if (!users.value.some((u) => u.id === restored.id)) {
      users.value.push(restored);
    }
    return mutationOk();
  } catch (e) {
    return mutationFailed(e);
  }
}

export function useUsers() {
  return {
    users,
    usersLoaded,
    currentUser,
    archivedAgents,
    loadUsers,
    loadArchivedAgents,
    applyUsers,
    selectUser,
    handleCreateUser,
    handleUpdateUser,
    handleDuplicateUser,
    handleDeleteUser,
    handleReorderUsers,
    handleArchiveUser,
    handleRestoreUser,
  };
}
