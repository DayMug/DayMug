// Split out of apiTypes.ts (kept as a re-export barrel) so each domain's
// types live next to their concerns. Import via "@/composables/apiTypes".

// Mirrors the Go platform registry (imbot.SupportedPlatforms). Naming the
// union once keeps AgentBot, the input/test shapes and the requirements record
// in step when a platform is added.
export type AgentBotPlatform = "slack" | "feishu" | "telegram" | "wechat";

// WeChat credentials are produced by a QR handshake rather than typed, so the
// pairing flow has its own two shapes instead of reusing the credential form.
export interface WeChatPairingStart {
  challenge: string;
  /** base64 PNG rendered server-side, ready for an <img> data URL. */
  qr_image?: string;
  /** The URL the QR encodes, offered as a fallback when the image is absent. */
  qr_content?: string;
}

export interface WeChatPairingStatus {
  // "scanned" means the phone read the code but has not confirmed yet;
  // "blocked" means WeChat wants a verification code DayMug cannot supply.
  status: "pending" | "scanned" | "confirmed" | "expired" | "blocked";
  bot_token?: string;
  base_url?: string;
}

export interface AgentBotStatus {
  agent_id: string;
  bot_id: string;
  platform: AgentBotPlatform;
  running: boolean;
  error?: string;
}

// Credentials are write-only: the server never sends a bot token or app secret
// back, only whether each one is stored. Reads and writes therefore have
// different shapes — AgentBot for responses, AgentBotInput for submissions.
export interface AgentBot {
  id: string;
  agent_id: string;
  name: string;
  platform: AgentBotPlatform;
  enabled: boolean;
  /** Empty inherits the Agent/server configured default for each new thread. */
  model: string;
  /** Go duration; empty uses the 12h default and 0 keeps a thread indefinitely sticky. */
  max_conversation_duration: string;
  bot_token_configured: boolean;
  bot_app_token_configured: boolean;
  bot_app_id_configured: boolean;
  bot_app_secret_configured: boolean;
  /** True when every credential this bot's platform needs is already stored. */
  credentials_configured: boolean;
  channels: string;
  unconfigured_reply: string;
  unauthorized_reply: string;
  created_at?: string;
}

export type AgentBotCredentialField =
  | "bot_token"
  | "bot_app_token"
  | "bot_app_id"
  | "bot_app_secret";

// A blank credential means "keep the stored one"; only a non-empty value
// replaces it.
export type AgentBotInput = Pick<
  AgentBot,
  | "name"
  | "platform"
  | "enabled"
  | "model"
  | "max_conversation_duration"
  | "channels"
  | "unconfigured_reply"
  | "unauthorized_reply"
> &
  Record<AgentBotCredentialField, string>;

// bot_id lets the server back-fill blank credentials from the saved bot, so a
// connection test works without re-typing secrets the UI cannot read.
export type AgentBotTestInput = AgentBotInput & { bot_id?: string };

export interface BotPermissionRequirement {
  key: string;
  credential: "bot_token" | "app_token" | "app";
  required: boolean;
}

export interface BotPermissionCheck extends BotPermissionRequirement {
  granted: boolean;
}

export type BotPermissionRequirements = Record<AgentBot["platform"], BotPermissionRequirement[]>;

export interface BotConnectionTestResult {
  platform: AgentBot["platform"];
  connected: boolean;
  identity?: string;
  all_required_permissions_granted: boolean;
  permissions: BotPermissionCheck[];
  error?: string;
}
