import { apiFetch, jsonRequestInit, request } from "./apiClient";
import type { AuthOptions, AuthUser, EnvironmentSettings } from "./apiTypes";

export async function fetchMe(): Promise<AuthUser> {
  return request<AuthUser>(`/api/auth/me`, undefined, { label: "fetch me" });
}

export async function login(username: string, password: string): Promise<AuthUser> {
  // apiFetch lets the login endpoint's own 401 through; the thrown ApiError
  // carries the status so the page can tell bad credentials from other
  // failures.
  return request<AuthUser>(`/api/auth/login`, jsonRequestInit("POST", { username, password }), {
    label: "login",
    errorBody: "message",
  });
}

export async function logout(): Promise<void> {
  await apiFetch(`/api/auth/logout`, { method: "POST" });
}

export async function changeMyPassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  await request<void>(
    `/api/auth/password`,
    jsonRequestInit("POST", { current_password: currentPassword, new_password: newPassword }),
    { label: "change password", errorBody: "message", expect: "none" },
  );
}

export async function fetchMyEnvironment(): Promise<EnvironmentSettings> {
  return request<EnvironmentSettings>(`/api/auth/environment`, undefined, {
    label: "fetch environment",
  });
}

export async function updateMyEnvironment(env: string): Promise<EnvironmentSettings> {
  return request<EnvironmentSettings>(`/api/auth/environment`, jsonRequestInit("PUT", { env }), {
    label: "update environment",
  });
}

// fetchAuthOptions powers the login page: it tells us whether to render the
// password form, whether to show the SSO button, and what label to put on
// it. Public endpoint, no auth required.
export async function fetchAuthOptions(): Promise<AuthOptions> {
  return request<AuthOptions>(`/api/auth/options`, undefined, { label: "fetch auth options" });
}

// NotificationSettings is the JSON shape this PUT accepts. notification_channel
// must be "", "bark", or "pushdeer" — the backend rejects anything else with
// a 400 so a stale enum value can't silently disable notifications.
export interface NotificationSettings {
  bark_url: string;
  pushdeer_key: string;
  notification_channel: "" | "bark" | "pushdeer";
}

// updateMyNotifications writes the signed-in user's notification settings.
// Notification config is per-human-owner — agent rows don't carry their own
// — so this endpoint always operates on the authed user, not on a target id.
export async function updateMyNotifications(settings: NotificationSettings): Promise<void> {
  await request<void>(`/api/auth/notifications`, jsonRequestInit("PUT", settings), {
    label: "update notifications",
    errorBody: "message",
    expect: "none",
  });
}
