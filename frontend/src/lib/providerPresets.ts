import type { AgentFramework } from "./providerTypes";

// Admin-declared limits of one model id; mirrors backend service.ModelSpec.
// Zero/absent context_window trusts whatever window the agent reports.
export interface ModelSpec {
  context_window?: number;
  no_image_input?: boolean;
}

export interface PresetModel {
  id: string;
  spec?: ModelSpec;
}

// How one vendor is reached through one framework: the endpoint the agent
// talks to and the starting model list. Only what the vendor documents is
// filled in — every value stays editable, and model catalogs drift.
export interface PresetEndpoint {
  baseUrl: string;
  models: PresetModel[];
  summaryModel: string;
  // Extra account env, e.g. pinning Claude Code's helper models so a
  // background request never asks a third-party endpoint for a Claude id.
  env?: Record<string, string>;
}

export interface ApiPreset {
  id: string;
  label: string;
  // Name the new account gets (suffixed when taken).
  accountName: string;
  docsUrl?: string;
  endpoints: Partial<Record<AgentFramework, PresetEndpoint>>;
}

export const API_PRESETS: ApiPreset[] = [
  {
    id: "anthropic",
    label: "Anthropic API",
    accountName: "anthropic-api",
    endpoints: {
      "claude-code": {
        baseUrl: "https://api.anthropic.com",
        models: [
          { id: "claude-opus-5-5[1m]" },
          { id: "claude-sonnet-5" },
          { id: "claude-haiku-4-5" },
        ],
        summaryModel: "claude-haiku-4-5",
      },
    },
  },
  {
    id: "openai",
    label: "OpenAI API",
    accountName: "openai-api",
    endpoints: {
      codex: {
        baseUrl: "https://api.openai.com/v1",
        models: [{ id: "gpt-5.6-sol" }, { id: "gpt-5.6-terra" }, { id: "gpt-5.6-luna" }],
        summaryModel: "gpt-5.6-luna",
      },
    },
  },
  {
    id: "deepseek",
    label: "DeepSeek",
    accountName: "deepseek",
    docsUrl: "https://api-docs.deepseek.com/quick_start/agent_integrations/claude_code/",
    endpoints: {
      "claude-code": {
        baseUrl: "https://api.deepseek.com/anthropic",
        models: [{ id: "deepseek-flash[1m]" }, { id: "deepseek-v4-pro[1m]" }],
        summaryModel: "deepseek-flash",
        env: {
          ANTHROPIC_MODEL: "deepseek-flash[1m]",
          ANTHROPIC_DEFAULT_OPUS_MODEL: "deepseek-flash[1m]",
          ANTHROPIC_DEFAULT_SONNET_MODEL: "deepseek-flash[1m]",
          ANTHROPIC_DEFAULT_HAIKU_MODEL: "deepseek-flash",
          CLAUDE_CODE_SUBAGENT_MODEL: "deepseek-flash",
        },
      },
    },
  },
  // A blank endpoint: any vendor that speaks Anthropic Messages (Claude Code)
  // or OpenAI Responses (Codex).
  {
    id: "custom",
    label: "",
    accountName: "api",
    endpoints: {
      "claude-code": { baseUrl: "", models: [], summaryModel: "" },
      codex: { baseUrl: "", models: [], summaryModel: "" },
    },
  },
];

export function presetsFor(framework: AgentFramework): ApiPreset[] {
  return API_PRESETS.filter((preset) => preset.endpoints[framework]);
}

// Context windows are entered the way vendors publish them ("128K", "1M").
// Returns undefined for blank (meaning: trust the agent) and null for input
// that is not a positive size.
export function parseContextWindow(value: string): number | undefined | null {
  const text = value.trim().toLowerCase().replace(/,/g, "");
  if (!text) return undefined;
  const match = /^(\d+(?:\.\d+)?)\s*([km]?)$/.exec(text);
  if (!match) return null;
  const scale = match[2] === "m" ? 1_000_000 : match[2] === "k" ? 1_000 : 1;
  const tokens = Math.round(Number(match[1]) * scale);
  return tokens > 0 ? tokens : null;
}

export function formatContextWindow(tokens: number | undefined): string {
  if (!tokens) return "";
  if (tokens % 1_000_000 === 0) return `${tokens / 1_000_000}M`;
  if (tokens % 1_000 === 0) return `${tokens / 1_000}K`;
  return String(tokens);
}
