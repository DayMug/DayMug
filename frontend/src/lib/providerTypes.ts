// The four provider types a DayMug deployment can configure, and the family
// each belongs to. Mirrors config.SupportedCLITypes / IsCodexFamily
// in backend/internal/config/config.go.
//
// A `*-compatible` type is the same transport as its first-party sibling
// pointed at a third-party endpoint through the account's env block, so every
// question about *how the CLI behaves* (which config dir it reads, whether it
// accepts a caller-chosen session id) is a family question — only billing and
// account binding are per-type.

export const PROVIDER_TYPES = [
  "claude",
  "codex",
  "claude-compatible",
  "openai-compatible",
] as const;

export type ProviderType = (typeof PROVIDER_TYPES)[number];

export function isCodexFamily(type: string): boolean {
  return type === "codex" || type === "openai-compatible";
}

// An account is chosen in two independent steps: which agent framework runs
// the conversation, and how that framework authenticates. The four provider
// types are just the product of the two, so a new framework (OpenCode, Pi, …)
// slots in as one more row here instead of another flat type in a dropdown.
// The same third-party API key can sit behind either framework when the
// vendor exposes both an Anthropic- and an OpenAI-shaped endpoint.
export type AgentFramework = "claude-code" | "codex";
export type AccessMode = "login" | "api-key";

export const AGENT_FRAMEWORKS: {
  id: AgentFramework;
  label: string;
  types: Record<AccessMode, ProviderType>;
}[] = [
  {
    id: "claude-code",
    label: "Claude Code",
    types: { login: "claude", "api-key": "claude-compatible" },
  },
  { id: "codex", label: "Codex", types: { login: "codex", "api-key": "openai-compatible" } },
];

export function providerTypeFor(framework: AgentFramework, access: AccessMode): ProviderType {
  const entry = AGENT_FRAMEWORKS.find((f) => f.id === framework) ?? AGENT_FRAMEWORKS[0]!;
  return entry.types[access];
}

export function frameworkOf(type: string): AgentFramework {
  return isCodexFamily(type) ? "codex" : "claude-code";
}

export function accessModeOf(type: string): AccessMode {
  return type === "claude-compatible" || type === "openai-compatible" ? "api-key" : "login";
}
