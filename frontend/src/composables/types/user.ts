// Split out of apiTypes.ts (kept as a re-export barrel) so each domain's
// types live next to their concerns. Import via "@/composables/apiTypes".

// SandboxMode is the per-user agent isolation tier. Mirrors the backend
// store.SandboxMode* constants.
export type SandboxMode = "jailed" | "unrestricted";

export interface User {
  id: string;
  name: string;
  username: string;
  email: string;
  is_admin: boolean;
  disabled: boolean;
  // Referential owner pointer for agent rows (username === ""): the id of the
  // human login row that owns the agent. Humans carry their own id. Optional
  // in TS only so fixtures predating the field stay valid — the backend always
  // sends it.
  owner_id?: string;
  // Per-CLI-type binding map. Keys are CLI type strings ("claude",
  // "codex"); values are provider names referencing entries in
  // AdminPublicConfig.providers. Optional because the backend omits the
  // key when the user has no bindings at all (resolves to the implicit
  // "default" account at runtime).
  provider_bindings?: Record<string, string>;
  // Per-CLI-type set of accounts the user may use, default account first
  // within each type. provider_bindings[type] always equals
  // provider_accounts[type][0]. The conversation account picker offers this
  // set; absent when the user has no bindings.
  provider_accounts?: Record<string, string[]>;
  // Optional model id new conversations inherit when the create caller pins
  // neither provider nor model. Humans receive it from the admin user panel;
  // Agent / Bot subjects receive it from their own settings form.
  default_model?: string;
  // Optional reasoning effort inherited by every turn for this Agent.
  think_level?: "" | "low" | "medium" | "high" | "max";
  // When on, every IM bot under this Agent runs in case-file mode: the durable
  // state of a thread lives in a bounded case document and the CLI session
  // behind it is rotated once its context crosses a threshold.
  case_mode: boolean;
  // Agent filesystem isolation tier. "unrestricted" = full host reach
  // (admins / trusted); "jailed" = confined to work_dir once the server-side
  // bwrap sandbox is on.
  sandbox_mode: SandboxMode;
  work_dir: string;
  avatar: string;
  role_definition: string;
  env?: string;
  mcp_config: string;
  claude_md_content: string;
  manage_claude_md: boolean;
  bark_url: string;
  pushdeer_key: string;
  // Empty string, "bark", or "pushdeer". Only meaningful when both
  // bark_url and pushdeer_key are non-empty; otherwise the runtime falls
  // back to whichever single channel is configured.
  notification_channel: string;
  // Manual sidebar position among the owner's agents, set by drag-to-reorder.
  // 0 for humans / never-reordered agents.
  sort_order: number;
  // True when the agent has been archived (hidden from the sidebar) via the
  // right-click menu. Archived agents are restorable from settings.
  archived: boolean;
  // Platforms of the agent's attached first-class bots; assembled by the
  // backend user list so the sidebar can mark bot-connected agents.
  bot_platforms?: string[];
  created_at: string;
}

export interface AuthUser {
  id: string;
  username: string;
  name: string;
  is_admin: boolean;
  // Notification settings live on the human user, not per-agent. Empty
  // strings when the user hasn't configured the corresponding channel.
  // notification_channel disambiguates which channel is used when both
  // bark_url and pushdeer_key are populated (values: "", "bark", "pushdeer").
  bark_url: string;
  pushdeer_key: string;
  notification_channel: string;
  // The signed-in human's own work_dir. The agent-creation form uses
  // this to root its DirPicker — agents can only land inside their
  // owner's home, matching the backend's `requireWorkDirWithinCaller`.
  work_dir: string;
}

export interface AuthOptions {
  password_login_enabled: boolean;
  oidc: {
    enabled: boolean;
    button_label: string;
  };
}

export interface EnvironmentSettings {
  env: string;
}

export interface CreateUserData {
  name: string;
  work_dir: string;
  avatar?: string;
  role_definition?: string;
  mcp_config?: string;
  claude_md_content?: string;
  manage_claude_md?: boolean;
  default_model?: string;
  think_level?: string;
  case_mode?: boolean;
}

export interface UpdateUserData {
  name: string;
  work_dir: string;
  avatar: string;
  role_definition: string;
  mcp_config: string;
  claude_md_content: string;
  manage_claude_md: boolean;
  default_model?: string;
  think_level?: string;
  case_mode?: boolean;
}

export interface BrowseDirEntry {
  name: string;
  path: string;
}

export interface BrowseDirResult {
  current: string;
  parent: string;
  dirs: BrowseDirEntry[];
}
