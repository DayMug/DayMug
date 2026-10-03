import { jsonRequestInit, request } from "./apiClient";
import type { AuthUser } from "./apiTypes";

// SetupMode mirrors service.FirstAdminSetupMode: which variant of the setup
// form fits the sign-in methods the server config enables.
//   password    — email + password account
//   sso_email   — password login is off; record only the email, which SSO
//                 login later matches
//   sso         — SSO provisions the first admin; no form
//   unavailable — no sign-in method is enabled; show the config fix
export type SetupMode = "password" | "sso_email" | "sso" | "unavailable";

// BootstrapStatus drives the first-run gate: setup_required sends the visitor
// to /setup/admin before /login. It is false once any human user exists, and
// also on a fresh install whose SSO provisions the first admin by itself.
export interface BootstrapStatus {
  has_users: boolean;
  setup_required: boolean;
  setup_mode?: SetupMode;
  min_password_length?: number;
}

export async function fetchBootstrapStatus(): Promise<BootstrapStatus> {
  return request<BootstrapStatus>(`/api/bootstrap/status`, undefined, {
    label: "bootstrap status",
  });
}

export interface BootstrapAdminInput {
  username: string;
  email: string;
  // Omitted in sso_email mode, where the account signs in through SSO only.
  password?: string;
  name?: string;
}

// createBootstrapAdmin provisions the very first admin and opens a session
// in the same request. On success the SPA can hard-reload straight into the
// chat surface — no separate /login round-trip needed.
export async function createBootstrapAdmin(input: BootstrapAdminInput): Promise<AuthUser> {
  return request<AuthUser>(`/api/bootstrap/admin`, jsonRequestInit("POST", input), {
    label: "bootstrap admin",
    errorBody: "message",
  });
}
