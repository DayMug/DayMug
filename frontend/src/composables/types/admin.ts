// Split out of apiTypes.ts (kept as a re-export barrel) so each domain's
// types live next to their concerns. Import via "@/composables/apiTypes".

import type { SandboxMode } from "./user";

// AdminPublicConfigProvider mirrors one row in the `providers` response key.
// Carries the CLI type so the user-edit form can render one picker per CLI
// type.
export interface AdminPublicConfigProvider {
  name: string;
  type: string;
  max_concurrent: number;
}

export interface AdminPublicConfig {
  default_home_root: string;
  // default_cli_type is the CLI type of the first entry in `providers` —
  // the implicit default for new conversations. Empty when no providers
  // are configured (the server then refuses new conversations).
  default_cli_type?: string;
  // providers is every configured provider. Each entry carries the CLI type
  // so the admin UI can render per-type pickers without a separate filter
  // pass.
  providers: AdminPublicConfigProvider[];
  sandbox: { enabled: boolean; type: string };
  upgrade: { enabled: boolean };
  current_version: string;
  backend: string;
}

export interface AdminDBOptimizeResult {
  before_bytes: number;
  after_bytes: number;
  bytes_reclaimed: number;
  expired_sessions_deleted: number;
  purged_conversations: number;
  purged_users: number;
  purged_agents: number;
}

export interface AdminUpgradeReleaseNote {
  hash: string;
  subject: string;
}

export interface AdminUpgradeRelease {
  version: string;
  released_at: string;
  // chore:/docs:/ci: commits are filtered out at release time. When this
  // array is empty the admin UI renders a "general improvements" fallback
  // instead of leaving the row blank.
  notes: AdminUpgradeReleaseNote[];
}

export interface AdminUpgradeCheck {
  current_version: string;
  latest_version: string;
  released_at: string;
  has_update: boolean;
  // False when the release carries no binary for this host's OS/arch —
  // the normal shape of a Linux-only release seen from a Mac, since macOS
  // builds are opt-in per release. has_update is forced false alongside
  // it (nothing to install), so this is what distinguishes "you're
  // current" from "this release skipped your platform".
  platform_supported: boolean;
  download_url: string;
  // Live count of running Claude/Codex jobs at the moment the manifest
  // was fetched. The admin UI uses this to disable the Update button so
  // operators don't kill an in-flight reply by restarting the service.
  // Apply re-checks this value server-side under the upgrade mutex; the
  // field here is advisory and can race with newly-started jobs.
  in_flight_jobs: number;
  jobs: AdminUpgradeJob[];
  // Last ~5 tagged releases (newest first) with filtered commit subjects.
  // Always present on the wire (empty array on legacy manifests).
  recent_releases: AdminUpgradeRelease[];
}

export interface AdminUpgradeJob {
  id: number;
  user_id?: string;
  username?: string;
  provider_type?: string;
  account_name?: string;
  // "queued" while waiting for an account concurrency slot, "running" once the
  // slot is granted, "waiting" while parked on a question; "background" for
  // resident work that outlived its turn.
  status: "queued" | "running" | "waiting" | "background";
  conversation_id?: string;
  started_at: string;
}

export interface AdminUpgradeRestartResult {
  status: "restarting" | "waiting_for_conversations";
  in_flight_jobs: number;
  jobs?: AdminUpgradeJob[];
  drain_reason?: string;
}

export interface AdminUpgradeBusy {
  in_flight_jobs: number;
  jobs: AdminUpgradeJob[];
}

export type UpgradePhase =
  | "idle"
  | "waiting_for_conversations"
  | "validating"
  | "ok"
  | "rolled_back"
  | "failed";

export interface AdminUpgradeStatus {
  phase: UpgradePhase;
  old_version: string;
  new_version: string;
  started_at: string;
  finished_at: string;
  error: string;
  current_version: string;
  backup_available: boolean;
  pending_operation?: "upgrade" | "restart" | "";
}

export interface AdminUpgradeApplyResult {
  status: "validating" | "up_to_date" | "waiting_for_conversations";
  old_version?: string;
  new_version?: string;
  version?: string;
  watchdog_active?: boolean;
}

export interface AdminCreateUserData {
  username: string;
  password: string;
  name?: string;
  email?: string;
  is_admin?: boolean;
  work_dir?: string;
  // Initial agent isolation tier. Omit to default to jailed.
  sandbox_mode?: SandboxMode;
  // provider_bindings maps CLI type → provider name. Each entry is
  // validated against the live config; empty strings clear the slot.
  provider_bindings?: Record<string, string>;
  // provider_accounts grants a SET of accounts per CLI type (default first).
  // Authoritative for any type it names, superseding provider_bindings.
  provider_accounts?: Record<string, string[]>;
}

export interface AdminUpdateUserData {
  name?: string;
  email?: string;
  work_dir?: string;
  // Per-type binding patches. Missing keys mean "don't touch"; empty
  // values clear the slot; non-empty values upsert.
  provider_bindings?: Record<string, string>;
  // Full account SET per CLI type (default first). Authoritative for any type
  // it names, superseding provider_bindings for that type.
  provider_accounts?: Record<string, string[]>;
  is_admin?: boolean;
  // Per-user default model patch. Omit to leave untouched, "" to clear, a
  // model id to set. A non-empty id must resolve to a configured provider.
  default_model?: string;
  // Agent isolation tier patch. Omit to leave untouched.
  sandbox_mode?: SandboxMode;
}

// AdminBatchDefaultModelData is the POST /api/admin/users/default-model body:
// set (or clear, when model is "") the per-user default model for every id in
// one call.
export interface AdminBatchDefaultModelData {
  user_ids: string[];
  model: string;
}

// AdminBatchSandboxModeData is the POST /api/admin/users/sandbox-mode body:
// set the agent isolation tier for every id in one call.
export interface AdminBatchSandboxModeData {
  user_ids: string[];
  sandbox_mode: SandboxMode;
}

export interface AdminBatchProviderBindingData {
  user_ids: string[];
  provider_type: string;
  /** Granted accounts for the type, default account first. Empty clears it. */
  provider_names: string[];
}

export type UsageGroupBy = "day" | "user" | "agent" | "model" | "conversation";
export type UsageRankBy = "user" | "agent" | "model" | "conversation" | "cron";
export type UsageSortBy =
  | "cost"
  | "total_tokens"
  | "cache_ratio"
  | "user_instructions"
  | "model_requests"
  | "tool_calls"
  | "last_activity";

export interface UsageInsightsQuery {
  start?: string;
  end?: string;
  user_id?: string;
  agent_id?: string;
  provider?: string;
  model?: string;
  conversation_id?: string;
  source_type?: "manual" | "cron";
  group_by?: UsageGroupBy;
  rank_by?: UsageRankBy;
  sort_by?: UsageSortBy;
  sort_order?: "asc" | "desc";
  page?: number;
  page_size?: number;
}

export interface UsageMetricComparison {
  total_tokens: number | null;
  cost_usd: number | null;
  user_instructions: number | null;
  active_conversations: number | null;
  model_requests: number | null;
  tool_calls: number | null;
  cache_read_ratio: number | null;
}

export interface UsageSummary {
  total_tokens: number;
  cost_usd: number;
  user_instructions: number;
  active_conversations: number;
  model_requests: number;
  tool_calls: number;
  cache_read_ratio: number;
  historical_estimate: boolean;
  comparison: UsageMetricComparison;
}

export interface UsageCompositionPoint {
  key: string;
  label: string;
  input_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
  output_tokens: number;
  reasoning_output_tokens: number;
  cost_usd: number;
}

export interface UsageAnomaly {
  code:
    | "high_cache_read"
    | "high_model_requests"
    | "high_tool_calls"
    | "context_near_compact"
    | "high_cost"
    | "historical_estimate";
  value: number;
  threshold: number;
}

export interface UsageRankingRow {
  key: string;
  owner_id: string;
  owner_name: string;
  agent_id: string;
  agent_name: string;
  conversation_id: string;
  conversation_title: string;
  cron_job_id: string;
  cron_job_name: string;
  provider: string;
  model: string;
  source_type: string;
  user_instructions: number;
  model_requests: number;
  tool_calls: number;
  input_tokens: number;
  cache_read_input_tokens: number;
  cache_creation_input_tokens: number;
  output_tokens: number;
  reasoning_output_tokens: number;
  cache_read_ratio: number;
  cost_usd: number;
  context_usage_ratio: number;
  last_activity: string;
  historical_estimate: boolean;
  request_count_scope: string;
  anomalies: UsageAnomaly[];
}

export interface UsageFacet {
  id: string;
  label: string;
}

export interface UsageFacets {
  users: UsageFacet[];
  agents: UsageFacet[];
  providers: UsageFacet[];
  models: UsageFacet[];
  conversations: UsageFacet[];
  cron_jobs: UsageFacet[];
}

export interface UsageThresholds {
  cache_read_ratio: number;
  model_requests: number;
  tool_calls: number;
  context_warning_ratio: number;
  conversation_cost_usd: number;
}

export interface UsageInsightsResponse {
  summary: UsageSummary;
  composition: UsageCompositionPoint[];
  rankings: UsageRankingRow[];
  facets: UsageFacets;
  page: number;
  page_size: number;
  total_rows: number;
  timezone: string;
  group_by: UsageGroupBy;
  rank_by: UsageRankBy;
  thresholds: UsageThresholds;
}
