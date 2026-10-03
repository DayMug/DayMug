import { request } from "./apiClient";

// ServerInfo carries the small slice of server state every authenticated
// user is allowed to see (running version, sandbox state).
export interface ServerInfo {
  version: string;
  sandbox_enabled: boolean;
  // Implementation name (currently "noop") or "" when sandboxing is off
  // entirely. The frontend treats any non-empty value with enabled=true
  // as a sandbox; surfacing the type leaves room for future per-type
  // behaviour without a schema bump.
  sandbox_type: string;
  manifest_url: string;
}

export async function fetchServerInfo(): Promise<ServerInfo> {
  return request<ServerInfo>(`/api/server-info`, undefined, { label: "server info" });
}
