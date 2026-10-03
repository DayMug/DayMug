import { apiBase } from "./apiBase";
import { jsonRequestInit, request } from "./apiClient";
import type { ProviderType } from "@/lib/providerTypes";
import type { ModelSpec } from "@/lib/providerPresets";
import {
  type AdminBatchDefaultModelData,
  type AdminBatchProviderBindingData,
  type AdminBatchSandboxModeData,
  type AdminCreateUserData,
  type AdminDBOptimizeResult,
  type AdminPublicConfig,
  type AdminUpdateUserData,
  type AdminUpgradeApplyResult,
  type AdminUpgradeBusy,
  type AdminUpgradeCheck,
  type AdminUpgradeRestartResult,
  type AdminUpgradeStatus,
  type User,
} from "./apiTypes";

// adminSessionBundleUrl returns the download URL for a conversation's
// diagnostic bundle. Returned as a URL rather than fetched because the
// response is a streamed zip attachment — session auth rides on the cookie,
// so handing it to the browser as a navigation is enough.
export function adminSessionBundleUrl(conversationId: string): string {
  return `${apiBase()}/api/admin/conversations/${encodeURIComponent(conversationId)}/session-bundle`;
}

export async function adminListUsers(): Promise<User[]> {
  return request(`/api/admin/users`, undefined, { label: "admin list users" });
}

export async function adminCreateUser(data: AdminCreateUserData): Promise<User> {
  return request(`/api/admin/users`, jsonRequestInit("POST", data), {
    label: "admin create user",
    errorBody: "message",
  });
}

export async function adminUpdateUser(id: string, data: AdminUpdateUserData): Promise<User> {
  return request(`/api/admin/users/${encodeURIComponent(id)}`, jsonRequestInit("PUT", data), {
    label: "admin update user",
    errorBody: "message",
  });
}

// adminBatchSetDefaultModel sets (or clears, when model is "") the per-user
// default model for every id in user_ids in one request.
export async function adminBatchSetDefaultModel(
  data: AdminBatchDefaultModelData,
): Promise<{ updated: number; model: string }> {
  return request(`/api/admin/users/default-model`, jsonRequestInit("POST", data), {
    label: "admin batch default model",
    errorBody: "message",
  });
}

// adminBatchSetSandboxMode sets the agent isolation tier (jailed /
// unrestricted) for every id in user_ids in one request.
export async function adminBatchSetSandboxMode(
  data: AdminBatchSandboxModeData,
): Promise<{ updated: number; sandbox_mode: string }> {
  return request(`/api/admin/users/sandbox-mode`, jsonRequestInit("POST", data), {
    label: "admin batch sandbox mode",
    errorBody: "message",
  });
}

export async function adminBatchSetProviderBinding(
  data: AdminBatchProviderBindingData,
): Promise<{ updated: number; provider_type: string; provider_names: string[] }> {
  return request(`/api/admin/users/provider-binding`, jsonRequestInit("POST", data), {
    label: "admin batch provider binding",
    errorBody: "message",
  });
}

export async function adminDeleteUser(id: string): Promise<void> {
  await request<void>(
    `/api/admin/users/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    { label: "admin delete user", errorBody: "message", expect: "none" },
  );
}

export async function adminSetPassword(id: string, password: string): Promise<void> {
  await request<void>(
    `/api/admin/users/${encodeURIComponent(id)}/password`,
    jsonRequestInit("POST", { password }),
    { label: "admin set password", errorBody: "message", expect: "none" },
  );
}

export async function adminSetDisabled(id: string, disabled: boolean): Promise<void> {
  const action = disabled ? "disable" : "enable";
  await request<void>(
    `/api/admin/users/${encodeURIComponent(id)}/${action}`,
    { method: "POST" },
    { label: `admin ${action}`, errorBody: "message", expect: "none" },
  );
}

export async function adminFetchPublicConfig(): Promise<AdminPublicConfig> {
  return request(`/api/admin/config/public`, undefined, { label: "admin public config" });
}

export async function adminUpgradeCheck(): Promise<AdminUpgradeCheck> {
  return request(`/api/admin/upgrade/check`, undefined, {
    label: "upgrade check",
    errorBody: "message",
  });
}

export async function adminUpgradeApply(force = false): Promise<AdminUpgradeApplyResult> {
  const path = force ? `/api/admin/upgrade/apply?force=1` : `/api/admin/upgrade/apply`;
  return request(path, { method: "POST" }, { label: "upgrade apply", errorBody: "message" });
}

export async function adminUpgradeRestart(): Promise<AdminUpgradeRestartResult> {
  return request(
    `/api/admin/upgrade/restart`,
    { method: "POST" },
    { label: "upgrade restart", errorBody: "message" },
  );
}

// adminUpgradeBusy polls the live in-flight job count so the admin panel
// can re-enable the Update button the moment users finish their prompts.
// Cheap (read of an atomic counter); safe to call on an interval.
export async function adminUpgradeBusy(): Promise<AdminUpgradeBusy> {
  return request(`/api/admin/upgrade/busy`, undefined, {
    label: "upgrade busy",
    errorBody: "message",
  });
}

export async function adminUpgradeStatus(): Promise<AdminUpgradeStatus> {
  return request(`/api/admin/upgrade/status`, undefined, {
    label: "upgrade status",
    errorBody: "message",
  });
}

export async function adminOptimizeDatabase(): Promise<AdminDBOptimizeResult> {
  return request(
    `/api/admin/db/optimize`,
    { method: "POST" },
    { label: "optimize db", errorBody: "message" },
  );
}

export async function adminDatabaseSize(): Promise<number> {
  const body = await request<{ bytes: number }>(`/api/admin/db/size`, undefined, {
    label: "db size",
    errorBody: "message",
  });
  return body.bytes;
}

// AdminPauseState is the operator's hold on new work. resumed_conversations is
// only meaningful on the response to a resume — it reports how many queued
// conversations were handed back to the dispatcher.
export interface AdminPauseState {
  paused: boolean;
  resumed_conversations?: number;
}

export async function adminFetchPause(): Promise<AdminPauseState> {
  return request(`/api/admin/pause`, undefined, { label: "pause state", errorBody: "message" });
}

export async function adminSetPause(paused: boolean): Promise<AdminPauseState> {
  return request(`/api/admin/pause`, jsonRequestInit("PUT", { paused }), {
    label: "set pause",
    errorBody: "message",
  });
}

// AdminPricingRate covers every provider whose pricing the admin can
// edit. Providers populate the subset of fields that maps to their
// billing model (see backend/internal/agent/pricing/pricing.go):
//   - the Codex family (codex, openai-compatible) uses input /
//     cached_input / output; the 5m/1h fields stay 0.
//   - claude-compatible uses every field — cached_input is cache_read, the
//     5m/1h fields are Anthropic-style ephemeral cache-write rates.
export interface AdminPricingRate {
  input: number;
  cached_input?: number;
  output: number;
  cache_creation_5m?: number;
  cache_creation_1h?: number;
}

export interface AdminPricing {
  provider: string;
  models: string[];
  rates: Record<string, AdminPricingRate>;
  defaults: Record<string, AdminPricingRate>;
}

// adminFetchPricing returns the active rates for a provider merged on top
// of the compile-time defaults. The defaults map is returned alongside so
// the UI can render a "reset to default" hint per row.
export async function adminFetchPricing(provider: string): Promise<AdminPricing> {
  return request(`/api/admin/pricing/${encodeURIComponent(provider)}`, undefined, {
    label: `${provider} pricing`,
    errorBody: "message",
  });
}

// adminSavePricing replaces the override for a provider. Pass an empty
// rates object to clear it (server reverts to compile-time defaults).
export async function adminSavePricing(
  provider: string,
  rates: Record<string, AdminPricingRate>,
): Promise<AdminPricing> {
  return request(
    `/api/admin/pricing/${encodeURIComponent(provider)}`,
    jsonRequestInit("PUT", { rates }),
    { label: `save ${provider} pricing`, errorBody: "message" },
  );
}

// AdminAccountModels is one editable row of the model registry: which models
// an account offers, in picker order, and which one it summarises with. There
// is no built-in list — an account nobody configured offers no models.
export interface AdminAccountModels {
  account: string;
  provider: string;
  models: string[];
  summary_model: string;
  // Per-model limits keyed by model id; always an object on the wire.
  specs: Record<string, ModelSpec>;
  overridden: boolean;
}

export interface AdminModels {
  accounts: AdminAccountModels[];
}

// The PUT body carries only the editable fields, keyed by account name.
export type AdminAccountModelsEdit = Pick<AdminAccountModels, "models" | "summary_model" | "specs">;

export async function adminFetchModels(): Promise<AdminModels> {
  return request(`/api/admin/models`, undefined, { label: "models", errorBody: "message" });
}

// adminSaveModels replaces the whole registry. An account left out of
// `accounts` — or submitted with an empty list — offers no models. An account
// with models must name a summary model; the server rejects it otherwise.
export async function adminSaveModels(
  accounts: Record<string, AdminAccountModelsEdit>,
): Promise<AdminModels> {
  return request(`/api/admin/models`, jsonRequestInit("PUT", { accounts }), {
    label: "save models",
    errorBody: "message",
  });
}

// AgentTransport is how DayMug drives a provider type: `cli` spawns one CLI
// process per turn (messages sent mid-turn wait for it to finish), while
// `agent-sdk` / `app-server` keep a structured session that accepts input
// mid-turn.
export type AgentTransport = "cli" | "agent-sdk" | "app-server";

export interface AdminTransport {
  provider: string;
  transport: AgentTransport;
  // Supported transports for the type, default first.
  options: AgentTransport[];
}

export interface AdminTransports {
  transports: AdminTransport[];
}

export async function adminFetchTransports(): Promise<AdminTransports> {
  return request(`/api/admin/transports`, undefined, { label: "transports", errorBody: "message" });
}

// Types left out of `transports` keep their current transport.
export async function adminSaveTransports(
  transports: Record<string, AgentTransport>,
): Promise<AdminTransports> {
  return request(`/api/admin/transports`, jsonRequestInit("PUT", { transports }), {
    label: "save transports",
    errorBody: "message",
  });
}

export interface AdminProvider {
  name: string;
  type: ProviderType;
  max_concurrent: number;
  config_dir: string;
  env: Record<string, string>;
}

export interface AdminProviders {
  providers: AdminProvider[];
}

// AdminAccountCheck mirrors POST /api/admin/providers/:name/check: the
// account CLI's own login status, then one tiny read-only turn on the
// account's default model through the transport chat actually uses.
export interface AdminAccountAuthStatus {
  // False for the compatible types, which authenticate with an env API key.
  checked: boolean;
  logged_in: boolean;
  method?: string;
  detail?: string;
  error?: string;
}

export interface AdminAccountRunResult {
  // False when the run was skipped because the account is logged out.
  attempted: boolean;
  ok: boolean;
  model?: string;
  latency_ms?: number;
  reply?: string;
  error?: string;
}

export interface AdminAccountCheck {
  account: string;
  provider: string;
  transport: string;
  ok: boolean;
  auth: AdminAccountAuthStatus;
  run: AdminAccountRunResult;
  checked_at: string;
}

export async function adminCheckAccount(account: string): Promise<AdminAccountCheck> {
  return request(
    `/api/admin/providers/${encodeURIComponent(account)}/check`,
    { method: "POST" },
    { label: "check account", errorBody: "message" },
  );
}

// AdminSummaryCheck mirrors POST /api/admin/providers/:name/summary-check:
// one real auto-title run on the given (possibly unsaved) summary model.
export interface AdminSummaryCheck {
  account: string;
  model: string;
  ok: boolean;
  title?: string;
  latency_ms?: number;
  error?: string;
}

export async function adminCheckSummaryModel(
  account: string,
  model: string,
): Promise<AdminSummaryCheck> {
  return request(
    `/api/admin/providers/${encodeURIComponent(account)}/summary-check`,
    jsonRequestInit("POST", { model }),
    { label: "check summary model", errorBody: "message" },
  );
}

export async function adminFetchProviders(): Promise<AdminProviders> {
  return request(`/api/admin/providers`, undefined, { label: "providers", errorBody: "message" });
}

export async function adminSaveProviders(providers: AdminProvider[]): Promise<AdminProviders> {
  return request(`/api/admin/providers`, jsonRequestInit("PUT", { providers }), {
    label: "save providers",
    errorBody: "message",
  });
}
