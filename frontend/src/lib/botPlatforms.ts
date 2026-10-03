import type { AgentBotCredentialField, AgentBotPlatform } from "@/composables/apiTypes";

// Everything that differs per IM platform, in one table: which credentials the
// connection test needs, what the platform calls its channel and user ids, and
// which help string describes its permissions. A new platform is one entry
// here rather than another branch in each consumer.
export type PlatformCredential = {
  field: AgentBotCredentialField;
  labelKey: string;
  /** Rendered as a password input, so it is never shoulder-readable. */
  secret: boolean;
  testId: string;
};

export type BotPlatformMeta = {
  // Translated, not hardcoded: "飞书" is the Chinese brand name and reads as
  // "Feishu (Lark)" in English.
  labelKey: string;
  credentials: PlatformCredential[];
  channelExample: string;
  otherChannelExample: string;
  userExample: string;
  permissionsHelpKey: string;
  // Names the "bot_token" permission credential on platforms that have no
  // input field for it, so the permission badge still reads sensibly.
  botTokenLabelKey?: string;
  // Whether the sample rules advertise the bot-relay keys. Presentation
  // only: the backend gates relays on Capabilities.SupportsBotRelay.
  showsBotRelayExample: boolean;
};

export const BOT_PLATFORMS: Record<AgentBotPlatform, BotPlatformMeta> = {
  slack: {
    labelKey: "settings.agentForm.platformSlack",
    credentials: [
      {
        field: "bot_token",
        labelKey: "settings.agentForm.slackBotToken",
        secret: true,
        testId: "bot-token-input",
      },
      {
        field: "bot_app_token",
        labelKey: "settings.agentForm.slackAppToken",
        secret: true,
        testId: "bot-app-token-input",
      },
    ],
    channelExample: "C0123456789",
    otherChannelExample: "C0987654321",
    userExample: "U0123456789",
    permissionsHelpKey: "settings.agentForm.requiredPermissionsSlackHelp",
    showsBotRelayExample: true,
  },
  feishu: {
    labelKey: "settings.agentForm.platformFeishu",
    credentials: [
      {
        field: "bot_app_id",
        labelKey: "settings.agentForm.feishuAppId",
        secret: false,
        testId: "bot-app-id-input",
      },
      {
        field: "bot_app_secret",
        labelKey: "settings.agentForm.feishuAppSecret",
        secret: true,
        testId: "bot-app-secret-input",
      },
    ],
    channelExample: "oc_1234567890abcdef",
    otherChannelExample: "oc_fedcba0987654321",
    userExample: "ou_1234567890abcdef",
    permissionsHelpKey: "settings.agentForm.requiredPermissionsFeishuHelp",
    showsBotRelayExample: false,
  },
  telegram: {
    // Telegram chat and user ids are bare numbers; supergroup ids are negative.
    labelKey: "settings.agentForm.platformTelegram",
    credentials: [
      {
        field: "bot_token",
        labelKey: "settings.agentForm.telegramBotToken",
        secret: true,
        testId: "bot-token-input",
      },
    ],
    channelExample: "-1001234567890",
    otherChannelExample: "-1009876543210",
    userExample: "123456789",
    permissionsHelpKey: "settings.agentForm.requiredPermissionsTelegramHelp",
    showsBotRelayExample: true,
  },
  wechat: {
    // No credential inputs: both halves of a WeChat pairing come from the QR
    // handshake, so a text field would only invite pasting a token that is
    // meaningless without the gateway issued alongside it.
    labelKey: "settings.agentForm.platformWeChat",
    credentials: [],
    // Weixin ids are opaque and suffixed by their kind.
    channelExample: "user_abcdef@im.wechat",
    otherChannelExample: "user_123456@im.wechat",
    userExample: "user_abcdef@im.wechat",
    permissionsHelpKey: "settings.agentForm.requiredPermissionsWeChatHelp",
    botTokenLabelKey: "settings.agentForm.wechatPairing",
    // The inbound stream carries no bot identities, so a relay has no target.
    showsBotRelayExample: false,
  },
};

export const BOT_PLATFORM_IDS = Object.keys(BOT_PLATFORMS) as AgentBotPlatform[];

// The i18n key for a platform's display name, or null for a platform this
// build does not know (a newer server can report one before the UI ships it).
export function botPlatformLabelKey(platform: string): string | null {
  return Object.hasOwn(BOT_PLATFORMS, platform)
    ? BOT_PLATFORMS[platform as AgentBotPlatform].labelKey
    : null;
}

const DEFAULT_CHANNELS = '[{"channel":"*","require_mention":true}]';
const WECHAT_DEFAULT_CHANNELS = '[{"channel":"dm","allowed_user_ids":["*"]}]';

export function defaultChannelsFor(platform: AgentBotPlatform): string {
  return platform === "wechat" ? WECHAT_DEFAULT_CHANNELS : DEFAULT_CHANNELS;
}
