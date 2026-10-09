import { jsonRequestInit, request } from "./apiClient";

export interface MarketplaceApp {
  id: string;
  name: string;
  description: string;
  url: string;
  icon_url: string;
  deploy_dir: string;
  created_by: string;
  created_by_name: string;
  created_at: string;
}

export interface MarketplaceAppInput {
  name: string;
  description: string;
  url: string;
  icon_url?: string;
  deploy_dir?: string;
}

const marketplaceBase = "/api/marketplace/apps";

export function listMarketplaceApps(): Promise<MarketplaceApp[]> {
  return request<MarketplaceApp[]>(marketplaceBase, undefined, {
    label: "list apps",
    errorBody: "message",
  });
}

export function createMarketplaceApp(input: MarketplaceAppInput): Promise<MarketplaceApp> {
  return request<MarketplaceApp>(marketplaceBase, jsonRequestInit("POST", input), {
    label: "create app",
    errorBody: "message",
  });
}

export function updateMarketplaceApp(
  id: string,
  input: MarketplaceAppInput,
): Promise<MarketplaceApp> {
  return request<MarketplaceApp>(
    `${marketplaceBase}/${encodeURIComponent(id)}`,
    jsonRequestInit("PUT", input),
    { label: "update app", errorBody: "message" },
  );
}

export async function deleteMarketplaceApp(id: string): Promise<void> {
  await request<void>(
    `${marketplaceBase}/${encodeURIComponent(id)}`,
    { method: "DELETE" },
    { label: "delete app", errorBody: "message", expect: "none" },
  );
}
