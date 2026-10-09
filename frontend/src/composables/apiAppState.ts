import { request } from "./apiClient";
import type { AuthUser, Conversation, User } from "./apiTypes";
import type { ServerInfo } from "./apiServerInfo";

// AppState is the cold-start aggregate the SPA fires on app mount: bundles
// the user identity, visible users, server-info, and (optionally) the
// active agent's conversations into one round-trip so the chat surface
// reaches first paint without the previous serial waterfall through
// /auth/me → /users → /server-info → /conversations.
//
// `conversations` is null when the request didn't carry a `user_id` query
// param, or when the caller can't access that user — the SPA falls back to
// its existing per-user fetch in either case.
export interface AppState {
  auth_user: AuthUser;
  users: User[];
  server_info: ServerInfo;
  preloaded_user_id: string;
  // First page only — this endpoint fires on every cold load, so an account
  // with thousands of conversations must not ship all of them here. The
  // sidebar pages the rest in through GET /api/conversations on demand.
  conversations: Conversation[] | null;
  conversations_has_more: boolean;
}

// fetchAppState calls GET /api/app-state, optionally hinting which user's
// conversations to preload. The hint is the URL's `:userId` on hard-reload
// (or the saved-user-id from localStorage when there's no route), so the
// most common cold-start case is satisfied in a single round-trip.
export async function fetchAppState(userIdHint?: string): Promise<AppState> {
  const qs = userIdHint ? `?user_id=${encodeURIComponent(userIdHint)}` : "";
  return request<AppState>(`/api/app-state${qs}`, undefined, { label: "fetch app-state" });
}
